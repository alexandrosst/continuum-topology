package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Health tells Kubernetes whether the agent is alive and whether it is doing its job. It holds no secrets and reports no
// cluster data: only a state word and how long ago something happened.
//
// Live is false only when the agent's own loop has not run for LiveStale, that is, the process is wedged and a restart
// is the right cure. Being unable to reach the server, waiting for an administrator's approval, or being revoked are NOT
// reasons to restart: a restart changes none of them and would only turn "waiting" into a crash loop.
//
// Ready is true while the agent is waiting for approval (that is normal, not a fault), while its stream to the server is
// up, and for ReadyGrace after the stream last dropped, so a short server outage or a certificate renewal does not flap.
// It is false before the agent has first reached the server, after ReadyGrace without a connection, and once the server
// has revoked the agent. Ready also decides whether the receiver Service (probe and flow reports) has this pod behind it.
//
// Every method is safe on a nil *Health, so callers need not check whether health reporting is switched on.
type Health struct {
	now func() time.Time

	// LiveStale and ReadyGrace are variables of the value, not constants, so tests can shrink them.
	LiveStale  time.Duration
	ReadyGrace time.Duration

	beat atomic.Int64 // unix nanoseconds of the main loop's last sign of life
	okAt atomic.Int64 // unix nanoseconds of the last time the server was reachable (0: never)
	mode atomic.Int32 // one of the mode constants
}

const (
	modeStarting int32 = iota
	modePending        // enrolled, waiting for an administrator to approve
	modeStreaming
	modeDisconnected
	modeRevoked
)

func NewHealth() *Health {
	h := &Health{now: time.Now, LiveStale: 5 * time.Minute, ReadyGrace: 10 * time.Minute}
	h.beat.Store(h.now().UnixNano())
	return h
}

// Beat records that the agent's main loop is running. Call it wherever the loop wakes up.
func (h *Health) Beat() {
	if h != nil {
		h.beat.Store(h.now().UnixNano())
	}
}

// Enrolling records that the server answered and is holding this enrollment for approval.
func (h *Health) Enrolling() { h.reach(modePending) }

// Connected records that the stream to the server is established.
func (h *Health) Connected() { h.reach(modeStreaming) }

// Disconnected records that the stream or the enrollment poll ended (the agent will retry).
func (h *Health) Disconnected() {
	if h == nil || h.mode.Load() == modeRevoked {
		return
	}
	h.beat.Store(h.now().UnixNano())
	if h.mode.Load() != modeStarting {
		h.okAt.Store(h.now().UnixNano())
		h.mode.Store(modeDisconnected)
	}
}

// Revoked records that the server revoked or rejected this agent. It holds for a while, then the CLI exits (code 3).
func (h *Health) Revoked() {
	if h != nil {
		h.beat.Store(h.now().UnixNano())
		h.mode.Store(modeRevoked)
	}
}

func (h *Health) reach(m int32) {
	if h == nil || h.mode.Load() == modeRevoked {
		return
	}
	now := h.now().UnixNano()
	h.beat.Store(now)
	h.okAt.Store(now)
	h.mode.Store(m)
}

// Live is false only if the main loop has shown no sign of life for LiveStale (a revoked agent idles by design and is always live).
func (h *Health) Live() bool {
	if h == nil || h.mode.Load() == modeRevoked {
		return true // a revoked agent idles on purpose (it may wait many minutes before it exits): that is not a wedge
	}
	return h.now().Sub(time.Unix(0, h.beat.Load())) < h.LiveStale
}

// Status reports whether the agent is doing its job, and says why in plain words.
func (h *Health) Status() (ready bool, why string) {
	if h == nil {
		return true, "health reporting is off"
	}
	switch h.mode.Load() {
	case modeStarting:
		return false, "starting: has not reached the server yet"
	case modePending:
		return true, "enrolled, waiting for an administrator to approve (normal; not a fault)"
	case modeStreaming:
		return true, "connected"
	case modeRevoked:
		return false, "revoked or rejected by the server: inactive until enrolled again with a new token"
	}
	ago := h.now().Sub(time.Unix(0, h.okAt.Load()))
	if ago < h.ReadyGrace {
		return true, fmt.Sprintf("disconnected %s ago; retrying", ago.Round(time.Second))
	}
	return false, fmt.Sprintf("no connection to the server for %s", ago.Round(time.Second))
}

func (h *Health) Ready() bool { r, _ := h.Status(); return r }

// Handler serves /healthz (liveness) and /readyz (readiness) with a one-line plain text body.
func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !h.Live() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "the agent's main loop has stopped responding")
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ok, why := h.Status()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		fmt.Fprintln(w, why)
	})
	return mux
}

// ServeHealth listens on addr (for example ":8082": the kubelet probes the pod's own address, so not loopback) and serves
// h until the returned function is called. It answers the two probe paths and nothing else.
func ServeHealth(addr string, h *Health, log *slog.Logger) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4 << 10}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health endpoint stopped", "err", err)
		}
	}()
	log.Info("serving /healthz and /readyz", "addr", ln.Addr().String())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

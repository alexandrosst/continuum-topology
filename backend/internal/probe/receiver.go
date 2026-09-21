package probe

import (
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)

const maxNodes = 5000

// Receiver is the agent's side: it verifies reports and keeps the latest observation per node in
// memory. Nothing is written to disk and nothing is trusted beyond the signature.
type Receiver struct {
	secret []byte
	// Window is how far a report's timestamp may be from this clock; zero means MaxClockSkew (5 minutes).
	Window  time.Duration
	replay  *ReplayCache
	log     *slog.Logger
	now     func() time.Time
	changes chan struct{}

	mu     sync.Mutex
	nodes  map[string]*continuumv1.HostProbe
	seen   map[string]time.Time // when each node last reported
	paused bool
}

func NewReceiver(secret []byte, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.Default()
	}
	return &Receiver{secret: secret, replay: NewReplayCache(0), log: log, now: time.Now, changes: make(chan struct{}, 1), nodes: map[string]*continuumv1.HostProbe{}, seen: map[string]time.Time{}}
}

// Changes receives a signal whenever a report differs from the previous one for its node.
func (r *Receiver) Changes() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.changes
}

// Get returns a copy of the latest observation for a node, or nil.
func (r *Receiver) Get(node string) *continuumv1.HostProbe {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return nil
	}
	if h := r.nodes[node]; h != nil {
		return proto.Clone(h).(*continuumv1.HostProbe)
	}
	return nil
}

// Prune forgets nodes that no longer exist, so the map cannot grow without bound.
func (r *Receiver) Prune(keep map[string]bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.nodes {
		if !keep[k] {
			delete(r.nodes, k)
			delete(r.seen, k)
		}
	}
}

// SetPaused stops (or resumes) keeping what nodes report. Pausing forgets what is held, so nothing of it is attached
// to the next picture the agent sends; reports that arrive while paused are answered as usual and thrown away.
func (r *Receiver) SetPaused(paused bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	changed := r.paused != paused
	r.paused = paused
	if paused {
		r.nodes, r.seen = map[string]*continuumv1.HostProbe{}, map[string]time.Time{}
	}
	r.mu.Unlock()
	if changed {
		select {
		case r.changes <- struct{}{}:
		default:
		}
	}
}

// Presence says how many nodes reported within the last `within` and when any node last did.
func (r *Receiver) Presence(within time.Duration) (nodes int, last time.Time) {
	if r == nil {
		return 0, time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, at := range r.seen {
		if now.Sub(at) <= within {
			nodes++
		}
		if at.After(last) {
			last = at
		}
	}
	return nodes, last
}

func (r *Receiver) put(node string, h *continuumv1.HostProbe) (accepted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return true
	}
	old, exists := r.nodes[node]
	if !exists && len(r.nodes) >= maxNodes {
		return false
	}
	r.nodes[node] = h
	r.seen[node] = r.now()
	if !exists || !proto.Equal(old, h) {
		select {
		case r.changes <- struct{}{}:
		default:
		}
	}
	return true
}

// Handler serves POST /v1/probe. It answers with the least information possible.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathReport, func(w http.ResponseWriter, req *http.Request) {
		node, body, status, err := ReadSigned(w, req, Signed{Secret: r.secret, MaxBody: MaxBody, Window: r.Window, Replay: r.replay, NodeOK: nodeName.MatchString, Now: r.now()})
		if err != nil {
			if status == http.StatusUnauthorized || status == http.StatusConflict {
				r.log.Warn("rejected a node probe report", "reason", err.Error(), "from", req.RemoteAddr)
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		var h continuumv1.HostProbe
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &h); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !r.put(node, Sanitize(&h)) {
			http.Error(w, "too many nodes", http.StatusInsufficientStorage)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

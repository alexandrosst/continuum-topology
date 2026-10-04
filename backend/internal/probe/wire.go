package probe

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/encoding/protojson"
)

// The probe and the agent share a random secret that the Helm chart generates and mounts into both.
// Every report is signed: HMAC-SHA256 over version, timestamp, node name and body. The timestamp
// limits replay to a window (5 minutes by default) and receivers remember the signatures they accepted
// inside it (see ReplayCache), so a captured report cannot be sent again.
const (
	PathReport = "/v1/probe"
	HeaderNode = "X-Continuum-Node"
	HeaderTime = "X-Continuum-Timestamp"
	HeaderSig  = "X-Continuum-Signature"
	// HeaderPaused marks a response to a report that the receiver still answered 2xx (the signature and
	// freshness were checked as usual, so a bad secret or clock is still reported the ordinary way) but
	// whose data went nowhere, because the server has asked this collector to pause. It rides along on
	// the existing success status rather than becoming a distinct status code so an older collector,
	// which has never heard of pausing, still sees a plain success and simply never finds out it was
	// wasted; a collector that does know looks for this header and treats it as the backoff signal
	// PauseBackoff below is for, never as a transport error.
	HeaderPaused = "X-Continuum-Paused"
	// MaxBody bounds a node probe report (a hardware description is a few KiB).
	MaxBody = 128 << 10
	// MaxClockSkew is the default timestamp window.
	MaxClockSkew = 5 * time.Minute
)

func Sign(secret []byte, ts, node string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("continuum-probe-v1\n" + ts + "\n" + node + "\n"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a report's signature and freshness with the default window (MaxClockSkew).
func Verify(secret []byte, ts, node, sig string, body []byte, now time.Time) error {
	return VerifyWindow(secret, ts, node, sig, body, now, MaxClockSkew)
}

// Push sends one observation to the agent. paused reports whether the agent's response said this
// report was ignored because the agent is currently paused (see HeaderPaused) - true is possible even
// when err is nil, since a paused receiver still answers 2xx.
func Push(ctx context.Context, hc *http.Client, url string, secret []byte, node string, h *continuumv1.HostProbe, now time.Time) (paused bool, err error) {
	body, err := protojson.Marshal(h)
	if err != nil {
		return false, err
	}
	return PostSigned(ctx, hc, url+PathReport, secret, node, body, now)
}

// PostSigned posts a body to a receiver that verifies with the same scheme: the node flow collector
// uses it too, with its own secret and its own path. paused reports whether the response carried
// HeaderPaused - see Push's own doc comment for what that means.
func PostSigned(ctx context.Context, hc *http.Client, fullURL string, secret []byte, node string, body []byte, now time.Time) (paused bool, err error) {
	ts := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderNode, node)
	req.Header.Set(HeaderTime, ts)
	req.Header.Set(HeaderSig, Sign(secret, ts, node, body))
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	paused = resp.Header.Get(HeaderPaused) != ""
	if resp.StatusCode/100 != 2 {
		return paused, fmt.Errorf("agent answered %s", resp.Status)
	}
	return paused, nil
}

// PauseBackoffTicks is the fixed multiple a PauseBackoff skips a loop's own real cycle by while the
// server reports it paused: of every PauseBackoffTicks ticks, exactly one does the real work (read,
// sign, POST) and the rest are free skips. A single multiple, not an exponential ramp, is enough here
// because the signal being backed off is a plain boolean (paused or not) with nothing to ramp against:
// there is no backlog growing and no rate to approach, so a bigger miss just means the same "still
// paused" answer again, not a worse one, and reacting to it by climbing some curve would only slow
// noticing the server unpause for no benefit.
const PauseBackoffTicks = 4

// PauseBackoff decides, tick by tick, whether a collection loop's next tick should do its real cycle
// (the eBPF/procfs read, the sign, the full POST) or be skipped because the last real cycle found the
// server still paused. It bounds how stale a resume can be to PauseBackoffTicks ticks of the loop's own
// interval: whatever tick the server flips paused back off on, the loop is at worst PauseBackoffTicks-1
// ticks into a skip run already, so its next real attempt - which is what notices the unpause - is at
// most PauseBackoffTicks ticks away. Not safe for concurrent use; each loop keeps its own.
type PauseBackoff struct {
	skip int
}

// Due reports whether this tick should run the real cycle. It consumes one skip when it returns false.
func (b *PauseBackoff) Due() bool {
	if b.skip > 0 {
		b.skip--
		return false
	}
	return true
}

// Observe records what a just-completed real cycle's response said. Call it only when that cycle
// actually reached the server (no transport error) - a transport failure is a different problem with
// its own retry handling and must not be read as either "paused" or "not paused".
func (b *PauseBackoff) Observe(paused bool) {
	if paused {
		b.skip = PauseBackoffTicks - 1
	} else {
		b.skip = 0
	}
}

// Run reads and reports until ctx ends. It retries quietly with backoff while the agent is not
// reachable (the agent may start after the probe), and separately backs off its own real cycles (see
// PauseBackoff) while the agent reports it is paused, still waking up every `every` but only actually
// reading and sending on the ticks PauseBackoff says are due - so a paused agent is noticed again, at
// the latest, PauseBackoffTicks * every after it stops being paused.
func Run(ctx context.Context, paths Paths, url string, secret []byte, node string, every time.Duration, logf func(msg string, kv ...any)) {
	hc := &http.Client{Timeout: 10 * time.Second}
	wait := 5 * time.Second
	var backoff PauseBackoff
	for {
		next := every
		if backoff.Due() {
			paused, err := Push(ctx, hc, url, secret, node, Read(paths), time.Now())
			if err != nil {
				logf("could not report to the agent, will retry", "err", err, "in", wait)
				next = wait
				if wait < 2*time.Minute {
					wait *= 2
				}
			} else {
				wait = 5 * time.Second
				backoff.Observe(paused)
			}
		}
		select {
		case <-time.After(next):
		case <-ctx.Done():
			return
		}
	}
}

package agent

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d)) }

func newTestHealth() (*Health, *clock) {
	c := &clock{}
	c.t.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	h := NewHealth()
	h.now = c.now
	h.beat.Store(c.t.Load())
	return h, c
}

func TestHealthReadiness(t *testing.T) {
	h, c := newTestHealth()
	if h.Ready() {
		t.Error("ready before the server was ever reached")
	}
	if !h.Live() {
		t.Error("a fresh process is alive")
	}
	// Waiting for approval is normal: ready, and it stays ready however long the wait.
	h.Enrolling()
	c.advance(3 * time.Hour)
	h.Beat()
	if !h.Ready() {
		t.Error("a pending enrollment must be ready")
	}
	h.Connected()
	if ok, why := h.Status(); !ok || why != "connected" {
		t.Errorf("connected: %v %q", ok, why)
	}
	// The stream drops: still ready for the grace period, not after.
	h.Disconnected()
	c.advance(9 * time.Minute)
	if !h.Ready() {
		t.Error("a short outage must not flip readiness")
	}
	c.advance(2 * time.Minute)
	if h.Ready() {
		t.Error("ready after 11 minutes without a connection")
	}
	h.Connected()
	if !h.Ready() {
		t.Error("not ready again after reconnecting")
	}
	// Revoked is final, and never makes the agent unlive (a restart cures nothing).
	h.Revoked()
	h.Connected()
	if h.Ready() {
		t.Error("a revoked agent must not become ready again")
	}
	c.advance(30 * time.Minute) // the enrollment code's hold can last many minutes without a beat
	if !h.Live() {
		t.Error("a revoked agent is alive")
	}
}

func TestHealthLivenessOnlyFailsWhenTheLoopStops(t *testing.T) {
	h, c := newTestHealth()
	h.Connected()
	c.advance(4 * time.Minute)
	if !h.Live() {
		t.Error("4 minutes of quiet is not a wedge")
	}
	c.advance(2 * time.Minute)
	if h.Live() {
		t.Error("6 minutes without the loop turning is a wedge")
	}
	h.Beat()
	if !h.Live() {
		t.Error("a beat revives liveness")
	}
	// Being offline for hours never fails liveness while the loop turns.
	h.Disconnected()
	for i := 0; i < 20; i++ {
		c.advance(time.Minute)
		h.Beat()
	}
	if !h.Live() || h.Ready() {
		t.Errorf("offline: live=%v ready=%v, want live and not ready", h.Live(), h.Ready())
	}
}

func TestHealthNilIsSafe(t *testing.T) {
	var h *Health
	h.Beat()
	h.Enrolling()
	h.Connected()
	h.Disconnected()
	h.Revoked()
	if !h.Live() || !h.Ready() {
		t.Error("a nil Health reports healthy")
	}
}

func TestHealthEndpoints(t *testing.T) {
	h, _ := newTestHealth()
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Errorf("/healthz = %d", code)
	}
	if code, _ := get("/readyz"); code != 503 {
		t.Errorf("/readyz before contact = %d, want 503", code)
	}
	h.Enrolling()
	if code, body := get("/readyz"); code != 200 || body == "" {
		t.Errorf("/readyz while pending = %d %q", code, body)
	}
	if code, _ := get("/nope"); code != 404 {
		t.Errorf("unknown path = %d", code)
	}
}

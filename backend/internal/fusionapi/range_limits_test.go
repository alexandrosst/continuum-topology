package fusionapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Tempo's search and Loki take less than the 31 days the API allows. A longer range is refused here, with the store's
// limit in the message and without a call; a list that is only a sample of the newest items reads the newest part instead.
func TestRangesLongerThanAStoreTakesAreRefused(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"traces": []any{}}) }
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{}}})
	}
	c, ctx := f.client(), context.Background()
	now := c.Now()
	long := func(d time.Duration) TimeRange { return TimeRange{From: now.Add(-d), To: now} }

	_, _, err := c.SearchTraces(ctx, AllSignals(), TraceFilter{}, long(8*24*time.Hour), 10)
	if e, ok := err.(*Error); !ok || e.Status != 400 || !strings.Contains(e.Msg, "Tempo reads at most 7 days") {
		t.Fatalf("8 days of traces: %v", err)
	}
	_, _, err = c.Logs(ctx, AllSignals(), LogFilter{}, long(31*24*time.Hour), 10)
	if e, ok := err.(*Error); !ok || e.Status != 400 || !strings.Contains(e.Msg, "Loki reads at most 30 days") {
		t.Fatalf("31 days of logs: %v", err)
	}
	if len(f.paths()) != 0 {
		t.Fatalf("a refused range reached the stores: %v", f.paths())
	}
	if _, _, err := c.SearchTraces(ctx, AllSignals(), TraceFilter{}, long(7*24*time.Hour), 10); err != nil {
		t.Fatalf("7 days of traces: %v", err)
	}
	if _, _, err := c.Logs(ctx, AllSignals(), LogFilter{}, long(30*24*time.Hour), 10); err != nil {
		t.Fatalf("30 days of logs: %v", err)
	}

	// A scoped services list reads a sample of the newest traces, so a long range reads its newest week.
	scoped := Scope{Signals: Signals, Namespaces: []string{"shop"}}
	if _, _, err := c.traceServices(ctx, scoped, long(31*24*time.Hour)); err != nil {
		t.Fatalf("scoped service list over 31 days: %v", err)
	}
}

// Days and weeks are the natural units of a range that goes to 31 days.
func TestDurationsTakeDaysAndWeeks(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "1w3d12h": (10*24 + 12) * time.Hour, "90m": 90 * time.Minute, "1d6h": 30 * time.Hour,
	} {
		if got, err := DurationParam(in); err != nil || got != want {
			t.Errorf("DurationParam(%q) = %v, %v; want %v", in, got, err, want)
		}
		if tr, err := ParseRange("now-"+in, "", now); (want <= MaxWindow) != (err == nil) || (err == nil && tr.From != now.Add(-want)) {
			t.Errorf("from=now-%s: %v, %v", in, tr, err)
		}
	}
	for _, bad := range []string{"d", "w", "1.5d", "-1d", "1dd", "d1", "7days", "99999999d", "5000w"} {
		if got, err := DurationParam(bad); err == nil {
			t.Errorf("DurationParam(%q) = %v, want an error", bad, got)
		}
	}
}

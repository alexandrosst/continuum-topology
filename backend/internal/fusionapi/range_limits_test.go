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

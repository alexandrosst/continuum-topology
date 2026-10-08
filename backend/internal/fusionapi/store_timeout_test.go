package fusionapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// hang is a store that accepts the call and never answers it.
func hang(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// One store that hangs costs the read that part only: the trace and the metrics already read are returned, and the part
// that timed out is named, in words, as a store that was too slow.
func TestFuseTraceKeepsWhatWasReadWhenOneStoreHangs(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = hang
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{
			map[string]any{"metric": map[string]string{"__name__": "up", "k8s_namespace_name": "shop"}, "values": [][]any{{1791200000.0, "1"}}}}}})
	}
	c := f.client()
	c.StoreTimeout = 100 * time.Millisecond
	start := time.Now()
	got, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, Metrics: true})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if got.Sources[SignalLogs] != SourceError || got.Sources[SignalMetrics] != SourceOK || got.SpanCount != 3 {
		t.Fatalf("sources %v", got.Sources)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "Loki took too long") {
		t.Fatalf("warnings %v", got.Warnings)
	}
	withMetrics := 0
	for _, r := range got.Resources {
		if len(r.Metrics) > 0 {
			withMetrics++
		}
	}
	if withMetrics == 0 {
		t.Fatal("the metrics that were read were thrown away")
	}
}

// The same holds when it is the request's whole budget that runs out: what was read comes back, with the parts that did not
// finish named, instead of a bare 504.
func TestFuseTraceReturnsWhatItHasWhenTheRequestRunsOutOfTime(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = hang
	c := f.client()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	got, err := c.FuseTrace(ctx, AllSignals(), traceHex, FuseOptions{Logs: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SignalLogs] != SourceError || got.SpanCount != 3 || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "took too long") {
		t.Fatalf("sources %v warnings %v", got.Sources, got.Warnings)
	}
}

// A caller that went away is not answered at all.
func TestFuseTraceStopsWhenTheCallerGoesAway(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = hang
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := f.client().FuseTrace(ctx, AllSignals(), traceHex, FuseOptions{Logs: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// A single-signal read of a store that hangs is a 504 that names the store.
func TestASlowStoreIsAGatewayTimeoutNamingIt(t *testing.T) {
	f := newFake(t)
	f.loki = hang
	c := f.client()
	c.StoreTimeout = 50 * time.Millisecond
	_, _, err := c.Logs(context.Background(), AllSignals(), LogFilter{}, rangeAll, 10)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusGatewayTimeout || !strings.Contains(e.Msg, "Loki") {
		t.Fatalf("%v", err)
	}
}

package fusionapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// A list that is exactly as long as the limit is complete; one the store had more of is cut, and says so. The store is
// asked for one more than the limit, which is how the two tell apart.
func TestListsSayWhenTheyWereCut(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/label/__name__/values":
			writeJSON(w, map[string]any{"status": "success", "data": []string{"a", "b", "c"}})
		case "/api/v1/series":
			writeJSON(w, map[string]any{"status": "success", "data": []map[string]string{{"__name__": "a"}, {"__name__": "b"}, {"__name__": "c"}}})
		}
	}
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		var ts []map[string]any
		for i := 0; i < 3; i++ {
			ts = append(ts, map[string]any{"traceID": fmt.Sprintf("%032x", i+1), "startTimeUnixNano": "1791200000000000000"})
		}
		writeJSON(w, map[string]any{"traces": ts})
	}
	c, ctx, s := f.client(), context.Background(), AllSignals()
	for _, tc := range []struct {
		limit int
		cut   bool
		n     int
	}{{2, true, 2}, {3, false, 3}} {
		names, cut, err := c.MetricNames(ctx, s, MetricFilter{}, rangeAll, tc.limit)
		if err != nil || cut != tc.cut || len(names) != tc.n {
			t.Errorf("names limit %d: %v %v %v", tc.limit, names, cut, err)
		}
		series, cut, err := c.Series(ctx, s, MetricFilter{}, rangeAll, tc.limit)
		if err != nil || cut != tc.cut || len(series) != tc.n {
			t.Errorf("series limit %d: %v %v %v", tc.limit, series, cut, err)
		}
		traces, cut, err := c.SearchTraces(ctx, s, TraceFilter{}, rangeAll, tc.limit)
		if err != nil || cut != tc.cut || len(traces) != tc.n {
			t.Errorf("traces limit %d: %v %v %v", tc.limit, traces, cut, err)
		}
	}
}

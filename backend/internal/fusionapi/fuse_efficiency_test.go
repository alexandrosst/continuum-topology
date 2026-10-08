package fusionapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The log join asks Loki for the trace's own services (an index label), not for every stream with the trace id filtered
// out of the lines afterwards.
func TestTheLogJoinSelectsTheTracesServices(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, lokiResult()) }
	if _, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true}); err != nil {
		t.Fatal(err)
	}
	q := f.last("/loki/api/v1/query_range").Get("query")
	if !strings.HasPrefix(q, `{service_name=~"audit|billing|cart"}`) || !strings.Contains(q, `| trace_id="`+traceHex+`"`) {
		t.Fatalf("query = %s", q)
	}
	// A service name is quoted like any value that goes into a query, and checked like one.
	sel, err := LogFilter{Services: []string{`a"} or {x=~".+`}}.selector(AllSignals())
	if err != nil || sel != `{service_name=~"a\"\\} or \\{x=~\"\\.\\+"}` {
		t.Fatalf("%s %v", sel, err)
	}
	if _, err := (LogFilter{Services: []string{"a\nb"}}).selector(AllSignals()); err == nil {
		t.Fatal("a control character went into a selector")
	}
	// A caller's own service still wins, and a token's application focus still narrows.
	sel, _ = LogFilter{Service: "cart", Services: []string{"a", "b"}}.selector(Scope{Signals: Signals, FocusServices: []string{"cart", "x"}})
	if sel != `{service_name="cart",service_name=~"cart|x"}` {
		t.Fatalf("selector = %s", sel)
	}
}

package fusionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
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

// replicas is a trace of one service run by n pods on one node.
func replicas(n int) map[string]any {
	var rss []any
	base := time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC).UnixNano()
	for i := 0; i < n; i++ {
		pod := "cart-" + strconv.Itoa(i)
		rss = append(rss, map[string]any{
			"resource": map[string]any{"attributes": []any{kv("service.name", "cart"), kv("k8s.namespace.name", "shop"), kv("k8s.pod.name", pod),
				kv("k8s.node.name", "node-1"), kv("continuum.cluster.id", "cl-1")}},
			"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"traceId": traceHex, "spanId": "00000000000000" + strconv.Itoa(10+i),
				"name": "GET " + pod, "startTimeUnixNano": json.Number(itoa(base)), "endTimeUnixNano": json.Number(itoa(base + 1e6))}}}},
		})
	}
	return map[string]any{"trace": map[string]any{"resourceSpans": rss}}
}

// The replicas of a service share one app view and the pods of a node one node view: each is asked of Prometheus once, and
// every resource still carries the series.
func TestReplicasShareTheirAppAndNodeMetricReads(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, replicas(3)) }
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{
			map[string]any{"metric": map[string]string{"__name__": "up", "k8s_namespace_name": "shop"}, "values": [][]any{{1791200000.0, "1"}}}}}})
	}
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Metrics: true, MetricViews: MetricViews{App: true, Pod: true, Node: true}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	var queries []string
	for _, r := range f.reqs {
		if r.URL.Path == "/api/v1/query_range" {
			queries = append(queries, r.URL.Query().Get("query"))
		}
	}
	f.mu.Unlock()
	app, node, pod := 0, 0, 0
	for _, q := range queries {
		switch {
		case strings.Contains(q, "k8s_node_name"):
			node++
		case strings.Contains(q, "k8s_pod_name"):
			pod++
		default:
			app++
		}
	}
	if app != 1 || node != 1 || pod != 3 {
		t.Fatalf("app %d, node %d, pod %d reads: %v", app, node, pod, queries)
	}
	for _, r := range got.Resources {
		if len(r.Metrics) == 0 || r.Metrics[0].Name != "up" {
			t.Fatalf("%s carries %+v", r.Pod, r.Metrics)
		}
	}
}

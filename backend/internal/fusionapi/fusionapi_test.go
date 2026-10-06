package fusionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is three stores on one httptest server each, recording what they were asked.
type fake struct {
	t          *testing.T
	mu         sync.Mutex
	reqs       []*http.Request
	prom, loki http.HandlerFunc
	tempo      http.HandlerFunc
	pSrv, lSrv *httptest.Server
	tSrv       *httptest.Server
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t}
	wrap := func(h *http.HandlerFunc) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.reqs = append(f.reqs, r)
			f.mu.Unlock()
			if *h == nil {
				http.NotFound(w, r)
				return
			}
			(*h)(w, r)
		}))
	}
	f.pSrv, f.lSrv, f.tSrv = wrap(&f.prom), wrap(&f.loki), wrap(&f.tempo)
	t.Cleanup(func() { f.pSrv.Close(); f.lSrv.Close(); f.tSrv.Close() })
	return f
}

func (f *fake) client() *Client {
	return &Client{Prometheus: f.pSrv.URL, Loki: f.lSrv.URL, Tempo: f.tSrv.URL, Now: func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }}
}

// last returns the query parameters of the most recent request to a path.
func (f *fake) last(path string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].URL.Path == path {
			return f.reqs[i].URL.Query()
		}
	}
	f.t.Fatalf("no request to %s; saw %v", path, f.paths())
	return nil
}

func (f *fake) paths() []string {
	var p []string
	for _, r := range f.reqs {
		p = append(p, r.URL.Path)
	}
	return p
}

func (f *fake) lastHeader(path, h string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].URL.Path == path {
			return f.reqs[i].Header.Get(h)
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

var (
	traceHex = "0af7651916cd43dd8448eb211c80319c"
	rangeAll = TimeRange{From: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	limited  = Scope{Signals: Signals, Namespaces: []string{"shop", "pay"}, Clusters: []string{"cl-1"}}
)

func strVal(s string) map[string]any { return map[string]any{"stringValue": s} }

func kv(k, v string) map[string]any { return map[string]any{"key": k, "value": strVal(v)} }

// tempoTrace is a trace across two namespaces: cart (shop) calls billing (pay), plus a span in a namespace nobody
// scoped sees. Ids are hex in one batch and base64 in another, since Tempo has used both.
func tempoTrace() map[string]any {
	span := func(id, parent, name string, start, end int64, code string) map[string]any {
		return map[string]any{"traceId": traceHex, "spanId": id, "parentSpanId": parent, "name": name, "kind": "SPAN_KIND_SERVER",
			"startTimeUnixNano": json.Number(itoa(start)), "endTimeUnixNano": json.Number(itoa(end)),
			"attributes": []any{kv("http.method", "GET"), map[string]any{"key": "http.status_code", "value": map[string]any{"intValue": "200"}}},
			"status":     map[string]any{"code": code, "message": ""}}
	}
	base := time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC).UnixNano()
	return map[string]any{"trace": map[string]any{"resourceSpans": []any{
		map[string]any{
			"resource":   map[string]any{"attributes": []any{kv("service.name", "cart"), kv("k8s.namespace.name", "shop"), kv("k8s.pod.name", "cart-1"), kv("continuum.cluster.id", "cl-1")}},
			"scopeSpans": []any{map[string]any{"spans": []any{span("00f067aa0ba902b7", "", "GET /cart", base, base+90e6, "STATUS_CODE_UNSET")}}},
		},
		map[string]any{
			"resource": map[string]any{"attributes": []any{kv("service.name", "billing"), kv("k8s.namespace.name", "pay"), kv("k8s.pod.name", "billing-7"), kv("continuum.cluster.id", "cl-1")}},
			// base64 of 00f067aa0ba902b7 and of b7...: the id in the parent is the hex above, written as base64.
			"scopeSpans": []any{map[string]any{"spans": []any{span("AAAAAAAAAAE=", "APBnqgupArc=", "charge", base+10e6, base+60e6, "STATUS_CODE_ERROR")}}},
		},
		map[string]any{
			"resource":   map[string]any{"attributes": []any{kv("service.name", "audit"), kv("k8s.namespace.name", "secret"), kv("continuum.cluster.id", "cl-1")}},
			"scopeSpans": []any{map[string]any{"spans": []any{span("00000000000000aa", "AAAAAAAAAAE=", "write", base+20e6, base+30e6, "STATUS_CODE_UNSET")}}},
		},
	}}}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestTraceNormalisesIdsAndHidesWhatTheScopeCannotSee(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	c := f.client()

	all, err := c.Trace(context.Background(), AllSignals(), "0AF7651916CD43DD8448EB211C80319C")
	if err != nil {
		t.Fatal(err)
	}
	if all.TraceID != traceHex || all.SpanCount != 3 || len(all.Resources) != 3 || all.ErrorCount != 1 {
		t.Fatalf("whole trace = %+v", all)
	}
	if got := f.lastPath(); got != "/api/v2/traces/"+traceHex {
		t.Fatalf("asked Tempo for %s", got)
	}
	byName := map[string]*Span{}
	for _, sp := range all.Spans {
		byName[sp.Name] = sp
	}
	if byName["charge"].SpanID != "0000000000000001" || byName["charge"].ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("base64 ids not turned into hex: %+v", byName["charge"])
	}
	if byName["charge"].Depth != 1 || byName["write"].Depth != 2 || byName["GET /cart"].Depth != 0 || len(all.Roots) != 1 {
		t.Fatalf("depths/roots = %d %d %d %v", byName["GET /cart"].Depth, byName["charge"].Depth, byName["write"].Depth, all.Roots)
	}
	if byName["charge"].Status != "error" || byName["charge"].Kind != "server" || byName["charge"].Attributes["http.status_code"] != int64(200) {
		t.Fatalf("charge = %+v", byName["charge"])
	}
	if !reflect.DeepEqual(all.Services, []string{"audit", "billing", "cart"}) || all.DurationMs != 90 {
		t.Fatalf("services/duration = %v %v", all.Services, all.DurationMs)
	}

	// A scope for shop and pay never sees the "secret" namespace's span, and the orphaned child is not given a depth
	// under something hidden.
	lim, err := c.Trace(context.Background(), limited, traceHex)
	if err != nil {
		t.Fatal(err)
	}
	if lim.SpanCount != 2 || len(lim.Resources) != 2 || reflect.DeepEqual(lim.Services, all.Services) {
		t.Fatalf("scoped trace = %+v", lim)
	}
	for _, sp := range lim.Spans {
		if sp.Namespace == "secret" {
			t.Fatalf("saw a span of a namespace outside the scope: %+v", sp)
		}
	}

	// Nothing visible reads as not found, the same as a trace that does not exist.
	_, err = c.Trace(context.Background(), Scope{Signals: Signals, Namespaces: []string{"other"}}, traceHex)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Fatalf("a trace entirely outside the scope: %v", err)
	}
	// A scope without traces is refused before Tempo is asked.
	before := len(f.reqs)
	if _, err := c.Trace(context.Background(), Scope{Signals: []string{SignalLogs}}, traceHex); !errors.As(err, &e) || e.Status != http.StatusForbidden || len(f.reqs) != before {
		t.Fatalf("scope without traces: %v (%d requests)", err, len(f.reqs)-before)
	}
	if _, err := c.Trace(context.Background(), AllSignals(), "not-hex"); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Fatalf("bad id: %v", err)
	}
}

func (f *fake) lastPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return ""
	}
	return f.reqs[len(f.reqs)-1].URL.Path
}

func TestMetricQueriesCarryTheScopeAndEscapeValues(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/series":
			writeJSON(w, map[string]any{"status": "success", "data": []map[string]string{
				{"__name__": "http_requests_total", "k8s_namespace_name": "shop", "continuum_cluster_id": "cl-1"},
				{"__name__": "leak", "k8s_namespace_name": "secret", "continuum_cluster_id": "cl-1"}, // the store ignored the matcher
				{"__name__": "nolabel"},
			}})
		default:
			http.NotFound(w, r)
		}
	}
	c := f.client()
	series, err := c.Series(context.Background(), limited, MetricFilter{Service: `cart"} or {job=~".+`, Namespace: "shop"}, rangeAll, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0]["__name__"] != "http_requests_total" {
		t.Fatalf("a series outside the scope was returned: %v", series)
	}
	sel := f.last("/api/v1/series").Get("match[]")
	for _, want := range []string{`k8s_namespace_name=~"shop|pay"`, `continuum_cluster_id=~"cl-1"`, `k8s_namespace_name="shop"`, `service_name="cart\"} or {job=~\".+"`} {
		if !strings.Contains(sel, want) {
			t.Fatalf("selector %s lacks %s", sel, want)
		}
	}
	// A scope that does not include metrics is refused without asking Prometheus.
	var e *Error
	if _, err := c.Series(context.Background(), Scope{Signals: []string{SignalLogs}}, MetricFilter{}, rangeAll, 10); !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("scope without metrics: %v", err)
	}
	// A bad regular expression is refused here.
	if _, err := c.Series(context.Background(), AllSignals(), MetricFilter{NameRegex: "(unclosed"}, rangeAll, 10); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Fatalf("bad regex: %v", err)
	}
}

func TestMetricRangeNormalisesSamples(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{
			map[string]any{"metric": map[string]string{"__name__": "cpu", "k8s_namespace_name": "shop", "k8s_pod_name": "cart-1"},
				"values": [][]any{{1791200000.0, "1"}, {1791200015.0, "NaN"}, {1791200030.0, "3"}}},
			map[string]any{"metric": map[string]string{"__name__": "cpu", "k8s_namespace_name": "secret"}, "values": [][]any{{1791200000.0, "9"}}},
		}}})
	}
	series, truncated, err := f.client().MetricRange(context.Background(), Scope{Signals: Signals, Namespaces: []string{"shop"}}, MetricFilter{Name: "cpu"}, rangeAll, 30*time.Second, 10)
	if err != nil || truncated {
		t.Fatal(err, truncated)
	}
	if len(series) != 1 {
		t.Fatalf("series = %+v", series)
	}
	m := series[0]
	if m.Name != "cpu" || len(m.Points) != 2 || m.Min != 1 || m.Max != 3 || m.Avg != 2 || m.Last != 3 || m.Labels["__name__"] != "" {
		t.Fatalf("series = %+v", m)
	}
	b, _ := json.Marshal(m.Points)
	if string(b) != "[[1791200000,1],[1791200030,3]]" {
		t.Fatalf("points marshal as %s", b)
	}
	q := f.last("/api/v1/query_range")
	if q.Get("step") != "30" || !strings.Contains(q.Get("query"), `__name__="cpu"`) {
		t.Fatalf("query_range = %v", q)
	}
}

func TestRawQueriesNeedAnUnrestrictedScope(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
	}
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{}}})
	}
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"traces": []any{}}) }
	c := f.client()
	ctx := context.Background()
	var e *Error
	if _, err := c.RawMetricQuery(ctx, limited, "query", url.Values{"query": {"up"}}); !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("raw PromQL with a limited scope: %v", err)
	}
	if _, _, err := c.RawLogQuery(ctx, limited, `{a="b"}`, false, rangeAll, 10); !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("raw LogQL with a limited scope: %v", err)
	}
	if _, err := c.RawTraceSearch(ctx, limited, `{ true }`, rangeAll, 10); !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("raw TraceQL with a limited scope: %v", err)
	}
	if len(f.reqs) != 0 {
		t.Fatalf("a refused raw query still reached a store: %v", f.paths())
	}
	// Signal-limited but not namespace-limited is fine.
	if _, err := c.RawMetricQuery(ctx, Scope{Signals: []string{SignalMetrics}}, "query", url.Values{"query": {"up"}, "evil": {"x"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last("/api/v1/query"); got.Get("evil") != "" || got.Get("query") != "up" {
		t.Fatalf("a parameter outside the allow-list was forwarded: %v", got)
	}
	if _, err := c.RawMetricQuery(ctx, AllSignals(), "admin/tsdb/delete_series", url.Values{"query": {"up"}}); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Fatalf("an endpoint outside the allow-list: %v", err)
	}
}

func lokiResult(entries ...[]any) map[string]any {
	// one stream per entry's labels, with the categorize-labels shape
	var result []any
	for _, e := range entries {
		result = append(result, map[string]any{"stream": e[0], "values": []any{e[1]}})
	}
	return map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": result}}
}

func TestLogsBuildAScopedQueryAndDecodeStructuredMetadata(t *testing.T) {
	f := newFake(t)
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, lokiResult(
			[]any{map[string]string{"service_name": "cart", "k8s_namespace_name": "shop", "k8s_pod_name": "cart-1"},
				[]any{"1791200000000000000", "payment failed", map[string]any{"structuredMetadata": map[string]string{"trace_id": traceHex, "span_id": "0000000000000001", "severity_text": "ERROR", "continuum_cluster_id": "cl-1"}}}},
			[]any{map[string]string{"service_name": "audit", "k8s_namespace_name": "secret"},
				[]any{"1791200001000000000", "hidden", map[string]any{"structuredMetadata": map[string]string{"continuum_cluster_id": "cl-1"}}}},
			[]any{map[string]string{"service_name": "cart", "k8s_namespace_name": "shop"},
				[]any{"1791200002000000000", "other cluster", map[string]any{"structuredMetadata": map[string]string{"continuum_cluster_id": "cl-9"}}}},
		))
	}
	c := f.client()
	lines, trunc, err := c.Logs(context.Background(), limited, LogFilter{TraceID: "AF7651916CD43DD8448EB211C80319C", Severity: "error", Contains: `fail"ed`}, rangeAll, 100)
	if err != nil || trunc {
		t.Fatal(err, trunc)
	}
	if len(lines) != 1 || lines[0].Line != "payment failed" || lines[0].Severity != "ERROR" || lines[0].SpanID != "0000000000000001" || lines[0].Namespace != "shop" || lines[0].Cluster != "cl-1" {
		t.Fatalf("lines = %+v", lines)
	}
	q := f.last("/loki/api/v1/query_range")
	query := q.Get("query")
	for _, want := range []string{`{service_name=~".+",k8s_namespace_name=~"shop|pay"}`, `|= "fail\"ed"`, `| trace_id="0` + "af7651916cd43dd8448eb211c80319c" + `"`, `severity_text=~"(?i)error"`, `continuum_cluster_id=~"cl-1"`} {
		if !strings.Contains(query, want) {
			t.Fatalf("LogQL %s lacks %s", query, want)
		}
	}
	if q.Get("direction") != "forward" || q.Get("limit") != "100" || len(q.Get("start")) != 19 {
		t.Fatalf("params = %v", q)
	}
	if f.lastHeader("/loki/api/v1/query_range", "X-Loki-Response-Encoding-Flags") != "categorize-labels" {
		t.Fatal("the categorize-labels flag was not sent")
	}
	var e *Error
	if _, _, err := c.Logs(context.Background(), AllSignals(), LogFilter{Severity: "err or {"}, rangeAll, 10); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Fatalf("a severity that is not a word: %v", err)
	}
}

func TestLogsReadStructuredMetadataFromLabelsOnAnOlderLoki(t *testing.T) {
	f := newFake(t)
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, lokiResult([]any{map[string]string{"service_name": "cart", "trace_id": traceHex, "span_id": "0000000000000001", "detected_level": "warn"}, []any{"1791200000000000000", "slow"}}))
	}
	lines, _, err := f.client().Logs(context.Background(), AllSignals(), LogFilter{}, rangeAll, 10)
	if err != nil || len(lines) != 1 || lines[0].TraceID != traceHex || lines[0].Severity != "warn" {
		t.Fatalf("%+v %v", lines, err)
	}
}

func TestTraceSearchBuildsScopedTraceQLAndHidesTheRoot(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"traces": []any{map[string]any{
			"traceID": traceHex, "rootServiceName": "gateway", "rootTraceName": "GET /", "startTimeUnixNano": "1791200000000000000", "durationMs": 120,
			"spanSet": map[string]any{"matched": 2, "spans": []any{map[string]any{"attributes": []any{kv("service.name", "cart")}}, map[string]any{"attributes": []any{kv("service.name", "cart")}}}},
		}}})
	}
	c := f.client()
	ctx := context.Background()
	got, err := c.SearchTraces(ctx, limited, TraceFilter{Service: "cart", Status: "error", MinDuration: 250 * time.Millisecond, Name: "GET /cart"}, rangeAll, 20)
	if err != nil {
		t.Fatal(err)
	}
	q := f.last("/api/search").Get("q")
	for _, want := range []string{`resource.service.name = "cart"`, `name = "GET /cart"`, `status = error`, `duration >= 250ms`,
		`(resource.k8s.namespace.name = "shop" || resource.k8s.namespace.name = "pay")`, `(resource.continuum.cluster.id = "cl-1")`} {
		if !strings.Contains(q, want) {
			t.Fatalf("TraceQL %s lacks %s", q, want)
		}
	}
	if len(got) != 1 || got[0].RootService != "" || got[0].RootName != "" || got[0].MatchedSpans != 2 || !reflect.DeepEqual(got[0].Services, []string{"cart"}) || got[0].TraceID != traceHex {
		t.Fatalf("a limited scope's summary = %+v", got)
	}
	full, err := c.SearchTraces(ctx, AllSignals(), TraceFilter{}, rangeAll, 20)
	if err != nil || full[0].RootService != "gateway" || f.last("/api/search").Get("q") != "{ true }" {
		t.Fatalf("%+v %v %q", full, err, f.last("/api/search").Get("q"))
	}
	var e *Error
	if _, err := c.SearchTraces(ctx, AllSignals(), TraceFilter{Status: "bogus"}, rangeAll, 20); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Fatalf("bad status: %v", err)
	}
}

func TestFuseTraceJoinsLogsToSpansAndMetricsToResources(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, lokiResult(
			[]any{map[string]string{"service_name": "billing", "k8s_namespace_name": "pay"}, []any{"1791200000000000000", "card declined", map[string]any{"structuredMetadata": map[string]string{"trace_id": traceHex, "span_id": "0000000000000001", "severity_text": "ERROR"}}}},
			[]any{map[string]string{"service_name": "cart", "k8s_namespace_name": "shop"}, []any{"1791200001000000000", "no span id", map[string]any{"structuredMetadata": map[string]string{"trace_id": traceHex}}}},
		))
	}
	var seen []string
	var mu sync.Mutex
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query().Get("query"))
		mu.Unlock()
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{
			map[string]any{"metric": map[string]string{"__name__": "m_" + strings.ReplaceAll(r.URL.Query().Get("query")[:30], "\"", ""), "k8s_namespace_name": "shop"}, "values": [][]any{{1791200000.0, "1"}}},
		}}})
	}
	c := f.client()
	got, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, Metrics: true, MetricRegex: "cpu.*|mem.*"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SignalLogs] != SourceOK || got.Sources[SignalMetrics] != SourceOK || got.Sources[SignalTraces] != SourceOK {
		t.Fatalf("sources = %v", got.Sources)
	}
	var charge *Span
	for _, sp := range got.Spans {
		if sp.Name == "charge" {
			charge = sp
		}
	}
	if len(charge.Logs) != 1 || charge.Logs[0].Line != "card declined" || got.Logs.Matched != 1 || got.Logs.Total != 2 || len(got.Logs.Unmatched) != 1 || got.Logs.Unmatched[0].Line != "no span id" {
		t.Fatalf("logs: span %+v, summary %+v", charge.Logs, got.Logs)
	}
	// The window around the trace, padded two minutes each side, was used for logs.
	q := f.last("/loki/api/v1/query_range")
	wantStart := time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC).Add(-2 * time.Minute)
	if q.Get("start") != itoa(wantStart.UnixNano()) {
		t.Fatalf("log window start = %s, want %d", q.Get("start"), wantStart.UnixNano())
	}
	// Each of the three resources was asked for twice at most (service and pod view) and carries a series.
	if len(seen) < 5 {
		t.Fatalf("metric queries = %v", seen)
	}
	joined := strings.Join(seen, "\n")
	for _, want := range []string{`service_name="cart"`, `k8s_pod_name="cart-1"`, `service_name="billing"`, `k8s_namespace_name="pay"`, `__name__=~"cpu.*|mem.*"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("no metric query with %s in:\n%s", want, joined)
		}
	}
	for _, r := range got.Resources {
		if r.Service == "cart" && len(r.Metrics) == 0 {
			t.Fatalf("cart has no metrics: %+v", r)
		}
	}
}

func TestFuseTraceReportsAStoreThatIsDownWithoutLosingTheTrace(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	c := f.client()
	f.lSrv.Close() // Loki is off
	got, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SignalLogs] != SourceUnavailable || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "Loki") || got.SpanCount != 3 {
		t.Fatalf("sources %v warnings %v", got.Sources, got.Warnings)
	}
	// A token without logs gets the trace and an honest "not allowed".
	got, err = c.FuseTrace(context.Background(), Scope{Signals: []string{SignalTraces}}, traceHex, FuseOptions{Logs: true, Metrics: true})
	if err != nil || got.Sources[SignalLogs] != SourceNotAllowed || got.Sources[SignalMetrics] != SourceNotAllowed {
		t.Fatalf("%v %v", got.Sources, err)
	}
	got, _ = c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{})
	if got.Sources[SignalLogs] != SourceNotRequested {
		t.Fatalf("%v", got.Sources)
	}
}

func TestApplicationsMergeTheThreeStores(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": []string{"cart", "billing"}})
	}
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": []string{"cart"}})
	}
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"tagValues": []any{map[string]any{"type": "string", "value": "cart"}, map[string]any{"type": "string", "value": "gateway"}}})
	}
	c := f.client()
	apps, sources, err := c.Applications(context.Background(), AllSignals(), rangeAll)
	if err != nil {
		t.Fatal(err)
	}
	want := []Application{{"billing", []string{"metrics"}}, {"cart", []string{"metrics", "logs", "traces"}}, {"gateway", []string{"traces"}}}
	if !reflect.DeepEqual(apps, want) || sources[SignalLogs] != SourceOK {
		t.Fatalf("apps %+v sources %v", apps, sources)
	}
	// The scope reaches every store's own listing call.
	if _, _, err := c.Applications(context.Background(), limited, rangeAll); err != nil {
		t.Fatal(err)
	}
	if m := f.last("/api/v1/label/service_name/values").Get("match[]"); !strings.Contains(m, `k8s_namespace_name=~"shop|pay"`) || !strings.Contains(m, `continuum_cluster_id=~"cl-1"`) {
		t.Fatalf("metrics listing was not scoped: %s", m)
	}
	if q := f.last("/loki/api/v1/query_range").Get("query"); !strings.Contains(q, `continuum_cluster_id=~"cl-1"`) {
		t.Fatalf("logs listing for a cluster-limited scope: %s", q)
	}
	if q := f.last("/api/v2/search/tag/resource.service.name/values").Get("q"); !strings.Contains(q, `resource.k8s.namespace.name = "shop"`) {
		t.Fatalf("traces listing was not scoped: %s", q)
	}
	// Everything down reports unavailable, not an empty list.
	f.pSrv.Close()
	f.lSrv.Close()
	f.tSrv.Close()
	if _, _, err := c.Applications(context.Background(), AllSignals(), rangeAll); !IsUnavailable(err) {
		t.Fatalf("all stores down: %v", err)
	}
}

func TestParseRangeAndStep(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tr, err := ParseRange("now-15m", "", now)
	if err != nil || !tr.To.Equal(now) || tr.To.Sub(tr.From) != 15*time.Minute {
		t.Fatalf("%+v %v", tr, err)
	}
	tr, err = ParseRange("", "", now)
	if err != nil || tr.To.Sub(tr.From) != DefaultWindow {
		t.Fatalf("default window: %+v %v", tr, err)
	}
	tr, err = ParseRange("2026-10-05T10:00:00Z", "1791198000", now)
	if err != nil || !tr.From.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", tr, err)
	}
	for _, bad := range [][2]string{{"yesterday", ""}, {"", "soon"}, {"now", "now-1h"}, {"now-40d", ""}} {
		if _, err := ParseRange(bad[0], bad[1], now); err == nil {
			t.Fatalf("ParseRange(%q, %q) accepted", bad[0], bad[1])
		}
	}
	step, err := ChooseStep(TimeRange{From: now.Add(-time.Hour), To: now}, 0, 60)
	if err != nil || step != time.Minute {
		t.Fatalf("step = %v %v", step, err)
	}
	if step, _ = ChooseStep(TimeRange{From: now.Add(-time.Minute), To: now}, 0, 60); step != 15*time.Second {
		t.Fatalf("a short range uses the minimum step, got %v", step)
	}
	if _, err := ChooseStep(TimeRange{From: now.Add(-24 * time.Hour), To: now}, time.Second, 60); err == nil {
		t.Fatal("a step giving 86400 points was accepted")
	}
}

func TestAnAnswerBiggerThanTheLimitIsRefused(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"trace":` + strings.Repeat(" ", 4096) + `{}}`))
	}
	c := f.client()
	c.MaxBytes = 1024
	_, err := c.Trace(context.Background(), AllSignals(), traceHex)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
		t.Fatalf("%v", err)
	}
}

func TestStoreErrorsKeepTheirMeaning(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error: unexpected end of input"}`))
	}
	_, err := f.client().RawMetricQuery(context.Background(), AllSignals(), "query", url.Values{"query": {"sum("}})
	var e *Error
	if !errors.As(err, &e) || e.Status != 400 || !strings.Contains(e.Msg, "parse error") {
		t.Fatalf("%v", err)
	}
	f.prom = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }
	if _, err := f.client().RawMetricQuery(context.Background(), AllSignals(), "query", url.Values{"query": {"up"}}); !errors.As(err, &e) || e.Status != http.StatusBadGateway {
		t.Fatalf("%v", err)
	}
	// An unconfigured store is unavailable.
	if _, err := (&Client{}).Trace(context.Background(), AllSignals(), traceHex); !IsUnavailable(err) {
		t.Fatalf("%v", err)
	}
}

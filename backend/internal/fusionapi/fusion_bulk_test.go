package fusionapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tempoTraceOnNode is tempoTrace with the cart and billing pods on named nodes.
func tempoTraceOnNode() map[string]any {
	t := tempoTrace()
	rs := t["trace"].(map[string]any)["resourceSpans"].([]any)
	for i, node := range []string{"node-a", "node-b"} {
		res := rs[i].(map[string]any)["resource"].(map[string]any)
		res["attributes"] = append(res["attributes"].([]any), kv("k8s.node.name", node))
	}
	return t
}

func TestParseFuseParams(t *testing.T) {
	parse := func(raw string) (FuseOptions, bool, error) {
		q, _ := url.ParseQuery(raw)
		return ParseFuseParams(q)
	}
	if _, fused, err := parse(""); fused || err != nil {
		t.Fatalf("no parameters is not a fused read: %v %v", fused, err)
	}
	o, fused, err := parse("fused=true")
	if !fused || err != nil || !o.Logs || !o.Metrics || o.ContextLogs || o.SystemLogs {
		t.Fatalf("fused=true means logs and metrics: %+v %v %v", o, fused, err)
	}
	// An include list alone implies fused; "all" and the alias work.
	o, fused, _ = parse("include=unlinked_logs")
	if !fused || !o.ContextLogs || o.Logs || o.Metrics {
		t.Fatalf("%+v", o)
	}
	if o, _, _ = parse("include=all"); !(o.Logs && o.ContextLogs && o.SystemLogs && o.Metrics) {
		t.Fatalf("%+v", o)
	}
	o, fused, err = parse("include=none&omit=attributes,events&span_status=error&span_min_duration=50ms&span_service=cart&log_severity=error,warn&metric_scope=node,app&max_logs=99999&points=0x")
	if err == nil {
		t.Fatalf("points=0x must be refused, got %+v", o)
	}
	o, fused, err = parse("include=none&omit=attributes,events&span_status=error&span_min_duration=50ms&span_service=cart&log_severity=error,warn&metric_scope=node,app&max_logs=99999&system_namespaces=kube-system,kube-public&pad=5m")
	if err != nil || !fused || o.Logs || o.Metrics || !o.OmitAttributes || !o.OmitEvents || o.Spans.Status != "error" || o.Spans.MinDuration != 50*time.Millisecond ||
		o.Spans.Service != "cart" || o.LogSeverity != "error,warn" || !o.MetricViews.Node || !o.MetricViews.App || o.MetricViews.Pod ||
		o.MaxLogs != hardMaxLogs || len(o.SystemNamespaces) != 2 || o.Pad != 5*time.Minute {
		t.Fatalf("%+v %v %v", o, fused, err)
	}
	for _, bad := range []string{"fused=maybe", "include=logs,secrets", "fused=false&include=logs", "fused=true&omit=spans", "fused=true&metric_scope=cluster",
		"fused=true&max_logs=-1", "fused=true&pad=-1m", "fused=true&span_min_duration=quick"} {
		if _, _, err := parse(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// fused=false switches it off even with the other options given.
	if _, fused, err := parse("fused=false&pad=5m"); fused || err != nil {
		t.Fatalf("fused=false: %v %v", fused, err)
	}
	// The names the parser reads are the names the API documents.
	for _, n := range FuseParamNames {
		if n == "" {
			t.Fatal("empty parameter name")
		}
	}
}

func TestFuseTraceReadsContextLogsAndSystemLogs(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTraceOnNode()) }
	var mu sync.Mutex
	var queries []string
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		line := func(svc, ns, text, at string, meta map[string]string) []any {
			return []any{map[string]string{"service_name": svc, "k8s_namespace_name": ns}, []any{at, text, map[string]any{"structuredMetadata": meta}}}
		}
		switch {
		case strings.Contains(q, `k8s_node_name=`):
			writeJSON(w, lokiResult(line("kubelet", "kube-system", "evicting pod", "1791200002000000000", map[string]string{"k8s_node_name": strings.Split(strings.Split(q, `k8s_node_name="`)[1], `"`)[0]})))
		case strings.Contains(q, `trace_id=""`):
			writeJSON(w, lokiResult(
				line("cart", "shop", "newer", "1791200003000000000", nil),
				line("cart", "shop", "older", "1791200001000000000", nil)))
		default:
			writeJSON(w, lokiResult(line("billing", "pay", "declined", "1791200000000000000", map[string]string{"trace_id": traceHex, "span_id": "0000000000000001"})))
		}
	}
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, ContextLogs: true, SystemLogs: true, LogSeverity: "error,warn"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SourceContextLogs] != SourceOK || got.Sources[SourceSystemLogs] != SourceOK || got.Sources[SignalLogs] != SourceOK {
		t.Fatalf("sources = %v", got.Sources)
	}
	if !strings.HasPrefix(got.Joins[SourceContextLogs], "associated") || !strings.Contains(got.Joins[SourceSystemLogs], "kube-system") {
		t.Fatalf("joins = %v", got.Joins)
	}
	// Context lines sit on the resource, oldest first; the lines of a trace do not.
	var cart *Resource
	for _, r := range got.Resources {
		if r.Service == "cart" {
			cart = r
		}
	}
	if len(cart.Logs) != 2 || cart.Logs[0].Line != "older" || cart.Logs[1].Line != "newer" {
		t.Fatalf("cart context logs = %+v", cart.Logs)
	}
	if got.Logs.Total != 1 || got.Logs.Matched != 1 {
		t.Fatalf("trace logs = %+v", got.Logs)
	}
	// System lines: one read per node, the namespaces named, kept once.
	if got.SystemLogs == nil || strings.Join(got.SystemLogs.Nodes, ",") != "node-a,node-b" || len(got.SystemLogs.Entries) != 2 || got.SystemLogs.Namespaces[0] != "kube-system" {
		t.Fatalf("system logs = %+v", got.SystemLogs)
	}
	joined := strings.Join(queries, "\n")
	for _, want := range []string{`k8s_namespace_name=~"kube-system"`, `k8s_node_name="node-a"`, `k8s_node_name="node-b"`, `trace_id=""`, `severity_text=~"(?i)(error|warn)"`, `k8s_pod_name="cart-1"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("no query with %s in:\n%s", want, joined)
		}
	}
	// The same read for a token that cannot read the logs: honest "not allowed", and nothing asked of Loki.
	f.mu.Lock()
	before := len(queries)
	f.mu.Unlock()
	got, err = f.client().FuseTrace(context.Background(), Scope{Signals: []string{SignalTraces}}, traceHex, FuseOptions{ContextLogs: true, SystemLogs: true})
	if err != nil || got.Sources[SourceContextLogs] != SourceNotAllowed || got.Sources[SourceSystemLogs] != SourceNotAllowed || got.SystemLogs != nil {
		t.Fatalf("%v %v", got.Sources, err)
	}
	if len(queries) != before {
		t.Fatal("Loki was asked on behalf of a token that cannot read logs")
	}
}

func TestSystemLogsRespectTheScopeAndTheTracesNodes(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) } // no node in this one
	f.loki = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, lokiResult()) }
	c := f.client()
	got, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{SystemLogs: true})
	if err != nil || got.SystemLogs == nil || len(got.SystemLogs.Entries) != 0 || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "node") {
		t.Fatalf("a trace without nodes: %+v %v %v", got.SystemLogs, got.Warnings, err)
	}
	// A token that cannot see kube-system gets a warning, not a read.
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTraceOnNode()) }
	asked := false
	f.loki = func(w http.ResponseWriter, r *http.Request) { asked = true; writeJSON(w, lokiResult()) }
	got, err = c.FuseTrace(context.Background(), limited, traceHex, FuseOptions{SystemLogs: true})
	if err != nil || asked || len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "system namespaces") {
		t.Fatalf("limited: asked=%v %v %v", asked, got.Warnings, err)
	}
	// A bad log filter is a bad request whatever the stores are doing.
	if _, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, LogSeverity: "err or {"}); err == nil {
		t.Fatal("a bad severity was accepted")
	}
}

func TestFuseTraceShapesTheAnswer(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, lokiResult([]any{map[string]string{"service_name": "billing", "k8s_namespace_name": "pay"}, []any{"1791200000000000000", "card declined", map[string]any{"structuredMetadata": map[string]string{"trace_id": traceHex, "span_id": "0000000000000001"}}}}))
	}
	c := f.client()
	got, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, OmitAttributes: true, OmitEvents: true, Spans: SpanFilter{Status: "error"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spans) != 1 || got.Spans[0].Name != "charge" || got.SpansOmitted != 2 || got.SpanCount != 3 || got.Spans[0].Attributes != nil || got.Resources[0].Attributes != nil {
		t.Fatalf("spans %d omitted %d count %d", len(got.Spans), got.SpansOmitted, got.SpanCount)
	}
	// The line sits on the span that stayed; one on a span that left is counted, not lost.
	if len(got.Spans[0].Logs) != 1 || got.Logs.OnFilteredSpans != 0 {
		t.Fatalf("logs = %+v on span %+v", got.Logs, got.Spans[0].Logs)
	}
	got, _ = c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Logs: true, Spans: SpanFilter{Service: "cart"}})
	if len(got.Spans) != 1 || got.Logs.OnFilteredSpans != 1 || got.Logs.Matched != 1 {
		t.Fatalf("filtered span's line: %+v", got.Logs)
	}
	got, _ = c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Spans: SpanFilter{MinDuration: 60 * time.Millisecond}})
	if len(got.Spans) != 1 || got.Spans[0].Name != "GET /cart" {
		t.Fatalf("min duration: %+v", got.Spans)
	}
	if _, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Spans: SpanFilter{Status: "weird"}}); err == nil {
		t.Fatal("a bad span status was accepted")
	}
}

func TestMetricScopePicksWhoseSeriesAResourceCarries(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTraceOnNode()) }
	var mu sync.Mutex
	var queries []string
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		mu.Unlock()
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}
	c := f.client()
	run := func(v MetricViews) string {
		mu.Lock()
		queries = nil
		mu.Unlock()
		if _, err := c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Metrics: true, MetricViews: v}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(queries, "\n")
	}
	if q := run(MetricViews{}); strings.Contains(q, `k8s_node_name`) || !strings.Contains(q, `service_name="cart"`) || !strings.Contains(q, `k8s_pod_name="cart-1"`) {
		t.Fatalf("default views:\n%s", q)
	}
	if q := run(MetricViews{Node: true}); !strings.Contains(q, `k8s_node_name="node-a"`) || strings.Contains(q, `service_name="cart"`) || strings.Contains(q, `k8s_pod_name="cart-1"`) || !strings.Contains(q, `k8s_pod_name=""`) {
		t.Fatalf("node only:\n%s", q)
	}
}

func TestFuseManyIsBoundedKeepsOrderAndReportsEachFailure(t *testing.T) {
	f := newFake(t)
	var cur, peak int32
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		writeJSON(w, tempoTrace())
	}
	ids := []string{}
	for i := 0; i < 12; i++ {
		ids = append(ids, strings.Repeat("a", 31)+"0123456789ab"[i:i+1])
	}
	ids[4] = strings.Repeat("d", 32) // the fake answers 404 for this one
	f.tempo = wrapDead(f.tempo, ids[4])
	ids = append(ids, "not a trace id")
	var doneN int32
	var seq []string
	var mu sync.Mutex
	items := f.client().FuseMany(context.Background(), AllSignals(), ids, FuseOptions{}, func(it BulkItem) {
		atomic.AddInt32(&doneN, 1)
		mu.Lock()
		seq = append(seq, it.ID)
		mu.Unlock()
	})
	if len(items) != len(ids) || int(doneN) != len(ids) {
		t.Fatalf("items %d done %d", len(items), doneN)
	}
	for i, it := range items {
		if it.ID != ids[i] {
			t.Fatalf("item %d is %q, want the order asked", i, it.ID)
		}
	}
	if items[4].Status != http.StatusNotFound || items[len(ids)-1].Status != http.StatusBadRequest || items[0].Status != 200 || items[0].Trace == nil {
		t.Fatalf("statuses: %d %d %d", items[4].Status, items[len(ids)-1].Status, items[0].Status)
	}
	if sum := Summarise(items); sum.Requested != 13 || sum.OK != 11 || sum.Failed != 2 {
		t.Fatalf("summary %+v", sum)
	}
	if peak > bulkParallel {
		t.Fatalf("%d traces read at once, the bound is %d", peak, bulkParallel)
	}
}

// wrapDead answers 404 for one id and passes the rest on.
func wrapDead(next http.HandlerFunc, dead string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, dead) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func TestFuseManyReportsTracesItNeverStartedWhenTheTimeRunsOut(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
		}
		writeJSON(w, tempoTrace())
	}
	var ids []string
	for i := 0; i < 9; i++ {
		ids = append(ids, strings.Repeat("b", 31)+"0123456789ab"[i:i+1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	items := f.client().FuseMany(ctx, AllSignals(), ids, FuseOptions{}, nil)
	if len(items) != 9 {
		t.Fatalf("%d items", len(items))
	}
	for i, it := range items {
		if it.Status != http.StatusGatewayTimeout || it.Error == "" {
			t.Errorf("item %d: %+v", i, it)
		}
	}
}

func TestBulkReadsLeaveRoomForASingleRead(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	release := make(chan struct{})
	var inProm int32
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inProm, 1)
		defer atomic.AddInt32(&inProm, -1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}
	c := f.client()
	var ids []string
	for i := 0; i < 9; i++ {
		ids = append(ids, strings.Repeat("c", 31)+"0123456789ab"[i:i+1])
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.FuseMany(context.Background(), AllSignals(), ids, FuseOptions{Metrics: true}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&inProm) < maxBulkUpstream && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&inProm); got != maxBulkUpstream {
		t.Fatalf("bulk holds %d store calls, expected it to fill its lane of %d", got, maxBulkUpstream)
	}
	// The bulk read is stuck waiting on Prometheus, and a single read still goes straight through.
	one := make(chan error, 1)
	go func() { _, err := c.Trace(context.Background(), AllSignals(), traceHex); one <- err }()
	select {
	case err := <-one:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a single read waited behind the bulk read")
	}
	close(release)
	<-done
}

func TestNormalizeIDs(t *testing.T) {
	got, err := NormalizeIDs([]string{" " + traceHex, strings.ToUpper(traceHex), "", "abc"})
	if err != nil || len(got) != 2 || got[0] != traceHex || got[1] != "abc" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := NormalizeIDs([]string{" ", ""}); err == nil {
		t.Fatal("no ids accepted")
	}
	many := make([]string, MaxBatch+1)
	for i := range many {
		many[i] = strings.Repeat("a", 24) + strings.Repeat("0", 8-len(itoa(int64(i)))) + itoa(int64(i))
	}
	if _, err := NormalizeIDs(many); err == nil {
		t.Fatal("more than MaxBatch accepted")
	}
}

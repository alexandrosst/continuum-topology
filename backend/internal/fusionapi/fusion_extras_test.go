package fusionapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParsePromQueries(t *testing.T) {
	qs, err := ParsePromQueries([]string{`errors=sum(rate(x{a="${service}"}[1m]))`, " cpu = up == 1 ", `rss=m{n="${pod:re}"}`})
	if err != nil || len(qs) != 3 || qs[0].Name != "errors" || !qs[0].perResource || qs[1].Expr != "up == 1" || qs[1].perResource || !qs[2].perResource {
		t.Fatalf("%+v %v", qs, err)
	}
	for _, bad := range []string{`up`, `rate(x{a="b"}[1m])`, `n=`, `=up`, `1bad=up`, `n==up`, `n=up{a="${nope}"}`, `n=up{a="${service"}`, `n=up{a="${service:glob}"}`,
		strings.Repeat("a", 33) + `=up`, `n=` + strings.Repeat("x", maxPromQueryLen+1)} {
		if _, err := ParsePromQueries([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ParsePromQueries([]string{`a=up`, `a=down`}); err == nil {
		t.Error("a repeated name was accepted")
	}
	var many []string
	for i := 0; i <= maxPromQueries; i++ {
		many = append(many, "q"+string(rune('a'+i))+"=up")
	}
	if _, err := ParsePromQueries(many); err == nil {
		t.Error("too many queries were accepted")
	}
	// An escaped ${ stays literal.
	qs, err = ParsePromQueries([]string{`n=label_replace(up, "x", "$${1}", "", "")`})
	if err != nil || qs[0].perResource {
		t.Fatalf("%+v %v", qs, err)
	}
	if e, _ := qs[0].expand(nil); e != `label_replace(up, "x", "${1}", "", "")` {
		t.Fatalf("%s", e)
	}
}

func TestPromQLValuesCannotChangeTheQuery(t *testing.T) {
	qs, err := ParsePromQueries([]string{`a=up{service_name="${service}",pod=~"${pod:re}"}`})
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]
	e, skip := q.expand(map[string]string{"service": "cart", "pod": "cart-1.x"})
	if skip != "" || e != `up{service_name="cart",pod=~"cart-1\\.x"}` {
		t.Fatalf("%q %q", e, skip)
	}
	for _, bad := range []string{`x"} or vector(1) {a="`, `a\b`, "a\nb", "a`b", "a{b}", "a'b"} {
		if _, skip := q.expand(map[string]string{"service": bad, "pod": "p"}); skip == "" {
			t.Errorf("%q was substituted", bad)
		}
	}
	if _, skip := q.expand(map[string]string{"service": "cart"}); !strings.Contains(skip, "no pod") {
		t.Fatalf("a missing value must say so: %q", skip)
	}
}

func matrix(name string, vals ...float64) map[string]any {
	base := float64(time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC).Unix())
	var v [][]any
	for i, x := range vals {
		v = append(v, []any{base - 60 + float64(i)*20, itoa(int64(x))})
	}
	return map[string]any{"metric": map[string]string{"__name__": name}, "values": v}
}

func TestFuseTraceRunsYourPromQLPerResourceAndOnce(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	var mu sync.Mutex
	var queries []string
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{matrix("", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)}}})
	}
	qs, err := ParsePromQueries([]string{`errs=rate(x{svc="${service}",pod="${pod}"}[${range}])`, `all=sum(up{trace="${trace_id}"})`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{PromQL: qs, PromQLSpans: true, SpanPad: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SourcePromQL] != SourceOK || got.Sources[SignalMetrics] != SourceNotRequested || got.Sources[SignalLogs] != SourceNotRequested {
		t.Fatalf("sources %v", got.Sources)
	}
	if !strings.HasPrefix(got.Joins[SourcePromQL], "associated") {
		t.Fatalf("joins %v", got.Joins)
	}
	// 2 resources with a pod + 1 whole-trace query. The audit resource has no pod: skipped, and said.
	if len(queries) != 3 {
		t.Fatalf("queries = %v", queries)
	}
	byKey := map[string]*Resource{}
	for _, r := range got.Resources {
		byKey[r.Service] = r
	}
	cart := byKey["cart"]
	if len(cart.Queries) != 1 || cart.Queries[0].Name != "errs" || !strings.HasPrefix(cart.Queries[0].Query, `rate(x{svc="cart",pod="cart-1"}[`) || len(cart.Queries[0].Series) != 1 {
		t.Fatalf("cart: %+v", cart.Queries)
	}
	if len(byKey["billing"].Queries) != 1 || len(byKey["audit"].Queries) != 0 {
		t.Fatalf("billing %+v audit %+v", byKey["billing"].Queries, byKey["audit"].Queries)
	}
	if len(got.Queries) != 1 || got.Queries[0].Name != "all" || !strings.Contains(got.Queries[0].Query, traceHex) {
		t.Fatalf("whole-trace: %+v", got.Queries)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "promql errs: not evaluated for") || !strings.Contains(strings.Join(got.Warnings, "\n"), "no pod") {
		t.Fatalf("warnings %v", got.Warnings)
	}
	// The span of the cart resource carries the points of its own time (the root: base .. base+90ms, plus 25 s).
	var root *Span
	for _, sp := range got.Spans {
		if sp.Name == "GET /cart" {
			root = sp
		}
	}
	if len(root.Queries) != 1 || root.Queries[0].Name != "errs" || len(root.Queries[0].Series) != 1 || len(root.Queries[0].Series[0].Points) == 0 || len(root.Queries[0].Series[0].Points) >= 10 {
		t.Fatalf("span queries: %+v", root.Queries)
	}
	if root.Queries[0].Query != "" {
		t.Fatalf("a span does not repeat the expression")
	}
	if p := f.last("/api/v1/query_range"); p.Get("step") == "" || p.Get("limit") == "" {
		t.Fatalf("%v", p)
	}
}

func TestSameExpressionIsEvaluatedOnce(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	var n int
	var mu sync.Mutex
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}
	qs, _ := ParsePromQueries([]string{`a=up{ns="${namespace}",cl="${cluster}"}`, `b=up{ns="${namespace}",cl="${cluster}"}`, `c=up{cl="${cluster}"}`})
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{PromQL: qs})
	if err != nil {
		t.Fatal(err)
	}
	// a and b are one expression per namespace (3), c is one for the cluster all three share.
	if n != 4 {
		t.Fatalf("store reads = %d", n)
	}
	for _, r := range got.Resources {
		if len(r.Queries) != 3 {
			t.Fatalf("%s carries %d results", r.Service, len(r.Queries))
		}
	}
}

func TestPromQLNeedsAnUnrestrictedCallerAndOneBadQueryDoesNotSpoilTheRest(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "broken") {
			w.WriteHeader(400)
			writeJSON(w, map[string]any{"status": "error", "error": "parse error: unexpected end of input"})
			return
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{matrix("up", 1, 2)}}})
	}
	qs, _ := ParsePromQueries([]string{`good=up`, `bad=broken(`})
	c := f.client()
	got, err := c.FuseTrace(context.Background(), limited, traceHex, FuseOptions{PromQL: qs})
	if err != nil || got.Sources[SourcePromQL] != SourceNotAllowed || len(got.Queries) != 0 || !strings.Contains(strings.Join(got.Warnings, " "), "not limited") {
		t.Fatalf("%v %v %v", got.Sources, got.Warnings, err)
	}
	for _, p := range f.paths() {
		if p == "/api/v1/query_range" {
			t.Fatal("a limited caller's query reached Prometheus")
		}
	}
	got, err = c.FuseTrace(context.Background(), Scope{Signals: []string{SignalTraces}}, traceHex, FuseOptions{PromQL: qs})
	if err != nil || got.Sources[SourcePromQL] != SourceNotAllowed {
		t.Fatalf("%v %v", got.Sources, err)
	}
	got, err = c.FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{PromQL: qs})
	if err != nil || got.Sources[SourcePromQL] != SourceError || len(got.Queries) != 2 {
		t.Fatalf("%v %v %v", got.Sources, got.Queries, err)
	}
	for _, q := range got.Queries {
		switch q.Name {
		case "good":
			if q.Error != "" || len(q.Series) != 1 {
				t.Fatalf("%+v", q)
			}
		case "bad":
			if !strings.Contains(q.Error, "parse error") || len(q.Series) != 0 {
				t.Fatalf("%+v", q)
			}
		}
	}
	if got.SpanCount != 3 {
		t.Fatal("the trace must still come back")
	}
}

func TestPromQLOptionsAreParsed(t *testing.T) {
	q := url.Values{}
	q.Add("promql", `a=up{s="${service}"}`)
	q.Add("promql", `b=sum(up)`)
	q.Set("promql_spans", "true")
	q.Set("promql_step", "15s")
	q.Set("promql_pad", "10m")
	q.Set("promql_services", "cart,billing")
	o, fused, err := ParseFuseParams(q)
	if err != nil || !fused || len(o.PromQL) != 2 || !o.PromQLSpans || o.PromQLStep != 15*time.Second || o.PromQLPad != 10*time.Minute || len(o.PromQLServices) != 2 {
		t.Fatalf("%+v %v %v", o, fused, err)
	}
	if o.Logs || o.Metrics {
		t.Fatal("promql alone must not add logs and metrics")
	}
	q.Set("include", "logs")
	if o, _, _ = ParseFuseParams(q); !o.Logs || o.Metrics || len(o.PromQL) != 2 {
		t.Fatalf("%+v", o)
	}
	for _, bad := range []string{"promql_spans=true&include=logs", "promql_spans=maybe&promql=a=up", "fused=false&promql=a=up", "promql=up", "include=topology&changes_before=-1m"} {
		if _, _, err := ParseFuseParams(mustQuery(t, bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, o := range []FuseOptions{{PromQLStep: 200 * time.Millisecond}, {PromQLPad: 2 * time.Hour}, {ChangesBefore: 48 * time.Hour}} {
		if err := o.defaults(); err == nil {
			t.Errorf("%+v was accepted", o)
		}
	}
	o2, _, err := ParseFuseParams(mustQuery(t, "include=topology,changes&changes_before=2h&max_changes=7"))
	if err != nil || !o2.Topology || !o2.Changes || o2.ChangesBefore != 2*time.Hour || o2.MaxChanges != 7 || o2.Logs {
		t.Fatalf("%+v %v", o2, err)
	}
	if o3, _, _ := ParseFuseParams(mustQuery(t, "include=all")); !o3.Topology || !o3.Changes {
		t.Fatalf("%+v", o3)
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

type fakeExtras struct {
	view   *TopologyView
	events []ChangeEvent
	err    error
	since  time.Time
	until  time.Time
	cls    []string
}

func (f *fakeExtras) Topology(context.Context) (*TopologyView, error) { return f.view, f.err }
func (f *fakeExtras) Changes(_ context.Context, since, until time.Time, clusters []string, _ int) ([]ChangeEvent, error) {
	f.since, f.until, f.cls = since, until, clusters
	return f.events, f.err
}

func testView() *TopologyView {
	return &TopologyView{
		Services: []TopoService{
			{ID: "s-cart", Name: "cart", Namespace: "shop", Cluster: "cl-1", Kind: "Deployment", Replicas: 2, Ready: 2},
			{ID: "s-billing", Name: "billing", Namespace: "pay", Cluster: "cl-1", Replicas: 1, Ready: 0, Restarts: 4},
			{ID: "s-cart-other", Name: "cart", Namespace: "shop", Cluster: "cl-2"},
			{ID: "s-db", Name: "ledger", Namespace: "secret", Cluster: "cl-1"},
			{ID: "s-web", Name: "web", Namespace: "shop", Cluster: "cl-1"},
		},
		Links: []TopoLink{
			{From: "s-cart", To: "s-billing", FromKind: "service", ToKind: "service", Protocol: "tcp", Port: 8080, Confidence: "high"},
			{From: "s-cart", To: "s-db", FromKind: "service", ToKind: "service"},
			{From: "s-cart", To: "x-1", FromKind: "service", ToKind: "external", Port: 443},
			{From: "s-cart", To: "s-dns", FromKind: "service", ToKind: "service", Noise: "dns"},
			{From: "s-web", To: "s-cart", FromKind: "service", ToKind: "service", Protocol: "tcp"},
		},
		Externals: map[string]string{"x-1": "GitHub"},
	}
}

func TestFuseTraceJoinsTopologyNeighbours(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	ex := &fakeExtras{view: testView()}
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Topology: true, Extras: ex})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SourceTopology] != SourceOK {
		t.Fatalf("%v %v", got.Sources, got.Warnings)
	}
	var cart, audit *Resource
	for _, r := range got.Resources {
		switch r.Service {
		case "cart":
			cart = r
		case "audit":
			audit = r
		}
	}
	if audit.Topology != nil {
		t.Fatal("a service Ikhnos does not know has no topology")
	}
	tp := cart.Topology
	if tp == nil || tp.Service.ID != "s-cart" || tp.Service.Cluster != "cl-1" {
		t.Fatalf("matched the wrong cart: %+v", tp)
	}
	var calls []string
	for _, n := range tp.Calls {
		calls = append(calls, n.Kind+":"+n.Name)
	}
	if strings.Join(calls, ",") != "external:GitHub,service:billing,service:ledger" || len(tp.CalledBy) != 1 || tp.CalledBy[0].Name != "web" {
		t.Fatalf("calls %v calledBy %+v", calls, tp.CalledBy)
	}
	// A limited token does not learn the neighbours it may not see, nor the addresses outside the clusters.
	got, err = f.client().FuseTrace(context.Background(), Scope{Signals: Signals, Namespaces: []string{"shop"}}, traceHex, FuseOptions{Topology: true, Extras: ex})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Resources {
		if r.Service == "cart" {
			if len(r.Topology.Calls) != 0 || len(r.Topology.CalledBy) != 1 {
				t.Fatalf("%+v", r.Topology)
			}
		}
	}
}

func TestFuseTraceJoinsChangesAroundTheTrace(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTraceOnNode()) }
	at := time.Date(2026, 10, 5, 11, 20, 0, 0, time.UTC)
	ex := &fakeExtras{view: testView(), events: []ChangeEvent{
		{Time: at.Add(2 * time.Minute), Kind: "node-status", TargetKind: "node", TargetID: "node-a", Name: "node-a", Cluster: "cl-1"},
		{Time: at, Kind: "service-scaled", TargetKind: "service", TargetID: "s-cart", Name: "cart", Cluster: "cl-1", Detail: "2 -> 3 replicas"},
		{Time: at, Kind: "service-scaled", TargetKind: "service", TargetID: "s-web", Name: "web", Cluster: "cl-1"},       // not in the trace
		{Time: at, Kind: "service-scaled", TargetKind: "service", TargetID: "s-ledger", Name: "ledger", Cluster: "cl-1"}, // unknown to the topology
		{Time: at, Kind: "node-status", TargetKind: "node", TargetID: "node-z", Name: "node-z", Cluster: "cl-1"},         // another node
		{Time: at.Add(time.Minute), Kind: "cluster-added", TargetKind: "cluster", TargetID: "cl-1", Cluster: "cl-1"},
		{Time: at, Kind: "drift", TargetKind: "agent", TargetID: "a", Cluster: "cl-1"},
	}}
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Changes: true, Extras: ex, ChangesBefore: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[SourceChanges] != SourceOK || len(got.Changes) != 3 {
		t.Fatalf("%v %+v", got.Sources, got.Changes)
	}
	if got.Changes[0].Kind != "service-scaled" || got.Changes[0].Namespace != "shop" || got.Changes[0].Resource == "" || got.Changes[1].Kind != "cluster-added" || got.Changes[2].Kind != "node-status" {
		t.Fatalf("%+v", got.Changes)
	}
	// The look-back is before the trace, the look-ahead is pad after it, and the clusters narrow the read.
	if want := time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC); !ex.since.Equal(want) || !ex.until.After(time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC)) || len(ex.cls) != 1 || ex.cls[0] != "cl-1" {
		t.Fatalf("%v %v %v", ex.since, ex.until, ex.cls)
	}
	// A token limited to a namespace sees only that namespace's service events.
	got, _ = f.client().FuseTrace(context.Background(), Scope{Signals: Signals, Namespaces: []string{"shop"}}, traceHex, FuseOptions{Changes: true, Extras: ex})
	if len(got.Changes) != 1 || got.Changes[0].TargetID != "s-cart" {
		t.Fatalf("%+v", got.Changes)
	}
	got, _ = f.client().FuseTrace(context.Background(), Scope{Signals: Signals, Namespaces: []string{"pay"}}, traceHex, FuseOptions{Changes: true, Extras: ex})
	if len(got.Changes) != 0 {
		t.Fatalf("%+v", got.Changes)
	}
	// MaxChanges keeps the newest.
	got, _ = f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Changes: true, Extras: ex, MaxChanges: 1})
	if len(got.Changes) != 1 || got.Changes[0].Kind != "node-status" || !strings.Contains(strings.Join(got.Warnings, " "), "newest") {
		t.Fatalf("%+v %v", got.Changes, got.Warnings)
	}
}

func TestExtrasThatAreMissingOrFailingAreReportedNotFatal(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tempoTrace()) }
	got, err := f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Topology: true, Changes: true})
	if err != nil || got.Sources[SourceTopology] != SourceUnavailable || got.Sources[SourceChanges] != SourceUnavailable || got.SpanCount != 3 {
		t.Fatalf("%v %v", got.Sources, err)
	}
	got, err = f.client().FuseTrace(context.Background(), AllSignals(), traceHex, FuseOptions{Topology: true, Extras: &fakeExtras{err: errf(503, "down")}})
	if err != nil || got.Sources[SourceTopology] != SourceUnavailable || got.Sources[SourceChanges] != "" {
		t.Fatalf("%v %v", got.Sources, err)
	}
}

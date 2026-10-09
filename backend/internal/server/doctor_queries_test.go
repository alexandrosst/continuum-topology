package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"continuum/internal/fusionapi"
	"continuum/internal/model"
)

// vocName is the label the stores give an attribute of the vocabulary.
func vocName(t *testing.T, attr string) string {
	t.Helper()
	for _, l := range fusionapi.Vocabulary {
		if l.Attr == attr {
			return l.Name()
		}
	}
	t.Fatalf("%s is not in the vocabulary", attr)
	return ""
}

// decodeAppInfo reads the labels of every data point of an OTLP/HTTP metrics request, the way Prometheus' OTLP receiver
// would turn them into series: resource_metrics(1) > scope_metrics(2) > metrics(2) > gauge(5) > data_points(1) > attributes(7).
func decodeAppInfo(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, dp := range fields(body, 1, 2, 2, 5, 1) {
		labels := map[string]string{}
		for _, kv := range fields(dp, 7) {
			k, v := fields(kv, 1), fields(fields(kv, 2)[0], 1)
			labels[string(k[0])] = string(v[0])
		}
		out = append(out, labels)
	}
	return out
}

// fields follows the nested messages of a protobuf by field number and returns the payloads at the end of the path.
func fields(b []byte, path ...protowire.Number) [][]byte {
	if len(path) == 0 {
		return [][]byte{b}
	}
	var out [][]byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return out
		}
		if typ == protowire.BytesType && num == path[0] {
			v, _ := protowire.ConsumeBytes(b)
			out = append(out, fields(v, path[1:]...)...)
		}
		b = b[m:]
	}
	return out
}

// sampleParams are values for the parameters of the data API that have no example of their own; the others take theirs.
var sampleParams = map[string]string{"application": "shop", "service": "cart", "namespace": "shop", "cluster": "cl-1", "pod": "cart-0",
	"node": "n1", "trace_id": testTraceID, "span_id": "00f067aa0ba902b7", "contains": "boom"}

func sampleValue(p string) string {
	fp := fusionParams[p]
	switch {
	case sampleParams[p] != "":
		return sampleParams[p]
	case fp.Example != "":
		return fp.Example
	case len(fp.Enum) > 0:
		return fp.Enum[0]
	case fp.Type == "boolean":
		return "true"
	}
	return "1"
}

// readEverything calls every structured read of the data API, as auth, and returns what the stores were asked. With all it gives
// every parameter a route takes a value (leaving out one that would make another do nothing); without it, only the time range.
func readEverything(t *testing.T, d *dataRig, auth opt, all bool) []string {
	t.Helper()
	before := d.stores.count()
	for _, op := range fusionOps(d.a) {
		if op.Method != "GET" || op.Open || strings.HasPrefix(op.Path, "/metrics/query") || op.Path == "/status" {
			continue // a raw query is sent as written, and cannot be built from the vocabulary
		}
		path := fusionAPIPath + strings.NewReplacer("{id}", testTraceID, "{name}", "shop").Replace(op.Path)
		if strings.HasPrefix(op.Path, "/services/") {
			path = fusionAPIPath + "/services/cart"
		}
		q := url.Values{}
		for _, p := range op.Params {
			if !all && p != "from" && p != "to" {
				continue
			}
			if !slices.ContainsFunc(op.excluded(p), q.Has) && p != "query" && p != "q" {
				q.Set(p, sampleValue(p))
			}
		}
		if r := d.get(path+"?"+q.Encode(), auth); r.Code >= 500 {
			t.Errorf("%s: %d %s", op.Path, r.Code, r.Body.String())
		}
	}
	return d.stores.asked[before:]
}

// logFields are the log record's own fields (structured metadata), which a pipeline filter may name besides the vocabulary.
var logFields = []string{"trace_id", "span_id", "severity_text"}

// checkAsked holds one request the API sent a store to the vocabulary: PromQL names its labels, a LogQL stream selector names
// index labels only (and one that an empty value satisfies is refused by Loki), a pipeline filter names any label, and TraceQL
// names resource attributes.
func checkAsked(t *testing.T, info map[string]bool, asked string) {
	t.Helper()
	names := vocabNames()
	store, rest, _ := strings.Cut(asked, " ")
	u, _ := url.Parse(rest)
	q := u.Query()
	asked, _ = url.QueryUnescape(asked) // as the store reads it, for the message
	label := func(l string) {
		if _, ok := names[l]; !ok && !info[l] && l != "__name__" && !slices.Contains(logFields, l) {
			t.Errorf("%s: %q is not a label of the vocabulary", asked, l)
		}
	}
	switch store {
	case "prom":
		for _, e := range append(q["match[]"], q.Get("query")) {
			for _, m := range matchersIn(e) {
				label(m.label)
			}
			for _, l := range listedLabels(e) {
				label(l)
			}
		}
		if l, ok := strings.CutPrefix(u.Path, "/api/v1/label/"); ok {
			label(strings.TrimSuffix(l, "/values"))
		}
	case "loki":
		for _, e := range append(q["match[]"], q.Get("query")) {
			sels := streamSelectors(e)
			if len(sels) == 0 {
				continue
			}
			ms := matchersIn(sels[0])
			for _, m := range ms {
				if l, ok := names[m.label]; !ok || !l.Index {
					t.Errorf("%s: the stream selector names %q, which is not a Loki index label of the vocabulary", asked, m.label)
				}
			}
			if !canSelect(ms) {
				t.Errorf("%s: no matcher of the stream selector excludes an empty value; Loki refuses it", asked)
			}
			for _, m := range matchersIn(e[strings.Index(e, "}")+1:]) {
				label(m.label)
			}
		}
	case "tempo":
		for _, e := range []string{q.Get("q"), u.Path} {
			for _, m := range traceAttrRE.FindAllStringSubmatch(e, -1) {
				if !slices.ContainsFunc(fusionapi.Vocabulary, func(l fusionapi.Label) bool { return l.Attr == m[1] }) {
					t.Errorf("%s: %q is not an attribute of the vocabulary", asked, m[1])
				}
			}
		}
	}
}

func doctorQueries(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	limited, _ := d.mint(t, map[string]any{"name": "shop", "namespaces": []string{"shop"}, "clusters": []string{"cl-1"}})
	info := infoLabels(t)

	t.Run("every query the API builds names the vocabulary's labels and can select something", func(t *testing.T) {
		for name, run := range map[string]func() []string{
			"a token limited to a namespace and a cluster, every filter given": func() []string { return readEverything(t, d, bearer(limited), true) },
			"an administrator, every filter given":                             func() []string { return readEverything(t, d, withCookie(d.admin), true) },
			"an administrator, nothing narrowed":                               func() []string { return readEverything(t, d, withCookie(d.admin), false) },
		} {
			asked := run()
			stores := map[string]int{}
			for _, a := range asked {
				stores[strings.SplitN(a, " ", 2)[0]]++
				checkAsked(t, info, a)
			}
			if stores["prom"] == 0 || stores["loki"] == 0 || stores["tempo"] == 0 {
				t.Errorf("%s: the reads asked %v; a store nobody asked is one nothing was checked against", name, stores)
			}
		}
	})

	// One workspace, three readers: the graph (applicationsIn, which recordApplicationsGraph links), the API (appGroups) and
	// Grafana (the info series pushed to Prometheus) must name the same services in each application, accepted hints
	// included. The pushed series are what the dashboards join on.
	var pushed []map[string]string
	t.Run("the graph, the API and the info series have the same members, accepted hints included", func(t *testing.T) {
		doc := StateDoc{Topology: model.Topology{Services: []model.Service{
			{ID: "sv-ref", Name: "ref", Namespace: "shop", ClusterID: "cl-1"},
			{ID: "sv-inline", Name: "inline", Namespace: "shop", ClusterID: "cl-1"},
			{ID: "sv-hint", Name: "hint", Namespace: "shop", ClusterID: "cl-1", ApplicationHint: "app-1"},
		}}}
		ws := []byte(`{"schemaVersion":4,"applications":[{"id":"app-1","name":"Shop"}],
			"refs":{"sv-ref":{"kind":"service","applicationId":"app-1"}},
			"services":[{"id":"sv-inline","source":"manual","applicationId":"app-1","name":"inline"}]}`)
		groups := appGroups(ws, doc)
		apps, err := applicationsIn(ws, hintsOf(doc))
		if err != nil || len(apps) != 1 || len(groups) != 1 {
			t.Fatalf("apps %v groups %v err %v", apps, groups, err)
		}
		var api []string
		for _, m := range groups[0].Members {
			api = append(api, m.ID)
		}
		slices.Sort(api)
		if want := []string{"sv-hint", "sv-inline", "sv-ref"}; !slices.Equal(apps[0].ServiceIDs, want) || !slices.Equal(api, want) {
			t.Fatalf("graph %v, API %v, want %v", apps[0].ServiceIDs, api, want)
		}
		var body []byte
		prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body, _ = io.ReadAll(r.Body) }))
		defer prom.Close()
		push := newDataRig(t)
		push.a.Fusion.Data = &fusionapi.Client{Prometheus: prom.URL}
		push.a.extras = appExtras{groups}
		k := push.a.Fusion.Kube.(*fakeKube)
		for _, n := range fusionNames {
			k.replicas[n] = 1
		}
		k.allReady()
		if n, cut, err := push.a.PushApplicationInfo(context.Background()); err != nil || n != 3 || cut {
			t.Fatalf("pushed %d series, cut %v, err %v", n, cut, err)
		}
		pushed = decodeAppInfo(t, body)
		var ids []string
		for _, s := range pushed {
			ids = append(ids, s["service_id"])
		}
		slices.Sort(ids)
		if !slices.Equal(ids, api) {
			t.Errorf("Grafana's info series name %v, the API %v", ids, api)
		}
	})

	t.Run("an application is narrowed by the labels the dashboards join it on", func(t *testing.T) {
		dims := []string{vocName(t, "service.name"), vocName(t, "k8s.namespace.name"), vocName(t, "continuum.cluster.id")}
		slices.Sort(dims)
		before := d.stores.count()
		for _, p := range []string{"/metrics/series", "/logs", "/traces"} {
			if r := d.get(fusionAPIPath+p+"?application=shop", withCookie(d.admin)); r.Code != 200 {
				t.Fatalf("%s: %d %s", p, r.Code, r.Body.String())
			}
		}
		for _, a := range d.stores.asked[before:] {
			_, rest, _ := strings.Cut(a, " ")
			u, _ := url.Parse(rest)
			var e string
			for _, k := range []string{"match[]", "query", "q"} {
				e += u.Query().Get(k)
			}
			got := map[string]bool{}
			for _, m := range matchersIn(e) {
				if _, ok := vocabNames()[m.label]; ok {
					got[m.label] = true
				}
			}
			for _, m := range traceAttrRE.FindAllStringSubmatch(e, -1) {
				got[strings.ReplaceAll(m[1], ".", "_")] = true
			}
			if len(got) > 0 && !(len(got) == len(dims) && got[dims[0]] && got[dims[1]] && got[dims[2]]) {
				t.Errorf("%s: the application is selected by %v, want %v", a, got, dims)
			}
		}
		queries, _ := dashboards(t)
		join := regexp.MustCompile(`\bon\s*\(([^)]+)\)\s*(?:group_left\s*(?:\([^)]*\))?\s*)?` + fusionapi.AppInfoMetric)
		infoSel := regexp.MustCompile(fusionapi.AppInfoMetric + `\{([^}]*)\}`)
		joins := 0
		for _, q := range queries {
			for _, m := range join.FindAllStringSubmatch(q.expr, -1) {
				joins++
				var on []string
				for _, l := range strings.Split(m[1], ",") {
					on = append(on, strings.TrimSpace(l))
				}
				slices.Sort(on)
				if !slices.Equal(on, dims) {
					t.Errorf("%s joins on %v, but the API narrows an application by %v: %s", q.file, on, dims, q.expr)
				}
			}
			for _, m := range infoSel.FindAllStringSubmatch(q.expr, -1) {
				for _, mt := range matchersIn(m[1]) {
					if len(pushed) > 0 && pushed[0][mt.label] == "" {
						t.Errorf("%s selects the info series by %q, which the series do not carry (%v): %s", q.file, mt.label, pushed[0], q.expr)
					}
				}
			}
		}
		if joins == 0 {
			t.Error("no dashboard joins on the info series; the check looked at nothing")
		}
		for _, l := range dims {
			if len(pushed) > 0 && pushed[0][l] == "" {
				t.Errorf("the info series do not carry %s, which both the API and the dashboards join on: %v", l, pushed[0])
			}
		}
	})

	t.Run("an application series that does not fit one push is reported as cut", func(t *testing.T) {
		var members []fusionapi.AppMember
		for i := 0; i < 5001; i++ {
			members = append(members, fusionapi.AppMember{ID: fmt.Sprintf("sv-%d", i), Name: fmt.Sprintf("svc-%d", i), Namespace: "shop", Cluster: "cl-1"})
		}
		push := newDataRig(t)
		push.a.extras = appExtras{[]fusionapi.AppGroup{{ID: "app-1", Name: "Big", Members: members}}}
		k := push.a.Fusion.Kube.(*fakeKube)
		for _, n := range fusionNames {
			k.replicas[n] = 1
		}
		k.allReady()
		if n, cut, err := push.a.PushApplicationInfo(context.Background()); err != nil || !cut || n != 5000 {
			t.Errorf("pushed %d series, cut %v, err %v: 5001 members must be reported as cut at 5000", n, cut, err)
		}
	})
}

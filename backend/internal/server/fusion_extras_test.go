package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/model"
	"continuum/internal/store"
)

func TestYourPromQLIsExpandedPerResourceAndReachesPrometheus(t *testing.T) {
	d := newDataRig(t)
	q := `errs=sum(rate(http_errors_total{service_name="${service}",k8s_namespace_name="${namespace}"}[1m]))`
	r := d.get("/api/v1/fusion/traces/"+testTraceID+"?promql="+urlEscape(q)+"&promql_spans=true", withCookie(d.admin))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	src := j["sources"].(map[string]any)
	if src["promql"] != "ok" || src["logs"] != "not requested" || src["metrics"] != "not requested" {
		t.Fatalf("sources %v", src)
	}
	if !d.stores.askedAbout(urlEscape(`http_errors_total{service_name="cart",k8s_namespace_name="shop"}`)) ||
		!d.stores.askedAbout(urlEscape(`http_errors_total{service_name="audit",k8s_namespace_name="secret"}`)) {
		t.Fatalf("expanded queries were not sent: %v", d.stores.asked)
	}
	res := j["resources"].([]any)[0].(map[string]any)
	qs := res["queries"].([]any)
	if len(qs) != 1 || qs[0].(map[string]any)["name"] != "errs" || !strings.Contains(qs[0].(map[string]any)["query"].(string), `service_name="`) {
		t.Fatalf("resource queries: %v", res)
	}

	// The batch body takes an object of name to expression, and a list.
	before := d.stores.count()
	for _, body := range []map[string]any{
		{"ids": []string{testTraceID}, "promql": map[string]string{"errs": `up{service_name="${service}"}`}, "include": []string{"none"}},
		{"ids": []string{testTraceID}, "promql": []string{`errs=up{service_name="${service}"}`, `all=sum(up)`}},
	} {
		r := d.post("/api/v1/fusion/traces/batch", body, withCookie(d.admin))
		if r.Code != 200 {
			t.Fatalf("%v: %d %s", body, r.Code, r.Body.String())
		}
		tr := r.json(t)["results"].([]any)[0].(map[string]any)["trace"].(map[string]any)
		if tr["sources"].(map[string]any)["promql"] != "ok" {
			t.Fatalf("batch sources %v", tr["sources"])
		}
	}
	if d.stores.count() == before {
		t.Fatal("batch read nothing")
	}
	// A query with a variable that does not exist is the caller's mistake, said before any read.
	before = d.stores.count()
	for _, bad := range []string{`/api/v1/fusion/traces/` + testTraceID + `?promql=` + urlEscape(`x=up{a="${nope}"}`), `/api/v1/fusion/traces/` + testTraceID + `?promql=up`} {
		if r := d.get(bad, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d", bad, r.Code)
		}
	}
	if r := d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": []string{testTraceID}, "promql": 5}, withCookie(d.admin)); r.Code != 400 {
		t.Errorf("a number as promql: %d", r.Code)
	}
	if d.stores.count() != before {
		t.Fatal("a refused request reached a store")
	}
}

func TestYourPromQLNeedsAnUnrestrictedToken(t *testing.T) {
	d := newDataRig(t)
	limited, _ := d.mint(t, map[string]any{"name": "shop", "namespaces": []string{"shop"}})
	unlimited, _ := d.mint(t, map[string]any{"name": "all"})
	path := "/api/v1/fusion/traces/" + testTraceID + "?promql=" + urlEscape(`n=up{service_name="${service}"}`)

	before := d.stores.count()
	j := d.get(path, bearer(limited)).json(t)
	if j["sources"].(map[string]any)["promql"] != "not allowed" {
		t.Fatalf("%v", j["sources"])
	}
	if d.stores.askedAbout(urlEscape(`up{service_name=`)) {
		t.Fatalf("a limited token's query reached Prometheus: %v", d.stores.asked[before:])
	}
	if j := d.get(path, bearer(unlimited)).json(t); j["sources"].(map[string]any)["promql"] != "ok" {
		t.Fatalf("%v", j["sources"])
	}
}

func TestTopologyAndChangesAreOfferedAndReportedHonestly(t *testing.T) {
	d := newDataRig(t)
	j := d.get("/api/v1/fusion/traces/"+testTraceID+"?include=topology,changes", withCookie(d.admin)).json(t)
	src := j["sources"].(map[string]any)
	if src["topology"] != "ok" || src["changes"] != "ok" || src["logs"] != "not requested" {
		t.Fatalf("sources %v warnings %v", src, j["warnings"])
	}
	// No service of the fixture is in the (empty) topology: said, not silent.
	if !strings.Contains(strings.Join(anyStrings(j["warnings"]), " "), "topology") || j["joins"].(map[string]any)["topology"] == nil {
		t.Fatalf("%v", j)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, e := range l {
		out = append(out, e.(string))
	}
	return out
}

func TestTheTopologyViewCarriesWhatTheFusedReadJoins(t *testing.T) {
	doc := StateDoc{GeneratedAt: "2026-10-05T12:00:00Z", Topology: model.Topology{
		Services: []model.Service{{ID: "s1", Name: "cart", Namespace: "shop", ClusterID: "cl-1", Kind: "Deployment", Replicas: 3, ReadyReplicas: 2, Restarts: 5, Labels: map[string]string{"app": "cart"}}},
		Dependencies: []model.Dependency{{From: "s1", FromKind: "service", To: "x1", ToKind: "external", Protocol: "tcp", Port: 443, Confidence: "high", Noise: ""},
			{From: "s1", FromKind: "service", To: "s9", ToKind: "service", Noise: "dns"}},
		ExternalEndpoints: []model.ExternalEndpoint{{ID: "x1", Host: "198.51.100.7", Port: 443}, {ID: "x2", Host: "h", Name: "GitHub"}},
	}}
	v := topologyView(doc)
	if len(v.Services) != 1 || v.Services[0].Replicas != 3 || v.Services[0].Ready != 2 || v.Services[0].Restarts != 5 || v.Services[0].Cluster != "cl-1" || v.Services[0].Labels["app"] != "cart" {
		t.Fatalf("%+v", v.Services)
	}
	if len(v.Links) != 2 || v.Links[0].Port != 443 || v.Links[1].Noise != "dns" || v.Externals["x1"] != "198.51.100.7:443" || v.Externals["x2"] != "GitHub" || v.At.IsZero() {
		t.Fatalf("%+v %v", v.Links, v.Externals)
	}
}

func TestChangesComeFromTheEventStoreNarrowedToTheClusters(t *testing.T) {
	a := newAdminRig(t)
	at := time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC)
	if err := a.st.AddEvents(a.ctx, "org-1", []store.Event{
		{At: at, Kind: "service-scaled", TargetKind: "service", TargetID: "s1", Name: "cart", ClusterID: "cl-1", ClusterName: "prod", Detail: "2 to 3", Cause: "autoscaler", Severity: "info"},
		{At: at.Add(time.Minute), Kind: "node-status", TargetKind: "node", TargetID: "n1", Name: "n1", ClusterID: "cl-2"},
		{At: at.Add(-3 * time.Hour), Kind: "old", TargetKind: "node", TargetID: "n1", ClusterID: "cl-1"},
	}); err != nil {
		t.Fatal(err)
	}
	ex := a.a.fusionExtras()
	if ex == nil {
		t.Fatal("a platform has extras")
	}
	got, err := ex.Changes(context.Background(), at.Add(-time.Hour), at.Add(time.Hour), []string{"cl-1"}, 100)
	if err != nil || len(got) != 1 || got[0].Kind != "service-scaled" || got[0].Cluster != "cl-1" || got[0].Cause != "autoscaler" || got[0].Detail != "2 to 3" {
		t.Fatalf("%+v %v", got, err)
	}
	if all, _ := ex.Changes(context.Background(), at.Add(-time.Hour), at.Add(time.Hour), nil, 100); len(all) != 2 {
		t.Fatalf("%+v", all)
	}
	if (&Admin{}).fusionExtras() != nil {
		t.Fatal("a server with no platform has no extras")
	}
	var _ fusionapi.Extras = fusionExtras{}
}

func urlEscape(s string) string { return url.QueryEscape(s) }

func TestTheBatchBodySchemaNamesEveryFusedOption(t *testing.T) {
	spec := fusionOpenAPI()
	props := spec["components"].(obj)["schemas"].(obj)["BatchRequest"].(map[string]any)["properties"].(map[string]any)
	for _, n := range fusionapi.FuseParamNames {
		if n == fusionapi.ParamFused {
			continue // a batch is always fused
		}
		if _, ok := props[n]; !ok {
			t.Errorf("BatchRequest does not describe %q", n)
		}
	}
}

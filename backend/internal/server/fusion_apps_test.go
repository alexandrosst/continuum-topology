package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/model"
)

// appExtras serves applications from memory in place of the platform.
type appExtras struct{ groups []fusionapi.AppGroup }

func (e appExtras) Topology(context.Context) (*fusionapi.TopologyView, error) {
	return &fusionapi.TopologyView{}, nil
}
func (e appExtras) Applications(context.Context) ([]fusionapi.AppGroup, error) { return e.groups, nil }
func (e appExtras) Changes(context.Context, time.Time, time.Time, []string, int) ([]fusionapi.ChangeEvent, error) {
	return nil, nil
}

func shopGroups() []fusionapi.AppGroup {
	return []fusionapi.AppGroup{
		{ID: "app-1", Name: "Shop", Members: []fusionapi.AppMember{
			{Name: "cart", Namespace: "shop", Cluster: "cl-1", Aliases: []string{"cart-app"}},
			{Name: "web", Namespace: "shop", Cluster: "cl-1"},
		}},
		{ID: "app-2", Name: "Pay", Members: []fusionapi.AppMember{{Name: "pay", Namespace: "pay", Cluster: "cl-2"}}},
	}
}

func TestTheApplicationFilterNarrowsEveryStructuredReadAndNothingElse(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}

	svc := url.QueryEscape(`service_name=~"cart|cart-app|web"`)
	for _, path := range []string{
		"/api/v1/fusion/metrics/series?application=shop",
		"/api/v1/fusion/metrics/names?application=app-1",
		"/api/v1/fusion/metrics/range?application=Shop&from=now-10m",
		"/api/v1/fusion/applications?application=SHOP",
		"/api/v1/fusion/logs?application=shop",
	} {
		before := d.stores.count()
		r := d.get(path, withCookie(d.admin))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		asked := strings.Join(d.stores.asked[before:], "\n")
		if !strings.Contains(asked, svc) && !strings.Contains(asked, `service_name%3D~%22cart%7Ccart-app%7Cweb%22`) {
			t.Errorf("%s did not carry the application's services:\n%s", path, asked)
		}
		if !strings.Contains(asked, "k8s_namespace_name") || !strings.Contains(asked, "continuum_cluster_id") {
			t.Errorf("%s did not carry the application's namespace and cluster:\n%s", path, asked)
		}
	}

	// traces: the search carries the services; the fused reads around its hits do not narrow to the application
	before := d.stores.count()
	r := d.get("/api/v1/fusion/traces?application=shop&fused=true&include=logs", withCookie(d.admin))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	asked := d.stores.asked[before:]
	if !strings.Contains(asked[0], "cart") || !strings.Contains(strings.Join(asked, "\n"), "tempo /api/search") {
		t.Fatalf("search = %v", asked)
	}
	for _, a := range asked[1:] {
		if strings.HasPrefix(a, "loki") && strings.Contains(a, "cart-app") {
			t.Errorf("the fused join was narrowed to the application: %s", a)
		}
	}
	if r.json(t)["results"] == nil {
		t.Fatalf("a fused search should still return results: %s", r.Body.String())
	}
}

func TestTheApplicationFilterRefusesWhatItCannotCarryAndWhatIsNotThere(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	for _, path := range []string{
		"/api/v1/fusion/logs?application=shop&query=" + url.QueryEscape(`{service_name="x"}`),
		"/api/v1/fusion/traces?application=shop&q=" + url.QueryEscape(`{ status = error }`),
		"/api/v1/fusion/metrics/query?application=shop&query=up",
	} {
		before := d.stores.count()
		if r := d.get(path, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d", path, r.Code)
		}
		if d.stores.count() != before {
			t.Errorf("%s reached a store", path)
		}
	}
	if r := d.get("/api/v1/fusion/logs?application=nope", withCookie(d.admin)); r.Code != 404 {
		t.Errorf("unknown application: %d", r.Code)
	}
	// a token that sees none of the application is told "not found", exactly as for one that does not exist
	other, _ := d.mint(t, map[string]any{"name": "pay-only", "namespaces": []string{"pay"}})
	if r := d.get("/api/v1/fusion/logs?application=shop", bearer(other)); r.Code != 404 {
		t.Errorf("outside the token's scope: %d", r.Code)
	}
	if r := d.get("/api/v1/fusion/logs?application=pay", bearer(other)); r.Code != 200 {
		t.Errorf("inside the token's scope: %d %s", r.Code, r.Body.String())
	}
}

func TestTheGroupsRouteListsApplicationsWithinTheCallersScope(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	j := d.get("/api/v1/fusion/groups", withCookie(d.admin)).json(t)
	gs := j["groups"].([]any)
	if len(gs) != 2 {
		t.Fatalf("%v", j)
	}
	shop := gs[0].(map[string]any)
	if shop["name"] != "Shop" || len(shop["serviceNames"].([]any)) != 3 || shop["namespaces"].([]any)[0] != "shop" || len(shop["services"].([]any)) != 2 {
		t.Fatalf("%v", shop)
	}
	tok, _ := d.mint(t, map[string]any{"name": "pay-only", "namespaces": []string{"pay"}})
	gs = d.get("/api/v1/fusion/groups", bearer(tok)).json(t)["groups"].([]any)
	if len(gs) != 1 || gs[0].(map[string]any)["name"] != "Pay" {
		t.Fatalf("a token sees only what is in its scope: %v", gs)
	}
	// no provider, no pretending
	d2 := newDataRig(t)
	if r := d2.get("/api/v1/fusion/groups", withCookie(d2.admin)); r.Code != 503 && r.Code != 200 {
		t.Fatalf("%d", r.Code)
	}
}

func TestApplicationGroupsAreBuiltFromTheWorkspaceAndTheTopology(t *testing.T) {
	doc := StateDoc{Topology: model.Topology{Services: []model.Service{
		{ID: "sv-1", Name: "cart-deploy", Namespace: "shop", ClusterID: "cl-1", Kind: "Deployment", Labels: map[string]string{"app": "cart", "app.kubernetes.io/name": "cart"}},
		{ID: "sv-2", Name: "other", Namespace: "x", ClusterID: "cl-1"},
	}}}
	ws := []byte(`{"schemaVersion":4,"applications":[{"id":"app-1","name":"Shop"},{"id":"app-3","name":"Gone"}],
		"services":[{"id":"sv-1","source":"manual","applicationId":"app-1","name":"cart-deploy"},{"id":"sv-missing","source":"manual","applicationId":"app-1","name":"x"},{"id":"sv-2","source":"manual","applicationId":"app-3","name":"other"}]}`)
	gs := appGroups(ws, doc)
	if len(gs) != 2 {
		t.Fatalf("%+v", gs)
	}
	var shop fusionapi.AppGroup
	for _, g := range gs {
		if g.ID == "app-1" {
			shop = g
		}
	}
	if len(shop.Members) != 1 || shop.Members[0].Name != "cart-deploy" || len(shop.Members[0].Aliases) != 1 || shop.Members[0].Aliases[0] != "cart" {
		t.Fatalf("a member the topology does not have is left out; the app label is an alias: %+v", shop)
	}
	if got := shop.ServiceNames(); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	v := topologyView(doc)
	annotateApplications(v, gs)
	if len(v.Services[0].Applications) != 1 || v.Services[0].Applications[0] != "Shop" || len(v.Services[1].Applications) != 1 {
		t.Fatalf("%+v", v.Services)
	}
	if appGroups(nil, doc) != nil || appGroups([]byte("junk"), doc) != nil {
		t.Fatal("no workspace, no groups")
	}
}

func TestTheApplicationSeriesAreWrittenOnlyWhileFusionRuns(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	k := d.a.Fusion.Kube.(*fakeKube)
	// FUSION off: nothing is written, and that is not an error
	if n, err := d.a.PushApplicationInfo(context.Background()); n != 0 || err != nil || d.stores.count() != 0 {
		t.Fatalf("n=%d err=%v asked=%v", n, err, d.stores.asked)
	}
	for _, n := range fusionNames {
		k.replicas[n] = 1
	}
	k.allReady()
	n, err := d.a.PushApplicationInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || !d.stores.askedAbout("prom /api/v1/otlp/v1/metrics") {
		t.Fatalf("n=%d asked=%v", n, d.stores.asked)
	}
	if n, err := (*Admin)(nil).PushApplicationInfo(context.Background()); n != 0 || err != nil {
		t.Fatal("no admin, no series")
	}
}

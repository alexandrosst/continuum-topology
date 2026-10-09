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
		"/api/v1/fusion/services?application=SHOP",
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

func TestApplicationsAreTheIkhnosOnesNotTheServicesTelemetryNames(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	j := d.get("/api/v1/fusion/applications", withCookie(d.admin)).json(t)
	apps := j["applications"].([]any)
	if len(apps) != 2 {
		t.Fatalf("%v", j)
	}
	pay, shop := apps[0].(map[string]any), apps[1].(map[string]any) // sorted by name
	if shop["name"] != "Shop" || shop["id"] != "app-1" || len(shop["services"].([]any)) != 2 || shop["namespaces"].([]any)[0] != "shop" {
		t.Fatalf("%v", shop)
	}
	if pay["name"] != "Pay" {
		t.Fatalf("%v", pay)
	}
	// "cart" has telemetry in the fake stores; the application it belongs to says so, and no service is listed as an application
	cart := shop["services"].([]any)[0].(map[string]any)
	if cart["name"] != "cart" || len(cart["signals"].([]any)) == 0 || len(shop["signals"].([]any)) == 0 {
		t.Fatalf("%v", cart)
	}
	for _, a := range apps {
		if n := a.(map[string]any)["name"]; n == "cart" || n == "audit" || n == "gateway" {
			t.Fatalf("a service is listed as an application: %v", a)
		}
	}
	// the services as telemetry names them, with the applications each is in
	sv := d.get("/api/v1/fusion/services", withCookie(d.admin)).json(t)["services"].([]any)
	found := false
	for _, e := range sv {
		m := e.(map[string]any)
		if m["name"] == "cart" {
			found = true
			if as := m["applications"].([]any); len(as) != 1 || as[0] != "Shop" {
				t.Fatalf("%v", m)
			}
		}
		if m["applications"] == nil {
			t.Fatalf("applications must be [] and not null: %v", m)
		}
	}
	if !found {
		t.Fatalf("%v", sv)
	}
	// one application and one service at a glance, by id or by name
	for path, name := range map[string]string{"/api/v1/fusion/applications/app-1": "Shop", "/api/v1/fusion/applications/shop": "Shop", "/api/v1/fusion/services/cart": "cart"} {
		o := d.get(path, withCookie(d.admin))
		if o.Code != 200 || o.json(t)["name"] != name {
			t.Errorf("%s: %d %s", path, o.Code, o.Body.String())
		}
	}
	if r := d.get("/api/v1/fusion/applications/cart", withCookie(d.admin)); r.Code != 404 {
		t.Errorf("a service is not an application: %d", r.Code)
	}
	// a limited token sees an application only through its services in its scope, and learns nothing about the rest
	tok, _ := d.mint(t, map[string]any{"name": "pay-only", "namespaces": []string{"pay"}})
	apps = d.get("/api/v1/fusion/applications", bearer(tok)).json(t)["applications"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["name"] != "Pay" {
		t.Fatalf("a token sees only what is in its scope: %v", apps)
	}
	for _, ref := range []string{"shop", "app-1", "nope"} {
		r := d.get("/api/v1/fusion/applications/"+ref, bearer(tok))
		if r.Code != 404 || !strings.Contains(r.Body.String(), "no application") {
			t.Errorf("%s: %d %s", ref, r.Code, r.Body.String())
		}
	}
}

func TestAFocusedAdministratorStillSeesTheRootOfATrace(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	plain := d.get("/api/v1/fusion/traces", withCookie(d.admin)).json(t)["traces"].([]any)[0].(map[string]any)
	focused := d.get("/api/v1/fusion/traces?application=shop", withCookie(d.admin)).json(t)["traces"].([]any)[0].(map[string]any)
	if plain["rootService"] == "" || focused["rootService"] != plain["rootService"] || focused["durationMs"] != plain["durationMs"] {
		t.Fatalf("an administrator must not lose the root by choosing an application:\nplain   %v\nfocused %v", plain, focused)
	}
}

func TestParametersTheRouteDoesNotTakeAreRefused(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	for path, why := range map[string]string{
		"/api/v1/fusion/logs?servce=cart":                              "a typo is told, not dropped",
		"/api/v1/fusion/logs?query=%7Ba%3D%22b%22%7D&service=cart":     "query overrides the filters",
		"/api/v1/fusion/logs?query=%7Ba%3D%22b%22%7D&application=shop": "query overrides the application",
		"/api/v1/fusion/traces?q=%7B%7D&status=error":                  "q overrides the filters",
		"/api/v1/fusion/metrics/names?name=up&metric=u.*":              "name and metric",
		"/api/v1/fusion/metrics/query?query=up&application=shop":       "the PromQL route takes no application",
		"/api/v1/fusion/applications?application=shop":                 "the list of applications takes none",
		"/api/v1/fusion/traces?omit=events":                            "shaping options need a fused read",
		"/api/v1/fusion/traces/" + testTraceID + "?span_service=audit": "so does a single trace",
		"/api/v1/fusion/logs?application=":                             "an empty application is a mistake, not no filter",
	} {
		if r := d.get(path, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s (%s): %d %s", path, why, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/fusion/logs?service=cart&severity=error", "/api/v1/fusion/traces?fused=true&omit=events",
		"/api/v1/fusion/metrics/names?metric=u.*", "/api/v1/fusion/prometheus/api/v1/query?query=up&anything=goes"} {
		if r := d.get(path, withCookie(d.admin)); r.Code == 400 {
			t.Errorf("%s: %s", path, r.Body.String())
		}
	}
}

func TestACookieDoesNotBeatTheTokenYouTyped(t *testing.T) {
	d := newDataRig(t)
	limited, _ := d.mint(t, map[string]any{"name": "shop-only", "namespaces": []string{"shop"}})
	j := d.get("/api/v1/fusion/status", withCookie(d.admin), bearer(limited)).json(t)["access"].(map[string]any)
	if j["kind"] != "token" || j["rawQueries"] != false {
		t.Fatalf("the explicit credential must decide: %v", j)
	}
	if r := d.get("/api/v1/fusion/status", withCookie(d.admin), bearer("cnf_garbage")); r.Code != 401 {
		t.Fatalf("a bad token is not rescued by the cookie: %d", r.Code)
	}
}

func TestApplicationGroupsAreBuiltFromTheWorkspaceAndTheTopology(t *testing.T) {
	doc := StateDoc{Topology: model.Topology{Services: []model.Service{
		{ID: "sv-1", Name: "cart-deploy", Namespace: "shop", ClusterID: "cl-1", Kind: "Deployment", Labels: map[string]string{"app": "cart", "app.kubernetes.io/name": "cart"}},
		{ID: "sv-2", Name: "other", Namespace: "x", ClusterID: "cl-1"},
	}}}
	ws := []byte(`{"schemaVersion":4,"applications":[{"id":"app-1","name":"Shop"},{"id":"app-3","name":"Gone"}],
		"refs":{"sv-gone":{"kind":"service","applicationId":"app-1"}},
		"services":[{"id":"sv-1","source":"manual","applicationId":"app-1","name":"cart-deploy"},{"id":"sv-hand","source":"manual","applicationId":"app-1","name":"billing","namespace":"pay","clusterId":"cl-9","kind":"Deployment"},{"id":"sv-2","source":"manual","applicationId":"app-3","name":"other"}]}`)
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
	// A discovered member is read from the topology (its app label is an alias); one written by hand is not in the topology
	// but says what it is and where it runs itself; one only a ref remembers, whose service is gone, is counted and left out.
	if len(shop.Members) != 2 || shop.Members[0].Name != "cart-deploy" || len(shop.Members[0].Aliases) != 1 || shop.Members[0].Aliases[0] != "cart" {
		t.Fatalf("%+v", shop)
	}
	if shop.Members[0].ID != "sv-1" || shop.Members[1].ID != "sv-hand" {
		t.Fatalf("a member says which service it is by id: %+v", shop.Members)
	}
	if m := shop.Members[1]; m.Name != "billing" || m.Namespace != "pay" || m.Cluster != "cl-9" || m.Kind != "Deployment" {
		t.Fatalf("a service written by hand matches telemetry by its own record: %+v", m)
	}
	if shop.Unresolved != 1 {
		t.Fatalf("one member could not be tied to a service: %+v", shop)
	}
	if got := shop.ServiceNames(); len(got) != 3 {
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

// A service is in an application when a member has its key, whichever id the member was written with: a record written by
// hand for a service that discovery also found names it in the application, and the discovered one shows it too.
func TestAServiceIsAnnotatedByItsKeyNotItsId(t *testing.T) {
	v := &fusionapi.TopologyView{Services: []fusionapi.TopoService{
		{ID: "sv-1", Name: "cart", Namespace: "shop", Cluster: "cl-1"}, {ID: "sv-2", Name: "cart", Namespace: "other", Cluster: "cl-1"}}}
	annotateApplications(v, []fusionapi.AppGroup{
		{ID: "app-1", Name: "Shop", Members: []fusionapi.AppMember{{ID: "svc-by-hand", Name: "cart", Namespace: "shop", Cluster: "cl-1"}, {ID: "sv-1", Name: "cart", Namespace: "shop", Cluster: "cl-1"}}},
		{ID: "app-2", Name: "Pay", Members: []fusionapi.AppMember{{ID: "sv-1", Name: "cart", Namespace: "shop", Cluster: "cl-1"}}}})
	if got := v.Services[0].Applications; len(got) != 2 || got[0] != "Shop" || got[1] != "Pay" {
		t.Errorf("%v", got)
	}
	if got := v.Services[1].Applications; len(got) != 0 {
		t.Errorf("same name, other namespace: %v", got)
	}
}

func TestTheApplicationSeriesAreWrittenOnlyWhileFusionRuns(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	k := d.a.Fusion.Kube.(*fakeKube)
	// FUSION off: nothing is written, and that is not an error
	if n, _, err := d.a.PushApplicationInfo(context.Background()); n != 0 || err != nil || d.stores.count() != 0 {
		t.Fatalf("n=%d err=%v asked=%v", n, err, d.stores.asked)
	}
	for _, n := range fusionNames {
		k.replicas[n] = 1
	}
	k.allReady()
	n, _, err := d.a.PushApplicationInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || !d.stores.askedAbout("prom /api/v1/otlp/v1/metrics") {
		t.Fatalf("n=%d asked=%v", n, d.stores.asked)
	}
	if n, _, err := (*Admin)(nil).PushApplicationInfo(context.Background()); n != 0 || err != nil {
		t.Fatal("no admin, no series")
	}
}

// Accepting a grouping suggestion in Ikhnos creates the application and nothing else: the services belong to it because their
// own labels pointed at it (the hint), which is not stored as a membership. A service a person put somewhere else is not
// taken, a deleted application is not an application, and a hint at an application that does not exist is nothing.
func TestAServiceBelongsToTheApplicationItsLabelsSuggestOnceThatApplicationExists(t *testing.T) {
	doc := StateDoc{Topology: model.Topology{Services: []model.Service{
		{ID: "sv-a", Name: "api", Namespace: "core", ClusterID: "cl-1", ApplicationHint: "app-core"},
		{ID: "sv-b", Name: "db", Namespace: "core", ClusterID: "cl-1", ApplicationHint: "app-core"},
		{ID: "sv-c", Name: "moved", Namespace: "core", ClusterID: "cl-1", ApplicationHint: "app-core"},
		{ID: "sv-d", Name: "ghost", Namespace: "x", ClusterID: "cl-1", ApplicationHint: "app-unknown"},
		{ID: "sv-e", Name: "old", Namespace: "x", ClusterID: "cl-1", ApplicationHint: "app-old"},
	}}}
	ws := []byte(`{"schemaVersion":4,"applications":[{"id":"app-core","name":"app-core"},{"id":"app-other","name":"Other"},{"id":"app-old","name":"Old","deletedAt":"2026-10-01T00:00:00Z"}],
		"refs":{"sv-c":{"kind":"service","applicationId":"app-other"}}}`)
	got := map[string][]string{}
	for _, g := range appGroups(ws, doc) {
		for _, m := range g.Members {
			got[g.ID] = append(got[g.ID], m.Name)
		}
		if g.ID == "app-old" {
			t.Errorf("a deleted application is listed: %+v", g)
		}
	}
	if len(got["app-core"]) != 2 || got["app-core"][0] != "api" || got["app-core"][1] != "db" {
		t.Errorf("app-core = %v, want api and db (its labels point at it); moved was put elsewhere", got["app-core"])
	}
	if len(got["app-other"]) != 1 || got["app-other"][0] != "moved" {
		t.Errorf("app-other = %v", got["app-other"])
	}
}

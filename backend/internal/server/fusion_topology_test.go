package server

import (
	"context"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/history"
	"continuum/internal/model"
)

// topoExtras serves a topology from memory, and records what it was asked for.
type topoExtras struct {
	appExtras
	view   *fusionapi.TopologyView
	events []fusionapi.ChangeEvent
	ids    []string
	since  time.Time
	until  time.Time
	then   func(at time.Time) (*fusionapi.TopologyView, []fusionapi.AppMember, error) // set: the server remembers the past
}

func (e *topoExtras) Topology(context.Context) (*fusionapi.TopologyView, error) { return e.view, nil }
func (e *topoExtras) Changes(_ context.Context, since, until time.Time, _, ids []string, _ int) ([]fusionapi.ChangeEvent, error) {
	e.since, e.until, e.ids = since, until, ids
	return e.events, nil
}

type rememberingExtras struct{ *topoExtras }

func (e rememberingExtras) TopologyAt(_ context.Context, _ string, at time.Time) (*fusionapi.TopologyView, []fusionapi.AppMember, error) {
	return e.then(at)
}

func shopView() *fusionapi.TopologyView {
	return &fusionapi.TopologyView{At: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Services: []fusionapi.TopoService{
			{ID: "s-cart", Name: "cart", Namespace: "shop", Cluster: "cl-1", Replicas: 2, Ready: 2},
			{ID: "s-web", Name: "web", Namespace: "shop", Cluster: "cl-1", Replicas: 1, Ready: 1},
			{ID: "s-pay", Name: "pay", Namespace: "pay", Cluster: "cl-2", Replicas: 1, Ready: 1},
		},
		Links: []fusionapi.TopoLink{
			{ID: "d1", From: "s-web", To: "s-cart", FromKind: "service", ToKind: "service", Port: 8080},
			{ID: "d2", From: "s-cart", To: "s-pay", FromKind: "service", ToKind: "service", Traffic: fusionapi.LinkTraffic{Bytes: 10}},
			{ID: "d3", From: "s-cart", To: "x-1", FromKind: "service", ToKind: "external"},
		},
		Externals: map[string]string{"x-1": "GitHub"}}
}

func topoRig(t *testing.T, ex fusionapi.Extras) *dataRig {
	d := newDataRig(t)
	d.a.extras = ex
	return d
}

func TestTheTopologyEndpointAnswersLiveFromTheCachedTopology(t *testing.T) {
	groups := shopGroups()
	groups[0].Members[0].ID, groups[0].Members[1].ID = "s-cart", "s-web"
	ex := &topoExtras{appExtras: appExtras{groups}, view: shopView(), events: []fusionapi.ChangeEvent{{Time: time.Now(), Kind: "dependency-seen", TargetKind: "dependency", TargetID: "d1"}}}
	d := topoRig(t, ex)

	before := d.stores.count()
	r := d.get("/api/v1/fusion/applications/Shop/topology", withCookie(d.admin))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if d.stores.count() != before {
		t.Error("the topology read telemetry stores")
	}
	j := r.json(t)
	if j["source"] != "live" || j["application"].(map[string]any)["id"] != "app-1" || len(j["services"].([]any)) != 2 || len(j["links"].([]any)) != 3 ||
		len(j["neighbours"].([]any)) != 1 || len(j["externals"].([]any)) != 1 || len(j["changes"].([]any)) != 1 || j["snapshotAt"] != nil {
		t.Fatalf("%v", j)
	}
	if !slices.Contains(ex.ids, "app-1") || !slices.Contains(ex.ids, "d1") || !slices.Contains(ex.ids, "s-cart") {
		t.Errorf("events were asked for %v", ex.ids)
	}
	if ex.until.Sub(ex.since) != 24*time.Hour {
		t.Errorf("window %v", ex.until.Sub(ex.since))
	}
	// by id, with a window; the machinery only when asked for
	if r := d.get("/api/v1/fusion/applications/app-1/topology?window=6h&noise=1", withCookie(d.admin)); r.Code != 200 || ex.until.Sub(ex.since) != 6*time.Hour {
		t.Errorf("%d %v", r.Code, ex.until.Sub(ex.since))
	}
	for _, bad := range []string{"window=8d", "window=soon", "noise=maybe", "at=yesterday", "since=1h"} {
		if r := d.get("/api/v1/fusion/applications/Shop/topology?"+bad, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d", bad, r.Code)
		}
	}
}

func TestTheTopologyEndpointDoesNotLetTheScopeLearnWhatItMayNotSee(t *testing.T) {
	groups := append(shopGroups(), fusionapi.AppGroup{ID: "app-3", Name: "shop", Members: []fusionapi.AppMember{{Name: "x", Namespace: "x", Cluster: "cl-1"}}})
	groups[0].Members[0].ID, groups[0].Members[1].ID = "s-cart", "s-web"
	ex := &topoExtras{appExtras: appExtras{groups}, view: shopView()}
	d := topoRig(t, ex)

	// two applications of one name are not guessed between
	if r := d.get("/api/v1/fusion/applications/shop/topology", withCookie(d.admin)); r.Code != 400 {
		t.Errorf("an ambiguous name: %d", r.Code)
	}
	if r := d.get("/api/v1/fusion/applications/app-1/topology", withCookie(d.admin)); r.Code != 200 {
		t.Fatalf("by id: %d", r.Code)
	}
	if r := d.get("/api/v1/fusion/applications/nope/topology", withCookie(d.admin)); r.Code != 404 {
		t.Errorf("unknown: %d", r.Code)
	}
	// a token that sees none of the application is told "not found", exactly as for one that does not exist
	payOnly, _ := d.mint(t, map[string]any{"name": "pay-only", "namespaces": []string{"pay"}})
	if r := d.get("/api/v1/fusion/applications/app-1/topology", bearer(payOnly)); r.Code != 404 {
		t.Errorf("outside the token's scope: %d", r.Code)
	}
	shopOnly, _ := d.mint(t, map[string]any{"name": "shop-only", "namespaces": []string{"shop"}})
	r := d.get("/api/v1/fusion/applications/app-1/topology", bearer(shopOnly))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	body := r.Body.String()
	if strings.Contains(body, "pay/pay") || strings.Contains(body, "GitHub") || strings.Contains(body, "x-1") {
		t.Errorf("the token learnt of what it may not see: %s", body)
	}
	if slices.Contains(ex.ids, "d2") || slices.Contains(ex.ids, "d3") {
		t.Errorf("asked for the events of links the token may not see: %v", ex.ids)
	}
}

func TestTheTopologyEndpointAnswersHistoryOnlyWhereHistoryIsKept(t *testing.T) {
	groups := shopGroups()
	at := "2026-10-05T11:30:00Z"
	// no graph: a clear answer, not an empty one
	d := topoRig(t, &topoExtras{appExtras: appExtras{groups}, view: shopView()})
	r := d.get("/api/v1/fusion/applications/Shop/topology?at="+url.QueryEscape(at), withCookie(d.admin))
	if r.Code != 404 || !strings.Contains(r.Body.String(), "history is not enabled") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}

	// with one: the topology and the members of that time, and the events before it
	base := &topoExtras{appExtras: appExtras{groups}, view: shopView()}
	then := time.Date(2026, 10, 5, 11, 29, 0, 0, time.UTC)
	hist := rememberingExtras{base}
	base.then = func(time.Time) (*fusionapi.TopologyView, []fusionapi.AppMember, error) {
		v := shopView()
		v.At = then
		return v, []fusionapi.AppMember{{ID: "s-cart", Name: "cart", Namespace: "shop", Cluster: "cl-1"}, {ID: "s-pay", Name: "pay", Namespace: "pay", Cluster: "cl-2"}}, nil
	}
	d = topoRig(t, hist)
	r = d.get("/api/v1/fusion/applications/Shop/topology?at="+url.QueryEscape(at), withCookie(d.admin))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	if j["source"] != "history" || j["asOf"] != at || j["snapshotAt"] != "2026-10-05T11:29:00Z" || len(j["services"].([]any)) != 2 {
		t.Fatalf("%v", j)
	}
	at0, _ := time.Parse(time.RFC3339, at)
	if !base.until.Equal(at0) || base.until.Sub(base.since) != 24*time.Hour {
		t.Errorf("events from %v to %v", base.since, base.until)
	}
	// the caller sees the application as it was, through its own scope
	payOnly, _ := d.mint(t, map[string]any{"name": "pay-only", "namespaces": []string{"pay"}})
	if r := d.get("/api/v1/fusion/applications/Pay/topology?at="+url.QueryEscape(at), bearer(payOnly)); r.Code != 200 {
		t.Errorf("%d %s", r.Code, r.Body.String())
	}
	// an application the caller cannot see now does not exist for it, whatever it held then
	if r := d.get("/api/v1/fusion/applications/Shop/topology?at="+url.QueryEscape(at), bearer(payOnly)); r.Code != 404 {
		t.Errorf("%d %s", r.Code, r.Body.String())
	}
}

// With Neo4j the past is the graph's: the topology of the newest recording and the members that application had then.
func TestTheTopologyOfAnApplicationInThePastComesFromTheGraph(t *testing.T) {
	a, gs := graphRig(t)
	_, org := a.register(t, "alice", "Alice Lab")
	c := a.core.ForOrg(org)
	c.AppHints = func(context.Context) map[string]string { return nil }
	t0 := a.now.Add(-time.Hour)
	topo := model.Topology{
		Clusters: []model.Cluster{{ID: "c-1", Name: "edge", Status: "connected"}},
		Services: []model.Service{
			{ID: "svc-a", ClusterID: "c-1", Name: "a", Namespace: "n", Kind: "Deployment", Replicas: 1, ReadyReplicas: 1},
			{ID: "svc-b", ClusterID: "c-1", Name: "b", Namespace: "n", Kind: "Deployment", Replicas: 1, ReadyReplicas: 1},
			{ID: "svc-c", ClusterID: "c-1", Name: "c", Namespace: "n", Kind: "Deployment", Replicas: 1, ReadyReplicas: 0}},
		Dependencies: []model.Dependency{{ID: "dep-ab", From: "svc-a", FromKind: "service", To: "svc-b", ToKind: "service", Protocol: "tcp", Port: 80, Bytes: 4096, RttMs: 3},
			{ID: "dep-ac", From: "svc-a", FromKind: "service", To: "svc-c", ToKind: "service", Protocol: "tcp"}},
	}
	b, _, _ := history.Encode(history.Compact(topo))
	if err := gs.AddHistory(a.ctx, org, t0, b); err != nil {
		t.Fatal(err)
	}
	gs.Sync(a.ctx)
	*a.now = t0.Add(time.Minute)
	if _, err := c.SaveWorkspace(a.ctx, "ann", 0, []byte(`{"schemaVersion":4,"applications":[{"id":"app-1","name":"One"}],"refs":{"svc-a":{"kind":"service","applicationId":"app-1"},"svc-b":{"kind":"service","applicationId":"app-1"}}}`)); err != nil {
		t.Fatal(err)
	}
	// later, svc-b leaves the application
	*a.now = t0.Add(30 * time.Minute)
	if _, err := c.SaveWorkspace(a.ctx, "ann", 1, []byte(`{"schemaVersion":4,"applications":[{"id":"app-1","name":"One"}],"refs":{"svc-a":{"kind":"service","applicationId":"app-1"}}}`)); err != nil {
		t.Fatal(err)
	}

	members := func(at time.Time) []string {
		t.Helper()
		v, ms, err := topologyAt(a.ctx, c, "app-1", at)
		if err != nil {
			t.Fatal(err)
		}
		if !v.At.Equal(t0.UTC().Truncate(time.Second)) || len(v.Links) != 2 || v.Links[0].Traffic.Bytes+v.Links[1].Traffic.Bytes != 4096 {
			t.Errorf("the topology of the recording: %+v", v)
		}
		var ids []string
		for _, m := range ms {
			ids = append(ids, m.ID+"="+m.Name+"/"+m.Namespace+"/"+m.Cluster)
		}
		slices.Sort(ids)
		return ids
	}
	if got := members(t0.Add(10 * time.Minute)); !reflect.DeepEqual(got, []string{"svc-a=a/n/c-1", "svc-b=b/n/c-1"}) {
		t.Errorf("then: %v", got)
	}
	if got := members(t0.Add(40 * time.Minute)); !reflect.DeepEqual(got, []string{"svc-a=a/n/c-1"}) {
		t.Errorf("after svc-b left: %v", got)
	}
	if _, _, err := topologyAt(a.ctx, c, "app-1", t0.Add(-time.Hour)); err == nil || !strings.Contains(err.Error(), "nothing was recorded") {
		t.Errorf("before anything was recorded: %v", err)
	}
	// without a graph there is no past
	plain := newAdminRig(t)
	if _, _, err := topologyAt(plain.ctx, plain.core, "app-1", t0); err == nil || !strings.Contains(err.Error(), "history is not enabled") {
		t.Errorf("no graph: %v", err)
	}
}

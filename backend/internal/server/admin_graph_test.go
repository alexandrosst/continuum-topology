package server

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"continuum/internal/graph"
	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/store"
)

// graphRig is the admin API on a store that keeps memory in a real Neo4j (CONTINUUM_TEST_NEO4J).
func graphRig(t *testing.T) (*adminRig, *graph.Store) {
	t.Helper()
	u := os.Getenv("CONTINUUM_TEST_NEO4J")
	if u == "" {
		t.Skip("set CONTINUUM_TEST_NEO4J to run the Neo4j tests")
	}
	c, err := graph.NewClient(graph.Config{URL: u, User: "neo4j", Password: os.Getenv("CONTINUUM_TEST_NEO4J_PASSWORD")})
	if err != nil {
		t.Fatal(err)
	}
	db := graph.NewDB(c)
	e := newEnvBare(t)
	gs := graph.Wrap(e.st, db, slog.Default())
	e.base.Store = gs
	e.core = e.base.ForOrg("org-1")
	gs.Sync(e.ctx)
	if !gs.Ready() {
		t.Fatal("graph not ready")
	}
	t.Cleanup(func() {
		orgs, _ := e.st.ListOrgs(e.ctx)
		for _, o := range orgs {
			_ = db.PurgeTenant(e.ctx, o.ID)
		}
	})
	return adminRigOn(t, e), gs
}

func snap(name string, replicas int32) []byte {
	t := model.Topology{
		Clusters:     []model.Cluster{{ID: "c-1", Name: "edge", Status: "connected"}},
		Services:     []model.Service{{ID: "svc-web", ClusterID: "c-1", Name: name, Replicas: replicas, ReadyReplicas: replicas, Status: "ready"}},
		Dependencies: []model.Dependency{{ID: "dep-1", From: "svc-web", FromKind: "service", To: "svc-web", ToKind: "service", Protocol: "tcp"}},
	}
	b, _, _ := history.Encode(history.Compact(t))
	return b
}

func TestHistoryTimelineAndAuditThroughTheAPIStayInsideTheirOrganisation(t *testing.T) {
	a, gs := graphRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	bob, bOrg := a.register(t, "bob", "Bob Works")
	t0 := time.Now().Add(-3 * time.Hour).Truncate(time.Second)

	// The same ids in both organisations, told apart only by content.
	for i := 0; i < 3; i++ {
		if err := gs.AddHistory(a.ctx, aOrg, t0.Add(time.Duration(i)*time.Hour), snap("web-of-alice", int32(i+1))); err != nil {
			t.Fatal(err)
		}
	}
	if err := gs.AddHistory(a.ctx, bOrg, t0, snap("web-of-bob", 9)); err != nil {
		t.Fatal(err)
	}
	if err := gs.AddEvents(a.ctx, aOrg, []store.Event{{At: t0.Add(time.Hour), Kind: "service-scaled", TargetKind: "service", TargetID: "svc-web", Name: "web-of-alice"}}); err != nil {
		t.Fatal(err)
	}
	gs.Sync(a.ctx) // projects the audit trail (organisation creation, sign-ups)

	// A moment in the past, chosen by the person: the estate as it was then.
	at := t0.Add(90 * time.Minute).UTC().Format(time.RFC3339)
	r := a.do("GET", org(aOrg, "history/snapshot?at="+at), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("snapshot: %d %s", r.Code, r.Body.String())
	}
	svc := r.json(t)["topology"].(map[string]any)["services"].([]any)[0].(map[string]any)
	if svc["name"] != "web-of-alice" || svc["replicas"].(float64) != 2 {
		t.Errorf("as of 90 minutes in: %v", svc)
	}
	// Bob asking the same question about his own organisation gets his own answer.
	r = a.do("GET", org(bOrg, "history/snapshot?at="+at), nil, withCookie(bob))
	if svc := r.json(t)["topology"].(map[string]any)["services"].([]any)[0].(map[string]any); svc["name"] != "web-of-bob" {
		t.Errorf("Bob saw %v", svc["name"])
	}
	// The index lists the moments, and the traffic endpoint answers from the same store.
	if pts := a.do("GET", org(aOrg, "history"), nil, withCookie(alice)).json(t)["points"].([]any); len(pts) != 3 {
		t.Errorf("index: %d points", len(pts))
	}
	if r := a.do("GET", org(aOrg, "history/traffic?hours=24"), nil, withCookie(alice)); r.Code != 200 || r.json(t)["snapshots"].(float64) != 3 {
		t.Errorf("traffic: %d %s", r.Code, r.Body.String())
	}

	// One record's life: three versions, with what changed, and the event about it.
	r = a.do("GET", org(aOrg, "timeline?kind=service&id=svc-web"), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("timeline: %d %s", r.Code, r.Body.String())
	}
	tl := r.json(t)
	if vs := tl["versions"].([]any); len(vs) != 3 {
		t.Errorf("versions: %d", len(vs))
	}
	if evs := tl["events"].([]any); len(evs) != 1 {
		t.Errorf("events: %d", len(evs))
	}
	if r := a.do("GET", org(bOrg, "timeline?kind=service&id=svc-web"), nil, withCookie(bob)); r.json(t)["versions"].([]any)[0].(map[string]any)["name"] != "web-of-bob" {
		t.Errorf("Bob's timeline shows someone else's record")
	}
	if c := a.do("GET", org(aOrg, "timeline?kind=nonsense&id=x"), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("unknown kind: %d", c)
	}
	if c := a.do("GET", org(aOrg, "timeline?kind=service"), nil, withCookie(alice)).Code; c != 400 {
		t.Errorf("missing id: %d", c)
	}

	// Who did what: administrators of the organisation only, and only their own organisation's trail.
	r = a.do("GET", org(aOrg, "audit"), nil, withCookie(alice))
	if r.Code != 200 || r.json(t)["source"] != "graph" {
		t.Fatalf("audit: %d %s", r.Code, r.Body.String())
	}
	rows := r.json(t)["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("no audit rows")
	}
	for _, x := range rows {
		if x.(map[string]any)["actor"] == "bob" {
			t.Errorf("Alice's trail contains Bob's action: %v", x)
		}
	}
	if r := a.do("GET", org(aOrg, "audit?actor=alice&action=org-created"), nil, withCookie(alice)); len(r.json(t)["rows"].([]any)) != 1 {
		t.Errorf("filtered audit: %s", r.Body.String())
	}

	// A viewer can read history but not the audit trail.
	vid := a.account(t, "vera")
	_ = a.st.AddMember(a.ctx, store.Membership{OrgID: aOrg, UserID: vid, Role: RoleViewer, CreatedAt: *a.now})
	vera := a.login(t, "vera", goodPW)
	if c := a.do("GET", org(aOrg, "audit"), nil, withCookie(vera)).Code; c != 403 {
		t.Errorf("viewer read the audit trail: %d", c)
	}
	if c := a.do("GET", org(aOrg, "timeline?kind=service&id=svc-web"), nil, withCookie(vera)).Code; c != 200 {
		t.Errorf("viewer timeline: %d", c)
	}

	// Storage status: members learn it works; only administrators see counts and reasons.
	if s := a.do("GET", org(aOrg, "storage"), nil, withCookie(vera)).json(t); s["backend"] != "neo4j" || s["connected"] != true || s["stats"] != nil || s["error"] != nil {
		t.Errorf("viewer storage: %v", s)
	}
	if s := a.do("GET", org(aOrg, "storage"), nil, withCookie(alice)).json(t); s["stats"] == nil {
		t.Errorf("owner storage: %v", s)
	}

	// The workspace's past.
	if r := a.do("PUT", org(aOrg, "workspace"), map[string]any{"schemaVersion": 3, "v": 1}, withCookie(alice), withHeader("If-Match", "0")); r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	revs := a.do("GET", org(aOrg, "workspace/revisions"), nil, withCookie(alice)).json(t)["revisions"].([]any)
	if len(revs) != 1 || revs[0].(map[string]any)["by"] != "alice" {
		t.Errorf("revisions: %v", revs)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if r := a.do("GET", org(aOrg, "workspace/at?at="+future), nil, withCookie(alice)); r.Code != 200 || r.json(t)["rev"].(float64) != 1 {
		t.Errorf("workspace at: %d %s", r.Code, r.Body.String())
	}

	// Deleting the organisation removes its memory too.
	if r := a.do("POST", org(bOrg, "delete"), map[string]string{"confirm": "Bob Works"}, withCookie(bob)); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
	if _, _, err := gs.GetHistory(a.ctx, bOrg, time.Now()); err == nil {
		t.Errorf("Bob's history outlived his organisation")
	}
	if _, _, err := gs.GetHistory(a.ctx, aOrg, time.Now()); err != nil {
		t.Errorf("Alice's history was affected: %v", err)
	}
}

func TestWithoutTheGraphTheSameRoutesSayWhatIsMissing(t *testing.T) {
	a := newAdminRig(t)
	alice, id := a.register(t, "alice", "Alice Lab")
	if s := a.do("GET", org(id, "storage"), nil, withCookie(alice)).json(t); s["backend"] != "sqlite" || s["enabled"] != false {
		t.Errorf("storage: %v", s)
	}
	if c := a.do("GET", org(id, "timeline?kind=service&id=x"), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("timeline without graph: %d", c)
	}
	r := a.do("GET", org(id, "audit?action=org-created"), nil, withCookie(alice))
	if r.Code != 200 || r.json(t)["source"] != "local" || len(r.json(t)["rows"].([]any)) != 1 {
		t.Errorf("audit without graph: %d %s", r.Code, r.Body.String())
	}
}

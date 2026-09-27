package server

import (
	"log/slog"
	"os"
	"strings"
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

func TestHistorySnapshotCarriesAgentsAsOfTheSameMomentAndGraphSnapshotIsSchemaAgnostic(t *testing.T) {
	a, gs := graphRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	if err := gs.AddHistory(a.ctx, aOrg, t0, snap("web", 1)); err != nil {
		t.Fatal(err)
	}
	agentDoc := AgentSnapshot{Name: "edge-collector", Status: "approved", ClusterID: "c-1", InstalledTier: 2, TierCap: 2, AccessTier: 1}
	if err := gs.RecordEntity(a.ctx, aOrg, t0, "agent", "ag-1", agentDoc.Name, agentDoc.Status, agentDoc.ClusterID, agentDoc); err != nil {
		t.Fatal(err)
	}
	gs.Sync(a.ctx)

	at := t0.Add(time.Minute).UTC().Format(time.RFC3339)

	// history/snapshot: the UI's typed view, now carrying the agents that existed as of the same moment
	// as the topology beside them, alongside the seven kinds it has always understood.
	r := a.do("GET", org(aOrg, "history/snapshot?at="+at), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("snapshot: %d %s", r.Code, r.Body.String())
	}
	body := r.json(t)
	if svcs := body["topology"].(map[string]any)["services"].([]any); len(svcs) != 1 {
		t.Errorf("topology missing: %v", body["topology"])
	}
	agents, ok := body["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("expected one historic agent, got %v", body["agents"])
	}
	ag := agents[0].(map[string]any)
	if ag["id"] != "ag-1" || ag["name"] != "edge-collector" || ag["status"] != "approved" || ag["clusterId"] != "c-1" || ag["accessTier"].(float64) != 1 {
		t.Errorf("historic agent = %v", ag)
	}

	// graph/snapshot: the schema-agnostic view of the same moment, every kind side by side with no
	// projection into the UI's fixed shape -- an entity here needs no case anywhere to be seen.
	r = a.do("GET", org(aOrg, "graph/snapshot?at="+at), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("graph snapshot: %d %s", r.Code, r.Body.String())
	}
	gbody := r.json(t)
	entities := gbody["entities"].([]any)
	byKind := map[string]int{}
	var rawAgentDoc map[string]any
	for _, e := range entities {
		row := e.(map[string]any)
		byKind[row["kind"].(string)]++
		if row["kind"] == "agent" {
			rawAgentDoc = row["doc"].(map[string]any)
		}
	}
	if byKind["service"] == 0 || byKind["cluster"] == 0 || byKind["agent"] == 0 {
		t.Fatalf("expected the polled kinds and agent side by side, got %v", byKind)
	}
	if rawAgentDoc == nil || rawAgentDoc["accessTier"].(float64) != 1 {
		t.Errorf("agent's raw doc did not come through: %v", rawAgentDoc)
	}

	// A moment before any of this existed: both endpoints say so the same way.
	before := t0.Add(-time.Hour).UTC().Format(time.RFC3339)
	if c := a.do("GET", org(aOrg, "history/snapshot?at="+before), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("history/snapshot before anything existed: %d", c)
	}
	if c := a.do("GET", org(aOrg, "graph/snapshot?at="+before), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("graph/snapshot before anything existed: %d", c)
	}
}

func TestSavingAWorkspaceVersionsItsApplicationsAndTheirMembersInTheGraph(t *testing.T) {
	a, gs := graphRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)

	// The service an application will point at, and a second one for it to later stop pointing at, must
	// already exist as graph entities the way a topology poll would have put them there.
	if err := gs.AddHistory(a.ctx, aOrg, t0, snap("web", 1)); err != nil {
		t.Fatal(err)
	}

	members := func(appID string) []string {
		t.Helper()
		res, err := gs.DB.C.Run(a.ctx, gs.DB.C.For(aOrg).S(`MATCH (:Entity {org:$org, kind:'application', id:$id})-[r:CONTAINS {org:$org}]->(m:Entity) WHERE r.validTo IS NULL RETURN m.id ORDER BY m.id`, map[string]any{"id": appID}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range res[0].Rows {
			out = append(out, row[0].(string))
		}
		return out
	}

	// A discovered service's membership arrives as an inline field on the record, exactly like a real
	// client's own document would carry it; the server files it under a ref, and this must still resolve.
	doc := map[string]any{
		"schemaVersion": 4,
		"applications":  []map[string]any{{"id": "app-1", "name": "Shop", "origin": "manual", "confidence": "high"}},
		"services":      []map[string]any{{"id": "svc-web", "source": "discovered", "applicationId": "app-1"}},
	}
	*a.now = a.now.Add(time.Minute) // each save needs its own instant, or it replaces the one before it
	if r := a.do("PUT", org(aOrg, "workspace"), doc, withCookie(alice), withHeader("If-Match", "0")); r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	tl, err := gs.Timeline(a.ctx, aOrg, "application", "app-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 1 || tl.Versions[0].Name != "Shop" {
		t.Fatalf("application not recorded: %+v", tl.Versions)
	}
	if got := members("app-1"); len(got) != 1 || got[0] != "svc-web" {
		t.Fatalf("expected svc-web as the one member, got %v", got)
	}
	// The version this save produced carries why it looks the way it does, the same EXPLAINS mechanism
	// the polled kinds already get from history.Diff - an application used to be versioned with no event
	// behind it at all.
	if ex := tl.Versions[0].Explains; len(ex) != 1 || ex[0].Kind != "application-added" {
		t.Fatalf("a newly created application should explain itself as application-added: %+v", ex)
	}

	// Renamed and its membership cleared: a second version, and the CONTAINS edge closes.
	doc2 := map[string]any{
		"schemaVersion": 4,
		"applications":  []map[string]any{{"id": "app-1", "name": "Storefront", "origin": "manual", "confidence": "high"}},
		"services":      []map[string]any{{"id": "svc-web", "source": "discovered"}},
	}
	*a.now = a.now.Add(time.Minute)
	if r := a.do("PUT", org(aOrg, "workspace"), doc2, withCookie(alice), withHeader("If-Match", "1")); r.Code != 200 {
		t.Fatalf("save 2: %d %s", r.Code, r.Body.String())
	}
	tl2, err := gs.Timeline(a.ctx, aOrg, "application", "app-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl2.Versions) != 2 || tl2.Versions[0].Name != "Storefront" {
		t.Fatalf("rename not recorded: %+v", tl2.Versions)
	}
	if got := members("app-1"); len(got) != 0 {
		t.Fatalf("expected no members after they were cleared, got %v", got)
	}
	// Two things changed in the same save (the name, and the membership going to none): both should be
	// there, not just whichever one a single generic "application-changed" event would have picked.
	kinds := map[string]bool{}
	for _, ex := range tl2.Versions[0].Explains {
		kinds[ex.Kind] = true
	}
	if !kinds["application-renamed"] || !kinds["application-membership"] {
		t.Fatalf("a rename plus a membership change should both explain the new version: %+v", tl2.Versions[0].Explains)
	}

	// Read schema-agnostically too, while it is still open: /graph/snapshot shows it right alongside the
	// polled kinds, with no case anywhere needed to make that so.
	beforeRemoval := a.now.UTC().Format(time.RFC3339)
	hasApp := func(at string) bool {
		t.Helper()
		r := a.do("GET", org(aOrg, "graph/snapshot?at="+at), nil, withCookie(alice))
		if r.Code != 200 {
			t.Fatalf("graph snapshot: %d %s", r.Code, r.Body.String())
		}
		for _, e := range r.json(t)["entities"].([]any) {
			row := e.(map[string]any)
			if row["kind"] == "application" && row["id"] == "app-1" {
				return true
			}
		}
		return false
	}
	if !hasApp(beforeRemoval) {
		t.Error("graph/snapshot should show the application while it still exists")
	}

	// Removed from the document entirely: the application is retired, not silently left open forever.
	doc3 := map[string]any{"schemaVersion": 4}
	*a.now = a.now.Add(time.Minute)
	if r := a.do("PUT", org(aOrg, "workspace"), doc3, withCookie(alice), withHeader("If-Match", "2")); r.Code != 200 {
		t.Fatalf("save 3: %d %s", r.Code, r.Body.String())
	}
	res, err := gs.DB.C.Run(a.ctx, gs.DB.C.For(aOrg).S(`MATCH (e:Entity {org:$org, kind:'application', id:'app-1'}) RETURN e.gone IS NOT NULL`, nil))
	if err != nil || len(res[0].Rows) == 0 || res[0].Rows[0][0] != true {
		t.Fatalf("app-1 should have been retired once removed from the document: %v, err=%v", res, err)
	}
	// The removal itself is an event too, even though there is no new version left to attach it to as an
	// EXPLAINS edge - Timeline's plain Events list is where a removal like this belongs.
	tl3, err := gs.Timeline(a.ctx, aOrg, "application", "app-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	removedSeen := false
	for _, ev := range tl3.Events {
		removedSeen = removedSeen || ev.Kind == "application-removed"
	}
	if !removedSeen {
		t.Fatalf("app-1's removal should show up as an application-removed event: %+v", tl3.Events)
	}
	// Once retired it is honestly absent from a snapshot taken at or after that moment - the same
	// half-open validity window every other kind's version already has.
	if hasApp(a.now.UTC().Format(time.RFC3339)) {
		t.Error("graph/snapshot should not show the application once it has been retired")
	}
	// But the past is still the past: asked about the moment before removal, it is still there.
	if !hasApp(beforeRemoval) {
		t.Error("graph/snapshot asked about an earlier moment should still show the application as it was then")
	}
}

func depTopo(clusterID string, svcAReplicas int32) model.Topology {
	return model.Topology{
		Clusters: []model.Cluster{{ID: clusterID, Name: "edge", Status: "connected"}},
		Services: []model.Service{
			{ID: "svc-a", ClusterID: clusterID, Name: "a", Replicas: svcAReplicas, ReadyReplicas: svcAReplicas, Status: "ready"},
			{ID: "svc-b", ClusterID: clusterID, Name: "b", Replicas: 1, ReadyReplicas: 1, Status: "ready"},
		},
		Dependencies: []model.Dependency{{ID: "dep-ab", From: "svc-a", FromKind: "service", To: "svc-b", ToKind: "service", Protocol: "tcp"}},
	}
}

// TestGraphTraversalAndDiffThroughTheAPI exercises /graph/dependents, /graph/dependencies and
// /graph/diff end to end: svc-a calls svc-b, so svc-a is what breaks if svc-b goes away and svc-b is
// what svc-a needs; a later moment where svc-a scaled up shows up as a structural diff.
func TestGraphTraversalAndDiffThroughTheAPI(t *testing.T) {
	a, gs := graphRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	b, _, _ := history.Encode(history.Compact(depTopo("c-1", 1)))
	if err := gs.AddHistory(a.ctx, aOrg, t0, b); err != nil {
		t.Fatal(err)
	}
	gs.Sync(a.ctx)
	at := t0.UTC().Format(time.RFC3339)

	r := a.do("GET", org(aOrg, "graph/dependents?kind=service&id=svc-b&at="+at), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("dependents: %d %s", r.Code, r.Body.String())
	}
	reached := r.json(t)["reached"].([]any)
	if len(reached) != 1 || reached[0].(map[string]any)["id"] != "svc-a" {
		t.Errorf("dependents of svc-b = %v", reached)
	}

	r = a.do("GET", org(aOrg, "graph/dependencies?kind=service&id=svc-a&at="+at), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("dependencies: %d %s", r.Code, r.Body.String())
	}
	found := false
	for _, d := range r.json(t)["reached"].([]any) {
		if d.(map[string]any)["id"] == "svc-b" {
			found = true
		}
	}
	if !found {
		t.Errorf("dependencies of svc-a should include svc-b: %v", r.json(t)["reached"])
	}

	if c := a.do("GET", org(aOrg, "graph/dependents?kind=service&id=nope&at="+at), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("dependents of an unknown record: %d", c)
	}
	if c := a.do("GET", org(aOrg, "graph/dependents?kind=service&at="+at), nil, withCookie(alice)).Code; c != 400 {
		t.Errorf("dependents without id: %d", c)
	}

	// hops is never trusted verbatim: garbage, negative or absurdly large values must not error, hang or
	// crash the request - they fall back to the documented default, or are silently clamped downstream in
	// DB.walk (see TestWalkHopsAreClamped in the graph package for the clamp itself).
	for _, badHops := range []string{"", "0", "-5", "abc", "3.5", "99999999"} {
		r := a.do("GET", org(aOrg, "graph/dependents?kind=service&id=svc-b&at="+at+"&hops="+badHops), nil, withCookie(alice))
		if r.Code != 200 {
			t.Errorf("hops=%q: %d %s", badHops, r.Code, r.Body.String())
			continue
		}
		if _, ok := r.json(t)["reached"].([]any); !ok {
			t.Errorf("hops=%q did not return a usable result: %v", badHops, r.json(t))
		}
	}

	t1 := t0.Add(time.Hour)
	b2, _, _ := history.Encode(history.Compact(depTopo("c-1", 3)))
	if err := gs.AddHistory(a.ctx, aOrg, t1, b2); err != nil {
		t.Fatal(err)
	}
	gs.Sync(a.ctx)
	r = a.do("GET", org(aOrg, "graph/diff?from="+at+"&to="+t1.UTC().Format(time.RFC3339)), nil, withCookie(alice))
	if r.Code != 200 {
		t.Fatalf("diff: %d %s", r.Code, r.Body.String())
	}
	body := r.json(t)
	sawReplicas := false
	for _, c := range body["changed"].([]any) {
		row := c.(map[string]any)
		if row["id"] == "svc-a" {
			for _, ch := range row["changes"].([]any) {
				if ch.(map[string]any)["field"] == "replicas" {
					sawReplicas = true
				}
			}
		}
	}
	if !sawReplicas {
		t.Errorf("diff should report svc-a's replica count changing: %v", body["changed"])
	}
	if len(body["added"].([]any)) != 0 || len(body["removed"].([]any)) != 0 {
		t.Errorf("nothing was added or removed between the two moments, only changed: %v", body)
	}

	if c := a.do("GET", org(aOrg, "graph/diff?from="+at), nil, withCookie(alice)).Code; c != 400 {
		t.Errorf("diff without to: %d", c)
	}
}

// TestAgentTierAndConsentChangesExplainThemselvesInTheGraph is recordAgentGraph's own version of the
// applications test above: an agent's version in the graph used to carry only the "what" (a new doc,
// unlike the last one) with no "why" behind it, unlike every polled kind. Both actions here are already
// audited (see TestTierChangeStaysWithinTheInstalledCeilingAndIsAudited and
// TestConsentOverridesAreValidatedPersistedAndPushed in consent_test.go) - this checks that the same
// detail also reaches the graph as an EXPLAINS-linked event, not just the audit trail.
func TestAgentTierAndConsentChangesExplainThemselvesInTheGraph(t *testing.T) {
	a, gs := graphRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab") // an owner, so no separate editor account is needed
	a.core = a.base.ForOrg(aOrg)                       // approvedAgent below enrolls and approves through this org's own Core
	id, _, _ := a.approvedAgent(t, fp)                 // approved at tier 2, installed at 2

	if r := a.do("POST", org(aOrg, "agents/"+id+"/tier"), map[string]any{"tier": 0}, withCookie(alice)); r.Code != 200 {
		t.Fatalf("narrow tier: %d %s", r.Code, r.Body.String())
	}
	tl, err := gs.Timeline(a.ctx, aOrg, "agent", id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) == 0 {
		t.Fatal("no version recorded for the agent")
	}
	var tierEvent *graph.ExplainingEvent
	for i, ex := range tl.Versions[0].Explains {
		if ex.Kind == "agent-tier-changed" {
			tierEvent = &tl.Versions[0].Explains[i]
		}
	}
	if tierEvent == nil || !strings.Contains(tierEvent.Detail, "access tier 2") {
		t.Fatalf("the version after narrowing the tier should explain itself as agent-tier-changed, with the same detail the audit trail got: %+v", tl.Versions[0].Explains)
	}

	if r := a.do("POST", org(aOrg, "agents/"+id+"/consent"), map[string]any{"pausedCollectors": []string{"flow"}}, withCookie(alice)); r.Code != 200 {
		t.Fatalf("set consent: %d %s", r.Code, r.Body.String())
	}
	tl2, err := gs.Timeline(a.ctx, aOrg, "agent", id, 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawConsentEvent bool
	for _, ex := range tl2.Versions[0].Explains {
		sawConsentEvent = sawConsentEvent || ex.Kind == "agent-consent-changed"
	}
	if !sawConsentEvent {
		t.Fatalf("the version after changing consent should explain itself as agent-consent-changed: %+v", tl2.Versions[0].Explains)
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
	if c := a.do("GET", org(id, "graph/snapshot?at="+time.Now().UTC().Format(time.RFC3339)), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("graph snapshot without graph: %d", c)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	if c := a.do("GET", org(id, "graph/dependents?kind=service&id=x&at="+at), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("graph dependents without graph: %d", c)
	}
	if c := a.do("GET", org(id, "graph/dependencies?kind=service&id=x&at="+at), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("graph dependencies without graph: %d", c)
	}
	if c := a.do("GET", org(id, "graph/diff?from="+at+"&to="+at), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("graph diff without graph: %d", c)
	}
	r := a.do("GET", org(id, "audit?action=org-created"), nil, withCookie(alice))
	if r.Code != 200 || r.json(t)["source"] != "local" || len(r.json(t)["rows"].([]any)) != 1 {
		t.Errorf("audit without graph: %d %s", r.Code, r.Body.String())
	}
}

package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/store"
)

// The tests that need a database run against a real Neo4j when CONTINUUM_TEST_NEO4J is set
// (http://127.0.0.1:7474, with CONTINUUM_TEST_NEO4J_USER / _PASSWORD) and are skipped otherwise.

var orgSeq atomic.Int64

func testDB(t *testing.T) (*DB, string) {
	t.Helper()
	u := os.Getenv("CONTINUUM_TEST_NEO4J")
	if u == "" {
		t.Skip("set CONTINUUM_TEST_NEO4J to run the Neo4j tests")
	}
	user := os.Getenv("CONTINUUM_TEST_NEO4J_USER")
	if user == "" {
		user = "neo4j"
	}
	c, err := NewClient(Config{URL: u, User: user, Password: os.Getenv("CONTINUUM_TEST_NEO4J_PASSWORD")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := NewDB(c)
	org := fmt.Sprintf("t%d-%d", time.Now().UnixNano()%1e9, orgSeq.Add(1))
	t.Cleanup(func() { _ = db.PurgeTenant(context.Background(), org) })
	return db, org
}

func orgN(t *testing.T, db *DB) string {
	org := fmt.Sprintf("t%d-%d", time.Now().UnixNano()%1e9, orgSeq.Add(1))
	t.Cleanup(func() { _ = db.PurgeTenant(context.Background(), org) })
	return org
}

func estate() model.Topology {
	return model.Topology{
		Clusters: []model.Cluster{
			{Provenance: model.Provenance{OrgID: "x", LastSeen: "2026-01-01T00:00:00Z", Revision: 4}, ID: "c-1", Name: "edge-a", Tier: "edge", Status: "connected", Region: "eu-north"},
			{ID: "c-2", Name: "cloud", Tier: "cloud", Status: "connected"},
		},
		Nodes: []model.Node{
			{ID: "n-1", ClusterID: "c-1", Name: "n1", Status: "ready", CPU: 4},
			{ID: "n-2", ClusterID: "c-2", Name: "n2", Status: "ready", CPU: 8},
		},
		Namespaces: []model.Namespace{{ID: "ns-1", ClusterID: "c-1", Name: "shop"}},
		Services: []model.Service{
			{ID: "s-1", ClusterID: "c-1", Name: "web", Replicas: 2, ReadyReplicas: 2, Status: "ready", NodeIDs: []string{"n-1"}},
			{ID: "s-2", ClusterID: "c-2", Name: "db", Replicas: 1, ReadyReplicas: 1, Status: "ready", NodeIDs: []string{"n-2"}},
		},
		Dependencies: []model.Dependency{
			{ID: "d-1", From: "s-1", FromKind: "service", To: "s-2", ToKind: "service", Protocol: "tcp", Port: 5432, Bytes: 1000, Connections: 5, Stats: &model.DependencyStats{BytesPerSec: 12.5, WindowSec: 30}},
			{ID: "d-2", From: "s-1", FromKind: "service", To: "ext-1", ToKind: "external", Protocol: "tcp", Port: 443, Bytes: 10},
		},
		ExternalEndpoints: []model.ExternalEndpoint{{ID: "ext-1", Host: "api.example.com", Port: 443, Kind: "saas"}},
		Paths:             []model.Path{{ID: "p-1", FromCluster: "c-1", Host: "10.0.0.2", Port: 443, ToCluster: "c-2", Source: "observed", RTTP50: 12, RTTP95: 20, LossPct: 0.5, Samples: 10, At: "2026-09-19T10:00:00Z"}},
	}
}

func record(t *testing.T, db *DB, org string, at time.Time, topo model.Topology) {
	t.Helper()
	c := history.Compact(topo)
	data, fp, err := history.Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Record(context.Background(), org, at, c, fp, len(data)); err != nil {
		t.Fatalf("record %s: %v", at, err)
	}
}

func TestATenantStatementMustSayWhoseDataItReads(t *testing.T) {
	c, _ := NewClient(Config{URL: "http://127.0.0.1:1"})
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not refuse", name)
			}
		}()
		f()
	}
	mustPanic("no $org", func() { c.For("a").S(`MATCH (n) RETURN n`, nil) })
	mustPanic("no tenant", func() { c.For("").S(`MATCH (n {org:$org}) RETURN n`, nil) })
	// The tenant comes from the scope, not from parameters a caller passes.
	s := c.For("mine").S(`MATCH (n {org:$org}) RETURN n`, map[string]any{"org": "theirs"})
	if s.P["org"] != "mine" {
		t.Errorf("a caller-supplied org overrode the scope: %v", s.P["org"])
	}
}

func TestEveryStatementInThePackageIsTenantScopedOrDeliberatelyGlobal(t *testing.T) {
	// Global statements (schema, ping, listing tenant ids) are the only ones allowed
	// to skip the scope; adding one is a decision to be made on purpose.
	files, _ := filepath.Glob("*.go")
	global := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		global += strings.Count(string(b), "Global(")
		global -= strings.Count(string(b), "func Global(")
		global -= strings.Count(string(b), "// Global(") // mentions in comments
		if strings.Contains(string(b), "Stmt{Q:") && f != "client.go" {
			t.Errorf("%s assembles a Stmt by hand; use Scope.S or Global", f)
		}
		if strings.Contains(string(b), "Client.Run(") && f != "client.go" {
			t.Errorf("%s runs statements on the raw client", f)
		}
	}
	if global != 4 {
		t.Errorf("found %d raw statements outside a tenant scope; expected the 4 known ones (schema, schema meta, ping, tenant ids). Review any new one.", global)
	}
}

func TestTheEstateComesBackExactlyAsItWasAtEveryMoment(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	// t1: web is scaled to 5; traffic counters move.
	e1 := estate()
	e1.Services[0].Replicas, e1.Services[0].ReadyReplicas = 5, 3
	e1.Dependencies[0].Bytes = 5000
	record(t, db, org, t0.Add(time.Hour), e1)

	// t2: the db service and its dependency vanish; a node goes not-ready.
	e2 := estate()
	e2.Services = e2.Services[:1]
	e2.Dependencies = e2.Dependencies[1:]
	e2.Nodes[0].Status = "not-ready"
	record(t, db, org, t0.Add(2*time.Hour), e2)

	// Before anything was recorded.
	if _, err := db.AsOf(ctx, org, t0.Add(-time.Second)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("before the first snapshot: %v", err)
	}

	s0, err := db.AsOf(ctx, org, t0.Add(30*time.Minute)) // between snapshots: the one at or before
	if err != nil {
		t.Fatal(err)
	}
	if !s0.At.Equal(t0) {
		t.Errorf("at %v, want the snapshot at %v", s0.At, t0)
	}
	want0 := history.Compact(estate())
	if len(s0.Topology.Clusters) != 2 || len(s0.Topology.Nodes) != 2 || len(s0.Topology.Services) != 2 || len(s0.Topology.Dependencies) != 2 || len(s0.Topology.ExternalEndpoints) != 1 || len(s0.Topology.Paths) != 1 {
		t.Fatalf("t0 is missing records: %+v", s0.Topology)
	}
	if history.Fingerprint(s0.Topology) != history.Fingerprint(want0) {
		t.Errorf("the reconstructed t0 differs from what was recorded")
	}
	d1 := s0.Topology.Dependencies[0]
	if d1.Bytes != 1000 || d1.Connections != 5 || d1.Stats == nil || d1.Stats.BytesPerSec != 12.5 {
		t.Errorf("t0 traffic counters were not restored: %+v", d1)
	}
	if p := s0.Topology.Paths[0]; p.RTTP50 != 12 || p.LossPct != 0.5 || p.Samples != 10 || p.At != "2026-09-19T10:00:00Z" {
		t.Errorf("path quality was not restored: %+v", p)
	}
	if s0.Topology.Clusters[0].LastSeen == "" {
		t.Errorf("history should stamp records as seen at the snapshot")
	}

	s1, err := db.AsOf(ctx, org, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s1.Topology.Services[0].Replicas != 5 || s1.Topology.Services[0].ReadyReplicas != 3 || s1.Topology.Dependencies[0].Bytes != 5000 {
		t.Errorf("t1 wrong: %+v / %+v", s1.Topology.Services[0], s1.Topology.Dependencies[0])
	}
	// The past did not change when the present did.
	s0again, _ := db.AsOf(ctx, org, t0)
	if s0again.Topology.Services[0].Replicas != 2 {
		t.Errorf("t0 was rewritten by a later change")
	}

	s2, err := db.AsOf(ctx, org, t0.Add(3*time.Hour)) // after the last: the last
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Topology.Services) != 1 || len(s2.Topology.Dependencies) != 1 || s2.Topology.Nodes[0].Status != "not-ready" {
		t.Errorf("t2 wrong: %d services, %d deps, node %s", len(s2.Topology.Services), len(s2.Topology.Dependencies), s2.Topology.Nodes[0].Status)
	}

	// Only changes are stored: 2 clusters + 2 nodes + 1 ns + 2 services + 2 deps + 1 external + 1 path = 11 at
	// t0; t1 adds the scaled service (1, the traffic counters do not count); t2 adds the not-ready node and
	// web back at two replicas (2) and closes db + d-1 without a new version.
	st, err := db.Stats(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if st.Versions != 14 || st.Snapshots != 3 {
		t.Errorf("stored %d versions in %d snapshots, want 14 in 3 (unchanged records must not be re-stored)", st.Versions, st.Snapshots)
	}

	pts, _ := db.Points(ctx, org, time.Time{}, time.Time{})
	if len(pts) != 3 || !pts[0].At.Equal(t0) {
		t.Errorf("points: %+v", pts)
	}
	if pts2, _ := db.Points(ctx, org, t0.Add(30*time.Minute), t0.Add(90*time.Minute)); len(pts2) != 1 {
		t.Errorf("a window should hold one point, got %d", len(pts2))
	}
}

func TestLinksAreTemporalToo(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())
	e := estate()
	e.Services[0].NodeIDs = []string{"n-2"} // web moves to the other node
	record(t, db, org, t0.Add(time.Hour), e)

	q := func(at time.Time) []string {
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (s:Service {org:$org, id:'s-1'})-[r:RUNS_ON]->(n:Node {org:$org})
WHERE r.validFrom <= datetime($at) AND (r.validTo IS NULL OR r.validTo > datetime($at)) RETURN n.id`, map[string]any{"at": ts(at)}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range res[0].Rows {
			out = append(out, str(r[0]))
		}
		return out
	}
	if got := q(t0.Add(time.Minute)); len(got) != 1 || got[0] != "n-1" {
		t.Errorf("before the move web ran on %v", got)
	}
	if got := q(t0.Add(2 * time.Hour)); len(got) != 1 || got[0] != "n-2" {
		t.Errorf("after the move web ran on %v", got)
	}
	// A graph question the snapshot store could not answer: which services called the db, and when.
	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (a:Service {org:$org})-[r:CALLS {org:$org}]->(b:Service {org:$org, id:'s-2'}) RETURN a.id, r.port, r.validTo IS NULL`, nil))
	if err != nil || len(res[0].Rows) != 1 || str(res[0].Rows[0][0]) != "s-1" {
		t.Errorf("calls: %v %v", res, err)
	}
}

func TestRecordEntityVersionsSomethingOutsideThePolledTopology(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // clusters c-1/c-2 exist, so the entity's IN_CLUSTER edge has somewhere to land

	type doc struct {
		AccessTier int      `json:"accessTier"`
		Excluded   []string `json:"excluded,omitempty"`
	}
	rec := func(at time.Time, status, cluster string, d doc) {
		t.Helper()
		if err := db.RecordEntity(ctx, org, at, "agent", "ag-1", "edge-collector", status, cluster, d); err != nil {
			t.Fatalf("record entity %s: %v", at, err)
		}
	}

	rec(t0.Add(time.Minute), "approved", "c-1", doc{AccessTier: 1})
	rec(t0.Add(time.Minute), "approved", "c-1", doc{AccessTier: 1}) // identical: must not open a second version
	rec(t0.Add(2*time.Hour), "approved", "c-1", doc{AccessTier: 2, Excluded: []string{"kube-system"}})
	rec(t0.Add(3*time.Hour), "approved", "c-2", doc{AccessTier: 2, Excluded: []string{"kube-system"}}) // moves cluster, doc otherwise identical

	tl, err := db.Timeline(ctx, org, "agent", "ag-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 3 {
		t.Fatalf("expected 3 versions (the repeat should not have made a fourth): %+v", tl.Versions)
	}
	if tl.Versions[0].To != nil {
		t.Errorf("the newest version should still be open: %+v", tl.Versions[0])
	}

	// The one open IN_CLUSTER edge should have moved to c-2, not accumulated a second one, even though
	// the move alone (with no other field changing) is what triggered this version.
	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'agent', id:'ag-1'})-[r:IN_CLUSTER {org:$org}]->(c:Cluster) RETURN c.id, r.validTo IS NULL ORDER BY c.id`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Rows) != 2 {
		t.Fatalf("expected the c-1 edge closed and the c-2 edge open, got %v", res[0].Rows)
	}
	open := map[string]bool{}
	for _, r := range res[0].Rows {
		open[str(r[0])] = r[1].(bool)
	}
	if open["c-1"] || !open["c-2"] {
		t.Errorf("edge did not move as expected: %v", res[0].Rows)
	}

	if err := db.RecordEntity(ctx, org, t0, "not-a-kind", "x", "x", "x", "", nil); err == nil {
		t.Error("an unknown kind should be refused")
	}
}

// TestAsOfEntitiesSeesEverythingAsOfProjectsOnlyWhatItKnows verifies the split this package's read
// side is built on. AsOfEntities is the schema-agnostic foundation: it sees an "agent" version
// (something outside the seven polled kinds) exactly like any other entity, and -- unlike AsOf -- it
// reads the graph at the exact instant asked for rather than rounding down to the last full-topology
// poll, so a RecordEntity write shows up the moment it happens rather than waiting for the next poll
// to catch up. AsOf, projecting the same graph into the typed model.Topology shape the UI already
// knows, keeps rounding to the last poll (the seven kinds it understands only ever change together, in
// lockstep with one) and goes on quietly leaving "agent" out, since model.Topology has nowhere to put
// it. Neither behavior is accidental; this pins both down so a future change to either one has to break
// a test to break the split.
func TestAsOfEntitiesSeesEverythingAsOfProjectsOnlyWhatItKnows(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // the only full-topology poll: clusters/services/etc. as of t0

	type doc struct {
		AccessTier int `json:"accessTier"`
	}
	agentAt := t0.Add(2 * time.Hour) // long after the one poll, with no later poll to round down to
	if err := db.RecordEntity(ctx, org, agentAt, "agent", "ag-1", "edge-collector", "approved", "c-1", doc{AccessTier: 1}); err != nil {
		t.Fatalf("record entity: %v", err)
	}

	resolved, entities, err := db.AsOfEntities(ctx, org, agentAt)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Equal(agentAt.UTC().Truncate(time.Second)) {
		t.Errorf("AsOfEntities should resolve to the instant asked for, not round it: %v", resolved)
	}
	byKey := map[string]EntitySnapshot{}
	for _, e := range entities {
		byKey[e.Kind+"/"+e.ID] = e
	}
	ag, ok := byKey["agent/ag-1"]
	if !ok {
		t.Fatalf("AsOfEntities dropped the agent entity, two hours after the last poll: %+v", entities)
	}
	if ag.Name != "edge-collector" || ag.Status != "approved" || ag.Cluster != "c-1" {
		t.Errorf("agent entity carried the wrong fields: %+v", ag)
	}
	var d doc
	if err := json.Unmarshal(ag.Doc, &d); err != nil || d.AccessTier != 1 {
		t.Errorf("agent entity's doc did not round-trip: %s (err=%v)", ag.Doc, err)
	}
	if _, ok := byKey["cluster/c-1"]; !ok {
		t.Errorf("AsOfEntities should still carry the seven polled kinds alongside agent: %+v", entities)
	}

	// AsOf, asked about that same instant, has no later poll to round down to, so it still lands on the
	// t0 poll and its Topology has no field for the agent version at all -- a different, and correct,
	// answer to a differently-scoped question.
	snap, err := db.AsOf(ctx, org, agentAt)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.At.Equal(t0) {
		t.Errorf("AsOf should have rounded down to the one poll at t0, got %v", snap.At)
	}
	if len(snap.Topology.Clusters) == 0 {
		t.Error("AsOf lost the polled kinds it has always known")
	}
}

// TestAsOfEntitiesSaysWhenItHasNoMemoryThatFarBack mirrors AsOf's own ErrNotFound contract for the
// schema-agnostic path: asking about an instant before this org's very first recorded entity must fail
// clearly, not answer with a silently empty list that looks identical to "nothing changed."
func TestAsOfEntitiesSaysWhenItHasNoMemoryThatFarBack(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	if _, _, err := db.AsOfEntities(ctx, org, t0.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound before anything was recorded, got %v", err)
	}
	if _, entities, err := db.AsOfEntities(ctx, org, t0); err != nil || len(entities) == 0 {
		t.Errorf("expected entities at the moment they were first recorded, got %d entities, err=%v", len(entities), err)
	}
}

// TestRecordDoesNotTouchAnEdgeItDidNotWrite guards a real bug: Record's own edge sweep for a
// relationship type it shares with RecordEntity (IN_CLUSTER: polled kinds use it too) used to close
// ANY currently-open edge of that type it did not see in the topology it was just given - including
// one RecordEntity opened for an agent, which a topology poll never reports and so never "sees" by
// design. In production that meant an agent's cluster edge was closed again by the very next scheduled
// poll, minutes after RecordEntity opened it, silently breaking "which cluster was this agent in"
// before anyone could query it. Record must leave an edge alone entirely unless the edge belongs to a
// kind its own poll actually covers.
func TestRecordDoesNotTouchAnEdgeItDidNotWrite(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	e := estate() // clusters c-1/c-2, services/nodes attached to them via their own IN_CLUSTER edges
	record(t, db, org, t0, e)
	if err := db.RecordEntity(ctx, org, t0.Add(time.Minute), "agent", "ag-1", "edge-collector", "approved", "c-1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}

	// A normal poll, repeatedly, finding nothing new about the topology at all - what recorder.go's
	// scan() does every few minutes regardless of whether anything about an agent ever changes.
	for i := 1; i <= 3; i++ {
		if err := db.Record(ctx, org, t0.Add(time.Duration(i+1)*time.Minute), e, fmt.Sprintf("fp%d", i), 10); err != nil {
			t.Fatal(err)
		}
	}

	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'agent', id:'ag-1'})-[r:IN_CLUSTER {org:$org}]->(c:Cluster) WHERE r.validTo IS NULL RETURN c.id`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Rows) != 1 || str(res[0].Rows[0][0]) != "c-1" {
		t.Fatalf("the agent's cluster edge should have survived three unrelated polls untouched, got %v", res[0].Rows)
	}
	// The polled kinds' own IN_CLUSTER edges are unaffected by the fix either way.
	nodeEdges, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'node', id:'n-1'})-[r:IN_CLUSTER {org:$org}]->(c:Cluster) WHERE r.validTo IS NULL RETURN c.id`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeEdges[0].Rows) != 1 || str(nodeEdges[0].Rows[0][0]) != "c-1" {
		t.Errorf("a polled kind's own edge should be exactly as the topology says: %v", nodeEdges[0].Rows)
	}
}

func TestLinkEntitiesKeepsExactlyTheGivenMembersOpen(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // s-1, s-2 exist as services for the application to point at

	if err := db.RecordEntity(ctx, org, t0.Add(time.Minute), "application", "app-1", "Shop", "", "", map[string]any{"name": "Shop"}); err != nil {
		t.Fatal(err)
	}
	open := func() []string {
		t.Helper()
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'application', id:'app-1'})-[r:CONTAINS {org:$org}]->(m:Entity) WHERE r.validTo IS NULL RETURN m.id ORDER BY m.id`, nil))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range res[0].Rows {
			out = append(out, str(row[0]))
		}
		return out
	}
	validFromOf := func(memberID string) string {
		t.Helper()
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'application', id:'app-1'})-[r:CONTAINS {org:$org}]->(:Entity {id:$id}) WHERE r.validTo IS NULL RETURN toString(r.validFrom)`, map[string]any{"id": memberID}))
		if err != nil || len(res[0].Rows) == 0 {
			t.Fatalf("no open edge to %s", memberID)
		}
		return str(res[0].Rows[0][0])
	}

	if err := db.LinkEntities(ctx, org, t0.Add(time.Minute), "CONTAINS", "application", "app-1", "service", []string{"s-1", "s-2"}); err != nil {
		t.Fatal(err)
	}
	if got := open(); len(got) != 2 || got[0] != "s-1" || got[1] != "s-2" {
		t.Fatalf("expected s-1 and s-2 open, got %v", got)
	}
	s2From := validFromOf("s-2")

	// s-1 drops out; s-2 stays a member throughout and its edge must not be touched.
	if err := db.LinkEntities(ctx, org, t0.Add(2*time.Minute), "CONTAINS", "application", "app-1", "service", []string{"s-2"}); err != nil {
		t.Fatal(err)
	}
	if got := open(); len(got) != 1 || got[0] != "s-2" {
		t.Fatalf("expected only s-2 open after the change, got %v", got)
	}
	if validFromOf("s-2") != s2From {
		t.Error("s-2's edge should never have been closed and reopened, only left alone")
	}

	// An empty member list retires membership entirely without touching the entity itself.
	if err := db.LinkEntities(ctx, org, t0.Add(3*time.Minute), "CONTAINS", "application", "app-1", "service", nil); err != nil {
		t.Fatal(err)
	}
	if got := open(); len(got) != 0 {
		t.Fatalf("expected no open members, got %v", got)
	}

	if err := db.LinkEntities(ctx, org, t0, "NOT_A_TYPE", "application", "app-1", "service", nil); err == nil {
		t.Error("an unknown relationship type should be refused")
	}
}

func TestCloseMissingEntitiesRetiresGoneOnesAndLeavesOthersAlone(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	mustRecord := func(at time.Time, id, name string) {
		t.Helper()
		if err := db.RecordEntity(ctx, org, at, "application", id, name, "", "", map[string]any{"name": name}); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord(t0, "app-1", "Shop")
	mustRecord(t0, "app-2", "Billing")

	closed, err := db.CloseMissingEntities(ctx, org, t0.Add(time.Minute), "application", []string{"app-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0] != "app-2" {
		t.Fatalf("expected app-2 reported closed, got %v", closed)
	}

	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (e:Entity {org:$org, kind:'application', id:'app-2'}) RETURN e.gone IS NOT NULL`, nil))
	if err != nil || len(res[0].Rows) == 0 || res[0].Rows[0][0] != true {
		t.Fatalf("app-2 should be marked gone: %v, err=%v", res, err)
	}
	tl, err := db.Timeline(ctx, org, "application", "app-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 1 || tl.Versions[0].To == nil {
		t.Fatalf("app-2's one version should be closed, not open: %+v", tl.Versions)
	}

	res1, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (e:Entity {org:$org, kind:'application', id:'app-1'}) RETURN e.gone IS NOT NULL`, nil))
	if err != nil || len(res1[0].Rows) == 0 || res1[0].Rows[0][0] != false {
		t.Fatalf("app-1 should be untouched: %v, err=%v", res1, err)
	}

	// A second call with nothing new to close is a quiet no-op.
	closed2, err := db.CloseMissingEntities(ctx, org, t0.Add(2*time.Minute), "application", []string{"app-1"})
	if err != nil || len(closed2) != 0 {
		t.Fatalf("expected nothing new to close, got %v, err=%v", closed2, err)
	}
}

func TestEventsExplainTheVersionTheyProduced(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	e := estate()
	e.Services[0].Replicas = 5 // s-1: 2 -> 5
	scaled := store.Event{At: t0.Add(time.Hour).Add(137 * time.Millisecond), Kind: "service-scaled", TargetKind: "service", TargetID: "s-1", Detail: "2 -> 5", Cause: "autoscaler (1-6)"}
	unrelated := store.Event{At: t0.Add(time.Hour), Kind: "cluster-status", TargetKind: "cluster", TargetID: "c-2", Detail: "reachable -> unreachable"} // no version for c-2 changed
	if err := db.AddEvents(ctx, org, []store.Event{scaled, unrelated}); err != nil {
		t.Fatal(err)
	}
	record(t, db, org, t0.Add(time.Hour), e)
	if err := db.LinkEventChanges(ctx, org, t0.Add(time.Hour), []store.Event{scaled, unrelated}); err != nil {
		t.Fatal(err)
	}

	tl, err := db.Timeline(ctx, org, "service", "s-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 2 {
		t.Fatalf("versions: %+v", tl.Versions)
	}
	if len(tl.Versions[0].Explains) != 1 || tl.Versions[0].Explains[0].Kind != "service-scaled" || tl.Versions[0].Explains[0].Cause != "autoscaler (1-6)" {
		t.Fatalf("the newest version should be explained by the scale event: %+v", tl.Versions[0].Explains)
	}
	if len(tl.Versions[1].Explains) != 0 {
		t.Errorf("the first version was never produced by an event, it was just first seen: %+v", tl.Versions[1].Explains)
	}

	// The unrelated event (about a cluster that got no new version at this instant) should not have been
	// linked to anything - not to s-1's version, and there should be no dangling EXPLAINS edge for it either.
	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (ev:Event {org:$org, targetKind:'cluster', targetId:'c-2'})-[:EXPLAINS]->() RETURN count(*)`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if i64(res[0].Rows[0][0]) != 0 {
		t.Errorf("the unrelated event should not explain anything (no version was made for c-2 at that instant): %v", res[0].Rows)
	}
}

func TestRecordingTheSameInstantTwiceReplacesRatherThanBreaks(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())
	e := estate()
	e.Services[0].Replicas = 9
	record(t, db, org, t0, e) // same second, different content
	s, err := db.AsOf(ctx, org, t0)
	if err != nil || s.Topology.Services[0].Replicas != 9 {
		t.Fatalf("replace failed: %v %v", err, s.Topology.Services)
	}
	if st, _ := db.Stats(ctx, org); st.Snapshots != 1 {
		t.Errorf("%d snapshots at one instant", st.Snapshots)
	}
}

func TestThePastCannotBeWrittenAfterTheFact(t *testing.T) {
	db, org := testDB(t)
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())
	c := history.Compact(estate())
	if err := db.Record(context.Background(), org, t0.Add(-time.Hour), c, "x", 1); !errors.Is(err, ErrOutOfOrder) {
		t.Errorf("got %v", err)
	}
}

func TestTenantsNeverSeeEachOthersHistoryEventsOrAudit(t *testing.T) {
	db, a := testDB(t)
	b := orgN(t, db)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	// The same ids in both tenants, different contents: the collision a shared database invites.
	ea, eb := estate(), estate()
	ea.Services[0].Name = "web-of-A"
	eb.Services[0].Name = "web-of-B"
	eb.Clusters = eb.Clusters[:1]
	record(t, db, a, t0, ea)
	record(t, db, b, t0, eb)
	if err := db.AddEvents(ctx, a, []store.Event{{At: t0, Kind: "service-scaled", TargetKind: "service", TargetID: "s-1", Name: "web-of-A"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddEvents(ctx, b, []store.Event{{At: t0, Kind: "service-scaled", TargetKind: "service", TargetID: "s-1", Name: "web-of-B"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddAudit(ctx, []store.AuditEvent{
		{ID: 1, At: t0, OrgID: a, Actor: "alice", Action: "agent.approve", TargetKind: "agent", TargetID: "s-1"},
		{ID: 2, At: t0, OrgID: b, Actor: "bob", Action: "agent.approve", TargetKind: "agent", TargetID: "s-1"},
	}); err != nil {
		t.Fatal(err)
	}

	sa, _ := db.AsOf(ctx, a, t0)
	sb, _ := db.AsOf(ctx, b, t0)
	if sa.Topology.Services[0].Name != "web-of-A" || sb.Topology.Services[0].Name != "web-of-B" {
		t.Errorf("history crossed: %q / %q", sa.Topology.Services[0].Name, sb.Topology.Services[0].Name)
	}
	if len(sa.Topology.Clusters) != 2 || len(sb.Topology.Clusters) != 1 {
		t.Errorf("cluster counts crossed: %d / %d", len(sa.Topology.Clusters), len(sb.Topology.Clusters))
	}
	if ea, _ := db.Events(ctx, a, store.EventQuery{}); len(ea) != 1 || ea[0].Name != "web-of-A" {
		t.Errorf("A's events: %+v", ea)
	}
	if au, _ := db.Audit(ctx, a, AuditQuery{}); len(au) != 1 || au[0].Actor != "alice" {
		t.Errorf("A's audit: %+v", au)
	}
	if au, _ := db.Audit(ctx, b, AuditQuery{}); len(au) != 1 || au[0].Actor != "bob" {
		t.Errorf("B's audit: %+v", au)
	}
	if tl, err := db.Timeline(ctx, b, "service", "s-1", 10); err != nil || tl.Versions[0].Name != "web-of-B" || len(tl.Events) != 1 || tl.Events[0].Name != "web-of-B" {
		t.Errorf("B's timeline: %+v %v", tl, err)
	}
	// An id that exists only in the other tenant is simply not there.
	if _, err := db.Timeline(ctx, b, "cluster", "c-2", 10); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B saw A's cluster: %v", err)
	}
	// Deleting one tenant leaves the other whole.
	if err := db.PurgeTenant(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AsOf(ctx, a, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("A still has history after purge: %v", err)
	}
	if s, err := db.AsOf(ctx, b, t0); err != nil || len(s.Topology.Services) != 2 {
		t.Errorf("B was damaged by A's purge: %v", err)
	}
	if au, _ := db.Audit(ctx, b, AuditQuery{}); len(au) != 1 {
		t.Errorf("B's audit lost")
	}
}

func TestTimelineShowsWhatChangedAndWhoDidIt(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())
	e := estate()
	e.Services[0].Replicas = 5
	e.Services[0].Image = "web:2"
	record(t, db, org, t0.Add(time.Hour), e)
	_ = db.AddEvents(ctx, org, []store.Event{{At: t0.Add(time.Hour), Kind: "service-scaled", TargetKind: "service", TargetID: "s-1", Name: "web", Detail: "2 → 5"}})
	_ = db.AddAudit(ctx, []store.AuditEvent{{ID: 10, At: t0.Add(time.Hour), OrgID: org, Actor: "alice", Action: "workspace.save", TargetKind: "service", TargetID: "s-1", Detail: "override"}})

	tl, err := db.Timeline(ctx, org, "service", "s-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 2 || tl.Versions[0].To != nil || tl.Versions[1].To == nil {
		t.Fatalf("versions: %+v", tl.Versions)
	}
	got := map[string]bool{}
	for _, c := range tl.Versions[0].Changes {
		got[c.Field] = true
	}
	if !got["replicas"] || !got["image"] || got["orgId"] || got["lastSeen"] {
		t.Errorf("changes: %+v", tl.Versions[0].Changes)
	}
	if len(tl.Versions[1].Changes) != 0 {
		t.Errorf("the first version has nothing before it to differ from")
	}
	if len(tl.Events) != 1 || len(tl.Audit) != 1 || tl.Audit[0].Actor != "alice" {
		t.Errorf("events/audit: %+v %+v", tl.Events, tl.Audit)
	}
	if _, err := db.Timeline(ctx, org, "service", "nope", 10); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown entity: %v", err)
	}
}

func TestEventsKeepTheirOrderFiltersAndPruning(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var evs []store.Event
	for i := 0; i < 10; i++ {
		k := "a"
		if i%2 == 1 {
			k = "b"
		}
		evs = append(evs, store.Event{At: t0.Add(time.Duration(i) * time.Minute), Kind: k, TargetID: fmt.Sprint("t", i%3), Name: fmt.Sprint("e", i), ClusterID: "c-1"})
	}
	if err := db.AddEvents(ctx, org, evs[:5]); err != nil {
		t.Fatal(err)
	}
	if err := db.AddEvents(ctx, org, evs[5:]); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Events(ctx, org, store.EventQuery{})
	if len(all) != 10 || all[0].Name != "e9" || all[9].Name != "e0" {
		t.Fatalf("order: %v", names(all))
	}
	seen := map[int64]bool{}
	for _, e := range all {
		if seen[e.ID] {
			t.Errorf("duplicate id %d", e.ID)
		}
		seen[e.ID] = true
	}
	if b, _ := db.Events(ctx, org, store.EventQuery{Kind: "b"}); len(b) != 5 {
		t.Errorf("kind filter: %d", len(b))
	}
	if w, _ := db.Events(ctx, org, store.EventQuery{Since: t0.Add(3 * time.Minute), Until: t0.Add(5 * time.Minute)}); len(w) != 3 {
		t.Errorf("window: %v", names(w))
	}
	if l, _ := db.Events(ctx, org, store.EventQuery{Limit: 2}); len(l) != 2 {
		t.Errorf("limit: %d", len(l))
	}
	if err := db.PruneEvents(ctx, org, t0.Add(2*time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.Events(ctx, org, store.EventQuery{}); len(left) != 8 {
		t.Errorf("after age prune: %d", len(left))
	}
	if err := db.PruneEvents(ctx, org, t0.Add(-time.Hour), 3); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.Events(ctx, org, store.EventQuery{}); len(left) != 3 || left[0].Name != "e9" {
		t.Errorf("after count prune: %v", names(left))
	}
	// keepNewest <= 0 means age alone decides: it must not be read as "keep newest 0", which would delete
	// everything regardless of age.
	if err := db.PruneEvents(ctx, org, t0.Add(8*time.Minute), 0); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.Events(ctx, org, store.EventQuery{}); len(left) != 2 || names(left)[0] != "e9" || names(left)[1] != "e8" {
		t.Errorf("after age-only prune (keepNewest=0): %v", names(left))
	}
}

func names(evs []store.Event) []string {
	var o []string
	for _, e := range evs {
		o = append(o, e.Name)
	}
	return o
}

func TestThinningSnapshotsKeepsEveryStillRememberedMomentAnswerable(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		e := estate()
		e.Services[0].Replicas = int32(i + 1)
		record(t, db, org, t0.Add(time.Duration(i)*time.Hour), e)
	}
	// Retention drops the middle two moments and the oldest.
	if err := db.DeleteSnapshots(ctx, org, []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s, err := db.AsOf(ctx, org, t0.Add(3*time.Hour))
	if err != nil || s.Topology.Services[0].Replicas != 4 || len(s.Topology.Services) != 2 {
		t.Fatalf("the remaining moment is wrong: %v %+v", err, s.Topology.Services)
	}
	st, _ := db.Stats(ctx, org)
	// The old service versions ended before the oldest remaining moment, so they are gone.
	if st.Snapshots != 1 || st.Versions != 11 {
		t.Errorf("after thinning: %d snapshots, %d versions (want 1 and 11)", st.Snapshots, st.Versions)
	}
}

func TestMembershipsAreProjectedWithTheirHistory(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ti := TenantInfo{ID: org, Name: "Acme", CreatedBy: "u-1", CreatedAt: t0}
	if err := db.SyncTenant(ctx, ti, []MemberInfo{{"alice", "owner", t0}, {"bob", "viewer", t0.Add(time.Hour)}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// bob is promoted, carol joins.
	if err := db.SyncTenant(ctx, ti, []MemberInfo{{"alice", "owner", t0}, {"bob", "editor", t0.Add(time.Hour)}, {"carol", "viewer", t0.Add(3 * time.Hour)}}, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Repeating changes nothing.
	if err := db.SyncTenant(ctx, ti, []MemberInfo{{"alice", "owner", t0}, {"bob", "editor", t0.Add(time.Hour)}, {"carol", "viewer", t0.Add(3 * time.Hour)}}, t0.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (a:Actor {org:$org, name:'bob'})-[m:MEMBER_OF]->(:Tenant {id:$org}) RETURN m.role, toString(m.validFrom), toString(m.validTo) ORDER BY m.validFrom`, nil))
	if err != nil || len(res[0].Rows) != 2 {
		t.Fatalf("bob's roles: %v %v", res, err)
	}
	r := res[0].Rows
	if str(r[0][0]) != "viewer" || str(r[0][2]) == "" || str(r[1][0]) != "editor" || str(r[1][2]) != "" {
		t.Errorf("bob's history: %v", r)
	}
	// Who was a member of what, when: bob was a viewer at 3h.
	who, _ := db.C.Run(ctx, db.C.For(org).S(`MATCH (a:Actor {org:$org})-[m:MEMBER_OF]->(:Tenant {id:$org}) WHERE m.validFrom <= datetime($at) AND (m.validTo IS NULL OR m.validTo > datetime($at)) RETURN a.name, m.role ORDER BY a.name`, map[string]any{"at": ts(t0.Add(3*time.Hour + 30*time.Minute))}))
	if len(who[0].Rows) != 3 || str(who[0].Rows[1][1]) != "viewer" || str(who[0].Rows[2][1]) != "viewer" {
		t.Errorf("membership as of 3h30: %v", who[0].Rows)
	}
}

func TestWorkspaceRevisionsCanBeReadBackAtAnyTime(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 3; i++ {
		if err := db.AppendWorkspace(ctx, org, store.Workspace{OrgID: org, Rev: i, Data: []byte(fmt.Sprintf(`{"n":%d}`, i)), UpdatedAt: t0.Add(time.Duration(i) * time.Hour), UpdatedBy: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	w, err := db.WorkspaceAt(ctx, org, t0.Add(150*time.Minute))
	if err != nil || w.Rev != 2 || string(w.Data) != `{"n":2}` || w.By != "alice" {
		t.Errorf("as of 2h30: %+v %v", w, err)
	}
	if _, err := db.WorkspaceAt(ctx, org, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("before the first: %v", err)
	}
	if revs, _ := db.WorkspaceRevs(ctx, org, 10); len(revs) != 3 || revs[0].Rev != 3 || revs[0].Data != nil {
		t.Errorf("list: %+v", revs)
	}
}

// ---- the composite store ----

func openSQLite(t *testing.T) *store.SQLite {
	t.Helper()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func encoded(t *testing.T, topo model.Topology) []byte {
	t.Helper()
	data, _, err := history.Encode(history.Compact(topo))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHistoryAnEarlierVersionKeptInSQLiteMovesIntoTheGraph(t *testing.T) {
	db, _ := testDB(t)
	org := orgN(t, db)
	ctx := context.Background()
	sq := openSQLite(t)
	if err := sq.CreateUser(ctx, store.User{ID: "u-1", Username: "alice", PasswordHash: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := sq.CreateOrg(ctx, store.Org{ID: org, Name: "Acme", CreatedAt: time.Now(), CreatedBy: "u-1"}, "u-1"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		e := estate()
		e.Services[0].Replicas = int32(i + 1)
		if err := sq.AddHistory(ctx, org, t0.Add(time.Duration(i)*time.Hour), encoded(t, e)); err != nil {
			t.Fatal(err)
		}
	}
	_ = sq.AddEvents(ctx, org, []store.Event{{At: t0, Kind: "cluster-added", Name: "old"}})

	st := Wrap(sq, db, slog.Default())
	// While not prepared, everything reads and writes the buffer.
	if pts, _ := st.ListHistory(ctx, org, time.Time{}, time.Time{}); len(pts) != 3 {
		t.Fatalf("buffer not readable before ready: %d", len(pts))
	}
	st.Sync(ctx)
	if !st.Ready() {
		t.Fatal("not ready")
	}
	if left, _ := sq.ListHistory(ctx, org, time.Time{}, time.Time{}); len(left) != 0 {
		t.Errorf("%d snapshots still in SQLite", len(left))
	}
	if left, _ := sq.ListEvents(ctx, org, store.EventQuery{}); len(left) != 0 {
		t.Errorf("%d events still in SQLite", len(left))
	}
	pts, _ := st.ListHistory(ctx, org, time.Time{}, time.Time{})
	if len(pts) != 3 {
		t.Fatalf("graph has %d points", len(pts))
	}
	p, data, err := st.GetHistory(ctx, org, t0.Add(90*time.Minute))
	if err != nil || !p.At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("get: %v %v", p, err)
	}
	topo, _ := history.Decode(data)
	if topo.Services[0].Replicas != 2 {
		t.Errorf("replicas %d at 1h30", topo.Services[0].Replicas)
	}
	if evs, _ := st.ListEvents(ctx, org, store.EventQuery{}); len(evs) != 1 || evs[0].Name != "old" {
		t.Errorf("events: %+v", evs)
	}

	// New history goes straight to the graph now, in order after the old.
	if err := st.AddHistory(ctx, org, t0.Add(5*time.Hour), encoded(t, estate())); err != nil {
		t.Fatal(err)
	}
	if left, _ := sq.ListHistory(ctx, org, time.Time{}, time.Time{}); len(left) != 0 {
		t.Errorf("new history landed in SQLite")
	}
	if pts, _ := st.ListHistory(ctx, org, time.Time{}, time.Time{}); len(pts) != 4 {
		t.Errorf("%d points", len(pts))
	}
}

func TestWhileTheGraphIsAwayNothingIsLostAndItCatchesUp(t *testing.T) {
	db, _ := testDB(t)
	org := orgN(t, db)
	ctx := context.Background()
	sq := openSQLite(t)
	_ = sq.CreateUser(ctx, store.User{ID: "u-1", Username: "alice", PasswordHash: "x", CreatedAt: time.Now()})
	_ = sq.CreateOrg(ctx, store.Org{ID: org, Name: "Acme", CreatedAt: time.Now(), CreatedBy: "u-1"}, "u-1")
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	st := Wrap(sq, db, slog.Default())
	st.Sync(ctx)
	if err := st.AddHistory(ctx, org, t0, encoded(t, estate())); err != nil {
		t.Fatal(err)
	}
	// Take the database away: point the client at nothing.
	real := db.C
	dead, _ := NewClient(Config{URL: "http://127.0.0.1:1", Timeout: time.Second})
	db.C = dead
	e := estate()
	e.Services[0].Replicas = 7
	if err := st.AddHistory(ctx, org, t0.Add(time.Hour), encoded(t, e)); err != nil {
		t.Fatalf("a snapshot taken while the graph is away must still be kept: %v", err)
	}
	if err := st.AddEvents(ctx, org, []store.Event{{At: t0.Add(time.Hour), Kind: "service-scaled", Name: "during-outage"}}); err != nil {
		t.Fatal(err)
	}
	// It can still be read back, from the buffer.
	if _, data, err := st.GetHistory(ctx, org, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("read during outage: %v", err)
	} else if topo, _ := history.Decode(data); topo.Services[0].Replicas != 7 {
		t.Errorf("outage read is stale")
	}
	if evs, _ := st.ListEvents(ctx, org, store.EventQuery{}); len(evs) != 1 {
		t.Errorf("events during outage: %d", len(evs))
	}
	if err := st.AddHistory(ctx, org, t0.Add(2*time.Hour), encoded(t, e)); err != nil {
		t.Fatal(err)
	}
	if got := st.Status(ctx, org); got.Connected || !got.Buffering {
		t.Errorf("status during outage: %+v", got)
	}

	// It comes back.
	db.C = real
	st.Sync(ctx)
	if left, _ := sq.ListHistory(ctx, org, time.Time{}, time.Time{}); len(left) != 0 {
		t.Errorf("%d snapshots still buffered after recovery", len(left))
	}
	pts, _ := st.ListHistory(ctx, org, time.Time{}, time.Time{})
	if len(pts) != 3 {
		t.Fatalf("after recovery the graph has %d points, want 3", len(pts))
	}
	s, _ := db.AsOf(ctx, org, t0.Add(90*time.Minute))
	if s.Topology.Services[0].Replicas != 7 {
		t.Errorf("the buffered change was not applied")
	}
	if evs, _ := st.ListEvents(ctx, org, store.EventQuery{}); len(evs) != 1 || evs[0].Name != "during-outage" {
		t.Errorf("events after recovery: %+v", evs)
	}
	if got := st.Status(ctx, org); !got.Connected || got.Buffering {
		t.Errorf("status after recovery: %+v", got)
	}
}

func TestAuditTenantsAndWorkspaceFlowFromTheControlStoreIntoTheGraph(t *testing.T) {
	db, _ := testDB(t)
	org := orgN(t, db)
	ctx := context.Background()
	sq := openSQLite(t)
	now := time.Now()
	_ = sq.CreateUser(ctx, store.User{ID: "u-1", Username: "alice", PasswordHash: "x", CreatedAt: now})
	_ = sq.CreateUser(ctx, store.User{ID: "u-2", Username: "bob", PasswordHash: "x", CreatedAt: now})
	if err := sq.CreateOrg(ctx, store.Org{ID: org, Name: "Acme", CreatedAt: now, CreatedBy: "u-1"}, "u-1"); err != nil {
		t.Fatal(err)
	}
	_ = sq.AddMember(ctx, store.Membership{OrgID: org, UserID: "u-2", Role: "viewer", CreatedAt: now, AddedBy: "u-1"})
	_ = sq.AddAudit(ctx, store.AuditEvent{At: now, OrgID: org, Actor: "alice", Action: "member.invite", TargetKind: "invite", TargetID: "i-1"})
	_ = sq.AddAudit(ctx, store.AuditEvent{At: now, OrgID: "", Actor: "operator", Action: "server.start"})

	st := Wrap(sq, db, slog.Default())
	st.Sync(ctx)
	if _, err := st.PutWorkspace(ctx, org, 0, []byte(`{"a":1}`), "alice", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutWorkspace(ctx, org, 1, []byte(`{"a":2}`), "bob", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st.Sync(ctx)

	au, err := st.Audit(ctx, org, AuditQuery{})
	if err != nil || len(au) != 1 || au[0].Actor != "alice" || au[0].Action != "member.invite" {
		t.Fatalf("audit: %+v %v", au, err)
	}
	if got, _ := st.Audit(ctx, org, AuditQuery{Actor: "bob"}); len(got) != 0 {
		t.Errorf("filter by actor: %+v", got)
	}
	// The server's own audit is kept apart and no tenant can read it.
	if got, _ := db.Audit(ctx, serverOrg, AuditQuery{}); len(got) < 1 {
		t.Errorf("server audit missing")
	}
	_ = db.PurgeTenant(ctx, serverOrg)

	res, _ := db.C.Run(ctx, db.C.For(org).S(`MATCH (a:Actor {org:$org})-[m:MEMBER_OF]->(:Tenant {id:$org, name:'Acme'}) WHERE m.validTo IS NULL RETURN a.name, m.role ORDER BY a.name`, nil))
	if len(res[0].Rows) != 2 || str(res[0].Rows[0][0]) != "alice" || str(res[0].Rows[0][1]) != "owner" || str(res[0].Rows[1][1]) != "viewer" {
		t.Errorf("members: %v", res[0].Rows)
	}
	revs, _ := st.WorkspaceRevs(ctx, org, 10)
	if len(revs) != 2 || revs[0].By != "bob" {
		t.Errorf("workspace revisions: %+v", revs)
	}
	// Running again must not duplicate anything.
	st.lastRec = time.Time{}
	st.Sync(ctx)
	if au2, _ := st.Audit(ctx, org, AuditQuery{}); len(au2) != 1 {
		t.Errorf("audit duplicated: %d", len(au2))
	}

	// Deleting the organisation removes what the graph holds for it and nothing else.
	if err := st.DeleteOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AsOf(ctx, org, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("history remains: %v", err)
	}
	if au3, _ := db.Audit(ctx, org, AuditQuery{}); len(au3) != 0 {
		t.Errorf("audit remains")
	}
	// (Other tenants may exist in a shared test database; what matters is that this one is gone.)
	for _, o := range st.Orphans(ctx) {
		if o == org {
			t.Errorf("the deleted organisation is still in the graph: %v", o)
		}
	}
}

func TestAServerWithNoOrganisationsCannotWipeTheMemory(t *testing.T) {
	// A fresh control database pointed at an old graph must not delete what it does not know.
	db, _ := testDB(t)
	org := orgN(t, db)
	ctx := context.Background()
	record(t, db, org, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), estate())
	if err := db.SyncTenant(ctx, TenantInfo{ID: org, Name: "Old", CreatedAt: time.Now()}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	st := Wrap(openSQLite(t), db, slog.Default())
	st.Sync(ctx)
	st.lastRec = time.Time{}
	st.Sync(ctx)
	if _, err := db.AsOf(ctx, org, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("history was deleted by a reconcile: %v", err)
	}
	found := false
	for _, o := range st.Orphans(ctx) {
		found = found || o == org
	}
	if !found {
		t.Errorf("the orphan should be reported, not deleted")
	}
}

func TestBadConfigurationIsRefusedClearly(t *testing.T) {
	for _, u := range []string{"", "neo4j://host:7687", "http://user:pw@host:7474", "not a url"} {
		if _, err := NewClient(Config{URL: u}); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	c, err := NewClient(Config{URL: "http://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Run(context.Background(), Global("RETURN 1", nil))
	if !errors.Is(err, ErrUnavailable) || c.Healthy() {
		t.Errorf("an unreachable database should read as unavailable: %v", err)
	}
	// While marked down, calls fail at once instead of each waiting for a timeout.
	start := time.Now()
	_, _ = c.Run(context.Background(), Global("RETURN 1", nil))
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("a call while down took %v", time.Since(start))
	}
}

func TestWrongPasswordIsReportedWithoutLeakingIt(t *testing.T) {
	u := os.Getenv("CONTINUUM_TEST_NEO4J")
	if u == "" {
		t.Skip("set CONTINUUM_TEST_NEO4J to run the Neo4j tests")
	}
	c, _ := NewClient(Config{URL: u, User: "neo4j", Password: "definitely-wrong-password"})
	err := c.Ping(context.Background())
	if err == nil || strings.Contains(err.Error(), "definitely-wrong-password") {
		t.Errorf("err = %v", err)
	}
}

// The tenant parameter is supplied by the scope and enforced by the client, not by a substring.
func TestTheClientRefusesStatementsThatAreNotTenantScoped(t *testing.T) {
	// No server: everything below must be refused before anything is sent.
	c, err := NewClient(Config{URL: "http://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	refused := func(name string, stmts ...Stmt) {
		t.Helper()
		_, err := c.Run(ctx, stmts...)
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: want a refusal, got %v", name, err)
		}
	}
	// A statement assembled by hand, with or without the parameter, has no scope.
	refused("bare literal", Stmt{Q: "MATCH (n) RETURN n"})
	refused("literal that mentions $org", Stmt{Q: "MATCH (n {org:$org}) RETURN n", P: map[string]any{"org": "victim"}})
	// A scoped statement whose parameter was changed after it was built.
	st := c.For("mine").S(`MATCH (n {org:$org}) RETURN n`, nil)
	st.P["org"] = "victim"
	refused("tampered $org", st)
	st2 := c.For("mine").S(`MATCH (n {org:$org}) RETURN n`, nil)
	delete(st2.P, "org")
	refused("removed $org", st2)
	// A scoped statement, mixed with a global one, cannot smuggle a bare one along.
	refused("one bad statement among good ones", c.For("mine").S(`MATCH (n {org:$org}) RETURN n`, nil), Stmt{Q: "MATCH (n) DETACH DELETE n"})
	// A scope runs only what it built.
	if _, err := c.For("mine").Run(ctx, c.For("other").S(`MATCH (n {org:$org}) RETURN n`, nil)); err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("a scope ran another tenant's statement: %v", err)
	}
	if _, err := c.For("mine").Run(ctx, Global("RETURN 1", nil)); err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("a scope ran a global statement: %v", err)
	}
	// A global statement must not use $org.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Global accepted a query that uses $org")
			}
		}()
		Global("MATCH (n {org:$org}) RETURN n", nil)
	}()
	// Well-formed ones get as far as the network (which is not there).
	if _, err := c.Run(ctx, c.For("mine").S(`MATCH (n {org:$org}) RETURN n`, nil)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a scoped statement should be sent: %v", err)
	}
	if _, err := c.Run(ctx, Global("RETURN 1", nil)); !errors.Is(err, ErrUnavailable) && err != nil && !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("a global statement should be sent: %v", err)
	}
}

func TestOrgParameterDetectionIsStructural(t *testing.T) {
	for q, want := range map[string]bool{
		`MATCH (n {org:$org}) RETURN n`:              true,
		`MATCH (n) WHERE n.org=$org`:                 true,
		`MATCH (n {org:$org})`:                       true,
		`MATCH (n) WHERE n.org=$organisation`:        false, // a different parameter
		`MATCH (n) WHERE n.org=$org_x`:               false,
		`MATCH (n) RETURN '$org'`:                    false, // inside a string
		`MATCH (n) RETURN "a $org b"`:                false,
		"MATCH (n) // filter by $org\nRETURN n":      false, // in a comment
		"MATCH (n) /* $org */ RETURN n":              false,
		`MATCH (n) RETURN 'it\'s' + $org`:            true, // escaped quote does not end the string early
		"MATCH (n {org:$org}) // done":               true,
		"MATCH (`$org`) RETURN 1":                    false, // quoted identifier
		`MATCH (n) WHERE n.x = 'a' AND n.org = $org`: true,
		`MATCH (n) RETURN n`:                         false,
		`UNWIND $orgs AS o MATCH (n) RETURN n`:       false,
		"MATCH (n) /* unterminated $org":             false,
	} {
		if got := usesParam(q, "org"); got != want {
			t.Errorf("%q: got %v, want %v", q, got, want)
		}
	}
}

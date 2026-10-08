package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	return testDBTimeout(t, 0)
}

// testDBTimeout is testDB with the client's own per-request timeout overridden (0 keeps NewClient's
// usual 30s default) - for the one test whose own setup, not the code under test, pushes an unusually
// large bulk write through Neo4j (thousands of entities in a single transaction). A slow-but-genuine
// response to that write is not the same thing as the database being unreachable, and walkTimeout (see
// record.go) still bounds the walk under test on its own, independent, shorter budget regardless of what
// this client's socket timeout is set to - so raising it here does not weaken what such a test proves.
func testDBTimeout(t *testing.T, timeout time.Duration) (*DB, string) {
	t.Helper()
	u := os.Getenv("CONTINUUM_TEST_NEO4J")
	if u == "" {
		t.Skip("set CONTINUUM_TEST_NEO4J to run the Neo4j tests")
	}
	user := os.Getenv("CONTINUUM_TEST_NEO4J_USER")
	if user == "" {
		user = "neo4j"
	}
	c, err := NewClient(Config{URL: u, User: user, Password: os.Getenv("CONTINUUM_TEST_NEO4J_PASSWORD"), Timeout: timeout})
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
	if global != 6 {
		t.Errorf("found %d raw statements outside a tenant scope; expected the 6 known ones (schema, schema version, schema migration, schema meta, ping, tenant ids). Review any new one.", global)
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

// TestRecordEntitiesMatchesRecordEntityOneByOne checks the batch call against the exact same scenario
// TestRecordEntityVersionsSomethingOutsideThePolledTopology already proves for the single-entity call:
// a repeat that changed nothing opens no second version, a real change opens one, and an entity moving
// cluster moves its one open IN_CLUSTER edge rather than accumulating a second - all for several entities
// written in the same call.
func TestRecordEntitiesMatchesRecordEntityOneByOne(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // clusters c-1/c-2 exist

	type doc struct {
		AccessTier int `json:"accessTier"`
	}
	recs := []EntityRecord{
		{ID: "ag-1", Name: "edge-collector", Cluster: "c-1", Doc: doc{AccessTier: 1}},
		{ID: "ag-2", Name: "core-collector", Cluster: "c-1", Doc: doc{AccessTier: 1}},
	}
	if err := db.RecordEntities(ctx, org, t0.Add(time.Minute), "agent", recs); err != nil {
		t.Fatalf("record entities: %v", err)
	}
	// Identical again: must not open a second version for either.
	if err := db.RecordEntities(ctx, org, t0.Add(time.Minute), "agent", recs); err != nil {
		t.Fatalf("record entities (repeat): %v", err)
	}
	// ag-1 changes and moves cluster; ag-2 is untouched.
	recs2 := []EntityRecord{
		{ID: "ag-1", Name: "edge-collector", Cluster: "c-2", Doc: doc{AccessTier: 2}},
		{ID: "ag-2", Name: "core-collector", Cluster: "c-1", Doc: doc{AccessTier: 1}},
	}
	if err := db.RecordEntities(ctx, org, t0.Add(2*time.Hour), "agent", recs2); err != nil {
		t.Fatalf("record entities (change): %v", err)
	}

	for id, want := range map[string]int{"ag-1": 2, "ag-2": 1} {
		tl, err := db.Timeline(ctx, org, "agent", id, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(tl.Versions) != want {
			t.Fatalf("%s: expected %d versions, got %+v", id, want, tl.Versions)
		}
	}

	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'agent', id:'ag-1'})-[r:IN_CLUSTER {org:$org}]->(c:Cluster) WHERE r.validTo IS NULL RETURN c.id`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Rows) != 1 || str(res[0].Rows[0][0]) != "c-2" {
		t.Fatalf("ag-1's cluster edge did not move as expected: %v", res[0].Rows)
	}

	if err := db.RecordEntities(ctx, org, t0, "not-a-kind", []EntityRecord{{ID: "x"}}); err == nil {
		t.Error("an unknown kind should be refused")
	}
	if err := db.RecordEntities(ctx, org, t0, "agent", nil); err != nil {
		t.Errorf("an empty batch should be a no-op, not an error: %v", err)
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

// volatileEstate is estate() with the live readings a tier-2 agent and the flow collector attach filled in;
// bump moves every one of them, the way the next window would.
func volatileEstate(bump float64) model.Topology {
	e := estate()
	f := func(v float64) *float64 { x := v + bump; return &x }
	u := func(v uint32) *uint32 { x := v + uint32(bump); return &x }
	n := int32(3 + bump)
	e.Nodes[0].PodCount = &n
	e.Nodes[0].Requested = &model.Resources{}
	e.Nodes[0].CPUPressurePct, e.Nodes[0].MemoryPressurePct, e.Nodes[0].IOPressurePct, e.Nodes[0].HostWatts = f(1), f(2), f(3), f(4)
	om := uint64(7 + bump)
	e.Nodes[0].OomKillCount = &om
	e.Nodes[0].LinkSaturation = []model.LinkSaturation{{Iface: "eth0", ThroughputBps: uint64(100 + bump)}}
	e.Nodes[0].SnatExhaustion, e.Nodes[0].CpuFreqChangeCount, e.Nodes[0].ThermalTripCount = uint64(bump), uint64(bump), uint64(bump)
	e.Services[0].Pods = []model.Pod{{Name: "web-0", Phase: "Running", Ready: true, Traffic: []model.PodPeer{{Peer: "s-2", PeerKind: "service", Direction: "out", Port: 5432, Connections: uint64(bump)}}}}
	d := &e.Dependencies[0]
	d.Retransmits, d.RtoRetransmits, d.FailedAttempts, d.BufferDrops = uint64(bump), uint64(bump), uint64(bump), uint64(bump)
	d.RttMs, d.JitterMs, d.HandshakeMs, d.DnsRttMs = 5+bump, bump, bump, bump
	d.CwndSegments, d.PacingBps, d.MssBytes = uint32(bump), uint64(bump), uint32(bump)
	d.RcvWndBytes, d.SndWndBytes, d.WmemQueuedBytes, d.SndbufBytes = u(1), u(2), u(3), u(4)
	d.DnsQueryNames = []string{fmt.Sprintf("a%v.example", bump)}
	d.Stats.LossPct = f(0.1)
	return e
}

// TestVolatileReadingsAreNotVersioned: pod traffic, node pressure and counters, a dependency's kernel
// gauges move on every window. They must neither change an entity's hash (a new Version per entity per
// recording) nor be stored in its document; the two a history reader draws (round-trip time, loss) ride on
// the snapshot with the other counters.
func TestVolatileReadingsAreNotVersioned(t *testing.T) {
	a, _, tra, _ := extract(volatileEstate(0))
	b, _, trb, _ := extract(volatileEstate(10))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("extract found %d and %d entities", len(a), len(b))
	}
	for k, va := range a {
		if vb := b[k]; va.Hash != vb.Hash {
			t.Errorf("%s changed with nothing but live readings changing:\n%s\n%s", k, va.Doc, vb.Doc)
		}
	}
	if tra["d-1"].Rtt != 5 || trb["d-1"].Rtt != 15 || trb["d-1"].Loss == nil || *trb["d-1"].Loss != 10.1 {
		t.Errorf("rtt and loss should ride on the snapshot counters: %+v / %+v", tra["d-1"], trb["d-1"])
	}
	// A real change to the same service still counts.
	e := volatileEstate(0)
	e.Services[0].Pods[0].Ready = false
	c, _, _, _ := extract(e)
	if c[vkey("service", "s-1")].Hash == a[vkey("service", "s-1")].Hash {
		t.Error("a pod going not-ready is a fact and must make a new version")
	}
	// extract must not edit the topology it was given (the live hub's state shares these slices).
	e = volatileEstate(0)
	extract(e)
	if len(e.Services[0].Pods[0].Traffic) != 1 || e.Dependencies[0].RttMs != 5 {
		t.Errorf("extract wrote into its input: %+v", e.Services[0].Pods[0])
	}
}

// TestRecordingOnlyLiveReadingsWritesNoNewVersions is the same through the database, and checks the
// round-trip time and loss come back with the moment they were recorded at.
func TestRecordingOnlyLiveReadingsWritesNoNewVersions(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, volatileEstate(0))
	before, err := db.Stats(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	record(t, db, org, t0.Add(time.Hour), volatileEstate(10))
	after, err := db.Stats(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if after.Versions != before.Versions || after.Snapshots != before.Snapshots+1 {
		t.Errorf("versions %d -> %d, snapshots %d -> %d: only the snapshot should be new", before.Versions, after.Versions, before.Snapshots, after.Snapshots)
	}
	for i, want := range []float64{5, 15} {
		s, err := db.AsOf(ctx, org, t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		d := s.Topology.Dependencies[0]
		if d.RttMs != want || d.Stats == nil || d.Stats.LossPct == nil || *d.Stats.LossPct != 0.1+float64(i)*10 {
			t.Errorf("moment %d: rtt/loss not restored: %+v %+v", i, d, d.Stats)
		}
	}
}

// openFlagMismatches counts versions and links whose open flag disagrees with validTo: every current
// one must have open=true and every closed one none, whichever write path made or closed it.
func openFlagMismatches(t *testing.T, db *DB, org string) int64 {
	t.Helper()
	q := []string{`MATCH (v:Version {org:$org}) WHERE (v.validTo IS NULL) <> (v.open IS NOT NULL) OR v.open = false RETURN count(v)`}
	for _, rt := range relTypes {
		q = append(q, fmt.Sprintf(`MATCH ()-[r:%s {org:$org}]->() WHERE (r.validTo IS NULL) <> (r.open IS NOT NULL) OR r.open = false RETURN count(r)`, rt))
	}
	var n int64
	for _, s := range q {
		res, err := db.C.Run(context.Background(), db.C.For(org).S(s, nil))
		if err != nil {
			t.Fatal(err)
		}
		n += i64(res[0].Rows[0][0])
	}
	return n
}

// TestEveryWritePathKeepsTheOpenFlagInStepWithValidTo: Record reads what is open by the flag alone (an
// index seek) instead of scanning all history for validTo IS NULL, so a path that forgot to set or clear
// it would make Record see the wrong state.
func TestEveryWritePathKeepsTheOpenFlagInStepWithValidTo(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	record(t, db, org, at(0), estate())
	e1 := estate()
	e1.Services[0].Replicas = 9 // a new version
	e1.Dependencies = e1.Dependencies[1:]
	e1.Paths = nil // links and entities close
	record(t, db, org, at(10), e1)
	record(t, db, org, at(10), e1) // the same instant again replaces rather than breaks
	if _, _, err := db.RecordCatchUp(ctx, org, []CatchUpPoint{{At: at(20), Topo: estate(), FP: "x", Size: 1}, {At: at(30), Topo: e1, FP: "y", Size: 1}}); err != nil {
		t.Fatal(err)
	}

	type doc struct{ N int }
	for i, cl := range []string{"c-1", "c-2", "c-1"} { // an agent that moves away and comes back
		if err := db.RecordEntity(ctx, org, at(40+i), "agent", "ag-1", "a", "ok", cl, doc{i}); err != nil {
			t.Fatal(err)
		}
	}
	for i, cl := range []string{"c-1", "c-2"} {
		if err := db.RecordEntities(ctx, org, at(50+i), "agent", []EntityRecord{{ID: "ag-2", Name: "b", Cluster: cl, Doc: doc{i}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordEntity(ctx, org, at(60), "application", "app-1", "shop", "", "", doc{1}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordEntity(ctx, org, at(61), "application", "app-2", "other", "", "", doc{1}); err != nil {
		t.Fatal(err)
	}
	for i, ids := range [][]string{{"s-1", "s-2"}, {"s-1"}} {
		if err := db.LinkEntities(ctx, org, at(70+i), "CONTAINS", "application", "app-1", "service", ids); err != nil {
			t.Fatal(err)
		}
		if err := db.LinkEntitiesBatch(ctx, org, at(70+i), "CONTAINS", "application", "service", []MemberSet{{ID: "app-2", TargetIDs: ids}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CloseMissingEntities(ctx, org, at(80), "application", []string{"app-1"}); err != nil {
		t.Fatal(err)
	}

	if n := openFlagMismatches(t, db, org); n != 0 {
		t.Errorf("%d versions or links have an open flag that disagrees with validTo", n)
	}
	// And the agent that came back to c-1 has exactly one open cluster link, to c-1.
	res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'agent', id:'ag-1'})-[r:IN_CLUSTER {org:$org}]->(c:Cluster) WHERE r.validTo IS NULL RETURN c.id`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Rows) != 1 || str(res[0].Rows[0][0]) != "c-1" {
		t.Errorf("an agent that returned to its first cluster should be linked to it again: %v", res[0].Rows)
	}
}

// TestEnsureFlagsWhatWasOpenBeforeTheFlagExisted: a graph written by schema 2 has no open flag; Ensure
// must set it on exactly the current versions and links, or Record would see an empty estate and write
// every entity a second time.
func TestEnsureFlagsWhatWasOpenBeforeTheFlagExisted(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	e := estate()
	record(t, db, org, t0, e)
	e.Services = e.Services[:1] // db goes: closed versions and links exist
	e.Dependencies = e.Dependencies[1:]
	record(t, db, org, t0.Add(time.Hour), e)

	// Put the graph back as schema 2 left it.
	sc := db.C.For(org)
	stmts := []Stmt{
		sc.S(`MATCH (v:Version {org:$org}) REMOVE v.open`, nil),
		Global(`MERGE (m:SchemaMeta {id:'schema'}) SET m.version = 2`, nil),
	}
	for _, rt := range relTypes {
		stmts = append(stmts, sc.S(fmt.Sprintf(`MATCH ()-[r:%s {org:$org}]->() REMOVE r.open`, rt), nil))
	}
	if _, err := db.C.Run(ctx, stmts...); err != nil {
		t.Fatal(err)
	}
	if n := openFlagMismatches(t, db, org); n == 0 {
		t.Fatal("setup did not remove the flags")
	}

	if err := db.C.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if n := openFlagMismatches(t, db, org); n != 0 {
		t.Errorf("%d versions or links are still unflagged or wrongly flagged after Ensure", n)
	}
	// The next recording of the same estate finds nothing to write: all it adds is the snapshot.
	before, _ := db.Stats(ctx, org)
	record(t, db, org, t0.Add(2*time.Hour), e)
	after, _ := db.Stats(ctx, org)
	if after.Versions != before.Versions {
		t.Errorf("recording an unchanged estate after the migration wrote %d versions", after.Versions-before.Versions)
	}
}

// TestRecordLeavesAnEntityItDidNotWriteAlone: Record's entity sweep closes and marks gone every open
// version a topology does not contain, which for the kinds RecordEntity writes (agent, application) is
// all of them, since no topology ever carries those. Both Record and RecordCatchUp must leave them open.
func TestRecordLeavesAnEntityItDidNotWriteAlone(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	e := estate()
	record(t, db, org, t0, e)
	if err := db.RecordEntity(ctx, org, t0.Add(time.Minute), "application", "app-1", "shop", "", "", map[string]any{"name": "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordEntity(ctx, org, t0.Add(time.Minute), "agent", "ag-1", "edge-collector", "approved", "c-1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}

	record(t, db, org, t0.Add(2*time.Minute), e)
	if _, _, err := db.RecordCatchUp(ctx, org, []CatchUpPoint{{At: t0.Add(3 * time.Minute), Topo: e, FP: "fp", Size: 10}}); err != nil {
		t.Fatal(err)
	}

	for _, k := range [][2]string{{"application", "app-1"}, {"agent", "ag-1"}} {
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (e:Entity {org:$org, kind:$kind, id:$id}) OPTIONAL MATCH (e)-[:HAS_VERSION]->(v:Version) WHERE v.validTo IS NULL
RETURN e.gone IS NULL, count(v)`, map[string]any{"kind": k[0], "id": k[1]}))
		if err != nil {
			t.Fatal(err)
		}
		if len(res[0].Rows) != 1 || res[0].Rows[0][0] != true || i64(res[0].Rows[0][1]) != 1 {
			t.Errorf("%s %s should still be live with exactly one open version, got %v", k[0], k[1], res[0].Rows)
		}
	}
	// And RecordEntity on the same document is still a no-op rather than a duplicate version.
	if err := db.RecordEntity(ctx, org, t0.Add(4*time.Minute), "application", "app-1", "shop", "", "", map[string]any{"name": "shop"}); err != nil {
		t.Fatal(err)
	}
	tl, err := db.Timeline(ctx, org, "application", "app-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 1 {
		t.Errorf("expected the one version, got %+v", tl.Versions)
	}
}

func hasReached(rs []Reached, kind, id string) bool {
	for _, r := range rs {
		if r.Kind == kind && r.ID == id {
			return true
		}
	}
	return false
}

// TestDependentsWalksBackwardAcrossRelationshipTypes checks the core claim behind "blast radius": every
// relationship type this schema writes points from the dependent thing to the thing it depends on, so
// walking backward from an entity finds what would notice if it disappeared - across more than one
// relationship type in the same walk, not just the one nearest the start.
func TestDependentsWalksBackwardAcrossRelationshipTypes(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // s-1 CALLS s-2; both IN_CLUSTER their own cluster; p-1 PATH_TO c-2

	sat, one, err := db.Dependents(ctx, org, t0, "service", "s-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sat.Equal(t0) {
		t.Errorf("Dependents should resolve to the instant asked for: %v", sat)
	}
	if !hasReached(one, "service", "s-1") {
		t.Errorf("s-1 calls s-2, so it should be one hop away: %v", one)
	}
	if hasReached(one, "cluster", "c-2") {
		t.Errorf("c-2 does not depend on s-2, it hosts it: %v", one)
	}

	_, two, err := db.Dependents(ctx, org, t0, "cluster", "c-2", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind, id string
		hop      int
	}{{"node", "n-2", 1}, {"service", "s-2", 1}, {"path", "p-1", 1}, {"service", "s-1", 2}}
	for _, w := range want {
		found := false
		for _, r := range two {
			if r.Kind == w.kind && r.ID == w.id {
				found = true
				if r.Hops != w.hop {
					t.Errorf("%s/%s: expected %d hops from c-2, got %d", w.kind, w.id, w.hop, r.Hops)
				}
			}
		}
		if !found {
			t.Errorf("walking backward from c-2 to 2 hops should reach %s/%s: %v", w.kind, w.id, two)
		}
	}
}

// TestDependenciesWalksForwardTheOppositeDirection is Dependents' mirror image: what an entity itself
// needs, found by walking the same edges the other way.
func TestDependenciesWalksForwardTheOppositeDirection(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	_, one, err := db.Dependencies(ctx, org, t0, "service", "s-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"service", "external", "cluster", "node"} {
		found := false
		for _, r := range one {
			if r.Kind == want {
				found = true
			}
		}
		if !found {
			t.Errorf("s-1 depends directly on something of kind %s: %v", want, one)
		}
	}
	if hasReached(one, "cluster", "c-2") {
		t.Errorf("c-2 is two hops out (via s-2), should not appear at hops=1: %v", one)
	}

	_, two, err := db.Dependencies(ctx, org, t0, "service", "s-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReached(two, "cluster", "c-2") {
		t.Errorf("s-1 depends on s-2, which belongs to c-2, two hops out: %v", two)
	}
}

// TestDependentsRespectsTheMomentAsked confirms the walk is temporal, not just structural: an edge
// that closed before the moment asked about must not connect anything, the same discipline AsOfEntities
// already holds for a single entity's own doc.
func TestDependentsRespectsTheMomentAsked(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	after := estate()
	after.Dependencies = after.Dependencies[1:] // drop d-1 (s-1 -> s-2): s-1 no longer calls s-2
	t1 := t0.Add(time.Hour)
	record(t, db, org, t1, after)

	_, before, err := db.Dependents(ctx, org, t0, "service", "s-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReached(before, "service", "s-1") {
		t.Errorf("at t0, s-1 still called s-2: %v", before)
	}
	_, atT1, err := db.Dependents(ctx, org, t1, "service", "s-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if hasReached(atT1, "service", "s-1") {
		t.Errorf("at t1, s-1 no longer calls s-2, so it should not be a dependent: %v", atT1)
	}
}

func TestDependentsOfAnUnknownEntityIsNotFound(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	record(t, db, org, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), estate())
	if _, _, err := db.Dependents(ctx, org, time.Now(), "service", "does-not-exist", 3); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown entity, got %v", err)
	}
}

// TestWalkHopsAreClamped guards the reason a caller-supplied hop count is bounded before it drives the
// walk: however far past reason it goes, the walk should stop opening new levels at the same place.
func TestWalkHopsAreClamped(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())
	_, atMax, err := db.Dependencies(ctx, org, t0, "service", "s-1", maxWalkHops)
	if err != nil {
		t.Fatal(err)
	}
	_, overMax, err := db.Dependencies(ctx, org, t0, "service", "s-1", maxWalkHops*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(atMax) != len(overMax) {
		t.Errorf("a hop count far beyond maxWalkHops should clamp to the same result: %d vs %d", len(atMax), len(overMax))
	}
}

// TestWalkHopsBelowOneClampToOne is TestWalkHopsAreClamped's missing other half: the doc comment on the
// clamp itself (record.go) claims hops < 1 becomes 1, but nothing previously proved that specific number -
// only that a caller-supplied hops=0 or a negative value did not error. Unclamped, hops=0 would make the
// walk's own "for h := 1; h <= hops" loop never run at all, reporting nothing; s-1 has four direct
// forward edges in estate() across every relationship type Dependencies walks, not just CALLS - IN_CLUSTER
// to c-1, RUNS_ON to n-1, and CALLS to s-2 and ext-1 - so a real clamp to 1 is visible as "found all four
// direct neighbors," not just "found no fewer than some other call."
func TestWalkHopsBelowOneClampToOne(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	_, atOne, err := db.Dependencies(ctx, org, t0, "service", "s-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(atOne) != 4 {
		t.Fatalf("sanity check: hops=1 should reach s-1's four direct neighbors (c-1, n-1, s-2, ext-1), got %d: %+v", len(atOne), atOne)
	}

	for _, bad := range []int{0, -1, -5} {
		_, got, err := db.Dependencies(ctx, org, t0, "service", "s-1", bad)
		if err != nil {
			t.Fatalf("hops=%d: %v", bad, err)
		}
		if len(got) != len(atOne) {
			t.Errorf("hops=%d should clamp to 1 and match hops=1's result (%d reached), got %d: %+v", bad, len(atOne), len(got), got)
		}
	}
}

// topoStep1 and topoStep2 build a short, deliberately eventful history on top of estate(): a version
// change (s-1 scales from 2 replicas to 3) and then an entity actually disappearing (s-2, along with the
// dependency that pointed at it) - between them they exercise every branch diffEntities has to get right
// when several snapshots are chained in one RecordCatchUp batch rather than applied one Record call at a
// time: closing a version, opening a new one, marking an entity gone, and closing the edge that named it.
func topoStep1() model.Topology {
	t := estate()
	svcs := append([]model.Service{}, t.Services...)
	svcs[0].Replicas = 3
	t.Services = svcs
	return t
}

func topoStep2() model.Topology {
	t := topoStep1()
	var svcs []model.Service
	for _, s := range t.Services {
		if s.ID != "s-2" {
			svcs = append(svcs, s)
		}
	}
	t.Services = svcs
	var deps []model.Dependency
	for _, d := range t.Dependencies {
		if d.To != "s-2" {
			deps = append(deps, d)
		}
	}
	t.Dependencies = deps
	return t
}

// TestRecordCatchUpMatchesSequentialRecordCalls is RecordCatchUp's central correctness claim: replaying
// several buffered snapshots as one batched transaction has to leave the graph exactly as calling Record
// once per snapshot, in order, would have - a version closed and reopened, then an entity retired
// entirely, chained across three points in one call rather than three separate ones. Passed out of order
// on purpose, so this also proves RecordCatchUp sorts its input rather than trusting the caller to.
func TestRecordCatchUpMatchesSequentialRecordCalls(t *testing.T) {
	db, orgSeq := testDB(t)
	orgBatch := orgN(t, db)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t1.Add(time.Hour)
	topos := []model.Topology{estate(), topoStep1(), topoStep2()}
	times := []time.Time{t0, t1, t2}

	for i, topo := range topos {
		record(t, db, orgSeq, times[i], topo)
	}

	var points []CatchUpPoint
	for i, topo := range topos {
		c := history.Compact(topo)
		data, fp, err := history.Encode(c)
		if err != nil {
			t.Fatal(err)
		}
		points = append(points, CatchUpPoint{At: times[i], Topo: c, FP: fp, Size: len(data)})
	}
	points[0], points[2] = points[2], points[0] // shuffled: newest first, oldest last
	applied, dropped, err := db.RecordCatchUp(ctx, orgBatch, points)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 3 || len(dropped) != 0 {
		t.Fatalf("expected all 3 points applied and none dropped, got applied=%d dropped=%v", applied, dropped)
	}

	_, snapSeq, err := db.AsOfEntities(ctx, orgSeq, t2.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, snapBatch, err := db.AsOfEntities(ctx, orgBatch, t2.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	key := func(e EntitySnapshot) string { return e.Kind + "/" + e.ID }
	sort.Slice(snapSeq, func(i, j int) bool { return key(snapSeq[i]) < key(snapSeq[j]) })
	sort.Slice(snapBatch, func(i, j int) bool { return key(snapBatch[i]) < key(snapBatch[j]) })
	if len(snapSeq) != len(snapBatch) {
		t.Fatalf("sequential Record left %d entities, RecordCatchUp left %d:\n  sequential: %+v\n  batched:    %+v", len(snapSeq), len(snapBatch), snapSeq, snapBatch)
	}
	for i := range snapSeq {
		a, b := snapSeq[i], snapBatch[i]
		if a.Kind != b.Kind || a.ID != b.ID || a.Name != b.Name || a.Status != b.Status || a.Cluster != b.Cluster || string(a.Doc) != string(b.Doc) {
			t.Errorf("entity %d differs between sequential and batched recording:\n  sequential: %+v\n  batched:    %+v", i, a, b)
		}
	}
	for _, e := range snapBatch {
		if e.Kind == "service" && e.ID == "s-2" {
			t.Errorf("s-2 disappeared in topoStep2 and should have been retired by t2 in the batched tenant too, found: %+v", e)
		}
	}

	// Timeline for the entity that actually changed version (s-1) should show the same two versions
	// either way - the batched path is not just leaving the same end state, it got there through the
	// same intermediate step.
	tlSeq, err := db.Timeline(ctx, orgSeq, "service", "s-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	tlBatch, err := db.Timeline(ctx, orgBatch, "service", "s-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tlSeq.Versions) != len(tlBatch.Versions) {
		t.Fatalf("s-1 has %d versions sequentially but %d batched: %+v vs %+v", len(tlSeq.Versions), len(tlBatch.Versions), tlSeq.Versions, tlBatch.Versions)
	}
}

// TestRecordCatchUpDropsWhatIsAlreadyOlderThanRecorded is RecordCatchUp's other half of ErrOutOfOrder
// parity: a lone Record call refuses an older-than-newest write outright, without touching anything.
// RecordCatchUp instead has to keep going - the whole point is applying everything in a backlog that
// still can be, not refusing the entire batch because one buffered point turned out to be stale (for
// instance because a live sync already recorded something newer while the backlog was building up).
func TestRecordCatchUpDropsWhatIsAlreadyOlderThanRecorded(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t1.Add(time.Hour)
	record(t, db, org, t1, estate()) // as if a live sync already recorded t1 while a backlog was building

	mk := func(at time.Time, topo model.Topology) CatchUpPoint {
		c := history.Compact(topo)
		data, fp, err := history.Encode(c)
		if err != nil {
			t.Fatal(err)
		}
		return CatchUpPoint{At: at, Topo: c, FP: fp, Size: len(data)}
	}
	points := []CatchUpPoint{mk(t0, estate()), mk(t2, topoStep1())} // t0 is now stale; t2 is still new
	applied, dropped, err := db.RecordCatchUp(ctx, org, points)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("expected exactly the t2 point to apply, got applied=%d", applied)
	}
	if len(dropped) != 1 || !dropped[0].Equal(t0) {
		t.Errorf("expected t0 reported as dropped for being older than what is already recorded, got %v", dropped)
	}
	tl, err := db.Timeline(ctx, org, "service", "s-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Versions) != 2 {
		t.Fatalf("expected the t1 (live) and t2 (batch) versions only - the dropped t0 point should not have added a third: %+v", tl.Versions)
	}
}

// TestDependentsWalksACycleWithoutHangingOrDuplicating is the reason the walk was rewritten around a
// visited set instead of Cypher's *1..hops variable-length pattern: a call graph with a cycle in it
// gives a variable-length pattern infinitely many paths to enumerate at any hop bound above the cycle's
// own length (every trip around it is a longer, still-valid path), which is exactly the kind of
// explosion that made the old query dangerous on a real, long-lived estate. A visited-by-entity walk
// instead reaches each member of the cycle exactly once, at its true shortest distance, and terminates
// because there is nothing left to visit.
func TestDependentsWalksACycleWithoutHangingOrDuplicating(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	cluster := model.Cluster{ID: "c-1", Name: "edge-a", Tier: "edge", Status: "connected"}
	svc := func(id string) model.Service {
		return model.Service{ID: id, ClusterID: "c-1", Name: id, Replicas: 1, ReadyReplicas: 1, Status: "ready"}
	}
	topo := model.Topology{
		Clusters: []model.Cluster{cluster},
		Services: []model.Service{svc("a"), svc("b"), svc("c")},
		Dependencies: []model.Dependency{
			{ID: "d-ab", From: "a", FromKind: "service", To: "b", ToKind: "service", Protocol: "tcp", Port: 80},
			{ID: "d-bc", From: "b", FromKind: "service", To: "c", ToKind: "service", Protocol: "tcp", Port: 80},
			{ID: "d-ca", From: "c", FromKind: "service", To: "a", ToKind: "service", Protocol: "tcp", Port: 80},
		},
	}
	record(t, db, org, t0, topo)

	done := make(chan struct{})
	var reached []Reached
	var err error
	go func() {
		_, reached, err = db.Dependents(ctx, org, t0, "service", "a", maxWalkHops)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Dependents did not return: a cycle made the walk hang")
	}
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for _, r := range reached {
		byID[r.ID] = r.Hops
	}
	if len(byID) != 2 {
		t.Fatalf("a 3-node cycle has exactly two other members to reach, once each: %v", reached)
	}
	if byID["c"] != 1 {
		t.Errorf("c calls a directly, should be 1 hop: %+v", byID)
	}
	if byID["b"] != 2 {
		t.Errorf("b reaches a only via c, should be 2 hops, not re-counted every lap of the cycle: %+v", byID)
	}
}

// TestDependentsBoundsRealFanOut is the other half of the same rewrite: a hub entity with fan-out well
// past what anyone would page through must still answer promptly and correctly at hops=1 (every caller,
// each exactly once), and past maxWalkResults callers the walk must stop growing rather than keep
// following an ever-larger frontier out to the hop limit.
func TestDependentsBoundsRealFanOut(t *testing.T) {
	db, org := testDBTimeout(t, 90*time.Second)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	const callers = maxWalkResults + 200
	cluster := model.Cluster{ID: "c-1", Name: "edge-a", Tier: "edge", Status: "connected"}
	topo := model.Topology{Clusters: []model.Cluster{cluster}, Services: []model.Service{{ID: "hub", ClusterID: "c-1", Name: "hub", Replicas: 1, ReadyReplicas: 1, Status: "ready"}}}
	for i := 0; i < callers; i++ {
		sid := fmt.Sprintf("caller-%d", i)
		topo.Services = append(topo.Services, model.Service{ID: sid, ClusterID: "c-1", Name: sid, Replicas: 1, ReadyReplicas: 1, Status: "ready"})
		topo.Dependencies = append(topo.Dependencies, model.Dependency{ID: "d-" + sid, From: sid, FromKind: "service", To: "hub", ToKind: "service", Protocol: "tcp", Port: 80})
	}
	record(t, db, org, t0, topo)

	start := time.Now()
	_, reached, err := db.Dependents(ctx, org, t0, "service", "hub", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("a single hop with real fan-out took %v", d)
	}
	if len(reached) != maxWalkResults-1 {
		t.Fatalf("fan-out of %d callers past the cap should be held at maxWalkResults-1 (the start does not count against it): got %d", callers, len(reached))
	}
	for _, r := range reached {
		if r.Hops != 1 {
			t.Errorf("every caller here is one hop from hub, got %d for %s", r.Hops, r.ID)
		}
		if r.Kind != "service" {
			t.Errorf("unexpected kind reached: %s/%s", r.Kind, r.ID)
		}
	}
}

// TestDiffEntitiesReportsAddedRemovedAndChanged checks the three shapes a structural diff can take,
// together: something new, something gone, and something that looked different by the second moment -
// reusing diffDocs, Timeline's own per-field diff, rather than a new comparison of its own.
func TestDiffEntitiesReportsAddedRemovedAndChanged(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	after := estate()
	after.Services[0].Replicas = 3                                                                                  // s-1 changed
	after.ExternalEndpoints = nil                                                                                   // ext-1 removed
	after.Dependencies = after.Dependencies[:1]                                                                     // d-2 (s-1 -> ext-1) removed along with it
	after.Nodes = append(after.Nodes, model.Node{ID: "n-3", ClusterID: "c-1", Name: "n3", Status: "ready", CPU: 2}) // n-3 added
	t1 := t0.Add(time.Hour)
	record(t, db, org, t1, after)

	diff, err := db.DiffEntities(ctx, org, t0, t1)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.From.Equal(t0) || !diff.To.Equal(t1) {
		t.Errorf("diff should report the two moments it actually resolved to: %v -> %v", diff.From, diff.To)
	}
	addedHas := func(kind, id string) bool {
		for _, e := range diff.Added {
			if e.Kind == kind && e.ID == id {
				return true
			}
		}
		return false
	}
	removedHas := func(kind, id string) bool {
		for _, e := range diff.Removed {
			if e.Kind == kind && e.ID == id {
				return true
			}
		}
		return false
	}
	if !addedHas("node", "n-3") {
		t.Errorf("n-3 is new at t1, should be Added: %v", diff.Added)
	}
	if !removedHas("external", "ext-1") {
		t.Errorf("ext-1 dropped out at t1, should be Removed: %v", diff.Removed)
	}
	if !removedHas("dependency", "d-2") {
		t.Errorf("d-2 dropped out along with ext-1, should be Removed: %v", diff.Removed)
	}
	var sChange *EntityDiff
	for i := range diff.Changed {
		if diff.Changed[i].Kind == "service" && diff.Changed[i].ID == "s-1" {
			sChange = &diff.Changed[i]
		}
	}
	if sChange == nil {
		t.Fatalf("s-1's replica count changed, it should be reported as Changed: %v", diff.Changed)
	}
	found := false
	for _, c := range sChange.Changes {
		if c.Field == "replicas" {
			found = true
		}
	}
	if !found {
		t.Errorf("s-1's Changed entry should include the replicas field: %+v", sChange.Changes)
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

// TestLinkEntitiesBatchKeepsExactlyTheGivenMembersOpenForEachEntity is
// TestLinkEntitiesKeepsExactlyTheGivenMembersOpen's scenario, for two entities linked in the same call:
// each keeps exactly its own member set, a member staying open across a call is left alone (not closed
// and reopened), and a change to one entity's set never touches the other's edges.
func TestLinkEntitiesBatchKeepsExactlyTheGivenMembersOpenForEachEntity(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate()) // s-1, s-2 exist as services

	for _, id := range []string{"app-1", "app-2"} {
		if err := db.RecordEntity(ctx, org, t0.Add(time.Minute), "application", id, id, "", "", map[string]any{"name": id}); err != nil {
			t.Fatal(err)
		}
	}
	openOf := func(app string) []string {
		t.Helper()
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'application', id:$id})-[r:CONTAINS {org:$org}]->(m:Entity) WHERE r.validTo IS NULL RETURN m.id ORDER BY m.id`, map[string]any{"id": app}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range res[0].Rows {
			out = append(out, str(row[0]))
		}
		return out
	}
	validFromOf := func(app, member string) string {
		t.Helper()
		res, err := db.C.Run(ctx, db.C.For(org).S(`MATCH (:Entity {org:$org, kind:'application', id:$app})-[r:CONTAINS {org:$org}]->(:Entity {id:$id}) WHERE r.validTo IS NULL RETURN toString(r.validFrom)`,
			map[string]any{"app": app, "id": member}))
		if err != nil || len(res[0].Rows) == 0 {
			t.Fatalf("no open edge %s -> %s", app, member)
		}
		return str(res[0].Rows[0][0])
	}

	if err := db.LinkEntitiesBatch(ctx, org, t0.Add(time.Minute), "CONTAINS", "application", "service",
		[]MemberSet{{ID: "app-1", TargetIDs: []string{"s-1", "s-2"}}, {ID: "app-2", TargetIDs: []string{"s-2"}}}); err != nil {
		t.Fatal(err)
	}
	if got := openOf("app-1"); len(got) != 2 || got[0] != "s-1" || got[1] != "s-2" {
		t.Fatalf("app-1: expected s-1 and s-2 open, got %v", got)
	}
	if got := openOf("app-2"); len(got) != 1 || got[0] != "s-2" {
		t.Fatalf("app-2: expected only s-2 open, got %v", got)
	}
	app1S2From := validFromOf("app-1", "s-2")

	// app-1 drops s-1; app-2 is not mentioned in this call at all, and must be left untouched.
	if err := db.LinkEntitiesBatch(ctx, org, t0.Add(2*time.Minute), "CONTAINS", "application", "service",
		[]MemberSet{{ID: "app-1", TargetIDs: []string{"s-2"}}}); err != nil {
		t.Fatal(err)
	}
	if got := openOf("app-1"); len(got) != 1 || got[0] != "s-2" {
		t.Fatalf("app-1: expected only s-2 open after the change, got %v", got)
	}
	if validFromOf("app-1", "s-2") != app1S2From {
		t.Error("app-1's edge to s-2 should never have been closed and reopened, only left alone")
	}
	if got := openOf("app-2"); len(got) != 1 || got[0] != "s-2" {
		t.Fatalf("app-2: an untouched entity's membership must not change just because another entity was linked in the same call: %v", got)
	}

	if err := db.LinkEntitiesBatch(ctx, org, t0, "not-a-rel", "application", "service", []MemberSet{{ID: "app-1"}}); err == nil {
		t.Error("an unknown relationship type should be refused")
	}
	if err := db.LinkEntitiesBatch(ctx, org, t0, "CONTAINS", "application", "service", nil); err != nil {
		t.Errorf("an empty batch should be a no-op, not an error: %v", err)
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

// TestBatchMethodsCostAConstantNumberOfRoundTripsNotOnePerEntity is what RecordEntities and
// LinkEntitiesBatch exist for: a call-site loop of RecordEntity/LinkEntities costs one read-then-write
// pair of round trips per entity, and the whole point of batching is that the siblings cost the same
// small constant number regardless of how many entities are in the batch. This package's tests run
// against a real Neo4j (or skip outright) rather than a mock transport, so this counts actual calls to
// Client.Run via RunCount - test-only instrumentation on the one Client every test already shares -
// instead of inventing a fake client the rest of the suite does not use.
func TestBatchMethodsCostAConstantNumberOfRoundTripsNotOnePerEntity(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	const n = 25
	recs := make([]EntityRecord, n)
	for i := range recs {
		id := fmt.Sprintf("app-%d", i)
		recs[i] = EntityRecord{ID: id, Name: id, Doc: map[string]any{"name": id}}
	}

	before := db.C.RunCount()
	if err := db.RecordEntities(ctx, org, t0, "application", recs); err != nil {
		t.Fatal(err)
	}
	if got := db.C.RunCount() - before; got != 2 {
		t.Fatalf("RecordEntities of %d new entities should cost exactly 2 round trips (one read, one write), cost %d", n, got)
	}

	// No members for any of them: the read alone should settle it, with nothing left to open or close.
	sets := make([]MemberSet, n)
	for i := range sets {
		sets[i] = MemberSet{ID: recs[i].ID}
	}
	before = db.C.RunCount()
	if err := db.LinkEntitiesBatch(ctx, org, t0, "CONTAINS", "application", "service", sets); err != nil {
		t.Fatal(err)
	}
	if got := db.C.RunCount() - before; got != 1 {
		t.Fatalf("LinkEntitiesBatch of %d entities with nothing to open or close should cost exactly 1 round trip (the read), cost %d", n, got)
	}

	// Repeating RecordEntities with identical state costs only the read: nothing changed, so there is
	// nothing to write - the same short-circuit RecordEntity itself takes, just for the whole batch at once.
	before = db.C.RunCount()
	if err := db.RecordEntities(ctx, org, t0, "application", recs); err != nil {
		t.Fatal(err)
	}
	if got := db.C.RunCount() - before; got != 1 {
		t.Fatalf("RecordEntities repeating unchanged state should cost exactly 1 round trip (the read, nothing to write), cost %d", got)
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

// TestEnsureCreatesTheEntityOrgIndex checks the dedicated (org) index on :Entity exists after Ensure runs
// - the fix for AsOfEntities' existence check, PurgeTenant's sweep and Stats' count all matching :Entity by
// org alone, with no kind to narrow by, unlike everything else that scans this label.
func TestEnsureCreatesTheEntityOrgIndex(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	res, err := db.C.Run(ctx, Global(`SHOW INDEXES YIELD name, labelsOrTypes, properties WHERE name = 'entity_org' RETURN labelsOrTypes, properties`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Rows) != 1 {
		t.Fatalf("entity_org index not found: %v", res[0].Rows)
	}
	labels, _ := res[0].Rows[0][0].([]any)
	props, _ := res[0].Rows[0][1].([]any)
	if len(labels) != 1 || labels[0] != "Entity" || len(props) != 1 || props[0] != "org" {
		t.Fatalf("entity_org index is on the wrong thing: labels=%v properties=%v", labels, props)
	}
}

// TestWalkTimeoutBoundsTheWholeOperation proves walk's overall deadline is actually wired in, not just
// spelled correctly: with it shrunk to nothing, even a one-hop Dependencies call on data that exists must
// fail promptly with a context error, rather than hanging or quietly succeeding on whatever finished
// before the (nonexistent) deadline.
func TestWalkTimeoutBoundsTheWholeOperation(t *testing.T) {
	db, org := testDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	record(t, db, org, t0, estate())

	old := walkTimeout
	walkTimeout = time.Nanosecond
	defer func() { walkTimeout = old }()

	_, _, err := db.Dependencies(ctx, org, t0, "service", "s-1", 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an all-but-zero walk timeout should fail with a context deadline error, got %v", err)
	}
}

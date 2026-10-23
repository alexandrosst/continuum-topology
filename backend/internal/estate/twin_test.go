package estate

import (
	"encoding/json"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
	"strconv"
	"strings"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/kernel"
	"ikhnos/internal/twin"
)

func entityOf(m twin.Model, kind, name string) *twin.Entity {
	for i := range m.Entities {
		if m.Entities[i].Kind == kind && m.Entities[i].Name == name {
			return &m.Entities[i]
		}
	}
	return nil
}

func TestObservationStateFollowsTheClockTheConnectionAndRevocation(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	if _, _, err := h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		m, _, err := h.Model(r.ctx)
		if err != nil {
			t.Fatal(err)
		}
		c := entityOf(m, "cluster", a.Name)
		if c == nil {
			t.Fatalf("no cluster in the model: %+v", m.Entities)
		}
		return string(c.State)
	}
	if got := state(); got != "live" {
		t.Fatalf("connected and fresh: %s", got)
	}
	// The stream closes: nothing new arrives but the picture is recent.
	h.Mu.Lock()
	h.Views[a.ID].Connected = false
	h.Mu.Unlock()
	*r.now = r.now.Add(time.Minute)
	if got := state(); got != "disconnected" {
		t.Fatalf("closed stream, recent picture: %s", got)
	}
	// Silence past the staleness window (4 heartbeats of 30 s).
	*r.now = r.now.Add(2 * time.Hour)
	m, _, _ := h.Model(r.ctx)
	c := entityOf(m, "cluster", a.Name)
	if c.State != twin.Stale || !strings.Contains(c.StateReason, "stale for 2 h") {
		t.Fatalf("stale: %s %q", c.State, c.StateReason)
	}
	if n := entityOf(m, "node", "n1"); n.State != twin.Stale {
		t.Fatalf("the node must be as stale as its cluster: %s", n.State)
	}
	// A stale cluster is not a target, and the reason says why.
	ex := twin.Exclusions(m)
	if len(ex) != 1 || ex[0].ClusterID != c.ID || !strings.Contains(ex[0].Reason, "stale for 2 h") {
		t.Fatalf("exclusions: %+v", ex)
	}
	// The state document the UI polls agrees, and keeps the old boolean.
	doc, _ := h.State(r.ctx)
	if len(doc.Topology.Clusters) != 1 || !doc.Topology.Clusters[0].Stale || doc.Topology.Clusters[0].State != "stale" {
		t.Fatalf("state document: %+v", doc.Topology.Clusters)
	}
	// The agent entity is marked exactly as stale as the cluster it serves - markObservation stamps
	// every entity kind from the same Observation, Agent included.
	if len(doc.Topology.Agents) != 1 || !doc.Topology.Agents[0].Stale || doc.Topology.Agents[0].State != "stale" || doc.Topology.Agents[0].ClusterID != a.ClusterID {
		t.Fatalf("agent must be as stale as its cluster: %+v", doc.Topology.Agents)
	}

	// Revocation: the picture stays, marked revoked, and is never a target.
	if err := h.C.Agents.Revoke(r.ctx, "alex", a.ID, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	m, _, _ = h.Model(r.ctx)
	c = entityOf(m, "cluster", a.Name)
	if c == nil || c.State != twin.Revoked || !strings.Contains(c.StateReason, "revoked") {
		t.Fatalf("revoked: %+v", c)
	}
	if ex := twin.Exclusions(m); len(ex) != 1 || ex[0].State != twin.Revoked {
		t.Fatalf("a revoked cluster must be excluded: %+v", ex)
	}
	// After the retention window it is no longer shown.
	*r.now = r.now.Add(8 * 24 * time.Hour)
	if m, _, _ = h.Model(r.ctx); entityOf(m, "cluster", a.Name) != nil {
		t.Fatal("a cluster revoked more than a week ago is still in the model")
	}
}

// TestAgentSelfStatsFlowsIntoTheTopologyEntity covers the thread from an agent's heartbeat-reported
// SelfStats, through its ring (view.self), into Topology.Agents[0].Self - the "light pointer to its own
// resource footprint" model.Agent.Self documents. Before any heartbeat carries SelfStats, Self stays
// nil (never a fabricated zero-valued sample); once one does, the document's Agent reflects exactly that
// sample's RSS/goroutines (never duplicating/recomputing it).
func TestAgentSelfStatsFlowsIntoTheTopologyEntity(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	if _, _, err := h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}), false); err != nil {
		t.Fatal(err)
	}

	doc, err := h.State(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Topology.Agents) != 1 || doc.Topology.Agents[0].Self != nil {
		t.Fatalf("before any heartbeat carries SelfStats, Self must stay nil: %+v", doc.Topology.Agents)
	}

	h.Mu.Lock()
	h.Views[a.ID].NoteSelfStats(&ikhnosv1.SelfStats{RssBytes: 123456, Goroutines: 42}, h.C.Now())
	h.Mu.Unlock()
	// Bust stateFor's own short (modelCacheTTL) cache: a self-stats sample never bumps tw.gen (see
	// Hub.SampleSelfStats' own doc for why that counter, like a tier narrowing, deliberately does not),
	// so only the clock moving the first State call's cache entry past modelCacheTTL guarantees the
	// second call rebuilds rather than replaying the pre-heartbeat snapshot it already cached.
	*r.now = r.now.Add(3 * time.Second)

	doc, err = h.State(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ag := doc.Topology.Agents[0]
	if ag.Self == nil || ag.Self.RSSBytes != 123456 || ag.Self.Goroutines != 42 {
		t.Fatalf("agent.Self = %+v, want the heartbeat's own RSS/goroutines", ag.Self)
	}
}

func TestARevokedAgentsPictureSurvivesARestartAsRevoked(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	if _, _, err := h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false); err != nil {
		t.Fatal(err)
	}
	h.persist(r.ctx, a.ID, true)
	if err := h.C.Agents.Revoke(r.ctx, "alex", a.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	h2 := NewHub(h.C)
	h2.Restore(r.ctx)
	m, _, err := h2.Model(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := entityOf(m, "cluster", a.Name); c == nil || c.State != twin.Revoked {
		t.Fatalf("after a restart: %+v", c)
	}
}

func TestADeletedWorkloadBecomesATombstoneAndComesBack(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	nodes := []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}
	cart, pay := testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart, pay), false)
	*r.now = r.now.Add(time.Minute)
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart), true) // pay was deleted
	doc, _ := h.State(r.ctx)
	if len(doc.Topology.Services) != 1 || len(doc.Tombstones) != 1 {
		t.Fatalf("services %d tombstones %+v", len(doc.Topology.Services), doc.Tombstones)
	}
	ts := doc.Tombstones[0]
	if ts.Kind != "service" || ts.Name != "pay" || ts.GoneAt != kernel.RFC(*r.now) || ts.Reason == "" || !json.Valid(ts.Record) {
		t.Fatalf("tombstone: %+v", ts)
	}
	m, _, _ := h.Model(r.ctx)
	g := entityOf(m, "service", "pay")
	if g == nil || g.State != twin.Gone || g.GoneAt == "" {
		t.Fatalf("the model must show the deleted workload as gone: %+v", g)
	}
	// It survives a restart.
	h.Flush(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 1 {
		t.Fatalf("persisted tombstones: %+v", list)
	}
	h2 := NewHub(h.C)
	h2.Restore(r.ctx)
	if got := h2.Tombstones(r.ctx); len(got) != 1 || got[0].Name != "pay" {
		t.Fatalf("after restart: %+v", got)
	}
	// A Job finishing is not news.
	job := &ikhnosv1.WorkloadFacts{Key: "shop/Job/migrate", Namespace: "shop", Kind: "Job", Name: "migrate", Replicas: 1}
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart, job), true)
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart), true)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 1 {
		t.Fatalf("a Job that completed must not leave a tombstone: %+v", doc.Tombstones)
	}
	// The workload is back: the tombstone goes.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart, pay), true)
	doc, _ = h.State(r.ctx)
	if len(doc.Tombstones) != 0 || len(doc.Topology.Services) != 2 {
		t.Fatalf("after it came back: %+v", doc.Tombstones)
	}
	h.Flush(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 0 {
		t.Fatalf("the persisted tombstone was not removed: %+v", list)
	}
	// Retention: a tombstone older than the window disappears.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, cart), true)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 1 {
		t.Fatal("no tombstone")
	}
	*r.now = r.now.Add(8 * 24 * time.Hour)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 0 {
		t.Fatalf("retention not applied: %+v", doc.Tombstones)
	}
	h.Flush(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 0 {
		t.Fatalf("expired tombstones were not swept from the store: %+v", list)
	}
}

func TestLoweringTheAccessTierIsNotADeletion(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	nodes := []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, testkit.TwinWorkload("shop", "cart")), false)
	a.AccessTier = 1 // workloads are no longer read
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes), true)
	doc, _ := h.State(r.ctx)
	for _, ts := range doc.Tombstones {
		if ts.Kind == "service" && !strings.Contains(ts.Reason, "access tier") {
			t.Fatalf("consent narrowing recorded as a deletion: %+v", ts)
		}
	}
}

func TestRenamedNodeKeepsItsIdAndReplacedNodeGetsANewOne(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	idOf := func(name string) string {
		doc, _ := h.State(r.ctx)
		for _, n := range doc.Topology.Nodes {
			if n.Name == name {
				return n.ID
			}
		}
		return ""
	}
	h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("worker-1", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}), false)
	first := idOf("worker-1")
	if first == "" {
		t.Fatal("no node")
	}
	// The same machine under a new name is the same record, and remembers its old name.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("edge-7", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}), true)
	if got := idOf("edge-7"); got != first {
		t.Fatalf("a rename created a new node: %s vs %s", got, first)
	}
	doc, _ := h.State(r.ctx)
	if n := doc.Topology.Nodes[0]; len(n.Aliases) != 1 || n.Aliases[0] != "worker-1" || n.IdentityBasis != "machine-id" {
		t.Fatalf("aliases/basis: %+v %s", n.Aliases, n.IdentityBasis)
	}
	// A different machine that takes the old name is a different node.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("edge-7", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d"), testkit.TwinNode("worker-1", "aaaaaaaabbbbccccddddeeeeffff0000")}), true)
	if idOf("worker-1") == first || idOf("worker-1") == "" {
		t.Fatalf("a replaced node inherited the old id: %s", idOf("worker-1"))
	}
	// The registry survives a restart: the rename is still one machine.
	h.Flush(r.ctx)
	h2 := NewHub(h.C)
	h2.Restore(r.ctx)
	doc2, _ := h2.StateFor(r.ctx, false)
	_ = doc2
	if ids, _ := r.st.ListIdentities(r.ctx, "org-1"); len(ids) != 2 {
		t.Fatalf("identities persisted: %+v", ids)
	}
}

// TestStateForCachesTopologyButNeverAgentDocsOrAudit is a regression test for a real bug caught while
// adding stateFor's cache: narrowing an agent's access tier (SetAgentTier) never bumps tw.gen, because
// nothing needed it to before this cache existed. Caching the per-agent AgentDoc list the same way as
// topology would have served a stale AccessTier/HardenHelm for up to modelCacheTTL after a change - this
// pins down that AgentDoc is always rebuilt fresh, while the expensive topology/correlation pass is still
// genuinely cached against tw.gen (a second State() call with nothing changed must agree with the first).
func TestStateForCachesTopologyButNeverAgentDocsOrAudit(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	nodes := []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, testkit.TwinWorkload("shop", "cart")), false)

	first, _ := h.State(r.ctx)
	second, _ := h.State(r.ctx) // same gen, well within modelCacheTTL: must agree with the first
	if len(first.Topology.Services) != 1 || len(second.Topology.Services) != 1 {
		t.Fatalf("topology should be stable across an unchanged cache hit: %d vs %d", len(first.Topology.Services), len(second.Topology.Services))
	}

	// SetAgentTier changes AccessTier through a path that never bumps tw.gen. If AgentDoc were cached the
	// same way as topology, this would still show the old tier on the very next call.
	if _, err := h.SetAgentTier(r.ctx, "tester", a.ID, 0, nil); err != nil {
		t.Fatalf("SetAgentTier: %v", err)
	}
	third, _ := h.State(r.ctx)
	if len(third.Agents) != 1 || third.Agents[0].AccessTier != 0 {
		t.Fatalf("agent docs must never be served from the topology cache: %+v", third.Agents)
	}

	// A real topology-affecting change (another sync) is reflected immediately too, even inside the TTL
	// window: tw.gen, not the clock, is what invalidates the cache.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")), true)
	fourth, _ := h.State(r.ctx)
	if len(fourth.Topology.Services) != 2 {
		t.Fatalf("a gen-bumping change must invalidate the topology cache immediately: %d services", len(fourth.Topology.Services))
	}

	// Audit rows are never cached, regardless of topology cache state: StateFor(ctx, true) always re-reads them.
	if err := r.core.Audited(r.ctx, "tester", "note", "agent", a.ID, "looked at", func() error { return nil }); err != nil {
		t.Fatalf("audited: %v", err)
	}
	withAudit, _ := h.StateFor(r.ctx, true)
	if len(withAudit.AuditLog) == 0 {
		t.Fatalf("audit log must be fetched fresh even when the topology cache is hit: %+v", withAudit.AuditLog)
	}
}

// TestStateForTopologyCacheHitMissAndTTL pins down stateFor's own topology cache (tw.stateCache,
// guarded by tw.mu, never h.mu) more precisely than TestStateForCachesTopologyButNeverAgentDocsOrAudit
// above: it observes a hit/miss directly, by pointer identity of the *effectiveBuild buildTopology
// produces (the same object twin.Model's "after" callback also reads) rather than by the shape of the
// resulting document, and it exercises every invalidation path - a real gen bump, and the clock alone
// carrying the entry past modelCacheTTL - separately. It also confirms, with a call counter rather than
// just a non-empty slice, that the audit-log fetch happens on every single call, hit or miss alike.
func TestStateForTopologyCacheHitMissAndTTL(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, testkit.FP)
	nodes := []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, testkit.TwinWorkload("shop", "cart")), false)

	var auditFetches int
	// stateFor's own after callback does not see the fetched audit rows directly, so the count is taken
	// the same way the existing cache test does: an Audited() call between two stateFor calls must show
	// up in StateFor(ctx, true)'s AuditLog every time, cache hit or not.
	auditedOnce := func() {
		auditFetches++
		if err := r.core.Audited(r.ctx, "tester", "note", "agent", a.ID, "looked at "+strconv.Itoa(auditFetches), func() error { return nil }); err != nil {
			t.Fatalf("audited: %v", err)
		}
	}
	captureEB := func() *effectiveBuild {
		var got *effectiveBuild
		if _, err := h.stateFor(r.ctx, false, func(_ *StateDoc, eb *effectiveBuild) { got = eb }); err != nil {
			t.Fatalf("stateFor: %v", err)
		}
		return got
	}

	// First call: nothing cached yet, so this is necessarily a build. Capture its effectiveBuild.
	eb1 := captureEB()
	if eb1 == nil {
		t.Fatal("expected an effectiveBuild from the first call")
	}

	// Second call, immediately after, same gen, well within modelCacheTTL: a hit - the exact same
	// effectiveBuild instance, proving buildTopology (and the interpret/observe/correlate pass inside it)
	// did not run again.
	eb2 := captureEB()
	if eb2 != eb1 {
		t.Fatalf("expected a cache hit (identical effectiveBuild pointer) on an unchanged second call")
	}

	// A real input change (another sync, which bumps tw.gen) forces a miss on the very next call, even
	// though it happens well inside the TTL window.
	h.ApplySync(a, testkit.TwinFull(testkit.FP, nodes, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")), true)
	eb3 := captureEB()
	if eb3 == eb2 {
		t.Fatal("a gen-bumping change must force a rebuild, not reuse the cached effectiveBuild")
	}

	// Immediately after that miss, with nothing changed again: back to a hit.
	eb4 := captureEB()
	if eb4 != eb3 {
		t.Fatal("expected a cache hit immediately after a rebuild, with nothing changed since")
	}

	// The clock alone, with no further gen bump, carrying the entry past modelCacheTTL forces a miss too.
	*r.now = r.now.Add(modelCacheTTL + time.Second)
	eb5 := captureEB()
	if eb5 == eb4 {
		t.Fatal("an entry older than modelCacheTTL must force a rebuild, even with nothing else changed")
	}

	// And a hit resumes right after that rebuild.
	eb6 := captureEB()
	if eb6 != eb5 {
		t.Fatal("expected a cache hit immediately after the TTL-driven rebuild")
	}

	// The audit-log fetch must never be skipped or served from this cache, on a hit or a miss: add one
	// audit row before each of several calls (two of which land on what was just shown above to be a
	// topology cache hit) and confirm every single one shows up.
	for i := 0; i < 3; i++ {
		auditedOnce()
		doc, err := h.StateFor(r.ctx, true)
		if err != nil {
			t.Fatalf("StateFor: %v", err)
		}
		if len(doc.AuditLog) < auditFetches {
			t.Fatalf("call %d: audit log has %d rows, want at least %d - the audit fetch was skipped or cached", i, len(doc.AuditLog), auditFetches)
		}
	}
}

func TestRetentionUsesOverrideThenLiveSettingThenDefault(t *testing.T) {
	e := newEnv(t)
	h := NewHub(e.core)
	if got := h.retention(); got != twin.DefaultRetention {
		t.Fatalf("no settings saved: got %v, want the default %v", got, twin.DefaultRetention)
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", kernel.Settings{TombstoneRetentionDays: 21}); err != nil {
		t.Fatal(err)
	}
	if got, want := h.retention(), 21*24*time.Hour; got != want {
		t.Fatalf("a changed setting was not picked up live: got %v, want %v", got, want)
	}
	// A live setting change takes effect without touching the test-only override.
	if _, err := e.core.SaveSettings(e.ctx, "alex", kernel.Settings{TombstoneRetentionDays: 5}); err != nil {
		t.Fatal(err)
	}
	if got, want := h.retention(), 5*24*time.Hour; got != want {
		t.Fatalf("the setting did not update live a second time: got %v, want %v", got, want)
	}
	// The test-only override still wins over whatever the live setting says.
	h.TombstoneRetention = 3 * time.Hour
	if got, want := h.retention(), 3*time.Hour; got != want {
		t.Fatalf("the override was not honoured: got %v, want %v", got, want)
	}
}

// twinAgent approves an agent for a cluster in the hub's organisation and gives it an empty picture, connected.
func twinAgent(t *testing.T, e *env, h *Hub, cluster string) store.Agent {
	t.Helper()
	id, _, _ := e.approvedAgent(t, cluster)
	a, err := e.st.GetAgent(e.ctx, "org-1", id)
	if err != nil {
		t.Fatal(err)
	}
	h.Mu.Lock()
	v := NewView()
	v.Connected = true
	v.LastBeat = h.C.Now()
	h.Views[id] = v
	h.Mu.Unlock()
	return a
}

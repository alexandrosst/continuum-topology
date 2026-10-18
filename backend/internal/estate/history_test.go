package estate

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
	"continuum/internal/kernel"
	"continuum/internal/observed"
	"continuum/internal/store"
	"continuum/internal/testkit"
)

func TestApplySyncReportsDriftOnlyForLaterFullPictures(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, id)
	a.AccessTier = 2
	r.hub.Mu.Lock()
	r.hub.Views[id] = &View{EstView: EstView{State: facts.New()}}
	r.hub.Mu.Unlock()

	n1 := &continuumv1.NodeFacts{Key: "n1", Name: "n1"}
	w1 := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Namespace: "ns", Kind: "Deployment", Name: "a", Replicas: 1}
	full := func(nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) *continuumv1.Sync {
		return &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP}, Nodes: nodes, Workloads: ws}
	}
	if _, checked, _ := r.hub.ApplySync(a, full([]*continuumv1.NodeFacts{n1}, w1), false); checked {
		t.Fatal("the first full picture was treated as a check")
	}
	d, checked, _ := r.hub.ApplySync(a, full([]*continuumv1.NodeFacts{n1}, w1), true)
	if !checked || d.Total() != 0 {
		t.Fatalf("identical picture: checked=%v drift=%+v", checked, d)
	}
	// The cluster changed behind the server's back: a workload scaled and a node appeared.
	w1b := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Namespace: "ns", Kind: "Deployment", Name: "a", Replicas: 5}
	n2 := &continuumv1.NodeFacts{Key: "n2", Name: "n2"}
	d, checked, _ = r.hub.ApplySync(a, full([]*continuumv1.NodeFacts{n1, n2}, w1b), true)
	if !checked || d.Changed != 1 || d.Missing != 1 || d.Extra != 0 {
		t.Fatalf("drift = %+v", d)
	}
	if got := r.hub.Views[id].State.Workloads["ns/Deployment/a"].Replicas; got != 5 {
		t.Fatalf("the server was not corrected: %d replicas", got)
	}
	d, _, _ = r.hub.ApplySync(a, full([]*continuumv1.NodeFacts{n1}), true) // n2 and the workload are gone
	if d.Extra != 2 || d.Missing != 0 || d.Changed != 0 {
		t.Fatalf("removals: %+v", d)
	}
	if !strings.Contains(d.String(), "no longer has") {
		t.Fatalf("description: %q", d.String())
	}
	if _, checked, _ := r.hub.ApplySync(a, &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{n2}}, true); checked {
		t.Fatal("a delta was treated as a check")
	}
}

func TestReportConsistencyStoresAWarningOnlyWhenSomethingDiffers(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, id)
	r.hub.reportConsistency(r.ctx, a, facts.Drift{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(evs) != 0 {
		t.Fatalf("a clean check left %d events", len(evs))
	}
	r.hub.reportConsistency(r.ctx, a, facts.Drift{Missing: 1, Changed: 2, Samples: []string{"node n2"}})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour), Kind: "drift"})
	if len(evs) != 1 || evs[0].Severity != "warning" || !strings.Contains(evs[0].Detail, "node n2") || evs[0].TargetID != id {
		t.Fatalf("events = %+v", evs)
	}
}

func TestNoteMeasurementsAcceptsOnlyIssuedTargetsAndSaneNumbers(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = &View{EstView: EstView{State: facts.New()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{"t1": {ID: "t1", Host: "203.0.113.10", Port: 443, Source: "manual"}}}}
	r.hub.Mu.Unlock()
	r.hub.NoteMeasurements(id, &continuumv1.Measurements{Results: []*continuumv1.PathResult{
		{TargetId: "t1", Samples: 5, Failed: 1, RttMinMs: 10, RttP50Ms: 12, RttP95Ms: 1e12},
		{TargetId: "made-up", Samples: 5, RttP50Ms: 1}, // never issued
		{TargetId: "t1", Samples: 0},                   // nothing measured
		{TargetId: "t1", Samples: 101},                 // absurd
		{TargetId: "t1", Samples: 5, Failed: 6},        // more failures than attempts
	}})
	v := r.hub.Views[id]
	if len(v.Paths) != 1 || v.Paths["t1"] == nil || len(v.Paths["t1"].Rounds) != 1 {
		t.Fatalf("paths = %+v", v.Paths)
	}
	if got := v.Paths["t1"].Rounds[0].P95; got != 60000 {
		t.Fatalf("an absurd time was not clamped: %v", got)
	}
	r.hub.NoteMeasurements("unknown-agent", &continuumv1.Measurements{}) // must not panic
}

func TestConfigForIssuesManualAndObservedTargetsByTier(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, id)
	if _, err := r.core.SaveSettings(r.ctx, "alex", kernel.Settings{MeasureSeconds: 30, ProbeTargets: []kernel.ProbeTarget{
		{ID: "mine", ClusterID: a.ClusterID, Host: "203.0.113.20", Port: 8443},
		{ID: "theirs", ClusterID: "another-cluster", Host: "203.0.113.21", Port: 8443},
	}}); err != nil {
		t.Fatal(err)
	}
	v := &View{EstView: EstView{State: facts.New(), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{"stale": {}}, Targets: map[string]connectivity.IssuedTarget{}}}
	testkit.ExtEdge(v.Flows, "ns/Deployment/a", "198.51.100.10", 443, "tcp", 20, "")

	a.AccessTier = 1 // flows come from pods; tier 1 does not cover them
	cfg := r.hub.ConfigFor(a, v)
	if len(cfg.ProbeTargets) != 1 || cfg.ProbeTargets[0].Id != "mine" || cfg.MeasureSeconds != 30 {
		t.Fatalf("tier 1 config = %+v", cfg)
	}
	a.AccessTier = 2
	cfg = r.hub.ConfigFor(a, v)
	if len(cfg.ProbeTargets) != 2 || cfg.ResyncSeconds != int32(15*60) {
		t.Fatalf("tier 2 config = %+v", cfg)
	}
	if _, ok := v.Paths["stale"]; ok {
		t.Fatal("a path for a target no longer issued was kept")
	}
	h1 := v.CfgHash
	r.hub.ConsistencyEvery = 2 * time.Second
	if cfg = r.hub.ConfigFor(a, v); cfg.ResyncSeconds != 2 || v.CfgHash == h1 {
		t.Fatalf("the test override was ignored: %+v", cfg)
	}
	if _, err := r.core.SaveSettings(r.ctx, "alex", kernel.Settings{}); err != nil {
		t.Fatal(err)
	}
	bare := &View{EstView: EstView{State: facts.New(), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
	if cfg = r.hub.ConfigFor(a, bare); cfg.MeasureSeconds != 0 || len(cfg.ProbeTargets) != 0 {
		t.Fatalf("empty config = %+v", cfg)
	}
}

func TestRecorderStoresSnapshotsAndEventsOnlyForRealChanges(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = liveView(*r.now, "n1", "n2")
	r.hub.Mu.Unlock()

	r.hub.ScanNow(r.ctx)
	pts, _ := r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	if len(pts) != 1 {
		t.Fatalf("%d snapshots after the first scan", len(pts))
	}
	*r.now = r.now.Add(time.Minute)
	r.hub.Mu.Lock()
	r.hub.Views[id].LastBeat = *r.now
	r.hub.Mu.Unlock()
	r.hub.ScanNow(r.ctx)
	pts, _ = r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(pts) != 1 || len(evs) != 0 {
		t.Fatalf("an unchanged estate produced %d snapshots and %d events", len(pts), len(evs))
	}
	r.hub.Mu.Lock()
	delete(r.hub.Views[id].State.Nodes, "n2")
	r.hub.Mu.Unlock()
	*r.now = r.now.Add(time.Minute)
	r.hub.Mu.Lock()
	r.hub.Views[id].LastBeat = *r.now
	r.hub.Mu.Unlock()
	r.hub.ScanNow(r.ctx)
	pts, _ = r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(pts) != 2 || len(evs) == 0 {
		t.Fatalf("a removed node produced %d snapshots and %d events", len(pts), len(evs))
	}
	var sawNode bool
	for _, e := range evs {
		if e.TargetKind == "node" {
			sawNode = true
		}
	}
	if !sawNode {
		t.Fatalf("no node event among %+v", evs)
	}
}

func TestEventRetentionSettingPrunesByAgeOnlyForItsOwnOrgWhenOn(t *testing.T) {
	r := newHubRig(t)
	now := *r.now
	old := store.Event{At: now.Add(-10 * 24 * time.Hour), Kind: "test", TargetKind: "node", TargetID: "n1", Name: "old"}
	recent := store.Event{At: now.Add(-time.Hour), Kind: "test", TargetKind: "node", TargetID: "n2", Name: "recent"}
	if err := r.st.AddEvents(r.ctx, "org-1", []store.Event{old, recent}); err != nil {
		t.Fatal(err)
	}
	// A second organisation's own old event must survive pruning done for org-1 (tenancy isolation).
	if err := r.st.CreateOrg(r.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	other := store.Event{At: now.Add(-10 * 24 * time.Hour), Kind: "test", TargetKind: "node", TargetID: "n3", Name: "other-org"}
	if err := r.st.AddEvents(r.ctx, "org-2", []store.Event{other}); err != nil {
		t.Fatal(err)
	}

	// Off (the default): the opt-in setting is inert, nothing of ours is pruned by it.
	r.hub.rec.prune(r.ctx, now, kernel.Settings{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
	if len(evs) != 2 {
		t.Fatalf("with EventRetentionDays=0, %d events remain, want 2 (the opt-in setting must stay inert)", len(evs))
	}

	// On: only the old event in org-1 is pruned; the recent one and org-2's event are untouched.
	r.hub.rec.prune(r.ctx, now, kernel.Settings{EventRetentionDays: 7})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
	if len(evs) != 1 || evs[0].Name != "recent" {
		t.Fatalf("org-1 events after pruning = %+v", evs)
	}
	evs2, _ := r.st.ListEvents(r.ctx, "org-2", store.EventQuery{Limit: 100})
	if len(evs2) != 1 || evs2[0].Name != "other-org" {
		t.Fatalf("another organisation's events were touched: %+v", evs2)
	}
}

// A write that fails while recording is retried by the next scan: a lost event write loses no event, and a
// lost snapshot write does not store its events a second time.
func TestRecorderLosesNoEventAndRepeatsNoneAfterAFailedWrite(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	f := &testkit.FailHistory{Store: r.hub.C.Store}
	r.hub.C.Store = f
	setNodes := func(names ...string) {
		r.hub.Mu.Lock()
		defer r.hub.Mu.Unlock()
		v := liveView(*r.now, names...)
		r.hub.Views[id] = v
	}
	scan := func() {
		*r.now = r.now.Add(time.Minute)
		r.hub.ScanNow(r.ctx)
	}
	nodeEvents := func() (n int) {
		evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
		for _, e := range evs {
			if e.TargetKind == "node" {
				n++
			}
		}
		return n
	}
	snapshots := func() int {
		pts, _ := r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
		return len(pts)
	}
	setNodes("n1", "n2")
	scan()
	if snapshots() != 1 || nodeEvents() != 0 {
		t.Fatalf("baseline: %d snapshots, %d node events", snapshots(), nodeEvents())
	}

	// The events cannot be written: nothing is stored, and once the database answers the change is still found.
	f.Events.Store(true)
	setNodes("n1", "n2", "n3")
	scan()
	if nodeEvents() != 0 || snapshots() != 1 {
		t.Fatalf("while events fail: %d node events, %d snapshots", nodeEvents(), snapshots())
	}
	f.Events.Store(false)
	scan()
	if nodeEvents() != 1 || snapshots() != 2 {
		t.Fatalf("after events recover: %d node events, %d snapshots, want 1 and 2", nodeEvents(), snapshots())
	}

	// The snapshot cannot be written: the events are, once; the snapshot follows, and the events are not repeated.
	f.Snapshots.Store(true)
	setNodes("n1", "n2")
	scan()
	if nodeEvents() != 2 || snapshots() != 2 {
		t.Fatalf("while snapshots fail: %d node events, %d snapshots, want 2 and 2", nodeEvents(), snapshots())
	}
	f.Snapshots.Store(false)
	scan()
	if nodeEvents() != 2 || snapshots() != 3 {
		t.Fatalf("after snapshots recover: %d node events, %d snapshots, want 2 and 3", nodeEvents(), snapshots())
	}
}

// A revoked agent's snapshots and in-memory view are kept while its last picture is still shown, and released
// once it is not; the approved agent beside it is untouched.
func TestRevokedAgentsAreReclaimedAfterTheirRetention(t *testing.T) {
	r := newHubRig(t)
	r.hub.TombstoneRetention = time.Hour
	live, _, _ := r.approvedAgent(t, testkit.FP)
	gone, _, _ := r.approvedAgent(t, "11111111-2222-4333-8444-555566667777")
	for _, id := range []string{live, gone} {
		r.hub.Mu.Lock()
		r.hub.Views[id] = liveView(*r.now, "n1")
		r.hub.Mu.Unlock()
		for _, k := range []string{id, FlowsKey(id), agents.ConsentKey(id)} {
			if err := r.st.SaveSnapshot(r.ctx, k, []byte("{}"), *r.now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := r.core.Agents.Revoke(r.ctx, "alex", gone, "reinstalled"); err != nil {
		t.Fatal(err)
	}
	has := func(id string) (view bool, snaps int) {
		r.hub.Mu.Lock()
		view = r.hub.Views[id] != nil
		r.hub.Mu.Unlock()
		for _, k := range []string{id, FlowsKey(id), agents.ConsentKey(id)} {
			if _, _, err := r.st.LoadSnapshot(r.ctx, k); err == nil {
				snaps++
			}
		}
		return
	}

	r.hub.rec.prune(r.ctx, *r.now, kernel.Settings{})
	if v, n := has(gone); !v || n != 3 {
		t.Fatalf("within its retention a revoked agent keeps its view (%v) and snapshots (%d of 3)", v, n)
	}
	*r.now = r.now.Add(2 * time.Hour)
	r.hub.rec.prune(r.ctx, *r.now, kernel.Settings{})
	if v, n := has(gone); v || n != 0 {
		t.Fatalf("past its retention a revoked agent keeps its view (%v) and snapshots (%d of 3)", v, n)
	}
	if v, n := has(live); !v || n != 3 {
		t.Fatalf("an approved agent lost its view (%v) or snapshots (%d of 3)", !v, n)
	}
}

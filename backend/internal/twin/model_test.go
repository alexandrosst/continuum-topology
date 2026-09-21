package twin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"
	"continuum/internal/model"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func i32(n int32) *int32 { return &n }

// fixture: one cluster with two nodes (only one has pod data) and two workloads (one with requests, one without).
func fixture(t *testing.T, clusterID, agentID string, tier int) (Input, *facts.State) {
	t.Helper()
	st := facts.New()
	st.Cluster = &continuumv1.ClusterFacts{Uid: "0a1b2c3d-0000-4000-8000-000000000001", Version: "v1.30.2+k3s1"}
	st.Nodes["edge-1"] = &continuumv1.NodeFacts{Key: "edge-1", Name: "edge-1", Ready: true, Architecture: "arm64", OsImage: "Debian", CpuCapacityMillis: 4000, MemoryCapacityBytes: 8 << 30,
		CpuAllocatableMillis: 3900, MemoryAllocatableBytes: 7 << 30, PodCount: i32(5), CpuRequestedMillis: 1500, MemoryRequestedBytes: 2 << 30, PodCapacity: 110, InternalIps: []string{"10.0.0.1"},
		Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}
	st.Nodes["edge-2"] = &continuumv1.NodeFacts{Key: "edge-2", Name: "edge-2", Ready: true, CpuCapacityMillis: 2000, MemoryCapacityBytes: 4 << 30, CpuAllocatableMillis: 1900, MemoryAllocatableBytes: 3 << 30}
	st.Namespaces["shop"] = &continuumv1.NamespaceFacts{Key: "shop", Name: "shop"}
	st.Workloads["shop/Deployment/cart"] = &continuumv1.WorkloadFacts{Key: "shop/Deployment/cart", Namespace: "shop", Kind: "Deployment", Name: "cart", Replicas: 3, ReadyReplicas: 3,
		CpuRequestMillis: 250, MemoryRequestBytes: 256 << 20, CpuLimitMillis: 500, Images: []*continuumv1.ContainerImage{{Image: "cart:1"}}, NodeNames: []string{"edge-1"}}
	st.Workloads["shop/Deployment/bare"] = &continuumv1.WorkloadFacts{Key: "shop/Deployment/bare", Namespace: "shop", Kind: "Deployment", Name: "bare", Replicas: 1, ReadyReplicas: 1}
	topo := interpret.Interpret(interpret.Input{OrgID: "org", AgentID: agentID, ClusterID: clusterID, Name: "edge-a", State: st, Now: now, AccessTier: tier})
	obs := map[string]Observation{agentID: Assess(AssessInput{Now: now, LastObserved: now.Add(-5 * time.Second), Connected: true, StaleAfter: 2 * time.Minute})}
	return Input{Now: now, StaleAfter: 2 * time.Minute, Retention: DefaultRetention, Topology: topo, Facts: map[string]*facts.State{clusterID: st},
		Agents: map[string]AgentInfo{agentID: {ID: agentID, ClusterID: clusterID, Tier: tier}}, Observations: obs}, st
}

func find(m Model, kind, name string) *Entity {
	for i := range m.Entities {
		if m.Entities[i].Kind == kind && m.Entities[i].Name == name {
			return &m.Entities[i]
		}
	}
	return nil
}

func TestModelCarriesUnitsSourcesAndConfidenceOnEveryAttribute(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	m := Build(in)
	if m.Contract != Contract || m.Observation.StaleAfterSeconds != 120 || m.Observation.TombstoneRetentionDays != 7 {
		t.Fatalf("%+v", m)
	}
	n := find(m, "node", "edge-1")
	if n == nil {
		t.Fatal("node missing")
	}
	// Exact quantities, with explicit units.
	cpu := n.Attributes["cpuAllocatable"]
	if cpu.Value != 3.9 || cpu.Unit != "cores" || cpu.Source != FromAgent || cpu.AgentID != "ag-1" || cpu.Confidence != Reported || cpu.State != Live || cpu.ObservedAt == "" {
		t.Errorf("cpuAllocatable = %+v", cpu)
	}
	mem := n.Attributes["memoryAllocatable"]
	if mem.Value != float64(7<<30) || mem.Unit != "bytes" {
		t.Errorf("memoryAllocatable = %+v", mem)
	}
	if n.Attributes["podCount"].Value != 5 || n.Attributes["podCount"].Unit != "count" {
		t.Errorf("podCount = %+v", n.Attributes["podCount"])
	}
	// The other node: pods not read. Unknown is not zero.
	n2 := find(m, "node", "edge-2")
	for _, a := range []string{"podCount", "cpuRequested", "memoryRequested"} {
		at := n2.Attributes[a]
		if at.Value != nil || at.Confidence != Unknown || at.Evidence == "" || at.Unit == "" && a != "podCount" {
			t.Errorf("%s must be unknown with a reason, got %+v", a, at)
		}
	}
	// Inferred values name their evidence and are never presented as reported.
	kind := n.Attributes["kind"]
	if kind.Source != FromInferred || (kind.Confidence != Inferred && kind.Confidence != Guess) || kind.Evidence == "" {
		t.Errorf("kind = %+v", kind)
	}
	tier := find(m, "cluster", "edge-a").Attributes["tier"]
	if tier.Confidence != Guess || tier.Evidence == "" {
		t.Errorf("an on-prem tier is a guess: %+v", tier)
	}
	if find(m, "cluster", "edge-a").Attributes["distribution"].Confidence != Inferred {
		t.Errorf("k3s from the version suffix is inferred")
	}
	// Every attribute of every entity is complete.
	for _, e := range m.Entities {
		if e.State == "" || e.Attributes == nil {
			t.Errorf("%s %s: %+v", e.Kind, e.Name, e)
		}
		for name, a := range e.Attributes {
			if a.Source == "" || a.Confidence == "" || a.State == "" {
				t.Errorf("%s.%s incomplete: %+v", e.Name, name, a)
			}
			if a.Confidence == Unknown && a.Value != nil {
				t.Errorf("%s.%s: unknown must be null, got %v", e.Name, name, a.Value)
			}
			if a.Confidence != Unknown && a.Value == nil && a.Evidence == "" {
				t.Errorf("%s.%s: a null that is not unknown must say why", e.Name, name)
			}
		}
	}
	// It is JSON with nulls kept.
	b, _ := json.Marshal(n2.Attributes["podCount"])
	if !strings.Contains(string(b), `"value":null`) {
		t.Errorf("unknown must serialise as null: %s", b)
	}
}

func TestClusterCapacityIsUnknownUnlessEveryNodeReportsIt(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	c := find(Build(in), "cluster", "edge-a")
	if c.Attributes["nodeCount"].Value != 2 || c.Attributes["cpuAllocatable"].Value != 5.8 {
		t.Errorf("%+v %+v", c.Attributes["nodeCount"], c.Attributes["cpuAllocatable"])
	}
	// One node has no pod data, so what is requested across the cluster is not known (not "1.5 cores").
	if r := c.Attributes["cpuRequested"]; r.Value != nil || r.Confidence != Unknown {
		t.Errorf("cpuRequested must be unknown: %+v", r)
	}
	// An agent at tier 0 reads no nodes: nothing about capacity is known, and nothing is zero.
	in0, _ := fixture(t, "cl-0", "ag-0", 0)
	in0.Topology.Nodes, in0.Topology.Services, in0.Topology.Namespaces = nil, nil, nil
	c0 := find(Build(in0), "cluster", "edge-a")
	for _, a := range []string{"nodeCount", "cpuAllocatable", "memoryAllocatable", "cpuRequested"} {
		if c0.Attributes[a].Confidence != Unknown || c0.Attributes[a].Value != nil {
			t.Errorf("%s at tier 0 must be unknown: %+v", a, c0.Attributes[a])
		}
	}
}

func TestServiceRequestsAreUnknownWhenUnset(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	m := Build(in)
	cart, bare := find(m, "service", "cart"), find(m, "service", "bare")
	if cart.Attributes["cpuRequest"].Value != 0.25 || cart.Attributes["cpuRequest"].Unit != "cores" || cart.Attributes["memoryRequest"].Value != float64(256<<20) {
		t.Errorf("%+v", cart.Attributes)
	}
	if bare.Attributes["cpuRequest"].Confidence != Unknown || bare.Attributes["cpuRequest"].Value != nil {
		t.Errorf("no request set is an unknown need, not a need of zero: %+v", bare.Attributes["cpuRequest"])
	}
	// No limit is a fact (unbounded), reported, and null.
	if l := bare.Attributes["cpuLimit"]; l.Value != nil || l.Confidence != Reported || l.Evidence != "no limit is set" {
		t.Errorf("%+v", l)
	}
	if cart.Identity == nil || cart.Identity.Basis != "workload" || !strings.HasSuffix(cart.Identity.Key, "shop/Deployment/cart") {
		t.Errorf("%+v", cart.Identity)
	}
}

func TestEveryEntityCarriesItsObservationState(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	// The agent went quiet two hours ago.
	in.Observations["ag-1"] = Assess(AssessInput{Now: now, LastObserved: now.Add(-2 * time.Hour), Connected: false, StaleAfter: 2 * time.Minute})
	m := Build(in)
	for _, e := range m.Entities {
		if e.State != Stale || e.StateReason != "stale for 2 h" {
			t.Errorf("%s %s: %s %q", e.Kind, e.Name, e.State, e.StateReason)
		}
		for n, a := range e.Attributes {
			if a.State != Stale {
				t.Errorf("%s.%s state %s", e.Name, n, a.State)
			}
		}
	}
	// Revoked.
	in.Observations["ag-1"] = Assess(AssessInput{Now: now, LastObserved: now.Add(-time.Minute), StaleAfter: 2 * time.Minute, Revoked: true, RevokedAt: now.Add(-time.Hour)})
	if e := find(Build(in), "cluster", "edge-a"); e.State != Revoked || e.StateReason != "agent revoked 1 h ago" {
		t.Errorf("%+v", e)
	}
}

func TestExclusionsListNonLiveClustersAndUnknownCapacityWithReasons(t *testing.T) {
	live, _ := fixture(t, "cl-live", "ag-live", 2)
	stale, _ := fixture(t, "cl-stale", "ag-stale", 2)
	stale.Observations = map[string]Observation{"ag-stale": Assess(AssessInput{Now: now, LastObserved: now.Add(-2 * time.Hour), StaleAfter: 2 * time.Minute})}
	revoked, _ := fixture(t, "cl-rev", "ag-rev", 2)
	revoked.Observations = map[string]Observation{"ag-rev": Assess(AssessInput{Now: now, LastObserved: now, StaleAfter: 2 * time.Minute, Revoked: true, RevokedAt: now.Add(-3 * time.Hour)})}
	blind, _ := fixture(t, "cl-blind", "ag-blind", 0)
	blind.Topology.Nodes = nil

	in := live
	rename := func(t model.Topology, name string) model.Topology {
		t.Clusters[0].Name = name
		return t
	}
	in.Topology = rename(in.Topology, "live")
	for _, x := range []struct {
		in   Input
		name string
	}{{stale, "stale"}, {revoked, "revoked"}, {blind, "blind"}} {
		x.in.Topology = rename(x.in.Topology, x.name)
		in.Topology.Clusters = append(in.Topology.Clusters, x.in.Topology.Clusters...)
		in.Topology.Nodes = append(in.Topology.Nodes, x.in.Topology.Nodes...)
		for k, v := range x.in.Observations {
			in.Observations[k] = v
		}
		for k, v := range x.in.Facts {
			in.Facts[k] = v
		}
		for k, v := range x.in.Agents {
			in.Agents[k] = v
		}
	}
	got := map[string]Exclusion{}
	for _, e := range Exclusions(Build(in)) {
		got[e.Name] = e
	}
	if _, ok := got["live"]; ok {
		t.Errorf("a live cluster with known capacity is a target: %+v", got["live"])
	}
	for name, want := range map[string]string{"stale": "stale for 2 h", "revoked": "agent revoked 3 h ago", "blind": "capacity unknown: nodes are not read at this access tier"} {
		if got[name].Reason != want {
			t.Errorf("%s: reason %q, want %q", name, got[name].Reason, want)
		}
	}
	if got["stale"].State != Stale || got["revoked"].State != Revoked || got["blind"].State != Live {
		t.Errorf("%+v", got)
	}
}

func TestDeclaredOverridesWinAndKeepTheObservedValue(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	svc := find(Build(in), "service", "cart")
	in.Declared = workspace.Declared{Refs: map[string]workspace.Ref{
		"cl-1": {Kind: "cluster", SiteID: "site-1", Overrides: map[string]any{"trustZone": "restricted", "tier": "cloud"}},
		svc.ID: {Kind: "service", ApplicationID: "app-shop", Overrides: map[string]any{"sensitivity": "confidential", "name": "Cart (prod)"}},
		"gone": {Kind: "service", Overrides: map[string]any{"x": 1}}, // a reference to nothing known
	}, Records: map[string][]map[string]any{
		"site":    {{"id": "site-1", "name": "Patras", "lat": 38.2, "lng": 21.7, "trustZone": "private"}},
		"node":    {{"id": "nd-lab", "clusterId": "cl-1", "name": "lab-pi", "cpu": 4.0, "memoryGb": 2.0}},
		"service": {{"id": "sv-decl", "clusterId": "cl-1", "name": "declared-svc", "cpuRequestM": 250.0, "memRequestMi": 256.0, "replicas": 3.0}},
		"device":  {{"id": "dev-1", "name": "camera", "count": 12.0, "deletedAt": "x"}},
	}}
	m := Build(in)
	c := find(m, "cluster", "edge-a")
	if c.Origin != "observed+declared" {
		t.Errorf("origin %s", c.Origin)
	}
	tier := c.Attributes["tier"]
	if tier.Value != "cloud" || tier.Source != FromDeclared || tier.Confidence != Reported {
		t.Errorf("the declared tier wins: %+v", tier)
	}
	if tier.Shadowed == nil || tier.Shadowed.Value != "edge" || tier.Shadowed.Confidence != Guess {
		t.Errorf("the observed guess stays visible under it: %+v", tier.Shadowed)
	}
	if c.Attributes["trustZone"].Value != "restricted" || c.Attributes["siteId"].Value != "site-1" {
		t.Errorf("%+v", c.Attributes)
	}
	s := find(m, "service", "Cart (prod)")
	if s == nil || s.Attributes["sensitivity"].Value != "confidential" || s.Attributes["applicationId"].Value != "app-shop" {
		t.Fatalf("%+v", s)
	}
	// Declared records appear as declared entities with explicit units.
	site := find(m, "site", "Patras")
	if site == nil || site.State != Declared || site.Origin != "declared" || site.Attributes["lat"].Value != 38.2 {
		t.Errorf("%+v", site)
	}
	// Units are explicit for declared values too: millicores and MiB are converted, never left for the reader to guess.
	ds := find(m, "service", "declared-svc")
	if ds == nil || ds.Attributes["cpuRequest"].Value != 0.25 || ds.Attributes["cpuRequest"].Unit != "cores" ||
		ds.Attributes["memoryRequest"].Value != float64(256<<20) || ds.Attributes["memoryRequest"].Unit != "bytes" || ds.Attributes["replicas"].Unit != "count" {
		t.Errorf("declared service units: %+v", ds)
	}
	lab := find(m, "node", "lab-pi")
	if lab == nil || lab.Attributes["cpuCapacity"].Value != 4.0 || lab.Attributes["cpuCapacity"].Unit != "cores" ||
		lab.Attributes["memoryCapacity"].Value != float64(2<<30) || lab.Attributes["memoryCapacity"].Unit != "bytes" || lab.Attributes["cpuCapacity"].Source != FromDeclared {
		t.Errorf("%+v", lab)
	}
	if find(m, "device", "camera") != nil {
		t.Error("a deleted declared record is not part of the model")
	}
	// The observed value is untouched by declarations that do not name it.
	if find(m, "node", "edge-1").Attributes["cpuCapacity"].Source != FromAgent {
		t.Error("observed values stay observed")
	}
}

func TestTombstonesAppearAsGoneAndComeBackAsLive(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	gone := now.Add(-3 * time.Hour)
	in.Tombstones = []store.Tombstone{
		{Kind: "service", ID: "sv-deleted", Name: "checkout", ClusterID: "cl-1", AgentID: "ag-1", GoneAt: gone, LastSeen: gone.Add(-time.Minute), Reason: "deleted in the cluster"},
		{Kind: "service", ID: "sv-ancient", Name: "old", GoneAt: now.Add(-8 * 24 * time.Hour)},
	}
	m := Build(in)
	e := find(m, "service", "checkout")
	if e == nil || e.State != Gone || e.GoneAt != gone.Format(time.RFC3339) || e.StateReason != "deleted in the cluster" || e.ClusterID != "cl-1" || e.LastObservedAt == "" {
		t.Fatalf("%+v", e)
	}
	if find(m, "service", "old") != nil {
		t.Error("past retention it is not part of the model")
	}
	// The same id showing up live wins over its tombstone.
	live := find(m, "service", "cart")
	in.Tombstones = append(in.Tombstones, store.Tombstone{Kind: "service", ID: live.ID, Name: "cart", GoneAt: gone})
	m = Build(in)
	n := 0
	for _, x := range m.Entities {
		if x.ID == live.ID {
			n++
			if x.State != Live {
				t.Errorf("%+v", x)
			}
		}
	}
	if n != 1 {
		t.Errorf("one record per id, got %d", n)
	}
	if len(m.Warnings) != 0 {
		t.Errorf("a revived tombstone is not a collision: %v", m.Warnings)
	}
}

func TestClustersWithTheSameNameAreKeptApartAndSaid(t *testing.T) {
	a, _ := fixture(t, "cl-aaaa", "ag-a", 2)
	b, _ := fixture(t, "cl-bbbb", "ag-b", 2)
	b.Topology.Clusters[0].Key = "0a1b2c3d-0000-4000-8000-000000000002" // re-created: a different kube-system UID
	a.Topology.Clusters = append(a.Topology.Clusters, b.Topology.Clusters...)
	a.Topology.Nodes = append(a.Topology.Nodes, b.Topology.Nodes...)
	a.Topology.Services = append(a.Topology.Services, b.Topology.Services...)
	a.Topology.Namespaces = append(a.Topology.Namespaces, b.Topology.Namespaces...)
	a.Observations["ag-b"], a.Agents["ag-b"], a.Facts["cl-bbbb"] = b.Observations["ag-b"], b.Agents["ag-b"], b.Facts["cl-bbbb"]
	m := Build(a)
	n := 0
	for _, e := range m.Entities {
		if e.Kind == "cluster" {
			n++
			if e.Identity == nil || !strings.Contains(e.Identity.Note, "same name") || !strings.Contains(e.Identity.Note, "not merged") {
				t.Errorf("%+v", e.Identity)
			}
		}
	}
	if n != 2 || len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "different clusters") {
		t.Errorf("%d clusters, warnings %v", n, m.Warnings)
	}
	// Same service names in two clusters are different services, and nothing collides.
	ids := map[string]bool{}
	for _, e := range m.Entities {
		k := e.Kind + e.ID
		if ids[k] {
			t.Errorf("collision on %s", k)
		}
		ids[k] = true
	}
}

func TestCollidingIdsAreReportedNotSilentlyMerged(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	dup := in.Topology.Services[0]
	dup.Name = "impostor"
	dup.Key = "cl-1/shop/Deployment/other"
	in.Topology.Services = append(in.Topology.Services, dup)
	m := Build(in)
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "share the id") {
		t.Errorf("warnings: %v", m.Warnings)
	}
}

func TestFingerprintIgnoresTheClockButNotTheEstate(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	base := Fingerprint(Build(in))
	// Time passing while the agent stays live changes ages and observation times, not the model.
	later := in
	later.Now = now.Add(20 * time.Second)
	later.Observations = map[string]Observation{"ag-1": Assess(AssessInput{Now: later.Now, LastObserved: later.Now.Add(-time.Second), Connected: true, StaleAfter: 2 * time.Minute})}
	if got := Fingerprint(Build(later)); got != base {
		t.Error("a heartbeat must not change the model")
	}
	// A state change does.
	stale := in
	stale.Observations = map[string]Observation{"ag-1": Assess(AssessInput{Now: now, LastObserved: now.Add(-time.Hour), StaleAfter: 2 * time.Minute})}
	fpStale := Fingerprint(Build(stale))
	if fpStale == base {
		t.Error("live -> stale is a change of the model")
	}
	// ...but the reason's age does not (1 h vs 2 h stale is the same model until it is something else).
	stale2 := in
	stale2.Observations = map[string]Observation{"ag-1": Assess(AssessInput{Now: now, LastObserved: now.Add(-2 * time.Hour), StaleAfter: 2 * time.Minute})}
	if Fingerprint(Build(stale2)) != fpStale {
		t.Error("getting older while stale is not a change")
	}
	// A value change does.
	changed, st := fixture(t, "cl-1", "ag-1", 2)
	st.Workloads["shop/Deployment/cart"].Replicas = 4
	changed.Topology = interpret.Interpret(interpret.Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-1", Name: "edge-a", State: st, Now: now, AccessTier: 2})
	if Fingerprint(Build(changed)) == base {
		t.Error("a replica count changed")
	}
}

func TestModelOrderIsDeterministic(t *testing.T) {
	in, _ := fixture(t, "cl-1", "ag-1", 2)
	a, _ := json.Marshal(Build(in))
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(Build(in))
		if string(a) != string(b) {
			t.Fatal("the same input must give the same bytes")
		}
	}
}

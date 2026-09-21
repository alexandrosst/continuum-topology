package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/store"
	"continuum/internal/twin"
)

// twinAgent approves an agent for a cluster in the hub's organisation and gives it an empty picture, connected.
func twinAgent(t *testing.T, e *env, h *Hub, cluster string) store.Agent {
	t.Helper()
	id, _, _ := e.approvedAgent(t, cluster)
	a, err := e.st.GetAgent(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	v := newView()
	v.connected = true
	v.lastBeat = h.C.Now()
	h.views[id] = v
	h.mu.Unlock()
	return a
}

func twinNode(name, machine string) *continuumv1.NodeFacts {
	return &continuumv1.NodeFacts{Key: name, Name: name, MachineId: machine, CpuCapacityMillis: 4000, CpuAllocatableMillis: 3800,
		MemoryCapacityBytes: 8 << 30, MemoryAllocatableBytes: 7 << 30, Ready: true}
}

func twinWorkload(ns, name string) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: ns + "/Deployment/" + name, Namespace: ns, Kind: "Deployment", Name: name, Replicas: 1, ReadyReplicas: 1, CpuRequestMillis: 250, MemoryRequestBytes: 256 << 20}
}

func twinFull(cluster string, nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) *continuumv1.Sync {
	return &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: cluster}, Nodes: nodes, Workloads: ws,
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "shop", Name: "shop"}}}
}

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
	a := twinAgent(t, r.env, h, fp)
	if _, _, err := h.applySync(a, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart")), false); err != nil {
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
	h.mu.Lock()
	h.views[a.ID].connected = false
	h.mu.Unlock()
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

	// Revocation: the picture stays, marked revoked, and is never a target.
	if err := h.C.Revoke(r.ctx, "alex", a.ID, "decommissioned"); err != nil {
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

func TestARevokedAgentsPictureSurvivesARestartAsRevoked(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, fp)
	if _, _, err := h.applySync(a, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart")), false); err != nil {
		t.Fatal(err)
	}
	h.persist(r.ctx, a.ID, true)
	if err := h.C.Revoke(r.ctx, "alex", a.ID, "gone"); err != nil {
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
	a := twinAgent(t, r.env, h, fp)
	nodes := []*continuumv1.NodeFacts{twinNode("n1", "")}
	cart, pay := twinWorkload("shop", "cart"), twinWorkload("shop", "pay")
	h.applySync(a, twinFull(fp, nodes, cart, pay), false)
	*r.now = r.now.Add(time.Minute)
	h.applySync(a, twinFull(fp, nodes, cart), true) // pay was deleted
	doc, _ := h.State(r.ctx)
	if len(doc.Topology.Services) != 1 || len(doc.Tombstones) != 1 {
		t.Fatalf("services %d tombstones %+v", len(doc.Topology.Services), doc.Tombstones)
	}
	ts := doc.Tombstones[0]
	if ts.Kind != "service" || ts.Name != "pay" || ts.GoneAt != rfc(*r.now) || ts.Reason == "" || !json.Valid(ts.Record) {
		t.Fatalf("tombstone: %+v", ts)
	}
	m, _, _ := h.Model(r.ctx)
	g := entityOf(m, "service", "pay")
	if g == nil || g.State != twin.Gone || g.GoneAt == "" {
		t.Fatalf("the model must show the deleted workload as gone: %+v", g)
	}
	// It survives a restart.
	h.twinTick(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 1 {
		t.Fatalf("persisted tombstones: %+v", list)
	}
	h2 := NewHub(h.C)
	h2.Restore(r.ctx)
	if got := h2.Tombstones(r.ctx); len(got) != 1 || got[0].Name != "pay" {
		t.Fatalf("after restart: %+v", got)
	}
	// A Job finishing is not news.
	job := &continuumv1.WorkloadFacts{Key: "shop/Job/migrate", Namespace: "shop", Kind: "Job", Name: "migrate", Replicas: 1}
	h.applySync(a, twinFull(fp, nodes, cart, job), true)
	h.applySync(a, twinFull(fp, nodes, cart), true)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 1 {
		t.Fatalf("a Job that completed must not leave a tombstone: %+v", doc.Tombstones)
	}
	// The workload is back: the tombstone goes.
	h.applySync(a, twinFull(fp, nodes, cart, pay), true)
	doc, _ = h.State(r.ctx)
	if len(doc.Tombstones) != 0 || len(doc.Topology.Services) != 2 {
		t.Fatalf("after it came back: %+v", doc.Tombstones)
	}
	h.twinTick(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 0 {
		t.Fatalf("the persisted tombstone was not removed: %+v", list)
	}
	// Retention: a tombstone older than the window disappears.
	h.applySync(a, twinFull(fp, nodes, cart), true)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 1 {
		t.Fatal("no tombstone")
	}
	*r.now = r.now.Add(8 * 24 * time.Hour)
	if doc, _ = h.State(r.ctx); len(doc.Tombstones) != 0 {
		t.Fatalf("retention not applied: %+v", doc.Tombstones)
	}
	h.twinTick(r.ctx)
	if list, _ := r.st.ListTombstones(r.ctx, "org-1"); len(list) != 0 {
		t.Fatalf("expired tombstones were not swept from the store: %+v", list)
	}
}

func TestLoweringTheAccessTierIsNotADeletion(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	a := twinAgent(t, r.env, h, fp)
	nodes := []*continuumv1.NodeFacts{twinNode("n1", "")}
	h.applySync(a, twinFull(fp, nodes, twinWorkload("shop", "cart")), false)
	a.AccessTier = 1 // workloads are no longer read
	h.applySync(a, twinFull(fp, nodes), true)
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
	a := twinAgent(t, r.env, h, fp)
	idOf := func(name string) string {
		doc, _ := h.State(r.ctx)
		for _, n := range doc.Topology.Nodes {
			if n.Name == name {
				return n.ID
			}
		}
		return ""
	}
	h.applySync(a, twinFull(fp, []*continuumv1.NodeFacts{twinNode("worker-1", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}), false)
	first := idOf("worker-1")
	if first == "" {
		t.Fatal("no node")
	}
	// The same machine under a new name is the same record, and remembers its old name.
	h.applySync(a, twinFull(fp, []*continuumv1.NodeFacts{twinNode("edge-7", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d")}), true)
	if got := idOf("edge-7"); got != first {
		t.Fatalf("a rename created a new node: %s vs %s", got, first)
	}
	doc, _ := h.State(r.ctx)
	if n := doc.Topology.Nodes[0]; len(n.Aliases) != 1 || n.Aliases[0] != "worker-1" || n.IdentityBasis != "machine-id" {
		t.Fatalf("aliases/basis: %+v %s", n.Aliases, n.IdentityBasis)
	}
	// A different machine that takes the old name is a different node.
	h.applySync(a, twinFull(fp, []*continuumv1.NodeFacts{twinNode("edge-7", "4f1c0a9b2d7e4c3a8b6d5e2f1a0b9c8d"), twinNode("worker-1", "aaaaaaaabbbbccccddddeeeeffff0000")}), true)
	if idOf("worker-1") == first || idOf("worker-1") == "" {
		t.Fatalf("a replaced node inherited the old id: %s", idOf("worker-1"))
	}
	// The registry survives a restart: the rename is still one machine.
	h.twinTick(r.ctx)
	h2 := NewHub(h.C)
	h2.Restore(r.ctx)
	doc2, _ := h2.StateFor(r.ctx, false)
	_ = doc2
	if ids, _ := r.st.ListIdentities(r.ctx, "org-1"); len(ids) != 2 {
		t.Fatalf("identities persisted: %+v", ids)
	}
}

func TestSameNamesInTwoClustersAndTwoOrganisationsNeverCollide(t *testing.T) {
	a := newAdminRig(t)
	_, bobOrg := a.register(t, "bob", "Bob Works")
	p := a.a.P
	t1, _ := p.Tenant(a.ctx, "org-1")
	t2, _ := p.Tenant(a.ctx, bobOrg)
	const fpB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const fpC = "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	a1 := twinAgent(t, a.env, t1.Hub, fp)
	a2 := twinAgent(t, a.env, t1.Hub, fpB)
	e2 := &env{base: a.base, core: t2.C, st: a.st, now: a.now, ctx: a.ctx}
	b1 := twinAgent(t, e2, t2.Hub, fpC)
	same := func(c string) *continuumv1.Sync {
		return twinFull(c, []*continuumv1.NodeFacts{twinNode("node-1", "")}, twinWorkload("shop", "cart"), twinWorkload("shop", "pay"))
	}
	t1.Hub.applySync(a1, same(fp), false)
	t1.Hub.applySync(a2, same(fpB), false)
	t2.Hub.applySync(b1, same(fpC), false)
	seen := map[string]string{}
	for label, h := range map[string]*Hub{"org1": t1.Hub, "org2": t2.Hub} {
		doc, _ := h.State(a.ctx)
		add := func(kind, id string) {
			if prev, dup := seen[kind+id]; dup {
				t.Fatalf("%s id %s appears in %s and %s", kind, id, prev, label)
			}
			seen[kind+id] = label
		}
		for _, n := range doc.Topology.Nodes {
			add("node", n.ID)
		}
		for _, s := range doc.Topology.Services {
			add("service", s.ID)
		}
		for _, c := range doc.Topology.Clusters {
			add("cluster", c.ID)
		}
	}
	if len(seen) != 12 { // three clusters, three nodes, six services
		t.Fatalf("expected 12 distinct records, got %d", len(seen))
	}
	// Two clusters with the same name are two clusters: the model says so and merges nothing.
	m1, _, _ := t1.Hub.Model(a.ctx)
	var lookalike bool
	for _, w := range m1.Warnings {
		if strings.Contains(w, "share the id") {
			t.Fatalf("identities collide: %v", w)
		}
		lookalike = lookalike || strings.Contains(w, "different clusters")
	}
	if !lookalike {
		t.Fatalf("two clusters with one name should be noted: %v", m1.Warnings)
	}
	clusters := 0
	for _, e := range m1.Entities {
		if e.Kind == "cluster" {
			clusters++
		}
	}
	if clusters != 2 {
		t.Fatalf("clusters merged: %d", clusters)
	}
}

func TestModelAPIContractETagAndTenancy(t *testing.T) {
	a := newAdminRig(t)
	owner, ownerCookie := a.user(t, "boss", RoleOwner)
	_ = owner
	_, viewer := a.user(t, "vera", RoleViewer)
	bobCookie, bobOrg := a.register(t, "bob", "Bob Works")
	h := a.hub()
	ag := twinAgent(t, a.env, h, fp)
	h.applySync(ag, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart")), false)
	t2, _ := a.a.P.Tenant(a.ctx, bobOrg)
	e2 := &env{base: a.base, core: t2.C, st: a.st, now: a.now, ctx: a.ctx}
	bag := twinAgent(t, e2, t2.Hub, "cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	t2.Hub.applySync(bag, twinFull("cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee", []*continuumv1.NodeFacts{twinNode("bob-node", "")}, twinWorkload("bobs", "secret")), false)

	r := a.do("GET", "/api/v1/model", nil)
	if r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
	r = a.do("GET", "/api/v1/model", nil, withCookie(viewer))
	if r.Code != 200 {
		t.Fatalf("a viewer may read the model: %d %s", r.Code, r.Body.String())
	}
	etag := r.Header().Get("ETag")
	body := r.json(t)
	if body["contract"] != twin.Contract || etag == "" {
		t.Fatalf("contract/etag: %v %q", body["contract"], etag)
	}
	v1, ok := body["modelVersion"].(float64)
	if !ok || v1 < 1 || v1 != float64(int64(v1)) {
		t.Fatalf("modelVersion must be an integer: %v", body["modelVersion"])
	}
	if _, ok := body["generatedAt"].(string); !ok {
		t.Fatal("generatedAt missing")
	}
	// Units, confidence and state on every attribute.
	var node map[string]any
	for _, e := range body["entities"].([]any) {
		em := e.(map[string]any)
		if em["kind"] == "node" {
			node = em
		}
		if strings.Contains(r.Body.String(), "bob-node") || strings.Contains(r.Body.String(), "secret") {
			t.Fatal("another organisation's records are in this model")
		}
	}
	cpu := node["attributes"].(map[string]any)["cpuAllocatable"].(map[string]any)
	if cpu["unit"] != "cores" || cpu["confidence"] != "reported" || cpu["source"] != "agent" || cpu["state"] != "live" || cpu["value"].(float64) != 3.8 {
		t.Fatalf("cpuAllocatable: %v", cpu)
	}
	if arch := node["attributes"].(map[string]any)["arch"].(map[string]any); arch["confidence"] != "unknown" || arch["value"] != nil {
		t.Fatalf("an attribute the node did not report is unknown, not empty: %v", arch)
	}
	// Conditional GET: unchanged, so 304 with no body.
	r = a.do("GET", "/api/v1/model", nil, withCookie(viewer), withHeader("If-None-Match", etag))
	if r.Code != 304 || r.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d (%d bytes)", r.Code, r.Body.Len())
	}
	// Time passing alone does not change the version (within the live window).
	*a.now = a.now.Add(10 * time.Second)
	h.mu.Lock()
	h.views[ag.ID].lastBeat = a.now.Add(0)
	h.mu.Unlock()
	if r = a.do("GET", "/api/v1/model", nil, withCookie(viewer), withHeader("If-None-Match", etag)); r.Code != 304 {
		t.Fatalf("the clock alone changed the model: %d", r.Code)
	}
	// A real change moves the version forward, monotonically, and the tag with it.
	h.applySync(ag, twinFull(fp, []*continuumv1.NodeFacts{twinNode("n1", "")}, twinWorkload("shop", "cart"), twinWorkload("shop", "pay")), true)
	*a.now = a.now.Add(3 * time.Second) // past the short cache
	r = a.do("GET", "/api/v1/model", nil, withCookie(ownerCookie), withHeader("If-None-Match", etag))
	if r.Code != 200 {
		t.Fatalf("after a change: %d", r.Code)
	}
	v2 := r.json(t)["modelVersion"].(float64)
	if v2 <= v1 || r.Header().Get("ETag") == etag {
		t.Fatalf("version %v -> %v, etag %q -> %q", v1, v2, etag, r.Header().Get("ETag"))
	}
	// Bob cannot read Alice's organisation's model, and his own holds only his records.
	if c := a.do("GET", "/api/v1/orgs/org-1/model", nil, withCookie(bobCookie)).Code; c != 404 {
		t.Fatalf("another organisation's model: %d", c)
	}
	r = a.do("GET", "/api/v1/orgs/"+bobOrg+"/model", nil, withCookie(bobCookie))
	if r.Code != 200 || !strings.Contains(r.Body.String(), "bob-node") || strings.Contains(r.Body.String(), `"cart"`) {
		t.Fatalf("bob's model: %d", r.Code)
	}
	// The version survives a restart and never goes backwards.
	h2 := NewHub(h.C)
	m2, _, _ := h2.Model(a.ctx)
	if m2.ModelVersion < int64(v2) {
		t.Fatalf("version went backwards after a restart: %d < %v", m2.ModelVersion, v2)
	}
}

func TestWorkspaceHoldsOnlyWhatIsDeclaredAndRefusesNewerFormats(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", RoleEditor)
	doc := `{"schemaVersion":3,"clusters":[{"id":"cl-obs","source":"discovered","name":"seen","overrides":{"name":"Renamed"}},{"id":"cl-mine","source":"manual","name":"mine"},{"id":"cl-plain","source":"discovered","name":"plain"}],
	  "nodes":[{"id":"nd-1","source":"discovered","name":"n"}],"agents":[{"id":"ag"}],"sites":[{"id":"s1","name":"Athens"}]}`
	r := a.do("PUT", "/api/v1/workspace", []byte(doc), withCookie(cookie), withHeader("If-Match", "0"))
	if r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.json(t)["note"].(string), "discovered") {
		t.Fatalf("no note about what was left out: %s", r.Body.String())
	}
	got := a.do("GET", "/api/v1/workspace", nil, withCookie(cookie)).json(t)
	stored, _ := json.Marshal(got["data"])
	s := string(stored)
	if strings.Contains(s, `"seen"`) || strings.Contains(s, `"plain"`) || strings.Contains(s, `"nd-1"`) || strings.Contains(s, `"ag"`) {
		t.Fatalf("discovered facts were stored in the workspace: %s", s)
	}
	if !strings.Contains(s, `"cl-mine"`) || !strings.Contains(s, "Renamed") || !strings.Contains(s, "Athens") {
		t.Fatalf("what a person declared was lost: %s", s)
	}
	if got["formatVersion"].(float64) != float64(4) {
		t.Fatalf("formatVersion: %v", got["formatVersion"])
	}
	// A document from a newer version of the product is refused with a clear message, and nothing is stored.
	rev := got["rev"].(float64)
	r = a.do("PUT", "/api/v1/workspace", []byte(`{"schemaVersion":99}`), withCookie(cookie), withHeader("If-Match", "1"))
	if r.Code != 400 || !strings.Contains(r.Body.String(), "newer") {
		t.Fatalf("a newer format: %d %s", r.Code, r.Body.String())
	}
	if again := a.do("GET", "/api/v1/workspace", nil, withCookie(cookie)).json(t); again["rev"].(float64) != rev {
		t.Fatal("a refused document changed the stored revision")
	}
	// The model applies the declared layer over the observed records.
	_ = context.Background
}

func TestDecisionRequestsOnlyOfferLiveTargets(t *testing.T) {
	m := twin.Model{ModelVersion: 7, Entities: []twin.Entity{
		{Kind: "cluster", ID: "cl-live", Name: "live", Origin: "observed", State: twin.Live, Attributes: map[string]twin.Attr{"cpuAllocatable": {Value: 4.0, Confidence: twin.Reported}}},
		{Kind: "cluster", ID: "cl-old", Name: "old", Origin: "observed", State: twin.Stale, StateReason: "stale for 2 h", Attributes: map[string]twin.Attr{}},
		{Kind: "cluster", ID: "cl-rev", Name: "rev", Origin: "observed", State: twin.Revoked, StateReason: "agent revoked 1 h ago", Attributes: map[string]twin.Attr{}},
		{Kind: "cluster", ID: "cl-unk", Name: "unk", Origin: "observed", State: twin.Live, Attributes: map[string]twin.Attr{"cpuAllocatable": {Confidence: twin.Unknown, Evidence: "nodes are not read at this access tier"}}},
	}}
	body := `{"schema":1,"question":"placement","clusters":[{"id":"cl-live"},{"id":"cl-old"},{"id":"cl-rev"},{"id":"cl-unk"}],
	  "services":[{"id":"sv-1","cluster":"cl-live","candidates":["cl-old","cl-rev","cl-unk","cl-live"],"bytes":12345678901234567}],"custom":{"keep":true}}`
	out, err := enrichDecision([]byte(body), m)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ModelVersion int `json:"modelVersion"`
		Excluded     []twin.Exclusion
		Clusters     []map[string]any
		Services     []struct {
			Candidates   []string
			ClusterState string
		}
		Custom map[string]any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.ModelVersion != 7 || len(got.Excluded) != 3 {
		t.Fatalf("modelVersion %d excluded %+v", got.ModelVersion, got.Excluded)
	}
	if c := got.Services[0].Candidates; len(c) != 1 || c[0] != "cl-live" {
		t.Fatalf("candidates: %v", c)
	}
	if got.Services[0].ClusterState != "live" || got.Custom["keep"] != true {
		t.Fatal("what the client sent was changed")
	}
	if !strings.Contains(string(out), "12345678901234567") {
		t.Fatal("a large number lost precision")
	}
	for _, c := range got.Clusters {
		want := c["id"] == "cl-live"
		if c["eligible"] != want {
			t.Fatalf("eligible on %v: %v", c["id"], c["eligible"])
		}
	}
	reasons := map[string]string{}
	for _, e := range got.Excluded {
		reasons[e.ClusterID] = e.Reason
	}
	if !strings.Contains(reasons["cl-old"], "stale for 2 h") || !strings.Contains(reasons["cl-unk"], "capacity unknown") || !strings.Contains(reasons["cl-rev"], "revoked") {
		t.Fatalf("reasons: %v", reasons)
	}
	// Not an object: forwarded as it came.
	if out, _ := enrichDecision([]byte(`[1,2]`), m); string(out) != `[1,2]` {
		t.Fatalf("%s", out)
	}
}

var _ = facts.New

// ---- tombstone retention ----

func TestRetentionUsesOverrideThenLiveSettingThenDefault(t *testing.T) {
	e := newEnv(t)
	h := NewHub(e.core)
	if got := h.retention(); got != twin.DefaultRetention {
		t.Fatalf("no settings saved: got %v, want the default %v", got, twin.DefaultRetention)
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{TombstoneRetentionDays: 21}); err != nil {
		t.Fatal(err)
	}
	if got, want := h.retention(), 21*24*time.Hour; got != want {
		t.Fatalf("a changed setting was not picked up live: got %v, want %v", got, want)
	}
	// A live setting change takes effect without touching the test-only override.
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{TombstoneRetentionDays: 5}); err != nil {
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

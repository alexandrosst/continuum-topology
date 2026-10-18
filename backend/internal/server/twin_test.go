package server

import (
	"context"
	"continuum/internal/store"
	"continuum/internal/testkit"
	"encoding/json"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/decisionloop"
	"continuum/internal/estate"
	"continuum/internal/facts"
	"continuum/internal/twin"
)

func TestSameNamesInTwoClustersAndTwoOrganisationsNeverCollide(t *testing.T) {
	a := newAdminRig(t)
	_, bobOrg := a.register(t, "bob", "Bob Works")
	p := a.a.P
	t1, _ := p.Tenant(a.ctx, "org-1")
	t2, _ := p.Tenant(a.ctx, bobOrg)
	const fpB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const fpC = "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	a1 := twinAgent(t, a.env, t1.Hub, testkit.FP)
	a2 := twinAgent(t, a.env, t1.Hub, fpB)
	e2 := &env{base: a.base, core: t2.C, st: a.st, now: a.now, ctx: a.ctx}
	b1 := twinAgent(t, e2, t2.Hub, fpC)
	same := func(c string) *continuumv1.Sync {
		return testkit.TwinFull(c, []*continuumv1.NodeFacts{testkit.TwinNode("node-1", "")}, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay"))
	}
	t1.Hub.ApplySync(a1, same(testkit.FP), false)
	t1.Hub.ApplySync(a2, same(fpB), false)
	t2.Hub.ApplySync(b1, same(fpC), false)
	seen := map[string]string{}
	for label, h := range map[string]*estate.Hub{"org1": t1.Hub, "org2": t2.Hub} {
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
	owner, ownerCookie := a.user(t, "boss", accounts.RoleOwner)
	_ = owner
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	bobCookie, bobOrg := a.register(t, "bob", "Bob Works")
	h := a.hub()
	ag := twinAgent(t, a.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*continuumv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false)
	t2, _ := a.a.P.Tenant(a.ctx, bobOrg)
	e2 := &env{base: a.base, core: t2.C, st: a.st, now: a.now, ctx: a.ctx}
	bag := twinAgent(t, e2, t2.Hub, "cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	t2.Hub.ApplySync(bag, testkit.TwinFull("cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee", []*continuumv1.NodeFacts{testkit.TwinNode("bob-node", "")}, testkit.TwinWorkload("bobs", "secret")), false)

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
	h.Mu.Lock()
	h.Views[ag.ID].LastBeat = a.now.Add(0)
	h.Mu.Unlock()
	if r = a.do("GET", "/api/v1/model", nil, withCookie(viewer), withHeader("If-None-Match", etag)); r.Code != 304 {
		t.Fatalf("the clock alone changed the model: %d", r.Code)
	}
	// A real change moves the version forward, monotonically, and the tag with it.
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*continuumv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")), true)
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
	h2 := estate.NewHub(h.C)
	m2, _, _ := h2.Model(a.ctx)
	if m2.ModelVersion < int64(v2) {
		t.Fatalf("version went backwards after a restart: %d < %v", m2.ModelVersion, v2)
	}
}

// TestModelCacheHitsAndMissesAreCounted covers twinRT's own CacheHits/CacheMisses counters (see
// ServerSelfStatsSample.ModelCacheHits' own doc comment for what a caller does with them): a second call
// within modelCacheTTL must be a hit, a change to an input (here, a new sync - the same gen bump a real
// agent picture would cause) must force a miss on the next call, and the very first call of all, with
// nothing cached yet, must also count as a miss, never silently skipped.
func TestModelCacheHitsAndMissesAreCounted(t *testing.T) {
	a := newAdminRig(t)
	h := a.hub()
	ag := twinAgent(t, a.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*continuumv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart")), false)

	if h.Tw.CacheHits.Load() != 0 || h.Tw.CacheMisses.Load() != 0 {
		t.Fatalf("before any call: hits=%d misses=%d, want 0/0", h.Tw.CacheHits.Load(), h.Tw.CacheMisses.Load())
	}
	if _, _, err := h.Model(a.ctx); err != nil {
		t.Fatal(err)
	}
	if h.Tw.CacheMisses.Load() != 1 || h.Tw.CacheHits.Load() != 0 {
		t.Fatalf("first call: hits=%d misses=%d, want 0/1", h.Tw.CacheHits.Load(), h.Tw.CacheMisses.Load())
	}
	// A second call with nothing changed, still inside modelCacheTTL: a hit.
	if _, _, err := h.Model(a.ctx); err != nil {
		t.Fatal(err)
	}
	if h.Tw.CacheHits.Load() != 1 || h.Tw.CacheMisses.Load() != 1 {
		t.Fatalf("second call (cached): hits=%d misses=%d, want 1/1", h.Tw.CacheHits.Load(), h.Tw.CacheMisses.Load())
	}
	// A real change (gen bumps) forces a rebuild on the next call: another miss, not a third hit.
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*continuumv1.NodeFacts{testkit.TwinNode("n1", "")}, testkit.TwinWorkload("shop", "cart"), testkit.TwinWorkload("shop", "pay")), true)
	*a.now = a.now.Add(3 * time.Second) // past the short cache, same as TestModelAPIContractETagAndTenancy
	if _, _, err := h.Model(a.ctx); err != nil {
		t.Fatal(err)
	}
	if h.Tw.CacheMisses.Load() != 2 || h.Tw.CacheHits.Load() != 1 {
		t.Fatalf("after a real change: hits=%d misses=%d, want 1/2", h.Tw.CacheHits.Load(), h.Tw.CacheMisses.Load())
	}
}

func TestWorkspaceHoldsOnlyWhatIsDeclaredAndRefusesNewerFormats(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "ed", accounts.RoleEditor)
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
	out, err := decisionloop.EnrichDecision([]byte(body), m)
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
	if out, _ := decisionloop.EnrichDecision([]byte(`[1,2]`), m); string(out) != `[1,2]` {
		t.Fatalf("%s", out)
	}
}

var _ = facts.New

// ---- tombstone retention ----

// twinAgent approves an agent for a cluster in the hub's organisation and gives it an empty picture, connected.
func twinAgent(t *testing.T, e *env, h *estate.Hub, cluster string) store.Agent {
	t.Helper()
	id, _, _ := e.approvedAgent(t, cluster)
	a, err := e.st.GetAgent(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	h.Mu.Lock()
	v := estate.NewView()
	v.Connected = true
	v.LastBeat = h.C.Now()
	h.Views[id] = v
	h.Mu.Unlock()
	return a
}

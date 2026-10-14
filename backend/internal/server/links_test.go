package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/store"
)

func TestLinkPairsAreCanonicalAndChecked(t *testing.T) {
	got, err := normalizeLinks([]LinkSetting{{A: "cl-b", B: "cl-a"}, {A: "cl-a", B: "cl-b"}, {A: " cl-c ", B: "cl-a"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name, in := range map[string][]LinkSetting{
		"empty":    {{A: "cl-a"}},
		"itself":   {{A: "cl-a", B: "cl-a"}},
		"too long": {{A: strings.Repeat("x", 65), B: "cl-a"}},
	} {
		if _, err := normalizeLinks(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := make([]LinkSetting, maxLinks+1)
	for i := range many {
		many[i] = LinkSetting{A: "cl-a", B: "cl-" + strings.Repeat("z", i+1)}
	}
	if _, err := normalizeLinks(many); err == nil {
		t.Error("more than the limit was accepted")
	}
}

func TestSettingsNormalizeTheirLinks(t *testing.T) {
	s := DefaultSettings()
	s.Links = []LinkSetting{{A: "cl-b", B: "cl-a"}}
	got, err := s.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Links) != 1 || got.Links[0].A != "cl-a" {
		t.Fatalf("links = %v", got.Links)
	}
	s.Links = []LinkSetting{{A: "x", B: "x"}}
	if _, err := s.Normalize(); err == nil {
		t.Fatal("a self-link was accepted")
	}
}

// nodeState is one node with the given internal address and, if any, external ones.
func nodeState(internal string, external ...string) *facts.State {
	n := &continuumv1.NodeFacts{InternalIps: []string{internal}}
	for _, e := range external {
		if e != "" {
			n.ExternalIps = append(n.ExternalIps, e)
		}
	}
	return &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": n}}
}

func linkHub(views map[string]*view) *Hub { return &Hub{views: views} }

func ag(id, cluster, name, ip string, tier int) store.Agent {
	return store.Agent{ID: id, ClusterID: cluster, Name: name, Status: store.StatusApproved, AccessTier: tier, ConnectingIP: ip}
}

func TestEachClusterIsToldOnlyAboutItsPairedPeers(t *testing.T) {
	a, b, c := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2), ag("ag-c", "cl-c", "gamma", "203.0.113.3", 2)
	h := linkHub(map[string]*view{
		"ag-a": {state: nodeState("10.0.0.1", "198.51.100.1")},
		"ag-b": {state: nodeState("10.1.0.1", "198.51.100.2")},
		"ag-c": {state: nodeState("10.2.0.1")},
	})
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}}
	all := []store.Agent{a, b, c}

	pa := h.linkPeers(a, all, links, false, nil)
	if len(pa) != 1 || pa[0].ClusterId != "cl-b" || pa[0].Name != "beta" {
		t.Fatalf("alpha was told %v", pa)
	}
	// the peer's node addresses and the address its agent connects from, sorted
	if want := []string{"10.1.0.1", "198.51.100.2", "203.0.113.2"}; !reflect.DeepEqual(pa[0].Addresses, want) {
		t.Fatalf("addresses = %v, want %v", pa[0].Addresses, want)
	}
	pb := h.linkPeers(b, all, links, false, nil)
	if len(pb) != 1 || pb[0].ClusterId != "cl-a" {
		t.Fatalf("beta was told %v", pb)
	}
	if pc := h.linkPeers(c, all, links, false, nil); len(pc) != 0 {
		t.Fatalf("a cluster in no pair was told about peers: %v", pc)
	}
}

func TestNoPeersBelowTierTwoWhileFlowIsPausedOrForUnknownClusters(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}})
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-zzz"}}
	if got := h.linkPeers(a, []store.Agent{a, b}, links, false, nil); len(got) != 1 {
		t.Fatalf("an unknown cluster should be skipped, got %v", got)
	}
	low := a
	low.AccessTier = 1
	if got := h.linkPeers(low, []store.Agent{low, b}, links, false, nil); got != nil {
		t.Fatalf("tier 1 was given peers: %v", got)
	}
	if got := h.linkPeers(a, []store.Agent{a, b}, links, true, nil); got != nil {
		t.Fatalf("a paused flow collector was given peers: %v", got)
	}
	pending := b
	pending.Status = store.StatusPending
	if got := h.linkPeers(a, []store.Agent{a, pending}, links, false, nil); len(got) != 0 {
		t.Fatalf("an unapproved peer was shared: %v", got)
	}
	noAddr := ag("ag-b", "cl-b", "beta", "", 2)
	h2 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}})
	if got := h2.linkPeers(a, []store.Agent{a, noAddr}, links, false, nil); len(got) != 0 {
		t.Fatalf("a peer with no known address was shared: %v", got)
	}
}

func TestAddressesTheTwoClustersShareAreNotSent(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	// both clusters use 10.0.0.1 for a node
	h := linkHub(map[string]*view{
		"ag-a": {state: nodeState("10.0.0.1")},
		"ag-b": {state: nodeState("10.0.0.1", "10.0.0.7")},
	})
	got := h.linkPeers(a, []store.Agent{a, b}, []LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	for _, ip := range got[0].Addresses {
		if ip == "10.0.0.1" {
			t.Fatalf("an address both clusters use was sent as the peer's: %v", got[0].Addresses)
		}
	}
	if !hasAddr(got[0].Addresses, "10.0.0.7") || !hasAddr(got[0].Addresses, "203.0.113.2") {
		t.Fatalf("the peer's own addresses went missing: %v", got[0].Addresses)
	}
}

func TestTheConfigHashChangesWithThePeers(t *testing.T) {
	base := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a"}
	h1 := configHash(base)
	withPeer := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "beta", Addresses: []string{"10.1.0.1"}}}}
	h2 := configHash(withPeer)
	moved := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "beta", Addresses: []string{"10.1.0.2"}}}}
	if h1 == h2 || h2 == configHash(moved) {
		t.Fatal("a change in the peer list does not change the config hash, so it would never be pushed")
	}
	if configHash(withPeer) != h2 {
		t.Fatal("the hash is not stable")
	}
}

func hasAddr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestEachPairKeepsItsOwnChoicesAndTheLastEntryOfAPairStands(t *testing.T) {
	got, err := normalizeLinks([]LinkSetting{
		{A: "cl-b", B: "cl-a"},
		{A: "cl-a", B: "cl-b", NoProbe: true, Throughput: &LinkThroughput{IntervalMinutes: 60, BudgetMB: 2048}},
		{A: "cl-a", B: "cl-c", Throughput: &LinkThroughput{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].NoProbe || got[0].Throughput == nil || got[0].Throughput.IntervalMinutes != 60 || got[0].Throughput.BudgetMB != 2048 {
		t.Fatalf("the later entry of the pair did not win: %+v", got)
	}
	if got[1].NoProbe || got[1].Throughput == nil {
		t.Fatalf("%+v", got[1])
	}
	// What was returned does not alias the caller's value.
	in := &LinkThroughput{BudgetMB: 1}
	out, _ := normalizeLinks([]LinkSetting{{A: "a", B: "b", Throughput: in}})
	in.BudgetMB = 99
	if out[0].Throughput.BudgetMB != 1 {
		t.Error("the stored setting shares memory with the request")
	}
	for name, th := range map[string]*LinkThroughput{
		"interval too short": {IntervalMinutes: 5},
		"interval too long":  {IntervalMinutes: 7*24*60 + 1},
		"budget too big":     {BudgetMB: 1<<20 + 1},
	} {
		if _, err := normalizeLinks([]LinkSetting{{A: "a", B: "b", Throughput: th}}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestThroughputTestsAreAskedOfOneSideOfThePairAndTheChoicesReachBoth(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}})
	links := []LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: true, Throughput: &LinkThroughput{IntervalMinutes: 90, BudgetMB: 512}}}
	fromA := h.linkPeers(a, []store.Agent{a, b}, links, false, nil)
	fromB := h.linkPeers(b, []store.Agent{a, b}, links, false, nil)
	if len(fromA) != 1 || len(fromB) != 1 {
		t.Fatalf("%v / %v", fromA, fromB)
	}
	if !fromA[0].Throughput || fromA[0].ThroughputIntervalSeconds != 90*60 || fromA[0].ThroughputBudgetBytes != 512<<20 {
		t.Errorf("the side that sorts first does not run the tests: %+v", fromA[0])
	}
	if fromB[0].Throughput || fromB[0].ThroughputIntervalSeconds != 0 || fromB[0].ThroughputBudgetBytes != 0 {
		t.Errorf("the serving side is asked to run them too: %+v", fromB[0])
	}
	if !fromA[0].NoProbe || !fromB[0].NoProbe {
		t.Error("the probe choice did not reach both sides")
	}
	plain := h.linkPeers(a, []store.Agent{a, b}, []LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
	if plain[0].NoProbe || plain[0].Throughput {
		t.Errorf("a plain pair must get the probe and no throughput tests: %+v", plain[0])
	}
}

func TestOnlyTheFirstClusterOfAPairRunsTheProbeWhileItCan(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	views := func() map[string]*view {
		return map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}}
	}
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}}
	all := []store.Agent{a, b}
	skip := func(h *Hub, from store.Agent, paused map[string]bool) bool {
		got := h.linkPeers(from, all, links, paused[from.ID], paused)
		if len(got) != 1 {
			t.Fatalf("%s was told %v", from.ID, got)
		}
		return got[0].SkipProbe
	}

	h := linkHub(views())
	if skip(h, a, nil) {
		t.Error("the first cluster was told to skip the probe")
	}
	if !skip(h, b, nil) {
		t.Error("the second cluster still probes: the pair is measured twice")
	}

	// Nobody may be left without a measurer: the second cluster probes whenever the first cannot.
	if !skip(h, b, map[string]bool{}) {
		t.Error("an empty paused map must read as not paused")
	}
	if skip(h, b, map[string]bool{"ag-a": true}) {
		t.Error("the first cluster has the flow collector paused, so it measures nothing, yet the second skips")
	}
	low := a
	low.AccessTier = 1
	if got := h.linkPeers(b, []store.Agent{low, b}, links, false, nil); len(got) != 1 || got[0].SkipProbe {
		t.Errorf("the first cluster is below tier 2: %v", got)
	}
	pending := a
	pending.Status = store.StatusPending
	if got := h.linkPeers(b, []store.Agent{pending, b}, links, false, nil); len(got) != 0 {
		t.Errorf("an unapproved first cluster was shared: %v", got)
	}
	// The first cluster has told the server nothing: the second is not given it as a peer at all.
	noAddr := ag("ag-a", "cl-a", "alpha", "", 2)
	silent := linkHub(map[string]*view{"ag-b": {state: nodeState("10.1.0.1")}})
	if got := silent.linkPeers(b, []store.Agent{noAddr, b}, links, false, nil); len(got) != 0 {
		t.Errorf("a first cluster that has told nothing yet was shared: %v", got)
	}
	// The second cluster has told nothing yet, so the first is not told about it and measures nothing: the second, which is
	// given the first, must keep probing.
	quiet := ag("ag-b", "cl-b", "beta", "", 2)
	h2 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}})
	if got := h2.linkPeers(b, []store.Agent{a, quiet}, links, false, nil); len(got) != 1 || got[0].SkipProbe {
		t.Errorf("the first cluster is not told about the second, yet the second skips its probe: %v", got)
	}
	// The second cluster's addresses are all shared with the first: the first drops the peer, so the second keeps probing.
	h3 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1", "10.9.9.9")}, "ag-b": {state: nodeState("10.9.9.9")}})
	bare := ag("ag-b", "cl-b", "beta", "", 2)
	got := h3.linkPeers(bare, []store.Agent{a, bare}, links, false, nil)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if got[0].SkipProbe {
		t.Error("the first cluster cannot use any address of the second, yet the second skips its probe")
	}

	// With the probe off for the pair nothing is skipped (and nothing is probed); the choice of the pair wins.
	off := []LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: true}}
	if got := h.linkPeers(b, all, off, false, nil); len(got) != 1 || got[0].SkipProbe || !got[0].NoProbe {
		t.Errorf("noProbe pair: %v", got)
	}
	// The runner is the first of the pair whichever way round the pair is listed, and a third cluster is unaffected.
	c := ag("ag-c", "cl-c", "gamma", "203.0.113.3", 2)
	h4 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}, "ag-c": {state: nodeState("10.2.0.1")}})
	two := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-b", B: "cl-c"}}
	fromB := h4.linkPeers(b, []store.Agent{a, b, c}, two, false, nil)
	if len(fromB) != 2 || !fromB[0].SkipProbe || fromB[1].SkipProbe {
		t.Errorf("cl-b skips towards cl-a (which sorts first) and probes cl-c: %+v", fromB)
	}
	if fromC := h4.linkPeers(c, []store.Agent{a, b, c}, two, false, nil); len(fromC) != 1 || !fromC[0].SkipProbe {
		t.Errorf("cl-c does not skip towards cl-b: %+v", fromC)
	}
}

func TestTheConfigHashChangesWithThePairChoices(t *testing.T) {
	mk := func(lp *continuumv1.LinkPeer) string {
		lp.ClusterId, lp.Name, lp.Addresses = "cl-b", "beta", []string{"10.1.0.1"}
		return configHash(&continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{lp}})
	}
	base := mk(&continuumv1.LinkPeer{})
	seen := map[string]bool{base: true}
	for name, lp := range map[string]*continuumv1.LinkPeer{
		"no probe": {NoProbe: true}, "skip probe": {SkipProbe: true}, "throughput": {Throughput: true}, "interval": {Throughput: true, ThroughputIntervalSeconds: 600}, "budget": {Throughput: true, ThroughputBudgetBytes: 1 << 20},
	} {
		h := mk(lp)
		if seen[h] {
			t.Errorf("%s does not change the hash, so it would never be pushed", name)
		}
		seen[h] = true
	}
}

func TestPairChoicesAreSavedWithTheSettingsUnderTheNamesTheUIUses(t *testing.T) {
	var s Settings
	in := `{"links":[{"a":"cl-b","b":"cl-a","noProbe":true,"throughput":{"intervalMinutes":120,"budgetMB":4096}},{"a":"cl-a","b":"cl-c"}]}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	links, err := normalizeLinks(s.Links)
	if err != nil {
		t.Fatal(err)
	}
	s.Links = links
	out, _ := json.Marshal(s.Links)
	if string(out) != `[{"a":"cl-a","b":"cl-b","noProbe":true,"throughput":{"intervalMinutes":120,"budgetMB":4096}},{"a":"cl-a","b":"cl-c"}]` {
		t.Errorf("%s", out)
	}
}

// The second cluster of a pair stops probing only while the first can: a pause of the first cluster's flow collector, which only
// its own consent records, has to reach the second cluster's Config.
func TestTheSecondClusterProbesAgainWhenTheFirstOnesFlowCollectorIsPaused(t *testing.T) {
	r := newHubRig(t)
	id1, _, _ := r.approvedAgent(t, fp)
	id2, _, _ := r.approvedAgent(t, fp2)
	a1, _ := r.st.GetAgent(r.ctx, id1)
	a2, _ := r.st.GetAgent(r.ctx, id2)
	if a1.ClusterID == "" || a2.ClusterID == "" || a1.ClusterID == a2.ClusterID {
		t.Fatalf("clusters %q %q", a1.ClusterID, a2.ClusterID)
	}
	first, second := a1, a2
	if second.ClusterID < first.ClusterID {
		first, second = second, first
	}
	if _, err := r.core.SaveSettings(r.ctx, "alex", Settings{Links: []LinkSetting{{A: first.ClusterID, B: second.ClusterID}}}); err != nil {
		t.Fatal(err)
	}
	newV := func(ip string) *view {
		return &view{state: nodeState(ip), flows: newFlowTable(), paths: map[string]*pathTrack{}, targets: map[string]issuedTarget{}}
	}
	fv, sv := newV("10.0.0.1"), newV("10.9.0.1")
	r.hub.mu.Lock()
	r.hub.views[first.ID], r.hub.views[second.ID] = fv, sv
	r.hub.mu.Unlock()
	skips := func() bool {
		t.Helper()
		cfg := r.hub.configFor(second, sv)
		if len(cfg.LinkPeers) != 1 || cfg.LinkPeers[0].ClusterId != first.ClusterID {
			t.Fatalf("second cluster's peers = %v", cfg.LinkPeers)
		}
		return cfg.LinkPeers[0].SkipProbe
	}
	if cfg := r.hub.configFor(first, fv); len(cfg.LinkPeers) != 1 || cfg.LinkPeers[0].SkipProbe {
		t.Fatalf("the first cluster must probe: %v", cfg.LinkPeers)
	}
	if !skips() {
		t.Error("the second cluster still probes beside a first cluster that measures")
	}
	if _, err := r.hub.SetConsent(r.ctx, "edith", first.ID, Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	if skips() {
		t.Error("the first cluster's flow collector is paused, so nobody measures, yet the second skips its probe")
	}
	if _, err := r.hub.SetConsent(r.ctx, "edith", first.ID, Consent{}); err != nil {
		t.Fatal(err)
	}
	if !skips() {
		t.Error("the first cluster measures again, but the second still probes")
	}
}

func TestAPairThatIsNotMeasuredSaysWhy(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}}
	codes := func(ns []LinkNote) string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Cluster+":"+n.Code)
		}
		return strings.Join(out, " ")
	}
	h := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}})

	if got := h.linkNotes(links, []store.Agent{a, b}, nil); len(got) != 0 {
		t.Fatalf("a pair that is measured has notes: %v", got)
	}
	// no approved agent at the far end: one note, about that cluster
	pending := b
	pending.Status = store.StatusPending
	if got := codes(h.linkNotes(links, []store.Agent{a, pending}, nil)); got != "cl-b:no-agent" {
		t.Fatalf("no-agent: %q", got)
	}
	low := a
	low.AccessTier = 1
	if got := codes(h.linkNotes(links, []store.Agent{low, b}, nil)); got != "cl-a:tier" {
		t.Fatalf("tier: %q", got)
	}
	if got := codes(h.linkNotes(links, []store.Agent{a, b}, map[string]bool{"ag-b": true})); got != "cl-b:paused" {
		t.Fatalf("paused: %q", got)
	}
	// a peer that has reported no address and does not connect from a public one
	h2 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}})
	noAddr := ag("ag-b", "cl-b", "beta", "", 2)
	if got := codes(h2.linkNotes(links, []store.Agent{a, noAddr}, nil)); got != "cl-a:peer-silent" {
		t.Fatalf("peer-silent: %q", got)
	}
	// every address of the peer is also this cluster's
	same := ag("ag-b", "cl-b", "beta", "203.0.113.1", 2)
	h3 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.0.0.1")}})
	ns := h3.linkNotes(links, []store.Agent{a, same}, nil)
	if len(ns) == 0 || ns[0].Code != "shared-address" || !strings.Contains(ns[0].Text, "10.0.0.1") {
		t.Fatalf("shared-address: %v", ns)
	}
	// the same cluster in two pairs is told once that it has no agent
	if got := codes(h.linkNotes([]LinkSetting{{A: "cl-a", B: "cl-z"}, {A: "cl-b", B: "cl-z"}}, []store.Agent{a, b}, nil)); got != "cl-z:no-agent" {
		t.Fatalf("one note per cluster: %q", got)
	}
}

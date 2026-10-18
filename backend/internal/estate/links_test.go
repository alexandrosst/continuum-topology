package estate

import (
	"reflect"
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/observed"
	"continuum/internal/store"
)

func TestEachClusterIsToldOnlyAboutItsPairedPeers(t *testing.T) {
	a, b, c := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2), ag("ag-c", "cl-c", "gamma", "203.0.113.3", 2)
	h := linkHub(map[string]*View{
		"ag-a": {EstView: EstView{State: nodeState("10.0.0.1", "198.51.100.1")}},
		"ag-b": {EstView: EstView{State: nodeState("10.1.0.1", "198.51.100.2")}},
		"ag-c": {EstView: EstView{State: nodeState("10.2.0.1")}},
	})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
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
	h := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-zzz"}}
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
	h2 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}})
	if got := h2.linkPeers(a, []store.Agent{a, noAddr}, links, false, nil); len(got) != 0 {
		t.Fatalf("a peer with no known address was shared: %v", got)
	}
}

func TestAddressesTheTwoClustersShareAreNotSent(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	// both clusters use 10.0.0.1 for a node
	h := linkHub(map[string]*View{
		"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}},
		"ag-b": {EstView: EstView{State: nodeState("10.0.0.1", "10.0.0.7")}},
	})
	got := h.linkPeers(a, []store.Agent{a, b}, []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
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

func hasAddr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestThroughputTestsAreAskedOfOneSideOfThePairAndTheChoicesReachBoth(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: true, Throughput: &kernel.LinkThroughput{IntervalMinutes: 90, BudgetMB: 512}}}
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
	plain := h.linkPeers(a, []store.Agent{a, b}, []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
	if plain[0].NoProbe || plain[0].Throughput {
		t.Errorf("a plain pair must get the probe and no throughput tests: %+v", plain[0])
	}
}

func TestOnlyTheFirstClusterOfAPairRunsTheProbeWhileItCan(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	views := func() map[string]*View {
		return map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}}
	}
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
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
	silent := linkHub(map[string]*View{"ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})
	if got := silent.linkPeers(b, []store.Agent{noAddr, b}, links, false, nil); len(got) != 0 {
		t.Errorf("a first cluster that has told nothing yet was shared: %v", got)
	}
	// The second cluster has told nothing yet, so the first is not told about it and measures nothing: the second, which is
	// given the first, must keep probing.
	quiet := ag("ag-b", "cl-b", "beta", "", 2)
	h2 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}})
	if got := h2.linkPeers(b, []store.Agent{a, quiet}, links, false, nil); len(got) != 1 || got[0].SkipProbe {
		t.Errorf("the first cluster is not told about the second, yet the second skips its probe: %v", got)
	}
	// The second cluster's addresses are all shared with the first: the first drops the peer, so the second keeps probing.
	h3 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1", "10.9.9.9")}}, "ag-b": {EstView: EstView{State: nodeState("10.9.9.9")}}})
	bare := ag("ag-b", "cl-b", "beta", "", 2)
	got := h3.linkPeers(bare, []store.Agent{a, bare}, links, false, nil)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if got[0].SkipProbe {
		t.Error("the first cluster cannot use any address of the second, yet the second skips its probe")
	}

	// With the probe off for the pair nothing is skipped (and nothing is probed); the choice of the pair wins.
	off := []kernel.LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: true}}
	if got := h.linkPeers(b, all, off, false, nil); len(got) != 1 || got[0].SkipProbe || !got[0].NoProbe {
		t.Errorf("noProbe pair: %v", got)
	}
	// The runner is the first of the pair whichever way round the pair is listed, and a third cluster is unaffected.
	c := ag("ag-c", "cl-c", "gamma", "203.0.113.3", 2)
	h4 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}, "ag-c": {EstView: EstView{State: nodeState("10.2.0.1")}}})
	two := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-b", B: "cl-c"}}
	fromB := h4.linkPeers(b, []store.Agent{a, b, c}, two, false, nil)
	if len(fromB) != 2 || !fromB[0].SkipProbe || fromB[1].SkipProbe {
		t.Errorf("cl-b skips towards cl-a (which sorts first) and probes cl-c: %+v", fromB)
	}
	if fromC := h4.linkPeers(c, []store.Agent{a, b, c}, two, false, nil); len(fromC) != 1 || !fromC[0].SkipProbe {
		t.Errorf("cl-c does not skip towards cl-b: %+v", fromC)
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
	if _, err := r.core.SaveSettings(r.ctx, "alex", kernel.Settings{Links: []kernel.LinkSetting{{A: first.ClusterID, B: second.ClusterID}}}); err != nil {
		t.Fatal(err)
	}
	newV := func(ip string) *View {
		return &View{EstView: EstView{State: nodeState(ip), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
	}
	fv, sv := newV("10.0.0.1"), newV("10.9.0.1")
	r.hub.Mu.Lock()
	r.hub.Views[first.ID], r.hub.Views[second.ID] = fv, sv
	r.hub.Mu.Unlock()
	skips := func() bool {
		t.Helper()
		cfg := r.hub.ConfigFor(second, sv)
		if len(cfg.LinkPeers) != 1 || cfg.LinkPeers[0].ClusterId != first.ClusterID {
			t.Fatalf("second cluster's peers = %v", cfg.LinkPeers)
		}
		return cfg.LinkPeers[0].SkipProbe
	}
	if cfg := r.hub.ConfigFor(first, fv); len(cfg.LinkPeers) != 1 || cfg.LinkPeers[0].SkipProbe {
		t.Fatalf("the first cluster must probe: %v", cfg.LinkPeers)
	}
	if !skips() {
		t.Error("the second cluster still probes beside a first cluster that measures")
	}
	if _, err := r.hub.SetConsent(r.ctx, "edith", first.ID, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	if skips() {
		t.Error("the first cluster's flow collector is paused, so nobody measures, yet the second skips its probe")
	}
	if _, err := r.hub.SetConsent(asRole(accounts.RoleAdmin), "edith", first.ID, agents.Consent{}); err != nil {
		t.Fatal(err)
	}
	if !skips() {
		t.Error("the first cluster measures again, but the second still probes")
	}
}

func TestAPairThatIsNotMeasuredSaysWhy(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
	codes := func(ns []connectivity.LinkNote) string {
		var out []string
		for _, n := range ns {
			if n.Code != "private-range" { // a hint, tested on its own
				out = append(out, n.Cluster+":"+n.Code)
			}
		}
		return strings.Join(out, " ")
	}
	h := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})

	// (Both clusters' nodes have private addresses only, which carries the range hint; that is tested on its own.)
	for _, n := range h.LinkNotes(links, []store.Agent{a, b}, nil) {
		if n.Code != "private-range" {
			t.Fatalf("a pair that is measured has a note: %v", n)
		}
	}
	// no approved agent at the far end: one note, about that cluster
	pending := b
	pending.Status = store.StatusPending
	if got := codes(h.LinkNotes(links, []store.Agent{a, pending}, nil)); got != "cl-b:no-agent" {
		t.Fatalf("no-agent: %q", got)
	}
	low := a
	low.AccessTier = 1
	if got := codes(h.LinkNotes(links, []store.Agent{low, b}, nil)); got != "cl-a:tier" {
		t.Fatalf("tier: %q", got)
	}
	if got := codes(h.LinkNotes(links, []store.Agent{a, b}, map[string]bool{"ag-b": true})); got != "cl-b:paused" {
		t.Fatalf("paused: %q", got)
	}
	// a peer that has reported no address and does not connect from a public one
	h2 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}})
	noAddr := ag("ag-b", "cl-b", "beta", "", 2)
	if got := codes(h2.LinkNotes(links, []store.Agent{a, noAddr}, nil)); got != "cl-a:peer-silent" {
		t.Fatalf("peer-silent: %q", got)
	}
	// every address of the peer is also this cluster's
	same := ag("ag-b", "cl-b", "beta", "203.0.113.1", 2)
	h3 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.0.0.1")}}})
	ns := h3.LinkNotes(links, []store.Agent{a, same}, nil)
	if len(ns) == 0 || ns[0].Code != "shared-address" || !strings.Contains(ns[0].Text, "10.0.0.1") {
		t.Fatalf("shared-address: %v", ns)
	}
	// the same cluster in two pairs is told once that it has no agent
	if got := codes(h.LinkNotes([]kernel.LinkSetting{{A: "cl-a", B: "cl-z"}, {A: "cl-b", B: "cl-z"}}, []store.Agent{a, b}, nil)); got != "cl-z:no-agent" {
		t.Fatalf("one note per cluster: %q", got)
	}
}

func TestAPairCanBePinnedToOneKindOfAddress(t *testing.T) {
	for _, via := range []string{"", "mesh", "private", "public"} {
		if _, err := kernel.NormalizeLinks([]kernel.LinkSetting{{A: "cl-a", B: "cl-b", Via: via}}); err != nil {
			t.Errorf("%q refused: %v", via, err)
		}
	}
	for _, via := range []string{"Mesh", "tailscale", "10.0.0.0/8", " public"} {
		_, err := kernel.NormalizeLinks([]kernel.LinkSetting{{A: "cl-a", B: "cl-b", Via: via}})
		if err == nil || !strings.Contains(err.Error(), "cl-a") || !strings.Contains(err.Error(), "mesh, private or public") {
			t.Errorf("%q: %v", via, err)
		}
	}
	// The choice belongs to the pair, so both clusters are told, and a pair without one is told nothing.
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})
	pinned := []kernel.LinkSetting{{A: "cl-a", B: "cl-b", Via: "public"}}
	for name, from := range map[string]store.Agent{"cl-a": a, "cl-b": b} {
		if got := h.linkPeers(from, []store.Agent{a, b}, pinned, false, nil); len(got) != 1 || got[0].Via != "public" {
			t.Errorf("%s was not told: %+v", name, got)
		}
	}
	if got := h.linkPeers(a, []store.Agent{a, b}, []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil); len(got) != 1 || got[0].Via != "" {
		t.Errorf("a pair that follows the traffic is pinned: %+v", got)
	}
}

func TestAPairPinnedToAKindThePeerHasNoneOfSaysSo(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}})
	// The private-range hint is about something else and is tested on its own.
	notes := func(l kernel.LinkSetting) []connectivity.LinkNote {
		var out []connectivity.LinkNote
		for _, n := range h.LinkNotes([]kernel.LinkSetting{l}, []store.Agent{a, b}, nil) {
			if n.Code != "private-range" {
				out = append(out, n)
			}
		}
		return out
	}
	if got := notes(kernel.LinkSetting{A: "cl-a", B: "cl-b", Via: "mesh"}); len(got) != 2 || got[0].Code != "no-address-of-kind" || got[0].Cluster != "cl-a" || got[0].Peer != "cl-b" || got[1].Cluster != "cl-b" ||
		!strings.Contains(got[0].Text, "mesh addresses, but cl-b has none") {
		t.Errorf("mesh: %+v", got)
	}
	// Both of the peers' kinds are there (a node address on a private range, the address its agent connects from).
	for _, via := range []string{"", "private", "public"} {
		if got := notes(kernel.LinkSetting{A: "cl-a", B: "cl-b", Via: via}); len(got) != 0 {
			t.Errorf("%q: %+v", via, got)
		}
	}
	// Nothing active runs for a pair with the probe off and no throughput tests, so there is nothing to explain.
	if got := notes(kernel.LinkSetting{A: "cl-a", B: "cl-b", Via: "mesh", NoProbe: true}); len(got) != 0 {
		t.Errorf("no probe: %+v", got)
	}
	// An address the peer shares with this cluster is not one it can be measured through.
	h2 := linkHub(map[string]*View{"ag-a": {EstView: EstView{State: nodeState("10.0.0.1")}}, "ag-b": {EstView: EstView{State: nodeState("10.0.0.1", "100.64.1.1")}}})
	// cl-b has a mesh address, so cl-a measures through it; cl-a has none left once the shared one is out, and cl-b is told so.
	got := h2.LinkNotes([]kernel.LinkSetting{{A: "cl-a", B: "cl-b", Via: "mesh"}}, []store.Agent{a, b}, nil)
	var kind []connectivity.LinkNote
	for _, n := range got {
		if n.Code == "no-address-of-kind" {
			kind = append(kind, n)
		}
	}
	if len(kind) != 1 || kind[0].Cluster != "cl-b" || kind[0].Peer != "cl-a" {
		t.Errorf("%+v", got)
	}
}

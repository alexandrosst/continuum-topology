package server

import (
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

	pa := h.linkPeers(a, all, links, false)
	if len(pa) != 1 || pa[0].ClusterId != "cl-b" || pa[0].Name != "beta" {
		t.Fatalf("alpha was told %v", pa)
	}
	// the peer's node addresses and the address its agent connects from, sorted
	if want := []string{"10.1.0.1", "198.51.100.2", "203.0.113.2"}; !reflect.DeepEqual(pa[0].Addresses, want) {
		t.Fatalf("addresses = %v, want %v", pa[0].Addresses, want)
	}
	pb := h.linkPeers(b, all, links, false)
	if len(pb) != 1 || pb[0].ClusterId != "cl-a" {
		t.Fatalf("beta was told %v", pb)
	}
	if pc := h.linkPeers(c, all, links, false); len(pc) != 0 {
		t.Fatalf("a cluster in no pair was told about peers: %v", pc)
	}
}

func TestNoPeersBelowTierTwoWhileFlowIsPausedOrForUnknownClusters(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}, "ag-b": {state: nodeState("10.1.0.1")}})
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-zzz"}}
	if got := h.linkPeers(a, []store.Agent{a, b}, links, false); len(got) != 1 {
		t.Fatalf("an unknown cluster should be skipped, got %v", got)
	}
	low := a
	low.AccessTier = 1
	if got := h.linkPeers(low, []store.Agent{low, b}, links, false); got != nil {
		t.Fatalf("tier 1 was given peers: %v", got)
	}
	if got := h.linkPeers(a, []store.Agent{a, b}, links, true); got != nil {
		t.Fatalf("a paused flow collector was given peers: %v", got)
	}
	pending := b
	pending.Status = store.StatusPending
	if got := h.linkPeers(a, []store.Agent{a, pending}, links, false); len(got) != 0 {
		t.Fatalf("an unapproved peer was shared: %v", got)
	}
	noAddr := ag("ag-b", "cl-b", "beta", "", 2)
	h2 := linkHub(map[string]*view{"ag-a": {state: nodeState("10.0.0.1")}})
	if got := h2.linkPeers(a, []store.Agent{a, noAddr}, links, false); len(got) != 0 {
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
	got := h.linkPeers(a, []store.Agent{a, b}, []LinkSetting{{A: "cl-a", B: "cl-b"}}, false)
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

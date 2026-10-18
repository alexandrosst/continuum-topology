package server

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/kernel"
	"continuum/internal/link"
	"continuum/internal/store"
)

// hubOfPeers is a hub with one runner (cl-a) and n peers cl-p000.., each with a node address of its own, the first `missing` of
// them without an approved agent.
func hubOfPeers(n, missing int) (*Hub, store.Agent, []store.Agent, []kernel.LinkSetting) {
	a := ag("ag-a", "cl-a", "alpha", "", 2)
	views := map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}}
	agents := []store.Agent{a}
	var links []kernel.LinkSetting
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("cl-p%03d", i)
		links = append(links, kernel.LinkSetting{A: "cl-a", B: id})
		o := ag("ag-"+id, id, id, "", 2)
		if i < missing {
			o.Status = store.StatusPending
		}
		views[o.ID] = &view{estView: estView{state: nodeState(fmt.Sprintf("10.%d.%d.1", 1+i/200, i%200))}}
		agents = append(agents, o)
	}
	return linkHub(views), a, agents, links
}

func peerIDs(ps []*continuumv1.LinkPeer) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ClusterId)
	}
	return out
}

// The cut at link.MaxPeers comes after the peers that cannot be told about are filtered out, not before: peers with no agent yet
// must not use up the room of those that have one.
func TestThePeerListIsCutAfterFilteringNotBefore(t *testing.T) {
	h, a, agents, links := hubOfPeers(80, 30)
	got := h.linkPeers(a, agents, links, false, nil)
	if len(got) != 50 {
		t.Fatalf("80 peers, 30 without an agent: %d sent, want the 50 usable ones", len(got))
	}
	if got[0].ClusterId != "cl-p030" || got[49].ClusterId != "cl-p079" {
		t.Fatalf("the id order is kept: %s .. %s", got[0].ClusterId, got[49].ClusterId)
	}

	// More usable peers than the list holds: the first link.MaxPeers by id are sent, the rest are said to be left out.
	h, a, agents, links = hubOfPeers(80, 10)
	got = h.linkPeers(a, agents, links, false, nil)
	if len(got) != link.MaxPeers || got[0].ClusterId != "cl-p010" || got[len(got)-1].ClusterId != "cl-p073" {
		t.Fatalf("%d sent, %v", len(got), peerIDs(got))
	}
	var cut []string
	for _, n := range h.linkNotes(links, agents, nil) {
		if n.Code == "too-many-peers" {
			if n.Cluster != "cl-a" {
				t.Errorf("note about %s", n.Cluster)
			}
			cut = append(cut, n.Peer)
		}
	}
	if want := []string{"cl-p074", "cl-p075", "cl-p076", "cl-p077", "cl-p078", "cl-p079"}; !reflect.DeepEqual(cut, want) {
		t.Fatalf("too-many-peers notes for %v, want %v", cut, want)
	}
	// A peer left out is not told to skip anything on its side: nobody measures towards it from here, so it probes itself.
	p79 := agents[80]
	h.views[p79.ID] = &view{estView: estView{state: nodeState("10.1.79.1")}}
	if sent := h.linkPeers(p79, agents, links, false, nil); len(sent) != 1 || sent[0].SkipProbe {
		t.Errorf("a cluster the runner cut skips its probe: %+v", sent)
	}

	// A hub within the limit is sent exactly the list it always was: every usable peer, in id order.
	h, a, agents, links = hubOfPeers(64, 0)
	if got := h.linkPeers(a, agents, links, false, nil); len(got) != 64 || got[0].ClusterId != "cl-p000" {
		t.Fatalf("%d sent", len(got))
	}
	for _, n := range h.linkNotes(links, agents, nil) {
		if n.Code == "too-many-peers" {
			t.Errorf("a full list of exactly %d peers has a cut note: %+v", link.MaxPeers, n)
		}
	}
}

// A mixed node: a flannel.1 device and a wireguard mesh device, plus the addresses the probe lists for the host.
func overlayNode() *continuumv1.NodeFacts {
	return &continuumv1.NodeFacts{
		InternalIps: []string{"192.168.1.10"},
		PodCidrs:    []string{"10.42.2.0/24"},
		Probe: &continuumv1.HostProbe{
			Tunnels: []*continuumv1.TunnelInterface{
				{Name: "flannel.1", Kind: "vxlan", Addresses: []string{"10.42.2.0/32"}},
				{Name: "wt0", Kind: "wireguard", Addresses: []string{"100.64.0.45/16"}},
				{Name: "tunl0", Kind: "ipip", Addresses: []string{"10.42.3.1/32"}},
			},
			HostAddresses: []*continuumv1.HostAddress{
				{Interface: "tailscale0", Kind: "tun", Address: "100.100.1.2/32"},
				{Interface: "eth1", Address: "172.16.0.9/24"},
				{Interface: "cni0", Kind: "bridge", Address: "10.42.2.1/24"},
				{Interface: "eth2", Address: "10.42.2.77/24"}, // a host address that lies in the pod range: not offered
			},
		},
	}
}

func TestOverlayAndPodRangeAddressesAreNeverOfferedButMeshAndHostOnesAre(t *testing.T) {
	st := &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": overlayNode()}, Cluster: &continuumv1.ClusterFacts{ServiceCidr: "10.43.0.0/16"}}
	got := LinkPeerAddresses(st)
	for _, bad := range []string{"10.42.2.0", "10.42.3.1", "10.42.2.1", "10.42.2.77"} {
		if hasAddr(got, bad) {
			t.Errorf("%s (a CNI device or a pod-range address) was offered: %v", bad, got)
		}
	}
	for _, good := range []string{"192.168.1.10", "100.64.0.45", "100.100.1.2", "172.16.0.9"} {
		if !hasAddr(got, good) {
			t.Errorf("%s was not offered: %v", good, got)
		}
	}
	// Probe-sourced addresses in the service range go too.
	st.Nodes["n1"].Probe.HostAddresses = append(st.Nodes["n1"].Probe.HostAddresses, &continuumv1.HostAddress{Interface: "eth3", Address: "10.43.0.1/32"})
	if got := LinkPeerAddresses(st); hasAddr(got, "10.43.0.1") {
		t.Errorf("a service-range address was offered: %v", got)
	}
	// The node's own reported addresses are never tested against the pod ranges: on a flat network the pod range holds them.
	flat := &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": {InternalIps: []string{"10.0.1.5"}, ExternalIps: []string{"10.0.1.6"}, PodCidrs: []string{"10.0.0.0/16"}}}}
	if got := LinkPeerAddresses(flat); !reflect.DeepEqual(dedupeSorted(got), []string{"10.0.1.5", "10.0.1.6"}) {
		t.Errorf("flat network: %v", got)
	}
	// An IPv4-mapped IPv6 spelling is the IPv4 address.
	mapped := &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": {InternalIps: []string{"::ffff:192.168.1.10", "192.168.1.10"}}}}
	if got := dedupeSorted(LinkPeerAddresses(mapped)); !reflect.DeepEqual(got, []string{"192.168.1.10"}) {
		t.Errorf("mapped: %v", got)
	}

	// End to end: the peer list carries them, and a cluster with a smaller flannel range than the peer's does not get the peer's
	// flannel.1 addresses either.
	a, b := ag("ag-a", "cl-a", "alpha", "", 2), ag("ag-b", "cl-b", "beta", "", 2)
	h := linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": overlayNode()}}}}})
	sent := h.linkPeers(a, []store.Agent{a, b}, []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
	if len(sent) != 1 || hasAddr(sent[0].Addresses, "10.42.2.0") || !hasAddr(sent[0].Addresses, "100.64.0.45") || !hasAddr(sent[0].Addresses, "100.100.1.2") {
		t.Fatalf("%+v", sent)
	}
}

// Both clusters see an address of the other that is its own too (symmetric): a mesh address one reports as a tunnel address
// and the other as a host address is the same address and is removed on both sides.
func TestAnAddressBothClustersHoldIsRemovedWhateverInterfaceReportsIt(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "", 2), ag("ag-b", "cl-b", "beta", "", 2)
	nodeWith := func(internal string, tunnel, host string) *facts.State {
		n := &continuumv1.NodeFacts{InternalIps: []string{internal}, Probe: &continuumv1.HostProbe{}}
		if tunnel != "" {
			n.Probe.Tunnels = []*continuumv1.TunnelInterface{{Name: "wt0", Kind: "wireguard", Addresses: []string{tunnel}}}
		}
		if host != "" {
			n.Probe.HostAddresses = []*continuumv1.HostAddress{{Interface: "tailscale0", Kind: "tun", Address: host}}
		}
		return &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": n}}
	}
	h := linkHub(map[string]*view{
		"ag-a": {estView: estView{state: nodeWith("10.0.0.1", "", "100.64.9.9/32")}},
		"ag-b": {estView: estView{state: nodeWith("10.1.0.1", "100.64.9.9/32", "")}},
	})
	sent := h.linkPeers(a, []store.Agent{a, b}, []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, false, nil)
	if len(sent) != 1 || hasAddr(sent[0].Addresses, "100.64.9.9") || !hasAddr(sent[0].Addresses, "10.1.0.1") {
		t.Fatalf("%+v", sent)
	}
}

// Two peers of one cluster claiming the same address: it is left out for both, with a note naming both, and the pair that is
// left with nothing is not measured, so the other side keeps probing.
func TestAnAddressTwoPeersShareIsRemovedAndSaid(t *testing.T) {
	a := ag("ag-a", "cl-a", "alpha", "", 2)
	b, c := ag("ag-b", "cl-b", "beta", "", 2), ag("ag-c", "cl-c", "gamma", "", 2)
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}}
	all := []store.Agent{a, b, c}

	// Every address of both is the same: neither is sent, nobody measures, and both are told to probe.
	h := linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: nodeState("192.168.1.5")}}, "ag-c": {estView: estView{state: nodeState("192.168.1.5")}}})
	if got := h.linkPeers(a, all, links, false, nil); len(got) != 0 {
		t.Fatalf("peers that share their only address were sent: %+v", got)
	}
	for _, from := range []store.Agent{b, c} {
		sent := h.linkPeers(from, all, links, false, nil)
		if len(sent) != 1 || sent[0].SkipProbe {
			t.Errorf("%s skips its probe towards a runner that measures nothing: %+v", from.ID, sent)
		}
	}
	notes := h.linkNotes(links, all, nil)
	var shared []LinkNote
	for _, n := range notes {
		if n.Code == "shared-address" && n.Cluster == "cl-a" {
			shared = append(shared, n)
		}
	}
	if len(shared) != 2 || shared[0].Peer != "cl-b" || shared[1].Peer != "cl-c" ||
		!strings.Contains(shared[0].Text, "cl-c") || !strings.Contains(shared[0].Text, "192.168.1.5") || !strings.Contains(shared[1].Text, "cl-b") {
		t.Fatalf("notes: %+v", notes)
	}

	// Only one address is shared (a NAT egress both sit behind): it goes from both, the rest stays, and the note is a hint.
	bn, cn := ag("ag-b", "cl-b", "beta", "203.0.113.7", 2), ag("ag-c", "cl-c", "gamma", "203.0.113.7", 2)
	h2 := linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: nodeState("10.1.0.1")}}, "ag-c": {estView: estView{state: nodeState("10.2.0.1")}}})
	got := h2.linkPeers(a, []store.Agent{a, bn, cn}, links, false, nil)
	if len(got) != 2 || !reflect.DeepEqual(got[0].Addresses, []string{"10.1.0.1"}) || !reflect.DeepEqual(got[1].Addresses, []string{"10.2.0.1"}) {
		t.Fatalf("%+v", got)
	}
	var hints int
	for _, n := range h2.linkNotes(links, []store.Agent{a, bn, cn}, nil) {
		if n.Code == "shared-address" && n.Cluster == "cl-a" && strings.Contains(n.Text, "203.0.113.7") {
			hints++
		}
	}
	if hints != 2 {
		t.Errorf("%d hints, want one for each peer", hints)
	}
	// A hint is not a blocker: the checklist's addresses step stays met.
	for _, s := range h2.linkSetups(links, []store.Agent{a, bn, cn}, nil, true, time.Now()) {
		for _, st := range s.Steps {
			if st.Code == "addresses" && st.State == "todo" {
				t.Errorf("a partly shared address failed the checklist: %+v", st)
			}
		}
	}
	// The same address spelled as IPv4-mapped IPv6 is the same address.
	bm := ag("ag-b", "cl-b", "beta", "::ffff:203.0.113.7", 2)
	if got := h2.linkPeers(a, []store.Agent{a, bm, cn}, links, false, nil); len(got) != 2 || hasAddr(got[0].Addresses, "203.0.113.7") || hasAddr(got[1].Addresses, "203.0.113.7") {
		t.Errorf("mapped spelling: %+v", got)
	}
}

// The agent's connecting address (a NAT's) is kept in the list for attribution but is not a node address: it must not make the
// private-range hint disappear for a NATed cluster.
func TestTheEgressAddressStaysListedButDoesNotSilenceThePrivateRangeHint(t *testing.T) {
	a := ag("ag-a", "cl-a", "alpha", "", 2)
	b := ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: nodeState("10.1.0.1")}}})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
	sent := h.linkPeers(a, []store.Agent{a, b}, links, false, nil)
	if len(sent) != 1 || !hasAddr(sent[0].Addresses, "203.0.113.2") {
		t.Fatalf("the egress address left the list: %+v", sent)
	}
	var hint *LinkNote
	for _, n := range h.linkNotes(links, []store.Agent{a, b}, nil) {
		if n.Code == "private-range" && n.Cluster == "cl-a" {
			n := n
			hint = &n
		}
	}
	if hint == nil || strings.Contains(hint.Text, "(10.1.0.1, 203.0.113.2)") || !strings.Contains(hint.Text, "203.0.113.2") || !strings.Contains(hint.Text, "not counted") {
		t.Fatalf("hint: %+v", hint)
	}
	// A node that really has the public address is a way across, whatever the agent connects from.
	h = linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: nodeState("10.1.0.1", "203.0.113.2")}}})
	for _, n := range h.linkNotes(links, []store.Agent{a, b}, nil) {
		if n.Code == "private-range" && n.Cluster == "cl-a" {
			t.Errorf("a node with a public address still gets the hint: %+v", n)
		}
	}
	// A CGNAT egress address counts as public to publicIP but is mesh to the pair: it is not a way across either.
	cg := ag("ag-b", "cl-b", "beta", "100.64.3.3", 2)
	h = linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: nodeState("10.1.0.1")}}})
	var found bool
	for _, n := range h.linkNotes(links, []store.Agent{a, cg}, nil) {
		found = found || n.Code == "private-range" && n.Cluster == "cl-a"
	}
	if !found {
		t.Error("a CGNAT egress address silenced the hint")
	}
}

func TestNotesNameAtMostSixAddresses(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "", 2), ag("ag-b", "cl-b", "beta", "", 2)
	n := &continuumv1.NodeFacts{}
	for i := 1; i <= 20; i++ {
		n.InternalIps = append(n.InternalIps, fmt.Sprintf("10.1.0.%d", i))
	}
	h := linkHub(map[string]*view{"ag-a": {estView: estView{state: nodeState("10.0.0.1")}}, "ag-b": {estView: estView{state: &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": n}}}}})
	var text string
	for _, x := range h.linkNotes([]kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, []store.Agent{a, b}, nil) {
		if x.Code == "private-range" && x.Cluster == "cl-a" {
			text = x.Text
		}
	}
	if strings.Count(text, "10.1.0.") != 6 || !strings.Contains(text, "+14 more") {
		t.Errorf("%q", text)
	}
	if got := listed([]string{"a", "b"}); got != "a, b" {
		t.Errorf("%q", got)
	}
}

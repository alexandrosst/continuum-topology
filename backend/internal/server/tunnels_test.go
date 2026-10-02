package server

import (
	"testing"

	"continuum/internal/model"
)

func nodeWithTunnel(id, name string, tuns ...model.TunnelInterface) model.Node {
	return model.Node{ID: id, Name: name, Tunnels: tuns}
}

func TestCorrelateTunnelsConfirmsAReciprocalPair(t *testing.T) {
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "node-b" {
		t.Errorf("node-a's tunnel Confirmed = %q, want node-b", nodes[0].Tunnels[0].Confirmed)
	}
	if nodes[1].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("node-b's tunnel Confirmed = %q, want node-a", nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsLeavesAOneSidedTunnelUnconfirmed(t *testing.T) {
	// Only one side is visible in this topology (the other end is outside every onboarded cluster, a
	// completely ordinary case) - it must stay unconfirmed, not be guessed into a link that was never
	// actually corroborated.
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a}
	correlateTunnels(nodes)
	if got := nodes[0].Tunnels[0].Confirmed; got != "" {
		t.Errorf("Confirmed = %q, want empty (nothing corroborates it)", got)
	}
}

func TestCorrelateTunnelsRequiresAgreementInBothDirections(t *testing.T) {
	// node-a's tunnel address falls inside node-b's routed prefix, but node-b's own tunnel address does
	// NOT fall inside anything node-a routes - a one-way match is not enough to call it confirmed.
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"192.168.99.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.9.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "" || nodes[1].Tunnels[0].Confirmed != "" {
		t.Errorf("got %q / %q, want both empty (agreement is one-directional only)", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsNeverMatchesTwoTunnelsOnTheSameNode(t *testing.T) {
	a := nodeWithTunnel("a", "node-a",
		model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.9.0.0/24"}},
		model.TunnelInterface{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.1/24"}, Routes: []string{"10.8.0.0/24"}},
	)
	nodes := []model.Node{a}
	correlateTunnels(nodes)
	for i, tu := range nodes[0].Tunnels {
		if tu.Confirmed != "" {
			t.Errorf("tunnel %d Confirmed = %q, want empty (a node cannot corroborate its own tunnel)", i, tu.Confirmed)
		}
	}
}

// TestCorrelateTunnelsRejectsCoincidentalOverlapOnABroadSharedSupernet pins a real fix: two unrelated
// tunnels that have never exchanged a packet, each independently routing the whole 10.0.0.0/8 (an
// entirely ordinary "route everything on this VPN" tunnel config, and a very commonly reused private
// range), used to confirm each other purely because their own /32-ish addresses happened to both fall
// inside that shared broad supernet - a false positive that directly contradicted this function's own
// "never fabricated" promise. A real point-to-point tunnel routes something specific; /8 is not that.
func TestCorrelateTunnelsRejectsCoincidentalOverlapOnABroadSharedSupernet(t *testing.T) {
	a := nodeWithTunnel("a", "customer-A-gateway", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.1.2.3/32"}, Routes: []string{"10.0.0.0/8"},
	})
	b := nodeWithTunnel("b", "customer-B-gateway", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.9.8.7/32"}, Routes: []string{"10.0.0.0/8"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "" || nodes[1].Tunnels[0].Confirmed != "" {
		t.Errorf("got %q / %q, want both empty - a shared /8 is not specific evidence of a real link", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

// TestCorrelateTunnelsStillConfirmsASpecificEnoughRoute makes sure the new specificity floor doesn't
// overcorrect: a realistic tunnel subnet (/24, well above the /16 floor) must still confirm normally.
func TestCorrelateTunnelsStillConfirmsASpecificEnoughRoute(t *testing.T) {
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "node-b" || nodes[1].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("a /24 route should still confirm normally: got %q / %q", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsFindsAMatchAcrossManyNodes(t *testing.T) {
	nodes := []model.Node{
		nodeWithTunnel("x", "noise-1", model.TunnelInterface{Name: "gre0", Kind: "gre", Addresses: []string{"192.0.2.1/30"}, Routes: []string{"198.51.100.0/24"}}),
		nodeWithTunnel("a", "node-a", model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}),
		nodeWithTunnel("y", "noise-2"),
		nodeWithTunnel("b", "node-b", model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}),
	}
	correlateTunnels(nodes)
	if nodes[1].Tunnels[0].Confirmed != "node-b" || nodes[3].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("node-a/node-b did not confirm each other: %q / %q", nodes[1].Tunnels[0].Confirmed, nodes[3].Tunnels[0].Confirmed)
	}
}
func nodeForClusterLink(clusterID, id, name string) model.Node {
	return model.Node{ClusterID: clusterID, ID: id, Name: name}
}

func TestCorrelateClusterLinksConfirmsAnOverlayLinkAcrossClusters(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1: %+v", len(got), got)
	}
	l := got[0]
	if l.Kind != "overlay" || l.FromCluster != "cluster-a" || l.ToCluster != "cluster-b" || l.FromName != "Cluster A" || l.ToName != "Cluster B" || l.Via != "wg0 (wireguard)" {
		t.Errorf("got %+v, want an overlay link cluster-a -> cluster-b via wg0 (wireguard)", l)
	}
	if l.Redundancy != 1 || l.FromNode != "node-a" || l.ToNode != "node-b" || l.FromAddress != "10.8.0.1/24" || l.ToAddress != "10.8.0.2/24" {
		t.Errorf("got %+v, want Redundancy=1 and the two corroborating nodes/addresses named", l)
	}
}

// TestCorrelateClusterLinksCountsRedundancyInsteadOfDroppingExtraCorroboration pins a real fix: a second,
// independent WireGuard peering between the same two clusters used to be matched, found, and then
// silently dropped on the floor by the old {from,to,kind} dedup - discarding exactly the "is there more
// than one path between these clusters" fact that matters for judging whether this is a single point of
// failure. It must now show up as the same one ClusterLink with Redundancy=2, not vanish and not double
// the slice.
func TestCorrelateClusterLinksCountsRedundancyInsteadOfDroppingExtraCorroboration(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{
		{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}},
		{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.1/24"}, Routes: []string{"10.9.0.0/24"}},
	}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{
		{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}},
		{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.2/24"}, Routes: []string{"10.9.0.0/24"}},
	}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d links, want exactly 1 (same pair, same kind - two paths, not two links): %+v", len(got), got)
	}
	if got[0].Redundancy != 2 {
		t.Errorf("got Redundancy=%d, want 2 (two independently corroborating tunnel pairs)", got[0].Redundancy)
	}
}

func TestCorrelateClusterLinksIgnoresATunnelMatchWithinOneCluster(t *testing.T) {
	// Two nodes OF THE SAME CLUSTER corroborating each other's tunnel says nothing about a relationship
	// BETWEEN two clusters - it must not show up as a (degenerate, same-cluster) ClusterLink.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	b := nodeForClusterLink("cluster-a", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	if got := correlateClusterLinks([]model.Node{a, b}, nil, nil, nil); got != nil {
		t.Errorf("got %+v, want nil - both ends are in the same cluster", got)
	}
}

func TestCorrelateClusterLinksConfirmsASubnetLinkAcrossClusters(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.HostSubnets = []string{"10.20.30.9/24"}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1: %+v", len(got), got)
	}
	l := got[0]
	if l.Kind != "subnet" || l.FromCluster != "cluster-a" || l.ToCluster != "cluster-b" || l.Via != "10.20.30.0/24" {
		t.Errorf("got %+v, want a subnet link cluster-a -> cluster-b via 10.20.30.0/24", l)
	}
}

func TestCorrelateClusterLinksRejectsATooBroadSharedSubnet(t *testing.T) {
	// Same reasoning as the tunnel specificity floor: two nodes that both merely happen to report a
	// prefix inside the same broad, commonly-reused private supernet is not evidence of anything.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.HostSubnets = []string{"10.1.2.3/8"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.HostSubnets = []string{"10.9.8.7/8"}
	if got := correlateClusterLinks([]model.Node{a, b}, nil, nil, nil); got != nil {
		t.Errorf("got %+v, want nil - a shared /8 is not specific evidence", got)
	}
}

func TestCorrelateClusterLinksDifferentNetworksDoNotMatch(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.HostSubnets = []string{"10.20.31.5/24"} // a genuinely different /24 - must not match
	if got := correlateClusterLinks([]model.Node{a, b}, nil, nil, nil); got != nil {
		t.Errorf("got %+v, want nil - these are different networks", got)
	}
}

func TestCorrelateClusterLinksDedupsAcrossSeveralCorroboratingNodePairs(t *testing.T) {
	// Two nodes per cluster, all four sharing the one subnet: still exactly one ClusterLink for the pair,
	// not one per corroborating node pair.
	a1 := nodeForClusterLink("cluster-a", "a1", "node-a1")
	a1.HostSubnets = []string{"10.20.30.5/24"}
	a2 := nodeForClusterLink("cluster-a", "a2", "node-a2")
	a2.HostSubnets = []string{"10.20.30.6/24"}
	b1 := nodeForClusterLink("cluster-b", "b1", "node-b1")
	b1.HostSubnets = []string{"10.20.30.7/24"}
	b2 := nodeForClusterLink("cluster-b", "b2", "node-b2")
	b2.HostSubnets = []string{"10.20.30.8/24"}
	got := correlateClusterLinks([]model.Node{a1, a2, b1, b2}, nil, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d links, want exactly 1 deduped link: %+v", len(got), got)
	}
}

func TestCorrelateClusterLinksOrdersFromToByClusterIDRegardlessOfScanOrder(t *testing.T) {
	a := nodeForClusterLink("z-cluster", "a", "node-a")
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("a-cluster", "b", "node-b")
	b.HostSubnets = []string{"10.20.30.9/24"}
	// b ("a-cluster") is scanned second, but its cluster ID sorts first.
	got := correlateClusterLinks([]model.Node{a, b}, nil, nil, nil)
	if len(got) != 1 || got[0].FromCluster != "a-cluster" || got[0].ToCluster != "z-cluster" {
		t.Errorf("got %+v, want from=a-cluster to=z-cluster regardless of scan order", got)
	}
}

func TestCorrelateClusterLinksBothKindsCanCoexistForTheSamePair(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	b.HostSubnets = []string{"10.20.30.9/24"}
	got := correlateClusterLinks([]model.Node{a, b}, nil, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d links, want 2 (overlay and subnet are independent facts): %+v", len(got), got)
	}
}

// TestCorrelateClusterLinksRollsUpFlowsCrossingAConfirmedOverlayLink pins the network-health rollup: a
// dependency qualifies only when its calling service sits in one of the link's two clusters AND its own
// Iface matches the confirmed tunnel interface name correlated on that exact side - cluster and interface
// together, not interface name alone, since a generic name like "wg0" is routinely reused across entirely
// unrelated tunnels elsewhere in the topology.
func TestCorrelateClusterLinksRollsUpFlowsCrossingAConfirmedOverlayLink(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}

	loss1, loss2 := 1.0, 3.0
	deps := []model.Dependency{
		// Calls from cluster-a, over wg0: qualifies.
		{From: "svc-a1", FromKind: "service", Iface: "wg0", RttMs: 10, Stats: &model.DependencyStats{LossPct: &loss1}},
		// Calls from cluster-b, over wg0: also qualifies (the other side of the same link).
		{From: "svc-b1", FromKind: "service", Iface: "wg0", RttMs: 20, Stats: &model.DependencyStats{LossPct: &loss2}},
		// Same interface name, but the calling service is in neither of this link's clusters (a totally
		// unrelated tunnel elsewhere that happens to also be named wg0) - must not be counted.
		{From: "svc-c1", FromKind: "service", Iface: "wg0", RttMs: 999},
		// Right cluster, wrong interface name - must not be counted.
		{From: "svc-a2", FromKind: "service", Iface: "eth0", RttMs: 5},
		// Right cluster and interface, but not a service caller (a device) - Iface still means something
		// physical here, but this rollup is specifically about service-originated calls.
		{From: "dev-a1", FromKind: "device", Iface: "wg0", RttMs: 1},
	}
	serviceClusterID := map[string]string{"svc-a1": "cluster-a", "svc-a2": "cluster-a", "svc-b1": "cluster-b", "svc-c1": "cluster-z"}

	got := correlateClusterLinks([]model.Node{a, b}, names, deps, serviceClusterID)
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1: %+v", len(got), got)
	}
	l := got[0]
	if l.FlowsObserved != 2 {
		t.Errorf("FlowsObserved = %d, want 2 (only the two calls actually on this link's confirmed interface, from either side)", l.FlowsObserved)
	}
	if l.AvgRttMs != 15 {
		t.Errorf("AvgRttMs = %v, want 15 (average of 10 and 20)", l.AvgRttMs)
	}
	if l.AvgLossPct == nil || *l.AvgLossPct != 2 {
		t.Errorf("AvgLossPct = %v, want *2 (average of 1 and 3)", l.AvgLossPct)
	}
}

func TestCorrelateClusterLinksRollupLeavesRttAndLossUnsetWithoutAnyMeasuredSample(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}

	// A conntrack-only call: it still counts as a flow using the tunnel, but RttMs=0 and no Stats means
	// nothing to average - same "0/nil means not measured" convention Dependency itself uses, not a
	// fabricated "0ms, 0% loss".
	deps := []model.Dependency{{From: "svc-a1", FromKind: "service", Iface: "wg0", Via: "conntrack"}}
	serviceClusterID := map[string]string{"svc-a1": "cluster-a"}

	got := correlateClusterLinks([]model.Node{a, b}, names, deps, serviceClusterID)
	l := got[0]
	if l.FlowsObserved != 1 {
		t.Errorf("FlowsObserved = %d, want 1", l.FlowsObserved)
	}
	if l.AvgRttMs != 0 {
		t.Errorf("AvgRttMs = %v, want 0 (no measured sample, not a fabricated figure)", l.AvgRttMs)
	}
	if l.AvgLossPct != nil {
		t.Errorf("AvgLossPct = %v, want nil (no measured sample)", *l.AvgLossPct)
	}
}

func TestCorrelateClusterLinksRollupNeverAppliesToASubnetLink(t *testing.T) {
	// A "subnet" link has no specific interface to correlate against - the rollup fields must stay at
	// their zero values regardless of what the dependency list contains.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.HostSubnets = []string{"10.20.30.9/24"}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	deps := []model.Dependency{{From: "svc-a1", FromKind: "service", Iface: "eth0", RttMs: 10}}
	serviceClusterID := map[string]string{"svc-a1": "cluster-a"}

	got := correlateClusterLinks([]model.Node{a, b}, names, deps, serviceClusterID)
	if len(got) != 1 || got[0].Kind != "subnet" {
		t.Fatalf("got %+v, want exactly one subnet link", got)
	}
	if got[0].FlowsObserved != 0 || got[0].AvgRttMs != 0 || got[0].AvgLossPct != nil {
		t.Errorf("got %+v, want every rollup field left at its zero value for a subnet link", got[0])
	}
}

func TestCorrelateClusterLinksRollupCountsFlowsOnAnyRedundantInterface(t *testing.T) {
	// Two independent WireGuard peerings between the same two clusters (Redundancy=2, different interface
	// names on each path) - a flow on EITHER confirmed interface counts, not just the first one found.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{
		{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}},
		{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.1/24"}, Routes: []string{"10.9.0.0/24"}},
	}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{
		{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}},
		{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.2/24"}, Routes: []string{"10.9.0.0/24"}},
	}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	deps := []model.Dependency{
		{From: "svc-a1", FromKind: "service", Iface: "wg0", RttMs: 10},
		{From: "svc-a2", FromKind: "service", Iface: "wg1", RttMs: 30},
	}
	serviceClusterID := map[string]string{"svc-a1": "cluster-a", "svc-a2": "cluster-a"}

	got := correlateClusterLinks([]model.Node{a, b}, names, deps, serviceClusterID)
	if len(got) != 1 || got[0].Redundancy != 2 {
		t.Fatalf("got %+v, want one link with Redundancy=2", got)
	}
	if got[0].FlowsObserved != 2 {
		t.Errorf("FlowsObserved = %d, want 2 (one flow per redundant path, both confirmed interfaces)", got[0].FlowsObserved)
	}
	if got[0].AvgRttMs != 20 {
		t.Errorf("AvgRttMs = %v, want 20 (average of 10 and 30 across both paths)", got[0].AvgRttMs)
	}
}

func TestCorrelateClusterLinksClassifiesAWireGuardOverlayAsEncrypted(t *testing.T) {
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 || got[0].Encryption != "encrypted" {
		t.Fatalf("got %+v, want exactly one link with Encryption=\"encrypted\" (WireGuard encrypts by design)", got)
	}
}

func TestCorrelateClusterLinksClassifiesAGREOverlayAsPlaintext(t *testing.T) {
	// GRE has no cryptography of its own - unlike WireGuard, it must not be reported as "encrypted" just
	// because it is a confirmed overlay tunnel.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.Tunnels = []model.TunnelInterface{{Name: "gre0", Kind: "gre", Addresses: []string{"192.0.2.1/30"}, Routes: []string{"192.0.2.0/24"}}}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.Tunnels = []model.TunnelInterface{{Name: "gre0", Kind: "gre", Addresses: []string{"192.0.2.2/30"}, Routes: []string{"192.0.2.0/24"}}}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 || got[0].Encryption != "plaintext" {
		t.Fatalf("got %+v, want exactly one link with Encryption=\"plaintext\" (GRE carries no cryptography of its own)", got)
	}
}

func TestCorrelateClusterLinksNeverClassifiesASubnetLink(t *testing.T) {
	// A "subnet" link has no tunnel driver at all to classify - Encryption must stay empty, never a
	// fabricated "plaintext" just because there is no tunnel encrypting it either.
	a := nodeForClusterLink("cluster-a", "a", "node-a")
	a.HostSubnets = []string{"10.20.30.5/24"}
	b := nodeForClusterLink("cluster-b", "b", "node-b")
	b.HostSubnets = []string{"10.20.30.9/24"}
	names := map[string]string{"cluster-a": "Cluster A", "cluster-b": "Cluster B"}
	got := correlateClusterLinks([]model.Node{a, b}, names, nil, nil)
	if len(got) != 1 || got[0].Encryption != "" {
		t.Fatalf("got %+v, want Encryption empty for a subnet link", got)
	}
}

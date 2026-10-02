package server

import (
	"net"

	"continuum/internal/model"
)

// minRoutePrefixV4/minRoutePrefixV6 are the narrowest-allowed (i.e. the SMALLEST acceptable prefix
// length - "narrowest" in the networking sense of "most specific") route a tunnel may offer as evidence
// for correlateTunnels below. Without this floor, two completely unrelated tunnels that each happen to
// route a whole shared private supernet (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 - an entirely ordinary
// way to configure a "route everything on this VPN" tunnel) would corroborate each other purely by
// coincidental overlap in commonly-reused address space, never having exchanged a single packet. A real
// point-to-point overlay link - a WireGuard peer subnet, a site-to-site GRE/IPsec tunnel - routes a
// specific subnet, almost always /24 or tighter; /16 leaves comfortable room above that while still
// excluding the broad "route everything" case. IPv6 has no equivalent reuse problem (its address space is
// not scarce the way RFC 1918 ranges are, so two unrelated tunnels coincidentally sharing a broad IPv6
// prefix is far less likely), but /48 (a site-sized allocation) is still kept as a sanity floor.
const (
	minRoutePrefixV4 = 16
	minRoutePrefixV6 = 48
)

// specificEnough reports whether route is a specific-enough destination to count as correlation evidence
// (see minRoutePrefixV4/V6's own comment for why this floor exists at all).
func specificEnough(route *net.IPNet) bool {
	ones, bits := route.Mask.Size()
	if bits == 32 { // IPv4
		return ones >= minRoutePrefixV4
	}
	return ones >= minRoutePrefixV6 // IPv6 (bits == 128)
}

// correlateTunnels looks, across every node in the whole topology (any cluster, not just one), for a
// pair of tunnel interfaces that corroborate each other: each one's own address falls inside a
// sufficiently specific (see specificEnough) routed prefix the OTHER one reports sending traffic
// through. That two-way agreement is what makes this an honest "confirmed" label rather than a guess
// from one side alone - a lone tunnel whose routed prefix matches nothing anyone else reports stays
// unconfirmed, because it may genuinely lead somewhere outside every onboarded cluster (a home gateway,
// a SaaS VPN concentrator), which is a completely ordinary case, not a failure of this method, and never
// fabricated into a link that was not actually corroborated.
//
// It mutates each matched TunnelInterface's Confirmed field in place (to the other end's node name) and
// returns nothing; it is pure data matching over already-built model.Node values, with no I/O of its
// own, which is what makes it straightforward to test directly (see tunnels_test.go) without a real
// cluster or tunnel. Called once, after every cluster's nodes have been merged into one list (see
// Hub.stateFor) - a single node's own tunnels can never corroborate each other.
func correlateTunnels(nodes []model.Node) {
	type ref struct {
		nodeIdx, tunIdx int
		addrs           []net.IP     // this tunnel's own addresses
		routes          []*net.IPNet // sufficiently specific prefixes this tunnel sends traffic through
	}
	var refs []ref
	for ni := range nodes {
		for ti := range nodes[ni].Tunnels {
			t := nodes[ni].Tunnels[ti]
			r := ref{nodeIdx: ni, tunIdx: ti}
			for _, a := range t.Addresses {
				if ip, _, err := net.ParseCIDR(a); err == nil {
					r.addrs = append(r.addrs, ip)
				}
			}
			for _, cidr := range t.Routes {
				if _, n, err := net.ParseCIDR(cidr); err == nil && specificEnough(n) {
					r.routes = append(r.routes, n)
				}
			}
			if len(r.addrs) > 0 && len(r.routes) > 0 {
				refs = append(refs, r)
			}
		}
	}
	reaches := func(addrs []net.IP, routes []*net.IPNet) bool {
		for _, a := range addrs {
			for _, rt := range routes {
				if rt.Contains(a) {
					return true
				}
			}
		}
		return false
	}
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			a, b := refs[i], refs[j]
			if a.nodeIdx == b.nodeIdx {
				continue // the two ends of a link are never the same machine
			}
			if reaches(a.addrs, b.routes) && reaches(b.addrs, a.routes) {
				nodes[a.nodeIdx].Tunnels[a.tunIdx].Confirmed = nodes[b.nodeIdx].Name
				nodes[b.nodeIdx].Tunnels[b.tunIdx].Confirmed = nodes[a.nodeIdx].Name
			}
		}
	}
}

// correlateClusterLinks looks across every node in the topology for two kinds of confirmed, cluster-pair
// network relationship - distinct from, and a coarser-grained sibling of, correlateTunnels above:
//
//   - "overlay": the exact same two-way, specificity-floored tunnel-reaches-address match
//     correlateTunnels performs (an address one tunnel owns falls inside a sufficiently specific prefix
//     the OTHER tunnel routes traffic through, and vice versa), kept here only when the two ends belong
//     to different clusters. The matching is deliberately recomputed independently rather than reusing
//     correlateTunnels' side effect on TunnelInterface.Confirmed (which records node names, not cluster
//     IDs, and is a separate, per-node-pair fact) - this keeps correlateClusterLinks pure and testable on
//     its own, the same way correlateTunnels is.
//   - "subnet": two nodes in different clusters report a HostSubnets prefix that is, exactly, the same
//     network (equal network address and mask) at or above the same specificity floor tunnel routes are
//     held to. Because a node's own reported subnet always contains the node's own address by
//     construction, "the same network" is the honest way to say this, not a dressed-up reciprocal
//     check: it is real, kernel-reported evidence that each side's own default-route uplink places it on
//     the identical network block, with no tunnel involved in reaching it at all. Being honest about its
//     one real limitation matters as much as the check itself: a specific-enough shared network identity
//     is still, in principle, something two genuinely separate private networks could be assigned by
//     coincidence (two independent sites both handed 10.20.30.0/24 by their own infrastructure, with no
//     route between them) - nothing a passive probe reads can fully rule that out without actively
//     probing across clusters, which this package deliberately never does. The specificity floor keeps
//     this to the same low-coincidence bar as a tunnel route, not a guarantee beyond it.
//
// Pure data matching over already-built model.Node values, with no I/O of its own (see tunnels_test.go
// for the matching style this follows). names maps a cluster ID to its display name. Returns one
// ClusterLink per distinct (cluster pair, kind) - a pair corroborated by several node pairs, or by both a
// tunnel and a shared subnet, is never duplicated, and from/to are ordered by cluster ID so the same pair
// is never recorded twice under swapped ends depending on scan order.
func correlateClusterLinks(nodes []model.Node, names map[string]string, dependencies []model.Dependency, serviceClusterID map[string]string) []model.ClusterLink {
	type tunRef struct {
		nodeIdx, tunIdx int
		addrs           []net.IP
		routes          []*net.IPNet
	}
	var tunRefs []tunRef
	for ni := range nodes {
		for ti := range nodes[ni].Tunnels {
			t := nodes[ni].Tunnels[ti]
			r := tunRef{nodeIdx: ni, tunIdx: ti}
			for _, a := range t.Addresses {
				if ip, _, err := net.ParseCIDR(a); err == nil {
					r.addrs = append(r.addrs, ip)
				}
			}
			for _, cidr := range t.Routes {
				if _, n, err := net.ParseCIDR(cidr); err == nil && specificEnough(n) {
					r.routes = append(r.routes, n)
				}
			}
			if len(r.addrs) > 0 && len(r.routes) > 0 {
				tunRefs = append(tunRefs, r)
			}
		}
	}
	reaches := func(addrs []net.IP, routes []*net.IPNet) bool {
		for _, a := range addrs {
			for _, rt := range routes {
				if rt.Contains(a) {
					return true
				}
			}
		}
		return false
	}

	// add records one piece of corroborating evidence for a cluster pair+kind. The first time a given
	// (from, to, kind) is seen, it becomes a new ClusterLink (with the node names/addresses that
	// corroborated it, so the evidence points at real machines, not just a cluster pair and a driver
	// name); every later corroborating pair for the exact same (from, to, kind) no longer creates a
	// second, duplicate entry - the old behavior - but also no longer vanishes without a trace: it bumps
	// that entry's Redundancy instead, which is itself a fact worth keeping (one path between two
	// clusters vs. several is the difference between a single point of failure and not).
	type linkKey struct{ from, to, kind string }
	seen := map[linkKey]int{} // value is the index into out
	// Every confirmed tunnel interface name seen on each side of a link, across every corroborating node
	// pair (not just the first - unlike FromNode/ToNode, which only name the first), keyed the same way
	// `seen` is. Only ever populated for "overlay" (add's ifaceA/ifaceB are always "" for "subnet").
	fromIfaces := map[linkKey]map[string]bool{}
	toIfaces := map[linkKey]map[string]bool{}
	var out []model.ClusterLink
	add := func(clusterA, clusterB, kind, via, nodeA, nodeB, addrA, addrB, ifaceA, ifaceB string) {
		if clusterA == "" || clusterB == "" || clusterA == clusterB {
			return
		}
		from, to := clusterA, clusterB
		fromNode, toNode := nodeA, nodeB
		fromAddr, toAddr := addrA, addrB
		fromIface, toIface := ifaceA, ifaceB
		if from > to {
			from, to = to, from
			fromNode, toNode = toNode, fromNode
			fromAddr, toAddr = toAddr, fromAddr
			fromIface, toIface = toIface, fromIface
		}
		k := linkKey{from, to, kind}
		if fromIface != "" {
			if fromIfaces[k] == nil {
				fromIfaces[k] = map[string]bool{}
			}
			fromIfaces[k][fromIface] = true
		}
		if toIface != "" {
			if toIfaces[k] == nil {
				toIfaces[k] = map[string]bool{}
			}
			toIfaces[k][toIface] = true
		}
		if idx, ok := seen[k]; ok {
			out[idx].Redundancy++
			return
		}
		seen[k] = len(out)
		out = append(out, model.ClusterLink{
			FromCluster: from, FromName: names[from], ToCluster: to, ToName: names[to],
			Kind: kind, Via: via, Redundancy: 1,
			FromNode: fromNode, ToNode: toNode, FromAddress: fromAddr, ToAddress: toAddr,
		})
	}

	for i := range tunRefs {
		for j := i + 1; j < len(tunRefs); j++ {
			a, b := tunRefs[i], tunRefs[j]
			if a.nodeIdx == b.nodeIdx {
				continue // the two ends of a link are never the same machine
			}
			ca, cb := nodes[a.nodeIdx].ClusterID, nodes[b.nodeIdx].ClusterID
			if ca == cb {
				continue // corroborating tunnels both inside one cluster say nothing about a cluster pair
			}
			if reaches(a.addrs, b.routes) && reaches(b.addrs, a.routes) {
				ta := nodes[a.nodeIdx].Tunnels[a.tunIdx]
				tb := nodes[b.nodeIdx].Tunnels[b.tunIdx]
				addrA, addrB := "", ""
				if len(ta.Addresses) > 0 {
					addrA = ta.Addresses[0]
				}
				if len(tb.Addresses) > 0 {
					addrB = tb.Addresses[0]
				}
				add(ca, cb, "overlay", ta.Name+" ("+ta.Kind+")", nodes[a.nodeIdx].Name, nodes[b.nodeIdx].Name, addrA, addrB, ta.Name, tb.Name)
			}
		}
	}

	type subnetRef struct {
		nodeIdx int
		network *net.IPNet
	}
	var subnetRefs []subnetRef
	for ni := range nodes {
		for _, s := range nodes[ni].HostSubnets {
			if _, n, err := net.ParseCIDR(s); err == nil && specificEnough(n) {
				subnetRefs = append(subnetRefs, subnetRef{nodeIdx: ni, network: n})
			}
		}
	}
	sameNetwork := func(a, b *net.IPNet) bool {
		return a.IP.Equal(b.IP) && a.Mask.String() == b.Mask.String()
	}
	for i := range subnetRefs {
		for j := i + 1; j < len(subnetRefs); j++ {
			a, b := subnetRefs[i], subnetRefs[j]
			if a.nodeIdx == b.nodeIdx {
				continue
			}
			ca, cb := nodes[a.nodeIdx].ClusterID, nodes[b.nodeIdx].ClusterID
			if ca == cb {
				continue // every node in a cluster typically shares its site's subnet - not a cross-cluster fact
			}
			if sameNetwork(a.network, b.network) {
				add(ca, cb, "subnet", a.network.String(), nodes[a.nodeIdx].Name, nodes[b.nodeIdx].Name, "", "", "", "")
			}
		}
	}
	// Roll up the live dependency flows that actually cross each confirmed overlay link, now that every
	// link and its full set of corroborating interface names (both sides, across every redundant path)
	// is known. A dependency qualifies when its calling service sits in one of the link's two clusters
	// and its own Iface is one of the confirmed tunnel names correlated on that exact side - matching by
	// cluster AND interface name together, not interface name alone, since a generic name like "wg0" is
	// commonly reused across entirely unrelated tunnels on other node pairs.
	for i := range out {
		if out[i].Kind != "overlay" {
			continue
		}
		k := linkKey{out[i].FromCluster, out[i].ToCluster, out[i].Kind}
		fIfaces, tIfaces := fromIfaces[k], toIfaces[k]
		var flows int
		var rttSum float64
		var rttCount int
		var lossSum float64
		var lossCount int
		for _, d := range dependencies {
			if d.FromKind != "service" || d.Iface == "" {
				continue
			}
			sc := serviceClusterID[d.From]
			var ifaces map[string]bool
			switch sc {
			case out[i].FromCluster:
				ifaces = fIfaces
			case out[i].ToCluster:
				ifaces = tIfaces
			default:
				continue
			}
			if !ifaces[d.Iface] {
				continue
			}
			flows++
			if d.RttMs > 0 {
				rttSum += d.RttMs
				rttCount++
			}
			if d.Stats != nil && d.Stats.LossPct != nil {
				lossSum += *d.Stats.LossPct
				lossCount++
			}
		}
		out[i].FlowsObserved = flows
		if rttCount > 0 {
			out[i].AvgRttMs = rttSum / float64(rttCount)
		}
		if lossCount > 0 {
			avg := lossSum / float64(lossCount)
			out[i].AvgLossPct = &avg
		}
	}
	return out
}

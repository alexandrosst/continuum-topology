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

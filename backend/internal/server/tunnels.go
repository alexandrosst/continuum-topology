package server

import (
	"net"

	"continuum/internal/model"
)

// correlateTunnels looks, across every node in the whole topology (any cluster, not just one), for a
// pair of tunnel interfaces that corroborate each other: each one's own address falls inside the routed
// prefix the OTHER one reports sending traffic through. That two-way agreement is what makes this an
// honest "confirmed" label rather than a guess from one side alone - a lone tunnel whose routed prefix
// matches nothing anyone else reports stays unconfirmed, because it may genuinely lead somewhere outside
// every onboarded cluster (a home gateway, a SaaS VPN concentrator), which is a completely ordinary case,
// not a failure of this method, and never fabricated into a link that was not actually corroborated.
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
		routes          []*net.IPNet // prefixes this tunnel sends traffic through
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
				if _, n, err := net.ParseCIDR(cidr); err == nil {
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

package probe

import (
	"sort"
	"strconv"

	"github.com/jsimonetti/rtnetlink/v2"

	continuumv1 "continuum/gen/continuumv1"
)

// overlayKinds is every kernel link driver name (netlink's IFLA_INFO_KIND, the same field `ip -d link
// show` prints after an interface's name) this probe recognizes as an overlay/tunnel, rather than an
// ordinary physical or virtual-but-mundane link (veth, bridge, bond, VLAN - none of those carry traffic
// to somewhere else the way a tunnel does, so they are deliberately left out even though they are also
// virtual). Picking the kind from this fixed, kernel-defined vocabulary - instead of guessing from an
// interface's name - is the whole point: a renamed WireGuard interface is still caught, and an interface
// that merely happens to be named "wg0" without actually being one is not.
//
// Not covered, on purpose (see TunnelInterface's own proto doc for the full reasoning): plain tun/tap
// devices, which the kernel does not register under a named link kind at all, and policy-based IPsec,
// which attaches XFRM policies directly to an ordinary interface instead of creating one of its own.
var overlayKinds = map[string]bool{
	"wireguard": true,
	"vxlan":     true,
	"geneve":    true,
	"gre":       true,
	"gretap":    true,
	"ip6gre":    true,
	"ip6gretap": true,
	"ipip":      true,
	"sit":       true, // 6in4 / 6to4 - the same tunnel family as ipip, one direction reversed
	"vti":       true, // route-based IPsec (an XFRM policy bound to a dedicated link)
	"vti6":      true,
	"xfrm":      true,
}

// maxTunnelRoutes bounds how many destination prefixes are kept per tunnel - the same small cap, and the
// same reasoning (a bounded, deduplicated set beats an ever-growing one), as Flow.dns_query_names.
const maxTunnelRoutes = 8

// networkEvidence reads one netlink link/address/route dump and shapes it into both of this probe's
// network-topology facts: the overlay/tunnel interfaces the node has up (see the TunnelInterface proto
// message doc for exactly what this is for and the two kinds of tunnel it cannot see) and this node's
// own routable subnet prefix(es) (see HostProbe.host_subnets' own doc). Both are derived from the same
// three dumps, so they are gathered together in one netlink session rather than two. Reading link/
// address/route information over netlink needs no privilege beyond what this probe already has: an
// ordinary unprivileged process can open an AF_NETLINK/NETLINK_ROUTE socket and dump this information,
// the same as the `ip` command does as a normal user (confirmed empirically, not just by documentation,
// while building this).
//
// A netlink dial or dump failure (a kernel built without CONFIG_NET, a deeply sandboxed environment with
// no network namespace at all) is treated as "nothing to report", the same as this package's sysfs
// readers already treat a missing file - never a fatal error for the rest of the probe. The three dumps
// (links, addresses, routes) are kept as thin, untestable I/O here; buildTunnels and buildHostSubnets
// below, which do the actual matching and shaping, take plain data and are what tunnels_test.go exercises.
func networkEvidence() ([]*continuumv1.TunnelInterface, []string) {
	conn, err := rtnetlink.Dial(nil)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()

	links, err := conn.Link.List()
	if err != nil {
		return nil, nil
	}
	var addrs []rtnetlink.AddressMessage
	if a, err := conn.Address.List(); err == nil {
		addrs = a
	}
	var routes []rtnetlink.RouteMessage
	if r, err := conn.Route.List(); err == nil {
		routes = r
	}
	return buildTunnels(links, addrs, routes), buildHostSubnets(addrs, routes)
}

// buildTunnels matches a netlink link/address/route dump into one TunnelInterface per link whose kind is
// in overlayKinds - pure data shaping, with no netlink I/O of its own, so it can be exercised directly
// with hand-built messages (see tunnels_test.go) without needing a real tunnel interface or root.
func buildTunnels(links []rtnetlink.LinkMessage, addrs []rtnetlink.AddressMessage, routes []rtnetlink.RouteMessage) []*continuumv1.TunnelInterface {
	// One entry per matched interface index, so addresses and routes can be attached to the right
	// interface in the two passes below.
	byIndex := map[uint32]*continuumv1.TunnelInterface{}
	for _, l := range links {
		if l.Attributes == nil || l.Attributes.Info == nil || !overlayKinds[l.Attributes.Info.Kind] {
			continue
		}
		byIndex[l.Index] = &continuumv1.TunnelInterface{Name: Clean(l.Attributes.Name), Kind: l.Attributes.Info.Kind}
	}
	if len(byIndex) == 0 {
		return nil
	}

	for _, a := range addrs {
		t, ok := byIndex[a.Index]
		if !ok || a.Attributes == nil {
			continue
		}
		ip := a.Attributes.Address
		if ip == nil {
			ip = a.Attributes.Local
		}
		if ip == nil {
			continue
		}
		t.Addresses = append(t.Addresses, ip.String()+"/"+strconv.Itoa(int(a.PrefixLength)))
	}

	for _, r := range routes {
		if r.Attributes.Dst == nil || r.DstLength == 0 {
			continue // no destination, or a default route (0.0.0.0/0, ::/0) - never informative here
		}
		t, ok := byIndex[r.Attributes.OutIface]
		if !ok || len(t.Routes) >= maxTunnelRoutes {
			continue
		}
		t.Routes = append(t.Routes, r.Attributes.Dst.String()+"/"+strconv.Itoa(int(r.DstLength)))
	}

	out := make([]*continuumv1.TunnelInterface, 0, len(byIndex))
	for _, t := range byIndex {
		sort.Strings(t.Addresses)
		sort.Strings(t.Routes)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildHostSubnets returns this node's own routable network prefix(es): the address(es) configured on
// whichever interface currently owns the machine's default route (DstLength == 0, checked for IPv4 and
// IPv6 alike) - the same kernel-chosen "real uplink" `ip route get 8.8.8.8` would show, never guessed by
// interface name or by scanning every address on the box regardless of whether it is actually reachable
// from outside. Loopback and link-local addresses are never included: a default route never legitimately
// points at either, so seeing one here would mean something is already wrong, not a subnet worth
// reporting. Pure data shaping like buildTunnels above, with no netlink I/O of its own, so it is directly
// testable with hand-built messages (see tunnels_test.go).
func buildHostSubnets(addrs []rtnetlink.AddressMessage, routes []rtnetlink.RouteMessage) []string {
	defaultRouteIfaces := map[uint32]bool{}
	for _, r := range routes {
		if r.DstLength != 0 {
			continue // has a destination prefix - not the default route
		}
		defaultRouteIfaces[r.Attributes.OutIface] = true
	}
	if len(defaultRouteIfaces) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		if a.Attributes == nil || !defaultRouteIfaces[a.Index] {
			continue
		}
		ip := a.Attributes.Address
		if ip == nil {
			ip = a.Attributes.Local
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		prefix := ip.String() + "/" + strconv.Itoa(int(a.PrefixLength))
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

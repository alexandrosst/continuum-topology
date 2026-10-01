package probe

import (
	"net"
	"reflect"
	"testing"

	"github.com/jsimonetti/rtnetlink/v2"
	"golang.org/x/sys/unix"

	continuumv1 "continuum/gen/continuumv1"
)

func link(index uint32, name, kind string) rtnetlink.LinkMessage {
	var info *rtnetlink.LinkInfo
	if kind != "" {
		info = &rtnetlink.LinkInfo{Kind: kind}
	}
	return rtnetlink.LinkMessage{Index: index, Attributes: &rtnetlink.LinkAttributes{Name: name, Info: info}}
}

// linkUpWithMtu is like link, but also sets the administrative IFF_UP flag and an MTU - for the tests
// below that exercise buildTunnels' new Mtu/Up extraction specifically.
func linkUpWithMtu(index uint32, name, kind string, mtu uint32, up bool) rtnetlink.LinkMessage {
	l := link(index, name, kind)
	l.Attributes.MTU = mtu
	if up {
		l.Flags |= unix.IFF_UP
	}
	return l
}

func addr(index uint32, ip string, prefix uint8) rtnetlink.AddressMessage {
	return rtnetlink.AddressMessage{
		Index:        index,
		PrefixLength: prefix,
		Attributes:   &rtnetlink.AddressAttributes{Address: net.ParseIP(ip)},
	}
}

func route(outIface uint32, dst string, dstLen uint8) rtnetlink.RouteMessage {
	return rtnetlink.RouteMessage{
		DstLength:  dstLen,
		Attributes: rtnetlink.RouteAttributes{Dst: net.ParseIP(dst), OutIface: outIface},
	}
}

func TestBuildTunnelsRecognizesEveryOverlayKind(t *testing.T) {
	for kind := range overlayKinds {
		got := buildTunnels([]rtnetlink.LinkMessage{link(1, "t0", kind)}, nil, nil)
		if len(got) != 1 || got[0].Kind != kind || got[0].Name != "t0" {
			t.Errorf("kind %q: got %+v, want one TunnelInterface{Name: t0, Kind: %s}", kind, got, kind)
		}
	}
}

func TestBuildTunnelsIgnoresOrdinaryAndMundaneVirtualLinks(t *testing.T) {
	links := []rtnetlink.LinkMessage{
		link(1, "eth0", ""),      // a physical NIC has no IFLA_LINKINFO at all
		link(2, "br0", "bridge"), // virtual, but not a tunnel - mundane housekeeping links stay out
		link(3, "veth0abc", "veth"),
		link(4, "bond0", "bond"),
	}
	if got := buildTunnels(links, nil, nil); got != nil {
		t.Errorf("got %+v, want nothing recognized", got)
	}
}

func TestBuildTunnelsAttachesAddressesAndRoutesToTheRightInterfaceByIndex(t *testing.T) {
	links := []rtnetlink.LinkMessage{
		link(5, "wg0", "wireguard"),
		link(6, "gre1", "gre"),
		link(7, "eth0", ""), // present in the dump, but not a tunnel - its address/route must not leak in
	}
	addrs := []rtnetlink.AddressMessage{
		addr(5, "10.8.0.1", 24),
		addr(6, "192.0.2.1", 30),
		addr(7, "203.0.113.5", 24),
	}
	routes := []rtnetlink.RouteMessage{
		route(5, "10.8.0.0", 24),
		route(6, "198.51.100.0", 24),
		route(7, "203.0.113.0", 24),
		route(7, "0.0.0.0", 0), // a default route via the physical NIC - irrelevant either way
	}
	got := buildTunnels(links, addrs, routes)
	if len(got) != 2 {
		t.Fatalf("got %d tunnels, want 2: %+v", len(got), got)
	}
	byName := map[string]*struct{ addrs, routes []string }{}
	for _, tu := range got {
		byName[tu.Name] = &struct{ addrs, routes []string }{tu.Addresses, tu.Routes}
	}
	if a := byName["wg0"]; a == nil || !reflect.DeepEqual(a.addrs, []string{"10.8.0.1/24"}) || !reflect.DeepEqual(a.routes, []string{"10.8.0.0/24"}) {
		t.Errorf("wg0 = %+v, want addr 10.8.0.1/24 and route 10.8.0.0/24", byName["wg0"])
	}
	if g := byName["gre1"]; g == nil || !reflect.DeepEqual(g.addrs, []string{"192.0.2.1/30"}) || !reflect.DeepEqual(g.routes, []string{"198.51.100.0/24"}) {
		t.Errorf("gre1 = %+v, want addr 192.0.2.1/30 and route 198.51.100.0/24", byName["gre1"])
	}
}

func TestBuildTunnelsDropsTheDefaultRouteAndCapsRouteCount(t *testing.T) {
	links := []rtnetlink.LinkMessage{link(1, "wg0", "wireguard")}
	routes := []rtnetlink.RouteMessage{route(1, "0.0.0.0", 0)}
	for i := 0; i < maxTunnelRoutes+5; i++ {
		routes = append(routes, route(1, "10.0.0.0", 24)) // same prefix repeated: caps by count, not distinctness
	}
	got := buildTunnels(links, nil, routes)
	if len(got) != 1 {
		t.Fatalf("got %d tunnels, want 1", len(got))
	}
	if n := len(got[0].Routes); n != maxTunnelRoutes {
		t.Errorf("routes = %d, want the cap of %d (and no 0.0.0.0/0 among them)", n, maxTunnelRoutes)
	}
}

func TestBuildTunnelsReturnsNothingWhenNoLinkMatches(t *testing.T) {
	if got := buildTunnels(nil, nil, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// TestBuildTunnelsReadsMtuAndUpFromTheSameLinkDump pins the new fields: both come off the exact same
// link dump Name/Kind already do, with Up reflecting the administrative IFF_UP flag specifically (not
// any operational-state attribute - see Up's own doc for why).
func TestBuildTunnelsReadsMtuAndUpFromTheSameLinkDump(t *testing.T) {
	links := []rtnetlink.LinkMessage{
		linkUpWithMtu(1, "wg0", "wireguard", 1420, true),
		linkUpWithMtu(2, "gre1", "gre", 1476, false),
	}
	got := buildTunnels(links, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d tunnels, want 2", len(got))
	}
	byName := map[string]*continuumv1.TunnelInterface{}
	for _, tu := range got {
		byName[tu.Name] = tu
	}
	if wg := byName["wg0"]; wg == nil || wg.Mtu != 1420 || !wg.Up {
		t.Errorf("wg0 = %+v, want Mtu=1420 Up=true", wg)
	}
	if gre := byName["gre1"]; gre == nil || gre.Mtu != 1476 || gre.Up {
		t.Errorf("gre1 = %+v, want Mtu=1476 Up=false", gre)
	}
}
func TestBuildHostSubnetsUsesOnlyTheDefaultRouteInterface(t *testing.T) {
	addrs := []rtnetlink.AddressMessage{
		addr(1, "10.0.5.12", 24), // on the default-route interface
		addr(2, "192.0.2.9", 24), // a second interface with no default route - must not leak in
	}
	routes := []rtnetlink.RouteMessage{
		route(1, "0.0.0.0", 0),    // the default route, via interface 1
		route(2, "192.0.2.0", 24), // interface 2's own subnet route - not a default route
	}
	got := buildHostSubnets(addrs, routes)
	want := []string{"10.0.5.12/24"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildHostSubnetsExcludesLoopbackAndLinkLocal(t *testing.T) {
	addrs := []rtnetlink.AddressMessage{
		addr(1, "127.0.0.1", 8),
		addr(1, "169.254.1.1", 16),
		addr(1, "fe80::1", 64),
		addr(1, "10.0.5.12", 24),
	}
	routes := []rtnetlink.RouteMessage{route(1, "0.0.0.0", 0)}
	got := buildHostSubnets(addrs, routes)
	want := []string{"10.0.5.12/24"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want only the real address, no loopback/link-local: %v", got, want)
	}
}

func TestBuildHostSubnetsCoversBothIPv4AndIPv6DefaultRoutes(t *testing.T) {
	addrs := []rtnetlink.AddressMessage{
		addr(1, "10.0.5.12", 24),
		addr(2, "2001:db8::5", 64),
	}
	routes := []rtnetlink.RouteMessage{
		route(1, "0.0.0.0", 0), // IPv4 default route, via interface 1
		route(2, "::", 0),      // IPv6 default route, via interface 2
	}
	got := buildHostSubnets(addrs, routes)
	want := []string{"10.0.5.12/24", "2001:db8::5/64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want both uplinks' subnets: %v", got, want)
	}
}

func TestBuildHostSubnetsReturnsNothingWithoutADefaultRoute(t *testing.T) {
	addrs := []rtnetlink.AddressMessage{addr(1, "10.0.5.12", 24)}
	routes := []rtnetlink.RouteMessage{route(1, "192.168.0.0", 16)} // a route, but never the default one
	if got := buildHostSubnets(addrs, routes); got != nil {
		t.Errorf("got %v, want nil - no default route means no reliable uplink to report", got)
	}
}

func TestBuildHostSubnetsDedupsRepeatedAddresses(t *testing.T) {
	addrs := []rtnetlink.AddressMessage{
		addr(1, "10.0.5.12", 24),
		addr(1, "10.0.5.12", 24), // the kernel occasionally reports the same address twice; never doubled here
	}
	routes := []rtnetlink.RouteMessage{route(1, "0.0.0.0", 0)}
	got := buildHostSubnets(addrs, routes)
	want := []string{"10.0.5.12/24"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want deduped to %v", got, want)
	}
}

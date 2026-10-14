package probe

import (
	"net"
	"reflect"
	"strconv"
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

func linkUp(index uint32, name, kind string) rtnetlink.LinkMessage {
	return linkUpWithMtu(index, name, kind, 1500, true)
}

func TestBuildHostAddressesOffersTheNodesOwnLanMeshAndPublicAddresses(t *testing.T) {
	links := []rtnetlink.LinkMessage{
		linkUp(1, "eth0", ""), linkUp(2, "wg0", "wireguard"), linkUp(3, "tailscale0", "tun"), linkUp(4, "ens5", ""),
	}
	addrs := []rtnetlink.AddressMessage{addr(1, "10.0.5.12", 24), addr(2, "10.8.0.3", 24), addr(3, "100.64.3.7", 32), addr(4, "203.0.113.20", 28), addr(1, "10.0.5.12", 24)}
	got := buildHostAddresses(links, addrs)
	var have []string
	for _, h := range got {
		have = append(have, h.Interface+" "+h.Address)
	}
	// Sorted by interface, an address listed twice once.
	if want := []string{"ens5 203.0.113.20/28", "eth0 10.0.5.12/24", "tailscale0 100.64.3.7/32", "wg0 10.8.0.3/24"}; !reflect.DeepEqual(have, want) {
		t.Fatalf("got %v, want %v", have, want)
	}
	if got[len(got)-1].Kind != "wireguard" {
		t.Fatalf("the link driver is kept: %+v", got)
	}
}

func TestBuildHostAddressesLeavesOutWhatOnlyMeansSomethingInsideTheCluster(t *testing.T) {
	links := []rtnetlink.LinkMessage{
		linkUp(1, "eth0", ""),
		linkUp(2, "lo", ""), // loopback by name only: the flag is what counts, set below
		linkUp(3, "cni0", "bridge"), linkUp(4, "docker0", "bridge"), linkUp(5, "flannel.1", "vxlan"), linkUp(6, "kube-ipvs0", "dummy"),
		linkUp(7, "cilium_host", ""), linkUp(8, "veth1a2b", "veth"), linkUp(9, "tunl0", "ipip"), linkUp(10, "br0", "bridge"),
		linkUpWithMtu(11, "eth1", "", 1500, false), // down
	}
	links[1].Flags |= unix.IFF_LOOPBACK
	addrs := []rtnetlink.AddressMessage{
		addr(1, "10.0.5.12", 24), addr(1, "fe80::1", 64), addr(2, "127.0.0.1", 8), addr(3, "10.244.0.1", 24), addr(4, "172.17.0.1", 16),
		addr(5, "10.244.1.0", 32), addr(6, "10.96.0.1", 32), addr(7, "10.0.0.5", 32), addr(8, "10.244.0.9", 32), addr(9, "10.244.2.1", 32),
		addr(10, "192.168.1.2", 24), addr(11, "10.9.9.9", 24),
	}
	var have []string
	for _, h := range buildHostAddresses(links, addrs) {
		have = append(have, h.Interface+" "+h.Address)
	}
	// The host's own bridge (br0 on a machine whose NIC is enslaved to it) is a real address; every container one is not.
	if want := []string{"br0 192.168.1.2/24", "eth0 10.0.5.12/24"}; !reflect.DeepEqual(have, want) {
		t.Fatalf("got %v, want %v", have, want)
	}
}

func TestBuildHostAddressesIsBounded(t *testing.T) {
	links := []rtnetlink.LinkMessage{linkUp(1, "eth0", "")}
	var addrs []rtnetlink.AddressMessage
	for i := 1; i <= 40; i++ {
		addrs = append(addrs, addr(1, "10.0.0."+strconv.Itoa(i), 24))
	}
	if got := buildHostAddresses(links, addrs); len(got) != maxHostAddresses {
		t.Fatalf("%d addresses", len(got))
	}
}

func TestSanitizeKeepsOnlyValidOfferableHostAddresses(t *testing.T) {
	s := Sanitize(&continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "wg0", Kind: "wireguard", Address: "10.8.0.3/24"},
		{Interface: "lo", Address: "127.0.0.1/8"}, {Interface: "eth0", Address: "fe80::1/64"}, {Interface: "eth0", Address: "not-an-ip"}, {Interface: "", Address: "10.0.0.1/24"}, nil,
	}})
	if len(s.HostAddresses) != 1 || s.HostAddresses[0].Interface != "wg0" || s.HostAddresses[0].Address != "10.8.0.3/24" {
		t.Fatalf("sanitized: %v", s.HostAddresses)
	}
}

func TestBuildHostAddressesLeavesOutTemporaryAndUnusableIPv6(t *testing.T) {
	links := []rtnetlink.LinkMessage{linkUp(1, "eno1", "")}
	with := func(ip string, flags uint8, attr uint32) rtnetlink.AddressMessage {
		a := addr(1, ip, 64)
		a.Flags, a.Attributes.Flags = flags, attr
		return a
	}
	addrs := []rtnetlink.AddressMessage{
		addr(1, "147.102.13.108", 24),
		addr(1, "2001:648:2000:d:1e69:7aff:fe61:a869", 64),                   // stable
		with("2001:648:2000:d:ea59:a895:3b26:d089", 0, unix.IFA_F_TEMPORARY), // the flag arrives in IFA_FLAGS
		with("2001:648:2000:d:4686:64c8:bbce:6df6", unix.IFA_F_TEMPORARY, 0), // or in the message header
		with("2001:648:2000:d::7", 0, unix.IFA_F_TENTATIVE),
		with("2001:648:2000:d::8", 0, unix.IFA_F_DEPRECATED),
	}
	var have []string
	for _, h := range buildHostAddresses(links, addrs) {
		have = append(have, h.Address)
	}
	if want := []string{"147.102.13.108/24", "2001:648:2000:d:1e69:7aff:fe61:a869/64"}; !reflect.DeepEqual(have, want) {
		t.Fatalf("got %v, want %v", have, want)
	}
}

// The server asks the same question of the tunnel interfaces it takes peer addresses from: a CNI's overlay device is the
// cluster's own, a mesh's WireGuard device (and a plain tun) is not.
func TestContainerNetworkInterfaceTellsACniDeviceFromAMesh(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		want       bool
	}{
		{"flannel.1", "vxlan", true}, {"cilium_vxlan", "vxlan", true}, {"tunl0", "ipip", true}, {"cilium_wg0", "wireguard", true},
		{"cali1234", "", true}, {"cni0", "bridge", true}, {"docker0", "bridge", true}, {"veth9", "veth", true}, {"geneve0", "geneve", true},
		{"wt0", "wireguard", false}, {"wg0", "wireguard", false}, {"tailscale0", "tun", false}, {"eth0", "", false}, {"br0", "bridge", false},
	} {
		if got := ContainerNetworkInterface(c.name, c.kind); got != c.want {
			t.Errorf("%s (%q): %v, want %v", c.name, c.kind, got, c.want)
		}
	}
}

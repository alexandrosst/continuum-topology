package link

import "net/netip"

// The kinds of address a peer is reached at. The active measurements can be pinned to one kind for a pair (Peer.Via), so an
// administrator can say which of the paths between two clusters is the one to measure.
const (
	// ClassMesh is an address of an overlay network: Tailscale's and Headscale's 100.64.0.0/10 and its IPv6 range.
	ClassMesh = "mesh"
	// ClassPrivate is a private range that is not a mesh's: RFC 1918, unique local IPv6 and the like.
	ClassPrivate = "private"
	// ClassPublic is any other global unicast address.
	ClassPublic = "public"
)

var (
	meshV4 = netip.MustParsePrefix("100.64.0.0/10")
	meshV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Class says which kind of address a is: mesh, private or public. An address that is none of them (loopback, link-local,
// multicast, unspecified) has no class and gives "", so a pin to a kind never selects one.
func Class(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case !a.IsValid():
		return ""
	case meshV4.Contains(a) || meshV6.Contains(a):
		return ClassMesh
	case a.IsPrivate():
		return ClassPrivate
	case a.IsGlobalUnicast():
		return ClassPublic
	}
	return ""
}

// ValidVia says whether s can be a pair's choice of address kind: one of the classes, or empty for "follow the traffic".
func ValidVia(s string) bool {
	switch s {
	case "", ClassMesh, ClassPrivate, ClassPublic:
		return true
	}
	return false
}

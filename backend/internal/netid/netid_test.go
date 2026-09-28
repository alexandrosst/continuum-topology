package netid

import (
	"net/netip"
	"testing"
)

func TestLookupGitHub(t *testing.T) {
	m, ok := Lookup(netip.MustParseAddr("140.82.112.3"))
	if !ok || m.Name != "GitHub" || m.Kind != "saas" || m.Shared {
		t.Fatalf("github = %+v %v, want {Name: GitHub, Kind: saas, Shared: false}, true", m, ok)
	}
}

func TestLookupCloudflareIsShared(t *testing.T) {
	m, ok := Lookup(netip.MustParseAddr("104.16.1.1"))
	if !ok || m.Name != "Cloudflare" || !m.Shared || m.Detail == "" {
		t.Fatalf("cloudflare = %+v %v, want Shared with a non-empty Detail caveat", m, ok)
	}
}

func TestLookupNoMatch(t *testing.T) {
	// A well-known public resolver, deliberately not in this small, curated table.
	if m, ok := Lookup(netip.MustParseAddr("8.8.8.8")); ok {
		t.Errorf("8.8.8.8 should not match any bundled range, got %+v", m)
	}
}

func TestLookupPrivateAndLoopbackNeverMatch(t *testing.T) {
	for _, s := range []string{"10.0.0.1", "192.168.1.1", "127.0.0.1", "::1"} {
		if m, ok := Lookup(netip.MustParseAddr(s)); ok {
			t.Errorf("%s should not match any bundled public range, got %+v", s, m)
		}
	}
}

func TestEveryEntryContainsItsOwnNetworkAddress(t *testing.T) {
	// Cheap sanity check against a copy-paste mistake in the table (e.g. a host bit set in the prefix).
	for _, e := range entries {
		if !e.prefix.Contains(e.prefix.Masked().Addr()) {
			t.Errorf("%v does not contain its own network address", e.prefix)
		}
	}
}

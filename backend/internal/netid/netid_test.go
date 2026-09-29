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

func TestLookupGitLab(t *testing.T) {
	m, ok := Lookup(netip.MustParseAddr("34.74.90.65"))
	if !ok || m.Name != "GitLab" || m.Kind != "saas" || m.Shared {
		t.Fatalf("gitlab = %+v %v, want {Name: GitLab, Kind: saas, Shared: false}, true", m, ok)
	}
}

func TestLookupGoogleDNS(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "8.8.4.4"} {
		m, ok := Lookup(netip.MustParseAddr(s))
		if !ok || m.Name != "Google Public DNS" || m.Kind != "saas" || m.Shared {
			t.Fatalf("%s = %+v %v, want {Name: Google Public DNS, Kind: saas, Shared: false}, true", s, m, ok)
		}
	}
}

func TestLookupGoogleInfraBlock(t *testing.T) {
	// Real addresses with no reverse-DNS PTR record at all (confirmed against Google's own authoritative
	// nameserver) - the reason this block earns a static entry despite the package's general policy
	// against hardcoding Google's IP space. See googleInfra's doc comment in netid.go. Not Shared, like
	// GitHub/GitLab: this is Google's own infrastructure, not a third party's, so it collapses to one
	// topology node with the DNS-matched Google entries below rather than staying scattered per address.
	for _, s := range []string{"216.239.34.178", "216.239.38.178", "216.239.32.10"} {
		m, ok := Lookup(netip.MustParseAddr(s))
		if !ok || m.Name != "Google" || m.Shared || m.Detail == "" {
			t.Fatalf("%s = %+v %v, want Google, not Shared, with a non-empty Detail caveat", s, m, ok)
		}
	}
}

func TestMatchHostGoogleEntriesAreNotShared(t *testing.T) {
	// Unlike the genuine third-party CDN entries (Cloudflare, Akamai, Fastly, ...), Google's own edge
	// hostnames are deliberately Shared: false so every matching address collapses into one "Google" node
	// per port instead of scattering across the canvas - see hostSuffixes' own doc comment in netid.go.
	for _, host := range []string{"lax17s79-in-f14.1e100.net", "raw-cdn-1.googleusercontent.com"} {
		m, ok := MatchHost(host)
		if !ok || m.Name != "Google" || m.Shared {
			t.Errorf("MatchHost(%q) = %+v, %v; want Google, not Shared", host, m, ok)
		}
	}
}

func TestLookupNoMatch(t *testing.T) {
	// A well-known public address, deliberately not in this small, curated table (see the package doc for
	// why broad cloud/CDN ranges like this one are left out on purpose).
	if m, ok := Lookup(netip.MustParseAddr("142.250.80.46")); ok {
		t.Errorf("142.250.80.46 (a Google front-end address, not the DNS resolver) should not match any bundled range, got %+v", m)
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

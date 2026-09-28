// Package netid resolves a public IP address against a small, hand-curated table of ranges the
// provider itself has published (Cloudflare's own edge ranges, GitHub's own AS, GitLab's own webhook
// range, Google Public DNS's two resolver addresses), never from a live network call. This runs only on
// the server - the Continuum control plane already reasons about every cluster's flows in one place, so
// identifying an address once there costs nothing extra in permissions, unlike granting every per-cluster
// agent its own outbound DNS/lookup capability just to do the same thing redundantly, once per cluster. A
// miss here means "not in this small table," not "unknown to the internet" - most of what a real
// cluster's egress actually touches is a major cloud's own compute or CDN edge (AWS, GCP, Azure, Akamai,
// Fastly, ...), which this table deliberately does NOT attempt to cover: those ranges are enormous,
// change often, and - short of Cloudflare's own small, stable edge list - would mean either shipping and
// refreshing a large third-party dataset or matching so broadly it stops meaning anything ("this address
// is somewhere on AWS" tells a viewer little). The table stays small and reviewable on purpose; extending
// it with a specific, stable, single-owner range (another SaaS's published webhook/API block, the way
// GitHub and GitLab already are here) follows the same shape as everything above.
package netid

import "net/netip"

// Match is a known public identity for an IP address.
type Match struct {
	// Name is a short, human label for who this address belongs to (e.g. "GitHub", "Cloudflare").
	Name string
	// Kind mirrors model.ExternalEndpoint.Kind's vocabulary. Every match from this package is "saas" -
	// nothing here ever claims "database" (that stays wellKnownPort's job, from the port number alone).
	Kind string
	// Shared is true when the matched range fronts many unrelated origins - a CDN/edge network - rather
	// than belonging to one single service. Cloudflare's published edge ranges carry traffic for
	// millions of unrelated sites, so two different IPs both matching a Shared range are NOT
	// necessarily "the same thing" the way two IPs both matching GitHub's own range are. A caller must
	// not merge endpoints into one identity on a Shared match, only label them.
	Shared bool
	// Detail is a longer, honest caveat worth keeping as evidence rather than folding into Name itself
	// (e.g. explaining that a Shared match doesn't identify the actual origin behind it).
	Detail string
}

var cloudflare = Match{
	Name:   "Cloudflare",
	Kind:   "saas",
	Shared: true,
	Detail: "Reaches Cloudflare's edge network; the actual origin behind it isn't identifiable from the IP alone.",
}

var github = Match{
	Name: "GitHub",
	Kind: "saas",
}

var gitlab = Match{
	Name: "GitLab",
	Kind: "saas",
}

// googleDNS is deliberately its own Match, not folded into a general "Google" entry: 8.8.8.8/8.8.4.4 are
// two single-purpose anycast resolver addresses Google has published on their own, unlike the rest of
// Google's IP space (search, Gmail, Cloud Platform tenants, ...), which all sit behind the same enormous,
// constantly-changing, genuinely shared range - exactly the kind of address this package's own doc comment
// says to leave out rather than guess at with a stale or approximate block.
var googleDNS = Match{
	Name: "Google Public DNS",
	Kind: "saas",
}

// entries: hand-curated, not fetched. Bits() is used to break ties when ranges nest, so more specific
// entries can be added later without reordering anything here.
var entries = []struct {
	prefix netip.Prefix
	match  Match
}{
	// GitHub, Inc's own announced range (AS36459), covering github.com/api.github.com/git over SSH.
	// GitHub's own docs explicitly say the Meta API's list "is not intended to be an exhaustive list,"
	// so this is deliberately just the one well-known, stable block, not an attempt at full coverage.
	{netip.MustParsePrefix("140.82.112.0/20"), github},

	// Cloudflare's published edge IPv4 ranges (https://www.cloudflare.com/ips/), fetched 2026-09-28.
	{netip.MustParsePrefix("103.21.244.0/22"), cloudflare},
	{netip.MustParsePrefix("103.22.200.0/22"), cloudflare},
	{netip.MustParsePrefix("103.31.4.0/22"), cloudflare},
	{netip.MustParsePrefix("104.16.0.0/13"), cloudflare},
	{netip.MustParsePrefix("104.24.0.0/14"), cloudflare},
	{netip.MustParsePrefix("108.162.192.0/18"), cloudflare},
	{netip.MustParsePrefix("131.0.72.0/22"), cloudflare},
	{netip.MustParsePrefix("141.101.64.0/18"), cloudflare},
	{netip.MustParsePrefix("162.158.0.0/15"), cloudflare},
	{netip.MustParsePrefix("172.64.0.0/13"), cloudflare},
	{netip.MustParsePrefix("173.245.48.0/20"), cloudflare},
	{netip.MustParsePrefix("188.114.96.0/20"), cloudflare},
	{netip.MustParsePrefix("190.93.240.0/20"), cloudflare},
	{netip.MustParsePrefix("197.234.240.0/22"), cloudflare},
	{netip.MustParsePrefix("198.41.128.0/17"), cloudflare},

	// GitLab.com's own published outbound range for webhooks and repository mirroring
	// (https://docs.gitlab.com/user/gitlab_com/#ip-range, fetched 2026-09-29). GitLab.com's own inbound
	// traffic is fronted by Cloudflare (already covered above) and it publishes no static range for
	// CI/CD runner egress, so this covers only that one specific, documented case - not "any GitLab.com
	// traffic" in general.
	{netip.MustParsePrefix("34.74.90.64/28"), gitlab},
	{netip.MustParsePrefix("34.74.226.0/24"), gitlab},

	// Google Public DNS's two anycast resolver addresses (https://developers.google.com/speed/public-dns,
	// fetched 2026-09-29).
	{netip.MustParsePrefix("8.8.8.8/32"), googleDNS},
	{netip.MustParsePrefix("8.8.4.4/32"), googleDNS},
}

// Lookup reports the known identity of ip, if any bundled range contains it - the longest matching
// prefix wins when more than one does (none of the entries above nest today, but a future addition
// might). False means no bundled range covers this address, not that the address is unrecognized by
// the internet at large: see the package doc for why this table is small on purpose.
func Lookup(ip netip.Addr) (Match, bool) {
	ip = ip.Unmap()
	bestBits := -1
	var best Match
	for _, e := range entries {
		if e.prefix.Bits() > bestBits && e.prefix.Contains(ip) {
			bestBits = e.prefix.Bits()
			best = e.match
		}
	}
	return best, bestBits >= 0
}

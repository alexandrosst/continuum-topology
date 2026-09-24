package server

import (
	"net/netip"
	"strings"
	"sync"

	"continuum/internal/geoip"
)

const geoCacheMax = 4096

// Geo turns an agent's connecting address into a suggested location using an offline database.
// A nil *Geo is valid and means the feature is off. Results are cached per address in memory so a
// state request never touches the file, and the cache is bounded.
type Geo struct {
	db *geoip.DB
	// pub is the optional fallback for a connecting address that is not Locatable at all (private,
	// loopback, CGNAT - see SetPublicIPFallback). Nil means the fallback is off; Locate then behaves
	// exactly as it did before the fallback existed.
	pub *PublicIPFinder
	// asn is an optional second, ASN-shaped database (see SetASN) that adds which network an address
	// belongs to alongside wherever db placed it. Nil means the feature is off.
	asn *geoip.DB

	mu    sync.Mutex
	cache map[netip.Addr]*geoip.Result // nil value = looked up, no answer
}

// NewGeo wraps an opened database; nil disables the feature.
func NewGeo(db *geoip.DB) *Geo {
	if db == nil {
		return nil
	}
	return &Geo{db: db, cache: map[netip.Addr]*geoip.Result{}}
}

// SetPublicIPFallback turns on Locate's fallback for connecting addresses that cannot be located at
// all: see PublicIPFinder's doc comment for why this server's own public address is a fair guess in
// exactly that case. Safe to call on a nil *Geo (the feature stays off, since there is nowhere to store it).
func (g *Geo) SetPublicIPFallback(f *PublicIPFinder) {
	if g == nil {
		return
	}
	g.pub = f
}

// SetASN turns on ASN (which-network) enrichment: every result Locate returns for an address this
// database has a record for gains an ASN and ASOrg alongside its city/country, whether that
// location came from the main database or (see SetPublicIPFallback) from the estimated fallback.
// db is a second file, in ASN-record shape rather than city/country shape - see geoip.LookupASN.
// Safe to call on a nil *Geo (the feature stays off, since there is nowhere to store it).
func (g *Geo) SetASN(db *geoip.DB) {
	if g == nil {
		return
	}
	g.asn = db
}

// Locate returns the suggested location of a textual address, or nil.
func (g *Geo) Locate(addr string) *geoip.Result {
	if g == nil {
		return nil
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return nil
	}
	ip = ip.Unmap().WithZone("")
	if r := g.lookup(ip); r != nil {
		g.addASN(r, ip)
		return r
	}
	// A result up to here means either the address was Locatable and simply had no record (a real
	// "don't know"), or it was not Locatable at all. Only the second is worth a fallback: falling back
	// for the first would silently swap a real, if unanswered, question for an unrelated address's
	// answer.
	if g.pub == nil || geoip.Locatable(ip) {
		return nil
	}
	pubIP, ok := g.pub.Current()
	if !ok {
		return nil
	}
	r := g.lookup(pubIP)
	if r == nil {
		return nil
	}
	r.Estimated = true
	g.addASN(r, pubIP)
	return r
}

// UnlocatableReason explains why addr itself could not be located directly: "cgnat" | "private" |
// "loopback" | "link-local" | "link-local-multicast" | "multicast" | "unspecified" | "invalid", or "" when
// addr is itself locatable (Locate may still have returned nil for it - a database miss, a different
// "don't know" than this). It does not say whether Locate ultimately produced an *estimated* result for
// addr via the public-IP fallback - only whether addr's own address needed one at all. "cgnat" and
// "private" both mean addr is behind some NAT, derived from the address alone (see geoip.UnlocatableReason).
func (g *Geo) UnlocatableReason(addr string) string {
	if g == nil {
		return ""
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	return geoip.UnlocatableReason(ip.Unmap().WithZone(""))
}

// addASN fills in r.ASN/r.ASOrg from the optional ASN database, for whichever address actually
// produced r's location (the agent's own, or - under the fallback - this server's public one).
// Deliberately not folded into lookup's cache: lookup's cache is filled once per address and never
// revisited, which would let ASN enrichment silently miss any address already cached before SetASN
// was called. r is always a caller-owned copy here (see lookup/copyResult), so mutating it is safe.
func (g *Geo) addASN(r *geoip.Result, ip netip.Addr) {
	if g.asn == nil {
		return
	}
	if a, ok := g.asn.LookupASN(ip); ok {
		r.ASN, r.ASOrg = a.ASN, a.Org
	}
}

// lookup is the direct, cached, offline-database path: what Locate always did before the fallback
// above existed, and still all of what it does for an address that is itself Locatable.
func (g *Geo) lookup(ip netip.Addr) *geoip.Result {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.cache[ip]; ok {
		return copyResult(r)
	}
	var out *geoip.Result
	if r, ok := g.db.Lookup(ip); ok {
		out = &r
	}
	if len(g.cache) >= geoCacheMax {
		for k := range g.cache { // evict an arbitrary entry
			delete(g.cache, k)
			break
		}
	}
	g.cache[ip] = out
	return copyResult(out)
}

// copyResult hands callers their own value so nothing shared can be mutated.
func copyResult(r *geoip.Result) *geoip.Result {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// GeoInfo is what /api/v1/info tells the UI about the feature.
type GeoInfo struct {
	Enabled     bool   `json:"enabled"`
	Database    string `json:"database,omitempty"`
	Description string `json:"description,omitempty"`
	BuiltAt     string `json:"builtAt,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	// PublicIpFallback says whether an unlocatable connecting address (private, CGNAT...) is estimated
	// from this server's own public address rather than left with no suggestion at all. Off by default;
	// see SetPublicIPFallback.
	PublicIPFallback bool `json:"publicIpFallback,omitempty"`
	// ASN says whether a result also carries which network the address belongs to (ASN + org name),
	// from a second database. Off by default; see SetASN.
	ASN bool `json:"asn,omitempty"`
}

// Info describes the configured database.
func (g *Geo) Info() GeoInfo {
	if g == nil {
		return GeoInfo{}
	}
	in := g.db.Info()
	gi := GeoInfo{Enabled: true, Database: in.DatabaseType, Description: in.Description, PublicIPFallback: g.pub != nil, ASN: g.asn != nil}
	if !in.BuiltAt.IsZero() {
		gi.BuiltAt = rfc(in.BuiltAt)
	}
	gi.Attribution = attributionFor(in.DatabaseType, in.Description)
	return gi
}

// attributionFor returns the credit line the database's licence asks for, or "" for an unknown source.
func attributionFor(dbType, description string) string {
	both := strings.ToLower(dbType + " " + description)
	switch {
	case strings.Contains(both, "db-ip") || strings.Contains(both, "dbip"):
		return "IP geolocation by DB-IP.com (CC BY 4.0)"
	case strings.Contains(both, "geolite2"):
		return "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com"
	}
	return ""
}

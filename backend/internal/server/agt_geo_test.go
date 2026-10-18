package server

import (
	"net/netip"
	"testing"

	"continuum/internal/geoip"
	"continuum/internal/geoip/geoiptest"
)

func TestGeoAttribution(t *testing.T) {
	for _, c := range []struct{ typ, desc, want string }{
		{"GeoLite2-City", "GeoLite2 City database", "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com"},
		{"DBIP-Country-Lite", "", "IP geolocation by DB-IP.com (CC BY 4.0)"},
		{"Custom", "DB-IP.com - IP to Country", "IP geolocation by DB-IP.com (CC BY 4.0)"},
		{"Internal", "our own data", ""},
	} {
		if got := attributionFor(c.typ, c.desc); got != c.want {
			t.Errorf("%s/%s: %q", c.typ, c.desc, got)
		}
	}
}

func TestGeoASNEnrichment(t *testing.T) {
	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGeo(db)

	// Off by default: a located address carries no ASN fields at all.
	r := g.Locate("203.0.113.24")
	if r == nil || r.ASN != 0 || r.ASOrg != "" {
		t.Fatalf("ASN enrichment must be off until SetASN: %+v", r)
	}

	asnDB, err := geoip.Open(geoiptest.TinyASNDB(t, 64512, "Example Networks LLC"))
	if err != nil {
		t.Fatal(err)
	}
	g.SetASN(asnDB)

	// The address above was already cached (by the "off by default" call right before SetASN) - a
	// naive implementation that merges ASN at cache-fill time would leave it stuck without ASN fields
	// forever. It must not: SetASN takes effect for every address, cached or not.
	r = g.Locate("203.0.113.24")
	if r == nil || r.City != "Patras" || r.ASN != 64512 || r.ASOrg != "Example Networks LLC" {
		t.Fatalf("expected Patras enriched with the ASN record, got %+v", r)
	}
	r.ASOrg = "mutated"
	if g.Locate("203.0.113.24").ASOrg != "Example Networks LLC" {
		t.Fatal("callers must not be able to change the cached answer")
	}

	// A located address the ASN database has nothing for keeps its location, just without ASN fields:
	// the two databases are independent, and one missing a record is not an error for the other.
	if r := g.Locate("203.0.114.1"); r != nil {
		t.Fatalf("no city record at all: %+v", r) // sanity: this address really has no city record
	}

	if (*Geo)(nil).Info().ASN {
		t.Fatal("a nil Geo must never report ASN as on")
	}
	if !g.Info().ASN {
		t.Fatal("Info() should report ASN once configured")
	}
}

func TestGeoLocatesAndCaches(t *testing.T) {
	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGeo(db)
	r := g.Locate("203.0.113.24")
	if r == nil || r.Country != "GR" || r.City != "Patras" || r.Level != "city" || r.Lat == nil {
		t.Fatalf("%+v", r)
	}
	r.City = "mutated"
	if g.Locate("203.0.113.24").City != "Patras" {
		t.Fatal("callers must not be able to change the cached answer")
	}
	for _, a := range []string{"10.0.0.1", "100.64.0.9", "203.0.114.1", "unknown", "", "not an ip"} {
		if g.Locate(a) != nil {
			t.Errorf("%q should have no location", a)
		}
	}
	for i := 0; i < geoCacheMax+500; i++ { // the cache stays bounded
		g.Locate(netip.AddrFrom4([4]byte{198, 18, byte(i >> 8), byte(i)}).String())
	}
	g.mu.Lock()
	n := len(g.cache)
	g.mu.Unlock()
	if n > geoCacheMax {
		t.Fatalf("cache grew to %d", n)
	}
	if (*Geo)(nil).Locate("203.0.113.24") != nil || (*Geo)(nil).Info().Enabled {
		t.Fatal("a nil Geo is the feature being off")
	}
}

func TestGeoPublicIPFallback(t *testing.T) {
	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGeo(db)

	// Off by default: a private address still gets no suggestion at all.
	if r := g.Locate("10.0.0.7"); r != nil {
		t.Fatalf("no fallback configured: %+v", r)
	}

	f := &PublicIPFinder{} // not started; Current() reports ok=false until a real answer lands
	g.SetPublicIPFallback(f)
	if r := g.Locate("10.0.0.7"); r != nil {
		t.Fatalf("fallback with no address learned yet: %+v", r)
	}

	f.mu.Lock()
	f.cached = netip.MustParseAddr("203.0.113.24") // Patras, per tinyGeoDB
	f.have = true
	f.mu.Unlock()

	r := g.Locate("10.0.0.7")
	if r == nil || !r.Estimated || r.City != "Patras" || r.Country != "GR" {
		t.Fatalf("expected an estimated Patras result, got %+v", r)
	}

	// A publicly routable address that simply has no database record must NOT fall back - that would
	// silently substitute an unrelated address's answer for a real "don't know".
	if r := g.Locate("203.0.114.1"); r != nil {
		t.Fatalf("a public address with no record must stay unanswered, got %+v", r)
	}

	// The agent's own address, when it IS locatable, always wins over the fallback.
	r = g.Locate("203.0.113.24")
	if r == nil || r.Estimated {
		t.Fatalf("a locatable address must use its own result, not the fallback: %+v", r)
	}

	if (*Geo)(nil).Info().PublicIPFallback {
		t.Fatal("a nil Geo must never report the fallback as on")
	}
	if !g.Info().PublicIPFallback {
		t.Fatal("Info() should report the fallback once configured")
	}
}

func TestGeoUnlocatableReason(t *testing.T) {
	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGeo(db)
	if got := g.UnlocatableReason("10.0.0.7"); got != "private" {
		t.Errorf("private address: %q, want private", got)
	}
	if got := g.UnlocatableReason("100.64.1.1"); got != "cgnat" {
		t.Errorf("cgnat address: %q, want cgnat", got)
	}
	if got := g.UnlocatableReason("203.0.113.24"); got != "" {
		t.Errorf("a locatable address must give no reason, got %q", got)
	}
	if got := g.UnlocatableReason("not-an-ip"); got != "" {
		t.Errorf("an unparseable address must give no reason (not a claim about it), got %q", got)
	}
	if got := (*Geo)(nil).UnlocatableReason("10.0.0.7"); got != "" {
		t.Errorf("a nil Geo must give no reason, got %q", got)
	}
}

package server

import (
	"encoding/binary"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/geoip"
)

// tinyGeoDB writes a valid IPv4 .mmdb (record size 24) that maps 203.0.113.0/24 to Patras.
func tinyGeoDB(t *testing.T) string {
	t.Helper()
	str := func(s string) []byte { return append([]byte{0x40 | byte(len(s))}, s...) }
	dbl := func(f float64) []byte { return binary.BigEndian.AppendUint64([]byte{0x68}, math.Float64bits(f)) }
	cat := func(parts ...[]byte) []byte {
		var o []byte
		for _, p := range parts {
			o = append(o, p...)
		}
		return o
	}
	names := func(en string) []byte { return cat([]byte{0xE1}, str("names"), []byte{0xE1}, str("en"), str(en)) }
	rec := cat([]byte{0xE3},
		str("city"), names("Patras"),
		str("country"), cat([]byte{0xE2}, str("iso_code"), str("GR"), str("names"), []byte{0xE1}, str("en"), str("Greece")),
		str("location"), cat([]byte{0xE2}, str("latitude"), dbl(38.2466), str("longitude"), dbl(21.7346)))

	const bits, nodes = 24, 24 // 203.0.113.0/24: 24 nodes, one per prefix bit
	addr := []byte{203, 0, 113}
	var tree []byte
	put3 := func(v int) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }
	for d := 0; d < bits; d++ {
		bit := int(addr[d>>3]>>(7-uint(d&7))) & 1
		next := d + 1
		if d == bits-1 {
			next = nodes + 16 // first byte of the data section
		}
		rec := [2]int{nodes, nodes} // nodes = "no data"
		rec[bit] = next
		tree = append(tree, put3(rec[0])...)
		tree = append(tree, put3(rec[1])...)
	}
	// build_epoch is a uint64: extended type 9 = 7+2, so control byte 0x04 (size 4) then 0x02
	meta := cat([]byte{0xE6},
		str("node_count"), []byte{0xC1, nodes},
		str("record_size"), []byte{0xA1, 24},
		str("ip_version"), []byte{0xA1, 4},
		str("database_type"), str("DBIP-City-Lite"),
		str("description"), cat([]byte{0xE1}, str("en"), str("DB-IP City Lite")),
		str("build_epoch"), []byte{0x04, 0x02, 0x65, 0xB0, 0x00, 0x00})
	file := cat(tree, make([]byte, 16), rec, []byte("\xAB\xCD\xEFMaxMind.com"), meta)
	p := filepath.Join(t.TempDir(), "geo.mmdb")
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// tinyASNDB writes a valid IPv4 .mmdb (record size 24) that maps 203.0.113.0/24 to an ASN record -
// the same range tinyGeoDB places in Patras, so the two can be layered to test SetASN's merge.
func tinyASNDB(t *testing.T, asn uint32, org string) string {
	t.Helper()
	// Unlike tinyGeoDB's field names, "autonomous_system_organization" is 30 bytes - one over the 29
	// that fit in a control byte's own 5-bit size field - so, unlike tinyGeoDB's str(), this one must
	// also handle the one-byte-extended length form (spec size 29: actual length = 29 + next byte).
	str := func(s string) []byte {
		n := len(s)
		if n < 29 {
			return append([]byte{0x40 | byte(n)}, s...)
		}
		if n >= 285 {
			t.Fatalf("test string %q too long for this helper", s)
		}
		return append([]byte{0x40 | 29, byte(n - 29)}, s...)
	}
	u32 := func(v uint32) []byte { return []byte{0xC4, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	cat := func(parts ...[]byte) []byte {
		var o []byte
		for _, p := range parts {
			o = append(o, p...)
		}
		return o
	}
	rec := cat([]byte{0xE2},
		str("autonomous_system_number"), u32(asn),
		str("autonomous_system_organization"), str(org))

	const bits, nodes = 24, 24 // 203.0.113.0/24: 24 nodes, one per prefix bit
	addr := []byte{203, 0, 113}
	var tree []byte
	put3 := func(v int) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }
	for d := 0; d < bits; d++ {
		bit := int(addr[d>>3]>>(7-uint(d&7))) & 1
		next := d + 1
		if d == bits-1 {
			next = nodes + 16
		}
		row := [2]int{nodes, nodes}
		row[bit] = next
		tree = append(tree, put3(row[0])...)
		tree = append(tree, put3(row[1])...)
	}
	meta := cat([]byte{0xE6},
		str("node_count"), []byte{0xC1, nodes},
		str("record_size"), []byte{0xA1, 24},
		str("ip_version"), []byte{0xA1, 4},
		str("database_type"), str("GeoLite2-ASN"),
		str("description"), cat([]byte{0xE1}, str("en"), str("GeoLite2 ASN")),
		str("build_epoch"), []byte{0x04, 0x02, 0x65, 0xB0, 0x00, 0x00})
	file := cat(tree, make([]byte, 16), rec, []byte("\xAB\xCD\xEFMaxMind.com"), meta)
	p := filepath.Join(t.TempDir(), "asn.mmdb")
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGeoASNEnrichment(t *testing.T) {
	db, err := geoip.Open(tinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGeo(db)

	// Off by default: a located address carries no ASN fields at all.
	r := g.Locate("203.0.113.24")
	if r == nil || r.ASN != 0 || r.ASOrg != "" {
		t.Fatalf("ASN enrichment must be off until SetASN: %+v", r)
	}

	asnDB, err := geoip.Open(tinyASNDB(t, 64512, "Example Networks LLC"))
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
	db, err := geoip.Open(tinyGeoDB(t))
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

func TestStateAndInfoCarryGeoOnlyWhenConfigured(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	enrollFrom := func(ip string) string {
		secret, _, err := a.core.CreateToken(a.ctx, "admin", "c-"+ip, 1)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := csr(t)
		resp, err := a.core.Enroll(a.ctx, ip, &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1, AgentVersion: "0.1.0"})
		if err != nil {
			t.Fatal(err)
		}
		return resp.AgentId
	}
	pub, priv := enrollFrom("203.0.113.24"), enrollFrom("10.0.0.7")
	geoOf := func() map[string]any { // agent id -> connectingGeo (nil when the key is absent)
		r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
		if r.Code != 200 {
			t.Fatalf("state: %d", r.Code)
		}
		out := map[string]any{}
		for _, ag := range r.json(t)["agents"].([]any) {
			m := ag.(map[string]any)
			out[m["id"].(string)] = m["connectingGeo"]
		}
		return out
	}
	infoGeo := func() map[string]any {
		return a.do("GET", "/api/v1/info", nil, withCookie(cookie)).json(t)["geoip"].(map[string]any)
	}

	// no database: nothing is added and the feature reads as disabled
	if g := geoOf(); len(g) != 2 || g[pub] != nil || g[priv] != nil {
		t.Fatalf("without a database: %v", g)
	}
	if i := infoGeo(); i["enabled"] != false || len(i) != 1 {
		t.Fatalf("info without a database: %v", i)
	}

	db, err := geoip.Open(tinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gdb := NewGeo(db)
	a.hub().Geo, a.a.P.Geo = gdb, gdb

	g := geoOf()
	loc, ok := g[pub].(map[string]any)
	if !ok || loc["country"] != "GR" || loc["city"] != "Patras" || loc["level"] != "city" || loc["lat"] != 38.2466 || loc["lng"] != 21.7346 || loc["database"] != "DBIP-City-Lite" {
		t.Fatalf("public agent: %v", g[pub])
	}
	if g[priv] != nil {
		t.Fatalf("a private address must show no location: %v", g[priv])
	}
	i := infoGeo()
	if i["enabled"] != true || i["database"] != "DBIP-City-Lite" || i["description"] != "DB-IP City Lite" ||
		i["attribution"] != "IP geolocation by DB-IP.com (CC BY 4.0)" || i["builtAt"] == nil {
		t.Fatalf("info with a database: %v", i)
	}
}

func TestGeoPublicIPFallback(t *testing.T) {
	db, err := geoip.Open(tinyGeoDB(t))
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

func TestStateCarriesEstimatedGeoThroughTheFallback(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	secret, _, err := a.core.CreateToken(a.ctx, "admin", "c-priv", 1)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := csr(t)
	resp, err := a.core.Enroll(a.ctx, "10.0.0.7", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1, AgentVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}

	db, err := geoip.Open(tinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gdb := NewGeo(db)
	f := &PublicIPFinder{}
	f.mu.Lock()
	f.cached, f.have = netip.MustParseAddr("203.0.113.24"), true
	f.mu.Unlock()
	gdb.SetPublicIPFallback(f)
	a.hub().Geo, a.a.P.Geo = gdb, gdb

	r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("state: %d", r.Code)
	}
	var geo map[string]any
	for _, ag := range r.json(t)["agents"].([]any) {
		m := ag.(map[string]any)
		if m["id"] == resp.AgentId {
			geo, _ = m["connectingGeo"].(map[string]any)
		}
	}
	if geo == nil || geo["estimated"] != true || geo["city"] != "Patras" {
		t.Fatalf("expected an estimated Patras result on the state document, got %v", geo)
	}

	info := a.do("GET", "/api/v1/info", nil, withCookie(cookie)).json(t)["geoip"].(map[string]any)
	if info["publicIpFallback"] != true {
		t.Fatalf("info should report the fallback as on: %v", info)
	}
}

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

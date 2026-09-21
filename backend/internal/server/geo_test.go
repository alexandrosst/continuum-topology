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

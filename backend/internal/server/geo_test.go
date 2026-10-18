package server

import (
	"net/netip"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/geoip"
	"continuum/internal/geoip/geoiptest"
)

func TestStateAndInfoCarryGeoOnlyWhenConfigured(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	enrollFrom := func(ip string) string {
		secret, _, err := a.core.Agents.CreateToken(a.ctx, "admin", "c-"+ip, 1)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := csr(t)
		resp, err := a.core.Agents.Enroll(a.ctx, ip, &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1, AgentVersion: "0.1.0", ApprovalCodeHash: hashFor(t, d)})
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

	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gdb := agents.NewGeo(db)
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

func TestStateCarriesEstimatedGeoThroughTheFallback(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	secret, _, err := a.core.Agents.CreateToken(a.ctx, "admin", "c-priv", 1)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := csr(t)
	resp, err := a.core.Agents.Enroll(a.ctx, "10.0.0.7", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1, AgentVersion: "0.1.0", ApprovalCodeHash: hashFor(t, d)})
	if err != nil {
		t.Fatal(err)
	}

	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gdb := agents.NewGeo(db)
	f := &agents.PublicIPFinder{}
	f.SetForTest(netip.MustParseAddr("203.0.113.24"))
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

func TestStateCarriesConnectingGeoReasonWithoutFallback(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	secret, _, err := a.core.Agents.CreateToken(a.ctx, "admin", "c-priv", 1)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := csr(t)
	resp, err := a.core.Agents.Enroll(a.ctx, "10.0.0.7", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1, AgentVersion: "0.1.0", ApprovalCodeHash: hashFor(t, d)})
	if err != nil {
		t.Fatal(err)
	}

	db, err := geoip.Open(geoiptest.TinyGeoDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gdb := agents.NewGeo(db) // no public-IP fallback configured
	a.hub().Geo, a.a.P.Geo = gdb, gdb

	r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("state: %d", r.Code)
	}
	var found bool
	for _, ag := range r.json(t)["agents"].([]any) {
		m := ag.(map[string]any)
		if m["id"] != resp.AgentId {
			continue
		}
		found = true
		if _, hasGeo := m["connectingGeo"]; hasGeo {
			t.Fatalf("no fallback configured: connectingGeo should be absent, got %v", m["connectingGeo"])
		}
		if m["connectingGeoReason"] != "private" {
			t.Errorf("connectingGeoReason = %v, want private (10.0.0.0/8)", m["connectingGeoReason"])
		}
	}
	if !found {
		t.Fatal("agent not found in state")
	}
}

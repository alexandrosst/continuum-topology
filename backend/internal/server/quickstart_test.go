package server

import (
	"testing"
	"time"
)

// savedBackend saves one quick-start backend and returns its id.
func (e *env) savedBackend(t *testing.T, kind, modality string) string {
	t.Helper()
	n, err := e.core.SaveSettings(e.ctx, "alex", Settings{QuickStartBackends: []QuickStartBackend{
		{Kind: kind, Modality: modality, Namespace: "observability", Retention: "72h"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return n.QuickStartBackends[0].ID
}

func TestMintGatewayTokenHappyPathReturnsTheSecretOnceAndStoresOnlyItsHash(t *testing.T) {
	e := newEnv(t)
	id := e.savedBackend(t, "jaeger", "traces")
	tok, secret, err := e.core.MintGatewayToken(e.ctx, "alex", id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		t.Fatal("no secret returned")
	}
	if tok.BackendID != id || tok.OrgID != "org-1" {
		t.Fatalf("%+v", tok)
	}
	if !tok.ExpiresAt.Equal(tok.CreatedAt.Add(DefaultGatewayTokenTTL)) {
		t.Fatalf("expiry not the default TTL from creation: %+v", tok)
	}
	stored, err := e.st.LatestGatewayToken(e.ctx, "org-1", id)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.SecretHash) == secret {
		t.Fatal("the plaintext secret must never be stored")
	}
	if len(stored.SecretHash) == 0 {
		t.Fatal("no hash stored")
	}
	if string(tok.SecretHash) != string(stored.SecretHash) {
		t.Fatalf("the struct MintGatewayToken returned must carry the same hash that got persisted, got %x want %x", tok.SecretHash, stored.SecretHash)
	}
}

func TestMintGatewayTokenRejectsAnUnknownBackend(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.core.MintGatewayToken(e.ctx, "alex", "qsb-does-not-exist", 0); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound, got %v", err)
	}
}

func TestMintGatewayTokenClampsTTLIntoRange(t *testing.T) {
	e := newEnv(t)
	id := e.savedBackend(t, "loki", "logs")
	tooShort, _, err := e.core.MintGatewayToken(e.ctx, "alex", id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := tooShort.ExpiresAt.Sub(tooShort.CreatedAt); got != MinGatewayTokenTTL {
		t.Fatalf("a 1s ttl was not clamped up to the minimum: got %v", got)
	}
	tooLong, _, err := e.core.MintGatewayToken(e.ctx, "alex", id, 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := tooLong.ExpiresAt.Sub(tooLong.CreatedAt); got != MaxGatewayTokenTTL {
		t.Fatalf("a 1y ttl was not clamped down to the maximum: got %v", got)
	}
}

func TestLatestGatewayTokenReportsNoneUntilOneIsMinted(t *testing.T) {
	e := newEnv(t)
	id := e.savedBackend(t, "prometheus", "metrics")
	if _, err := e.core.LatestGatewayToken(e.ctx, id); kindOf(err) == KindInvalid {
		t.Fatalf("unexpected error before any mint: %v", err)
	}
	if _, _, err := e.core.MintGatewayToken(e.ctx, "alex", id, 0); err != nil {
		t.Fatal(err)
	}
	got, err := e.core.LatestGatewayToken(e.ctx, id)
	if err != nil {
		t.Fatalf("expected the just-minted token, got error: %v", err)
	}
	if got.BackendID != id {
		t.Fatalf("%+v", got)
	}
}

func TestGatewayTokenHTTPRoleGatedMintAndStatus(t *testing.T) {
	a := newAdminRig(t)
	_, adminCookie := a.user(t, "alex", RoleAdmin)
	_, viewerCookie := a.user(t, "val", RoleViewer)
	id := a.savedBackend(t, "jaeger", "traces")

	if r := a.do("POST", "/api/v1/quick-start/"+id+"/gateway-token", map[string]any{}, withCookie(viewerCookie)); r.Code != 403 {
		t.Fatalf("a viewer minting a gateway token: %d %s", r.Code, r.Body.String())
	}

	if r := a.do("GET", "/api/v1/quick-start/"+id+"/gateway-token", nil, withCookie(adminCookie)); r.Code != 200 {
		t.Fatalf("status before mint: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t); got["active"] != false {
		t.Fatalf("expected inactive before any mint: %v", got)
	}

	r := a.do("POST", "/api/v1/quick-start/"+id+"/gateway-token", map[string]any{"ttlSeconds": 3600}, withCookie(adminCookie))
	if r.Code != 201 {
		t.Fatalf("mint: %d %s", r.Code, r.Body.String())
	}
	created := r.json(t)
	token, _ := created["token"].(string)
	if token == "" {
		t.Fatalf("no token in mint response: %v", created)
	}
	if created["expiresAt"] == nil || created["createdAt"] == nil {
		t.Fatalf("mint response missing timestamps: %v", created)
	}

	if r := a.do("GET", "/api/v1/quick-start/"+id+"/gateway-token", nil, withCookie(adminCookie)); r.Code != 200 {
		t.Fatalf("status after mint: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t); got["active"] != true {
		t.Fatalf("expected active after mint: %v", got)
	} else if _, leaked := got["token"]; leaked {
		t.Fatalf("the status call must never carry the secret: %v", got)
	}

	if r := a.do("POST", "/api/v1/quick-start/qsb-does-not-exist/gateway-token", map[string]any{}, withCookie(adminCookie)); r.Code != 404 {
		t.Fatalf("minting for an unknown backend: %d %s", r.Code, r.Body.String())
	}
}

// TestGatewayTokenHTTPReportsExpiredOncePastTTL exercises getGatewayToken's "expired" flag against the
// fake clock (a.now), not wall-clock time - it must come from the same injected clock every other
// time-dependent check in this package uses, or this would need a real sleep to ever flip to true.
func TestGatewayTokenHTTPReportsExpiredOncePastTTL(t *testing.T) {
	a := newAdminRig(t)
	_, adminCookie := a.user(t, "alex", RoleAdmin)
	id := a.savedBackend(t, "loki", "logs")

	r := a.do("POST", "/api/v1/quick-start/"+id+"/gateway-token", map[string]any{"ttlSeconds": int(MinGatewayTokenTTL.Seconds())}, withCookie(adminCookie))
	if r.Code != 201 {
		t.Fatalf("mint: %d %s", r.Code, r.Body.String())
	}

	if r := a.do("GET", "/api/v1/quick-start/"+id+"/gateway-token", nil, withCookie(adminCookie)); r.Code != 200 {
		t.Fatalf("status right after mint: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t); got["expired"] != false {
		t.Fatalf("expected not yet expired: %v", got)
	}

	*a.now = a.now.Add(MinGatewayTokenTTL + time.Second)

	if r := a.do("GET", "/api/v1/quick-start/"+id+"/gateway-token", nil, withCookie(adminCookie)); r.Code != 200 {
		t.Fatalf("status after ttl elapsed: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t); got["active"] != true || got["expired"] != true {
		t.Fatalf("expected active and expired once past ttl: %v", got)
	}
}

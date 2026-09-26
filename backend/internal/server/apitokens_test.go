package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func (r resp) jsonArray(t *testing.T) []map[string]any {
	t.Helper()
	var m []map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON array: %q", r.Body.String())
	}
	return m
}

func TestAPITokenCreateListAuthenticateAndRevoke(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "jamie", RoleViewer)

	// Nothing yet.
	if r := a.do("GET", "/api/v1/auth/tokens", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("list: %d %s", r.Code, r.Body.String())
	} else if got := r.jsonArray(t); len(got) != 0 {
		t.Fatalf("expected no tokens yet, got %v", got)
	}

	// Create one.
	r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "CI pipeline"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	created := r.json(t)
	secret, _ := created["token"].(string)
	id, _ := created["id"].(string)
	if secret == "" || !strings.HasPrefix(secret, patPrefix) || id == "" {
		t.Fatalf("create response = %v", created)
	}
	if created["name"] != "CI pipeline" {
		t.Fatalf("create response name = %v", created)
	}

	// It shows up in the list, without ever repeating the secret.
	list := a.do("GET", "/api/v1/auth/tokens", nil, withCookie(cookie)).jsonArray(t)
	if len(list) != 1 || list[0]["id"] != id || list[0]["name"] != "CI pipeline" {
		t.Fatalf("list after create = %v", list)
	}
	if _, leaked := list[0]["token"]; leaked {
		t.Fatalf("the list must never carry a token's secret: %v", list[0])
	}

	// The secret authenticates an API call as jamie, with a bearer header instead of the cookie - and
	// needs no X-Requested-With the way a cookie-carried request does, since nothing about it can be
	// forged from a browser cross-site.
	if r := a.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+secret), withoutXRW()); r.Code != 200 {
		t.Fatalf("me via token: %d %s", r.Code, r.Body.String())
	} else if u := r.json(t)["user"].(map[string]any); u["username"] != "jamie" {
		t.Fatalf("me via token resolved to %v", u)
	}

	// A malformed or unknown secret is refused the same as no credential at all.
	if r := a.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+patPrefix+"nope")); r.Code != 401 {
		t.Fatalf("bad token: %d %s", r.Code, r.Body.String())
	}

	// Revoking it ends it immediately.
	if r := a.do("POST", "/api/v1/auth/tokens/"+id+"/revoke", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+secret)); r.Code != 401 {
		t.Fatalf("me after revoke: %d %s", r.Code, r.Body.String())
	}
	if list := a.do("GET", "/api/v1/auth/tokens", nil, withCookie(cookie)).jsonArray(t); len(list) != 0 {
		t.Fatalf("list after revoke = %v", list)
	}

	// One person's token cannot be revoked by another.
	_, otherCookie := a.user(t, "riley", RoleViewer)
	r = a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "second"}, withCookie(cookie))
	id2, _ := r.json(t)["id"].(string)
	if r := a.do("POST", "/api/v1/auth/tokens/"+id2+"/revoke", nil, withCookie(otherCookie)); r.Code != 404 {
		t.Fatalf("cross-account revoke: %d %s", r.Code, r.Body.String())
	}
}

func TestAPITokenNameDefaultsAndTruncates(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "morgan", RoleViewer)

	r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "   "}, withCookie(cookie))
	if r.Code != 200 || r.json(t)["name"] != "Unnamed token" {
		t.Fatalf("blank name: %d %v", r.Code, r.json(t))
	}

	long := strings.Repeat("x", 200)
	r = a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": long}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("long name: %d %s", r.Code, r.Body.String())
	}
	if name, _ := r.json(t)["name"].(string); len(name) != maxAPITokenNameLen {
		t.Fatalf("long name kept at %d chars, want %d", len(name), maxAPITokenNameLen)
	}
}

func TestAPITokenListingRequiresASession(t *testing.T) {
	a := newAdminRig(t)
	if r := a.do("GET", "/api/v1/auth/tokens", nil); r.Code != 401 {
		t.Fatalf("unauthenticated list: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "x"}); r.Code != 401 {
		t.Fatalf("unauthenticated create: %d %s", r.Code, r.Body.String())
	}
}

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
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
	_, cookie := a.user(t, "jamie", accounts.RoleViewer)

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
	if secret == "" || !strings.HasPrefix(secret, kernel.PatPrefix) || id == "" {
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
	if r := a.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+kernel.PatPrefix+"nope")); r.Code != 401 {
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
	_, otherCookie := a.user(t, "riley", accounts.RoleViewer)
	r = a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "second"}, withCookie(cookie))
	id2, _ := r.json(t)["id"].(string)
	if r := a.do("POST", "/api/v1/auth/tokens/"+id2+"/revoke", nil, withCookie(otherCookie)); r.Code != 404 {
		t.Fatalf("cross-account revoke: %d %s", r.Code, r.Body.String())
	}
}

func TestAPITokenNameDefaultsAndTruncates(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "morgan", accounts.RoleViewer)

	r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "   "}, withCookie(cookie))
	if r.Code != 200 || r.json(t)["name"] != "Unnamed token" {
		t.Fatalf("blank name: %d %v", r.Code, r.json(t))
	}

	long := strings.Repeat("x", 200)
	r = a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": long}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("long name: %d %s", r.Code, r.Body.String())
	}
	if name, _ := r.json(t)["name"].(string); len(name) != accounts.MaxAPITokenNameLen {
		t.Fatalf("long name kept at %d chars, want %d", len(name), accounts.MaxAPITokenNameLen)
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

// A stolen personal token must not be able to mint copies of itself.
func TestAPITokenCannotMintAnotherToken(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "jamie", accounts.RoleViewer)
	secret := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "first"}, withCookie(cookie)).json(t)["token"].(string)
	bearer := withHeader("Authorization", "Bearer "+secret)
	if r := a.do("GET", "/api/v1/auth/tokens", nil, bearer); r.Code != 200 {
		t.Fatalf("a token lists its owner's tokens: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "copy"}, bearer); r.Code != 403 {
		t.Fatalf("a token minted a token: %d %s", r.Code, r.Body.String())
	}
	if got := a.do("GET", "/api/v1/auth/tokens", nil, withCookie(cookie)).jsonArray(t); len(got) != 1 {
		t.Fatalf("tokens after the refused copy: %v", got)
	}
}

// Offline recovery ends the account's personal tokens along with its sessions (and it re-enables a disabled
// account, so they must not wake up with it).
func TestOfflineRecoveryRevokesPersonalTokens(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "jamie", accounts.RoleViewer)
	secret := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": "ci"}, withCookie(cookie)).json(t)["token"].(string)
	bearer := withHeader("Authorization", "Bearer "+secret)
	if r := a.do("GET", "/api/v1/info", nil, bearer); r.Code != 200 {
		t.Fatalf("the token before recovery: %d %s", r.Code, r.Body.String())
	}
	if _, err := a.base.Accounts.RecoverPassword(a.ctx, "jamie"); err != nil {
		t.Fatal(err)
	}
	if r := a.do("GET", "/api/v1/info", nil, bearer); r.Code != 401 {
		t.Fatalf("the token after recovery: %d %s", r.Code, r.Body.String())
	}
}

func TestChangingThePasswordRevokesPersonalAPITokens(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "jamie", accounts.RoleViewer)
	_, otherCookie := a.user(t, "casey", accounts.RoleViewer)

	mint := func(c string, name string) string {
		r := a.do("POST", "/api/v1/auth/tokens", map[string]string{"name": name}, withCookie(c))
		if r.Code != 200 {
			t.Fatalf("create %s: %d %s", name, r.Code, r.Body.String())
		}
		return r.json(t)["token"].(string)
	}
	whoami := func(secret string) int {
		return a.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+secret), withoutXRW()).Code
	}
	mine1, mine2, theirs := mint(cookie, "ci"), mint(cookie, "laptop"), mint(otherCookie, "ci")
	for _, s := range []string{mine1, mine2, theirs} {
		if got := whoami(s); got != 200 {
			t.Fatalf("token should work before the change: %d", got)
		}
	}

	if r := a.do("POST", "/api/v1/auth/password", map[string]string{"current": goodPW, "new": "another long pass 7!"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("change password: %d %s", r.Code, r.Body.String())
	}

	for _, s := range []string{mine1, mine2} {
		if got := whoami(s); got != 401 {
			t.Fatalf("a token minted before the password change must stop working, got %d", got)
		}
	}
	if got := whoami(theirs); got != 200 {
		t.Fatalf("another account's token must be untouched, got %d", got)
	}
	// The person can mint a new one afterwards, and it works.
	if got := whoami(mint(cookie, "new")); got != 200 {
		t.Fatalf("a token minted after the change should work, got %d", got)
	}
}

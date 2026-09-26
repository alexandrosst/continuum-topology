package server

import (
	"testing"
	"time"

	"continuum/internal/store"
)

// ssoRig is an adminRig with trusted-header SSO turned on, header named X-Remote-User.
func ssoRig(t *testing.T) *adminRig {
	t.Helper()
	a := newAdminRig(t)
	a.a.SSOHeaderName = "X-Remote-User"
	a.h = a.a.Handler()
	return a
}

func TestSSOLoginSignsInAnExistingAccountByTheAssertedHeader(t *testing.T) {
	a := ssoRig(t)
	r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "org1-owner"))
	if r.Code != 200 {
		t.Fatalf("code=%d body=%s", r.Code, r.Body)
	}
	if r.cookie() == "" {
		t.Fatal("no session cookie set")
	}
	body := r.json(t)
	user, _ := body["user"].(map[string]any)
	if user["username"] != "org1-owner" {
		t.Errorf("signed in as %v, want org1-owner", user["username"])
	}
}

func TestSSOLoginIsCaseInsensitiveLikePasswordLogin(t *testing.T) {
	a := ssoRig(t)
	r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "Org1-Owner"))
	if r.Code != 200 || r.cookie() == "" {
		t.Fatalf("code=%d body=%s", r.Code, r.Body)
	}
}

func TestSSOLoginRejectsAnUnknownIdentity(t *testing.T) {
	a := ssoRig(t)
	r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "nobody-by-this-name"))
	if r.Code != 401 {
		t.Fatalf("code=%d, want 401", r.Code)
	}
	if r.cookie() != "" {
		t.Error("no session should be granted for an unknown identity")
	}
}

func TestSSOLoginRejectsAMissingOrEmptyHeader(t *testing.T) {
	a := ssoRig(t)
	if r := a.raw("/api/v1/auth/sso"); r.Code != 401 {
		t.Errorf("missing header: code=%d, want 401", r.Code)
	}
	if r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "")); r.Code != 401 {
		t.Errorf("empty header: code=%d, want 401", r.Code)
	}
}

func TestSSOLoginRejectsADisabledAccount(t *testing.T) {
	a := ssoRig(t)
	now := time.Now()
	u := store.User{ID: "u-disabled", Username: "disabled-user", PasswordHash: "!", CreatedAt: now}
	if err := a.st.CreateUser(a.ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetDisabled(a.ctx, u.ID, &now); err != nil {
		t.Fatal(err)
	}
	r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "disabled-user"))
	if r.Code != 401 {
		t.Fatalf("code=%d, want 401", r.Code)
	}
}

// A server that never turned SSO on doesn't expose the endpoint at all - not even a "wrong identity" 401
// that would confirm the feature exists.
func TestSSORouteDoesNotExistWhenNotConfigured(t *testing.T) {
	a := newAdminRig(t)
	r := a.raw("/api/v1/auth/sso", withHeader("X-Remote-User", "org1-owner"))
	if r.Code != 404 {
		t.Fatalf("code=%d, want 404 (route should not be registered)", r.Code)
	}
}

// serverInfo tells the UI whether it's worth trying the SSO path at all.
func TestServerInfoAdvertisesWhetherSSOIsConfigured(t *testing.T) {
	off := newAdminRig(t)
	if v := off.raw("/api/v1/server").json(t)["sso"]; v != false {
		t.Errorf("sso=%v, want false", v)
	}
	on := ssoRig(t)
	if v := on.raw("/api/v1/server").json(t)["sso"]; v != true {
		t.Errorf("sso=%v, want true", v)
	}
}

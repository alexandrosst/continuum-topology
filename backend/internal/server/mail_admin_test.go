package server

import (
	"testing"

	"continuum/internal/store"
)

// TestMailConfigOnlyDefaultOrgOwnerMayReadOrWrite checks that /api/v1/mail is gated to an owner of the
// default organisation specifically (requireDefaultOwner), not to any admin/owner of any organisation a
// stranger could just create for themselves - see the reasoning in requireDefaultOwner's doc comment.
func TestMailConfigOnlyDefaultOrgOwnerMayReadOrWrite(t *testing.T) {
	a := newAdminRig(t) // DefaultOrg is "org-1" (see newEnv)

	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin} {
		_, cookie := a.user(t, "member-"+role, role)
		if r := a.do("GET", "/api/v1/mail", nil, withCookie(cookie)); r.Code != 403 {
			t.Fatalf("GET as %s in the default org: %d %s", role, r.Code, r.Body.String())
		}
		if r := a.do("PUT", "/api/v1/mail", map[string]string{"host": "mail.example.com"}, withCookie(cookie)); r.Code != 403 {
			t.Fatalf("PUT as %s in the default org: %d %s", role, r.Code, r.Body.String())
		}
	}

	// Owning a *different* organisation - one anybody can create - must not be enough either.
	otherID := a.account(t, "other-owner")
	otherCookie := a.login(t, "other-owner", goodPW)
	if r := a.do("POST", "/api/v1/orgs", map[string]string{"name": "Someone Else's Org"}, withCookie(otherCookie)); r.Code != 201 {
		t.Fatalf("create org: %d %s", r.Code, r.Body.String())
	}
	_ = otherID
	if r := a.do("GET", "/api/v1/mail", nil, withCookie(otherCookie)); r.Code != 403 {
		t.Fatalf("GET as an owner of a non-default org: %d %s", r.Code, r.Body.String())
	}

	// An owner of the default organisation itself may both read and write.
	_, ownerCookie := a.user(t, "org1-boss", RoleOwner)
	if r := a.do("GET", "/api/v1/mail", nil, withCookie(ownerCookie)); r.Code != 200 {
		t.Fatalf("GET as the default org's owner: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("PUT", "/api/v1/mail", map[string]any{"host": "mail.example.com", "port": "587", "from": "continuum@example.com"}, withCookie(ownerCookie)); r.Code != 200 {
		t.Fatalf("PUT as the default org's owner: %d %s", r.Code, r.Body.String())
	}
}

// TestMailConfigMasksThePasswordAndKeepsItWhenOmitted mirrors putSettings' three-way convention for a
// write-only secret: a GET never carries the password, an update that omits it keeps the stored one, and
// clearPassword removes it explicitly.
func TestMailConfigMasksThePasswordAndKeepsItWhenOmitted(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "org1-boss", RoleOwner)

	r := a.do("PUT", "/api/v1/mail", map[string]any{"host": "mail.example.com", "port": "587", "username": "bot", "password": "s3cret", "from": "continuum@example.com"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("initial PUT: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	if _, has := doc["password"]; has {
		t.Fatalf("password leaked in the response: %v", doc)
	}
	if doc["passwordSet"] != true || doc["enabled"] != true {
		t.Fatalf("unexpected doc after setting a password: %v", doc)
	}
	if got := a.base.Mailer().Password; got != "s3cret" {
		t.Fatalf("stored password = %q, want s3cret", got)
	}

	// Updating another field without sending a password must not blank the stored one.
	r = a.do("PUT", "/api/v1/mail", map[string]any{"host": "mail.example.com", "port": "2525", "username": "bot", "from": "continuum@example.com"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("second PUT: %d %s", r.Code, r.Body.String())
	}
	if got := a.base.Mailer().Password; got != "s3cret" {
		t.Fatalf("password was blanked by an update that omitted it: %q", got)
	}

	// clearPassword removes it explicitly.
	r = a.do("PUT", "/api/v1/mail", map[string]any{"host": "mail.example.com", "port": "2525", "username": "bot", "from": "continuum@example.com", "clearPassword": true}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("clearing PUT: %d %s", r.Code, r.Body.String())
	}
	if doc := r.json(t); doc["passwordSet"] != false {
		t.Fatalf("passwordSet after clearing: %v", doc)
	}
	if got := a.base.Mailer().Password; got != "" {
		t.Fatalf("password after clearing = %q, want empty", got)
	}
}

// TestMailConfigIsSharedByEveryOrganisation confirms the whole point of the mailHolder indirection: a
// change saved through the platform-wide Core is visible through an organisation's own Core too, because
// ForOrg's shallow copy shares the same *mailHolder rather than getting an independent copy of a plain
// MailConfig value.
func TestMailConfigIsSharedByEveryOrganisation(t *testing.T) {
	e := newEnv(t)
	if e.core.Mailer().Enabled() {
		t.Fatal("mail should start disabled")
	}
	if _, err := e.base.SaveMailConfig(e.ctx, "org1-owner", MailConfig{Host: "mail.example.com", Port: "587", From: "continuum@example.com"}); err != nil {
		t.Fatalf("SaveMailConfig: %v", err)
	}
	if !e.core.Mailer().Enabled() {
		t.Fatal("org-1's Core did not see the platform-wide mail configuration")
	}

	// A Core scoped to a brand new organisation, created after the save, also sees it.
	org2 := store.Org{ID: "org-2", Name: "Org Two", CreatedAt: *e.now, CreatedBy: "u-owner"}
	if err := e.st.CreateOrg(e.ctx, org2, "u-owner"); err != nil {
		t.Fatal(err)
	}
	if !e.base.ForOrg("org-2").Mailer().Enabled() {
		t.Fatal("a newly scoped Core did not see the platform-wide mail configuration")
	}
}

// TestLoadMailConfigFallsBackToTheBootDefault checks the boot-time relationship between the --smtp-*
// flags (SetMailerDefault) and whatever is stored in the database (LoadMailConfig): nothing saved yet
// must leave the flag-provided default in place, exactly like LoadSettings leaves DefaultSettings().
func TestLoadMailConfigFallsBackToTheBootDefault(t *testing.T) {
	e := newEnvBare(t)
	e.base.SetMailerDefault(MailConfig{Host: "flag-provided.example.com", Port: "25"})
	e.base.LoadMailConfig(e.ctx) // nothing saved yet
	if got := e.base.Mailer().Host; got != "flag-provided.example.com" {
		t.Fatalf("Mailer().Host = %q, want the flag-provided default", got)
	}

	if _, err := e.base.SaveMailConfig(e.ctx, "someone", MailConfig{Host: "saved.example.com", Port: "587"}); err != nil {
		t.Fatal(err)
	}
	// A fresh Core over the same store (the shape of a server restart) picks up what was saved, not the
	// old flag default.
	fresh := NewCore(e.st, nil, "", nil)
	fresh.SetMailerDefault(MailConfig{Host: "flag-provided.example.com", Port: "25"})
	fresh.LoadMailConfig(e.ctx)
	if got := fresh.Mailer().Host; got != "saved.example.com" {
		t.Fatalf("Mailer().Host after reload = %q, want the saved value", got)
	}
}

// TestMeReportsCanManageMailForTheDefaultOrgOwnerOnly checks the /auth/me flag Settings uses to decide
// whether to show the SMTP section at all, so it doesn't show it and then have the request 403.
func TestMeReportsCanManageMailForTheDefaultOrgOwnerOnly(t *testing.T) {
	a := newAdminRig(t)
	_, viewerCookie := a.user(t, "member-viewer", RoleViewer)
	if r := a.do("GET", "/api/v1/auth/me", nil, withCookie(viewerCookie)); r.json(t)["user"].(map[string]any)["canManageMail"] != false {
		t.Fatalf("viewer of the default org should not manage mail: %v", r.json(t))
	}
	_, ownerCookie := a.user(t, "org1-boss", RoleOwner)
	if r := a.do("GET", "/api/v1/auth/me", nil, withCookie(ownerCookie)); r.json(t)["user"].(map[string]any)["canManageMail"] != true {
		t.Fatalf("owner of the default org should manage mail: %v", r.json(t))
	}
}

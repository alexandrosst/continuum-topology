package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"continuum/internal/store"
)

// failAudit is a store whose audit trail can be made unwritable.
type failAudit struct {
	store.Store
	fail atomic.Bool
}

func (f *failAudit) AddAudit(ctx context.Context, e store.AuditEvent) error {
	if f.fail.Load() {
		return errors.New("disk full")
	}
	return f.Store.AddAudit(ctx, e)
}

func TestPrivilegedActionsFailClosedWhenTheAuditRowCannotBeWritten(t *testing.T) {
	a := newAdminRig(t)
	fs := &failAudit{Store: a.st}
	a.base.Store = fs // organisations created from now on use it
	_, owner := a.user(t, "boss", RoleOwner)
	edID, _ := a.user(t, "ed", RoleEditor)

	pending, _ := a.enroll(t, 2, fp)
	live, _ := a.enroll(t, 2, "11111111-2222-4333-8444-555566667777")
	if r := a.do("POST", "/api/v1/agents/"+live.AgentId+"/approve", map[string]any{"confirm": "11111111", "tier": 2}, withCookie(owner)); r.Code != 204 {
		t.Fatalf("approve (audit working): %d %s", r.Code, r.Body.String())
	}
	tokID := a.do("POST", "/api/v1/tokens", map[string]any{"name": "keep", "tier": 1}, withCookie(owner)).json(t)["meta"].(map[string]any)["id"].(string)

	fs.fail.Store(true)
	expect := func(what string, r resp) {
		t.Helper()
		if r.Code != 500 || !strings.Contains(r.Body.String(), "audit trail could not be written") {
			t.Errorf("%s: %d %s", what, r.Code, r.Body.String())
		}
	}
	expect("approve", a.do("POST", "/api/v1/agents/"+pending.AgentId+"/approve", map[string]any{"confirm": fp[:8], "tier": 2}, withCookie(owner)))
	expect("reject", a.do("POST", "/api/v1/agents/"+pending.AgentId+"/reject", map[string]any{"reason": "no"}, withCookie(owner)))
	expect("revoke", a.do("POST", "/api/v1/agents/"+live.AgentId+"/revoke", map[string]any{"reason": "no"}, withCookie(owner)))
	expect("create token", a.do("POST", "/api/v1/tokens", map[string]any{"name": "new", "tier": 1}, withCookie(owner)))
	expect("delete token", a.do("DELETE", "/api/v1/tokens/"+tokID, nil, withCookie(owner)))
	expect("role change", a.do("POST", "/api/v1/members/"+edID+"/role", map[string]any{"role": "admin"}, withCookie(owner)))
	expect("remove member", a.do("POST", "/api/v1/members/"+edID+"/remove", nil, withCookie(owner)))
	expect("create invite", a.do("POST", "/api/v1/invites", map[string]any{"role": "viewer"}, withCookie(owner)))
	expect("decider change", a.do("PUT", "/api/v1/settings", Settings{DeciderURL: "https://decider.example.com/x"}, withCookie(owner)))
	expect("rename", a.do("POST", "/api/v1/rename", map[string]any{"name": "Renamed"}, withCookie(owner)))
	expect("delete organisation", a.do("POST", "/api/v1/delete", map[string]any{"confirm": "Org One"}, withCookie(owner)))

	// Nothing happened.
	if ag, _ := a.st.GetAgent(a.ctx, pending.AgentId); ag.Status != store.StatusPending {
		t.Errorf("the pending agent changed state to %s", ag.Status)
	}
	if ag, _ := a.st.GetAgent(a.ctx, live.AgentId); ag.Status != store.StatusApproved {
		t.Errorf("the approved agent changed state to %s", ag.Status)
	}
	toks, _ := a.st.ListTokens(a.ctx, "org-1")
	if len(toks) != 3 { // the two enrolment tokens (used) and "keep"; none of the refused ones
		t.Errorf("tokens: %d", len(toks))
	}
	var haveKeep bool
	for _, tk := range toks {
		haveKeep = haveKeep || tk.ID == tokID
		if tk.Label == "new" {
			t.Error("a token was created without an audit row")
		}
	}
	if !haveKeep {
		t.Error("a token was deleted without an audit row")
	}
	if m, _ := a.st.GetMembership(a.ctx, "org-1", edID); m.Role != RoleEditor {
		t.Errorf("the role changed to %q", m.Role)
	}
	if o, err := a.st.GetOrg(a.ctx, "org-1"); err != nil || o.Name != "Org One" {
		t.Errorf("the organisation was changed or deleted: %v %+v", err, o)
	}
	if data, _ := a.st.GetSettings(a.ctx, "org-1"); data != nil && strings.Contains(string(data), "decider.example.com") {
		t.Error("the decider was saved without an audit row")
	}

	// With the trail writable again, the same requests work.
	fs.fail.Store(false)
	if r := a.do("POST", "/api/v1/agents/"+pending.AgentId+"/reject", map[string]any{"reason": "no"}, withCookie(owner)); r.Code != 204 {
		t.Errorf("reject once the trail works: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/agents/"+live.AgentId+"/revoke", map[string]any{"reason": "done"}, withCookie(owner)); r.Code != 204 {
		t.Errorf("revoke once the trail works: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: "https://decider.example.com/x"}, withCookie(owner)); r.Code != 200 {
		t.Errorf("decider once the trail works: %d %s", r.Code, r.Body.String())
	}
}

func TestAFailedActionAfterItsAuditRowLeavesAFollowUpRow(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, fp)
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, "first"); err != nil {
		t.Fatal(err)
	}
	// a second rejection is refused by the store after the row was written: the trail must say so
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, "again"); kindOf(err) != KindConflict {
		t.Fatalf("second reject: %v", err)
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 10)
	var failed bool
	for _, ev := range evs {
		failed = failed || ev.Action == "agent-rejected-failed"
	}
	if !failed {
		t.Fatalf("no follow-up row: %+v", evs)
	}
}

func TestStateHidesTheAuditTrailFromViewersAndEditors(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "adm", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	_, viewer := a.user(t, "vw", RoleViewer)
	a.do("POST", "/api/v1/tokens", map[string]any{"name": "edge", "tier": 1}, withCookie(admin)) // leaves a row naming "adm"

	rows := func(cookie string) []any {
		r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
		if r.Code != 200 {
			t.Fatalf("state: %d", r.Code)
		}
		if strings.Contains(r.Body.String(), `"auditLog":null`) {
			t.Fatal("the UI expects an array, not null")
		}
		return r.json(t)["auditLog"].([]any)
	}
	if n := len(rows(admin)); n == 0 {
		t.Fatal("an administrator gets no audit rows")
	}
	for name, c := range map[string]string{"viewer": viewer, "editor": editor} {
		if got := rows(c); len(got) != 0 {
			t.Errorf("a %s received %d audit rows: %v", name, len(got), got)
		}
		if body := a.do("GET", "/api/v1/state", nil, withCookie(c)).Body.String(); strings.Contains(body, "adm") && strings.Contains(body, "token-created") {
			t.Errorf("a %s can read the audit trail: %s", name, body)
		}
	}
}

func TestLoginAddressStaysInTheServerTrailNotTheOrganisations(t *testing.T) {
	a := newAdminRig(t)
	id, _ := a.user(t, "alex", RoleAdmin)
	// alex also belongs to a second organisation
	other := store.Org{ID: "org-2", Name: "Two", CreatedAt: *a.now, CreatedBy: id}
	if err := a.st.CreateOrg(a.ctx, other, id); err != nil {
		t.Fatal(err)
	}
	r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, fromIP("203.0.113.77"))
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, org := range []string{"org-1", "org-2"} {
		evs, _ := a.st.ListAudit(a.ctx, org, 50)
		var saw bool
		for _, e := range evs {
			if e.Action != "login" {
				continue
			}
			saw = true
			if strings.Contains(e.Detail, "203.0.113.77") || strings.Contains(e.Detail, "from") {
				t.Errorf("%s: the address reached an organisation's trail: %+v", org, e)
			}
		}
		if !saw && org == "org-2" {
			t.Errorf("%s: the login event itself must still be recorded", org)
		}
	}
	server, _ := a.st.ListAudit(a.ctx, "", 50)
	var found bool
	for _, e := range server {
		found = found || (e.Action == "login" && strings.Contains(e.Detail, "203.0.113.77"))
	}
	if !found {
		t.Errorf("the server's own trail should keep the address: %+v", server)
	}
	// and no organisation's members can read the server trail
	if _, err := a.base.Store.ListAudit(a.ctx, "org-1", 500); err != nil {
		t.Fatal(err)
	}
}

func TestRejectAndRevokeReasonsAreClippedEverywhere(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("é", 1000) + "\n\x00" + strings.Repeat("x", 5000)
	resp, _ := e.enroll(t, 2, fp)
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, long); err != nil {
		t.Fatal(err)
	}
	ag, _ := e.st.GetAgent(e.ctx, resp.AgentId)
	if len(ag.Reason) > maxReason {
		t.Fatalf("stored reason is %d bytes", len(ag.Reason))
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 10)
	for _, ev := range evs {
		if ev.Action != "agent-rejected" {
			continue
		}
		if len(ev.Detail) > maxReason || strings.ContainsAny(ev.Detail, "\n\x00") {
			t.Fatalf("audit detail: %d bytes %q", len(ev.Detail), ev.Detail)
		}
		if !strings.HasPrefix(ev.Detail, "é") || strings.ContainsRune(ev.Detail, '�') {
			t.Fatalf("a character was split: %q", ev.Detail[:20])
		}
	}
	// Any other audit field is bounded too.
	e.core.audit(e.ctx, strings.Repeat("a", 999), "act", "k", strings.Repeat("i", 999), strings.Repeat("d", 99999))
	evs, _ = e.st.ListAudit(e.ctx, "org-1", 1)
	if len(evs[0].Actor) > maxAuditActor || len(evs[0].TargetID) > maxAuditTarget || len(evs[0].Detail) > maxAuditDetail {
		t.Fatalf("unbounded audit row: %d %d %d", len(evs[0].Actor), len(evs[0].TargetID), len(evs[0].Detail))
	}
}

func TestEveryAuditRowThroughTheServerJoinsTheChain(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "adm", RoleAdmin)
	a.do("POST", "/api/v1/tokens", map[string]any{"name": "edge", "tier": 1}, withCookie(admin))
	a.do("POST", "/api/v1/auth/login", map[string]string{"username": "adm", "password": "wrong wrong wrong"})
	r, err := a.st.VerifyAudit(a.ctx)
	if err != nil || !r.OK || r.Chained < 3 || r.Unchained != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// The writes that carry their own audit row (store.WithAudit) do not go through AddAudit, so the same
// failure is injected where they would meet it: the store reports that the row could not be written
// and has changed nothing. That the store really changes nothing is proven in the store's own tests.
// broken reports whether a write that carries its own audit row should meet the failure.
func (f *failAudit) broken(opts []store.WriteOption) error {
	if f.fail.Load() && len(opts) > 0 {
		return fmt.Errorf("%w: disk full", store.ErrAuditWrite)
	}
	return nil
}

func (f *failAudit) CreateToken(ctx context.Context, t store.Token, hash []byte, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.CreateToken(ctx, t, hash, opts...)
}

func (f *failAudit) DeleteToken(ctx context.Context, org, id string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.DeleteToken(ctx, org, id, opts...)
}

func (f *failAudit) RejectAgent(ctx context.Context, id, reason string, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RejectAgent(ctx, id, reason, now, opts...)
}

func (f *failAudit) CreateInvite(ctx context.Context, inv store.Invite, hash []byte, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.CreateInvite(ctx, inv, hash, opts...)
}

func (f *failAudit) RevokeInvite(ctx context.Context, org, id string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RevokeInvite(ctx, org, id, opts...)
}

func (f *failAudit) SetMemberRole(ctx context.Context, org, userID, role string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.SetMemberRole(ctx, org, userID, role, opts...)
}

func (f *failAudit) RemoveMember(ctx context.Context, org, userID string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RemoveMember(ctx, org, userID, opts...)
}

func (f *failAudit) RenameOrg(ctx context.Context, id, name string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RenameOrg(ctx, id, name, opts...)
}

func (f *failAudit) DeleteOrg(ctx context.Context, id string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.DeleteOrg(ctx, id, opts...)
}

func (f *failAudit) ApproveAgent(ctx context.Context, id string, tier int, approver, clusterID string, leaf []byte, notAfter, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.ApproveAgent(ctx, id, tier, approver, clusterID, leaf, notAfter, now, opts...)
}

func (f *failAudit) RevokeAgent(ctx context.Context, id, reason string, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RevokeAgent(ctx, id, reason, now, opts...)
}

func (f *failAudit) SetAccessTier(ctx context.Context, id string, tier int, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.SetAccessTier(ctx, id, tier, opts...)
}

func (f *failAudit) SaveSnapshot(ctx context.Context, agentID string, data []byte, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.SaveSnapshot(ctx, agentID, data, now, opts...)
}

func (f *failAudit) CreateOperator(ctx context.Context, op store.Operator, tokenHash []byte, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.CreateOperator(ctx, op, tokenHash, opts...)
}

func (f *failAudit) UpdateOperatorScope(ctx context.Context, id string, ids []string, dest store.Destination, m []store.Modality, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.UpdateOperatorScope(ctx, id, ids, dest, m, opts...)
}

func (f *failAudit) SetOperatorAddress(ctx context.Context, id, address string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.SetOperatorAddress(ctx, id, address, opts...)
}

func (f *failAudit) SetOperatorAddressIfEmpty(ctx context.Context, id, address string, opts ...store.WriteOption) (bool, error) {
	if err := f.broken(opts); err != nil {
		return false, err
	}
	return f.Store.SetOperatorAddressIfEmpty(ctx, id, address, opts...)
}

func (f *failAudit) RevokeOperator(ctx context.Context, id, reason string, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RevokeOperator(ctx, id, reason, now, opts...)
}

func (f *failAudit) DeleteOperator(ctx context.Context, id string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.DeleteOperator(ctx, id, opts...)
}

func (f *failAudit) SetOperatorHeartbeat(ctx context.Context, id string, hash []byte, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.SetOperatorHeartbeat(ctx, id, hash, now, opts...)
}

func (f *failAudit) ReissueOperatorInstall(ctx context.Context, id string, in store.OperatorReissue, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.ReissueOperatorInstall(ctx, id, in, opts...)
}

func (f *failAudit) CreateTelemetryIntent(ctx context.Context, ti store.TelemetryIntent, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.CreateTelemetryIntent(ctx, ti, opts...)
}

func (f *failAudit) UpdateTelemetryIntentScope(ctx context.Context, id string, ns, exc []string, sig []store.SignalGrant, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.UpdateTelemetryIntentScope(ctx, id, ns, exc, sig, opts...)
}

func (f *failAudit) UpdateTelemetryIntentDestination(ctx context.Context, id string, dest store.Destination, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.UpdateTelemetryIntentDestination(ctx, id, dest, opts...)
}

func (f *failAudit) UpdateTelemetryIntentDestinations(ctx context.Context, id string, dest store.Destination, routes map[store.Modality]store.Destination, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.UpdateTelemetryIntentDestinations(ctx, id, dest, routes, opts...)
}

func (f *failAudit) RevokeTelemetryIntent(ctx context.Context, id, reason string, now time.Time, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.RevokeTelemetryIntent(ctx, id, reason, now, opts...)
}

func (f *failAudit) DeleteTelemetryIntent(ctx context.Context, id string, opts ...store.WriteOption) error {
	if err := f.broken(opts); err != nil {
		return err
	}
	return f.Store.DeleteTelemetryIntent(ctx, id, opts...)
}

func TestPrivilegedActionsFailClosedWhenTheAuditRowCannotBeWritten(t *testing.T) {
	a := newAdminRig(t)
	fs := &failAudit{Store: a.st}
	a.base.Store = fs // organisations created from now on use it
	_, owner := a.user(t, "boss", RoleOwner)
	edID, _ := a.user(t, "ed", RoleEditor)

	pending, _ := a.enroll(t, 2, fp)
	live, _ := a.enroll(t, 2, "11111111-2222-4333-8444-555566667777")
	if r := a.do("POST", "/api/v1/agents/"+live.AgentId+"/approve", map[string]any{"confirm": a.codes[live.AgentId], "tier": 2}, withCookie(owner)); r.Code != 204 {
		t.Fatalf("approve (audit working): %d %s", r.Code, r.Body.String())
	}
	tokID := a.do("POST", "/api/v1/tokens", map[string]any{"name": "keep", "tier": 1}, withCookie(owner)).json(t)["meta"].(map[string]any)["id"].(string)

	// The main organisation cannot be deleted at all, so the deletion is tried on another one.
	carol, carolOrg := a.register(t, "carol", "Carol Co")

	fs.fail.Store(true)
	expect := func(what string, r resp) {
		t.Helper()
		if r.Code != 500 || !strings.Contains(r.Body.String(), "audit trail could not be written") {
			t.Errorf("%s: %d %s", what, r.Code, r.Body.String())
		}
	}
	expect("approve", a.do("POST", "/api/v1/agents/"+pending.AgentId+"/approve", map[string]any{"confirm": a.codes[pending.AgentId], "tier": 2}, withCookie(owner)))
	expect("reject", a.do("POST", "/api/v1/agents/"+pending.AgentId+"/reject", map[string]any{"reason": "no"}, withCookie(owner)))
	expect("revoke", a.do("POST", "/api/v1/agents/"+live.AgentId+"/revoke", map[string]any{"reason": "no"}, withCookie(owner)))
	expect("create token", a.do("POST", "/api/v1/tokens", map[string]any{"name": "new", "tier": 1}, withCookie(owner)))
	expect("delete token", a.do("DELETE", "/api/v1/tokens/"+tokID, nil, withCookie(owner)))
	expect("role change", a.do("POST", "/api/v1/members/"+edID+"/role", map[string]any{"role": "admin"}, withCookie(owner)))
	expect("remove member", a.do("POST", "/api/v1/members/"+edID+"/remove", nil, withCookie(owner)))
	expect("create invite", a.do("POST", "/api/v1/invites", map[string]any{"role": "viewer"}, withCookie(owner)))
	expect("decider change", a.do("PUT", "/api/v1/settings", Settings{DeciderURL: "https://decider.example.com/x"}, withCookie(owner)))
	expect("rename", a.do("POST", "/api/v1/rename", map[string]any{"name": "Renamed"}, withCookie(owner)))
	expect("delete organisation", a.do("POST", org(carolOrg, "delete"), map[string]any{"confirm": "Carol Co"}, withCookie(carol)))

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
	if _, err := a.st.GetOrg(a.ctx, carolOrg); err != nil {
		t.Errorf("an organisation was deleted without an audit row: %v", err)
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
	// the action is refused after the row was written: the trail must say so
	err := e.core.audited(e.ctx, "alex", "note", "agent", "a-1", "about to", func() error {
		return errf(KindConflict, "nothing to do")
	})
	if kindOf(err) != KindConflict {
		t.Fatalf("the action's own error must reach the caller: %v", err)
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 10)
	var claim, failed bool
	for _, ev := range evs {
		claim = claim || ev.Action == "note"
		if ev.Action == "note-failed" {
			failed = strings.Contains(ev.Detail, "nothing to do")
		}
	}
	if !claim || !failed {
		t.Fatalf("claim=%v follow-up=%v: %+v", claim, failed, evs)
	}
}

// A write that carries its own audit row records only what happened. A refused attempt changes
// nothing, so it leaves nothing: no claim, and no follow-up row either.
func TestARefusedWriteWithItsOwnAuditRowLeavesNoRow(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, fp)
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, "first"); err != nil {
		t.Fatal(err)
	}
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, "again"); kindOf(err) != KindConflict {
		t.Fatalf("second reject: %v", err)
	}
	other, _ := e.enroll(t, 2, "11111111-2222-4333-8444-555566667777")
	if err := e.core.Revoke(e.ctx, "alex", other.AgentId, "x"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := e.core.Revoke(e.ctx, "alex", other.AgentId, "again"); kindOf(err) != KindConflict {
		t.Fatalf("second revoke: %v", err)
	}
	if err := e.core.DeleteToken(e.ctx, "alex", "tk-nope"); kindOf(err) != KindNotFound {
		t.Fatalf("delete of a missing token: %v", err)
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 20)
	var rejected int
	for _, ev := range evs {
		if strings.HasPrefix(ev.Action, "agent-rejected") {
			rejected++
			if ev.Action != "agent-rejected" || ev.Detail != "first" {
				t.Errorf("unexpected row %+v", ev)
			}
		}
		if ev.Action == "agent-revoked" && ev.Detail != "x" {
			t.Errorf("a refused revocation left a row: %+v", ev)
		}
		if ev.Action == "token-deleted" || strings.HasSuffix(ev.Action, "-failed") {
			t.Errorf("a refused attempt left a row: %+v", ev)
		}
	}
	if rejected != 1 {
		t.Errorf("want exactly one agent-rejected row, got %d: %+v", rejected, evs)
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

// The follow-up row is written after the action failed, which is exactly when the request that
// started it is often gone (the client hung up, or its deadline ran out and that is why the action
// failed). The row must still be written, or the trail keeps a claim with no outcome.
func TestTheFollowUpRowIsWrittenEvenWhenTheRequestWasCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	err := e.core.audited(ctx, "alex", "note", "agent", "a-1", "about to", func() error {
		cancel() // the caller goes away while the action is running
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the action's own error must reach the caller: %v", err)
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 10)
	var claim, outcome bool
	for _, ev := range evs {
		claim = claim || ev.Action == "note"
		outcome = outcome || ev.Action == "note-failed"
	}
	if !claim || !outcome {
		t.Fatalf("claim=%v outcome=%v; rows: %+v", claim, outcome, evs)
	}
}

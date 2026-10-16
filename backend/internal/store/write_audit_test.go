package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func auditEv(action, id string) AuditEvent {
	return AuditEvent{At: time.Now(), OrgID: "org-1", Actor: "tester", Action: action, TargetKind: "token", TargetID: id}
}

func tokenFor(id string) Token {
	now := time.Now()
	return Token{ID: id, OrgID: "org-1", Label: "t", AccessTier: 1, CreatedBy: "tester", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func auditActions(t *testing.T, s *SQLite) []string {
	t.Helper()
	evs, err := s.ListAudit(context.Background(), "org-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Action)
	}
	return out
}

func TestAWriteAndItsAuditRowAreOneTransaction(t *testing.T) {
	s, ctx, _ := openAccounts(t)

	if err := s.CreateToken(ctx, tokenFor("tk-1"), []byte("h1"), WithAudit(auditEv("token-created", "tk-1"))); err != nil {
		t.Fatal(err)
	}
	if got := auditActions(t, s); len(got) != 1 || got[0] != "token-created" {
		t.Fatalf("rows after create: %v", got)
	}
	if err := s.DeleteToken(ctx, "org-1", "tk-1", WithAudit(auditEv("token-deleted", "tk-1"))); err != nil {
		t.Fatal(err)
	}

	// refused writes leave no row: nothing happened
	if err := s.DeleteToken(ctx, "org-1", "tk-1", WithAudit(auditEv("token-deleted", "tk-1"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if got := auditActions(t, s); len(got) != 2 {
		t.Fatalf("rows after a refused delete: %v", got)
	}

	// the chain made of such rows verifies
	rep, err := s.VerifyAudit(ctx)
	if err != nil || !rep.OK || rep.Chained != 2 {
		t.Fatalf("chain: %+v %v", rep, err)
	}
}

func TestIfTheAuditRowCannotBeWrittenTheWriteIsNotMade(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.CreateToken(ctx, tokenFor("tk-2"), []byte("h2"), WithAudit(auditEv("token-created", "tk-2")))
	if !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("want ErrAuditWrite, got %v", err)
	}
	if toks, _ := s.ListTokens(ctx, "org-1"); len(toks) != 0 {
		t.Fatalf("the token was stored although its audit row could not be written: %+v", toks)
	}

	// and without the option the write does not care about the trail, as before
	if err := s.CreateToken(ctx, tokenFor("tk-3"), []byte("h3")); err != nil {
		t.Fatalf("plain write: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER no_audit`); err != nil {
		t.Fatal(err)
	}
	// deleting while the trail is broken must keep the token too
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, "org-1", "tk-3", WithAudit(auditEv("token-deleted", "tk-3"))); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("delete: %v", err)
	}
	if toks, _ := s.ListTokens(ctx, "org-1"); len(toks) != 1 {
		t.Fatalf("the token was deleted although its audit row could not be written: %+v", toks)
	}
}

func TestOrganisationAndMembershipWritesRollBackWithTheirAuditRow(t *testing.T) {
	s, ctx, now := openAccounts(t)
	mustUser(t, s, "u1", "quinn", now)
	mustUser(t, s, "u2", "robin", now)
	if err := s.CreateOrg(ctx, Org{ID: "o1", Name: "Athens", CreatedAt: now, CreatedBy: "u1"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, Membership{OrgID: "o1", UserID: "u2", Role: "viewer", CreatedAt: now, AddedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	ev := func(a string) WriteOption { return WithAudit(AuditEvent{OrgID: "o1", Actor: "tester", Action: a}) }

	// with the trail healthy, the change and its row arrive together
	if err := s.SetMemberRole(ctx, "o1", "u2", "editor", ev("member-role-changed")); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameOrg(ctx, "o1", "Athens 2", ev("org-renamed")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, "o1", "u3", ev("member-removed")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing a stranger: %v", err)
	}
	if rows, _ := s.ListAudit(ctx, "o1", 10); len(rows) != 2 {
		t.Fatalf("rows: %+v (a refused removal must leave none)", rows)
	}

	// with the trail broken, none of them changes anything, and deleting the organisation keeps its data
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"role":   s.SetMemberRole(ctx, "o1", "u2", "admin", ev("x")),
		"rename": s.RenameOrg(ctx, "o1", "Gone", ev("x")),
		"remove": s.RemoveMember(ctx, "o1", "u2", ev("x")),
		"delete": s.DeleteOrg(ctx, "o1", ev("x")),
		"invite": s.CreateInvite(ctx, Invite{ID: "inv-1", OrgID: "o1", Role: "viewer", CreatedBy: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("h"), ev("x")),
	} {
		if !errors.Is(err, ErrAuditWrite) {
			t.Errorf("%s: %v, want ErrAuditWrite", name, err)
		}
	}
	o, err := s.GetOrg(ctx, "o1")
	if err != nil || o.Name != "Athens 2" {
		t.Fatalf("the organisation changed although the trail was broken: %+v %v", o, err)
	}
	if m, err := s.GetMembership(ctx, "o1", "u2"); err != nil || m.Role != "editor" {
		t.Fatalf("the membership changed although the trail was broken: %+v %v", m, err)
	}
	if invs, _ := s.ListInvites(ctx, "o1"); len(invs) != 0 {
		t.Fatalf("an invitation was stored without its audit row: %+v", invs)
	}
}

func TestAgentLifecycleWritesRollBackWithTheirAuditRow(t *testing.T) {
	s, ctx, now := openAccounts(t)
	leafExp := now.Add(24 * time.Hour)
	enroll(t, s, "tk-1", "ag-1", "fp-a", now)
	ev := func(a string) WriteOption {
		return WithAudit(AuditEvent{OrgID: "o1", Actor: "tester", Action: a, TargetKind: "agent", TargetID: "ag-1"})
	}
	rows := func() int { r, _ := s.ListAudit(ctx, "o1", 50); return len(r) }

	// a refused approval (already handled) leaves no row; a good one leaves exactly one
	if err := s.ApproveAgent(ctx, "ag-1", 2, "alex", "cl-1", []byte("leaf"), leafExp, now, ev("agent-approved")); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAgent(ctx, "ag-1", 2, "alex", "cl-1", []byte("leaf"), leafExp, now, ev("agent-approved")); !errors.Is(err, ErrBadState) {
		t.Fatalf("second approval: %v", err)
	}
	if rows() != 1 {
		t.Fatalf("rows = %d, want 1", rows())
	}

	// with the trail broken nothing about the agent changes
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccessTier(ctx, "ag-1", 1, ev("agent-tier-changed")); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("tier: %v", err)
	}
	if err := s.SaveSnapshot(ctx, "ag-1#consent", []byte("{}"), now, ev("agent-consent-changed")); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("consent: %v", err)
	}
	if err := s.RevokeAgent(ctx, "ag-1", "no", now, ev("agent-revoked")); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("revoke: %v", err)
	}
	a, _ := s.GetAgent(ctx, "ag-1")
	if a.Status != StatusApproved || a.AccessTier != 2 {
		t.Fatalf("the agent changed although the trail was broken: %+v", a)
	}
	if _, _, err := s.LoadSnapshot(ctx, "ag-1#consent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the consent was saved without its audit row: %v", err)
	}

	// enrol a second pending agent: approving it while the trail is broken leaves it pending
	enroll(t, s, "tk-2", "ag-2", "fp-b", now)
	if err := s.ApproveAgent(ctx, "ag-2", 1, "alex", "cl-2", []byte("leaf"), leafExp, now, ev("agent-approved")); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("approve: %v", err)
	}
	if b, _ := s.GetAgent(ctx, "ag-2"); b.Status != StatusPending {
		t.Fatalf("the agent was approved without its audit row: %s", b.Status)
	}
}

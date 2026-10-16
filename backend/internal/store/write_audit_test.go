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

func TestOperatorWritesRollBackWithTheirAuditRow(t *testing.T) {
	s, ctx, now := openAccounts(t)
	mk := func(id string, recv ReceiverAuth) Operator {
		return Operator{ID: id, OrgID: "o1", Name: id, Status: OperatorActive, ReceiverAuth: recv, CreatedBy: "alex", CreatedAt: now}
	}
	ev := func(a string) WriteOption {
		return WithAudit(AuditEvent{OrgID: "o1", Actor: "tester", Action: a, TargetKind: "operator"})
	}
	rows := func() int { r, _ := s.ListAudit(ctx, "o1", 50); return len(r) }

	if err := s.CreateOperator(ctx, mk("op-1", ReceiverAuthMTLS), nil, ev("operator-created")); err != nil {
		t.Fatal(err)
	}
	// the same id again is refused by the database: no second claim
	if err := s.CreateOperator(ctx, mk("op-1", ReceiverAuthMTLS), nil, ev("operator-created")); err == nil {
		t.Fatal("a duplicate operator was stored")
	}
	if rows() != 1 {
		t.Fatalf("rows = %d, want 1 (a refused creation must leave none)", rows())
	}

	// compare-and-set: the first writer wins and leaves a row; the loser changes nothing and leaves none
	if ok, err := s.SetOperatorAddressIfEmpty(ctx, "op-1", "a.example:443", ev("operator-address-changed")); err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := s.SetOperatorAddressIfEmpty(ctx, "op-1", "b.example:443", ev("operator-address-changed")); err != nil || ok {
		t.Fatalf("second: %v %v", ok, err)
	}
	if op, _ := s.GetOperator(ctx, "op-1"); op.Address != "a.example:443" {
		t.Fatalf("address = %q", op.Address)
	}
	if rows() != 2 {
		t.Fatalf("rows = %d, want 2 (the lost race must leave none)", rows())
	}

	// a reissue is one step: if its second part is refused, the first is undone too
	if err := s.CreateOperator(ctx, mk("op-2", ReceiverAuthMTLS), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOperator(ctx, "op-2", "gone", now); err != nil {
		t.Fatal(err)
	}
	err := s.ReissueOperatorInstall(ctx, "op-1", OperatorReissue{ReceiverNotAfter: now.Add(time.Hour), ClientNotAfter: now.Add(time.Hour), HeartbeatHash: []byte("hb"), Now: now}, ev("operator-install-reissued"))
	if err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if rows() != 3 {
		t.Fatalf("rows = %d, want 3", rows())
	}
	if err := s.ReissueOperatorInstall(ctx, "op-2", OperatorReissue{ReceiverNotAfter: now, ClientNotAfter: now}, ev("operator-install-reissued")); !errors.Is(err, ErrBadState) {
		t.Fatalf("reissue of a revoked operator: %v", err)
	}
	if rows() != 3 {
		t.Fatalf("a refused reissue left a row: %d", rows())
	}

	// with the trail broken, nothing about the operator changes
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"create":    s.CreateOperator(ctx, mk("op-3", ReceiverAuthMTLS), nil, ev("x")),
		"scope":     s.UpdateOperatorScope(ctx, "op-1", []string{"c1"}, Destination{}, nil, ev("x")),
		"address":   s.SetOperatorAddress(ctx, "op-1", "z.example:1", ev("x")),
		"heartbeat": s.SetOperatorHeartbeat(ctx, "op-1", []byte("new"), now, ev("x")),
		"revoke":    s.RevokeOperator(ctx, "op-1", "no", now, ev("x")),
		"delete":    s.DeleteOperator(ctx, "op-1", ev("x")),
	} {
		if !errors.Is(err, ErrAuditWrite) {
			t.Errorf("%s: %v, want ErrAuditWrite", name, err)
		}
	}
	op, err := s.GetOperator(ctx, "op-1")
	if err != nil || op.Status != OperatorActive || op.Address != "a.example:443" || len(op.SourceClusterIDs) != 0 {
		t.Fatalf("the operator changed although the trail was broken: %+v %v", op, err)
	}
	if _, err := s.GetOperator(ctx, "op-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an operator was stored without its audit row: %v", err)
	}
}

func TestTelemetryIntentWritesRollBackWithTheirAuditRow(t *testing.T) {
	s, ctx, now := openAccounts(t)
	ev := func(a string) WriteOption {
		return WithAudit(AuditEvent{OrgID: "o1", Actor: "tester", Action: a, TargetKind: "telemetry-intent"})
	}
	rows := func() int { r, _ := s.ListAudit(ctx, "o1", 50); return len(r) }
	ti := TelemetryIntent{ID: "ti-1", OrgID: "o1", AgentID: "ag-1", Name: "n", Status: TelemetryIntentActive, CreatedBy: "alex", CreatedAt: now}

	if err := s.CreateTelemetryIntent(ctx, ti, ev("telemetry-intent-created")); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeTelemetryIntent(ctx, "ti-1", "done", now, ev("telemetry-intent-revoked")); err != nil {
		t.Fatal(err)
	}
	// revoking again, scope change on a revoked one, and deleting a missing one are refused and leave nothing
	if err := s.RevokeTelemetryIntent(ctx, "ti-1", "again", now, ev("telemetry-intent-revoked")); !errors.Is(err, ErrBadState) {
		t.Fatalf("second revoke: %v", err)
	}
	if err := s.UpdateTelemetryIntentScope(ctx, "ti-1", nil, nil, nil, ev("telemetry-intent-scope-changed")); !errors.Is(err, ErrBadState) {
		t.Fatalf("scope of a revoked intent: %v", err)
	}
	if err := s.DeleteTelemetryIntent(ctx, "ti-none", ev("telemetry-intent-deleted")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of a missing intent: %v", err)
	}
	if rows() != 2 {
		t.Fatalf("rows = %d, want 2", rows())
	}

	// with the trail broken, nothing changes
	live := TelemetryIntent{ID: "ti-2", OrgID: "o1", AgentID: "ag-2", Name: "m", Status: TelemetryIntentActive, CreatedBy: "alex", CreatedAt: now}
	if err := s.CreateTelemetryIntent(ctx, live); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"create":       s.CreateTelemetryIntent(ctx, TelemetryIntent{ID: "ti-3", OrgID: "o1", AgentID: "ag-3", Name: "x", Status: TelemetryIntentActive, CreatedBy: "a", CreatedAt: now}, ev("x")),
		"scope":        s.UpdateTelemetryIntentScope(ctx, "ti-2", []string{"ns"}, nil, nil, ev("x")),
		"destination":  s.UpdateTelemetryIntentDestination(ctx, "ti-2", Destination{}, ev("x")),
		"destinations": s.UpdateTelemetryIntentDestinations(ctx, "ti-2", Destination{}, nil, ev("x")),
		"revoke":       s.RevokeTelemetryIntent(ctx, "ti-2", "no", now, ev("x")),
		"delete":       s.DeleteTelemetryIntent(ctx, "ti-2", ev("x")),
	} {
		if !errors.Is(err, ErrAuditWrite) {
			t.Errorf("%s: %v, want ErrAuditWrite", name, err)
		}
	}
	got, err := s.GetTelemetryIntent(ctx, "ti-2")
	if err != nil || got.Status != TelemetryIntentActive || len(got.Namespaces) != 0 {
		t.Fatalf("the intent changed although the trail was broken: %+v %v", got, err)
	}
	if _, err := s.GetTelemetryIntent(ctx, "ti-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an intent was stored without its audit row: %v", err)
	}
}

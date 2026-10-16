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

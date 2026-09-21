package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func addRows(t *testing.T, s *SQLite, n int, from int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.AddAudit(context.Background(), AuditEvent{At: time.Now(), OrgID: "org-1", Actor: "alex", Action: "act", TargetKind: "k", TargetID: "t", Detail: strings.Repeat("d", from+i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditChainVerifiesAndPinpointsTheFirstBrokenLink(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, err := s.VerifyAudit(ctx); err != nil || !r.OK || r.Rows != 0 {
		t.Fatalf("an empty trail: %+v %v", r, err)
	}
	addRows(t, s, 6, 1)
	r, err := s.VerifyAudit(ctx)
	if err != nil || !r.OK || r.Chained != 6 || r.Head == "" {
		t.Fatalf("intact trail: %+v %v", r, err)
	}

	tamper := func(name, sql string, wantAt int64) {
		t.Helper()
		// work on a copy of the state by re-adding after each case: use a fresh database
		d, _ := OpenSQLite(filepath.Join(t.TempDir(), name+".db"))
		defer d.Close()
		addRows(t, d, 6, 1)
		if _, err := d.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
		r, err := d.VerifyAudit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r.OK || r.BrokenAt != wantAt {
			t.Fatalf("%s: %+v (want first break at %d)", name, r, wantAt)
		}
	}
	tamper("edit", `UPDATE audit SET detail='forged' WHERE id=3`, 3)
	tamper("actor", `UPDATE audit SET actor='mallory' WHERE id=2`, 2)
	tamper("delete-middle", `DELETE FROM audit WHERE id=4`, 5)
	tamper("delete-tail", `DELETE FROM audit WHERE id=6`, 6)
	tamper("clear-hash", `UPDATE audit SET hash=NULL WHERE id=4`, 4)
	tamper("swap-order", `UPDATE audit SET id=100 WHERE id=2; UPDATE audit SET id=2 WHERE id=3; UPDATE audit SET id=3 WHERE id=100`, 2)
	tamper("delete-first", `DELETE FROM audit WHERE id=1`, 2)
}

func TestAuditChainStartsAtTheFirstNewRowOfAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	addRows(t, s, 3, 1)
	// Turn it into a database written before the chain existed: no hash column, user_version 1.
	if _, err := s.db.Exec(`ALTER TABLE audit DROP COLUMN hash`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = OpenSQLite(path) // migrates
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := s.ListAudit(ctx, "org-1", 10)
	if len(old) != 3 {
		t.Fatalf("old rows must keep working: %d", len(old))
	}
	if r, _ := s.VerifyAudit(ctx); !r.OK || r.Unchained != 3 || r.Chained != 0 {
		t.Fatalf("legacy rows only: %+v", r)
	}
	addRows(t, s, 2, 10)
	r, err := s.VerifyAudit(ctx)
	if err != nil || !r.OK || r.Unchained != 3 || r.Chained != 2 {
		t.Fatalf("after two new rows: %+v %v", r, err)
	}
	// Editing a legacy row cannot be detected (it was never hashed) but must not break the chain either.
	if _, err := s.db.Exec(`UPDATE audit SET detail='x' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.VerifyAudit(ctx); !r.OK {
		t.Fatalf("legacy rows are outside the chain: %+v", r)
	}
	// Reopening is idempotent.
	s.Close()
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, _ := s.VerifyAudit(ctx); !r.OK || r.Chained != 2 {
		t.Fatalf("after reopening: %+v", r)
	}
}

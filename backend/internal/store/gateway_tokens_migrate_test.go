package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A schema-18 database still has the table of quick-start gateway tokens. Opening it drops the table and keeps everything else: the
// audit trail and its hash chain, and the other organisation data.
func TestSchemaEighteenLosesTheGatewayTokenTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, org := range []string{"o1", "o2"} {
		if err := st.AddAudit(ctx, AuditEvent{At: now, OrgID: org, Actor: "alex", Action: "x", TargetKind: "org", TargetID: org}); err != nil {
			t.Fatal(err)
		}
	}
	// Make it a database written before the table was dropped: the table with a row in it, and version 18.
	for _, q := range []string{
		`CREATE TABLE gateway_tokens (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, backend_id TEXT NOT NULL, secret_hash BLOB NOT NULL, created_by TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE INDEX gateway_tokens_backend ON gateway_tokens(org_id, backend_id)`,
		`INSERT INTO gateway_tokens VALUES ('gwt-1','o1','qsb-1',x'00',  'alex', 1, 2)`,
		`PRAGMA user_version = 18`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 18 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	var n int
	if err := up.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'gateway_tokens%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d gateway token objects are left (%v)", n, err)
	}
	if rep, err := up.VerifyAudit(ctx); err != nil || !rep.OK || rep.Rows != 2 {
		t.Fatalf("the audit chain: %+v %v", rep, err)
	}
	// A new database never has the table at all.
	fresh, err := OpenSQLite(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'gateway_tokens%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a new database has %d gateway token objects (%v)", n, err)
	}
}

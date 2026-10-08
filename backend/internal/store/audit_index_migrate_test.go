package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A schema-17 database (no audit index) opens, gets the index, keeps its audit rows and their hash chain, and
// ListAudit reads through the index.
func TestSchemaSeventeenGainsTheAuditIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, org := range []string{"o1", "o2", "o1"} {
		if err := st.AddAudit(ctx, AuditEvent{At: now, OrgID: org, Actor: "alex", Action: "x", TargetKind: "org", TargetID: org}); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DROP INDEX audit_org`, `PRAGMA user_version = 17`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 17 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	if got, err := up.ListAudit(ctx, "o1", 10); err != nil || len(got) != 2 {
		t.Fatalf("o1 audit rows: %v %v", got, err)
	}
	if rep, err := up.VerifyAudit(ctx); err != nil || !rep.OK || rep.Rows != 3 {
		t.Fatalf("the audit chain: %+v %v", rep, err)
	}
	var plan strings.Builder
	rows, err := up.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM audit WHERE org_id=? ORDER BY id DESC LIMIT 50`, "o1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if !strings.Contains(plan.String(), "audit_org") {
		t.Errorf("ListAudit's query does not use the index: %s", plan.String())
	}
}

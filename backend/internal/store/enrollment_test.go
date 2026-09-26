package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A database written before approval codes (user_version 2, no such columns) opens, keeps its rows, and
// reads them as legacy enrollments and unbound tokens.
func TestOpeningADatabaseFromBeforeApprovalCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old := `
CREATE TABLE tokens (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, hash BLOB NOT NULL UNIQUE, label TEXT NOT NULL, access_tier INTEGER NOT NULL,
  created_by TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER, used_by TEXT NOT NULL DEFAULT '');
CREATE TABLE agents (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL, installed_tier INTEGER NOT NULL,
  tier_cap INTEGER NOT NULL DEFAULT 0, access_tier INTEGER NOT NULL DEFAULT 0, fingerprint TEXT NOT NULL, cluster_id TEXT NOT NULL DEFAULT '',
  csr BLOB NOT NULL, poll_secret_hash BLOB, version TEXT NOT NULL DEFAULT '', k8s_version TEXT NOT NULL DEFAULT '', token_id TEXT NOT NULL,
  created_at INTEGER NOT NULL, approved_at INTEGER, approved_by TEXT NOT NULL DEFAULT '', revoked_at INTEGER, reason TEXT NOT NULL DEFAULT '',
  last_seen INTEGER, connecting_ip TEXT NOT NULL DEFAULT '', leaf BLOB, leaf_not_after INTEGER);
CREATE TABLE audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, org_id TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
  target_kind TEXT NOT NULL, target_id TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', hash BLOB);
INSERT INTO tokens(id, org_id, hash, label, access_tier, created_by, created_at, expires_at) VALUES('tk-1','o',x'01','edge',2,'a',1,2);
INSERT INTO agents(id, org_id, name, status, installed_tier, fingerprint, csr, token_id, created_at) VALUES('ag-1','o','edge','pending',2,'fp',x'00','tk-1',1);
PRAGMA user_version = 2;`
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.GetAgent(context.Background(), "ag-1")
	if err != nil || len(a.ApprovalHash) != 0 || a.ApprovalAttempts != 0 || a.ClockSkewMs != 0 {
		t.Fatalf("%+v %v", a, err)
	}
	ts, _ := st.ListTokens(context.Background(), "o")
	if len(ts) != 1 || ts[0].ExpectedFingerprint != "" {
		t.Fatalf("%+v", ts)
	}
	// Opening it again changes nothing (the migration is recorded).
	st.Close()
	if st, err = OpenSQLite(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
}

func TestApprovalAttemptsAndExpiryInTheStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateToken(ctx, Token{ID: "tk-1", OrgID: "o", Label: "edge", AccessTier: 2, CreatedBy: "a", CreatedAt: now, ExpiresAt: now.Add(time.Hour), ExpectedFingerprint: "fp-a"}, []byte("h1")); err != nil {
		t.Fatal(err)
	}
	a := Agent{ID: "ag-1", InstalledTier: 2, Fingerprint: "fp-b", CSR: []byte("csr"), ApprovalHash: []byte("hash")}
	if tok, err := st.EnrollAgent(ctx, []byte("h1"), a, now); err != ErrWrongCluster || tok.OrgID != "o" {
		t.Fatalf("%+v %v", tok, err)
	}
	a.Fingerprint = "fp-a"
	if _, err := st.EnrollAgent(ctx, []byte("h1"), a, now); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		if n, err := st.CountApprovalAttempt(ctx, "ag-1"); err != nil || n != want {
			t.Fatalf("attempt %d = %d %v", want, n, err)
		}
	}
	got, _ := st.AgentByToken(ctx, []byte("h1"))
	if got.ID != "ag-1" || got.ApprovalAttempts != 3 || string(got.ApprovalHash) != "hash" {
		t.Fatalf("%+v", got)
	}
	if _, err := st.AgentByToken(ctx, []byte("nope")); err != ErrNotFound {
		t.Fatal(err)
	}
	// An agent that is not pending cannot be counted against.
	if err := st.RejectAgent(ctx, "ag-1", "no", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CountApprovalAttempt(ctx, "ag-1"); err != ErrBadState {
		t.Fatalf("%v", err)
	}
	if err := st.SetClockSkew(ctx, "ag-1", -180000); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.GetAgent(ctx, "ag-1"); g.ClockSkewMs != -180000 {
		t.Fatalf("%+v", g)
	}
}

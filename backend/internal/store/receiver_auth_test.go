package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestOperatorCreatedUnderSchemaNineReadsBackAsBearer is the 9 -> 10 upgrade: every operator written
// before receiver_auth existed was installed with receiver.auth.enabled=true and its token cannot be
// recovered, so it must read back as a bearer operator, token hash intact.
func TestOperatorCreatedUnderSchemaNineReadsBackAsBearer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE operators DROP COLUMN receiver_auth`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason)
		 VALUES('op-v9','o','from v9','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'0a0b','alex',1700000000000,'')`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 9 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 10 {
		t.Fatalf("user_version = %d (%v), want 10", v, err)
	}
	got, err := up.GetOperator(ctx, "op-v9")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReceiverAuth != ReceiverAuthBearer || string(got.ReceiverAuthTokenHash) != "\x0a\x0b" {
		t.Fatalf("a v9 operator reads back as %+v", got)
	}
}

func TestOperatorReceiverAuthRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	mk := func(id string, auth ReceiverAuth, hash []byte) {
		op := hbOp(id)
		op.CreatedAt = now
		op.ReceiverAuth = auth
		if err := st.CreateOperator(ctx, op, hash); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	mk("op-m", ReceiverAuthMTLS, nil) // an mTLS operator has no token hash: stored empty, not NULL
	mk("op-b", ReceiverAuthBearer, []byte("h"))
	mk("op-z", "", []byte("h")) // a caller that does not set it gets bearer, never mtls
	for id, want := range map[string]ReceiverAuth{"op-m": ReceiverAuthMTLS, "op-b": ReceiverAuthBearer, "op-z": ReceiverAuthBearer} {
		got, err := st.GetOperator(ctx, id)
		if err != nil || got.ReceiverAuth != want {
			t.Fatalf("%s: %v %+v, want %s", id, err, got, want)
		}
	}
	if m, _ := st.GetOperator(ctx, "op-m"); len(m.ReceiverAuthTokenHash) != 0 {
		t.Fatalf("mtls operator has a token hash: %x", m.ReceiverAuthTokenHash)
	}
}

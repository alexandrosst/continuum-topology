package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestOperatorsFromSchemaTenKeepNoCAAndReadBackAsOrgScope is the 10 -> 11 upgrade: an mTLS operator written
// before per-operator CAs has no CA, so its client certificates keep coming from the org CA and it reads back
// with the weaker "org" scope; a bearer operator reads back with no scope at all. Nothing is backfilled.
func TestOperatorsFromSchemaTenKeepNoCAAndReadBackAsOrgScope(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE operators DROP COLUMN client_ca_cert`,
		`ALTER TABLE operators DROP COLUMN client_ca_key`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-mtls10','o','legacy mtls','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-bearer10','o','bearer','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'0a0b','alex',1700000000000,'','bearer')`,
		`PRAGMA user_version = 10`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 10 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	for id, want := range map[string]string{"op-mtls10": ClientCAScopeOrg, "op-bearer10": ""} {
		got, err := up.GetOperator(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.ClientCACertPEM) != 0 || got.ClientCAScope() != want {
			t.Fatalf("%s: CA cert %q, scope %q, want none and %q", id, got.ClientCACertPEM, got.ClientCAScope(), want)
		}
		if _, err := up.GetOperatorClientCAKey(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: a pre-11 operator has a CA key: %v", id, err)
		}
	}
	// Opening again is a no-op (the version is already 11, and the columns exist).
	up.Close()
	again, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	again.Close()
}

func TestOperatorClientCARoundTripRevokeAndDelete(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	op := hbOp("op-ca")
	op.CreatedAt = now
	op.ReceiverAuth = ReceiverAuthMTLS
	op.ClientCACertPEM = []byte("CERT-PEM")
	op.ClientCAKeyPEM = []byte("SEALED-KEY-PEM")
	if err := st.CreateOperator(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetOperator(ctx, "op-ca")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ClientCACertPEM) != "CERT-PEM" || got.ClientCAScope() != ClientCAScopeOperator {
		t.Fatalf("read back %q scope %q", got.ClientCACertPEM, got.ClientCAScope())
	}
	if len(got.ClientCAKeyPEM) != 0 {
		t.Fatal("a read returned the CA key; it must be fetched only by GetOperatorClientCAKey")
	}
	list, err := st.ListOperators(ctx, op.OrgID)
	if err != nil || len(list) != 1 || len(list[0].ClientCAKeyPEM) != 0 {
		t.Fatalf("ListOperators: %v %+v", err, list)
	}
	if key, err := st.GetOperatorClientCAKey(ctx, "op-ca"); err != nil || string(key) != "SEALED-KEY-PEM" {
		t.Fatalf("key = %q, %v", key, err)
	}

	// Revoking erases the key (the certificate is public and stays, so the scope still reads "operator").
	if err := st.RevokeOperator(ctx, "op-ca", "gone", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOperatorClientCAKey(ctx, "op-ca"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked operator still has its CA key: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM operators WHERE id='op-ca' AND client_ca_key IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("client_ca_key not NULL after revoke: %d %v", n, err)
	}
	if got, _ := st.GetOperator(ctx, "op-ca"); got.ClientCAScope() != ClientCAScopeOperator {
		t.Fatalf("scope after revoke = %q", got.ClientCAScope())
	}

	// Deleting drops the row, key and all.
	if err := st.DeleteOperator(ctx, "op-ca"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOperatorClientCAKey(ctx, "op-ca"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted operator still has a CA key: %v", err)
	}
}

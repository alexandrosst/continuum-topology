package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An operator written before certificate dates were recorded (schema 15) comes back with none, and no expiry warning
// raised: the server then works the dates out from what it does hold.
func TestOperatorsFromSchemaFifteenHaveNoCertDatesAndKeepWorking(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE operators DROP COLUMN receiver_not_after`,
		`ALTER TABLE operators DROP COLUMN client_not_after`,
		`ALTER TABLE operators DROP COLUMN cert_alert_level`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-old15','o','legacy','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`PRAGMA user_version = 15`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 15 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	got, err := up.GetOperator(ctx, "op-old15")
	if err != nil || got.ReceiverNotAfter != nil || got.ClientNotAfter != nil || got.CertAlertLevel != 0 || got.Name != "legacy" {
		t.Fatalf("legacy operator = %+v, %v", got, err)
	}
}

func TestOperatorCertDatesAreStoredResetTheWarningLevelAndNeedAnActiveOperator(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	recv, client := time.UnixMilli(1800000000000), time.UnixMilli(1800000100000)
	op := Operator{ID: "op-1", OrgID: "o", Name: "eu", Status: OperatorActive, ReceiverAuth: ReceiverAuthMTLS, ReceiverNotAfter: &recv, ClientNotAfter: &client,
		Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}, CreatedBy: "alex", CreatedAt: time.Now()}
	if err := st.CreateOperator(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetOperator(ctx, "op-1")
	if got.ReceiverNotAfter == nil || !got.ReceiverNotAfter.Equal(recv) || got.ClientNotAfter == nil || !got.ClientNotAfter.Equal(client) {
		t.Fatalf("dates did not round-trip: %+v", got)
	}
	if err := st.SetOperatorCertAlertLevel(ctx, "op-1", 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetOperator(ctx, "op-1"); got.CertAlertLevel != 2 {
		t.Fatalf("alert level = %d", got.CertAlertLevel)
	}
	later := recv.Add(365 * 24 * time.Hour)
	if err := st.SetOperatorCerts(ctx, "op-1", later, later); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetOperator(ctx, "op-1")
	if !got.ReceiverNotAfter.Equal(later) || got.CertAlertLevel != 0 {
		t.Fatalf("re-issued dates must reset the warning level: %+v", got)
	}
	if list, _ := st.ListOperators(ctx, "o"); len(list) != 1 || list[0].ReceiverNotAfter == nil {
		t.Fatalf("list = %+v", list)
	}
	if err := st.RevokeOperator(ctx, "op-1", "gone", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOperatorCerts(ctx, "op-1", later, later); err != ErrBadState {
		t.Fatalf("a revoked operator's dates were changed: %v", err)
	}
}

func TestSetOperatorReceiverTokenReplacesTheHashOfAnActiveBearerOperatorOnly(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	mk := func(id string, auth ReceiverAuth) {
		op := Operator{ID: id, OrgID: "o", Name: id, Status: OperatorActive, ReceiverAuth: auth,
			Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}, CreatedBy: "alex", CreatedAt: time.Now()}
		if err := st.CreateOperator(ctx, op, []byte("old")); err != nil {
			t.Fatal(err)
		}
	}
	mk("op-b", ReceiverAuthBearer)
	mk("op-m", ReceiverAuthMTLS)
	if err := st.SetOperatorReceiverToken(ctx, "op-b", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetOperator(ctx, "op-b"); string(got.ReceiverAuthTokenHash) != "new" {
		t.Fatalf("hash = %q", got.ReceiverAuthTokenHash)
	}
	if err := st.SetOperatorReceiverToken(ctx, "op-m", []byte("new")); err != ErrBadState {
		t.Fatalf("an mTLS operator has no token to replace: %v", err)
	}
}

// A real schema-15 database, written by the previous release's own store (testdata/schema15_operators.db: a CA-backed
// mTLS operator, a bearer one, one chained to another, a revoked one, a telemetry intent and audit rows), is migrated
// in place without losing or changing a row, and the new columns are usable on what was already there.
func TestASchemaFifteenDatabaseWithRowsMigratesToSixteenIntact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "schema15_operators.db"))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "s.db")
	if err := os.WriteFile(dbPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 15 database: %v", err)
	}
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 16 {
		t.Fatalf("user_version = %d (%v), want 16", v, err)
	}
	ops, err := st.ListOperators(ctx, "org-1")
	if err != nil || len(ops) != 4 {
		t.Fatalf("operators after migrating: %d (%v), want the 4 that were there", len(ops), err)
	}
	for _, op := range ops {
		if op.ReceiverNotAfter != nil || op.ClientNotAfter != nil || op.CertAlertLevel != 0 {
			t.Errorf("%s: a migrated row invented cert state: %+v", op.ID, op)
		}
		if op.Address != "203.0.113.7:4317" || op.Exposure != "loadbalancer" || len(op.Labels) != 1 || op.CreatedBy != "alex" {
			t.Errorf("%s: a column from an earlier version changed: %+v", op.ID, op)
		}
	}
	gone, err := st.GetOperator(ctx, "op-gone0001")
	if err != nil || gone.Status != OperatorRevoked || gone.Reason != "decommissioned" || gone.RevokedAt == nil {
		t.Fatalf("the revoked operator: %+v %v", gone, err)
	}
	if key, err := st.GetOperatorClientCAKey(ctx, "op-mtls0001"); err != nil || string(key) != "sealed-key" {
		t.Fatalf("the sealed CA key did not survive: %q %v", key, err)
	}
	if b, err := st.GetOperator(ctx, "op-bear0001"); err != nil || string(b.ReceiverAuthTokenHash) != "hash" || b.ReceiverAuth != ReceiverAuthBearer {
		t.Fatalf("the bearer operator: %+v %v", b, err)
	}
	if tis, err := st.ListTelemetryIntents(ctx, "org-1"); err != nil || len(tis) != 1 || tis[0].Destination.TargetOperatorID != "op-mtls0001" {
		t.Fatalf("telemetry intents: %+v %v", tis, err)
	}
	if rep, err := st.VerifyAudit(ctx); err != nil || !rep.OK || rep.Rows != 2 {
		t.Fatalf("the audit trail after migrating: %+v %v", rep, err)
	}
	// The new columns work on a migrated row, and a second open changes nothing (migrations run once).
	end := time.UnixMilli(1900000000000)
	if err := st.SetOperatorCerts(ctx, "op-mtls0001", end, end); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOperatorCertAlertLevel(ctx, "op-mtls0001", 2); err != nil {
		t.Fatal(err)
	}
	st.Close()
	again, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, _ := again.GetOperator(ctx, "op-mtls0001"); got.ReceiverNotAfter == nil || !got.ReceiverNotAfter.Equal(end) || got.CertAlertLevel != 2 {
		t.Fatalf("reopening lost the recorded dates: %+v", got)
	}
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A schema-16 database (operators, no ledger) opens, gets an empty ledger and keeps its operators.
func TestSchemaSixteenGainsAnEmptyCertificateLedger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE operator_certs`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-old16','o','legacy','','active','[]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`PRAGMA user_version = 16`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 16 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	if certs, err := up.ListOperatorCerts(ctx, "op-old16"); err != nil || len(certs) != 0 {
		t.Fatalf("a certificate recorded out of nowhere: %v %v", certs, err)
	}
	if op, err := up.GetOperator(ctx, "op-old16"); err != nil || op.Name != "legacy" {
		t.Fatalf("the operator was lost: %v %v", op, err)
	}
}

func TestOperatorCertLedgerRecordsListsAndDiesWithItsOperator(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	add := func(serial, op string, kind OperatorCertKind, sender string, at time.Time) {
		t.Helper()
		if err := st.AddOperatorCert(ctx, OperatorCert{Serial: serial, OrgID: "o", OperatorID: op, Kind: kind, Subject: op + "-export-" + sender, Sender: sender, IssuedBy: "alex", IssuedAt: at, NotBefore: at, NotAfter: at.Add(365 * 24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	add("aa01", "op-1", OperatorCertReceiver, "", t0)
	add("aa02", "op-1", OperatorCertClient, "cl-a", t0.Add(time.Hour))
	add("bb01", "op-2", OperatorCertClient, "cl-b", t0)
	if err := st.AddOperatorCert(ctx, OperatorCert{Serial: "aa02", OperatorID: "op-1", Kind: OperatorCertClient, IssuedAt: t0, NotBefore: t0, NotAfter: t0}); err == nil {
		t.Error("a serial was recorded twice")
	}
	got, err := st.ListOperatorCerts(ctx, "op-1")
	if err != nil || len(got) != 2 || got[0].Serial != "aa02" || got[0].Sender != "cl-a" || got[1].Kind != OperatorCertReceiver || !got[0].NotAfter.Equal(t0.Add(time.Hour+365*24*time.Hour)) {
		t.Fatalf("op-1: %+v %v", got, err)
	}
	if err := st.CreateOperator(ctx, Operator{ID: "op-1", OrgID: "o", Name: "one", Status: OperatorActive, CreatedAt: t0, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteOperator(ctx, "op-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListOperatorCerts(ctx, "op-1"); len(got) != 0 {
		t.Errorf("a deleted operator's certificates are still listed: %+v", got)
	}
	if got, _ := st.ListOperatorCerts(ctx, "op-2"); len(got) != 1 {
		t.Errorf("another operator's ledger changed: %+v", got)
	}
}

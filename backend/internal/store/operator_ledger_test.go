package store

import (
	"context"
	"errors"
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
	if certs, err := up.ListOperatorCerts(ctx, "o", "op-old16"); err != nil || len(certs) != 0 {
		t.Fatalf("a certificate recorded out of nowhere: %v %v", certs, err)
	}
	if op, err := up.GetOperator(ctx, "o", "op-old16"); err != nil || op.Name != "legacy" {
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
	for _, id := range []string{"op-1", "op-2"} {
		if err := st.CreateOperator(ctx, Operator{ID: id, OrgID: "o", Name: id, Status: OperatorActive, CreatedAt: t0, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	add("aa01", "op-1", OperatorCertReceiver, "", t0)
	add("aa02", "op-1", OperatorCertClient, "cl-a", t0.Add(time.Hour))
	add("bb01", "op-2", OperatorCertClient, "cl-b", t0)
	if err := st.AddOperatorCert(ctx, OperatorCert{Serial: "aa02", OrgID: "o", OperatorID: "op-1", Kind: OperatorCertClient, IssuedAt: t0, NotBefore: t0, NotAfter: t0}); err == nil {
		t.Error("a serial was recorded twice")
	}
	got, err := st.ListOperatorCerts(ctx, "o", "op-1")
	if err != nil || len(got) != 2 || got[0].Serial != "aa02" || got[0].Sender != "cl-a" || got[1].Kind != OperatorCertReceiver || !got[0].NotAfter.Equal(t0.Add(time.Hour+365*24*time.Hour)) {
		t.Fatalf("op-1: %+v %v", got, err)
	}
	if err := st.DeleteOperator(ctx, "o", "op-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListOperatorCerts(ctx, "o", "op-1"); len(got) != 0 {
		t.Errorf("a deleted operator's certificates are still listed: %+v", got)
	}
	if got, _ := st.ListOperatorCerts(ctx, "o", "op-2"); len(got) != 1 {
		t.Errorf("another operator's ledger changed: %+v", got)
	}
}

// An operator is addressed by its id inside its organisation. Another organisation naming the same id finds no row, whatever it asks of
// it: it reads nothing, changes nothing, erases nothing, and is told what a missing id is told.
func TestAnotherOrganisationsOperatorIsNotFoundByEveryMethod(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	op := Operator{ID: "op-1", OrgID: "alice", Name: "eu", Status: OperatorActive, ReceiverAuth: ReceiverAuthMTLS, ClientCACertPEM: []byte("ca"), ClientCAKeyPEM: []byte("key"),
		CreatedAt: now, Destination: Destination{Kind: DestinationExternal, Endpoint: "a:4317"}}
	if err := st.CreateOperator(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.AddOperatorCert(ctx, OperatorCert{Serial: "aa01", OrgID: "alice", OperatorID: "op-1", Kind: OperatorCertReceiver, IssuedAt: now, NotBefore: now, NotAfter: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetOperator(ctx, "bob", "op-1"); err != ErrNotFound {
		t.Errorf("get: %v", err)
	}
	if _, err := st.GetOperatorClientCAKey(ctx, "bob", "op-1"); err != ErrNotFound {
		t.Errorf("CA key: %v", err)
	}
	if got, err := st.ListOperatorCerts(ctx, "bob", "op-1"); err != nil || len(got) != 0 {
		t.Errorf("ledger: %v %v", got, err)
	}
	for name, err := range map[string]error{
		"scope":       st.UpdateOperatorScope(ctx, "bob", "op-1", nil, Destination{Kind: DestinationExternal, Endpoint: "b:4317"}, nil),
		"address":     st.SetOperatorAddress(ctx, "bob", "op-1", "b.example.com:4317"),
		"heartbeat":   st.SetOperatorHeartbeat(ctx, "bob", "op-1", []byte("h"), now),
		"reissue":     st.ReissueOperatorInstall(ctx, "bob", "op-1", OperatorReissue{ReceiverNotAfter: now, ClientNotAfter: now, Now: now}),
		"certs":       st.SetOperatorCerts(ctx, "bob", "op-1", now, now),
		"alert level": st.SetOperatorCertAlertLevel(ctx, "bob", "op-1", 2),
		"touch":       st.TouchOperatorSeen(ctx, "bob", "op-1", now), // reports nothing: it changes nothing either
		"revoke":      st.RevokeOperator(ctx, "bob", "op-1", "no", now),
		"delete":      st.DeleteOperator(ctx, "bob", "op-1"),
	} {
		if name == "touch" {
			continue
		}
		if err != ErrNotFound {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if _, err := st.SetOperatorAddressIfEmpty(ctx, "bob", "op-1", "b.example.com:4317"); err != ErrNotFound {
		t.Errorf("address if empty: %v", err)
	}

	got, err := st.GetOperator(ctx, "alice", "op-1")
	if err != nil || got.Status != OperatorActive || got.Address != "" || got.Destination.Endpoint != "a:4317" || got.LastSeenAt != nil || got.HeartbeatEnabledAt != nil {
		t.Fatalf("Alice's operator changed: %+v %v", got, err)
	}
	if key, err := st.GetOperatorClientCAKey(ctx, "alice", "op-1"); err != nil || string(key) != "key" {
		t.Errorf("Alice's CA key: %q %v", key, err)
	}
	if certs, _ := st.ListOperatorCerts(ctx, "alice", "op-1"); len(certs) != 1 {
		t.Errorf("Alice's ledger: %+v", certs)
	}
	// A row recorded under Bob's organisation is Bob's, whatever operator id it names, and Alice's ledger does not show it.
	if err := st.AddOperatorCert(ctx, OperatorCert{Serial: "bb01", OrgID: "bob", OperatorID: "op-1", Kind: OperatorCertReceiver, IssuedAt: now, NotBefore: now, NotAfter: now}); err != nil {
		t.Fatal(err)
	}
	if certs, _ := st.ListOperatorCerts(ctx, "alice", "op-1"); len(certs) != 1 {
		t.Errorf("Alice's ledger shows a row of Bob's: %+v", certs)
	}

	// The id is global, so another organisation cannot make an operator with it either, and is told so (not that it is missing).
	if err := st.CreateOperator(ctx, Operator{ID: "op-1", OrgID: "bob", Name: "mine", Status: OperatorActive, CreatedAt: now}, nil); !errors.Is(err, ErrExists) {
		t.Errorf("a second operator with the id: %v, want ErrExists", err)
	}
}

// An operator and its first certificate are stored together: the certificate that cannot be recorded stops the operator
// from being created, and a creation that fails for any reason leaves no ledger row for an id that never existed.
func TestAnOperatorAndItsFirstCertificateAreStoredTogetherOrNotAtAll(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string) Operator {
		return Operator{ID: id, OrgID: "alice", Name: id, Status: OperatorActive, ReceiverAuth: ReceiverAuthMTLS, ClientCACertPEM: []byte("ca"), ClientCAKeyPEM: []byte("key"),
			CreatedAt: now, Destination: Destination{Kind: DestinationExternal, Endpoint: "a:4317"}}
	}
	cert := func(serial, op string) OperatorCert {
		return OperatorCert{Serial: serial, OrgID: "alice", OperatorID: op, Kind: OperatorCertReceiver, IssuedAt: now, NotBefore: now, NotAfter: now.Add(time.Hour)}
	}

	if err := st.CreateOperator(ctx, mk("op-1"), nil, WithOperatorCert(cert("aa01", "op-1"))); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListOperatorCerts(ctx, "alice", "op-1"); err != nil || len(got) != 1 || got[0].Serial != "aa01" {
		t.Fatalf("the certificate recorded with its operator: %v %v", got, err)
	}

	// The serial is already in the ledger: the certificate cannot be recorded, so the operator is not created.
	err = st.CreateOperator(ctx, mk("op-2"), nil, WithOperatorCert(cert("aa01", "op-2")))
	if !errors.Is(err, ErrLedgerWrite) {
		t.Fatalf("a certificate that cannot be recorded: %v", err)
	}
	if _, err := st.GetOperator(ctx, "alice", "op-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an operator was created whose certificate could not be recorded: %v", err)
	}

	// The id is taken: the creation fails and its certificate is not recorded either.
	if err := st.CreateOperator(ctx, mk("op-1"), nil, WithOperatorCert(cert("aa03", "op-1"))); !errors.Is(err, ErrExists) {
		t.Fatalf("a taken id: %v", err)
	}
	if got, _ := st.ListOperatorCerts(ctx, "alice", "op-1"); len(got) != 1 {
		t.Fatalf("a failed creation left a ledger row behind: %v", got)
	}

	// The audit row travels with them: a failed ledger write leaves no audit row either.
	before, _ := st.ListAudit(ctx, "alice", 100)
	err = st.CreateOperator(ctx, mk("op-3"), nil, WithOperatorCert(cert("aa01", "op-3")), WithAudit(AuditEvent{OrgID: "alice", Actor: "alex", Action: "operator-created", TargetKind: "operator", TargetID: "op-3"}))
	if !errors.Is(err, ErrLedgerWrite) {
		t.Fatalf("%v", err)
	}
	after, _ := st.ListAudit(ctx, "alice", 100)
	if len(after) != len(before) {
		t.Fatalf("a refused creation left an audit row: %d before, %d after", len(before), len(after))
	}
}

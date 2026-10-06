package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// An operator written before addresses existed (schema 13) comes back with none: only the in-cluster name was ever
// known for it, which is what "no address" still means.
func TestOperatorsFromSchemaThirteenHaveNoAddressAndKeepWorking(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE operators DROP COLUMN address`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-old13','o','legacy','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`PRAGMA user_version = 13`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 13 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	got, err := up.GetOperator(ctx, "op-old13")
	if err != nil || got.Address != "" || got.Name != "legacy" {
		t.Fatalf("legacy operator = %+v, %v", got, err)
	}
}

func TestSetOperatorAddressStoresAndClearsItForAnActiveOperatorOnly(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	op := Operator{ID: "op-1", OrgID: "o", Name: "eu", Status: OperatorActive, ReceiverAuth: ReceiverAuthMTLS,
		Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}, CreatedBy: "alex", CreatedAt: time.Now()}
	if err := st.CreateOperator(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOperatorAddress(ctx, "op-1", "otlp.example.com:4317"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetOperator(ctx, "op-1"); got.Address != "otlp.example.com:4317" {
		t.Fatalf("address = %q", got.Address)
	}
	if list, _ := st.ListOperators(ctx, "o"); len(list) != 1 || list[0].Address != "otlp.example.com:4317" {
		t.Fatalf("list = %+v", list)
	}
	if err := st.SetOperatorAddress(ctx, "op-1", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetOperator(ctx, "op-1"); got.Address != "" {
		t.Fatalf("cleared address = %q", got.Address)
	}
	if err := st.RevokeOperator(ctx, "op-1", "gone", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOperatorAddress(ctx, "op-1", "a.example.com:1"); !errors.Is(err, ErrBadState) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked operator's address was changed: %v", err)
	}
}

// How an operator was exposed is stored with it, and an operator from before it was asked has none.
func TestOperatorExposureIsStoredAndLegacyRowsHaveNone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	op := Operator{ID: "op-x", OrgID: "o", Name: "x", Status: OperatorActive, SourceClusterIDs: []string{"cl-a"}, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}, Exposure: "nodeport", CreatedBy: "a", CreatedAt: time.Now()}
	if err := st.CreateOperator(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-old14','o','legacy','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`ALTER TABLE operators DROP COLUMN exposure`,
		`PRAGMA user_version = 14`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 14 database: %v", err)
	}
	defer up.Close()
	if got, err := up.GetOperator(ctx, "op-old14"); err != nil || got.Exposure != "" {
		t.Fatalf("legacy operator = %+v, %v", got, err)
	}
	if err := up.CreateOperator(ctx, Operator{ID: "op-y", OrgID: "o", Name: "y", Status: OperatorActive, SourceClusterIDs: []string{"cl-a"}, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"}, Exposure: "loadbalancer", CreatedBy: "a", CreatedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := up.GetOperator(ctx, "op-y"); err != nil || got.Exposure != "loadbalancer" {
		t.Fatalf("exposure = %+v, %v", got, err)
	}
}

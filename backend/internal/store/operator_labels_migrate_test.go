package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestOperatorsFromSchemaElevenGetNoLabelsAndKeepWorking is the 11 -> 12 upgrade: an operator written before
// labels existed gains an empty label list (it was installed without any), and the database lands on 12.
func TestOperatorsFromSchemaElevenGetNoLabelsAndKeepWorking(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE operators DROP COLUMN labels`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason, receiver_auth)
		 VALUES('op-old11','o','legacy','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'','alex',1700000000000,'','mtls')`,
		`PRAGMA user_version = 11`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 11 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d (12, then the later migrations)", v, err, SchemaVersion)
	}
	got, err := up.GetOperator(ctx, "op-old11")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 0 {
		t.Fatalf("a pre-labels operator reads back with labels %+v", got.Labels)
	}
}

func TestOperatorLabelsRoundTripInOrder(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	op := Operator{ID: "op-l", OrgID: "o", Name: "labelled", Status: OperatorActive,
		SourceClusterIDs: []string{"cl-a"}, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"},
		Labels: []OperatorLabel{{Key: "region", Value: "eu-south"}, {Key: "env", Value: "prod"}}}
	if err := st.CreateOperator(ctx, op, []byte("h")); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetOperator(ctx, "op-l")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 2 || got.Labels[0] != (OperatorLabel{"region", "eu-south"}) || got.Labels[1] != (OperatorLabel{"env", "prod"}) {
		t.Fatalf("labels = %+v", got.Labels)
	}
	list, err := st.ListOperators(ctx, "o")
	if err != nil || len(list) != 1 || len(list[0].Labels) != 2 {
		t.Fatalf("listed labels = %+v (%v)", list, err)
	}
}

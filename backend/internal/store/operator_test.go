package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOperatorCRUDInTheStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()

	op := Operator{
		ID:               "op-1",
		OrgID:            "o",
		Name:             "athens-regional",
		SiteID:           "site-ath",
		Status:           OperatorActive,
		SourceClusterIDs: []string{"cl-edge-a", "cl-edge-b"},
		Destination:      Destination{Kind: DestinationExternal, Endpoint: "collector.example:4317", AuthHeaderName: "Authorization"},
		CreatedBy:        "alex",
		CreatedAt:        now,
	}
	if err := st.CreateOperator(ctx, op, []byte("tokhash")); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetOperator(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "athens-regional" || got.Status != OperatorActive || len(got.SourceClusterIDs) != 2 ||
		got.SourceClusterIDs[0] != "cl-edge-a" || got.Destination.Endpoint != "collector.example:4317" || got.Destination.Kind != DestinationExternal {
		t.Fatalf("%+v", got)
	}

	list, err := st.ListOperators(ctx, "o")
	if err != nil || len(list) != 1 || list[0].ID != "op-1" {
		t.Fatalf("%+v %v", list, err)
	}
	if other, err := st.ListOperators(ctx, "another-org"); err != nil || len(other) != 0 {
		t.Fatalf("cross-org leak: %+v %v", other, err)
	}

	if err := st.UpdateOperatorScope(ctx, "op-1", []string{"cl-edge-c"}, Destination{Kind: DestinationExternal, Endpoint: "collector2.example:4317"}, []Modality{ModalityMetrics}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetOperator(ctx, "op-1")
	if len(got.SourceClusterIDs) != 1 || got.SourceClusterIDs[0] != "cl-edge-c" || got.Destination.Endpoint != "collector2.example:4317" ||
		len(got.AcceptedModalities) != 1 || got.AcceptedModalities[0] != ModalityMetrics {
		t.Fatalf("scope not updated: %+v", got)
	}

	if err := st.RevokeOperator(ctx, "op-1", "no longer needed", now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetOperator(ctx, "op-1")
	if got.Status != OperatorRevoked || got.Reason != "no longer needed" || got.RevokedAt == nil {
		t.Fatalf("not revoked: %+v", got)
	}

	// A revoked operator's scope can no longer be changed - the same "only active" guard RevokeOperator itself needs.
	if err := st.UpdateOperatorScope(ctx, "op-1", []string{"cl-edge-d"}, Destination{Kind: DestinationExternal, Endpoint: "x"}, nil); err != ErrBadState {
		t.Fatalf("expected ErrBadState updating a revoked operator's scope, got %v", err)
	}
	if err := st.RevokeOperator(ctx, "op-1", "again", now); err != ErrBadState {
		t.Fatalf("expected ErrBadState revoking twice, got %v", err)
	}

	if err := st.DeleteOperator(ctx, "op-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOperator(ctx, "op-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err := st.DeleteOperator(ctx, "op-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting again, got %v", err)
	}
}

func TestOperatorDestinationRoundTripsAllFields(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dest := Destination{
		Kind: DestinationExternal, Endpoint: "collector.example:4317", Insecure: true, CAFile: "/etc/ca.pem",
		AuthHeaderName: "Authorization", AuthSecretName: "operator-export-auth", AuthSecretKey: "token",
	}
	op := Operator{ID: "op-2", OrgID: "o", Name: "fra-central", Status: OperatorActive, SourceClusterIDs: []string{"cl-region"}, Destination: dest, CreatedBy: "alex", CreatedAt: time.Now()}
	if err := st.CreateOperator(ctx, op, []byte("h")); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetOperator(ctx, "op-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Destination != dest {
		t.Fatalf("destination did not round-trip: got %+v want %+v", got.Destination, dest)
	}
}

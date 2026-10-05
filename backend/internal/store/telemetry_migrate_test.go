package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestSchemaLandsOnTheCurrentVersionAfterMigrations checks the migrations' own version bumps - a fresh
// database (or one upgraded from any earlier version, which migrateWebAuthn's own sibling migrations
// already exercise) ends up at the current SchemaVersion once OpenSQLite returns. (Named for 8 while
// migrateTelemetry was the newest; migrateHeartbeat made it 9 and migrateReceiverAuth 10 and migrateOperatorCA 11 - see heartbeat_test.go, receiver_auth_test.go and operator_ca_test.go.)
func TestSchemaLandsOnTheCurrentVersionAfterMigrations(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 11 || SchemaVersion != 11 {
		t.Fatalf("user_version = %d, SchemaVersion = %d, want both 11", v, SchemaVersion)
	}
}

// TestOperatorWithoutAcceptedModalitiesReopensEmptyNotError is the case telemetry_migrate.go's own comment
// promises: an operator row that predates accepted_modalities (the migration's ALTER TABLE default is
// '[]') reads back with an empty, not erroring, AcceptedModalities after the database is closed and
// reopened - the same "accepts everything" behaviour such a row always had.
func TestOperatorWithoutAcceptedModalitiesReopensEmptyNotError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()

	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	op := Operator{
		ID: "op-legacy", OrgID: "o", Name: "pre-existing", Status: OperatorActive,
		SourceClusterIDs: []string{"cl-a"}, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"},
		CreatedBy: "alex", CreatedAt: time.Now(),
	}
	if err := st.CreateOperator(ctx, op, []byte("h")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetOperator(ctx, "op-legacy")
	if err != nil {
		t.Fatalf("reopening after the migration already ran should not error: %v", err)
	}
	if len(got.AcceptedModalities) != 0 {
		t.Fatalf("expected no accepted modalities, got %+v", got.AcceptedModalities)
	}
}

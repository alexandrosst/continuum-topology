package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestTelemetryIntentFromSchemaTwelveHasNoRoutesAndKeepsWorking is the 12 -> 13 upgrade: an intent written
// before routes existed gains an empty set (it sent everything to its one destination, which is what no routes
// still means) and the database lands on 13.
func TestTelemetryIntentFromSchemaTwelveHasNoRoutesAndKeepsWorking(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE telemetry_intents DROP COLUMN routes`,
		`INSERT INTO telemetry_intents(id, org_id, agent_id, name, status, namespaces, exclude, signals, destination, created_by, created_at, reason)
		 VALUES('ti-old','o','ag-1','legacy','active','[]','[]','[]','{"Kind":"external","Endpoint":"c:4317"}','alex',1700000000000,'')`,
		`PRAGMA user_version = 12`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 12 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want SchemaVersion %d", v, err, SchemaVersion)
	}
	got, err := up.GetTelemetryIntent(ctx, "ti-old")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 0 || got.Destination.Endpoint != "c:4317" {
		t.Fatalf("a pre-routes intent reads back as %+v", got)
	}
}

func TestTelemetryIntentRoutesRoundTripAndAreReplacedWhole(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ti := TelemetryIntent{ID: "ti-r", OrgID: "o", AgentID: "ag-1", Name: "split", Status: TelemetryIntentActive,
		Destination: extTestDest("gw:4317"), CreatedBy: "alex", CreatedAt: time.Now(),
		Routes: map[Modality]Destination{ModalityTraces: {Kind: DestinationExternal, Endpoint: "z:9411", Insecure: true}}}
	if err := st.CreateTelemetryIntent(ctx, ti); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetTelemetryIntent(ctx, "ti-r")
	if len(got.Routes) != 1 || got.Routes[ModalityTraces].Endpoint != "z:9411" || !got.Routes[ModalityTraces].Insecure {
		t.Fatalf("routes after create = %+v", got.Routes)
	}
	routes := map[Modality]Destination{
		ModalityMetrics: {Kind: DestinationOperator, TargetOperatorID: "op-eu", Endpoint: "op-eu.continuum-system.svc:4317"},
		ModalityLogs:    extTestDest("loki:3100"),
	}
	if err := st.UpdateTelemetryIntentDestinations(ctx, "ti-r", extTestDest("other:4317"), routes); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTelemetryIntent(ctx, "ti-r")
	if got.Destination.Endpoint != "other:4317" || len(got.Routes) != 2 || got.Routes[ModalityMetrics].TargetOperatorID != "op-eu" || got.Routes[ModalityTraces].Endpoint != "" {
		t.Fatalf("after replacing: %+v / %+v", got.Destination, got.Routes)
	}
	if err := st.UpdateTelemetryIntentDestinations(ctx, "ti-r", extTestDest("gw:4317"), nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTelemetryIntent(ctx, "ti-r")
	if len(got.Routes) != 0 {
		t.Fatalf("routes were not cleared: %+v", got.Routes)
	}
	// A revoked intent's destinations cannot be changed.
	if err := st.RevokeTelemetryIntent(ctx, "ti-r", "done", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTelemetryIntentDestinations(ctx, "ti-r", extTestDest("x:1"), nil); err == nil {
		t.Fatal("changed the destinations of a revoked intent")
	}
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func extTestDest(endpoint string) Destination {
	return Destination{Kind: DestinationExternal, Endpoint: endpoint}
}

func TestTelemetryIntentCRUDInTheStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()

	ti := TelemetryIntent{
		ID: "ti-1", OrgID: "o", AgentID: "ag-1", Name: "patras-edge", Status: TelemetryIntentActive,
		Namespaces: []string{"checkout", "payments"}, Exclude: []string{"payments-canary"},
		Signals:     []SignalGrant{{ID: "resourceUsage", Source: "builtin"}, {ID: "traces", Source: "bundle-jaeger"}},
		Destination: extTestDest("collector.example:4317"),
		CreatedBy:   "alex", CreatedAt: now,
	}
	if err := st.CreateTelemetryIntent(ctx, ti); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTelemetryIntent(ctx, "o", "ti-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "patras-edge" || got.Status != TelemetryIntentActive || got.AgentID != "ag-1" ||
		len(got.Namespaces) != 2 || got.Namespaces[0] != "checkout" || len(got.Exclude) != 1 || got.Exclude[0] != "payments-canary" ||
		len(got.Signals) != 2 || got.Signals[0].ID != "resourceUsage" || got.Signals[0].Source != "builtin" ||
		got.Destination.Endpoint != "collector.example:4317" || got.Destination.Kind != DestinationExternal {
		t.Fatalf("%+v", got)
	}

	byAgent, err := st.ListTelemetryIntentsByAgent(ctx, "o", "ag-1")
	if err != nil || len(byAgent) != 1 || byAgent[0].ID != "ti-1" {
		t.Fatalf("%+v %v", byAgent, err)
	}
	if other, err := st.ListTelemetryIntentsByAgent(ctx, "o", "ag-does-not-exist"); err != nil || len(other) != 0 {
		t.Fatalf("expected no intents for an unrelated agent: %+v %v", other, err)
	}

	byOrg, err := st.ListTelemetryIntents(ctx, "o")
	if err != nil || len(byOrg) != 1 || byOrg[0].ID != "ti-1" {
		t.Fatalf("%+v %v", byOrg, err)
	}
	if other, err := st.ListTelemetryIntents(ctx, "another-org"); err != nil || len(other) != 0 {
		t.Fatalf("cross-org leak: %+v %v", other, err)
	}

	if err := st.UpdateTelemetryIntentScope(ctx, "o", "ti-1", []string{"checkout"}, nil, []SignalGrant{{ID: "traces", Source: "existing"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTelemetryIntent(ctx, "o", "ti-1")
	if len(got.Namespaces) != 1 || got.Namespaces[0] != "checkout" || len(got.Exclude) != 0 ||
		len(got.Signals) != 1 || got.Signals[0].ID != "traces" || got.Signals[0].Source != "existing" {
		t.Fatalf("scope not updated: %+v", got)
	}

	if err := st.UpdateTelemetryIntentDestination(ctx, "o", "ti-1", extTestDest("collector2.example:4317")); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTelemetryIntent(ctx, "o", "ti-1")
	if got.Destination.Endpoint != "collector2.example:4317" {
		t.Fatalf("destination not updated: %+v", got)
	}

	if err := st.RevokeTelemetryIntent(ctx, "o", "ti-1", "no longer needed", now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTelemetryIntent(ctx, "o", "ti-1")
	if got.Status != TelemetryIntentRevoked || got.Reason != "no longer needed" || got.RevokedAt == nil {
		t.Fatalf("not revoked: %+v", got)
	}

	// A revoked intent's scope/destination can no longer be changed - the same "only active" guard
	// RevokeTelemetryIntent itself needs.
	if err := st.UpdateTelemetryIntentScope(ctx, "o", "ti-1", []string{"checkout"}, nil, nil); err != ErrBadState {
		t.Fatalf("expected ErrBadState updating a revoked intent's scope, got %v", err)
	}
	if err := st.UpdateTelemetryIntentDestination(ctx, "o", "ti-1", extTestDest("x:4317")); err != ErrBadState {
		t.Fatalf("expected ErrBadState updating a revoked intent's destination, got %v", err)
	}
	if err := st.RevokeTelemetryIntent(ctx, "o", "ti-1", "again", now); err != ErrBadState {
		t.Fatalf("expected ErrBadState revoking twice, got %v", err)
	}

	if err := st.DeleteTelemetryIntent(ctx, "o", "ti-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTelemetryIntent(ctx, "o", "ti-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err := st.DeleteTelemetryIntent(ctx, "o", "ti-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting again, got %v", err)
	}
}

func TestGetTelemetryIntentNotFound(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.GetTelemetryIntent(ctx, "o", "ti-does-not-exist"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// An intent is addressed by its id inside its organisation. Another organisation naming the same id finds no row, whatever it asks of
// it: it reads nothing, changes nothing, and is told what a missing id is told, not that the row is in the wrong state.
func TestAnotherOrganisationsTelemetryIntentIsNotFoundByEveryMethod(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	ti := TelemetryIntent{ID: "ti-1", OrgID: "alice", AgentID: "ag-1", Name: "edge", Status: TelemetryIntentActive, Namespaces: []string{"checkout"}, Destination: extTestDest("a:4317"), CreatedBy: "a", CreatedAt: now}
	if err := st.CreateTelemetryIntent(ctx, ti); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetTelemetryIntent(ctx, "bob", "ti-1"); err != ErrNotFound {
		t.Errorf("get: %v", err)
	}
	if got, err := st.ListTelemetryIntentsByAgent(ctx, "bob", "ag-1"); err != nil || len(got) != 0 {
		t.Errorf("list by agent: %v %v", got, err)
	}
	for name, err := range map[string]error{
		"scope":        st.UpdateTelemetryIntentScope(ctx, "bob", "ti-1", []string{"x"}, nil, nil),
		"destination":  st.UpdateTelemetryIntentDestination(ctx, "bob", "ti-1", extTestDest("b:4317")),
		"destinations": st.UpdateTelemetryIntentDestinations(ctx, "bob", "ti-1", extTestDest("b:4317"), nil),
		"revoke":       st.RevokeTelemetryIntent(ctx, "bob", "ti-1", "no", now),
		"delete":       st.DeleteTelemetryIntent(ctx, "bob", "ti-1"),
	} {
		if err != ErrNotFound {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}

	got, err := st.GetTelemetryIntent(ctx, "alice", "ti-1")
	if err != nil || got.Status != TelemetryIntentActive || got.Destination.Endpoint != "a:4317" || len(got.Namespaces) != 1 || got.Namespaces[0] != "checkout" {
		t.Fatalf("Alice's intent changed: %+v %v", got, err)
	}
	// In its own organisation a revoked intent is in the wrong state, which is not the same answer.
	if err := st.RevokeTelemetryIntent(ctx, "alice", "ti-1", "done", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeTelemetryIntent(ctx, "alice", "ti-1", "again", now); err != ErrBadState {
		t.Errorf("revoking twice: %v, want ErrBadState", err)
	}
}

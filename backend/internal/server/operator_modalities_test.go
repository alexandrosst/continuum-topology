package server

import (
	"database/sql"
	"strings"
	"testing"

	"continuum/internal/kernel"
	"continuum/internal/store"
	"continuum/internal/testkit"

	_ "modernc.org/sqlite"
)

// A damaged accepted_modalities column must not read as "accepts everything": that would let a telemetry intent
// send a signal type the operator was configured to refuse. It reads as accepting nothing until the scope is saved again.
func TestCorruptAcceptedModalitiesRefuseInsteadOfAcceptEverything(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, testkit.FP)
	op, _, _, err := e.core.Operators.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, testkit.FP2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	traces := []store.SignalGrant{{ID: "traces", Source: "existing"}}

	if _, err := e.core.Telemetry.CreateTelemetryIntent(e.ctx, "alex", agentID, "before", nil, nil, traces, dest); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("sanity: traces into a metrics-only operator should be refused, got %v", err)
	}

	for _, corrupt := range []string{`["metrics"`, `not json`, `{"a":1}`, ``} {
		db, err := sql.Open("sqlite", "file:"+e.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`UPDATE operators SET accepted_modalities=? WHERE id=?`, corrupt, op.ID)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.core.Operators.GetOperator(e.ctx, op.ID)
		if err != nil {
			t.Fatalf("a damaged column must not make the operator unreadable: %v", err)
		}
		if len(got.AcceptedModalities) != 1 || got.AcceptedModalities[0] != store.ModalityUnreadable {
			t.Fatalf("corrupt %q: AcceptedModalities = %v, want [unreadable]", corrupt, got.AcceptedModalities)
		}
		_, err = e.core.Telemetry.CreateTelemetryIntent(e.ctx, "alex", agentID, "after", nil, nil, traces, dest)
		if testkit.KindOf(err) != kernel.KindInvalid || !strings.Contains(err.Error(), "could not be read") {
			t.Fatalf("corrupt %q: an intent must be refused with a clear reason, got %v", corrupt, err)
		}
	}

	// Saving the scope again repairs the row.
	if err := e.core.Operators.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics, store.ModalityTraces}); err != nil {
		t.Fatalf("saving the scope again should repair the operator: %v", err)
	}
	if _, err := e.core.Telemetry.CreateTelemetryIntent(e.ctx, "alex", agentID, "repaired", nil, nil, traces, dest); err != nil {
		t.Fatalf("after the repair, traces should be accepted: %v", err)
	}
}

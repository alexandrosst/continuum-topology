package server

import (
	"testing"

	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
)

func operatorWithIntentRig(t *testing.T) (*env, store.Operator, string, store.TelemetryIntent) {
	t.Helper()
	e := newEnv(t)
	opCluster := e.approvedCluster(t, testkit.FP)
	op, _, _, err := e.core.Operators.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, testkit.FP2)
	ti, err := e.core.Telemetry.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, nil,
		store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID})
	if err != nil {
		t.Fatal(err)
	}
	return e, op, agentID, ti
}

// Revoking an agent ends its telemetry intents, so a dead cluster does not keep an operator from being revoked.
func TestRevokingAnAgentRevokesItsTelemetryIntents(t *testing.T) {
	e, op, agentID, ti := operatorWithIntentRig(t)

	if err := e.core.Operators.RevokeOperator(e.ctx, "alex", op.ID, "decom"); testkit.KindOf(err) != kernel.KindConflict {
		t.Fatalf("sanity: a live intent should block revoking its operator, got %v", err)
	}
	if err := e.core.Agents.Revoke(e.ctx, "alex", agentID, "stolen"); err != nil {
		t.Fatal(err)
	}
	got, err := e.core.Telemetry.GetTelemetryIntent(e.ctx, ti.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.TelemetryIntentRevoked {
		t.Fatalf("intent status = %s, want revoked", got.Status)
	}
	if u := mustUsages(t, e)[op.ID]; !u.Empty() {
		t.Fatalf("a revoked agent's intent still counts as using the operator: %+v", u)
	}
	if err := e.core.Operators.RevokeOperator(e.ctx, "alex", op.ID, "decom"); err != nil {
		t.Fatalf("revoking the operator should now need no force: %v", err)
	}
}

// Intents written before revoking an agent ended them are still marked active; they must not count either.
func TestOperatorUsageIgnoresActiveIntentsOfRevokedAgents(t *testing.T) {
	e, op, agentID, ti := operatorWithIntentRig(t)

	if err := e.st.RevokeAgent(e.ctx, "org-1", agentID, "stolen", e.core.Now()); err != nil { // the store alone: no cascade, as in old data
		t.Fatal(err)
	}
	got, _ := e.core.Telemetry.GetTelemetryIntent(e.ctx, ti.ID)
	if got.Status != store.TelemetryIntentActive {
		t.Fatalf("setup: the intent should still be active, got %s", got.Status)
	}
	if u := mustUsages(t, e)[op.ID]; !u.Empty() {
		t.Fatalf("a revoked agent's intent counts as using the operator: %+v", u)
	}
	if err := e.core.Operators.RevokeOperator(e.ctx, "alex", op.ID, "decom"); err != nil {
		t.Fatalf("revoking the operator should need no force: %v", err)
	}
}

func mustUsages(t *testing.T, e *env) map[string]operators.OperatorUsage {
	t.Helper()
	u, err := e.core.Operators.OperatorUsages(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

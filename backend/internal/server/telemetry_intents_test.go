package server

import (
	"testing"

	"continuum/internal/store"
)

func TestCreateTelemetryIntentHappyPath(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge",
		[]string{"checkout"}, nil, []store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	if ti.Status != store.TelemetryIntentActive || ti.AgentID != agentID || len(ti.Namespaces) != 1 || ti.Namespaces[0] != "checkout" {
		t.Fatalf("%+v", ti)
	}
	stored, err := e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "patras-edge" || len(stored.Signals) != 1 || stored.Signals[0].ID != "resourceUsage" {
		t.Fatalf("%+v", stored)
	}
}

func TestCreateTelemetryIntentRejectsASecondActiveIntentOnTheSameAgent(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "first", nil, nil, nil, extDest("c:4317")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "second", nil, nil, nil, extDest("c2:4317")); kindOf(err) != KindConflict {
		t.Fatalf("expected KindConflict creating a second active intent on the same agent, got %v", err)
	}
}

func TestCreateTelemetryIntentRejectsAnUnapprovedOrForeignAgent(t *testing.T) {
	e := newEnv(t)
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", "ag-does-not-exist", "x", nil, nil, nil, extDest("c:4317")); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound for an unknown agent, got %v", err)
	}
	resp, _ := e.enroll(t, 2, fp)
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", resp.AgentId, "x", nil, nil, nil, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid targeting a pending (not yet approved) agent, got %v", err)
	}
}

func TestCreateTelemetryIntentValidatesNameNamespacesAndSignals(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "   ", nil, nil, nil, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("blank name: %v", err)
	}
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "x", []string{"Not_Valid!"}, nil, nil, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("bad namespace: %v", err)
	}
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "x", nil, nil, []store.SignalGrant{{ID: ""}}, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("blank signal id: %v", err)
	}
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "x", nil, nil, nil, extDest("")); kindOf(err) != KindInvalid {
		t.Fatalf("blank endpoint: %v", err)
	}
}

func TestUpdateTelemetryIntentScopeAndDestinationAudit(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", []string{"checkout"}, nil, nil, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}

	if err := e.core.UpdateTelemetryIntentScope(e.ctx, "alex", ti.ID, []string{"checkout", "payments"}, []string{"payments-canary"}, []store.SignalGrant{{ID: "traces", Source: "existing"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if len(got.Namespaces) != 2 || len(got.Exclude) != 1 || len(got.Signals) != 1 || got.Signals[0].ID != "traces" {
		t.Fatalf("%+v", got)
	}

	if err := e.core.UpdateTelemetryIntentDestination(e.ctx, "alex", ti.ID, extDest("collector2.example:4317")); err != nil {
		t.Fatal(err)
	}
	got, _ = e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if got.Destination.Endpoint != "collector2.example:4317" {
		t.Fatalf("%+v", got)
	}

	evs, err := e.st.ListAudit(e.ctx, "org-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	wantActions := map[string]bool{"telemetry-intent-created": false, "telemetry-intent-scope-changed": false, "telemetry-intent-destination-changed": false}
	for _, ev := range evs {
		if ev.TargetID == ti.ID {
			if _, ok := wantActions[ev.Action]; ok {
				wantActions[ev.Action] = true
			}
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Fatalf("no %q audit row for %s among %+v", action, ti.ID, evs)
		}
	}
}

func TestRevokeThenDeleteTelemetryIntentLifecycle(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, nil, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.RevokeTelemetryIntent(e.ctx, "alex", ti.ID, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if got.Status != store.TelemetryIntentRevoked || got.Reason != "decommissioned" {
		t.Fatalf("%+v", got)
	}
	if err := e.core.RevokeTelemetryIntent(e.ctx, "alex", ti.ID, "again"); kindOf(err) != KindConflict {
		t.Fatalf("expected KindConflict revoking twice, got %v", err)
	}
	// With the first intent revoked, a fresh one on the same agent is allowed again.
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge-2", nil, nil, nil, extDest("collector3.example:4317")); err != nil {
		t.Fatalf("expected a new active intent to be allowed once the old one is revoked: %v", err)
	}

	if err := e.core.DeleteTelemetryIntent(e.ctx, "alex", ti.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.core.GetTelemetryIntent(e.ctx, ti.ID); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound after delete, got %v", err)
	}
}

func TestTelemetryIntentsAreScopedToTheirOrganisation(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, nil, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: *e.now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	other := e.base.ForOrg("org-2")
	if _, err := other.GetTelemetryIntent(e.ctx, ti.ID); kindOf(err) != KindNotFound {
		t.Fatalf("a telemetry intent from another organisation must be invisible, got %v", err)
	}
	list, err := other.ListTelemetryIntents(e.ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("cross-org leak in ListTelemetryIntents: %+v %v", list, err)
	}
	if _, err := other.ListTelemetryIntentsForAgent(e.ctx, agentID); kindOf(err) != KindNotFound {
		t.Fatalf("an agent from another organisation must be invisible, got %v", err)
	}
}

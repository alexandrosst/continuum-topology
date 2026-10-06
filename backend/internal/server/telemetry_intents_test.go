package server

import (
	"strings"
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

// TestCreateTelemetryIntentAcceptsAnOperatorDestination exercises CreateTelemetryIntent with a
// destination naming a real, active regional operator - previously untested, since validateDestination
// unconditionally failed it before this release gave DestinationOperator a real check (see
// Core.validateDestination's own comment on the two-tier fleet it now supports).
func TestCreateTelemetryIntentAcceptsAnOperatorDestination(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}

	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, nil, dest)
	if err != nil {
		t.Fatalf("expected an active in-org operator destination to be accepted, got %v", err)
	}
	if ti.Destination.Kind != store.DestinationOperator || ti.Destination.TargetOperatorID != op.ID {
		t.Fatalf("%+v", ti.Destination)
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

// TestCreateTelemetryIntentRejectsASignalTheTargetOperatorDoesNotAccept confirms
// Core.checkOperatorAcceptsSignals is actually wired in: an operator that narrowed
// AcceptedModalities to metrics must reject a TelemetryIntent granting traces into it, naming the
// offending signal in the error (see telemetry-intent.md's "Where a grant exports to" - previously this
// was checked nowhere).
func TestCreateTelemetryIntentRejectsASignalTheTargetOperatorDoesNotAccept(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	_, err = e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "traces", Source: "existing"}}, dest)
	if kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid granting traces into a metrics-only operator, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "traces") {
		t.Fatalf("expected the error to name the offending signal, got %v", err)
	}
}

// TestCreateTelemetryIntentAcceptsCompatibleSignalsForANarrowedOperator is the positive counterpart of
// the above: metrics-shaped signals into a metrics-only operator must still succeed.
func TestCreateTelemetryIntentAcceptsCompatibleSignalsForANarrowedOperator(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	_, err = e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}, {ID: "kubernetesState", Source: "builtin"}}, dest)
	if err != nil {
		t.Fatalf("expected metrics signals into a metrics-only operator to be accepted, got %v", err)
	}
}

// TestCreateTelemetryIntentAllowsAnythingIntoAnOperatorWithNoModalityRestriction is a regression check:
// an operator with an empty AcceptedModalities must keep accepting every signal, the behaviour that
// existed before AcceptedModalities did.
func TestCreateTelemetryIntentAllowsAnythingIntoAnOperatorWithNoModalityRestriction(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	_, err = e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "traces", Source: "existing"}}, dest)
	if err != nil {
		t.Fatalf("expected an operator with no AcceptedModalities to accept anything, got %v", err)
	}
}

// TestUpdateTelemetryIntentScopeRejectsAnIncompatibleSignalAgainstTheCurrentOperatorDestination confirms
// the re-check UpdateTelemetryIntentScope must make against the intent's CURRENT (unchanged) destination
// when only the signals are changing.
func TestUpdateTelemetryIntentScopeRejectsAnIncompatibleSignalAgainstTheCurrentOperatorDestination(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}}, dest)
	if err != nil {
		t.Fatal(err)
	}
	err = e.core.UpdateTelemetryIntentScope(e.ctx, "alex", ti.ID, nil, nil,
		[]store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}, {ID: "traces", Source: "existing"}})
	if kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid adding a traces signal against a metrics-only operator destination, got %v", err)
	}
}

// TestUpdateTelemetryIntentDestinationRejectsAnIncompatibleOperatorAgainstTheCurrentSignals confirms the
// re-check UpdateTelemetryIntentDestination must make against the intent's CURRENT signals when only the
// destination is changing: a metrics-only intent cannot be repointed at a traces-only operator.
func TestUpdateTelemetryIntentDestinationRejectsAnIncompatibleOperatorAgainstTheCurrentSignals(t *testing.T) {
	e := newEnv(t)
	agentID := e.approvedAgentID(t, fp)
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	opCluster := e.approvedCluster(t, fp2)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector2.example:4317"), []store.Modality{store.ModalityTraces})
	if err != nil {
		t.Fatal(err)
	}
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	if err := e.core.UpdateTelemetryIntentDestination(e.ctx, "alex", ti.ID, dest); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid repointing a metrics-only intent at a traces-only operator, got %v", err)
	}
}

// TestCreateTelemetryIntentDoesNotBlockAnUnrecognisedSignalID confirms the fail-open behaviour for a
// signal id signalModality does not recognise (a newer frontend's not-yet-known id): it must be let
// through rather than treated as a mismatch, since this backend cannot vouch for its modality either way.
func TestCreateTelemetryIntentDoesNotBlockAnUnrecognisedSignalID(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	_, err = e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil,
		[]store.SignalGrant{{ID: "futureSignal", Source: "existing"}}, dest)
	if err != nil {
		t.Fatalf("expected an unrecognised signal id to be let through unchecked, got %v", err)
	}
}

// An intent that sends each signal type somewhere of its own is checked per signal: a metrics-only operator is
// a fine home for the metrics route of an intent that also grants logs, as long as logs go elsewhere.
func TestIntentRoutesAreCheckedPerSignalAgainstWhereEachGoes(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.approvedAgentID(t, fp2)
	signals := []store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}, {ID: "systemLogs", Source: "builtin"}}
	ti, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, signals, extDest("gw:4317"))
	if err != nil {
		t.Fatal(err)
	}
	opDest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	// Everything to the metrics-only operator: refused, logs cannot go there.
	if err := e.core.UpdateTelemetryIntentDestination(e.ctx, "alex", ti.ID, opDest); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid sending logs to a metrics-only operator, got %v", err)
	}
	// Metrics to it and logs to an endpoint: fine.
	routes := map[store.Modality]store.Destination{store.ModalityMetrics: opDest, store.ModalityLogs: extDest("loki:3100")}
	if err := e.core.UpdateTelemetryIntentDestinations(e.ctx, "alex", ti.ID, extDest("gw:4317"), routes); err != nil {
		t.Fatalf("routing metrics to the operator and logs elsewhere: %v", err)
	}
	got, _ := e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if len(got.Routes) != 2 || got.Routes[store.ModalityMetrics].TargetOperatorID != op.ID {
		t.Fatalf("routes = %+v", got.Routes)
	}
	// Routing LOGS to it is the one that is not allowed.
	bad := map[store.Modality]store.Destination{store.ModalityLogs: opDest}
	if err := e.core.UpdateTelemetryIntentDestinations(e.ctx, "alex", ti.ID, extDest("gw:4317"), bad); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid routing logs to a metrics-only operator, got %v", err)
	}
	// Widening the scope is checked against the routes too: traces have no route, so they follow the default.
	if err := e.core.UpdateTelemetryIntentScope(e.ctx, "alex", ti.ID, nil, nil, append(signals, store.SignalGrant{ID: "traces", Source: "existing"})); err != nil {
		t.Fatalf("traces follow the external default and should be fine: %v", err)
	}
	// ...and a route that is not one of the three signal types, or names an operator that is not there, is refused.
	if err := e.core.UpdateTelemetryIntentDestinations(e.ctx, "alex", ti.ID, extDest("gw:4317"), map[store.Modality]store.Destination{"events": extDest("x:1")}); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for an unknown signal type, got %v", err)
	}
	ghost := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: "op-nope"}
	if err := e.core.UpdateTelemetryIntentDestinations(e.ctx, "alex", ti.ID, extDest("gw:4317"), map[store.Modality]store.Destination{store.ModalityMetrics: ghost}); err == nil {
		t.Fatal("routed metrics to an operator that does not exist")
	}
	// An empty routes clears them.
	if err := e.core.UpdateTelemetryIntentDestinations(e.ctx, "alex", ti.ID, extDest("gw:4317"), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.core.GetTelemetryIntent(e.ctx, ti.ID); len(got.Routes) != 0 {
		t.Fatalf("routes not cleared: %+v", got.Routes)
	}
}

// Creating an intent with routes checks each signal against where it goes: a first lane that is a metrics-only
// operator does not make the whole intent refuse the logs that go to an endpoint.
func TestCreateTelemetryIntentWithRoutesChecksEachSignalWhereItGoes(t *testing.T) {
	e := newEnv(t)
	opCluster := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{opCluster}, extDest("collector.example:4317"), []store.Modality{store.ModalityMetrics})
	if err != nil {
		t.Fatal(err)
	}
	opDest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: op.ID}
	signals := []store.SignalGrant{{ID: "resourceUsage", Source: "builtin"}, {ID: "systemLogs", Source: "builtin"}}
	routes := map[store.Modality]store.Destination{store.ModalityMetrics: opDest, store.ModalityLogs: extDest("loki:3100")}

	agentID := e.approvedAgentID(t, fp2)
	// Without routes, the same grant is refused: the default would have to carry the logs.
	if _, err := e.core.CreateTelemetryIntent(e.ctx, "alex", agentID, "patras-edge", nil, nil, signals, opDest); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid without routes, got %v", err)
	}
	ti, err := e.core.CreateTelemetryIntentWithRoutes(e.ctx, "alex", agentID, "patras-edge", nil, nil, signals, opDest, routes)
	if err != nil {
		t.Fatalf("with routes: %v", err)
	}
	got, _ := e.core.GetTelemetryIntent(e.ctx, ti.ID)
	if len(got.Routes) != 2 || got.Routes[store.ModalityLogs].Endpoint != "loki:3100" {
		t.Fatalf("routes after create = %+v", got.Routes)
	}
}

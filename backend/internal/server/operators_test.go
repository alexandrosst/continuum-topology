package server

import (
	"testing"

	"continuum/internal/store"
)

const fp2 = "8f3c2a9e-2222-4222-8333-944455556677"

// approvedCluster enrolls and approves one agent, returning the cluster id CreateOperator's own
// validation checks source clusters against.
func (e *env) approvedCluster(t *testing.T, fingerprint string) string {
	t.Helper()
	resp, _ := e.enroll(t, 2, fingerprint)
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fingerprint, 1); err != nil {
		t.Fatal(err)
	}
	a, err := e.st.GetAgent(e.ctx, resp.AgentId)
	if err != nil {
		t.Fatal(err)
	}
	return a.ClusterID
}

func extDest(endpoint string) store.Destination {
	return store.Destination{Kind: store.DestinationExternal, Endpoint: endpoint}
}

func TestCreateOperatorHappyPathMintsASecretOnce(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, secret, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		t.Fatal("no secret returned")
	}
	if op.Status != store.OperatorActive || len(op.SourceClusterIDs) != 1 || op.SourceClusterIDs[0] != cl {
		t.Fatalf("%+v", op)
	}
	stored, err := e.st.GetOperator(e.ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.ReceiverAuthTokenHash) == secret {
		t.Fatal("the plaintext secret must never be stored")
	}
	if len(stored.ReceiverAuthTokenHash) == 0 {
		t.Fatal("no hash stored")
	}
}

func TestCreateOperatorRejectsAnUnapprovedOrForeignClusterID(t *testing.T) {
	e := newEnv(t)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{"cl-does-not-exist"}, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for an unknown cluster id, got %v", err)
	}
	// A pending (not yet approved) agent's cluster is not eligible either - it has no ClusterID yet.
	e.enroll(t, 2, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{""}, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for an empty cluster id, got %v", err)
	}
}

func TestCreateOperatorRejectsChainingToAnotherOperator(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: "op-other"}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl}, dest); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid rejecting operator chaining, got %v", err)
	}
}

func TestCreateOperatorValidatesNameAndEndpoint(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "   ", []string{cl}, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("blank name: %v", err)
	}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl}, extDest("")); kindOf(err) != KindInvalid {
		t.Fatalf("blank endpoint: %v", err)
	}
}

func TestCreateOperatorRejectsADuplicateSourceCluster(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl, cl}, extDest("c:4317")); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for a duplicate source cluster, got %v", err)
	}
}

func TestUpdateScopeRevokeAndDeleteOperator(t *testing.T) {
	e := newEnv(t)
	clA := e.approvedCluster(t, fp)
	clB := e.approvedCluster(t, fp2)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{clA}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}

	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clA, clB}, extDest("collector2.example:4317")); err != nil {
		t.Fatal(err)
	}
	got, _ := e.core.GetOperator(e.ctx, op.ID)
	if len(got.SourceClusterIDs) != 2 || got.Destination.Endpoint != "collector2.example:4317" {
		t.Fatalf("%+v", got)
	}

	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.core.GetOperator(e.ctx, op.ID)
	if got.Status != store.OperatorRevoked || got.Reason != "decommissioned" {
		t.Fatalf("%+v", got)
	}
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clA}, extDest("x:4317")); kindOf(err) != KindConflict {
		t.Fatalf("expected KindConflict changing a revoked operator's scope, got %v", err)
	}
	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "again"); kindOf(err) != KindConflict {
		t.Fatalf("expected KindConflict revoking twice, got %v", err)
	}

	if err := e.core.DeleteOperator(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.core.GetOperator(e.ctx, op.ID); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound after delete, got %v", err)
	}
}

func TestOperatorsAreScopedToTheirOrganisation(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: *e.now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	other := e.base.ForOrg("org-2")
	if _, err := other.GetOperator(e.ctx, op.ID); kindOf(err) != KindNotFound {
		t.Fatalf("an operator from another organisation must be invisible, got %v", err)
	}
	list, err := other.ListOperators(e.ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("cross-org leak in ListOperators: %+v %v", list, err)
	}
}

func TestCreateOperatorAudits(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := e.st.ListAudit(e.ctx, "org-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Action == "operator-created" && ev.TargetID == op.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no operator-created audit row among %+v", evs)
	}
}

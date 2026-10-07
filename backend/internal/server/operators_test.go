package server

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

// approvedAgentID enrolls and approves one agent, returning its agent id - what CreateTelemetryIntent's
// own validation targets directly, unlike CreateOperator which targets a cluster id (see approvedCluster).
func (e *env) approvedAgentID(t *testing.T, fingerprint string) string {
	t.Helper()
	resp, _ := e.enroll(t, 2, fingerprint)
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fingerprint, 1); err != nil {
		t.Fatal(err)
	}
	return resp.AgentId
}

func extDest(endpoint string) store.Destination {
	return store.Destination{Kind: store.DestinationExternal, Endpoint: endpoint}
}

func TestCreateOperatorHappyPathIsMTLSOnlyWithNoReceiverToken(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, secret, bundle, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		t.Fatalf("an mTLS operator must not be handed a receiver bearer token, got %q", secret)
	}
	if len(bundle.ReceiverCertPEM) == 0 || len(bundle.Senders) != 1 {
		t.Fatal("the TLS material that IS the receiver's gate was not minted")
	}
	if op.Status != store.OperatorActive || len(op.SourceClusterIDs) != 1 || op.SourceClusterIDs[0] != cl || op.ReceiverAuth != store.ReceiverAuthMTLS {
		t.Fatalf("%+v", op)
	}
	stored, err := e.st.GetOperator(e.ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReceiverAuth != store.ReceiverAuthMTLS || len(stored.ReceiverAuthTokenHash) != 0 {
		t.Fatalf("stored operator = %+v, want mtls with no token hash", stored)
	}
}

// An operator is a place to send to; clusters are pointed at it afterwards, so it needs no source at creation.
func TestCreateOperatorWithNoSourceClustersIsValid(t *testing.T) {
	e := newEnv(t)
	op, _, bundle, err := e.core.CreateOperator(e.ctx, "alex", "plant-7", nil, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(op.SourceClusterIDs) != 0 || len(bundle.ReceiverCertPEM) == 0 {
		t.Fatalf("%+v", op)
	}
}

func TestCreateOperatorRejectsAnUnapprovedOrForeignClusterID(t *testing.T) {
	e := newEnv(t)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{"cl-does-not-exist"}, extDest("c:4317"), nil); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for an unknown cluster id, got %v", err)
	}
	// A pending (not yet approved) agent's cluster is not eligible either - it has no ClusterID yet.
	e.enroll(t, 2, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{""}, extDest("c:4317"), nil); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for an empty cluster id, got %v", err)
	}
}

// TestCreateOperatorAcceptsChainingToAnActiveOperatorInOrg covers the three ways a destination naming
// another regional operator is checked: a currently active operator in the same organisation is accepted
// (a two-tier fleet - see validateDestination's own comment), a revoked one is rejected, and an unknown
// or foreign-org one is reported not found rather than forbidden, the same convention operatorInOrg
// itself follows.
func TestCreateOperatorAcceptsChainingToAnActiveOperatorInOrg(t *testing.T) {
	e := newEnv(t)
	clA := e.approvedCluster(t, fp)
	target, _, _, err := e.core.CreateOperator(e.ctx, "alex", "upstream", []string{clA}, extDest("upstream.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}

	clB := e.approvedCluster(t, fp2)
	dest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: target.ID}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "downstream", []string{clB}, dest, nil); err != nil {
		t.Fatalf("expected an active in-org operator destination to be accepted, got %v", err)
	}

	// An operator that something sends to is not revoked by accident; forced, it is.
	if err := e.core.RevokeOperator(e.ctx, "alex", target.ID, "decommissioned"); kindOf(err) != KindConflict {
		t.Fatalf("revoking an operator another one exports into: %v", err)
	}
	if err := e.core.RevokeOperatorForce(e.ctx, "alex", target.ID, "decommissioned", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "downstream-2", []string{clB}, dest, nil); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid chaining to a revoked operator, got %v", err)
	}

	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "downstream-3", []string{clB}, store.Destination{Kind: store.DestinationOperator, TargetOperatorID: "op-does-not-exist"}, nil); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound chaining to an unknown operator, got %v", err)
	}

	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: *e.now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	// validateDestination runs before validSourceClusters (see CreateOperator), so an invalid source
	// cluster list does not mask the destination check this is exercising.
	other := e.base.ForOrg("org-2")
	foreignDest := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: target.ID}
	if _, _, _, err := other.CreateOperator(e.ctx, "alex", "cross-org", []string{"irrelevant"}, foreignDest, nil); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound chaining to another organisation's operator, got %v", err)
	}
}

func TestCreateOperatorValidatesNameAndEndpoint(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "   ", []string{cl}, extDest("c:4317"), nil); kindOf(err) != KindInvalid {
		t.Fatalf("blank name: %v", err)
	}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl}, extDest(""), nil); kindOf(err) != KindInvalid {
		t.Fatalf("blank endpoint: %v", err)
	}
}

func TestCreateOperatorRejectsADuplicateSourceCluster(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl, cl}, extDest("c:4317"), nil); kindOf(err) != KindInvalid {
		t.Fatalf("expected KindInvalid for a duplicate source cluster, got %v", err)
	}
}

func TestUpdateScopeRevokeAndDeleteOperator(t *testing.T) {
	e := newEnv(t)
	clA := e.approvedCluster(t, fp)
	clB := e.approvedCluster(t, fp2)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{clA}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clA, clB}, extDest("collector2.example:4317"), nil); err != nil {
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
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clA}, extDest("x:4317"), nil); kindOf(err) != KindConflict {
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
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"), nil)
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
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"), nil)
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

// TestIssueOperatorClientCertReissuesOnDemand covers IssueOperatorClientCert's own promise: it mints a
// fresh, independently valid client certificate every time it is called (not once, cached), and refuses
// to do so for an operator that is not active, does not exist, or belongs to another organisation - the
// same not-found-not-forbidden convention operatorInOrg already uses for the last of those.
func TestIssueOperatorClientCertReissuesOnDemand(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{cl}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM, caPEM, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", "")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("not a PEM certificate: %q", certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("invalid certificate: %v", err)
	}
	if cert.Subject.CommonName != op.ID+"-export-cl-test" {
		t.Fatalf("client identity = %v", cert.Subject)
	}
	if kb, _ := pem.Decode(keyPEM); kb == nil || kb.Type != "EC PRIVATE KEY" {
		t.Fatalf("not a PEM EC private key: %q", keyPEM)
	}
	if cb, _ := pem.Decode(caPEM); cb == nil || cb.Type != "CERTIFICATE" {
		t.Fatalf("not a PEM CA certificate: %q", caPEM)
	}

	// "On demand" means on demand, not minted once and cached: a second call returns a second,
	// independently valid certificate rather than replaying the first.
	certPEM2, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(certPEM) == string(certPEM2) {
		t.Fatal("expected a fresh certificate on each call, got the same bytes twice")
	}

	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", ""); kindOf(err) != KindConflict {
		t.Fatalf("expected KindConflict reissuing for a revoked operator, got %v", err)
	}

	if _, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", "op-does-not-exist", "cl-test", ""); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound reissuing for an unknown operator, got %v", err)
	}

	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: *e.now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	other := e.base.ForOrg("org-2")
	if _, _, _, err := other.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", ""); kindOf(err) != KindNotFound {
		t.Fatalf("expected KindNotFound reissuing for another organisation's operator, got %v", err)
	}
}

func TestOperatorLabelsAreValidatedTrimmedAndStored(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	create := func(labels ...store.OperatorLabel) (store.Operator, error) {
		op, _, _, _, err := e.core.CreateOperatorWithOptions(e.ctx, "alex", "op-"+t.Name(), []string{cl}, extDest("c:4317"), nil, OperatorOptions{Labels: labels})
		return op, err
	}
	bad := map[string][]store.OperatorLabel{
		"reserved prefix": {{Key: "continuum.region", Value: "x"}},
		"empty key":       {{Key: " ", Value: "x"}},
		"bad key":         {{Key: "has space", Value: "x"}},
		"empty value":     {{Key: "region", Value: "  "}},
		"long value":      {{Key: "region", Value: strings.Repeat("v", 65)}},
		"control char":    {{Key: "region", Value: "a\nb"}},
		"duplicate":       {{Key: "region", Value: "a"}, {Key: "region", Value: "b"}},
	}
	for name, labels := range bad {
		if _, err := create(labels...); kindOf(err) != KindInvalid {
			t.Fatalf("%s: err = %v, want KindInvalid", name, err)
		}
	}
	tooMany := make([]store.OperatorLabel, 0, maxOperatorLabels+1)
	for i := 0; i <= maxOperatorLabels; i++ {
		tooMany = append(tooMany, store.OperatorLabel{Key: fmt.Sprintf("k%d", i), Value: "v"})
	}
	if _, err := create(tooMany...); kindOf(err) != KindInvalid {
		t.Fatalf("too many labels: err = %v", err)
	}
	op, err := create(store.OperatorLabel{Key: " region ", Value: " eu-south "})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := e.st.GetOperator(e.ctx, op.ID)
	if err != nil || len(stored.Labels) != 1 || stored.Labels[0] != (store.OperatorLabel{Key: "region", Value: "eu-south"}) {
		t.Fatalf("stored labels = %+v (%v)", stored.Labels, err)
	}
}

// An operator that exports to itself, or round a ring of operators back to itself (A -> B -> A), would pass telemetry
// from one to the next for ever and never reach a backend. Both are refused when an operator's scope is changed, and a
// new operator is refused a target whose chain already loops. A plain chain is still fine.
func TestOperatorDestinationsCannotLoop(t *testing.T) {
	e := newEnv(t)
	mk := func(name string, dest store.Destination) store.Operator {
		t.Helper()
		op, _, _, err := e.core.CreateOperator(e.ctx, "alex", name, nil, dest, nil)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	toOp := func(id string) store.Destination {
		return store.Destination{Kind: store.DestinationOperator, TargetOperatorID: id}
	}
	a := mk("a", extDest("backend.example:4317"))
	b := mk("b", toOp(a.ID))
	c := mk("c", toOp(b.ID)) // a plain chain c -> b -> a -> backend

	if err := e.core.UpdateOperatorScope(e.ctx, "alex", a.ID, nil, toOp(a.ID), nil); kindOf(err) != KindInvalid {
		t.Fatalf("an operator exporting to itself: %v", err)
	}
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", a.ID, nil, toOp(b.ID), nil); kindOf(err) != KindInvalid {
		t.Fatalf("a -> b -> a: %v", err)
	}
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", a.ID, nil, toOp(c.ID), nil); kindOf(err) != KindInvalid {
		t.Fatalf("a -> c -> b -> a: %v", err)
	}
	got, _ := e.core.GetOperator(e.ctx, a.ID)
	if got.Destination.Kind != store.DestinationExternal {
		t.Fatalf("a refused change was stored: %+v", got.Destination)
	}
	// Pointing elsewhere is still allowed: a backend, and a sibling that does not lead back.
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", a.ID, nil, extDest("other.example:4317"), nil); err != nil {
		t.Fatal(err)
	}
	d := mk("d", extDest("x.example:4317"))
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", b.ID, nil, toOp(d.ID), nil); err != nil {
		t.Fatalf("a re-pointed chain that does not loop: %v", err)
	}

	// A loop that is already in the database (from before this check) must not be followed for ever, and nothing new may
	// be sent into it.
	if err := e.st.UpdateOperatorScope(e.ctx, d.ID, nil, toOp(c.ID), nil); err != nil { // d -> c -> b -> d
		t.Fatal(err)
	}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "e", nil, toOp(c.ID), nil); kindOf(err) != KindInvalid {
		t.Fatalf("a new operator exporting into an existing loop: %v", err)
	}
}

// auditDetail is the detail of the newest audit row with that action for an operator.
func (e *env) auditDetail(t *testing.T, action, id string) string {
	t.Helper()
	evs, err := e.st.ListAudit(e.ctx, "org-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs { // newest first
		if ev.Action == action && ev.TargetID == id {
			return ev.Detail
		}
	}
	t.Fatalf("no %s row for %s among %+v", action, id, evs)
	return ""
}

// The audit rows for creating an operator and for changing its scope say where it sends and which clusters came and went:
// enough to answer "who pointed this at what, and when" from the trail alone, and nothing that is a secret.
func TestOperatorAuditRowsSayWhereItSendsAndWhoCameAndWent(t *testing.T) {
	e := newEnv(t)
	clA := e.approvedCluster(t, fp)
	clB := e.approvedCluster(t, fp2)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens-regional", []string{clA}, extDest("collector.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.auditDetail(t, "operator-created", op.ID); d != "athens-regional; to external collector.example:4317; 1 source cluster" {
		t.Fatalf("operator-created detail = %q", d)
	}

	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clB}, extDest("other.example:4317"), nil); err != nil {
		t.Fatal(err)
	}
	want := "to external other.example:4317; 1 source cluster; added " + clB + "; removed " + clA
	if d := e.auditDetail(t, "operator-scope-changed", op.ID); d != want {
		t.Fatalf("operator-scope-changed detail = %q, want %q", d, want)
	}

	// Chained to another operator: its id, not an endpoint. Nothing changed in the clusters: no added/removed.
	up, _, _, err := e.core.CreateOperator(e.ctx, "alex", "upstream", nil, extDest("up.example:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{clB}, store.Destination{Kind: store.DestinationOperator, TargetOperatorID: up.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if d := e.auditDetail(t, "operator-scope-changed", op.ID); d != "to operator "+up.ID+"; 1 source cluster" {
		t.Fatalf("a chained scope change: %q", d)
	}
}

func TestAuditDetailListsAreBounded(t *testing.T) {
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}
	if got := auditIDList(ids); got != "c1, c2, c3, c4, c5 and 2 more" {
		t.Fatalf("auditIDList = %q", got)
	}
	if got := auditIDList(ids[:2]); got != "c1, c2" {
		t.Fatalf("auditIDList = %q", got)
	}
	added, removed := idChanges([]string{"a", "b", "c"}, []string{"b", "d", "e"})
	if strings.Join(added, ",") != "d,e" || strings.Join(removed, ",") != "a,c" {
		t.Fatalf("idChanges = %v %v", added, removed)
	}
	// A hostile endpoint (or one that is merely long) is clipped, and the whole row stays within what a row may hold.
	long := store.Destination{Kind: store.DestinationExternal, Endpoint: strings.Repeat("a", 400)}
	if d := operatorAuditSummary(long, nil); len(d) > 200 || !strings.Contains(d, "0 source clusters") {
		t.Fatalf("summary = %q", d)
	}
}

// ledgerStore is a store whose certificate ledger can be made to fail.
type ledgerStore struct {
	store.Store
	mu   sync.Mutex
	fail bool
}

func (s *ledgerStore) setFail(v bool) { s.mu.Lock(); s.fail = v; s.mu.Unlock() }

func (s *ledgerStore) AddOperatorCert(ctx context.Context, c store.OperatorCert) error {
	s.mu.Lock()
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return errors.New("ledger unavailable")
	}
	return s.Store.AddOperatorCert(ctx, c)
}

// A receiver certificate that cannot be recorded in the ledger is not handed out. At creation nothing exists yet, so the
// request fails and no operator is left behind; on a reissue nothing has been rotated yet, so the operator keeps working
// with what it has and the person simply runs it again.
func TestAReceiverCertificateThatCannotBeRecordedIsNotHandedOut(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	ls := &ledgerStore{Store: e.core.Store, fail: true}
	core := e.base.ForOrg("org-1")
	core.Store = ls

	if _, _, _, err := core.CreateOperator(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil); err == nil {
		t.Fatal("an operator was created whose certificate could not be recorded")
	}
	if ops, _ := e.st.ListOperators(e.ctx, "org-1"); len(ops) != 0 {
		t.Fatalf("a failed creation left %d operators behind", len(ops))
	}

	ls.setFail(false)
	op, _, _, _, err := core.CreateOperatorWithHeartbeat(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ls.setFail(true)
	before, _ := e.st.GetOperator(e.ctx, op.ID)
	if _, _, err := core.ReissueOperatorInstall(e.ctx, "alex", op.ID); err == nil {
		t.Fatal("a reissue succeeded although its certificate could not be recorded")
	}
	after, _ := e.st.GetOperator(e.ctx, op.ID)
	if string(after.HeartbeatHash) != string(before.HeartbeatHash) || !after.HeartbeatEnabledAt.Equal(*before.HeartbeatEnabledAt) {
		t.Fatal("the heartbeat secret was rotated by a reissue that failed: its new value was never shown")
	}
	ls.setFail(false)
	if _, out, err := core.ReissueOperatorInstall(e.ctx, "alex", op.ID); err != nil || len(out.TLS.ReceiverCertPEM) == 0 || out.HeartbeatSecret == "" {
		t.Fatalf("a reissue once the ledger is back: %v", err)
	}
}

// Issuing a certificate for an operator and reissuing its install both hold depMu for reading, like creating an operator
// or changing its scope: a revoke or delete (which takes the write lock) waits for them, and they wait for it, so a
// certificate is never minted and recorded for an operator that was revoked in the middle.
func TestCertificateIssuingIsOrderedAgainstRevokeAndDelete(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"IssueOperatorClientCertFor": func() error {
			_, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, cl, "")
			return err
		},
		"ReissueOperatorInstall": func() error { _, _, err := e.core.ReissueOperatorInstall(e.ctx, "alex", op.ID); return err },
	} {
		e.core.depMu.Lock() // what a revoke or delete holds while it checks dependents and removes
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			e.core.depMu.Unlock()
			t.Fatalf("%s ran while a revoke or delete held the lock (err %v)", name, err)
		case <-time.After(150 * time.Millisecond):
		}
		e.core.depMu.Unlock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never ran after the lock was released", name)
		}
	}
}

// Creating an operator with source clusters, and reissuing one, issue client certificates inside their own hold of the lock;
// with a revoke queued behind them they must not wait on themselves.
func TestIssuingInsideTheLockDoesNotDeadlockWithAWaitingRevoke(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil); err == nil {
					_, _, _ = e.core.ReissueOperatorInstall(e.ctx, "alex", op.ID)
				}
			}()
			go func() { // a writer in between
				defer wg.Done()
				e.core.depMu.Lock()
				e.core.depMu.Unlock()
			}()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock: an issuing call waited for the lock it already held")
	}
}

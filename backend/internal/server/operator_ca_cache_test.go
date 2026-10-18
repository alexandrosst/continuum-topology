package server

import (
	"sync/atomic"
	"testing"

	"continuum/internal/operators"
	"continuum/internal/pki"
)

// countOpens counts how many times an operator CA is really opened (each is an Argon2id run) for the rest of the test.
func countOpens(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := operators.OpenOperatorCA
	operators.OpenOperatorCA = func(c *operators.Service, certPEM, keyPEM []byte) (*pki.CA, error) {
		n.Add(1)
		return old(c, certPEM, keyPEM)
	}
	t.Cleanup(func() { operators.OpenOperatorCA = old })
	return &n
}

// Every certificate issued for an operator used to open its CA again; now the first does and the rest reuse it.
func TestOperatorCAIsOpenedOnceNotOncePerCertificate(t *testing.T) {
	opens := countOpens(t)
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.Operators.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, _, _, err := e.core.Operators.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.core.Operators.ReissueOperatorInstall(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("the CA was opened %d times for 5 issues", got)
	}
}

// A revoked or deleted operator's key does not stay in memory.
func TestRevokingOrDeletingAnOperatorDropsItsOpenedCA(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	mk := func(name string) string {
		op, _, _, err := e.core.Operators.CreateOperator(e.ctx, "alex", name, []string{cl}, extDest("c:4317"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := e.core.Operators.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, "cl-test", ""); err != nil {
			t.Fatal(err)
		}
		return op.ID
	}
	cached := func(id string) bool {
		return e.base.Operators.HasOpenedCA(id)
	}
	a, b := mk("a"), mk("b")
	if !cached(a) || !cached(b) {
		t.Fatal("test setup: nothing cached")
	}
	if err := e.core.Operators.RevokeOperator(e.ctx, "alex", a, "gone"); err != nil {
		t.Fatal(err)
	}
	if cached(a) || !cached(b) {
		t.Fatalf("after revoking a: a cached %v, b cached %v", cached(a), cached(b))
	}
	if err := e.core.Operators.DeleteOperator(e.ctx, "alex", b); err != nil {
		t.Fatal(err)
	}
	if cached(b) {
		t.Fatal("a deleted operator's CA is still held")
	}
}

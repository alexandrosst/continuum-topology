package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

func keyFromPEM(t *testing.T, keyPEM []byte) *ecdsa.PrivateKey {
	t.Helper()
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		t.Fatal("not a PEM key")
	}
	k, err := x509.ParseECPrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// renewalFor is what a holder of certPEM/keyPEM sends: the old certificate, a request for a NEW key, and the old key's
// proof over that request.
func renewalFor(t *testing.T, certPEM, keyPEM []byte) (*continuumv1.RenewTelemetryCertRequest, *ecdsa.PrivateKey) {
	t.Helper()
	oldCert, err := pki.ParseCertificate(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER := csrWith(t, newKey)
	proof, err := pki.ProveRenewal(keyFromPEM(t, keyPEM), csrDER)
	if err != nil {
		t.Fatal(err)
	}
	return &continuumv1.RenewTelemetryCertRequest{OldLeafDer: oldCert.Raw, CsrDer: csrDER, Proof: proof}, newKey
}

type renewSetup struct {
	e      *env
	op     store.Operator
	bundle OperatorTLSBundle
	cl     string
	agent  string
}

func newRenewSetup(t *testing.T) renewSetup {
	t.Helper()
	e := newEnv(t)
	agent := e.approvedAgentID(t, fp)
	a, _ := e.st.GetAgent(e.ctx, agent)
	op, _, bundle, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{a.ClusterID}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Senders) != 1 {
		t.Fatal("expected one sender certificate")
	}
	return renewSetup{e: e, op: op, bundle: bundle, cl: a.ClusterID, agent: agent}
}

func TestSenderCertificateIsRenewedByProofOfTheOldKey(t *testing.T) {
	s := newRenewSetup(t)
	sc := s.bundle.Senders[0]
	req, newKey := renewalFor(t, sc.CertPEM, sc.KeyPEM)
	resp, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(resp.LeafDer)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := pki.ParseCertificate(sc.CertPEM)
	if leaf.Subject.CommonName != old.Subject.CommonName || leaf.Subject.Organization[0] != "org-1" {
		t.Fatalf("identity changed: %v -> %v", old.Subject, leaf.Subject)
	}
	if !leaf.PublicKey.(*ecdsa.PublicKey).Equal(&newKey.PublicKey) {
		t.Fatal("the new certificate is not for the new key")
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("usage = %v", leaf.ExtKeyUsage)
	}
	// It verifies against the operator's own CA, which the response carries, and against nothing else.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(resp.CaPem) {
		t.Fatal("no CA in the response")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: s.e.base.CA.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("the renewed certificate verifies against the org CA")
	}
	// The ledger knows who holds the new one, and the old one is untouched.
	ledger, _ := s.e.st.ListOperatorCerts(s.e.ctx, s.op.ID)
	var found bool
	for _, c := range ledger {
		if c.Serial == leaf.SerialNumber.Text(16) {
			found = true
			if c.Kind != store.OperatorCertClient || c.Sender != s.cl || c.IssuedBy != "renewal" {
				t.Fatalf("ledger row = %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("the renewal is not in the ledger")
	}
	// And the new certificate renews again, from its own key.
	nk, _ := x509.MarshalECPrivateKey(newKey)
	req2, _ := renewalFor(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.LeafDer}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: nk}))
	if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req2); err != nil {
		t.Fatalf("second renewal: %v", err)
	}
}

func TestReceiverCertificateIsRenewedWithTheSameNames(t *testing.T) {
	s := newRenewSetup(t)
	req, _ := renewalFor(t, s.bundle.ReceiverCertPEM, s.bundle.ReceiverKeyPEM)
	resp, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(resp.LeafDer)
	old, _ := pki.ParseCertificate(s.bundle.ReceiverCertPEM)
	if len(leaf.DNSNames) == 0 || len(leaf.DNSNames) != len(old.DNSNames) || leaf.Subject.CommonName != s.op.ID {
		t.Fatalf("names changed: %v -> %v (%s)", old.DNSNames, leaf.DNSNames, leaf.Subject.CommonName)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("usage = %v", leaf.ExtKeyUsage)
	}
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got < pki.OperatorTLSTTL-time.Hour || got > pki.OperatorTLSTTL+time.Hour {
		t.Fatalf("lives %v, want about %v", got, pki.OperatorTLSTTL)
	}
}

// A certificate from before the ledger existed has no row; the signature and the name are what identify it.
func TestACertificateWithNoLedgerRowStillRenews(t *testing.T) {
	s := newRenewSetup(t)
	issuer, _, err := s.e.core.operatorIssuer(s.e.ctx, s.op)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := issuer.IssueOperatorClientTLS(s.op.ID, "org-1", s.cl)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := renewalFor(t, certPEM, keyPEM)
	if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); err != nil {
		t.Fatalf("a certificate issued outside the ledger: %v", err)
	}
}

func TestRenewalNeedsThePossessionProof(t *testing.T) {
	s := newRenewSetup(t)
	sc := s.bundle.Senders[0]
	good, _ := renewalFor(t, sc.CertPEM, sc.KeyPEM)
	thief, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// someone with the certificate but not its key
	wrongKey := &continuumv1.RenewTelemetryCertRequest{OldLeafDer: good.OldLeafDer, CsrDer: good.CsrDer}
	wrongKey.Proof, _ = pki.ProveRenewal(thief, good.CsrDer)
	// the proof was made for a different request
	swapped := &continuumv1.RenewTelemetryCertRequest{OldLeafDer: good.OldLeafDer, CsrDer: csrWith(t, thief), Proof: good.Proof}
	empty := &continuumv1.RenewTelemetryCertRequest{OldLeafDer: good.OldLeafDer, CsrDer: good.CsrDer}
	for name, r := range map[string]*continuumv1.RenewTelemetryCertRequest{"wrong key": wrongKey, "other request": swapped, "no proof": empty, "garbage certificate": {OldLeafDer: []byte("x"), CsrDer: good.CsrDer, Proof: good.Proof}} {
		if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", r); kindOf(err) != KindUnauthenticated {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestACertificateFromAnotherCAIsNotRenewed(t *testing.T) {
	s := newRenewSetup(t)
	// The org CA and another operator's CA both sign certificates with the same names; neither is this operator's.
	other, _, _, _, err := s.e.core.CreateOperatorWithOptions(s.e.ctx, "alex", "sparta", []string{s.cl}, extDest("d:4317"), nil, OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issuer, _, _ := s.e.core.operatorIssuer(s.e.ctx, other)
	for name, ca := range map[string]*pki.CA{"org CA": s.e.base.CA, "another operator's CA": issuer} {
		certPEM, keyPEM, err := ca.IssueOperatorClientTLS(s.op.ID, "org-1", s.cl)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := renewalFor(t, certPEM, keyPEM)
		if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); kindOf(err) != KindUnauthenticated {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRenewalWindowRunsToTheGraceAfterExpiry(t *testing.T) {
	s := newRenewSetup(t)
	sc := s.bundle.Senders[0]
	old, _ := pki.ParseCertificate(sc.CertPEM)
	*s.e.now = old.NotAfter.Add(pki.OperatorRenewGrace - time.Hour)
	req, _ := renewalFor(t, sc.CertPEM, sc.KeyPEM)
	if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); err != nil {
		t.Fatalf("inside the grace: %v", err)
	}
	*s.e.now = old.NotAfter.Add(pki.OperatorRenewGrace + time.Hour)
	req, _ = renewalFor(t, sc.CertPEM, sc.KeyPEM)
	if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); kindOf(err) != KindUnauthenticated {
		t.Fatalf("past the grace: %v", err)
	}
}

// Revocation is entitlement ending: the certificate is simply not renewed. Each way a sender can stop being one is tried.
func TestEntitlementEndingStopsRenewal(t *testing.T) {
	t.Run("agent revoked", func(t *testing.T) {
		s := newRenewSetup(t)
		if err := s.e.core.Revoke(s.e.ctx, "alex", s.agent, "test"); err != nil {
			t.Fatal(err)
		}
		sc := s.bundle.Senders[0]
		req, _ := renewalFor(t, sc.CertPEM, sc.KeyPEM)
		if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); kindOf(err) != KindForbidden {
			t.Fatalf("a revoked agent's cluster was renewed: %v", err)
		}
		if n, _ := s.e.countAudit(t, "operator-cert-renewal-refused"); n != 1 {
			t.Fatalf("refusals audited = %d", n)
		}
	})
	t.Run("cluster taken out of scope", func(t *testing.T) {
		s := newRenewSetup(t)
		if err := s.e.core.UpdateOperatorScope(s.e.ctx, "alex", s.op.ID, nil, s.op.Destination, nil); err != nil {
			t.Fatal(err)
		}
		sc := s.bundle.Senders[0]
		req, _ := renewalFor(t, sc.CertPEM, sc.KeyPEM)
		if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); kindOf(err) != KindForbidden {
			t.Fatalf("a cluster no longer in scope was renewed: %v", err)
		}
	})
	t.Run("operator revoked", func(t *testing.T) {
		s := newRenewSetup(t)
		if err := s.e.core.RevokeOperator(s.e.ctx, "alex", s.op.ID, "test"); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ cert, key []byte }{{s.bundle.Senders[0].CertPEM, s.bundle.Senders[0].KeyPEM}, {s.bundle.ReceiverCertPEM, s.bundle.ReceiverKeyPEM}} {
			req, _ := renewalFor(t, c.cert, c.key)
			if _, err := s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); kindOf(err) != KindUnauthenticated {
				t.Fatalf("a revoked operator's certificate was renewed: %v", err)
			}
		}
	})
}

func TestRenewalIsRateLimitedPerCertificate(t *testing.T) {
	s := newRenewSetup(t)
	s.e.base.RenewRL = NewLimiter(1, 2)
	sc := s.bundle.Senders[0]
	var last error
	for i := 0; i < 4; i++ {
		req, _ := renewalFor(t, sc.CertPEM, sc.KeyPEM)
		_, last = s.e.base.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req)
	}
	if kindOf(last) != KindRateLimited {
		t.Fatalf("fourth renewal of one certificate: %v", last)
	}
}

// The call is on the anonymous, pinned-TLS listener (the same one as Rejoin): the holder has no agent certificate to
// present, only the old operator certificate in the request.
func TestRenewTelemetryCertOverTheAnonymousListener(t *testing.T) {
	r := newRig(t)
	agent := r.approvedAgentID(t, fp)
	a, _ := r.st.GetAgent(r.ctx, agent)
	_, _, bundle, err := r.core.CreateOperator(r.ctx, "alex", "athens", []string{a.ClusterID}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := renewalFor(t, bundle.Senders[0].CertPEM, bundle.Senders[0].KeyPEM)
	ctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer cancel()
	enr := continuumv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(r.base.CA.SPKIPin(), "127.0.0.1", nil)))
	resp, err := enr.RenewTelemetryCert(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.LeafDer) == 0 || len(resp.CaPem) == 0 || resp.NotAfter == nil {
		t.Fatalf("incomplete answer: %+v", resp)
	}
	bad := &continuumv1.RenewTelemetryCertRequest{OldLeafDer: req.OldLeafDer, CsrDer: req.CsrDer, Proof: []byte("no")}
	if _, err := enr.RenewTelemetryCert(ctx, bad); code(err) != codes.Unauthenticated {
		t.Fatalf("a bad proof: %v", err)
	}
}

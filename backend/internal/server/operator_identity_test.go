package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	"ikhnos/internal/kernel"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"
)

func TestAnOperatorsCertificatesStateWhoTheyWereIssuedTo(t *testing.T) {
	s := newRenewSetup(t)
	for name, c := range map[string]struct {
		pem  []byte
		want pki.HolderKind
	}{"receiver": {s.bundle.ReceiverCertPEM, pki.HolderReceiver}, "sender": {s.bundle.Senders[0].CertPEM, pki.HolderSender}} {
		cert, err := pki.ParseCertificate(c.pem)
		if err != nil {
			t.Fatal(err)
		}
		got, present, err := pki.IdentityOf(cert)
		if err != nil || !present || got != (pki.Identity{Kind: c.want, Org: "org-1", ID: s.op.ID}) {
			t.Errorf("%s: %+v, %v, %v", name, got, present, err)
		}
	}
}

func TestARenewedOperatorCertificateStatesItsIdentityEvenWhenTheOldOneStatedNone(t *testing.T) {
	s := newRenewSetup(t)
	issuer, _, err := s.e.core.Operators.OperatorIssuer(s.e.ctx, s.op)
	if err != nil {
		t.Fatal(err)
	}
	// A certificate from before they stated anything: the same names, signed by the operator's CA, no URI.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := issuer.SignLeaf(&key.PublicKey, pkix.Name{CommonName: pki.OperatorClientCN(s.op.ID, s.cl), Organization: []string{"org-1"}}, pki.OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	req, _ := renewalFor(t, certPEM, keyPEM)
	resp, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req)
	if err != nil {
		t.Fatalf("a certificate without a URI must still renew while they are not required: %v", err)
	}
	renewed, _ := x509.ParseCertificate(resp.LeafDer)
	if got, present, err := pki.IdentityOf(renewed); err != nil || !present || got.Kind != pki.HolderSender || got.ID != s.op.ID || got.Org != "org-1" {
		t.Fatalf("the renewed certificate states %+v, %v, %v", got, present, err)
	}

	pki.RequireIdentityURI = true
	t.Cleanup(func() { pki.RequireIdentityURI = false })
	req, _ = renewalFor(t, certPEM, keyPEM)
	if _, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("with the URI required, a certificate without one renewed: %v", err)
	}
}

// A certificate that is shaped like a receiver's (its own name, server use) but states it was issued to a sender is not renewed as either.
func TestAnOperatorCertificateThatStatesAnotherKindIsNotRenewed(t *testing.T) {
	s := newRenewSetup(t)
	issuer, _, err := s.e.core.Operators.OperatorIssuer(s.e.ctx, s.op)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := issuer.SignLeafAs(&key.PublicKey, pkix.Name{CommonName: s.op.ID, Organization: []string{"org-1"}}, pki.OperatorTLSTTL, x509.ExtKeyUsageServerAuth, []string{"op.example.com"},
		&pki.Identity{Kind: pki.HolderSender, Org: "org-1", ID: s.op.ID})
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	req, _ := renewalFor(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if _, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("a certificate that states another kind was renewed: %v", err)
	}
}

// An agent's certificate (the Ikhnos CA signs those) is not an operator's, and is refused by the renewal of operator certificates.
func TestAnAgentsCertificateIsNotRenewedAsAnOperators(t *testing.T) {
	s := newRenewSetup(t)
	id, key, leaf := s.e.approvedAgent(t, "11111111-2222-4333-8444-555555555555")
	_ = id
	keyDER, _ := x509.MarshalECPrivateKey(key)
	req, _ := renewalFor(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if _, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("an agent's certificate was renewed as an operator's: %v", err)
	}
}

// A sender's identity that names a different operator than the name's first part ("<operator>-export-<x>-export-<y>" stated as the sender of
// "<operator>-export-<x>") is not renewed: the operator is read from the start of the name, and the identity has to say the same.
func TestASenderIdentityThatNamesAnotherOperatorThanTheNameIsNotRenewed(t *testing.T) {
	s := newRenewSetup(t)
	issuer, _, err := s.e.core.Operators.OperatorIssuer(s.e.ctx, s.op)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other := s.op.ID + "-export-x"
	der, err := issuer.SignLeafAs(&key.PublicKey, pkix.Name{CommonName: other + "-export-y", Organization: []string{"org-1"}}, pki.OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil,
		&pki.Identity{Kind: pki.HolderSender, Org: "org-1", ID: other})
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	req, _ := renewalFor(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if _, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("a sender identity naming another operator was renewed: %v", err)
	}
}

// An agent's identity signed by an operator's CA, under a name that reads as a sender's ("<operator>-export-<x>"), is not renewed as that
// operator's sender: the identity names an agent, and an agent is not an operator's client.
func TestAnAgentIdentityUnderAnOperatorsClientNameIsNotRenewed(t *testing.T) {
	s := newRenewSetup(t)
	issuer, _, err := s.e.core.Operators.OperatorIssuer(s.e.ctx, s.op)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cn := s.op.ID + "-export-x"
	der, err := issuer.SignLeafAs(&key.PublicKey, pkix.Name{CommonName: cn, Organization: []string{"org-1"}}, pki.OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil,
		&pki.Identity{Kind: pki.HolderAgent, Org: "org-1", ID: cn})
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	req, _ := renewalFor(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if _, err := s.e.base.Operators.RenewTelemetryCert(s.e.ctx, "203.0.113.5", req); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("an agent identity was renewed as an operator's sender: %v", err)
	}
}

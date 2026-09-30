package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func csrFor(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: []string{"evil.example.com"}, // must be ignored by the server
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestCAPersistsAndKeyIsPrivate(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Pin() != b.Pin() {
		t.Fatal("reloading must yield the same CA")
	}
	st, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("ca.key mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestRefusesHalfCA(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("must not silently create a new CA when only ca.crt exists")
	}
}

func TestIssueAgentUsesServerChosenIdentity(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, err := ParseCSR(csrFor(t, key))
	if err != nil {
		t.Fatal(err)
	}
	der, notAfter, err := ca.IssueAgent(csr, "ag-1", "org-1", 100*time.Hour) // ttl above the cap is clamped
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	if leaf.Subject.CommonName != "ag-1" || leaf.Subject.Organization[0] != "org-1" {
		t.Fatalf("identity = %v", leaf.Subject)
	}
	if len(leaf.DNSNames) != 0 {
		t.Fatal("agent-requested SANs must not be copied")
	}
	if time.Until(notAfter) > AgentCertTTL {
		t.Fatal("certificate outlives the cap")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("leaf does not verify: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Fatal("agent certificate must not be usable as a server certificate")
	}
	other, _ := LoadOrCreate(t.TempDir())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: other.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("certificate verified against a different CA")
	}
}

func TestParseCSRRejectsWeakOrForeignKeys(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := ParseCSR(csrFor(t, rk)); err == nil {
		t.Error("RSA accepted")
	}
	_, ek, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParseCSR(csrFor(t, ek)); err == nil {
		t.Error("Ed25519 accepted")
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := ParseCSR(csrFor(t, p384)); err == nil {
		t.Error("P-384 accepted")
	}
	if _, err := ParseCSR([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := ParseCSR(nil); err == nil {
		t.Error("empty accepted")
	}
	// Tampered signature must fail.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der := csrFor(t, k)
	der[len(der)-3] ^= 0xff
	if _, err := ParseCSR(der); err == nil {
		t.Error("tampered CSR accepted")
	}
}

func TestServerCertCoversHostsAndIsReused(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	sc := NewServerCerts(ca, []string{"continuum.example.com", "10.0.0.5"})
	c1, err := sc.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := sc.GetCertificate(nil)
	if c1 != c2 {
		t.Fatal("certificate should be cached")
	}
	leaf, _ := x509.ParseCertificate(c1.Certificate[0])
	if err := leaf.VerifyHostname("continuum.example.com"); err != nil {
		t.Error(err)
	}
	if err := leaf.VerifyHostname("10.0.0.5"); err != nil {
		t.Error(err)
	}
	if err := leaf.VerifyHostname("other.example.com"); err == nil {
		t.Error("unexpected host accepted")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSName: "continuum.example.com"}); err != nil {
		t.Errorf("server cert does not verify: %v", err)
	}
}

func TestIssueOperatorReceiverAndClientTLS(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())

	rCertPEM, rKeyPEM, err := ca.IssueOperatorReceiverTLS("op-1", "org-1", []string{"op-1.continuum-system.svc", "203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	rCert := mustParsePEMCert(t, rCertPEM)
	if rCert.Subject.CommonName != "op-1" || rCert.Subject.Organization[0] != "org-1" {
		t.Fatalf("receiver identity = %v", rCert.Subject)
	}
	if len(rCert.ExtKeyUsage) != 1 || rCert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("receiver cert usage = %v, want ServerAuth only", rCert.ExtKeyUsage)
	}
	if len(rCert.DNSNames) != 1 || rCert.DNSNames[0] != "op-1.continuum-system.svc" {
		t.Fatalf("receiver DNS SANs = %v", rCert.DNSNames)
	}
	if len(rCert.IPAddresses) != 1 || rCert.IPAddresses[0].String() != "203.0.113.9" {
		t.Fatalf("receiver IP SANs = %v", rCert.IPAddresses)
	}
	mustMatchKey(t, rCert, rKeyPEM)
	if _, err := rCert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSName: "op-1.continuum-system.svc"}); err != nil {
		t.Fatalf("receiver cert does not verify: %v", err)
	}

	cCertPEM, cKeyPEM, err := ca.IssueOperatorClientTLS("op-1", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	cCert := mustParsePEMCert(t, cCertPEM)
	if cCert.Subject.CommonName != "op-1-export" {
		t.Fatalf("client identity = %v", cCert.Subject)
	}
	if len(cCert.ExtKeyUsage) != 1 || cCert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("client cert usage = %v, want ClientAuth only", cCert.ExtKeyUsage)
	}
	if len(cCert.DNSNames) != 0 || len(cCert.IPAddresses) != 0 {
		t.Fatal("a client certificate needs no SANs")
	}
	mustMatchKey(t, cCert, cKeyPEM)
	if _, err := cCert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("client cert does not verify: %v", err)
	}

	// The two identities are independent: an exporter's client certificate must not also pass as this
	// operator's own receiver server certificate, or vice versa.
	if _, err := cCert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Fatal("client certificate must not verify as a server certificate")
	}

	if _, _, err := ca.IssueOperatorReceiverTLS("", "org-1", nil); err == nil {
		t.Fatal("empty operator id must be rejected")
	}
	if _, _, err := ca.IssueOperatorClientTLS("", "org-1"); err == nil {
		t.Fatal("empty operator id must be rejected")
	}
}

func TestCertPEMMatchesTheLoadedCA(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	cert := mustParsePEMCert(t, ca.CertPEM())
	if SPKIPin(cert.Raw) != ca.SPKIPin() {
		t.Fatal("CertPEM does not round-trip to this CA's own certificate")
	}
}

func mustParsePEMCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("not a PEM certificate block: %q", certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("invalid certificate: %v", err)
	}
	return cert
}

func mustMatchKey(t *testing.T, cert *x509.Certificate, keyPEM []byte) {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		t.Fatalf("not a PEM EC private key block: %q", keyPEM)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("invalid private key: %v", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatal("private key does not match the certificate's public key")
	}
}

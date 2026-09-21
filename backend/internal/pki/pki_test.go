package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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

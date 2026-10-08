package pki

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	if b == nil {
		t.Fatalf("not PEM: %q", certPEM)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func poolOf(t *testing.T, certPEM []byte) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(certPEM) {
		t.Fatal("no certificate in PEM")
	}
	return p
}

func verifies(c *x509.Certificate, roots *x509.CertPool, eku x509.ExtKeyUsage) bool {
	_, err := c.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{eku}})
	return err == nil
}

// The whole point of a per-operator CA: a client certificate verifies against the pool of the operator
// whose CA signed it, and against nothing else - not another operator's CA in the same org, not the org CA.
// Real pki output, verified with crypto/x509 exactly as a TLS server with client_ca_file would.
func TestOperatorCertificatesVerifyOnlyAgainstTheirOwnOperatorsCA(t *testing.T) {
	cheapKDF(t)
	org, err := LoadOrCreateWith(t.TempDir(), Options{Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	issA, caA, _, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	issB, caB, _, err := org.NewOperatorCA("op-bbb", "org-1") // same organisation
	if err != nil {
		t.Fatal(err)
	}
	clientA, _, err := issA.IssueOperatorClientTLS("op-aaa", "org-1", "")
	if err != nil {
		t.Fatal(err)
	}
	clientB, _, err := issB.IssueOperatorClientTLS("op-bbb", "org-1", "")
	if err != nil {
		t.Fatal(err)
	}
	orgClient, _, err := org.IssueOperatorClientTLS("op-aaa", "org-1", "") // what a legacy operator is issued
	if err != nil {
		t.Fatal(err)
	}
	// The same operator and org name, but from the org CA, is still not this operator's certificate.
	poolA, poolB := poolOf(t, caA), poolOf(t, caB)
	for _, c := range []struct {
		name string
		leaf []byte
		pool *x509.CertPool
		want bool
	}{
		{"A's client cert against A's CA", clientA, poolA, true},
		{"B's client cert against B's CA", clientB, poolB, true},
		{"B's client cert against A's CA (other operator, same org)", clientB, poolA, false},
		{"A's client cert against B's CA", clientA, poolB, false},
		{"an org-CA client cert against A's CA", orgClient, poolA, false},
	} {
		if got := verifies(parseLeaf(t, c.leaf), c.pool, x509.ExtKeyUsageClientAuth); got != c.want {
			t.Errorf("%s: verifies=%v, want %v", c.name, got, c.want)
		}
	}
	// And in the other direction: the org CA's pool does not vouch for an operator-CA certificate.
	if verifies(parseLeaf(t, clientA), org.Pool(), x509.ExtKeyUsageClientAuth) {
		t.Error("an operator-CA client certificate verifies against the org CA")
	}
	// A different subject from the same operator CA is accepted: the gate is the CA, not the subject.
	other, _, err := issA.issueLeaf(pkixName("anything-at-all", "some-other-org"), time.Hour, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !verifies(parseLeaf(t, other), poolA, x509.ExtKeyUsageClientAuth) {
		t.Error("a certificate with another subject from the same CA must verify (by design, the CA is the gate)")
	}
	// The receiver's own certificate is signed by the same CA, for ServerAuth and the Service names.
	srv, _, err := issA.IssueOperatorReceiverTLS("op-aaa", "org-1", []string{"op-aaa.continuum-system.svc"})
	if err != nil {
		t.Fatal(err)
	}
	sc := parseLeaf(t, srv)
	if _, err := sc.Verify(x509.VerifyOptions{Roots: poolA, DNSName: "op-aaa.continuum-system.svc", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("receiver certificate does not verify against its operator CA: %v", err)
	}
	if verifies(sc, poolB, x509.ExtKeyUsageServerAuth) || verifies(sc, org.Pool(), x509.ExtKeyUsageServerAuth) {
		t.Error("receiver certificate verifies against a CA that did not sign it")
	}
	// Leaf subject is unchanged: CN op-id-export, O org.
	if cc := parseLeaf(t, clientA); cc.Subject.CommonName != "op-aaa-export" || len(cc.Subject.Organization) != 1 || cc.Subject.Organization[0] != "org-1" {
		t.Errorf("client subject = %v", cc.Subject)
	}
}

// The operator CA is its own root (self-signed), not an intermediate of the org CA, and can sign leaves only.
func TestOperatorCAIsAStandaloneLeafOnlyRoot(t *testing.T) {
	org, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	iss, certPEM, _, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	c := parseLeaf(t, certPEM)
	if !c.IsCA || !c.MaxPathLenZero || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("not a leaf-signing CA: %+v", c)
	}
	if err := c.CheckSignatureFrom(c); err != nil {
		t.Fatalf("not self-signed: %v", err)
	}
	if err := c.CheckSignatureFrom(org.cert); err == nil {
		t.Fatal("operator CA is signed by the org CA; it must be a separate root")
	}
	if c.Subject.CommonName != "Continuum operator CA op-aaa" || c.Subject.Organization[0] != "org-1" {
		t.Fatalf("subject = %v", c.Subject)
	}
	if got := time.Until(c.NotAfter); got < 9*365*24*time.Hour || got > OperatorCATTL {
		t.Fatalf("operator CA validity %v is not about OperatorCATTL (%v)", got, OperatorCATTL)
	}
	if !bytes.Equal(iss.CertPEM(), certPEM) {
		t.Fatal("issuer and returned certificate differ")
	}
	// A key can't be minted for an unnamed operator.
	if _, _, _, err := org.NewOperatorCA("", "org-1"); err == nil {
		t.Fatal("expected an error for an empty operator id")
	}
}

// The operator CA key is sealed with exactly the scheme and passphrase of the org CA key, and reopens only
// with that passphrase.
func TestOperatorCAKeyIsSealedLikeTheOrgCAKey(t *testing.T) {
	cheapKDF(t)
	org, err := LoadOrCreateWith(t.TempDir(), Options{Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, keyPEM, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(keyPEM)
	if b == nil || b.Type != encryptedKeyType {
		t.Fatalf("key block = %v, want %q", b, encryptedKeyType)
	}
	if strings.Contains(string(keyPEM), "EC PRIVATE KEY") || bytes.Contains(keyPEM, pass) {
		t.Fatal("the stored operator CA key is not encrypted")
	}
	re, err := org.OpenOperatorCA(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("reopening with the org passphrase: %v", err)
	}
	// What the reopened CA signs verifies against the original certificate.
	leaf, _, err := re.IssueOperatorClientTLS("op-aaa", "org-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !verifies(parseLeaf(t, leaf), poolOf(t, certPEM), x509.ExtKeyUsageClientAuth) {
		t.Fatal("a reopened operator CA signs certificates its own certificate does not vouch for")
	}
	// A server with another passphrase, or none, cannot open it.
	wrong, err := LoadOrCreateWith(t.TempDir(), Options{Passphrase: []byte("a different passphrase!")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.OpenOperatorCA(certPEM, keyPEM); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	plain, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.OpenOperatorCA(certPEM, keyPEM); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase: %v", err)
	}
	// The key does not match another operator's certificate.
	_, otherCert, _, _ := org.NewOperatorCA("op-bbb", "org-1")
	if _, err := org.OpenOperatorCA(otherCert, keyPEM); err == nil {
		t.Fatal("a certificate and a key of two different operator CAs were accepted together")
	}
}

// Without a passphrase the org CA key is plain (with a warning); the operator CA key follows suit, no
// weaker and no stronger than the org CA's.
func TestOperatorCAKeyIsPlainOnlyWhenTheOrgCAKeyIs(t *testing.T) {
	var sink logSink
	org, err := LoadOrCreateWith(t.TempDir(), Options{Log: sink.logger()})
	if err != nil {
		t.Fatal(err)
	}
	sink.buf.Reset()
	_, certPEM, keyPEM, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := pem.Decode(keyPEM); b == nil || b.Type != plainKeyType {
		t.Fatalf("key block = %v", b)
	}
	if !strings.Contains(sink.buf.String(), "UNENCRYPTED") {
		t.Fatalf("no warning about an unencrypted operator CA key: %q", sink.buf.String())
	}
	if _, err := org.OpenOperatorCA(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
}

// A leaf never outlives its CA, and an expired operator CA signs nothing.
func TestOperatorCAClampsAndExpires(t *testing.T) {
	old := OperatorCATTL
	t.Cleanup(func() { OperatorCATTL = old })
	OperatorCATTL = OperatorTLSTTL / 2
	org, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	iss, certPEM, _, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err := iss.IssueOperatorClientTLS("op-aaa", "org-1", "") // asks for OperatorTLSTTL, more than the CA has left
	if err != nil {
		t.Fatal(err)
	}
	if lc, cc := parseLeaf(t, leaf), parseLeaf(t, certPEM); lc.NotAfter.After(cc.NotAfter) {
		t.Fatalf("leaf expires %v, after its CA %v", lc.NotAfter, cc.NotAfter)
	}
	OperatorCATTL = -time.Hour // an already-expired CA
	exp, _, _, err := org.NewOperatorCA("op-old", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := exp.IssueOperatorClientTLS("op-old", "org-1", ""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired CA issued a certificate: %v", err)
	}
}

func pkixName(cn, org string) pkix.Name {
	return pkix.Name{CommonName: cn, Organization: []string{org}}
}

// NotAfter reads the expiry off a certificate the server just issued, which is all it keeps of it.
func TestNotAfterReadsTheExpiryOfAnIssuedCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	certPEM, _, err := ca.IssueOperatorClientTLS("op-aaa", "org-1", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := NotAfter(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if want := before.Add(OperatorTLSTTL); got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Fatalf("NotAfter = %v, want about %v", got, want)
	}
	if _, err := NotAfter([]byte("not a certificate")); err == nil {
		t.Fatal("garbage was accepted")
	}
}

// The sender is in the certificate's name, kept inside the CN limit, and two different senders never share one.
func TestOperatorClientCertificateNamesItsSender(t *testing.T) {
	cheapKDF(t)
	org, err := LoadOrCreateWith(t.TempDir(), Options{Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	iss, _, _, err := org.NewOperatorCA("op-aaa", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	cn := func(sender string) string {
		certPEM, _, err := iss.IssueOperatorClientTLS("op-aaa", "org-1", sender)
		if err != nil {
			t.Fatal(err)
		}
		return parseLeaf(t, certPEM).Subject.CommonName
	}
	if got := cn(""); got != "op-aaa-export" {
		t.Errorf("no sender: %q", got)
	}
	if got := cn("cluster-eu-1"); got != "op-aaa-export-cluster-eu-1" {
		t.Errorf("a plain sender: %q", got)
	}
	long := strings.Repeat("c", 200)
	a, b := cn(long+"x"), cn(long+"y")
	if len(a) > 64 || len(b) > 64 || a == b {
		t.Errorf("long senders: %q %q", a, b)
	}
	if got := cn("a b/c"); strings.ContainsAny(got, " /") || got == cn("a-b-c") {
		t.Errorf("an unsafe sender: %q", got)
	}
}

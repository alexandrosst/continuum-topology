package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOperatorCAsAreLongLivedAndRenewalFitsInsideALeaf(t *testing.T) {
	if OperatorCATTL < 10*365*24*time.Hour-time.Hour {
		t.Fatalf("operator CA TTL is %v", OperatorCATTL)
	}
	if OperatorRenewBefore >= 30*24*time.Hour || OperatorRenewGrace <= 0 {
		t.Fatalf("renew before %v, grace %v: renewal must start inside a 30 day certificate", OperatorRenewBefore, OperatorRenewGrace)
	}
}

func TestSignLeafKeepsTheCallersIdentityNotTheRequestersAndNeverOutlivesTheCA(t *testing.T) {
	org, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := org.SignLeaf(&key.PublicKey, pkix.Name{CommonName: "op-x-export", Organization: []string{"org-1"}}, OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "op-x-export" || !leaf.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatalf("unexpected leaf %v", leaf.Subject)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: org.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if got := time.Until(leaf.NotAfter); got > OperatorTLSTTL || got < OperatorTLSTTL-time.Minute {
		t.Fatalf("leaf lives %v", got)
	}
	srv, err := org.SignLeaf(&key.PublicKey, pkix.Name{CommonName: "recv"}, time.Hour, x509.ExtKeyUsageServerAuth, []string{"op.example.com", "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := x509.ParseCertificate(srv)
	if len(sc.DNSNames) != 1 || len(sc.IPAddresses) != 1 {
		t.Fatalf("hosts not written: %v %v", sc.DNSNames, sc.IPAddresses)
	}
}

func TestRenewalProofBindsTheOldKeyToTheOneRequest(t *testing.T) {
	org, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	oldDER, err := org.SignLeaf(&oldKey.PublicKey, pkix.Name{CommonName: "c"}, time.Hour, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := x509.ParseCertificate(oldDER)
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr := csrFor(t, newKey)
	proof, err := ProveRenewal(oldKey, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRenewalProof(old, csr, proof); err != nil {
		t.Fatalf("a good proof was refused: %v", err)
	}
	if err := VerifyRenewalProof(old, csrFor(t, newKey), proof); err == nil {
		t.Fatal("a proof made for one request was accepted for another")
	}
	thief, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	bad, _ := ProveRenewal(thief, csr)
	if err := VerifyRenewalProof(old, csr, bad); err == nil {
		t.Fatal("a proof by a key that is not the certificate's was accepted")
	}
	if err := VerifyRenewalProof(old, csr, nil); err == nil {
		t.Fatal("an empty proof was accepted")
	}
}

func TestVerifyExpiredWantsTheRightIssuerAndUsage(t *testing.T) {
	org, _ := LoadOrCreate(t.TempDir())
	other, _ := LoadOrCreate(t.TempDir())
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := org.SignLeaf(&key.PublicKey, pkix.Name{CommonName: "c"}, time.Hour, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := org.VerifyExpired(der, x509.ExtKeyUsageClientAuth); err != nil {
		t.Fatal(err)
	}
	if _, err := org.VerifyExpired(der, x509.ExtKeyUsageServerAuth); err == nil {
		t.Fatal("a client certificate verified as a server certificate")
	}
	if _, err := other.VerifyExpired(der, x509.ExtKeyUsageClientAuth); err == nil {
		t.Fatal("another CA's certificate verified")
	}
}

// A CA with under a year left is re-signed at start with the same key and subject: the public-key pin does
// not change and what it signed before still verifies. A CA that has already expired is renewed too.
func TestCertificateWithLittleTimeLeftIsResignedWithTheSameKey(t *testing.T) {
	for name, left := range map[string]time.Duration{"six months left": 180 * 24 * time.Hour, "already expired": -48 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ca, err := LoadOrCreate(dir)
			if err != nil {
				t.Fatal(err)
			}
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			leafDER, _ := ca.SignLeaf(&key.PublicKey, pkix.Name{CommonName: "c"}, time.Hour, x509.ExtKeyUsageClientAuth, nil)
			spki, hex := ca.SPKIPin(), ca.Pin()

			now := time.Now()
			tmpl := &x509.Certificate{
				SerialNumber: ca.cert.SerialNumber, Subject: ca.cert.Subject,
				NotBefore: now.Add(-9 * 365 * 24 * time.Hour), NotAfter: now.Add(left),
				KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
			}
			der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ca.key.PublicKey, ca.key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := LoadOrCreate(dir)
			if err != nil {
				t.Fatalf("a CA near its end must be renewed, not refused: %v", err)
			}
			if got.SPKIPin() != spki {
				t.Fatal("the public-key pin changed")
			}
			if got.Pin() == hex {
				t.Fatal("the certificate was not replaced")
			}
			if until := time.Until(got.cert.NotAfter); until < 9*365*24*time.Hour {
				t.Fatalf("renewed CA is valid for only %v", until)
			}
			leaf, _ := x509.ParseCertificate(leafDER)
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: got.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
				t.Fatalf("a leaf signed before the renewal no longer verifies: %v", err)
			}
			again, err := LoadOrCreate(dir)
			if err != nil || again.Pin() != got.Pin() {
				t.Fatalf("the renewed certificate was not kept on disk (%v)", err)
			}
		})
	}
}

func TestRejoinWindowSurvivesALongOutage(t *testing.T) {
	if RejoinWindow < 30*24*time.Hour {
		t.Fatalf("an edge cluster that is off for a month must be able to rejoin: window is %v", RejoinWindow)
	}
}

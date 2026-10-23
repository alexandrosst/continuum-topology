package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"strings"
	"testing"
	"time"
)

func certWith(cn, org string, eku x509.ExtKeyUsage, uris ...string) *x509.Certificate {
	c := &x509.Certificate{Subject: pkix.Name{CommonName: cn, Organization: []string{org}}, ExtKeyUsage: []x509.ExtKeyUsage{eku}}
	for _, u := range uris {
		pu, err := url.Parse(u)
		if err != nil {
			panic(err)
		}
		c.URIs = append(c.URIs, pu)
	}
	return c
}

func TestEveryKindOfCertificateTheServerIssuesStatesItsIdentity(t *testing.T) {
	ca, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// An agent.
	csrDER := csrFor(t, p256(t))
	csr, _ := ParseCSR(csrDER)
	der, _, err := ca.IssueAgent(csr, "ag-1", "org-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := x509.ParseCertificate(der)
	// An operator's receiver and a sender.
	rPEM, _, err := ca.IssueOperatorReceiverTLS("op-1", "org-1", []string{"op-1.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cPEM, _, err := ca.IssueOperatorClientTLS("op-1", "org-1", "cl-9")
	if err != nil {
		t.Fatal(err)
	}
	recv, _ := ParseCertificate(rPEM)
	send, _ := ParseCertificate(cPEM)
	for name, c := range map[string]struct {
		cert *x509.Certificate
		want Identity
	}{
		"agent":    {agent, Identity{HolderAgent, "org-1", "ag-1"}},
		"receiver": {recv, Identity{HolderReceiver, "org-1", "op-1"}},
		"sender":   {send, Identity{HolderSender, "org-1", "op-1"}},
	} {
		got, present, err := IdentityOf(c.cert)
		if err != nil || !present || got != c.want {
			t.Errorf("%s: %+v present=%v err=%v, want %+v", name, got, present, err, c.want)
		}
		// Exactly one URI name, and no other kind of name added by accident.
		if len(c.cert.URIs) != 1 || c.cert.URIs[0].Scheme != "spiffe" {
			t.Errorf("%s: URIs = %v", name, c.cert.URIs)
		}
		// The CA that issued it verifies it with the name in place (a URI name must not break the chain).
		usage := x509.ExtKeyUsageClientAuth
		if name == "receiver" {
			usage = x509.ExtKeyUsageServerAuth
		}
		if _, err := c.cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestHolderRefusesAnotherKindAndNamesBoth(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	cPEM, _, _ := ca.IssueOperatorClientTLS("op-1", "org-1", "")
	send, _ := ParseCertificate(cPEM)
	if _, _, err := Holder(send, HolderAgent); err == nil || !strings.Contains(err.Error(), "operator-sender") || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("an operator's sender passed as an agent, or the refusal does not say why: %v", err)
	}
	if _, _, err := Holder(send, HolderSender); err != nil {
		t.Fatal(err)
	}
}

func TestACertificateWithoutAnIdentityIsALegacyOneUntilTheURIIsRequired(t *testing.T) {
	legacy := certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth)
	if id, present, err := IdentityOf(legacy); err != nil || present || id != (Identity{}) {
		t.Fatalf("IdentityOf = %+v, %v, %v", id, present, err)
	}
	if _, present, err := Holder(legacy, HolderAgent); err != nil || present {
		t.Fatalf("a legacy certificate was refused while the URI is not required: %v", err)
	}
	RequireIdentityURI = true
	defer func() { RequireIdentityURI = false }()
	if _, _, err := Holder(legacy, HolderAgent); err == nil {
		t.Fatal("a certificate without an identity passed when it is required")
	}
}

func TestAnIdentityThatDoesNotAddUpIsAnErrorNeverALegacyCertificate(t *testing.T) {
	const ag = "spiffe://ikhnos/org/org-1/agent/ag-1"
	cases := map[string]*x509.Certificate{
		"two URI names":                   certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, ag, ag),
		"another scheme":                  certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "https://ikhnos/org/org-1/agent/ag-1"),
		"another trust domain":            certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://example.org/org/org-1/agent/ag-1"),
		"a port":                          certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos:99/org/org-1/agent/ag-1"),
		"a query":                         certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, ag+"?x=1"),
		"a fragment":                      certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, ag+"#x"),
		"user info":                       certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://u@ikhnos/org/org-1/agent/ag-1"),
		"no path":                         certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos"),
		"an unknown kind":                 certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/robot/ag-1"),
		"an extra segment":                certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, ag+"/x"),
		"an empty id":                     certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/agent/"),
		"an empty organisation":           certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org//agent/ag-1"),
		"a slash hidden in an id":         certWith("ag-1/x", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/agent/ag-1%2Fx"),
		"another id than the name":        certWith("ag-2", "org-1", x509.ExtKeyUsageClientAuth, ag),
		"another organisation":            certWith("ag-1", "org-2", x509.ExtKeyUsageClientAuth, ag),
		"no organisation in the name":     {Subject: pkix.Name{CommonName: "ag-1"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: certWith("", "", 0, ag).URIs},
		"an agent used for serving":       certWith("ag-1", "org-1", x509.ExtKeyUsageServerAuth, ag),
		"a receiver used as a client":     certWith("op-1", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/operator/op-1/receiver"),
		"a sender with a stranger's name": certWith("op-2-export", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/operator/op-1/sender"),
		"a sender named like another":     certWith("op-1x-export", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/operator/op-1/sender"),
	}
	for name, c := range cases {
		if id, present, err := IdentityOf(c); err == nil || !present {
			t.Errorf("%s: accepted as %+v (present=%v, err=%v)", name, id, present, err)
		}
	}
	// And the forms that are right, so the table above is not refusing everything.
	good := map[string]*x509.Certificate{
		"agent":                certWith("ag-1", "org-1", x509.ExtKeyUsageClientAuth, ag),
		"receiver":             certWith("op-1", "org-1", x509.ExtKeyUsageServerAuth, "spiffe://ikhnos/org/org-1/operator/op-1/receiver"),
		"sender":               certWith("op-1-export", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/operator/op-1/sender"),
		"sender with a sender": certWith("op-1-export-cl-9", "org-1", x509.ExtKeyUsageClientAuth, "spiffe://ikhnos/org/org-1/operator/op-1/sender"),
	}
	for name, c := range good {
		if _, present, err := IdentityOf(c); err != nil || !present {
			t.Errorf("%s: %v (present=%v)", name, err, present)
		}
	}
}

func TestAnIdentityIsRefusedBeforeItIsWrittenIfItCouldNotBeReadBack(t *testing.T) {
	for _, i := range []Identity{
		{HolderAgent, "", "ag-1"}, {HolderAgent, "org-1", ""}, {HolderAgent, "org/1", "ag-1"}, {HolderAgent, "org-1", "a?b"}, {"robot", "org-1", "x"},
	} {
		if _, err := i.URI(); err == nil {
			t.Errorf("%+v was written", i)
		}
	}
	ca, _ := LoadOrCreate(t.TempDir())
	csr, _ := ParseCSR(csrFor(t, p256(t)))
	if _, _, err := ca.IssueAgent(csr, "ag-1", "", time.Hour); err == nil {
		t.Error("an agent certificate was issued for no organisation")
	}
}

// FuzzIdentityOf: whatever URI a certificate carries, what is read from it is something that writes back to a URI which reads to the same.
func FuzzIdentityOf(f *testing.F) {
	for _, s := range []string{
		"spiffe://ikhnos/org/o/agent/a", "spiffe://ikhnos/org/o/operator/p/receiver", "spiffe://ikhnos/org/o/operator/p/sender",
		"spiffe://ikhnos/org/o%2Fx/agent/a", "spiffe://ikhnos/org/o/agent/a/", "spiffe://ikhnos//", "", "x", "spiffe://ikhnos/org/%00/agent/a",
	} {
		f.Add(s, "a", "o")
	}
	f.Fuzz(func(t *testing.T, raw, cn, org string) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth} {
			c := &x509.Certificate{Subject: pkix.Name{CommonName: cn, Organization: []string{org}}, ExtKeyUsage: []x509.ExtKeyUsage{eku}, URIs: []*url.URL{u}}
			id, present, err := IdentityOf(c)
			if err != nil || !present {
				continue
			}
			back, err := id.URI()
			if err != nil {
				t.Fatalf("%q was read as %+v, which cannot be written: %v", raw, id, err)
			}
			again := &x509.Certificate{Subject: c.Subject, ExtKeyUsage: c.ExtKeyUsage, URIs: []*url.URL{back}}
			id2, present2, err := IdentityOf(again)
			if err != nil || !present2 || id2 != id {
				t.Fatalf("%q read as %+v, written back as %q read as %+v (%v)", raw, id, back, id2, err)
			}
		}
	})
}

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

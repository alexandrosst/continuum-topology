package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/kernel"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc/codes"
)

func parseLeaf(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// legacyLeaf is what this server issued before certificates stated who they were for: the same name, the same key, no URI.
func legacyLeaf(t *testing.T, e *env, id string, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := e.core.CA.SignLeaf(&key.PublicKey, pkix.Name{CommonName: id, Organization: []string{"org-1"}}, time.Hour, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// mislabelled is a certificate this CA signed with an agent's name and key that says it was issued to something else. Only the check of what a
// certificate states can refuse it: by name it is the agent.
func mislabelled(t *testing.T, e *env, id string, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := e.core.CA.SignLeafAs(&key.PublicKey, pkix.Name{CommonName: id, Organization: []string{"org-1"}}, time.Hour, x509.ExtKeyUsageClientAuth, nil,
		&pki.Identity{Kind: pki.HolderSender, Org: "org-1", ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// statesOther is a certificate this CA signed with an agent's name and key whose identity says an agent, but another one's id or another
// organisation's: its subject alone would pass for the agent.
func statesOther(t *testing.T, e *env, id string, key *ecdsa.PrivateKey, org, as string) []byte {
	t.Helper()
	der, err := e.core.CA.SignLeafAs(&key.PublicKey, pkix.Name{CommonName: id, Organization: []string{org}}, time.Hour, x509.ExtKeyUsageClientAuth, nil,
		&pki.Identity{Kind: pki.HolderAgent, Org: org, ID: as})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestAnAgentsCertificateStatesThatItIsAnAgentsAndWhose(t *testing.T) {
	e := newEnv(t)
	id, _, leaf := e.approvedAgent(t, testkit.FP)
	got, present, err := pki.IdentityOf(parseLeaf(t, leaf))
	if err != nil || !present || got != (pki.Identity{Kind: pki.HolderAgent, Org: "org-1", ID: id}) {
		t.Fatalf("IdentityOf = %+v, %v, %v", got, present, err)
	}
}

func TestRejoinHoldsACertificateToWhatItStatesAndAnOldOneWorksUntilTheURIIsRequired(t *testing.T) {
	e := newEnv(t)
	id, key, leaf := e.approvedAgent(t, testkit.FP)
	rejoin := func(der []byte) error {
		_, err := e.core.Agents.Rejoin(e.ctx, "10.0.0.1", &ikhnosv1.RejoinRequest{ExpiredLeafDer: der, CsrDer: testkit.CSRWith(t, key)})
		return err
	}
	if err := rejoin(leaf); err != nil {
		t.Fatalf("a certificate of this version: %v", err)
	}
	if err := rejoin(legacyLeaf(t, e, id, key)); err != nil {
		t.Fatalf("a certificate issued before they stated who they were for must still rejoin: %v", err)
	}
	if err := rejoin(mislabelled(t, e, id, key)); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("a certificate that states another kind of holder rejoined as an agent: %v", err)
	}

	pki.RequireIdentityURI = true
	t.Cleanup(func() { pki.RequireIdentityURI = false })
	if err := rejoin(legacyLeaf(t, e, id, key)); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("with the URI required, a certificate without one rejoined: %v", err)
	}
	cur, _ := e.st.GetAgent(e.ctx, "org-1", id)
	if err := rejoin(cur.LeafDER); err != nil {
		t.Fatalf("with the URI required, a certificate that has one: %v", err)
	}
}

func TestAConnectionIsHeldToWhatItsCertificateStates(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, key, leaf := r.env.approvedAgent(t, testkit.FP)
	renew := func(der []byte) codes.Code {
		cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
		c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
		_, err := c.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: testkit.CSRWith(t, key)})
		return testkit.StatusCode(err)
	}
	if c := renew(leaf); c != codes.OK {
		t.Fatalf("a certificate of this version: %v", c)
	}
	if c := renew(legacyLeaf(t, r.env, id, key)); c != codes.OK {
		t.Fatalf("a certificate without a URI is refused while they are not required: %v", c)
	}
	if c := renew(mislabelled(t, r.env, id, key)); c != codes.Unauthenticated {
		t.Fatalf("a certificate that states it was issued to an operator's sender was accepted as an agent: %v", c)
	}
	pki.RequireIdentityURI = true
	t.Cleanup(func() { pki.RequireIdentityURI = false })
	if c := renew(legacyLeaf(t, r.env, id, key)); c != codes.Unauthenticated {
		t.Fatalf("with the URI required, a certificate without one connected: %v", c)
	}
}

// The identity a certificate states is compared with the agent it is presented as: the id with the name, and the organisation with the
// one the agent belongs to. Agent ids are random and one CA issues them, so nothing exploits the gap today; the comparison is what
// keeps it that way across a restore, a move between organisations or a CA that serves two servers.
func TestACertificateThatNamesAnotherAgentOrOrganisationIsRefused(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, key, leaf := r.env.approvedAgent(t, testkit.FP)
	renew := func(der []byte) codes.Code {
		cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
		c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
		_, err := c.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: testkit.CSRWith(t, key)})
		return testkit.StatusCode(err)
	}
	if c := renew(leaf); c != codes.OK {
		t.Fatalf("a certificate of this version: %v", c)
	}
	if c := renew(statesOther(t, r.env, id, key, "org-1", "some-other-agent")); c != codes.Unauthenticated {
		t.Errorf("a certificate named for this agent that states another agent's id connected: %v", c)
	}
	if c := renew(statesOther(t, r.env, id, key, "org-2", id)); c != codes.Unauthenticated {
		t.Errorf("a certificate for another organisation's agent of the same id connected: %v", c)
	}
	rejoin := func(der []byte) error {
		_, err := r.env.core.Agents.Rejoin(r.env.ctx, "10.0.0.1", &ikhnosv1.RejoinRequest{ExpiredLeafDer: der, CsrDer: testkit.CSRWith(t, key)})
		return err
	}
	if err := rejoin(statesOther(t, r.env, id, key, "org-1", "some-other-agent")); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Errorf("rejoin with another agent's id: %v", err)
	}
	if err := rejoin(statesOther(t, r.env, id, key, "org-2", id)); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Errorf("rejoin with another organisation's: %v", err)
	}
}

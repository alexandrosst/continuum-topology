package server

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/estate"
	"continuum/internal/kernel"
	"continuum/internal/pki"
	"continuum/internal/testkit"

	"google.golang.org/grpc/codes"
)

// approvedAgent enrolls and approves an agent, returning its id, private key and first certificate.
func (e *env) approvedAgent(t *testing.T, fingerprint string) (string, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	return testkit.ApprovedAgent(t, e.ctx, e.core.Agents, fingerprint)
}

func TestFingerprintMustBeCanonicalUUID(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{
		"aaaaaaaa-bbbb",                            // the old "anything 8-64 chars" shape
		"8F3C2A9E-1111-4222-8333-944455556666",     // upper case: k8s UIDs are lower case
		"8f3c2a9e-1111-4222-8333-94445555666",      // one digit short
		"8f3c2a9e-1111-4222-8333-944455556666-abc", // trailing junk
		"../../etc/passwd-0000-0000-0000-000000000000",
		"",
	} {
		secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "c", 1)
		d, _ := testkit.CSR(t)
		_, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: bad})
		if testkit.KindOf(err) != kernel.KindInvalid {
			t.Errorf("fingerprint %q: %v, want invalid", bad, err)
		}
	}
}

func TestEnrollRejectsOversizedVersionAndScrubsControlCharacters(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "c", 1)
	d, _ := testkit.CSR(t)
	_, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, AgentVersion: strings.Repeat("x", 65)})
	if testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("long version: %v", err)
	}
	resp, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, AgentVersion: "1.0\n\x1b[31mred", ApprovalCodeHash: testkit.HashFor(t, d)})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAgent(e.ctx, "org-1", resp.AgentId)
	if strings.ContainsAny(a.Version, "\n\x1b") {
		t.Fatalf("control characters survived: %q", a.Version)
	}
}

func TestRejoinAfterOutageAndItsLimits(t *testing.T) {
	e := newEnv(t)
	id, key, leaf := e.approvedAgent(t, testkit.FP)
	real := *e.now
	rejoin := func(leafDER []byte, k *ecdsa.PrivateKey) error {
		_, err := e.core.Agents.Rejoin(e.ctx, "10.0.0.1", &continuumv1.RejoinRequest{ExpiredLeafDer: leafDER, CsrDer: testkit.CSRWith(t, k)})
		return err
	}

	// Offline for two days: the 24 h certificate is long expired, rejoin works.
	*e.now = real.Add(48 * time.Hour)
	if err := rejoin(leaf, key); err != nil {
		t.Fatalf("rejoin after a two day outage: %v", err)
	}
	cur, _ := e.st.GetAgent(e.ctx, "org-1", id)
	if string(cur.LeafDER) == string(leaf) {
		t.Fatal("the new certificate was not stored")
	}

	// A request made with a different key is refused even though the certificate is genuine.
	_, otherKey := testkit.CSR(t)
	if err := rejoin(leaf, otherKey); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("someone else's key: %v", err)
	}
	// A certificate from a different CA is refused.
	other, _ := pki.LoadOrCreate(t.TempDir())
	parsed, _ := pki.ParseCSR(testkit.CSRWith(t, key))
	foreign, _, _ := other.IssueAgent(parsed, id, "org-1", time.Hour)
	if err := rejoin(foreign, key); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("foreign CA: %v", err)
	}
	// Garbage is refused.
	if err := rejoin([]byte("nope"), key); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("garbage: %v", err)
	}
	// Too long ago: the window is pki.RejoinWindow (60 days).
	*e.now = real.Add(pki.RejoinWindow + 72*time.Hour)
	if err := rejoin(leaf, key); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("outside the window: %v", err)
	}
	// Revocation always wins, even inside the window.
	*e.now = real.Add(48 * time.Hour)
	if err := e.core.Agents.Revoke(e.ctx, "alex", id, "test"); err != nil {
		t.Fatal(err)
	}
	if err := rejoin(leaf, key); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("revoked agent rejoined: %v", err)
	}
}

func TestRejoinIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	e.core.EnrollRL = kernel.NewLimiter(1, 2)
	for i := 0; i < 5; i++ {
		_, err := e.core.Agents.Rejoin(e.ctx, "10.9.9.9", &continuumv1.RejoinRequest{ExpiredLeafDer: []byte("x"), CsrDer: []byte("x")})
		want := kernel.KindUnauthenticated
		if i >= 2 {
			want = kernel.KindRateLimited
		}
		if testkit.KindOf(err) != want {
			t.Fatalf("attempt %d: %v, want kind %v", i, err, want)
		}
	}
}

func TestRenewIsRateLimitedPerAgent(t *testing.T) {
	e := newEnv(t)
	id, _, _ := e.approvedAgent(t, testkit.FP)
	e.core.RenewRL = kernel.NewLimiter(1, 2)
	var limited int
	for i := 0; i < 5; i++ {
		d, _ := testkit.CSR(t)
		if _, _, err := e.core.Agents.Renew(e.ctx, id, d); testkit.KindOf(err) == kernel.KindRateLimited {
			limited++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if limited != 3 {
		t.Fatalf("limited %d of 5 renewals, want 3", limited)
	}
}

func TestLimitKeyGroupsIPv6ByPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":                      "203.0.113.9",
		"::ffff:203.0.113.9":               "203.0.113.9",
		"2001:db8:1:2:aaaa:bbbb:cccc:dddd": "2001:db8:1:2::/64",
		"2001:db8:1:2:1111:2222:3333:4444": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":                  "2001:db8:1:3::/64",
		"fe80::1%eth0":                     "fe80::/64",
		"not an address":                   "not an address",
		"unknown":                          "unknown",
	} {
		if got := kernel.LimitKey(in); got != want {
			t.Errorf("LimitKey(%q) = %q, want %q", in, got, want)
		}
	}
	// One host cycling through addresses in its /64 shares one allowance.
	l := kernel.NewLimiter(1, 3)
	allowed := 0
	for i := 0; i < 100; i++ {
		if l.Allow(kernel.LimitKey(fmt.Sprintf("2001:db8:9:9::%x", i+1))) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("%d of 100 requests from one /64 were allowed, want the burst of 3", allowed)
	}
}

func TestTapCutsOffUnauthenticatedCallersBeforeTheBodyIsRead(t *testing.T) {
	r := newRig(t)
	r.base.TapRL = kernel.NewLimiter(1, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := r.core.CA.Pin()

	// Enrollment: the third call from one address in a burst is refused by the tap.
	enr := continuumv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	var limited int
	for i := 0; i < 4; i++ {
		_, err := enr.PollEnrollment(ctx, &continuumv1.PollRequest{AgentId: "ag-x", PollSecret: "s"})
		if testkit.StatusCode(err) == codes.ResourceExhausted {
			limited++
		}
	}
	if limited != 2 {
		t.Fatalf("tap limited %d of 4, want 2", limited)
	}

	// AgentService without a certificate: refused even with a 12 MB message.
	anon := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	if _, err := anon.Renew(ctx, &continuumv1.RenewRequest{CsrDer: make([]byte, 12<<20)}); testkit.StatusCode(err) != codes.Unauthenticated {
		t.Fatalf("large anonymous message: %v", err)
	}
}

type hubRig struct {
	*rig
	hub  *estate.Hub
	plat *Platform
}

// newHubRig is newRig with the real sync hub behind the gRPC server.
func newHubRig(t *testing.T) *hubRig {
	t.Helper()
	e := newEnv(t)
	p := NewPlatform(e.base, nil)
	tn, err := p.Tenant(e.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	hub := tn.Hub
	e.core = tn.C // the hub reads its settings from this core
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: hub, plat: p}
}

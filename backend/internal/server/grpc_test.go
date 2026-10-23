package server

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agents"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type rig struct {
	*env
	addr string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	e := newEnv(t)
	certs := pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"})
	return &rig{env: e, addr: testkit.Serve(t, e.base.Agents, certs, &agents.BaseAgentService{C: e.base.Agents})}
}

func (r *rig) dial(t *testing.T, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	return testkit.Dial(t, r.addr, cfg)
}

// newRigTrustProxy is newRig with TrustAgentProxy on: every connection must carry a PROXY protocol
// header before its TLS handshake, and the address it declares - not the raw TCP peer, always
// 127.0.0.1 in this test process - is what gets recorded as the connecting agent's address.
func newRigTrustProxy(t *testing.T) *rig {
	t.Helper()
	e := newEnv(t)
	e.base.TrustAgentProxy = true
	certs := pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"})
	return &rig{env: e, addr: testkit.Serve(t, e.base.Agents, certs, &agents.BaseAgentService{C: e.base.Agents})}
}

// dialProxied is r.dial, but first writes a v1 PROXY protocol line declaring proxiedIP on the raw TCP
// connection, the way a load balancer configured to send it would - before grpc-go layers TLS on top of
// the same connection.
func (r *rig) dialProxied(t *testing.T, cfg *tls.Config, proxiedIP string) *grpc.ClientConn {
	t.Helper()
	return testkit.DialProxied(t, r.addr, cfg, proxiedIP)
}

func TestAgentBehindProxyUsesTheDeclaredAddressNotTheTCPPeer(t *testing.T) {
	r := newRigTrustProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	secret, _, _ := r.core.Agents.CreateToken(ctx, "admin", "edge", 2)
	d, _ := testkit.CSR(t)
	enr := ikhnosv1.NewEnrollmentClient(r.dialProxied(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil), "203.0.113.42"))
	hash, _ := testkit.ApprovalFor(t, d)
	resp, err := enr.Enroll(ctx, &ikhnosv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: 2, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.core.Store.GetAgent(ctx, "org-1", resp.AgentId)
	if err != nil {
		t.Fatal(err)
	}
	if a.ConnectingIP != "203.0.113.42" {
		t.Fatalf("ConnectingIP = %q, want the proxy-declared address, not the TCP peer", a.ConnectingIP)
	}
}

func TestAgentBehindProxyRefusesAConnectionThatSkippedTheProxy(t *testing.T) {
	r := newRigTrustProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, _ := testkit.CSR(t)
	// A plain dial with no PROXY protocol header in front - what a client would look like if it reached
	// this listener directly, bypassing the proxy TrustAgentProxy assumes is the only way in.
	enr := ikhnosv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil)))
	if _, err := enr.Enroll(ctx, &ikhnosv1.EnrollRequest{Token: "ikh_x", CsrDer: d, ClusterFingerprint: testkit.FP}); testkit.StatusCode(err) != codes.Unavailable {
		t.Fatalf("connection without a PROXY protocol header was accepted: %v", err)
	}
}

func TestTransportEnrollThenMutualTLS(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := r.core.CA.Pin()

	// 1. Enroll over pinned TLS, no client certificate.
	secret, _, _ := r.core.Agents.CreateToken(ctx, "admin", "edge", 2)
	d, key := testkit.CSR(t)
	enr := ikhnosv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	hash, appr := testkit.ApprovalFor(t, d)
	resp, err := enr.Enroll(ctx, &ikhnosv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: 2, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	// 2. Human approval, then the agent collects its certificate.
	if err := r.core.Agents.Approve(ctx, "alex", resp.AgentId, appr, 2); err != nil {
		t.Fatal(err)
	}
	poll, err := enr.PollEnrollment(ctx, &ikhnosv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
	if err != nil || poll.State != ikhnosv1.PollResponse_APPROVED {
		t.Fatalf("poll: %v %v", poll, err)
	}
	cert := &tls.Certificate{Certificate: [][]byte{poll.LeafDer}, PrivateKey: key}

	// 3. Mutual TLS: Renew works with the certificate...
	ag := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", cert)))
	nd, _ := testkit.CSR(t)
	if _, err := ag.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: nd}); err != nil {
		t.Fatalf("renew over mTLS: %v", err)
	}
	// ...and is refused without one, at the application layer, not just the handshake.
	anon := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	if _, err := anon.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: nd}); testkit.StatusCode(err) != codes.Unauthenticated {
		t.Fatalf("renew without client cert: %v", err)
	}
	// 4. Revocation is effective on the very next call even though the certificate is still valid.
	if err := r.core.Agents.Revoke(ctx, "alex", resp.AgentId, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: nd}); testkit.StatusCode(err) != codes.Unauthenticated {
		t.Fatalf("renew after revoke: %v", err)
	}
}

func TestTransportRefusesWrongPinForeignCAAndOldTLS(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, _ := testkit.CSR(t)
	call := func(cfg *tls.Config) error {
		enr := ikhnosv1.NewEnrollmentClient(r.dial(t, cfg))
		_, err := enr.Enroll(ctx, &ikhnosv1.EnrollRequest{Token: "ikh_x", CsrDer: d, ClusterFingerprint: testkit.FP})
		return err
	}
	// Reaching the application layer answers Unauthenticated (bad token); anything else means the handshake failed.
	if err := call(pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil)); testkit.StatusCode(err) != codes.Unauthenticated {
		t.Fatalf("control: expected to reach the server, got %v", err)
	}
	// One digit changed: always another pin (prefixing "00" is the same pin 1 time in 256).
	pin := r.core.CA.Pin()
	flip := map[byte]byte{'0': '1'}[pin[0]]
	if flip == 0 {
		flip = '0'
	}
	wrong := pki.ClientTLS(string(flip)+pin[1:], "127.0.0.1", nil)
	if err := call(wrong); testkit.StatusCode(err) != codes.Unavailable {
		t.Fatalf("wrong pin was accepted: %v", err)
	}
	if err := call(pki.ClientTLS(r.core.CA.Pin(), "evil.example.com", nil)); testkit.StatusCode(err) != codes.Unavailable {
		t.Fatalf("wrong hostname was accepted: %v", err)
	}
	old := pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil)
	old.MinVersion, old.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if err := call(old); testkit.StatusCode(err) != codes.Unavailable {
		t.Fatalf("TLS 1.2 was accepted: %v", err)
	}
	// A client certificate from a different CA is rejected in the handshake.
	other, _ := pki.LoadOrCreate(t.TempDir())
	req, key := testkit.CSR(t)
	parsed, _ := pki.ParseCSR(req)
	leaf, _, _ := other.IssueAgent(parsed, "ag-forged", "org-1", time.Hour)
	forged := pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key})
	if err := call(forged); testkit.StatusCode(err) != codes.Unavailable {
		t.Fatalf("certificate from a foreign CA was accepted: %v", err)
	}
}

func TestForgedIdentityFromOurCAButUnknownAgentIsRefused(t *testing.T) {
	// A certificate signed by our CA for an id that was never approved must not pass.
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, key := testkit.CSR(t)
	parsed, _ := pki.ParseCSR(req)
	leaf, _, _ := r.core.CA.IssueAgent(parsed, "ag-never-approved", "org-1", time.Hour)
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	ag := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	if _, err := ag.Renew(ctx, &ikhnosv1.RenewRequest{CsrDer: req}); testkit.StatusCode(err) != codes.Unauthenticated {
		t.Fatalf("unknown agent with a valid certificate: %v", err)
	}
}

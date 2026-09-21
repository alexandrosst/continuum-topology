package server

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type rig struct {
	*env
	addr string
	stop func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	e := newEnv(t)
	certs := pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"})
	srv := e.base.NewGRPC(certs, &BaseAgentService{C: e.base})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return &rig{env: e, addr: l.Addr().String()}
}

func (r *rig) dial(t *testing.T, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(r.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func code(err error) codes.Code { return status.Code(err) }

func TestTransportEnrollThenMutualTLS(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := r.core.CA.Pin()

	// 1. Enroll over pinned TLS, no client certificate.
	secret, _, _ := r.core.CreateToken(ctx, "admin", "edge", 2)
	d, key := csr(t)
	enr := continuumv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	resp, err := enr.Enroll(ctx, &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 2. Human approval, then the agent collects its certificate.
	if err := r.core.Approve(ctx, "alex", resp.AgentId, fp[:8], 2); err != nil {
		t.Fatal(err)
	}
	poll, err := enr.PollEnrollment(ctx, &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
	if err != nil || poll.State != continuumv1.PollResponse_APPROVED {
		t.Fatalf("poll: %v %v", poll, err)
	}
	cert := &tls.Certificate{Certificate: [][]byte{poll.LeafDer}, PrivateKey: key}

	// 3. Mutual TLS: Renew works with the certificate...
	ag := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", cert)))
	nd, _ := csr(t)
	if _, err := ag.Renew(ctx, &continuumv1.RenewRequest{CsrDer: nd}); err != nil {
		t.Fatalf("renew over mTLS: %v", err)
	}
	// ...and is refused without one, at the application layer, not just the handshake.
	anon := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	if _, err := anon.Renew(ctx, &continuumv1.RenewRequest{CsrDer: nd}); code(err) != codes.Unauthenticated {
		t.Fatalf("renew without client cert: %v", err)
	}
	// 4. Revocation is effective on the very next call even though the certificate is still valid.
	if err := r.core.Revoke(ctx, "alex", resp.AgentId, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Renew(ctx, &continuumv1.RenewRequest{CsrDer: nd}); code(err) != codes.Unauthenticated {
		t.Fatalf("renew after revoke: %v", err)
	}
}

func TestTransportRefusesWrongPinForeignCAAndOldTLS(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, _ := csr(t)
	call := func(cfg *tls.Config) error {
		enr := continuumv1.NewEnrollmentClient(r.dial(t, cfg))
		_, err := enr.Enroll(ctx, &continuumv1.EnrollRequest{Token: "cnt_x", CsrDer: d, ClusterFingerprint: fp})
		return err
	}
	// Reaching the application layer answers Unauthenticated (bad token); anything else means the handshake failed.
	if err := call(pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil)); code(err) != codes.Unauthenticated {
		t.Fatalf("control: expected to reach the server, got %v", err)
	}
	wrong := pki.ClientTLS("00"+r.core.CA.Pin()[2:], "127.0.0.1", nil)
	if err := call(wrong); code(err) != codes.Unavailable {
		t.Fatalf("wrong pin was accepted: %v", err)
	}
	if err := call(pki.ClientTLS(r.core.CA.Pin(), "evil.example.com", nil)); code(err) != codes.Unavailable {
		t.Fatalf("wrong hostname was accepted: %v", err)
	}
	old := pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", nil)
	old.MinVersion, old.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if err := call(old); code(err) != codes.Unavailable {
		t.Fatalf("TLS 1.2 was accepted: %v", err)
	}
	// A client certificate from a different CA is rejected in the handshake.
	other, _ := pki.LoadOrCreate(t.TempDir())
	req, key := csr(t)
	parsed, _ := pki.ParseCSR(req)
	leaf, _, _ := other.IssueAgent(parsed, "ag-forged", "org-1", time.Hour)
	forged := pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key})
	if err := call(forged); code(err) != codes.Unavailable {
		t.Fatalf("certificate from a foreign CA was accepted: %v", err)
	}
}

func TestForgedIdentityFromOurCAButUnknownAgentIsRefused(t *testing.T) {
	// A certificate signed by our CA for an id that was never approved must not pass.
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, key := csr(t)
	parsed, _ := pki.ParseCSR(req)
	leaf, _, _ := r.core.CA.IssueAgent(parsed, "ag-never-approved", "org-1", time.Hour)
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	ag := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	if _, err := ag.Renew(ctx, &continuumv1.RenewRequest{CsrDer: req}); code(err) != codes.Unauthenticated {
		t.Fatalf("unknown agent with a valid certificate: %v", err)
	}
}

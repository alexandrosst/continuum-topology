package estate

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/kernel"
	"ikhnos/internal/pki"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
)

func TestAgentsAreServedByTheirOwnOrganisationsHubOnly(t *testing.T) {
	e := newEnv(t)
	u2 := store.User{ID: "u-two", Username: "two", PasswordHash: "!", CreatedAt: time.Now()}
	_ = e.st.CreateUser(e.ctx, u2)
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Two", CreatedAt: time.Now(), CreatedBy: u2.ID}, u2.ID); err != nil {
		t.Fatal(err)
	}
	plat := newTestPlatform(e.base)
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.base.CA, []string{"127.0.0.1"}), plat)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	r := &rig{env: e, addr: l.Addr().String()}

	c2 := e.base.ForOrg("org-2")
	id1, key1, leaf1 := e.approvedAgent(t, testkit.FP)
	e2 := &env{base: e.base, core: c2, st: e.st, now: e.now, ctx: e.ctx}
	id2, key2, leaf2 := e2.approvedAgent(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := func(leaf []byte, key any) {
		cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
		c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(e.base.CA.Pin(), "127.0.0.1", cert)))
		s, err := c.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Hello{Hello: &ikhnosv1.Hello{AgentVersion: "t"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	connect(leaf1, key1)
	connect(leaf2, key2)

	t1, _ := plat.Tenant(ctx, "org-1")
	t2, _ := plat.Tenant(ctx, "org-2")
	has := func(h *Hub, id string) bool { h.Mu.Lock(); defer h.Mu.Unlock(); return h.Views[id] != nil }
	if !has(t1.Hub, id1) || has(t1.Hub, id2) || !has(t2.Hub, id2) || has(t2.Hub, id1) {
		t.Fatalf("hubs mixed the tenants up: org-1 has %v/%v, org-2 has %v/%v", has(t1.Hub, id1), has(t1.Hub, id2), has(t2.Hub, id1), has(t2.Hub, id2))
	}
	// The certificate carries its organisation and renewal keeps it, through the platform-wide core.
	d, _ := testkit.CSR(t)
	renewed, _, err := e.base.Agents.Renew(ctx, id2, d)
	if err != nil {
		t.Fatal(err)
	}
	if o := testkit.CertOrg(t, renewed); o != "org-2" {
		t.Fatalf("renewed certificate names %q", o)
	}
	// A revocation in one organisation cannot be ordered from another.
	if err := e.core.Agents.Revoke(ctx, "boss", id2, "not yours"); testkit.KindOf(err) != kernel.KindNotFound {
		t.Fatalf("org-1 revoking org-2's agent: %v", err)
	}
	if err := e.core.Agents.Reject(ctx, "boss", id2, "not yours"); testkit.KindOf(err) != kernel.KindNotFound {
		t.Fatalf("org-1 rejecting org-2's agent: %v", err)
	}
}

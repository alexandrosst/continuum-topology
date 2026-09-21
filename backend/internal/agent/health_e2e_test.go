package agent_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"continuum/internal/agent"
	"continuum/internal/pki"
	"continuum/internal/server"
	"continuum/internal/store"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// What the Kubernetes probes would see over an agent's life: not ready before the server was reached, ready while it waits for
// approval, ready and connected once approved, and never unlive; not ready again once revoked.
func TestHealthFollowsTheAgentThroughEnrollmentAndRevocation(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ca, _ := pki.LoadOrCreate(t.TempDir())
	core := server.NewCore(st, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	hub := server.NewHub(core)
	srv := core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()

	kube := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00002-aaaa-4bbb-8ccc-dddddddddddd"}})
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token, _, err := core.CreateToken(context.Background(), "test", "health", 1)
	if err != nil {
		t.Fatal(err)
	}
	h := agent.NewHealth()
	if h.Ready() {
		t.Fatal("ready before doing anything")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 1, Kube: kube, Identity: ids, Version: "test", Debounce: 100 * time.Millisecond, Health: h})
	}()

	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := state().Agents; return len(a) == 1 && a[0].Status == "pending" })
	waitFor(t, "ready while waiting for approval", 10*time.Second, h.Ready)
	if ok, why := h.Status(); !ok {
		t.Fatalf("pending must be ready: %s", why)
	}
	if !h.Live() {
		t.Error("not live while waiting")
	}
	agentID := state().Agents[0].ID
	if err := core.Approve(context.Background(), "test", agentID, codeOf(t, ids), 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", 15*time.Second, func() bool { _, why := h.Status(); return why == "connected" })
	if err := core.Revoke(context.Background(), "test", agentID, "done"); err != nil {
		t.Fatal(err)
	}
	<-done
	if h.Ready() {
		t.Error("a revoked agent must not be ready")
	}
	if !h.Live() {
		t.Error("a revoked agent must stay live, or Kubernetes would restart it for nothing")
	}
}

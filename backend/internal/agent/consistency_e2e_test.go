package agent_test

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"continuum/internal/agent"
	"continuum/internal/pki"
	"continuum/internal/server"
	"continuum/internal/store"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A live agent and server with a two-second consistency check: changes made while checks run must
// not be reported as drift, the check must actually run, and an administrator's measurement target
// reaches the agent, gets timed, and shows up as a measured path (and goes away when removed).
func TestConsistencyChecksAndMeasurementsEndToEnd(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ca, _ := pki.LoadOrCreate(t.TempDir())
	core := server.NewCore(st, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	hub := server.NewHub(core)
	hub.ConsistencyEvery = 2 * time.Second // the server limits a stream to 2 messages a second
	srv := core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()

	q := resource.MustParse
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00003-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	)
	var mu sync.Mutex
	var dialed []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		a, b := net.Pipe()
		go b.Close()
		return a, nil
	}
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token, _, _ := core.CreateToken(context.Background(), "test", "measured", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 2, Kube: kube, Identity: ids, Version: "test",
			Debounce: 50 * time.Millisecond, Measure: true, Floors: agent.Floors{Resync: time.Second, Beat: time.Second}, MeasureDial: dial})
	}()
	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := state().Agents; return len(a) == 1 && a[0].Status == "pending" })
	agentID := state().Agents[0].ID
	if err := core.Approve(context.Background(), "test", agentID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, func() bool { return len(state().Topology.Nodes) == 1 })
	clusterID := state().Agents[0].ClusterID

	// Keep changing the cluster while the checks run: none of it may look like drift.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			select {
			case <-stop:
				return
			case <-time.After(700 * time.Millisecond):
			}
			name := "app-" + string(rune('a'+i))
			_, _ = kube.AppsV1().Deployments("shop").Create(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"}}, metav1.CreateOptions{})
		}
	}()
	waitFor(t, "several consistency checks", 30*time.Second, func() bool {
		c := state().Agents[0].Consistency
		return c != nil && c.Checks >= 3
	})
	close(stop)
	wg.Wait()
	if n := len(state().Topology.Services); n != 8 {
		t.Fatalf("%d of the 8 new deployments arrived", n)
	}
	if c := state().Agents[0].Consistency; c.Differences != 0 {
		t.Fatalf("false drift while the cluster was changing: %+v", c)
	}
	evs, _ := st.ListEvents(context.Background(), "org", store.EventQuery{Since: time.Now().Add(-time.Hour), Kind: "drift"})
	if len(evs) != 0 {
		t.Fatalf("drift events in a healthy run: %+v", evs)
	}

	// Nothing is timed until an administrator (or observed traffic) names a target.
	if state().Agents[0].Measuring != 0 {
		t.Fatal("the agent was asked to measure with nothing configured")
	}
	if _, err := core.SaveSettings(context.Background(), "test", server.Settings{MeasureSeconds: 30, ProbeTargets: []server.ProbeTarget{
		{ClusterID: clusterID, Label: "cloud gateway", Host: "203.0.113.10", Port: 4433}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a measured path", 20*time.Second, func() bool {
		for _, p := range state().Topology.Paths {
			if p.Host == "203.0.113.10" && p.Samples > 0 {
				return true
			}
		}
		return false
	})
	p := state().Topology.Paths[0]
	if p.FromCluster != clusterID || p.Port != 4433 || p.Source != "manual" || p.Label != "cloud gateway" || p.LossPct != 0 || p.Stale {
		t.Fatalf("path = %+v", p)
	}
	mu.Lock()
	first := dialed[0]
	mu.Unlock()
	if first != "203.0.113.10:4433" {
		t.Fatalf("the agent dialed %q", first)
	}
	if state().Agents[0].Measuring != 1 {
		t.Fatalf("measuring = %d", state().Agents[0].Measuring)
	}

	// Removing the target stops the measurements and the path disappears.
	if _, err := core.SaveSettings(context.Background(), "test", server.Settings{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the path to go away", 20*time.Second, func() bool { return len(state().Topology.Paths) == 0 && state().Agents[0].Measuring == 0 })
}

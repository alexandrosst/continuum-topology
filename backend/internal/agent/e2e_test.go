package agent_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/model"
	"continuum/internal/pki"
	"continuum/internal/probe"
	"continuum/internal/server"
	"continuum/internal/store"

	"google.golang.org/protobuf/encoding/protojson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// codeOf is the approval code an agent printed in its log, which is what an administrator would type. The
// agent keeps it in its identity store while it waits, so the test reads it from there.
func codeOf(t *testing.T, ids agent.IdentityStore) string {
	t.Helper()
	var code string
	waitFor(t, "the agent's approval code", 10*time.Second, func() bool {
		id, _ := ids.Load(context.Background())
		if id != nil {
			code = id.ApprovalCode
		}
		return code != ""
	})
	return code
}

// The whole path with real TLS and a real gRPC stream: enroll, approve, stream, certificate
// renewal (with a short lifetime), live changes after renewal, then revocation.
func TestEnrollStreamRenewRevoke(t *testing.T) {
	old := pki.AgentCertTTL
	pki.AgentCertTTL = 8 * time.Second
	defer func() { pki.AgentCertTTL = old }()

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ca, _ := pki.LoadOrCreate(t.TempDir())
	core := server.NewCore(st, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	core.RenewRL = server.NewLimiter(6000, 1000) // a few-second certificate renews constantly
	hub := server.NewHub(core)
	srv := core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()

	q := resource.MustParse
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00001-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token, _, err := core.CreateToken(context.Background(), "test", "e2e-cluster", 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 2, Kube: kube, Identity: ids, Version: "test", Debounce: 100 * time.Millisecond})
	}()

	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := state().Agents; return len(a) == 1 && a[0].Status == "pending" })
	agentID := state().Agents[0].ID
	if len(state().Topology.Clusters) != 0 {
		t.Fatal("nothing may be shown before approval")
	}
	if err := core.Approve(context.Background(), "test", agentID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, func() bool { return len(state().Topology.Nodes) == 1 })
	first, _ := ids.Load(context.Background())
	if len(first.PollSecret) != 0 {
		t.Error("poll secret should be gone once approved")
	}

	// Renewal happens at half of the certificate's remaining life, then the agent reconnects.
	waitFor(t, "certificate renewal", 20*time.Second, func() bool {
		cur, _ := ids.Load(context.Background())
		return cur != nil && string(cur.CertDER) != string(first.CertDER)
	})
	time.Sleep(500 * time.Millisecond)
	// Changes made after renewal still arrive: the new certificate works on the new stream.
	_, err = kube.AppsV1().Deployments("shop").Create(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "late", Namespace: "shop"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deployment created after renewal", 15*time.Second, func() bool {
		for _, s := range state().Topology.Services {
			if s.Name == "late" {
				return true
			}
		}
		return false
	})
	// And the agent keeps going well past the original certificate's expiry.
	time.Sleep(time.Until(time.Now().Add(9 * time.Second)))
	_, _ = kube.AppsV1().Deployments("shop").Create(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "later", Namespace: "shop"}}, metav1.CreateOptions{})
	waitFor(t, "deployment created after the first certificate expired", 15*time.Second, func() bool {
		for _, s := range state().Topology.Services {
			if s.Name == "later" {
				return true
			}
		}
		return false
	})

	if err := core.Revoke(context.Background(), "test", agentID, "done"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, agent.ErrRevoked) {
			t.Fatalf("agent stopped with %v, want ErrRevoked", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent kept running after revocation")
	}
	// A revoked agent keeps only a marker (why, and which token it came from): no key, no certificate.
	if id, _ := ids.Load(context.Background()); id == nil || !id.Ended() || id.Key != nil || len(id.CertDER) != 0 {
		t.Errorf("a revoked agent must wipe its key and certificate and remember that it was revoked, got %+v", id)
	}
}

// An agent that was offline past its certificate's life comes back on its own, without a new token.
func TestRejoinAfterCertificateExpiredWhileOffline(t *testing.T) {
	old := pki.AgentCertTTL
	pki.AgentCertTTL = 3 * time.Second
	defer func() { pki.AgentCertTTL = old }()

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ca, _ := pki.LoadOrCreate(t.TempDir())
	core := server.NewCore(st, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	core.RenewRL = server.NewLimiter(6000, 1000)
	hub := server.NewHub(core)
	srv := core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()

	kube := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00002-aaaa-4bbb-8ccc-dddddddddddd"}})
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token, _, _ := core.CreateToken(context.Background(), "test", "rejoin-cluster", 2)
	cfg := func(tok string) agent.Config {
		return agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: tok, Tier: 2, Kube: kube, Identity: ids, Version: "test", Debounce: 100 * time.Millisecond}
	}
	run := func(tok string) (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- agent.Run(ctx, cfg(tok)) }()
		return cancel, done
	}
	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }

	cancel, done := run(token)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := state().Agents; return len(a) == 1 && a[0].Status == "pending" })
	agentID := state().Agents[0].ID
	if err := core.Approve(context.Background(), "test", agentID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, func() bool { return len(state().Topology.Clusters) == 1 })
	cancel()
	<-done

	// Stay away until whatever certificate the agent holds has expired.
	time.Sleep(4 * time.Second)
	before, _ := ids.Load(context.Background())
	leaf, _ := x509.ParseCertificate(before.CertDER)
	if time.Now().Before(leaf.NotAfter) {
		t.Fatal("test setup: the certificate has not expired yet")
	}

	// Back online with no token at all.
	cancel2, done2 := run("")
	defer cancel2()
	waitFor(t, "rejoin", 15*time.Second, func() bool {
		for _, a := range state().Agents {
			if a.ID == agentID && a.Connected {
				return true
			}
		}
		return false
	})
	after, _ := ids.Load(context.Background())
	if string(after.CertDER) == string(before.CertDER) {
		t.Error("the agent did not receive a new certificate")
	}
	found := false
	withAudit, _ := hub.StateFor(context.Background(), true) // State leaves the audit rows out; only administrators are shown them
	for _, ev := range withAudit.AuditLog {
		found = found || ev.Action == "agent-rejoined"
	}
	if !found {
		t.Error("the rejoin was not audited")
	}

	// A revoked agent cannot use rejoin to come back: it must stop and wipe its identity.
	cancel2()
	<-done2
	if err := core.Revoke(context.Background(), "test", agentID, "gone"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	_, done3 := run("")
	select {
	case err := <-done3:
		if !errors.Is(err, agent.ErrRevoked) {
			t.Fatalf("got %v, want ErrRevoked", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a revoked agent kept trying")
	}
}

// A node probe reports to the agent; its facts reach the server and change how the node is described,
// and a report signed with the wrong secret changes nothing.
func TestNodeProbeReachesTopology(t *testing.T) {
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

	q := resource.MustParse
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00002-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	)
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	probeAddr := pl.Addr().String()
	pl.Close()
	secret := []byte("0123456789abcdef0123456789abcdef")
	token, _, _ := core.CreateToken(context.Background(), "test", "probe-cluster", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	go agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 2, Kube: kube,
		Identity: ids, Version: "test", Debounce: 100 * time.Millisecond,
		Probes: probe.NewReceiver(secret, nil), ProbeListen: probeAddr})

	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(state().Agents) == 1 })
	if err := core.Approve(context.Background(), "test", state().Agents[0].ID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, func() bool { return len(state().Topology.Nodes) == 1 })
	if n := state().Topology.Nodes[0]; n.Probed || n.Kind != "vm" || n.Evidence["kind"].Confidence != "low" {
		t.Fatalf("before any probe the node is a low-confidence guess: %+v", n)
	}

	url := "http://" + probeAddr
	hc := &http.Client{Timeout: 5 * time.Second}
	waitFor(t, "probe receiver", 5*time.Second, func() bool {
		return probe.Push(ctx, hc, url, []byte("not the right secret, not at all"), "n1", &continuumv1.HostProbe{}, time.Now()) != nil
	})
	time.Sleep(300 * time.Millisecond)
	if state().Topology.Nodes[0].Probed {
		t.Fatal("a report signed with the wrong secret must change nothing")
	}
	// A report about a node the cluster does not have is accepted but never shown.
	if err := probe.Push(ctx, hc, url, secret, "ghost", &continuumv1.HostProbe{SysVendor: "Ghost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := probe.Push(ctx, hc, url, secret, "n1", &continuumv1.HostProbe{SysVendor: "Dell Inc.", ProductName: "PowerEdge R640", Uplinks: []string{"ethernet"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "probe facts in the topology", 10*time.Second, func() bool { return state().Topology.Nodes[0].Probed })
	n := state().Topology.Nodes[0]
	if n.Kind != "bare-metal" || n.HardwareModel != "Dell Inc. PowerEdge R640" || n.Connectivity != "ethernet" || n.Evidence["kind"].Confidence != "high" {
		t.Fatalf("node after the probe = %+v", n)
	}
	if len(state().Topology.Nodes) != 1 {
		t.Fatal("a probe report must never create a node")
	}
}

// Observed traffic: a node flow collector reports raw addresses to the agent, the agent attributes them
// to workloads, the server turns them into a dependency, and a report signed with the probe's secret is refused.
func TestObservedTrafficReachesTopology(t *testing.T) {
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

	dep := func(name, app string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}}}}}
	}
	pod := func(name, rs, ip string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs}}},
			Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip}}
	}
	q := resource.MustParse
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00003-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		dep("cart", "cart"), dep("db", "db"),
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "cart-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "cart"}}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "db-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "db"}}}},
		pod("cart-1-a", "cart-1", "10.42.0.5"), pod("db-1-a", "db-1", "10.42.0.7"),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "db"}, ClusterIP: "10.43.0.21", Ports: []corev1.ServicePort{{Port: 5432}}}},
	)
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := pl.Addr().String()
	pl.Close()
	flowSecret, probeSecret := []byte("flow-secret-0123456789abcdef0123"), []byte("probe-secret-0123456789abcdef012")
	token, _, _ := core.CreateToken(context.Background(), "test", "flow-cluster", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	go agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 2, Kube: kube,
		Identity: ids, Version: "test", Debounce: 100 * time.Millisecond,
		Probes: probe.NewReceiver(probeSecret, nil), Flows: flow.NewPipeline(flowSecret, func() *collect.Index { return nil }, nil),
		ProbeListen: addr, FlowWindow: 300 * time.Millisecond})

	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(state().Agents) == 1 })
	if err := core.Approve(context.Background(), "test", state().Agents[0].ID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workloads", 15*time.Second, func() bool { return len(state().Topology.Services) == 2 })

	post := func(secret []byte, rep *continuumv1.FlowReport) error {
		body, _ := protojson.Marshal(rep)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req, _ := http.NewRequest("POST", "http://"+addr+flow.PathReport, bytes.NewReader(body))
		req.Header.Set(probe.HeaderNode, "n1")
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.Sign(secret, ts, "n1", body))
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return errors.New(resp.Status)
		}
		return nil
	}
	rep := &continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true, Flows: []*continuumv1.RawFlow{
		{Client: true, LocalIp: "10.42.0.5", PeerIp: "10.43.0.21", Port: 5432, Protocol: "tcp", Connections: 7, BytesOut: 700, BytesIn: 2100},
		{Client: true, LocalIp: "10.42.0.5", PeerIp: "93.184.216.34", Port: 443, Protocol: "tcp", Connections: 2},
	}}
	waitFor(t, "receiver", 5*time.Second, func() bool { return post([]byte("wrong secret, wrong secret, wrong!"), rep) != nil })
	if err := post(probeSecret, rep); err == nil {
		t.Fatal("the node probe's secret must not be accepted for flows")
	}
	if len(state().Topology.Dependencies) != 0 {
		t.Fatal("refused reports must change nothing")
	}
	if err := post(flowSecret, rep); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "observed dependencies", 10*time.Second, func() bool { return len(state().Topology.Dependencies) == 2 })
	d := state().Topology
	var toDB, toWeb bool
	for _, x := range d.Dependencies {
		switch x.Port {
		case 5432:
			toDB = x.ToKind == "service" && x.Connections == 7 && x.Via == "ebpf" && !x.CrossCluster
		case 443:
			toWeb = x.ToKind == "external"
		}
	}
	if !toDB || !toWeb || len(d.ExternalEndpoints) != 1 || d.ExternalEndpoints[0].Host != "93.184.216.34" {
		t.Fatalf("dependencies = %+v, external = %+v", d.Dependencies, d.ExternalEndpoints)
	}

	// The observer's health reaches the dashboard: which node is observed and how.
	obs := func() *server.ObserverDoc { return state().Agents[0].Observer }
	waitFor(t, "observer health", 5*time.Second, func() bool { return obs() != nil && len(obs().Collectors) == 1 })
	if c := obs().Collectors[0]; c.Node != "n1" || c.Method != "ebpf" || !c.BytesKnown {
		t.Errorf("collector = %+v", c)
	}
	// A quiet window from the same collector keeps it listed: silence must not read as "no observer".
	quiet := &continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true}
	if err := post(flowSecret, quiet); err != nil {
		t.Fatal(err)
	}
	time.Sleep(900 * time.Millisecond)
	if o := obs(); o == nil || len(o.Collectors) != 1 {
		t.Errorf("a collector that reported nothing this window disappeared: %+v", o)
	}
}

// Scope and service mesh, end to end: an agent told to skip a namespace never sends it, and the mesh found in
// the cluster arrives in the topology with the control plane marked as such.
func TestScopeAndMeshReachTheTopology(t *testing.T) {
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

	dep := func(ns, name, image string, tmpl map[string]string, containers ...string) []runtime.Object {
		var cs []corev1.Container
		for _, c := range containers {
			cs = append(cs, corev1.Container{Name: c, Image: image})
		}
		one := int32(1)
		return []runtime.Object{
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec:   appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: tmpl}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: image}}}}},
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs", Namespace: ns, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-1", Namespace: ns, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: name + "-rs"}}},
				Spec: corev1.PodSpec{NodeName: "n1", Containers: cs}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		}
	}
	q := resource.MustParse
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "e2e00003-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "istio-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"istio-injection": "enabled"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	}
	objs = append(objs, dep("istio-system", "istiod", "docker.io/istio/pilot:1.22.3", map[string]string{"istio": "pilot"}, "istiod")...)
	objs = append(objs, dep("shop", "web", "web:1", map[string]string{"app": "web"}, "web", "istio-proxy")...)
	objs = append(objs, dep("payments", "ledger", "ledger:1", map[string]string{"app": "ledger"}, "ledger")...)
	kube := fake.NewSimpleClientset(objs...)

	scope, err := collect.ParseScope("", "payments", "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, _ := core.CreateToken(context.Background(), "test", "mesh-cluster", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	go agent.Run(ctx, agent.Config{Server: l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: 2, Kube: kube, Scope: scope,
		Identity: ids, Version: "test", Debounce: 100 * time.Millisecond})

	state := func() server.StateDoc { d, _ := hub.State(context.Background()); return d }
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(state().Agents) == 1 })
	if err := core.Approve(context.Background(), "test", state().Agents[0].ID, codeOf(t, ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "services in the topology", 15*time.Second, func() bool { return len(state().Topology.Services) >= 2 })
	d := state()

	var names []string
	for _, s := range d.Topology.Services {
		names = append(names, s.Name)
		if s.Name == "ledger" {
			t.Fatal("a workload in an excluded namespace reached the server")
		}
	}
	for _, n := range d.Topology.Namespaces {
		if n.Name == "payments" {
			t.Fatal("an excluded namespace reached the server")
		}
	}
	if sc := d.Agents[0].Scope; sc == nil || sc.Total != 3 || sc.InScope != 2 || sc.Description != "1 namespace left out by name" {
		t.Fatalf("agent scope: %+v (services %v)", sc, names)
	}
	cl := d.Topology.Clusters[0]
	if cl.Mesh == nil || cl.Mesh.Kind != "istio" || cl.Mesh.Version != "1.22.3" || len(cl.Mesh.ControlPlane) != 1 {
		t.Fatalf("cluster mesh: %+v", cl.Mesh)
	}
	byName := map[string]model.Service{}
	for _, s := range d.Topology.Services {
		byName[s.Name] = s
	}
	if m := byName["web"].Mesh; m == nil || m.Proxy != "sidecar" || m.Source != "pods" {
		t.Errorf("web: %+v", m)
	}
	if m := byName["istiod"].Mesh; m == nil || !m.ControlPlane || byName["istiod"].ID != cl.Mesh.ControlPlane[0] {
		t.Errorf("istiod: %+v", byName["istiod"])
	}
	var haveMesh bool
	for _, m := range d.Agents[0].Modules {
		haveMesh = haveMesh || m.Name == "mesh"
	}
	if !haveMesh {
		t.Errorf("the mesh module should be listed once a mesh is found: %+v", d.Agents[0].Modules)
	}
}

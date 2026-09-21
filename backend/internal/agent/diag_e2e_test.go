package agent_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/facts"
	"continuum/internal/flow"
	"continuum/internal/pki"
	"continuum/internal/probe"
	"continuum/internal/server"
	"continuum/internal/store"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// dx is a real server and a real agent on a real gRPC stream, with the agent's timers set short. Each test builds the one it needs.
type dx struct {
	t      *testing.T
	st     *store.SQLite
	core   *server.Core
	hub    *server.Hub
	srv    *server.GRPCServer
	l      net.Listener
	kube   *fake.Clientset
	ids    agent.IdentityStore
	cfg    agent.Config
	cancel context.CancelFunc
	done   chan error
	over   chan struct{} // closed when Run has returned
	id     string
}

type dxOpts struct {
	installed int // the tier the agent's install allows (Config.Tier)
	approve   int // the tier the administrator approves
	objects   []runtime.Object
	uid       string
	limits    *facts.Limits
	tune      func(*agent.Config, *server.Core, *server.Hub)
	ids       func(agent.IdentityStore) agent.IdentityStore
	noApprove bool
}

func newDx(t *testing.T, o dxOpts) *dx {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, _ := pki.LoadOrCreate(t.TempDir())
	core := server.NewCore(st, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	core.RenewRL = server.NewLimiter(6000, 1000)
	hub := server.NewHub(core)
	hub.ConsistencyEvery = 30 * time.Second
	d := &dx{t: t, st: st, core: core, hub: hub}
	if o.uid == "" {
		o.uid = "d1a90001-aaaa-4bbb-8ccc-dddddddddddd"
	}
	q := resource.MustParse
	objs := append([]runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "d1a90001-aaaa-4bbb-8ccc-dddddddddddd"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}},
	}, o.objects...)
	d.kube = fake.NewSimpleClientset(objs...)
	if o.installed == 0 && o.approve == 0 {
		o.installed, o.approve = 2, 2
	}
	var ids agent.IdentityStore = agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	if o.ids != nil {
		ids = o.ids(ids)
	}
	d.ids = ids
	d.srv = core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	d.l, _ = net.Listen("tcp", "127.0.0.1:0")
	go d.srv.Serve(d.l)
	t.Cleanup(d.srv.Stop)
	token, _, err := core.CreateToken(context.Background(), "test", "dx", 2)
	if err != nil {
		t.Fatal(err)
	}
	d.cfg = agent.Config{Server: d.l.Addr().String(), CAPin: ca.Pin(), Token: token, Tier: o.installed, Kube: d.kube, Identity: ids, Version: "dx-1",
		Debounce: 50 * time.Millisecond, Floors: agent.Floors{Resync: time.Second, Beat: time.Second}, SyncWait: 2 * time.Second}
	if o.tune != nil {
		o.tune(&d.cfg, core, hub)
	}
	d.start()
	waitFor(t, "pending agent", 15*time.Second, func() bool { a := d.state().Agents; return len(a) == 1 && a[0].Status == "pending" })
	d.id = d.state().Agents[0].ID
	if !o.noApprove {
		if err := core.Approve(context.Background(), "test", d.id, codeOf(t, ids), o.approve); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func (d *dx) start() {
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan error, 1)
	over := make(chan struct{})
	d.over = over
	cfg := d.cfg
	go func() { d.done <- agent.Run(ctx, cfg); close(over) }()
	d.t.Cleanup(func() {
		cancel()
		select {
		case <-over:
		case <-time.After(20 * time.Second):
		}
	})
}

func (d *dx) state() server.StateDoc { s, _ := d.hub.State(context.Background()); return s }

func (d *dx) agentDoc() server.AgentDoc {
	for _, a := range d.state().Agents {
		if a.ID == d.id {
			return a
		}
	}
	return server.AgentDoc{}
}

func (d *dx) diag() *server.DiagnosticsDoc { return d.agentDoc().Diagnostics }

// waitDiagAny is waitDiag for an account that may be the one the hello carried (a stream that ends before its first heartbeat).
func (d *dx) waitDiagAny(what string, cond func(*server.DiagnosticsDoc) bool) *server.DiagnosticsDoc {
	d.t.Helper()
	var got *server.DiagnosticsDoc
	waitFor(d.t, what, 30*time.Second, func() bool {
		got = d.diag()
		return got != nil && cond(got)
	})
	return got
}

// waitDiag waits for a full (not just the hello's) account that satisfies cond.
func (d *dx) waitDiag(what string, cond func(*server.DiagnosticsDoc) bool) *server.DiagnosticsDoc {
	d.t.Helper()
	var got *server.DiagnosticsDoc
	waitFor(d.t, what, 30*time.Second, func() bool {
		got = d.diag()
		return got != nil && !got.Partial && cond(got)
	})
	return got
}

func problem(d *server.DiagnosticsDoc, code string) *server.ProblemDoc {
	for i := range d.Problems {
		if d.Problems[i].Code == code {
			return &d.Problems[i]
		}
	}
	return nil
}

func services(d *dx, ns string) int {
	n := 0
	for _, s := range d.state().Topology.Services {
		if ns == "" || s.Namespace == ns {
			n++
		}
	}
	return n
}

// ---- diagnostics ----

func TestDiagnosticsShowInstalledApprovedAndEffectiveTiers(t *testing.T) {
	d := newDx(t, dxOpts{installed: 2, approve: 1})
	g := d.waitDiag("the first full account", func(g *server.DiagnosticsDoc) bool { return len(g.Informers) > 0 })
	if g.InstalledTier != 2 || g.ApprovedTier != 1 || g.EffectiveTier != 1 {
		t.Fatalf("tiers: installed %d approved %d effective %d", g.InstalledTier, g.ApprovedTier, g.EffectiveTier)
	}
	if g.AgentVersion != "dx-1" || g.Arch == "" || g.OS == "" || g.Scope == nil {
		t.Fatalf("identity and scope: %+v", g)
	}
	names := map[string]bool{}
	for _, c := range g.Collectors {
		names[c.Name] = true
		if c.Configured || c.Enabled {
			t.Errorf("collector %s is configured in a bare install: %+v", c.Name, c)
		}
	}
	if !names["probes"] || !names["flow"] || !names["measure"] {
		t.Fatalf("collectors: %+v", g.Collectors)
	}
	for _, i := range g.Informers {
		if i.Module == "" && i.Name == "" {
			t.Errorf("informer without a name: %+v", i)
		}
	}
	for _, p := range g.Problems {
		if p.Severity != "info" {
			t.Errorf("a healthy agent reports a %s problem: %+v", p.Severity, p)
		}
	}
	if d.agentDoc().InstalledTier != 2 {
		t.Fatalf("the server did not record the ceiling: %+v", d.agentDoc())
	}
	// Tier 1 collects no workloads.
	if services(d, "") != 0 {
		t.Fatalf("services shown at tier 1: %d", services(d, ""))
	}

	// Widening within the ceiling is taken up by the running agent (it reconnects to collect at the new tier).
	if _, err := d.hub.SetAgentTier(context.Background(), "test", d.id, 2, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workloads at tier 2", 20*time.Second, func() bool { return services(d, "shop") == 1 })
	g = d.waitDiag("effective 2", func(g *server.DiagnosticsDoc) bool { return g.EffectiveTier == 2 && g.ApprovedTier == 2 })
	// Narrowing takes effect at once and the agent stops reading what it no longer may.
	if _, err := d.hub.SetAgentTier(context.Background(), "test", d.id, 0, nil); err != nil {
		t.Fatal(err)
	}
	g = d.waitDiag("effective 0", func(g *server.DiagnosticsDoc) bool { return g.EffectiveTier == 0 && g.ApprovedTier == 0 })
	waitFor(t, "no workloads at tier 0", 10*time.Second, func() bool { return services(d, "") == 0 && len(d.state().Topology.Nodes) == 0 })
}

func TestAnOverCeilingTierIsRefusedWithTheHelmCommand(t *testing.T) {
	d := newDx(t, dxOpts{installed: 1, approve: 1})
	waitFor(t, "first picture", 15*time.Second, func() bool { return d.agentDoc().Synced })
	_, err := d.hub.SetAgentTier(context.Background(), "test", d.id, 2, func(n int) string {
		return fmt.Sprintf("helm upgrade continuum-agent ./chart.tgz --namespace continuum-system --reuse-values --set access.tier=%d", n)
	})
	var se *server.Error
	if !errors.As(err, &se) || !strings.Contains(se.Msg, "helm upgrade continuum-agent ./chart.tgz --namespace continuum-system --reuse-values --set access.tier=2") {
		t.Fatalf("err = %v", err)
	}
	if a, _ := d.st.GetAgent(context.Background(), d.id); a.AccessTier != 1 || a.InstalledTier != 1 {
		t.Fatalf("agent record changed: %+v", a)
	}
}

func TestForbiddenResourcesAreReportedAndTheAgentCarriesOn(t *testing.T) {
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		kube := c.Kube.(*fake.Clientset)
		kube.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New(`User "system:serviceaccount:continuum-system:continuum-agent" cannot list resource "deployments" in API group "apps" at the cluster scope`))
		})
	}})
	g := d.waitDiag("an rbac problem", func(g *server.DiagnosticsDoc) bool { return problem(g, "rbac_forbidden") != nil })
	p := problem(g, "rbac_forbidden")
	if p.Severity != "error" || !strings.Contains(p.Message, "deployments") || !strings.Contains(p.Message, "list") || !strings.Contains(p.Message, "helm upgrade --reuse-values") {
		t.Fatalf("problem = %+v", p)
	}
	var forbidden bool
	for _, i := range g.Informers {
		forbidden = forbidden || (strings.Contains(i.Name, "deployments") && !i.Synced && i.LastError != "")
	}
	if !forbidden {
		t.Fatalf("the informer for deployments should be shown as failing: %+v", g.Informers)
	}
	// Half blind, and it says so: the nodes are there, the workloads module is in error, and it did not stop.
	waitFor(t, "the nodes despite the missing permission", 20*time.Second, func() bool { return len(d.state().Topology.Nodes) == 1 })
	var moduleErr bool
	for _, m := range d.agentDoc().Modules {
		moduleErr = moduleErr || (m.Status == "error" && strings.Contains(m.Reason, "deployments"))
	}
	if !moduleErr {
		t.Fatalf("modules do not say what is missing: %+v", d.agentDoc().Modules)
	}
}

func TestScopeEmptyAndClockSkewAreReported(t *testing.T) {
	d := newDx(t, dxOpts{tune: func(c *agent.Config, core *server.Core, _ *server.Hub) {
		sc, err := collect.ParseScope("no-such-namespace", "", "")
		if err != nil {
			t.Fatal(err)
		}
		c.Scope = sc
		core.Now = func() time.Time { return time.Now().Add(-10 * time.Minute) } // the server's clock is ten minutes behind the agent's
	}})
	g := d.waitDiag("scope and clock problems", func(g *server.DiagnosticsDoc) bool {
		return problem(g, "scope_empty") != nil && problem(g, "clock_skew") != nil
	})
	if p := problem(g, "scope_empty"); p.Severity != "warn" || !strings.Contains(p.Message, "no-such-namespace") {
		t.Fatalf("scope_empty: %+v", p)
	}
	if p := problem(g, "clock_skew"); p.Severity != "warn" || !strings.Contains(p.Message, "ahead") {
		t.Fatalf("clock_skew: %+v", p)
	}
}

func TestASilentCollectorIsReported(t *testing.T) {
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.Probes = probe.NewReceiver([]byte("secret-secret-secret-secret-1234"), nil)
		c.ProbeInterval = 100 * time.Millisecond // silent after three of these
	}})
	g := d.waitDiag("collector_silent", func(g *server.DiagnosticsDoc) bool { return problem(g, "collector_silent") != nil })
	p := problem(g, "collector_silent")
	if p.Severity == "info" || !strings.Contains(p.Message, "node probe") {
		t.Fatalf("problem = %+v", p)
	}
	var probes server.AgentCollectorDoc
	for _, c := range g.Collectors {
		if c.Name == "probes" {
			probes = c
		}
	}
	if !probes.Configured || !probes.Enabled || probes.Producing {
		t.Fatalf("probes = %+v", probes)
	}
}

// ---- consent overrides ----

func TestPausedCollectorsAndExcludedNamespacesTakeEffect(t *testing.T) {
	secret := []byte("secret-secret-secret-secret-1234")
	var dials atomic.Int64
	var pipe *flow.Pipeline
	var recv *probe.Receiver
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		recv = probe.NewReceiver(secret, nil)
		pipe = flow.NewPipeline(secret, nil, nil)
		c.Probes, c.Flows = recv, pipe
		c.Measure = true
		c.MeasureDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			a, b := net.Pipe()
			go b.Close()
			return a, nil
		}
		c.Floors.Measure = time.Second
	}})
	waitFor(t, "first picture", 20*time.Second, func() bool { return services(d, "shop") == 1 })
	clusterID := d.agentDoc().ClusterID
	if _, err := d.core.SaveSettings(context.Background(), "test", server.Settings{MeasureSeconds: 30, ProbeTargets: []server.ProbeTarget{{ClusterID: clusterID, Label: "gw", Host: "203.0.113.10", Port: 4433}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "measuring", 30*time.Second, func() bool { return dials.Load() > 0 })

	// Pause everything optional, and leave the shop namespace out.
	if _, err := d.hub.SetConsent(context.Background(), "test", d.id, server.Consent{Paused: []string{"flow", "probes", "measure"}, Excluded: []string{"shop"}}); err != nil {
		t.Fatal(err)
	}
	g := d.waitDiag("the overrides in force", func(g *server.DiagnosticsDoc) bool { return len(g.PausedCollectors) == 3 && g.ExcludedNamespaces == 1 })
	for _, c := range g.Collectors {
		if !c.PausedByServer || c.Producing {
			t.Errorf("collector %s should be paused and not producing: %+v", c.Name, c)
		}
	}
	// Really stopped: the aggregator takes nothing in, nothing is timed, and the excluded namespace's workloads are gone.
	pipe.Aggregator.Seen("node-a", "ebpf", true)
	if n, _ := pipe.Aggregator.Presence(); n != 0 {
		t.Fatalf("a paused flow collector still registers nodes: %d", n)
	}
	if fb := pipe.Aggregator.Flush(); fb != nil {
		t.Fatalf("a paused flow collector still flushes: %+v", fb)
	}
	waitFor(t, "the excluded namespace to disappear", 20*time.Second, func() bool { return services(d, "shop") == 0 })
	time.Sleep(1500 * time.Millisecond)
	settled := dials.Load()
	time.Sleep(2500 * time.Millisecond)
	if dials.Load() != settled {
		t.Fatalf("connections are still being timed after measure was paused (%d, then %d)", settled, dials.Load())
	}
	if n, _ := recv.Presence(time.Hour); n != 0 {
		t.Fatalf("a paused probe receiver still remembers nodes: %d", n)
	}

	// Lifting the overrides brings it all back.
	if _, err := d.hub.SetConsent(context.Background(), "test", d.id, server.Consent{}); err != nil {
		t.Fatal(err)
	}
	d.waitDiag("the overrides lifted", func(g *server.DiagnosticsDoc) bool { return len(g.PausedCollectors) == 0 && g.ExcludedNamespaces == 0 })
	waitFor(t, "shop to return", 20*time.Second, func() bool { return services(d, "shop") == 1 })
	waitFor(t, "timing to resume", 40*time.Second, func() bool { return dials.Load() > settled })
}

func TestAWideningOverrideFromTheServerIsIgnoredAndReported(t *testing.T) {
	// A server that approves more than the install allows (a bug, or a hostile one) gains nothing: the agent stays at its own ceiling and says so.
	d := newDx(t, dxOpts{installed: 1, approve: 1})
	waitFor(t, "first picture", 15*time.Second, func() bool { return d.agentDoc().Synced })
	if err := d.st.SetInstalledTier(context.Background(), d.id, 2); err != nil { // the record now claims the install allows 2
		t.Fatal(err)
	}
	if err := d.st.SetAccessTier(context.Background(), d.id, 2); err != nil {
		t.Fatal(err)
	}
	// Any change of settings makes the server push a fresh Config, which now approves tier 2.
	if _, err := d.core.SaveSettings(context.Background(), "test", server.Settings{MeasureSeconds: 45}); err != nil {
		t.Fatal(err)
	}
	g := d.waitDiag("override_ignored", func(g *server.DiagnosticsDoc) bool { return problem(g, "override_ignored") != nil })
	p := problem(g, "override_ignored")
	if !strings.Contains(p.Message, "helm upgrade --reuse-values") || !strings.Contains(p.Message, "above the tier 1") {
		t.Fatalf("problem = %+v", p)
	}
	if g.InstalledTier != 1 || g.EffectiveTier != 1 || g.ApprovedTier != 2 {
		t.Fatalf("installed %d, approved %d, effective %d", g.InstalledTier, g.ApprovedTier, g.EffectiveTier)
	}
	time.Sleep(2 * time.Second)
	if services(d, "") != 0 {
		t.Fatal("workloads were collected above the install's ceiling")
	}
}

// ---- chunked full sync ----

func manyDeployments(n int) []runtime.Object {
	var out []runtime.Object
	for i := 0; i < n; i++ {
		out = append(out, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%05d", i), Namespace: fmt.Sprintf("ns%d", i%20),
			Labels: map[string]string{"app": fmt.Sprintf("app-%05d", i)}}})
	}
	return out
}

func TestALargePictureIsSentInChunksAndAppliedWhole(t *testing.T) {
	before := server.Metrics.SyncsChunked()
	d := newDx(t, dxOpts{objects: manyDeployments(400), tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) { c.ChunkBytes = 4000 }})
	waitFor(t, "all 401 workloads", 60*time.Second, func() bool { return services(d, "") == 401 })
	if server.Metrics.SyncsChunked() <= before {
		t.Fatal("the picture was not chunked")
	}
	if p := problem(d.waitDiag("account", func(*server.DiagnosticsDoc) bool { return true }), "sync_too_large"); p != nil {
		t.Fatalf("problem: %+v", p)
	}
}

func TestAPictureOverTheServersLimitsIsReportedAsAProblem(t *testing.T) {
	limits := &facts.Limits{Nodes: 10, Namespaces: 100, Workloads: 100, Bytes: 8 << 20}
	d := newDx(t, dxOpts{objects: manyDeployments(300), noApprove: true, tune: func(c *agent.Config, _ *server.Core, h *server.Hub) {
		h.Limits = limits
		c.ChunkBytes = 4000
	}})
	if err := d.core.Approve(context.Background(), "test", d.id, codeOf(t, d.ids), 2); err != nil {
		t.Fatal(err)
	}
	g := d.waitDiagAny("sync_too_large", func(g *server.DiagnosticsDoc) bool { return problem(g, "sync_too_large") != nil })
	p := problem(g, "sync_too_large")
	if p.Severity != "error" || !strings.Contains(p.Message, "workloads") {
		t.Fatalf("problem = %+v", p)
	}
	if services(d, "") != 0 {
		t.Fatal("a refused picture was partly applied")
	}
}

func TestSixtyThousandWorkloadsFromARealAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("large")
	}
	before := server.Metrics.SyncsChunked()
	d := newDx(t, dxOpts{objects: manyDeployments(60000), noApprove: true, tune: func(c *agent.Config, _ *server.Core, h *server.Hub) {
		h.Limits = &facts.Limits{Nodes: 100, Namespaces: 1000, Workloads: 100000, Bytes: 128 << 20}
		c.SyncWait = 90 * time.Second
	}})
	if err := d.core.Approve(context.Background(), "test", d.id, codeOf(t, d.ids), 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "60001 workloads", 150*time.Second, func() bool { return services(d, "") == 60001 })
	if server.Metrics.SyncsChunked() <= before {
		t.Fatal("not chunked")
	}
}

// ---- resilience ----

func TestShutdownIsPromptAndCleanWhileStreaming(t *testing.T) {
	d := newDx(t, dxOpts{})
	waitFor(t, "connected and synced", 20*time.Second, func() bool { a := d.agentDoc(); return a.Connected && a.Synced })
	start := time.Now()
	d.cancel()
	select {
	case err := <-d.done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the agent did not stop within 6 seconds of being told to")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
	waitFor(t, "the server to see the stream end", 5*time.Second, func() bool { return !d.agentDoc().Connected })
	// The identity is intact for the next start: nothing was left half written.
	if id, err := d.ids.Load(context.Background()); err != nil || id == nil || id.Key == nil || len(id.CertDER) == 0 {
		t.Fatalf("identity after shutdown: %+v %v", id, err)
	}
}

func TestShutdownDoesNotHangOnAKubernetesAPIThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var block atomic.Bool
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.Kube.(*fake.Clientset).PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
			if block.Load() {
				<-release
			}
			return false, nil, nil
		})
		c.SyncWait = time.Minute
	}, noApprove: true})
	block.Store(true)
	if err := d.core.Approve(context.Background(), "test", d.id, codeOf(t, d.ids), 2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // the agent is now approved and its collector is waiting for an API that does not answer
	start := time.Now()
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent hung on shutdown while its collector was starting")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
}

// panicIdentity panics the first time the identity is loaded after being armed.
type panicIdentity struct {
	agent.IdentityStore
	armed atomic.Bool
}

func (p *panicIdentity) Load(ctx context.Context) (*agent.Identity, error) {
	if p.armed.CompareAndSwap(true, false) {
		panic("identity store exploded")
	}
	return p.IdentityStore.Load(ctx)
}

func TestAPanicInTheStreamLoopIsRecoveredAndReported(t *testing.T) {
	var pi *panicIdentity
	d := newDx(t, dxOpts{ids: func(s agent.IdentityStore) agent.IdentityStore { pi = &panicIdentity{IdentityStore: s}; return pi }})
	waitFor(t, "connected and synced", 20*time.Second, func() bool { a := d.agentDoc(); return a.Connected && a.Synced })
	pi.armed.Store(true)
	if _, err := d.hub.SetAgentTier(context.Background(), "test", d.id, 1, nil); err != nil { // makes the agent reconnect; loading its identity then panics
		t.Fatal(err)
	}
	g := d.waitDiagAny("internal_error", func(g *server.DiagnosticsDoc) bool { return problem(g, "internal_error") != nil })
	if p := problem(g, "internal_error"); p.Severity != "error" || !strings.Contains(p.Message, "identity store exploded") {
		t.Fatalf("problem = %+v", p)
	}
	// The loop restarted: the agent is connected and syncing again after the panic.
	waitFor(t, "reconnection after the panic", 30*time.Second, func() bool { return d.agentDoc().Connected })
}

// failingSave stops storing the identity once armed.
type failingSave struct {
	agent.IdentityStore
	armed atomic.Bool
}

func (f *failingSave) Save(ctx context.Context, id *agent.Identity) error {
	if f.armed.Load() {
		return errors.New("secrets \"continuum-agent-identity\" is forbidden: cannot update")
	}
	return f.IdentityStore.Save(ctx, id)
}

// ---- the RBAC self-check (rbac_check.go) ----

// selfSubjectAccessReviewReactor answers a SelfSubjectAccessReview the way a real cluster would for a ServiceAccount
// whose ClusterRoleBindings currently allow list on the given resources: it never errors (that call needs no
// permission of its own) and just says Allowed or not for the resource asked about.
func selfSubjectAccessReviewReactor(allow ...string) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		ca := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		want := ca.Spec.ResourceAttributes.Resource
		ca = ca.DeepCopy()
		for _, a := range allow {
			if a == want {
				ca.Status.Allowed = true
			}
		}
		return true, ca, nil
	}
}

func TestRBACSelfCheckFlagsAClusterThatStillGrantsMoreThanTheInstallDeclares(t *testing.T) {
	d := newDx(t, dxOpts{installed: 1, approve: 1, tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.RBACSelfCheck, c.RBACCheckEvery = true, 50*time.Millisecond
		// The cluster still grants tier 2 (pods), even though this install declares --tier 1: the leftover from a
		// `helm upgrade --set access.tier=1` that never actually ran against this cluster.
		c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", selfSubjectAccessReviewReactor("nodes", "pods"))
	}})
	g := d.waitDiagAny("rbac_wider_than_tier", func(g *server.DiagnosticsDoc) bool { return problem(g, agent.CodeRBACWiderThanTier) != nil })
	p := problem(g, agent.CodeRBACWiderThanTier)
	if p.Severity != "warn" || !strings.Contains(p.Message, "tier 2") || !strings.Contains(p.Message, "tier 1") {
		t.Fatalf("problem = %+v", p)
	}
}

func TestRBACSelfCheckIsQuietWhenTheGrantMatchesWhatIsDeclared(t *testing.T) {
	d := newDx(t, dxOpts{installed: 2, approve: 2, tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.RBACSelfCheck, c.RBACCheckEvery = true, 50*time.Millisecond
		c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", selfSubjectAccessReviewReactor("nodes", "pods"))
	}})
	// Give the check several rounds to have run, then confirm it never raised the problem: a tier-2 install being
	// granted tier 2 is exactly what should happen, not a drift to report.
	time.Sleep(300 * time.Millisecond)
	g := d.waitDiag("a full account", func(g *server.DiagnosticsDoc) bool { return len(g.Informers) > 0 })
	if p := problem(g, agent.CodeRBACWiderThanTier); p != nil {
		t.Fatalf("should be quiet when the grant matches the declared tier: %+v", p)
	}
}

func TestRBACSelfCheckIsOffByDefault(t *testing.T) {
	// A bare Config (what every other test in this package uses, and what RBACSelfCheck defaults to false in) never
	// runs the check, even if the cluster would otherwise have tripped it: only the CLI turns it on.
	d := newDx(t, dxOpts{installed: 1, approve: 1, tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", selfSubjectAccessReviewReactor("nodes", "pods"))
	}})
	time.Sleep(200 * time.Millisecond)
	g := d.waitDiag("a full account", func(g *server.DiagnosticsDoc) bool { return len(g.Informers) > 0 })
	if p := problem(g, agent.CodeRBACWiderThanTier); p != nil {
		t.Fatalf("the check should not run at all when RBACSelfCheck is false: %+v", p)
	}
}

// selfSubjectAccessReviewReactorNS is selfSubjectAccessReviewReactor, but "pods" is only allowed when the review
// asks about the given namespace specifically, never cluster-wide - exactly what a Role (never a ClusterRole)
// grants under rbac.mode=namespaced.
func selfSubjectAccessReviewReactorNS(clusterWide []string, podsNamespace string) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		ca := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		want := ca.Spec.ResourceAttributes.Resource
		ca = ca.DeepCopy()
		for _, a := range clusterWide {
			if a == want {
				ca.Status.Allowed = true
			}
		}
		if want == "pods" && ca.Spec.ResourceAttributes.Namespace == podsNamespace {
			ca.Status.Allowed = true
		}
		return true, ca, nil
	}
}

// Under rbac.mode=namespaced, tier 2 is never granted with a cluster-wide "list pods" (only a Role can grant it,
// scoped to one namespace), so the plain cluster-wide check that catches a leftover ClusterRoleBinding in cluster
// mode would never see a leftover per-namespace Role at all - it would ask the wrong question and always get "no".
// The self-check must ask about the declared namespaces instead when the cluster-wide ask comes back empty.
func TestRBACSelfCheckCatchesALeftoverPerNamespaceRoleUnderNamespacedRBAC(t *testing.T) {
	d := newDx(t, dxOpts{installed: 1, approve: 1, tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.RBACSelfCheck, c.RBACCheckEvery = true, 50*time.Millisecond
		c.RBACNamespaced = true
		c.Scope = &collect.Scope{Include: []string{"shop"}}
		// A `helm upgrade --set access.tier=1` narrowed the declared tier, but the tier-2 Role/RoleBinding in "shop"
		// from before was never removed (a partial upgrade, same class of bug as the ClusterRoleBinding case).
		c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", selfSubjectAccessReviewReactorNS([]string{"nodes"}, "shop"))
	}})
	g := d.waitDiagAny("rbac_wider_than_tier", func(g *server.DiagnosticsDoc) bool { return problem(g, agent.CodeRBACWiderThanTier) != nil })
	p := problem(g, agent.CodeRBACWiderThanTier)
	if p.Severity != "warn" || !strings.Contains(p.Message, "tier 2") || !strings.Contains(p.Message, "tier 1") {
		t.Fatalf("problem = %+v", p)
	}
}

// The mirror image: a namespaced install that matches its declared tier exactly must stay quiet, the same as
// cluster mode already does. Without the per-namespace check this would trivially never fire (nothing granted
// cluster-wide, ever, in this mode) - the real test is that this stays true even for the CORRECT case, once the
// self-check also asks about the declared namespaces.
func TestRBACSelfCheckUnderNamespacedRBACIsQuietWhenTheGrantMatchesWhatIsDeclared(t *testing.T) {
	d := newDx(t, dxOpts{installed: 2, approve: 2, tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		c.RBACSelfCheck, c.RBACCheckEvery = true, 50*time.Millisecond
		c.RBACNamespaced = true
		c.Scope = &collect.Scope{Include: []string{"shop"}}
		c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", selfSubjectAccessReviewReactorNS([]string{"nodes"}, "shop"))
	}})
	time.Sleep(300 * time.Millisecond)
	g := d.waitDiag("a full account", func(g *server.DiagnosticsDoc) bool { return len(g.Informers) > 0 })
	if p := problem(g, agent.CodeRBACWiderThanTier); p != nil {
		t.Fatalf("should be quiet when the grant matches the declared tier: %+v", p)
	}
	// The namespaced-mode note should be present instead, saying plainly that this install trades cluster-wide
	// coverage for a narrower blast radius.
	if p := problem(g, agent.CodeRBACNamespacedMode); p == nil || p.Severity != "info" {
		t.Fatalf("expected an info-level rbac_namespaced_mode note, got %+v", p)
	}
}

func TestAnIdentityThatCannotBeSavedIsReported(t *testing.T) {
	old := pki.AgentCertTTL
	pki.AgentCertTTL = 20 * time.Second
	t.Cleanup(func() { pki.AgentCertTTL = old })
	var fs *failingSave
	d := newDx(t, dxOpts{ids: func(s agent.IdentityStore) agent.IdentityStore { fs = &failingSave{IdentityStore: s}; return fs }})
	waitFor(t, "connected and synced", 20*time.Second, func() bool { a := d.agentDoc(); return a.Connected && a.Synced })
	fs.armed.Store(true) // the renewal, half way through the certificate's life, cannot be stored
	g := d.waitDiagAny("identity_secret_unwritable", func(g *server.DiagnosticsDoc) bool { return problem(g, "identity_secret_unwritable") != nil })
	if p := problem(g, "identity_secret_unwritable"); p.Severity != "error" || !strings.Contains(p.Message, "forbidden") {
		t.Fatalf("problem = %+v", p)
	}
}

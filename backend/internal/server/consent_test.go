package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/estate"
	"ikhnos/internal/installcfg"
	"ikhnos/internal/kernel"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---- tier changes ----

func agentDoc(t *testing.T, a *adminRig, cookie, id string) estate.AgentDoc {
	t.Helper()
	r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("state: %d %s", r.Code, r.Body.String())
	}
	var doc estate.StateDoc
	if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, d := range doc.Agents {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("agent %s not in the state document", id)
	return estate.AgentDoc{}
}

func TestTierChangeStaysWithinTheInstalledCeilingAndIsAudited(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, admin := a.user(t, "adam", accounts.RoleAdmin)
	_, viewer := a.user(t, "vic", accounts.RoleViewer)
	id, _, _ := a.approvedAgent(t, testkit.FP) // approved at 2, installed at 2

	tier := func(cookie string, n any) resp {
		return a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": n}, withCookie(cookie))
	}
	if c := tier(viewer, 1).Code; c != 403 {
		t.Fatalf("a viewer changed a tier: %d", c)
	}
	if got, _ := a.st.GetAgent(a.ctx, "org-1", id); got.AccessTier != 2 {
		t.Fatal("the refused request changed the tier")
	}

	// Narrowing takes effect at once and is audited old to new, with who did it.
	if r := tier(editor, 0); r.Code != 200 {
		t.Fatalf("narrow to 0: %d %s", r.Code, r.Body.String())
	}
	if got, _ := a.st.GetAgent(a.ctx, "org-1", id); got.AccessTier != 0 {
		t.Fatalf("tier is %d after narrowing", got.AccessTier)
	}
	// Widening back up to what the install allows takes an administrator, as approving did.
	if r := tier(editor, 2); r.Code != 403 {
		t.Fatalf("an editor widened a tier: %d %s", r.Code, r.Body.String())
	}
	if got, _ := a.st.GetAgent(a.ctx, "org-1", id); got.AccessTier != 0 {
		t.Fatalf("the refused widening changed the tier to %d", got.AccessTier)
	}
	if r := tier(admin, 2); r.Code != 200 {
		t.Fatalf("widen to the installed tier: %d %s", r.Code, r.Body.String())
	}
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	var narrowed, widened bool
	for _, e := range evs {
		if e.Action == "agent-tier-changed" && e.TargetID == id {
			narrowed = narrowed || e.Actor == "edith" && strings.Contains(e.Detail, "access tier 2 (services) to 0 (registered only)")
			widened = widened || e.Actor == "adam" && strings.Contains(e.Detail, "access tier 0 (registered only) to 2 (services)")
		}
	}
	if !narrowed || !widened {
		t.Fatalf("audit rows missing (narrowed %v, widened %v): %+v", narrowed, widened, evs)
	}
	before := kernel.Metrics.TierChanges.Load()
	tier(editor, 2) // unchanged: nothing to record
	if kernel.Metrics.TierChanges.Load() != before {
		t.Fatal("a no-op tier change was counted")
	}

	// Above the ceiling: refused, with the exact command for the cluster's owner.
	if err := a.st.SetInstalledTier(a.ctx, "org-1", id, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetAccessTier(a.ctx, "org-1", id, 1); err != nil {
		t.Fatal(err)
	}
	r := tier(admin, 2)
	if r.Code != 400 {
		t.Fatalf("over the ceiling: %d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	msg, _ := j["error"].(string)
	helm, _ := j["helm"].(string)
	want := "--reset-then-reuse-values --set access.tier=2"
	if !strings.Contains(msg, "helm upgrade ikhnos-agent") || !strings.Contains(msg, want) || !strings.Contains(helm, want) {
		t.Fatalf("the refusal does not carry the helm command: %v", j)
	}
	if got, _ := a.st.GetAgent(a.ctx, "org-1", id); got.AccessTier != 1 {
		t.Fatal("the refused request changed the tier")
	}
	// A tier past MaxTier and one merely past ImplementedTier (3, already covered by "over the ceiling" above via
	// InstalledTier=1) must read the same way: SetAgentTier used to have its own, differently-worded, differently
	// numbered ceiling for anything above MaxTier, which drifted from the ImplementedTier message right below it.
	r9 := tier(admin, 9)
	if r9.Code != 400 {
		t.Fatalf("tier 9: %d %s", r9.Code, r9.Body.String())
	}
	if msg, _ := r9.json(t)["error"].(string); !strings.Contains(msg, "access tier 9 is not available in this release (the highest is 2)") {
		t.Fatalf("tier 9 should cite the release ceiling like any other too-high tier does, got %q", msg)
	}
	if c := tier(editor, -1).Code; c != 400 {
		t.Fatalf("tier -1: %d", c)
	}
	if c := tier(editor, "x").Code; c != 400 {
		t.Fatalf("tier x: %d", c)
	}
	// A pending agent has no access to change.
	if c := a.do("POST", "/api/v1/agents/ag-nope/tier", map[string]any{"tier": 1}, withCookie(editor)).Code; c != 404 {
		t.Fatalf("unknown agent: %d", c)
	}
}

func TestNarrowingATierDropsWhatTheServerHoldsAboveIt(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	id, _, _ := a.approvedAgent(t, testkit.FP)
	v := liveView(*a.now, "n1")
	v.State.Apply(&ikhnosv1.Sync{Namespaces: []*ikhnosv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}},
		Workloads: []*ikhnosv1.WorkloadFacts{{Key: "shop/Deployment/web", Name: "web"}}})
	a.hub().Mu.Lock()
	a.hub().Views[id] = v
	a.hub().Mu.Unlock()
	if r := a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": 1}, withCookie(editor)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	a.hub().Mu.Lock()
	nw, nn, nodes := len(v.State.Workloads), len(v.State.Namespaces), len(v.State.Nodes)
	a.hub().Mu.Unlock()
	if nw != 0 || nn != 0 || nodes != 1 {
		t.Fatalf("after narrowing to 1 the server holds %d workloads, %d namespaces and %d nodes", nw, nn, nodes)
	}
}

// Narrowing the overrides drops what the server already holds, the same as narrowing a tier does: a stuck,
// disconnected or out-of-date agent would otherwise leave the old picture here until it next sent a full one.
func TestNarrowingConsentDropsWhatTheServerAlreadyHolds(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	id, _, _ := a.approvedAgent(t, testkit.FP)
	v := liveView(*a.now, "n1")
	v.State.Nodes["n1"].MachineId, v.State.Nodes["n1"].KernelVersion = "mid-1", "6.1"
	v.State.Apply(&ikhnosv1.Sync{
		Namespaces: []*ikhnosv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}},
		Workloads:  []*ikhnosv1.WorkloadFacts{{Key: "shop/Deployment/web", Name: "web", Namespace: "shop"}, {Key: "batch/Job/etl", Name: "etl", Namespace: "batch"}},
	})
	a.hub().Mu.Lock()
	a.hub().Views[id] = v
	a.hub().Mu.Unlock()
	held := func() (ns, wl []string, machineID string) {
		a.hub().Mu.Lock()
		defer a.hub().Mu.Unlock()
		for _, n := range v.State.Namespaces {
			ns = append(ns, n.Name)
		}
		for _, w := range v.State.Workloads {
			wl = append(wl, w.Name)
		}
		return ns, wl, v.State.Nodes["n1"].MachineId
	}
	save := func(body map[string]any) {
		t.Helper()
		if r := a.do("POST", "/api/v1/agents/"+id+"/consent", body, withCookie(editor)); r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	}

	save(map[string]any{"excludedNamespaces": []string{"shop"}, "pausedCollectors": []string{"probes"}})
	ns, wl, mid := held()
	if strings.Join(ns, ",") != "batch" || strings.Join(wl, ",") != "etl" {
		t.Fatalf("after excluding shop the server still holds namespaces %v and workloads %v", ns, wl)
	}
	if mid != "" {
		t.Fatalf("after pausing the node probe the server still holds the machine id %q", mid)
	}

	// Widening again takes an administrator. It has nothing to drop, and does not bring back what was dropped: only the agent can say it again.
	if r := a.do("POST", "/api/v1/agents/"+id+"/consent", map[string]any{}, withCookie(editor)); r.Code != 403 {
		t.Fatalf("an editor lifted a pause and an exclusion: %d %s", r.Code, r.Body.String())
	}
	_, admin := a.user(t, "adam", accounts.RoleAdmin)
	if r := a.do("POST", "/api/v1/agents/"+id+"/consent", map[string]any{}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if ns, _, _ := held(); strings.Join(ns, ",") != "batch" {
		t.Fatalf("widening changed what is held: %v", ns)
	}
}

// ---- consent overrides ----

func TestConsentOverridesAreValidatedPersistedAndPushed(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, viewer := a.user(t, "vic", accounts.RoleViewer)
	id, _, _ := a.approvedAgent(t, testkit.FP)
	put := func(cookie string, body any) resp {
		return a.do("POST", "/api/v1/agents/"+id+"/consent", body, withCookie(cookie))
	}
	if c := put(viewer, map[string]any{"pausedCollectors": []string{"flow"}}).Code; c != 403 {
		t.Fatalf("viewer: %d", c)
	}
	for name, body := range map[string]map[string]any{
		"unknown collector":  {"pausedCollectors": []string{"cameras"}},
		"bad namespace":      {"excludedNamespaces": []string{"Not_Valid"}},
		"system namespace":   {"excludedNamespaces": []string{"kube-system"}},
		"namespace too long": {"excludedNamespaces": []string{strings.Repeat("a", 64)}},
	} {
		if c := put(editor, body).Code; c != 400 {
			t.Errorf("%s: %d", name, c)
		}
	}
	r := put(editor, map[string]any{"pausedCollectors": []string{"flow", "probes", "flow"}, "excludedNamespaces": []string{"shop", "batch", "shop"}})
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var got agents.ConsentDoc
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || strings.Join(got.PausedCollectors, ",") != "flow,probes" || strings.Join(got.ExcludedNamespaces, ",") != "batch,shop" {
		t.Fatalf("stored %+v %v", got, err)
	}
	// The save response is the same projection the agent list carries (see AgentDoc.Consent), not a bare
	// echo of what was asked for: right after a save nothing has had time to apply yet, so it reads as
	// unconfirmed with a fresh SetAt, not the zero value a plain echo would give.
	if got.Confirmed || got.SetAt == "" {
		t.Fatalf("a fresh narrowing should come back unconfirmed with a SetAt, not a bare echo: %+v", got)
	}
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	var audited bool
	for _, e := range evs {
		audited = audited || (e.Action == "agent-consent-changed" && e.Actor == "edith" && strings.Contains(e.Detail, "flow, probes"))
	}
	if !audited {
		t.Fatalf("no audit row: %+v", evs)
	}

	// The Config the agent receives carries them, and a change of them is a different Config.
	ag, _ := a.st.GetAgent(a.ctx, "org-1", id)
	v := liveView(*a.now, "n1")
	a.hub().Mu.Lock()
	a.hub().Views[id] = v
	a.hub().Mu.Unlock()
	cfg := a.hub().ConfigFor(ag, v)
	if strings.Join(cfg.PausedCollectors, ",") != "flow,probes" || strings.Join(cfg.ExcludedNamespaces, ",") != "batch,shop" {
		t.Fatalf("config = %v", cfg)
	}
	h1 := v.CfgHash
	a.hub().ConfigFor(ag, v)
	if v.CfgHash != h1 {
		t.Fatal("the same overrides gave a different config hash")
	}
	if _, err := a.hub().SetConsent(testkit.AsRole(accounts.RoleAdmin), "edith", id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	a.hub().ConfigFor(ag, v)
	if v.CfgHash == h1 {
		t.Fatal("changed overrides did not change the config hash")
	}
	if _, err := a.hub().SetConsent(testkit.AsRole(accounts.RoleAdmin), "edith", id, agents.Consent{Paused: []string{"measure"}}); err != nil {
		t.Fatal(err)
	}
	if cfg := a.hub().ConfigFor(ag, v); len(cfg.ProbeTargets) != 0 || cfg.MeasureSeconds != 0 {
		t.Fatalf("a paused measure collector must be issued no targets: %+v", cfg)
	}

	// It survives a restart of the server (a new hub on the same store), before the agent has even reconnected.
	p2 := NewPlatform(a.base, nil)
	tn, err := p2.Tenant(a.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := tn.Hub.State(a.ctx)
	var found bool
	for _, d := range doc.Agents {
		if d.ID == id {
			found = d.Consent != nil && strings.Join(d.Consent.PausedCollectors, ",") == "measure"
		}
	}
	if !found {
		t.Fatalf("overrides lost across a restart: %+v", doc.Agents)
	}

	// Only people who may change an agent's access see diagnostics and overrides.
	if d := agentDoc(t, a, viewer, id); d.Consent != nil || d.Diagnostics != nil {
		t.Fatalf("a viewer sees %+v %+v", d.Consent, d.Diagnostics)
	}
	if d := agentDoc(t, a, editor, id); d.Consent == nil {
		t.Fatal("an editor does not see the overrides")
	}
}

// ---- hello, diagnostics, ceiling ----

func (r *hubRig) connect(t *testing.T, ctx context.Context, key any, leaf []byte, hello *ikhnosv1.Hello) ikhnosv1.AgentService_ConnectClient {
	t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	s, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- chunked full sync ----

// ---- health, readiness, metrics ----

func TestHealthzReadyzAndMetrics(t *testing.T) {
	a := newAdminRig(t)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	if r := get("/healthz"); r.Code != 200 || r.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", r.Code, r.Body.String())
	}
	if r := get("/readyz"); r.Code != 200 || !strings.HasPrefix(r.Body.String(), "ready") {
		t.Fatalf("readyz: %d %q", r.Code, r.Body.String())
	}
	listening, graphUp := true, false
	a.a.Readiness = &Readiness{AgentsListening: func() bool { return listening }, Graph: func() (bool, bool) { return true, graphUp }}
	// Neo4j configured but down: still ready, and says degraded.
	r := get("/readyz")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "degraded") {
		t.Fatalf("degraded: %d %q", r.Code, r.Body.String())
	}
	// The answer is cached for a moment, so a probe every second costs the database one read per interval.
	a.a.Readiness.mu.Lock()
	a.a.Readiness.at = time.Time{}
	a.a.Readiness.mu.Unlock()
	listening = false
	if r := get("/readyz"); r.Code != 503 || !strings.Contains(r.Body.String(), "listener") {
		t.Fatalf("listener down: %d %q", r.Code, r.Body.String())
	}
	a.a.Readiness = &Readiness{}
	a.st.Close()
	if r := get("/readyz"); r.Code != 503 || !strings.Contains(r.Body.String(), "database") {
		t.Fatalf("store down: %d %q", r.Code, r.Body.String())
	}
}

func TestMetricsTextAndToken(t *testing.T) {
	a := newAdminRig(t)
	a.approvedAgent(t, testkit.FP)
	kernel.Metrics.RateLimited.Add(0)
	h := MetricsHandler(a.a.P, "v-test", "s3cret")
	get := func(auth string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	if r := get(""); r.Code != 401 {
		t.Fatalf("no token: %d", r.Code)
	}
	if r := get("Bearer nope"); r.Code != 401 {
		t.Fatalf("wrong token: %d", r.Code)
	}
	r := get("Bearer s3cret")
	body := r.Body.String()
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, body)
	}
	for _, want := range []string{`ikhnos_agents{status="approved"} 1`, `ikhnos_agents{status="pending"} 0`, "ikhnos_agents_connected 0", "ikhnos_syncs_applied_total", "ikhnos_syncs_refused_total",
		"ikhnos_auth_failures_total", "ikhnos_rate_limited_total", "ikhnos_stream_panics_total", "ikhnos_store_errors_total", `ikhnos_build_info{version="v-test"`, "# TYPE ikhnos_agents gauge"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(body, "127.0.0.1") || strings.Contains(body, "edge") {
		t.Error("metrics carry names or addresses")
	}
}

// ---- panics and shutdown ----

type panickyAgents struct {
	ikhnosv1.UnimplementedAgentServiceServer
}

func (panickyAgents) Connect(ikhnosv1.AgentService_ConnectServer) error { panic("boom") }

func TestAPanicInAnAgentStreamIsRecoveredAndTheServerKeepsServing(t *testing.T) {
	e := newEnv(t)
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.base.CA, []string{"127.0.0.1"}), panickyAgents{})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()
	r := &rig{env: e, addr: l.Addr().String()}
	id, key, leaf := e.approvedAgent(t, testkit.FP)
	_ = id
	before := kernel.Metrics.StreamPanics.Load()
	for i := 0; i < 2; i++ { // the second connection proves the process is still serving
		cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
		c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(e.base.CA.Pin(), "127.0.0.1", cert)))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s, err := c.Connect(ctx)
		if err == nil {
			_, err = s.Recv()
		}
		cancel()
		if status.Code(err) != codes.Internal {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	if kernel.Metrics.StreamPanics.Load() != before+2 {
		t.Fatalf("panics counted: %d", kernel.Metrics.StreamPanics.Load()-before)
	}
}

func TestRecoverPanicHelpers(t *testing.T) {
	e := newEnv(t)
	before := kernel.Metrics.StreamPanics.Load()
	err := func() (err error) {
		defer e.base.Agents.RecoverPanic("test", &err)
		panic("x")
	}()
	if status.Code(err) != codes.Internal || strings.Contains(err.Error(), "x") {
		t.Fatalf("err = %v (the panic text must not reach the caller)", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); e.base.Agents.Safely("t", func() { panic("y") }) }()
	<-done
	if kernel.Metrics.StreamPanics.Load() != before+2 {
		t.Fatal("panics not counted")
	}
}

func TestStopWithinDoesNotWaitForeverForALongLivedStream(t *testing.T) {
	e := newEnv(t)
	p := NewPlatform(e.base, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tn, err := p.Tenant(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	e.core = tn.C
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	id, key, leaf := e.approvedAgent(t, testkit.FP)
	rr := &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: tn.Hub}
	s := rr.agentStream(t, ctx, id, key, leaf)
	start := time.Now()
	srv.StopWithin(700 * time.Millisecond)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("StopWithin took %v", d)
	}
	if _, err := s.Recv(); err == nil {
		t.Fatal("the stream is still open after the server stopped")
	}
}

// ---- an agent's own namespace, and the commands that need it ----

func TestHelloNamespaceIsRecordedAndUsedInGeneratedCommands(t *testing.T) {
	// One platform, so the gRPC hub the agent connects to is the same Hub instance the HTTP admin API reads.
	e := newEnv(t)
	p := NewPlatform(e.base, nil)
	tn, err := p.Tenant(e.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	e.core = tn.C
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	rr := &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: tn.Hub}
	id, key, leaf := rr.approvedAgent(t, testkit.FP) // approved at 2, installed at 2

	admin := &Admin{P: p, C: e.base, Settings: installcfg.Settings{AgentAddr: "x:1", ChartRef: "chart"}}
	a := &adminRig{env: e, h: admin.Handler(), a: admin}
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, adm := a.user(t, "adam", accounts.RoleAdmin)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hello := &ikhnosv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 2, Namespace: "observability", ReleaseName: "obs-agent"}
	rr.connect(t, ctx, key, leaf, hello)
	doc, _ := tn.Hub.State(ctx)
	if doc.Agents[0].Namespace != "observability" || doc.Agents[0].ReleaseName != "obs-agent" {
		t.Fatalf("namespace/release not recorded: %+v", doc.Agents[0])
	}
	if ns := tn.Hub.NamespaceOf(id); ns != "observability" {
		t.Fatalf("NamespaceOf: %q", ns)
	}
	if rel := tn.Hub.ReleaseNameOf(id); rel != "obs-agent" {
		t.Fatalf("ReleaseNameOf: %q", rel)
	}

	// The harden command that comes back with a narrowing names the real namespace and release, not a guess.
	resp := a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": 0}, withCookie(editor))
	if resp.Code != 200 {
		t.Fatalf("narrow: %d %s", resp.Code, resp.Body.String())
	}
	j := resp.json(t)
	helm, _ := j["hardenHelm"].(string)
	if !strings.Contains(helm, "helm upgrade obs-agent ") || !strings.Contains(helm, "--namespace observability") || !strings.Contains(helm, "access.tier=0") {
		t.Fatalf("hardenHelm missing or wrong: %v", j)
	}

	// The same command, persisted, shows up on the next poll while the gap between approved and installed remains.
	polled := agentDoc(t, a, editor, id)
	if !strings.Contains(polled.HardenHelm, "helm upgrade obs-agent ") || !strings.Contains(polled.HardenHelm, "--namespace observability") || !strings.Contains(polled.HardenHelm, "access.tier=0") {
		t.Fatalf("hardenHelm missing from the state document: %+v", polled)
	}

	// Widening back to the ceiling closes the gap: nothing left to harden.
	if resp := a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": 2}, withCookie(adm)); resp.Code != 200 {
		t.Fatalf("widen: %d %s", resp.Code, resp.Body.String())
	}
	if polled = agentDoc(t, a, editor, id); polled.HardenHelm != "" {
		t.Fatalf("hardenHelm should be empty once approved matches installed: %+v", polled)
	}
}

func TestRevokedAgentCarriesTeardownCommandsOnlyForEditorsAndAbove(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, viewer := a.user(t, "vic", accounts.RoleViewer)
	id, _, _ := a.approvedAgent(t, testkit.FP)
	if err := a.core.Agents.Revoke(a.ctx, "actor", id, "for the test"); err != nil {
		t.Fatal(err)
	}

	d := agentDoc(t, a, editor, id)
	if d.Teardown == nil || !strings.Contains(d.Teardown.Helm, "helm uninstall ikhnos-agent") || !strings.Contains(d.Teardown.Secret, "ikhnos-agent-identity") {
		t.Fatalf("teardown missing for an editor: %+v", d)
	}
	if !d.Teardown.NamespaceGuessed {
		t.Fatal("this agent never sent a namespace, so the command should say it is a guess")
	}

	dv := agentDoc(t, a, viewer, id)
	if dv.Teardown != nil {
		t.Fatalf("teardown shown to a viewer: %+v", dv.Teardown)
	}
}

// ---- the server-side backstop for paused collectors and excluded namespaces (dropConsentOverrides, noteFlows, NoteMeasurements) ----

// ---- the observability-intent panel's staleness signal (ConsentDoc.Confirmed / SetAt) ----

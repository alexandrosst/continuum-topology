package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/pki"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- tier changes ----

func agentDoc(t *testing.T, a *adminRig, cookie, id string) AgentDoc {
	t.Helper()
	r := a.do("GET", "/api/v1/state", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("state: %d %s", r.Code, r.Body.String())
	}
	var doc StateDoc
	if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, d := range doc.Agents {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("agent %s not in the state document", id)
	return AgentDoc{}
}

func TestTierChangeStaysWithinTheInstalledCeilingAndIsAudited(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", RoleEditor)
	_, viewer := a.user(t, "vic", RoleViewer)
	id, _, _ := a.approvedAgent(t, fp) // approved at 2, installed at 2

	tier := func(cookie string, n any) resp {
		return a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": n}, withCookie(cookie))
	}
	if c := tier(viewer, 1).Code; c != 403 {
		t.Fatalf("a viewer changed a tier: %d", c)
	}
	if got, _ := a.st.GetAgent(a.ctx, id); got.AccessTier != 2 {
		t.Fatal("the refused request changed the tier")
	}

	// Narrowing takes effect at once and is audited old to new, with who did it.
	if r := tier(editor, 0); r.Code != 200 {
		t.Fatalf("narrow to 0: %d %s", r.Code, r.Body.String())
	}
	if got, _ := a.st.GetAgent(a.ctx, id); got.AccessTier != 0 {
		t.Fatalf("tier is %d after narrowing", got.AccessTier)
	}
	// Widening back up to what the install allows is allowed.
	if r := tier(editor, 2); r.Code != 200 {
		t.Fatalf("widen to the installed tier: %d %s", r.Code, r.Body.String())
	}
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	var narrowed, widened bool
	for _, e := range evs {
		if e.Action == "agent-tier-changed" && e.Actor == "edith" && e.TargetID == id {
			narrowed = narrowed || strings.Contains(e.Detail, "access tier 2 (services) to 0 (registered only)")
			widened = widened || strings.Contains(e.Detail, "access tier 0 (registered only) to 2 (services)")
		}
	}
	if !narrowed || !widened {
		t.Fatalf("audit rows missing (narrowed %v, widened %v): %+v", narrowed, widened, evs)
	}
	before := Metrics.tierChanges.Load()
	tier(editor, 2) // unchanged: nothing to record
	if Metrics.tierChanges.Load() != before {
		t.Fatal("a no-op tier change was counted")
	}

	// Above the ceiling: refused, with the exact command for the cluster's owner.
	if err := a.st.SetInstalledTier(a.ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetAccessTier(a.ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	r := tier(editor, 2)
	if r.Code != 400 {
		t.Fatalf("over the ceiling: %d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	msg, _ := j["error"].(string)
	helm, _ := j["helm"].(string)
	want := "--reuse-values --set access.tier=2"
	if !strings.Contains(msg, "helm upgrade continuum-agent") || !strings.Contains(msg, want) || !strings.Contains(helm, want) {
		t.Fatalf("the refusal does not carry the helm command: %v", j)
	}
	if got, _ := a.st.GetAgent(a.ctx, id); got.AccessTier != 1 {
		t.Fatal("the refused request changed the tier")
	}
	// A tier past MaxTier and one merely past ImplementedTier (3, already covered by "over the ceiling" above via
	// InstalledTier=1) must read the same way: SetAgentTier used to have its own, differently-worded, differently
	// numbered ceiling for anything above MaxTier, which drifted from the ImplementedTier message right below it.
	r9 := tier(editor, 9)
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
	_, editor := a.user(t, "edith", RoleEditor)
	id, _, _ := a.approvedAgent(t, fp)
	v := liveView(*a.now, "n1")
	v.state.Apply(&continuumv1.Sync{Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}},
		Workloads: []*continuumv1.WorkloadFacts{{Key: "shop/Deployment/web", Name: "web"}}})
	a.hub().mu.Lock()
	a.hub().views[id] = v
	a.hub().mu.Unlock()
	if r := a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": 1}, withCookie(editor)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	a.hub().mu.Lock()
	nw, nn, nodes := len(v.state.Workloads), len(v.state.Namespaces), len(v.state.Nodes)
	a.hub().mu.Unlock()
	if nw != 0 || nn != 0 || nodes != 1 {
		t.Fatalf("after narrowing to 1 the server holds %d workloads, %d namespaces and %d nodes", nw, nn, nodes)
	}
}

// Narrowing the overrides drops what the server already holds, the same as narrowing a tier does: a stuck,
// disconnected or out-of-date agent would otherwise leave the old picture here until it next sent a full one.
func TestNarrowingConsentDropsWhatTheServerAlreadyHolds(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", RoleEditor)
	id, _, _ := a.approvedAgent(t, fp)
	v := liveView(*a.now, "n1")
	v.state.Nodes["n1"].MachineId, v.state.Nodes["n1"].KernelVersion = "mid-1", "6.1"
	v.state.Apply(&continuumv1.Sync{
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}},
		Workloads:  []*continuumv1.WorkloadFacts{{Key: "shop/Deployment/web", Name: "web", Namespace: "shop"}, {Key: "batch/Job/etl", Name: "etl", Namespace: "batch"}},
	})
	a.hub().mu.Lock()
	a.hub().views[id] = v
	a.hub().mu.Unlock()
	held := func() (ns, wl []string, machineID string) {
		a.hub().mu.Lock()
		defer a.hub().mu.Unlock()
		for _, n := range v.state.Namespaces {
			ns = append(ns, n.Name)
		}
		for _, w := range v.state.Workloads {
			wl = append(wl, w.Name)
		}
		return ns, wl, v.state.Nodes["n1"].MachineId
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

	// Widening again has nothing to drop, and does not bring back what was dropped: only the agent can say it again.
	save(map[string]any{})
	if ns, _, _ := held(); strings.Join(ns, ",") != "batch" {
		t.Fatalf("widening changed what is held: %v", ns)
	}
}

func TestConsentNarrowsOnlyWhenSomethingIsTakenAway(t *testing.T) {
	for name, c := range map[string]struct {
		cur, next Consent
		want      bool
	}{
		"nothing to something paused": {Consent{}, Consent{Paused: []string{"flow"}}, true},
		"something excluded":          {Consent{}, Consent{Excluded: []string{"shop"}}, true},
		"swapping one for another":    {Consent{Excluded: []string{"shop"}}, Consent{Excluded: []string{"batch"}}, true},
		"adding to what was there":    {Consent{Paused: []string{"flow"}}, Consent{Paused: []string{"flow", "probes"}}, true},
		"unchanged":                   {Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, false},
		"only widening":               {Consent{Paused: []string{"flow", "probes"}, Excluded: []string{"shop", "batch"}}, Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, false},
		"clearing everything":         {Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, Consent{}, false},
	} {
		if got := consentNarrows(c.cur, c.next); got != c.want {
			t.Errorf("%s: consentNarrows = %v, want %v", name, got, c.want)
		}
	}
}

// ---- consent overrides ----

func TestConsentOverridesAreValidatedPersistedAndPushed(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", RoleEditor)
	_, viewer := a.user(t, "vic", RoleViewer)
	id, _, _ := a.approvedAgent(t, fp)
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
	var got ConsentDoc
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
	ag, _ := a.st.GetAgent(a.ctx, id)
	v := liveView(*a.now, "n1")
	a.hub().mu.Lock()
	a.hub().views[id] = v
	a.hub().mu.Unlock()
	cfg := a.hub().configFor(ag, v)
	if strings.Join(cfg.PausedCollectors, ",") != "flow,probes" || strings.Join(cfg.ExcludedNamespaces, ",") != "batch,shop" {
		t.Fatalf("config = %v", cfg)
	}
	h1 := v.cfgHash
	a.hub().configFor(ag, v)
	if v.cfgHash != h1 {
		t.Fatal("the same overrides gave a different config hash")
	}
	if _, err := a.hub().SetConsent(a.ctx, "edith", id, Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	a.hub().configFor(ag, v)
	if v.cfgHash == h1 {
		t.Fatal("changed overrides did not change the config hash")
	}
	if _, err := a.hub().SetConsent(a.ctx, "edith", id, Consent{Paused: []string{"measure"}}); err != nil {
		t.Fatal(err)
	}
	if cfg := a.hub().configFor(ag, v); len(cfg.ProbeTargets) != 0 || cfg.MeasureSeconds != 0 {
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

func (r *hubRig) connect(t *testing.T, ctx context.Context, key any, leaf []byte, hello *continuumv1.Hello) continuumv1.AgentService_ConnectClient {
	t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	s, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHelloAdvertisesTheCeilingAndDiagnosticsFollowHeartbeats(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp) // approved at 2, installed 2

	// The owner has since run `helm upgrade --set access.tier=1`: the hello says so, the server records it and the approval follows it down.
	hello := &continuumv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 1, Diagnostics: &continuumv1.Diagnostics{AgentVersion: "9.9", Arch: "arm64", InstalledTier: 1, EffectiveTier: 1, ApprovedTier: 2}}
	s := r.connect(t, ctx, key, leaf, hello)
	a, _ := r.st.GetAgent(ctx, id)
	if a.InstalledTier != 1 || a.AccessTier != 1 {
		t.Fatalf("installed %d, approved %d after a hello that says 1", a.InstalledTier, a.AccessTier)
	}
	evs, _ := r.st.ListAudit(ctx, "org-1", 50)
	var ceiling, lowered bool
	for _, e := range evs {
		ceiling = ceiling || (e.Action == "agent-ceiling-changed" && e.Actor == "agent:"+id)
		lowered = lowered || (e.Action == "agent-tier-changed" && strings.Contains(e.Detail, "now allows at most 1"))
	}
	if !ceiling || !lowered {
		t.Fatalf("audit: ceiling %v, lowered %v: %+v", ceiling, lowered, evs)
	}
	doc, _ := r.hub.State(ctx)
	d := doc.Agents[0].Diagnostics
	if d == nil || !d.Partial || d.Arch != "arm64" || d.InstalledTier != 1 {
		t.Fatalf("diagnostics from the hello: %+v", d)
	}

	// A heartbeat carries a fuller account, with typed problems.
	beat := &continuumv1.Diagnostics{AgentVersion: "9.9", InstalledTier: 1, EffectiveTier: 1, ApprovedTier: 1, UptimeSeconds: 42,
		Collectors: []*continuumv1.CollectorDiag{{Name: "flow", Configured: true, Enabled: true, Producing: true, Reporting: 2, Expected: 3, LastData: timestamppb.Now()}},
		Informers:  []*continuumv1.InformerDiag{{Name: "nodes", Synced: true, Objects: 3}},
		Problems:   []*continuumv1.Problem{{Code: "rbac_forbidden", Severity: continuumv1.Problem_ERROR, Message: strings.Repeat("x", 5000), Since: timestamppb.Now()}}}
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{Diagnostics: beat}}}); err != nil {
		t.Fatal(err)
	}
	var got *DiagnosticsDoc
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		doc, _ = r.hub.State(ctx)
		if got = doc.Agents[0].Diagnostics; got != nil && !got.Partial {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil || got.Partial || got.UptimeSeconds != 42 || len(got.Problems) != 1 || got.Problems[0].Severity != "error" || len(got.Problems[0].Message) > 720 {
		t.Fatalf("diagnostics from the heartbeat: %+v", got)
	}
	if len(got.Collectors) != 1 || got.Collectors[0].Reporting != 2 || got.Collectors[0].Expected != 3 || got.Collectors[0].LastData == "" {
		t.Fatalf("collectors: %+v", got.Collectors)
	}
}

func TestAnOversizedDiagnosticsAccountIsIgnoredNotRefused(t *testing.T) {
	v := newView()
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1"}, false, time.Now())
	huge := &continuumv1.Diagnostics{AgentVersion: "2"}
	for i := 0; i < 5000; i++ {
		huge.Problems = append(huge.Problems, &continuumv1.Problem{Code: "x", Message: strings.Repeat("m", 100)})
	}
	noteDiagnostics(v, huge, false, time.Now())
	if v.ext.diag.AgentVersion != "1" {
		t.Fatal("an oversized account replaced the previous one")
	}
	many := &continuumv1.Diagnostics{AgentVersion: "3"}
	for i := 0; i < 100; i++ {
		many.Problems = append(many.Problems, &continuumv1.Problem{Code: "x"})
		many.Informers = append(many.Informers, &continuumv1.InformerDiag{Name: "i"})
	}
	noteDiagnostics(v, many, false, time.Now())
	if len(v.ext.diag.Problems) != maxDiagProblems || len(v.ext.diag.Informers) != maxDiagInformers {
		t.Fatalf("lists were not cut: %d %d", len(v.ext.diag.Problems), len(v.ext.diag.Informers))
	}
}

func TestInstalledTelemetrySignalsRoundTripAndTruncate(t *testing.T) {
	v := newView()
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1", InstalledTelemetrySignals: []string{"traces", "energy"}}, false, time.Now())
	doc := v.diagDoc()
	if got := strings.Join(doc.InstalledTelemetry, ","); got != "traces,energy" {
		t.Fatalf("InstalledTelemetry = %v, want [traces energy] (order preserved, the agent already sorts it)", doc.InstalledTelemetry)
	}

	// A longer-than-bound list is truncated, not refused outright - same treatment as PausedCollectors/Problems.
	v2 := newView()
	long := &continuumv1.Diagnostics{AgentVersion: "1"}
	for i := 0; i < maxDiagTelemetrySignals+5; i++ {
		long.InstalledTelemetrySignals = append(long.InstalledTelemetrySignals, fmt.Sprintf("signal%d", i))
	}
	noteDiagnostics(v2, long, false, time.Now())
	if len(v2.ext.diag.InstalledTelemetrySignals) != maxDiagTelemetrySignals {
		t.Fatalf("InstalledTelemetrySignals not truncated: %d entries", len(v2.ext.diag.InstalledTelemetrySignals))
	}
}

func TestInstalledTelemetryConfigRoundTrips(t *testing.T) {
	v := newView()
	tracesPct := uint32(20)
	energySource := "bundle-kepler"
	noteDiagnostics(v, &continuumv1.Diagnostics{
		AgentVersion:              "1",
		InstalledTelemetrySignals: []string{"traces", "energy"},
		InstalledTelemetryConfig: &continuumv1.InstalledTelemetryConfig{
			ExportEndpoint:           "otel-collector.example:4317",
			RedactionEnabled:         true,
			ResourceDetectionEnabled: false,
			TracesSamplingPercentage: &tracesPct,
			EnergySource:             &energySource,
		},
	}, false, time.Now())
	doc := v.diagDoc()
	cfg := doc.InstalledTelemetryConfig
	if cfg == nil {
		t.Fatal("InstalledTelemetryConfig is nil, want it carried over from the agent's diagnostics")
	}
	if cfg.ExportEndpoint != "otel-collector.example:4317" {
		t.Errorf("ExportEndpoint = %q, want the reported destination", cfg.ExportEndpoint)
	}
	if !cfg.RedactionEnabled || cfg.ResourceDetectionEnabled {
		t.Errorf("RedactionEnabled/ResourceDetectionEnabled = %v/%v, want true/false", cfg.RedactionEnabled, cfg.ResourceDetectionEnabled)
	}
	if cfg.TracesSamplingPercent == nil || *cfg.TracesSamplingPercent != 20 {
		t.Errorf("TracesSamplingPercent = %v, want 20", cfg.TracesSamplingPercent)
	}
	if cfg.EnergySource != "bundle-kepler" {
		t.Errorf("EnergySource = %q, want \"bundle-kepler\"", cfg.EnergySource)
	}
	if cfg.AcceleratorsSource != "" {
		t.Errorf("AcceleratorsSource = %q, want empty (accelerators not reported as enabled)", cfg.AcceleratorsSource)
	}

	// No telemetry config reported at all: the doc field stays nil, not present-with-zero-values.
	v2 := newView()
	noteDiagnostics(v2, &continuumv1.Diagnostics{AgentVersion: "1"}, false, time.Now())
	if doc2 := v2.diagDoc(); doc2.InstalledTelemetryConfig != nil {
		t.Fatalf("InstalledTelemetryConfig = %v, want nil when the agent reported none", doc2.InstalledTelemetryConfig)
	}
}

func TestExportHealthRelaysTheAgentsStatesAndBoundsThem(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	v := newView()
	h := &continuumv1.ExportHealth{
		ScrapedAt: timestamppb.New(at), PodsReached: 3, PodsFailed: 1, LastError: "read 10.0.0.2:8888: connection reset",
		Routes: []*continuumv1.ExportRouteHealth{
			{Exporter: "otlphttp/logs", Signal: "logs", State: continuumv1.ExportRouteHealth_FAILING, Sent: 5, Failed: 9, LastSentAt: timestamppb.New(at.Add(-time.Hour)), LastFailedAt: timestamppb.New(at)},
			{Exporter: "zipkin/traces", Signal: "traces", State: continuumv1.ExportRouteHealth_State(99)},
		},
	}
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1", ExportHealth: h}, false, at)
	got := v.diagDoc().ExportHealth
	if got == nil || got.PodsReached != 3 || got.PodsFailed != 1 || got.ScrapedAt != "2026-10-06T12:00:00Z" || got.LastError == "" || len(got.Routes) != 2 {
		t.Fatalf("ExportHealth = %+v", got)
	}
	r := got.Routes[0]
	if r.Exporter != "otlphttp/logs" || r.Signal != "logs" || r.State != "failing" || r.Sent != 5 || r.Failed != 9 || r.LastSentAt != "2026-10-06T11:00:00Z" || r.LastFailedAt != "2026-10-06T12:00:00Z" {
		t.Errorf("route = %+v", r)
	}
	// A state this server does not know claims nothing, rather than something the agent never said.
	if got.Routes[1].State != "waiting" || got.Routes[1].LastSentAt != "" {
		t.Errorf("unknown state route = %+v, want waiting", got.Routes[1])
	}

	// Nothing reported (health off, or an older agent): no field at all, not an empty one.
	v2 := newView()
	noteDiagnostics(v2, &continuumv1.Diagnostics{AgentVersion: "1"}, false, at)
	if v2.diagDoc().ExportHealth != nil {
		t.Error("ExportHealth must be nil when the agent reported none")
	}

	// An agent cannot make the server hold more routes than it should.
	many := &continuumv1.ExportHealth{}
	for i := 0; i < maxDiagExportRoutes+10; i++ {
		many.Routes = append(many.Routes, &continuumv1.ExportRouteHealth{Exporter: "e", Signal: "logs"})
	}
	v3 := newView()
	noteDiagnostics(v3, &continuumv1.Diagnostics{AgentVersion: "1", ExportHealth: many}, false, at)
	if n := len(v3.diagDoc().ExportHealth.Routes); n != maxDiagExportRoutes {
		t.Errorf("%d routes kept, want %d", n, maxDiagExportRoutes)
	}
}

// ---- chunked full sync ----

func workloads(n int, prefix string) []*continuumv1.WorkloadFacts {
	out := make([]*continuumv1.WorkloadFacts, n)
	for i := range out {
		out[i] = &continuumv1.WorkloadFacts{Key: fmt.Sprintf("ns%d/Deployment/%s%d", i%50, prefix, i), Name: fmt.Sprintf("%s%d", prefix, i), Namespace: fmt.Sprintf("ns%d", i%50), Kind: "Deployment",
			Labels: map[string]string{"app": fmt.Sprintf("%s%d", prefix, i), "tier": "backend"}}
	}
	return out
}

// pieces cuts a full picture into n chunks the way an agent does (cluster facts in the first).
func pieces(seq uint64, cluster *continuumv1.ClusterFacts, nodes []*continuumv1.NodeFacts, ws []*continuumv1.WorkloadFacts, n int, id string) []*continuumv1.Sync {
	var out []*continuumv1.Sync
	per := (len(ws) + n - 1) / n
	for i := 0; i < n; i++ {
		s := &continuumv1.Sync{Seq: seq, Full: true, ChunkIndex: uint32(i), ChunkTotal: uint32(n), SyncId: id}
		if i == 0 {
			s.Cluster, s.Nodes = cluster, nodes
		}
		lo, hi := i*per, min((i+1)*per, len(ws))
		if lo < hi {
			s.Workloads = ws[lo:hi]
		}
		out = append(out, s)
	}
	return out
}

func held(r *hubRig, id string) (nodes, workloads int, seq uint64) {
	r.hub.mu.Lock()
	defer r.hub.mu.Unlock()
	st := r.hub.views[id].state
	return len(st.Nodes), len(st.Workloads), st.Seq
}

func TestSixtyThousandWorkloadsArriveInChunksAndApplyOnTheLastOne(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 100, Workloads: 100000, Bytes: 64 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	s := r.agentStream(t, ctx, id, key, leaf)
	cluster := &continuumv1.ClusterFacts{Uid: fp}

	// An earlier, smaller picture is what a reader sees until the whole new one is in.
	if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 2), Workloads: workloads(10, "old")})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck().GetSeq() != 1 {
		t.Fatalf("first picture: %v %v", m, err)
	}

	const total, nChunks = 60000, 60 // more chunks than the per-stream burst: continuation pieces are not rate limited
	all := pieces(2, cluster, nodes("n", 3), workloads(total, "w"), nChunks, "2-abc")
	chunkedBefore := Metrics.syncsChunked.Load()
	for i, c := range all {
		if err := s.Send(syncMsg(c)); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if i == nChunks/2 {
			if n, w, _ := held(r, id); n != 2 || w != 10 {
				t.Fatalf("half way the server holds %d nodes and %d workloads: the old picture must stay until the last chunk", n, w)
			}
		}
	}
	m, err := s.Recv() // the only Ack, after the last chunk
	if err != nil || m.GetAck().GetSeq() != 2 {
		t.Fatalf("ack after the last chunk: %v %v", m, err)
	}
	if n, w, _ := held(r, id); n != 3 || w != total {
		t.Fatalf("after the last chunk: %d nodes, %d workloads", n, w)
	}
	if Metrics.syncsChunked.Load() != chunkedBefore+1 {
		t.Fatal("the chunked picture was not counted")
	}
	// The stream still works for ordinary messages afterwards.
	if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 3, Workloads: workloads(1, "late")})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck().GetSeq() != 3 {
		t.Fatalf("a delta after a chunked picture: %v %v", m, err)
	}
}

func TestAConnectionDroppedMidPictureLeavesThePreviousStateAlone(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	cluster := &continuumv1.ClusterFacts{Uid: fp}
	cctx, ccancel := context.WithCancel(ctx)
	s := r.agentStream(t, cctx, id, key, leaf)
	if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 4), Workloads: workloads(20, "old")})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck() == nil {
		t.Fatalf("%v %v", m, err)
	}
	all := pieces(2, cluster, nodes("new", 9), workloads(900, "new"), 6, "2-x")
	for _, c := range all[:4] { // four of six, then the connection dies
		if err := s.Send(syncMsg(c)); err != nil {
			t.Fatal(err)
		}
	}
	ccancel()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		r.hub.mu.Lock()
		on := r.hub.views[id].connected
		r.hub.mu.Unlock()
		if !on {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, w, _ := held(r, id); n != 4 || w != 20 {
		t.Fatalf("a dropped connection changed the state: %d nodes, %d workloads", n, w)
	}
	// The reconnected agent starts the picture again from chunk 0 and it applies.
	s2 := r.agentStream(t, ctx, id, key, leaf)
	for _, c := range pieces(3, cluster, nodes("new", 9), workloads(900, "new"), 6, "3-y") {
		if err := s2.Send(syncMsg(c)); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := s2.Recv(); err != nil || m.GetAck().GetSeq() != 3 {
		t.Fatalf("%v %v", m, err)
	}
	if n, w, _ := held(r, id); n != 9 || w != 900 {
		t.Fatalf("%d nodes, %d workloads", n, w)
	}
}

func TestChunkSeriesBreakingARuleAreRefusedAndChangeNothing(t *testing.T) {
	cluster := &continuumv1.ClusterFacts{Uid: fp}
	cases := []struct {
		name   string
		limits *facts.Limits
		send   func(id string) []*continuumv1.Sync
		code   codes.Code
		text   string
	}{
		{"chunk out of order", nil, func(string) []*continuumv1.Sync {
			p := pieces(2, cluster, nil, workloads(30, "w"), 3, "2-a")
			return []*continuumv1.Sync{p[0], p[2]}
		}, codes.InvalidArgument, "was expected"},
		{"a continuation with no start", nil, func(string) []*continuumv1.Sync {
			return pieces(2, cluster, nil, workloads(30, "w"), 3, "2-a")[1:2]
		}, codes.InvalidArgument, "no chunk 0"},
		{"another series in the middle", nil, func(string) []*continuumv1.Sync {
			a, b := pieces(2, cluster, nil, workloads(30, "w"), 3, "2-a"), pieces(2, cluster, nil, workloads(30, "w"), 3, "2-b")
			return []*continuumv1.Sync{a[0], b[1]}
		}, codes.InvalidArgument, "was expected"},
		{"too many chunks", nil, func(string) []*continuumv1.Sync {
			s := pieces(2, cluster, nil, workloads(3, "w"), 3, "2-a")[0]
			s.ChunkTotal = maxChunkTotal + 1
			return []*continuumv1.Sync{s}
		}, codes.InvalidArgument, "at most"},
		{"no sync id", nil, func(string) []*continuumv1.Sync {
			s := pieces(2, cluster, nil, workloads(3, "w"), 3, "")[0]
			return []*continuumv1.Sync{s}
		}, codes.InvalidArgument, "sync_id"},
		{"cluster facts in a later chunk", nil, func(string) []*continuumv1.Sync {
			p := pieces(2, cluster, nil, workloads(30, "w"), 3, "2-a")
			p[1].Cluster = cluster
			return p[:2]
		}, codes.InvalidArgument, "first chunk"},
		{"over the limits, caught before the rest is buffered", &facts.Limits{Nodes: 10, Namespaces: 10, Workloads: 50, Bytes: 1 << 20}, func(string) []*continuumv1.Sync {
			return pieces(2, cluster, nil, workloads(120, "w"), 4, "2-a") // 30 per chunk: the second takes it to 60
		}, codes.ResourceExhausted, "workloads"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHubRig(t)
			if c.limits != nil {
				r.hub.Limits = c.limits
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			id, key, leaf := r.approvedAgent(t, fp)
			s := r.agentStream(t, ctx, id, key, leaf)
			if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 2), Workloads: workloads(5, "old")})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Recv(); err != nil {
				t.Fatal(err)
			}
			refused := Metrics.syncsRefused.Load()
			for _, m := range c.send(id) {
				_ = s.Send(syncMsg(m))
			}
			_, err := s.Recv()
			if status.Code(err) != c.code || !strings.Contains(err.Error(), c.text) {
				t.Fatalf("got %v, want %v containing %q", err, c.code, c.text)
			}
			if n, w, _ := held(r, id); n != 2 || w != 5 {
				t.Fatalf("a refused series changed the state: %d nodes, %d workloads", n, w)
			}
			if Metrics.syncsRefused.Load() <= refused {
				t.Fatal("the refusal was not counted")
			}
		})
	}
}

func TestAnAssembledPictureMustMeetTheLimitsAsAWhole(t *testing.T) {
	// Every piece is within the limits, and so is each running total until the last piece takes it over: the assembly refuses that piece.
	a, err := newAssembly(&continuumv1.Sync{Seq: 1, Full: true, ChunkTotal: 3, SyncId: "1-a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	l := facts.Limits{Nodes: 10, Namespaces: 10, Workloads: 25, Bytes: 1 << 20}
	for i := 0; i < 3; i++ {
		_, err = a.add(&continuumv1.Sync{Seq: 1, Full: true, ChunkIndex: uint32(i), ChunkTotal: 3, SyncId: "1-a", Workloads: workloads(10, fmt.Sprint("p", i))}, l)
		if i < 2 && err != nil {
			t.Fatalf("piece %d: %v", i, err)
		}
	}
	if _, ok := err.(*facts.LimitError); !ok {
		t.Fatalf("the third piece: %v", err)
	}
	// The byte limit is enforced on what is buffered, too.
	a, _ = newAssembly(&continuumv1.Sync{Seq: 1, Full: true, ChunkTotal: 2, SyncId: "1-a"}, time.Now())
	if _, err := a.add(&continuumv1.Sync{Seq: 1, Full: true, ChunkTotal: 2, SyncId: "1-a", Workloads: workloads(100, "b")}, facts.Limits{Nodes: 1, Namespaces: 1, Workloads: 1000, Bytes: 500}); err == nil {
		t.Fatal("a piece over the byte limit was buffered")
	}
	if a.expired(time.Now()) || !a.expired(time.Now().Add(assemblyTTL+time.Second)) {
		t.Fatal("expiry")
	}
}

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
	a.approvedAgent(t, fp)
	Metrics.rateLimited.Add(0)
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
	for _, want := range []string{`continuum_agents{status="approved"} 1`, `continuum_agents{status="pending"} 0`, "continuum_agents_connected 0", "continuum_syncs_applied_total", "continuum_syncs_refused_total",
		"continuum_auth_failures_total", "continuum_rate_limited_total", "continuum_stream_panics_total", "continuum_store_errors_total", `continuum_build_info{version="v-test"`, "# TYPE continuum_agents gauge"} {
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
	continuumv1.UnimplementedAgentServiceServer
}

func (panickyAgents) Connect(continuumv1.AgentService_ConnectServer) error { panic("boom") }

func TestAPanicInAnAgentStreamIsRecoveredAndTheServerKeepsServing(t *testing.T) {
	e := newEnv(t)
	srv := e.base.NewGRPC(pki.NewServerCerts(e.base.CA, []string{"127.0.0.1"}), panickyAgents{})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	defer srv.Stop()
	r := &rig{env: e, addr: l.Addr().String()}
	id, key, leaf := e.approvedAgent(t, fp)
	_ = id
	before := Metrics.streamPanics.Load()
	for i := 0; i < 2; i++ { // the second connection proves the process is still serving
		cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
		c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(e.base.CA.Pin(), "127.0.0.1", cert)))
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
	if Metrics.streamPanics.Load() != before+2 {
		t.Fatalf("panics counted: %d", Metrics.streamPanics.Load()-before)
	}
}

func TestRecoverPanicHelpers(t *testing.T) {
	e := newEnv(t)
	before := Metrics.streamPanics.Load()
	err := func() (err error) {
		defer e.base.recoverPanic("test", &err)
		panic("x")
	}()
	if status.Code(err) != codes.Internal || strings.Contains(err.Error(), "x") {
		t.Fatalf("err = %v (the panic text must not reach the caller)", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); e.base.safely("t", func() { panic("y") }) }()
	<-done
	if Metrics.streamPanics.Load() != before+2 {
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
	srv := e.base.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	id, key, leaf := e.approvedAgent(t, fp)
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
	srv := e.base.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	rr := &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: tn.Hub}
	id, key, leaf := rr.approvedAgent(t, fp) // approved at 2, installed at 2

	admin := &Admin{P: p, C: e.base, AgentAddr: "x:1", ChartRef: "chart"}
	a := &adminRig{env: e, h: admin.Handler(), a: admin}
	_, editor := a.user(t, "edith", RoleEditor)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hello := &continuumv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 2, Namespace: "observability", ReleaseName: "obs-agent"}
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
	if resp := a.do("POST", "/api/v1/agents/"+id+"/tier", map[string]any{"tier": 2}, withCookie(editor)); resp.Code != 200 {
		t.Fatalf("widen: %d %s", resp.Code, resp.Body.String())
	}
	if polled = agentDoc(t, a, editor, id); polled.HardenHelm != "" {
		t.Fatalf("hardenHelm should be empty once approved matches installed: %+v", polled)
	}
}

func TestUpgradeAndTeardownCommandsGuessTheNamespaceWhenNeverReported(t *testing.T) {
	helm, secret, guessed := teardownCommands("", "")
	if !guessed || !strings.Contains(helm, "helm uninstall continuum-agent ") || !strings.Contains(helm, "--namespace continuum-system") || !strings.Contains(secret, "--namespace continuum-system") {
		t.Fatalf("teardown with nothing reported: %q %q %v", helm, secret, guessed)
	}
	helm, secret, guessed = teardownCommands("apps-team", "")
	if !guessed || !strings.Contains(helm, "helm uninstall continuum-agent ") || !strings.Contains(helm, "--namespace apps-team") || !strings.Contains(secret, "--namespace apps-team") {
		t.Fatalf("teardown with a known namespace but no release: %q %q %v", helm, secret, guessed)
	}
	helm, secret, guessed = teardownCommands("apps-team", "obs-agent")
	if guessed || !strings.Contains(helm, "helm uninstall obs-agent ") || !strings.Contains(helm, "--namespace apps-team") || !strings.Contains(secret, "--namespace apps-team") {
		t.Fatalf("teardown with a known namespace and release: %q %q %v", helm, secret, guessed)
	}
	// The identity Secret's own name never depends on the release: every chart object has a fixed name.
	if !strings.Contains(secret, "continuum-agent-identity") {
		t.Fatalf("secret name should stay fixed regardless of release name: %q", secret)
	}
}

func TestRevokedAgentCarriesTeardownCommandsOnlyForEditorsAndAbove(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", RoleEditor)
	_, viewer := a.user(t, "vic", RoleViewer)
	id, _, _ := a.approvedAgent(t, fp)
	if err := a.core.Revoke(a.ctx, "actor", id, "for the test"); err != nil {
		t.Fatal(err)
	}

	d := agentDoc(t, a, editor, id)
	if d.Teardown == nil || !strings.Contains(d.Teardown.Helm, "helm uninstall continuum-agent") || !strings.Contains(d.Teardown.Secret, "continuum-agent-identity") {
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

// ---- the server-side backstop for paused collectors and excluded namespaces (dropConsentOverrides, noteFlows, noteMeasurements) ----

// TestDropConsentOverridesStripsPausedProbeFields checks the server's own defense in depth for a paused "probes"
// collector: even if an agent kept sending machine-identity fields, applySync must not let them through.
func TestDropConsentOverridesStripsPausedProbeFields(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp) // approved at tier 2
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Paused: []string{"probes"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := r.st.GetAgent(r.ctx, id)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()
	n := &continuumv1.NodeFacts{Key: "n", Name: "n", MachineId: "m", SystemUuid: "u", ProviderId: "p",
		OsImage: "o", KernelVersion: "k", ContainerRuntime: "c", KubeletVersion: "kv", Architecture: "amd64"}
	if _, _, err := r.hub.applySync(a, &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: []*continuumv1.NodeFacts{n}}, false); err != nil {
		t.Fatal(err)
	}
	got := r.hub.views[id].state.Nodes["n"]
	if got == nil {
		t.Fatal("node was dropped entirely, not just its probe fields")
	}
	if got.MachineId != "" || got.SystemUuid != "" || got.ProviderId != "" || got.OsImage != "" || got.KernelVersion != "" || got.ContainerRuntime != "" || got.KubeletVersion != "" || got.Architecture != "" {
		t.Fatalf("a paused probes collector's fields survived: %+v", got)
	}

	// Un-pausing lets the same fields through again (a fresh node: dropConsentOverrides mutates the one above in place).
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{}); err != nil {
		t.Fatal(err)
	}
	n2 := &continuumv1.NodeFacts{Key: "n", Name: "n", MachineId: "m", SystemUuid: "u", ProviderId: "p",
		OsImage: "o", KernelVersion: "k", ContainerRuntime: "c", KubeletVersion: "kv", Architecture: "amd64"}
	if _, _, err := r.hub.applySync(a, &continuumv1.Sync{Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: []*continuumv1.NodeFacts{n2}}, true); err != nil {
		t.Fatal(err)
	}
	if got := r.hub.views[id].state.Nodes["n"]; got.MachineId != "m" || got.Architecture != "amd64" {
		t.Fatalf("un-pausing did not restore the fields: %+v", got)
	}
}

// TestDropConsentOverridesFiltersExcludedNamespaces checks the same backstop for excluded namespaces: they (and
// the workloads in them) must not reach the stored state even if the agent still reports them, and this must be
// independent from the tier check (both are tier-2 approvals here).
func TestDropConsentOverridesFiltersExcludedNamespaces(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp) // approved at tier 2
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := r.st.GetAgent(r.ctx, id)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}},
		Workloads:  []*continuumv1.WorkloadFacts{{Key: "shop/Deployment/web", Namespace: "shop"}, {Key: "batch/Job/x", Namespace: "batch"}}}
	if _, _, err := r.hub.applySync(a, sy, false); err != nil {
		t.Fatal(err)
	}
	st := r.hub.views[id].state
	if _, ok := st.Namespaces["ns/batch"]; ok {
		t.Fatal("an excluded namespace was kept")
	}
	if _, ok := st.Namespaces["ns/shop"]; !ok {
		t.Fatal("a namespace that was not excluded was dropped too")
	}
	for k := range st.Workloads {
		if strings.HasPrefix(k, "batch/") {
			t.Fatalf("a workload in an excluded namespace was kept: %s", k)
		}
	}
	if _, ok := st.Workloads["shop/Deployment/web"]; !ok {
		t.Fatal("a workload outside the excluded namespace was dropped too")
	}
}

// TestNoteFlowsRespectsTierAndPauseConsent covers noteFlows directly (no gRPC): the tier gate, the "flow" pause,
// and that a genuinely malformed batch is still a real error, not another quiet skip.
func TestNoteFlowsRespectsTierAndPauseConsent(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()
	fb := &continuumv1.FlowBatch{WindowSeconds: 60}

	if applied, err := r.hub.noteFlows(id, 1, fb, time.Now()); applied || err != nil {
		t.Fatalf("tier 1 (workloads not approved) should be quietly ignored: applied=%v err=%v", applied, err)
	}
	if applied, err := r.hub.noteFlows(id, 2, fb, time.Now()); !applied || err != nil {
		t.Fatalf("tier 2 with no pause should apply: applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	dirty := r.hub.views[id].flowsDirty
	r.hub.mu.Unlock()
	if !dirty {
		t.Fatal("an applied batch did not mark flows dirty")
	}

	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.mu.Lock()
	r.hub.views[id].flowsDirty = false
	r.hub.mu.Unlock()
	if applied, err := r.hub.noteFlows(id, 2, fb, time.Now()); applied || err != nil {
		t.Fatalf("a paused flow collector should be quietly ignored: applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	dirty = r.hub.views[id].flowsDirty
	r.hub.mu.Unlock()
	if dirty {
		t.Fatal("a paused batch was applied anyway")
	}

	bad := &continuumv1.FlowBatch{WindowSeconds: 0} // malformed: still an error, not just skipped
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{}); err != nil {
		t.Fatal(err)
	}
	if applied, err := r.hub.noteFlows(id, 2, bad, time.Now()); applied || err == nil {
		t.Fatalf("a malformed batch must be a real error, not a quiet skip: applied=%v err=%v", applied, err)
	}
}

// TestNoteFlowsReplacesPodFlowsWholesale covers view.podFlows' own "right now" rule directly: unlike
// the flowTable's historically-accumulated edges, a new batch's pod_flows replaces what was held, never
// merges with it - so a pod pair missing from the latest batch must disappear, not linger from an
// earlier report.
func TestNoteFlowsReplacesPodFlowsWholesale(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()

	first := flowOf(wep("a/Deployment/x"), xep("1.2.3.4"), 443, 1)
	first.SrcPod = "x-abc-1"
	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60, PodFlows: []*continuumv1.Flow{first}}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	got := r.hub.views[id].podFlows
	r.hub.mu.Unlock()
	if len(got) != 1 || got[0].SrcPod != "x-abc-1" {
		t.Fatalf("podFlows after the first batch = %v", got)
	}

	second := flowOf(wep("a/Deployment/x"), xep("1.2.3.4"), 443, 1)
	second.SrcPod = "x-abc-2"
	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60, PodFlows: []*continuumv1.Flow{second}}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	got = r.hub.views[id].podFlows
	r.hub.mu.Unlock()
	if len(got) != 1 || got[0].SrcPod != "x-abc-2" {
		t.Fatalf("a second batch must replace the first pod pair, not add to it: %v", got)
	}

	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	got = r.hub.views[id].podFlows
	r.hub.mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("a batch reporting no pod flows at all must clear what was held, not leave x-abc-2 behind: %v", got)
	}
}

// TestNoteFlowsReplacesLinkSaturationWholesale is linkSat's own counterpart to
// TestNoteFlowsReplacesPodFlowsWholesale right above: a node's link-saturation reading is a gauge of
// the window just reported, not a running total, so a batch that no longer mentions a node (or mentions
// it with nothing on any interface) must clear what was held for it, never let a stale reading linger.
func TestNoteFlowsReplacesLinkSaturationWholesale(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()

	pct := 55.0
	first := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{
		{Node: "n1", Method: "ebpf", LinkSaturation: []*continuumv1.LinkSaturation{{Iface: "eth0", ThroughputBps: 1, SaturationPct: &pct}}},
	}}
	if applied, err := r.hub.noteFlows(id, 2, first, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	got := r.hub.views[id].linkSat
	r.hub.mu.Unlock()
	if len(got["n1"]) != 1 || got["n1"][0].Iface != "eth0" {
		t.Fatalf("linkSat after the first batch = %v", got)
	}

	second := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{{Node: "n1", Method: "ebpf"}}}
	if applied, err := r.hub.noteFlows(id, 2, second, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.mu.Lock()
	got = r.hub.views[id].linkSat
	r.hub.mu.Unlock()
	if len(got["n1"]) != 0 {
		t.Fatalf("a batch with no link saturation for n1 must clear what was held, not leave eth0's old reading behind: %v", got)
	}
}

// TestNoteMeasurementsRespectsPauseConsent covers the same backstop for the "measure" collector, using
// noteMeasurements directly the way the existing tier tests use applySync directly.
func TestNoteMeasurementsRespectsPauseConsent(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	v := newView()
	v.targets["t1"] = issuedTarget{ID: "t1", Host: "h", Port: 80, Label: "l", Source: "observed"}
	r.hub.views[id] = v
	r.hub.mu.Unlock()

	m := &continuumv1.Measurements{Results: []*continuumv1.PathResult{{TargetId: "t1", Samples: 5, RttMinMs: 1, RttP50Ms: 2, RttP95Ms: 3}}}
	r.hub.noteMeasurements(id, m)
	if len(v.paths["t1"].rounds) != 1 {
		t.Fatalf("a measurement with no pause in force should be recorded: %+v", v.paths["t1"])
	}

	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Paused: []string{"measure"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.noteMeasurements(id, m)
	if len(v.paths["t1"].rounds) != 1 {
		t.Fatalf("a paused measure collector must not record anything new: %+v", v.paths["t1"])
	}
}

// ---- the observability-intent panel's staleness signal (ConsentDoc.Confirmed / SetAt) ----

// TestConsentDocSaysWhetherTheAgentHasConfirmedANarrowing covers consentDoc's own cross-check between
// what an administrator asked for and what the agent's diagnostics say is actually in force: unconfirmed
// until a matching report arrives, confirmed once one does, and unconfirmed again (with a fresh SetAt)
// the moment the ask changes without a new report to match it - the elapsed-time signal the
// observability-intent panel needs to show a narrowing that a stuck or too-old agent never picked up.
func TestConsentDocSaysWhetherTheAgentHasConfirmedANarrowing(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)

	// Nothing narrowed yet: trivially confirmed, nothing to time.
	doc := r.hub.consentOf(id)
	_ = doc
	r.hub.mu.Lock()
	v := r.hub.viewFor(id)
	r.hub.mu.Unlock()
	if cd := v.consentDoc(); !cd.Confirmed || cd.SetAt != "" {
		t.Fatalf("nothing narrowed should read as confirmed with no SetAt: %+v", cd)
	}

	// Narrow it: unconfirmed until the agent is heard from, and the change is timestamped.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("a fresh narrowing with no agent report yet should be unconfirmed, with a SetAt: %+v", cd)
	}

	// The agent reports back something that does not yet match (an older report, or a different pause):
	// still unconfirmed.
	r.hub.mu.Lock()
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{}}, false, *r.now)
	r.hub.mu.Unlock()
	if cd := v.consentDoc(); cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("a report that does not yet show the pause should stay unconfirmed: %+v", cd)
	}

	// The agent catches up: confirmed, and SetAt drops off since there is nothing stale to show any more.
	r.hub.mu.Lock()
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{"flow"}}, false, *r.now)
	r.hub.mu.Unlock()
	if cd := v.consentDoc(); !cd.Confirmed || cd.SetAt != "" {
		t.Fatalf("a report matching the ask should be confirmed with no SetAt: %+v", cd)
	}

	// Changing the ask again, with no new report yet, goes back to unconfirmed - the agent's last report
	// matched the *previous* ask, not this one.
	*r.now = r.now.Add(time.Minute)
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Paused: []string{"flow"}, Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	cd := v.consentDoc()
	if cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("widening what is asked for should be unconfirmed again until the agent reports it: %+v", cd)
	}
	if cd.SetAt != rfc(*r.now) {
		t.Fatalf("SetAt should be this change's own moment, not the earlier one: got %s want %s", cd.SetAt, rfc(*r.now))
	}

	// Excluded namespaces can only be confirmed by count (the agent never reports which ones - see
	// ExcludedNamespaces's own doc comment): a report with the right paused collector and the right count
	// of excluded namespaces reads as confirmed even though this test cannot tell the diagnostic "batch" is
	// the same namespace, which is the documented, accepted limitation.
	r.hub.mu.Lock()
	noteDiagnostics(v, &continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{"flow"}, ExcludedNamespaces: 1}, false, *r.now)
	r.hub.mu.Unlock()
	if cd := v.consentDoc(); !cd.Confirmed {
		t.Fatalf("matching pause and matching excluded count should confirm: %+v", cd)
	}
}

// TestConsentDocWarnsAboutExcludedNamespacesTheAgentHasNeverReported covers Fix 6: excluded namespace
// names are checked against what the agent has actually reported, but only as a warning next to the
// narrowing that named them - never blocking SetConsent itself, since an administrator may legitimately
// pre-declare a namespace before its first workload appears.
func TestConsentDocWarnsAboutExcludedNamespacesTheAgentHasNeverReported(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp) // approved at tier 2
	a, _ := r.st.GetAgent(r.ctx, id)

	// Nothing has ever synced: no warning, even though nothing is known yet - there is no "reality" to
	// check against, so silence here, not a false alarm about every excluded name.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.mu.Lock()
	v := r.hub.viewFor(id)
	r.hub.mu.Unlock()
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 0 {
		t.Fatalf("no sync yet: nothing should be flagged: %+v", cd)
	}

	// The agent's first picture reports "shop" only. "batch" was asked to be excluded but the agent has
	// never actually seen it - a likely typo, or one that has not rolled out yet either way.
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}}}
	if _, _, err := r.hub.applySync(a, sy, false); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 1 || cd.UnknownNamespaces[0] != "batch" {
		t.Fatalf("batch was never reported, should be the one namespace flagged: %+v", cd)
	}

	// Widen the exclusion to also cover "shop", which the agent does report: only "batch" stays flagged -
	// this is not a blanket "something is excluded" warning, it is specific to the name that is not real.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, Consent{Excluded: []string{"batch", "shop"}}); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 1 || cd.UnknownNamespaces[0] != "batch" {
		t.Fatalf("shop is real and excluded too, only batch should stay flagged: %+v", cd)
	}

	// The agent's next picture (a delta, on top of what it already reported) includes "batch" too - even
	// though it is excluded and dropConsentOverrides strips it back out before it is ever stored, the name
	// itself was seen before that filter ran, so the warning about it clears on the very next read.
	sy2 := &continuumv1.Sync{Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/batch", Name: "batch"}}}
	if _, _, err := r.hub.applySync(a, sy2, false); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 0 {
		t.Fatalf("batch has now been reported, the warning should have cleared: %+v", cd)
	}
	// And dropConsentOverrides' own job is untouched by any of this: batch is still excluded from what is
	// actually stored, only the metadata used to validate the exclusion's spelling learned about it.
	if _, ok := r.hub.views[id].state.Namespaces["ns/batch"]; ok {
		t.Fatal("batch is still excluded and should still be stripped from stored state")
	}
}

// TestKnownNamespacesOnlyLearnsFromASyncTheServerActuallyAccepted guards against a rejected sync teaching
// viewExt.knownNamespaces about namespaces that were never actually accepted: applySync's own doc comment
// says a sync Check refuses "changes nothing", and that has to hold for what an excluded name is later
// checked against too, not just for the stored state itself.
func TestKnownNamespacesOnlyLearnsFromASyncTheServerActuallyAccepted(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 1, Workloads: 100, Bytes: 64 << 20}
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)

	// Two namespaces against a limit of one: refused outright, nothing merged.
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}}}
	if _, _, err := r.hub.applySync(a, sy, false); err == nil {
		t.Fatal("a sync over the namespace limit should have been refused")
	}
	r.hub.mu.Lock()
	known := r.hub.viewFor(id).ext.knownNamespaces
	r.hub.mu.Unlock()
	if known["batch"] || known["shop"] {
		t.Fatalf("a rejected sync should not have taught knownNamespaces anything: %v", known)
	}
}

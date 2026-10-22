package estate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
	"continuum/internal/kernel"
	"continuum/internal/testkit"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHelloAdvertisesTheCeilingAndDiagnosticsFollowHeartbeats(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP) // approved at 2, installed 2

	// The owner has since run `helm upgrade --set access.tier=1`: the hello says so, the server records it and the approval follows it down.
	hello := &continuumv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 1, Diagnostics: &continuumv1.Diagnostics{AgentVersion: "9.9", Arch: "arm64", InstalledTier: 1, EffectiveTier: 1, ApprovedTier: 2}}
	s := r.connect(t, ctx, key, leaf, hello)
	a, _ := r.st.GetAgent(ctx, "org-1", id)
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
	var got *agents.DiagnosticsDoc
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
	v := NewView()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1"}, false, time.Now())
	huge := &continuumv1.Diagnostics{AgentVersion: "2"}
	for i := 0; i < 5000; i++ {
		huge.Problems = append(huge.Problems, &continuumv1.Problem{Code: "x", Message: strings.Repeat("m", 100)})
	}
	v.NoteDiagnostics(huge, false, time.Now())
	if v.Ext.Diag.AgentVersion != "1" {
		t.Fatal("an oversized account replaced the previous one")
	}
	many := &continuumv1.Diagnostics{AgentVersion: "3"}
	for i := 0; i < 100; i++ {
		many.Problems = append(many.Problems, &continuumv1.Problem{Code: "x"})
		many.Informers = append(many.Informers, &continuumv1.InformerDiag{Name: "i"})
	}
	v.NoteDiagnostics(many, false, time.Now())
	if len(v.Ext.Diag.Problems) != agents.MaxDiagProblems || len(v.Ext.Diag.Informers) != agents.MaxDiagInformers {
		t.Fatalf("lists were not cut: %d %d", len(v.Ext.Diag.Problems), len(v.Ext.Diag.Informers))
	}
}

func TestInstalledTelemetrySignalsRoundTripAndTruncate(t *testing.T) {
	v := NewView()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", InstalledTelemetrySignals: []string{"traces", "energy"}}, false, time.Now())
	doc := v.diagDoc()
	if got := strings.Join(doc.InstalledTelemetry, ","); got != "traces,energy" {
		t.Fatalf("InstalledTelemetry = %v, want [traces energy] (order preserved, the agent already sorts it)", doc.InstalledTelemetry)
	}

	// A longer-than-bound list is truncated, not refused outright - same treatment as PausedCollectors/Problems.
	v2 := NewView()
	long := &continuumv1.Diagnostics{AgentVersion: "1"}
	for i := 0; i < agents.MaxDiagTelemetrySignals+5; i++ {
		long.InstalledTelemetrySignals = append(long.InstalledTelemetrySignals, fmt.Sprintf("signal%d", i))
	}
	v2.NoteDiagnostics(long, false, time.Now())
	if len(v2.Ext.Diag.InstalledTelemetrySignals) != agents.MaxDiagTelemetrySignals {
		t.Fatalf("InstalledTelemetrySignals not truncated: %d entries", len(v2.Ext.Diag.InstalledTelemetrySignals))
	}
}

func TestInstalledTelemetryConfigRoundTrips(t *testing.T) {
	v := NewView()
	tracesPct := uint32(20)
	energySource := "bundle-kepler"
	v.NoteDiagnostics(&continuumv1.Diagnostics{
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
	v2 := NewView()
	v2.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1"}, false, time.Now())
	if doc2 := v2.diagDoc(); doc2.InstalledTelemetryConfig != nil {
		t.Fatalf("InstalledTelemetryConfig = %v, want nil when the agent reported none", doc2.InstalledTelemetryConfig)
	}
}

func TestExportHealthRelaysTheAgentsStatesAndBoundsThem(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	v := NewView()
	h := &continuumv1.ExportHealth{
		ScrapedAt: timestamppb.New(at), PodsReached: 3, PodsFailed: 1, LastError: "read 10.0.0.2:8888: connection reset",
		Routes: []*continuumv1.ExportRouteHealth{
			{Exporter: "otlphttp/logs", Signal: "logs", State: continuumv1.ExportRouteHealth_FAILING, Sent: 5, Failed: 9, LastSentAt: timestamppb.New(at.Add(-time.Hour)), LastFailedAt: timestamppb.New(at)},
			{Exporter: "zipkin/traces", Signal: "traces", State: continuumv1.ExportRouteHealth_State(99)},
		},
	}
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", ExportHealth: h}, false, at)
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
	v2 := NewView()
	v2.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1"}, false, at)
	if v2.diagDoc().ExportHealth != nil {
		t.Error("ExportHealth must be nil when the agent reported none")
	}

	// An agent cannot make the server hold more routes than it should.
	many := &continuumv1.ExportHealth{}
	for i := 0; i < agents.MaxDiagExportRoutes+10; i++ {
		many.Routes = append(many.Routes, &continuumv1.ExportRouteHealth{Exporter: "e", Signal: "logs"})
	}
	v3 := NewView()
	v3.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", ExportHealth: many}, false, at)
	if n := len(v3.diagDoc().ExportHealth.Routes); n != agents.MaxDiagExportRoutes {
		t.Errorf("%d routes kept, want %d", n, agents.MaxDiagExportRoutes)
	}
}

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
	r.hub.Mu.Lock()
	defer r.hub.Mu.Unlock()
	st := r.hub.Views[id].State
	return len(st.Nodes), len(st.Workloads), st.Seq
}

func TestSixtyThousandWorkloadsArriveInChunksAndApplyOnTheLastOne(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 100, Workloads: 100000, Bytes: 64 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	s := r.agentStream(t, ctx, id, key, leaf)
	cluster := &continuumv1.ClusterFacts{Uid: testkit.FP}

	// An earlier, smaller picture is what a reader sees until the whole new one is in.
	if err := s.Send(testkit.SyncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 2), Workloads: workloads(10, "old")})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck().GetSeq() != 1 {
		t.Fatalf("first picture: %v %v", m, err)
	}

	const total, nChunks = 60000, 60 // more chunks than the per-stream burst: continuation pieces are not rate limited
	all := pieces(2, cluster, nodes("n", 3), workloads(total, "w"), nChunks, "2-abc")
	chunkedBefore := kernel.Metrics.SyncsChunked.Load()
	for i, c := range all {
		if err := s.Send(testkit.SyncMsg(c)); err != nil {
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
	if kernel.Metrics.SyncsChunked.Load() != chunkedBefore+1 {
		t.Fatal("the chunked picture was not counted")
	}
	// The stream still works for ordinary messages afterwards.
	if err := s.Send(testkit.SyncMsg(&continuumv1.Sync{Seq: 3, Workloads: workloads(1, "late")})); err != nil {
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
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	cluster := &continuumv1.ClusterFacts{Uid: testkit.FP}
	cctx, ccancel := context.WithCancel(ctx)
	s := r.agentStream(t, cctx, id, key, leaf)
	if err := s.Send(testkit.SyncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 4), Workloads: workloads(20, "old")})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck() == nil {
		t.Fatalf("%v %v", m, err)
	}
	all := pieces(2, cluster, nodes("new", 9), workloads(900, "new"), 6, "2-x")
	for _, c := range all[:4] { // four of six, then the connection dies
		if err := s.Send(testkit.SyncMsg(c)); err != nil {
			t.Fatal(err)
		}
	}
	ccancel()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		r.hub.Mu.Lock()
		on := r.hub.Views[id].Connected
		r.hub.Mu.Unlock()
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
		if err := s2.Send(testkit.SyncMsg(c)); err != nil {
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
	cluster := &continuumv1.ClusterFacts{Uid: testkit.FP}
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
			id, key, leaf := r.approvedAgent(t, testkit.FP)
			s := r.agentStream(t, ctx, id, key, leaf)
			if err := s.Send(testkit.SyncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("old", 2), Workloads: workloads(5, "old")})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Recv(); err != nil {
				t.Fatal(err)
			}
			refused := kernel.Metrics.SyncsRefused.Load()
			for _, m := range c.send(id) {
				_ = s.Send(testkit.SyncMsg(m))
			}
			_, err := s.Recv()
			if status.Code(err) != c.code || !strings.Contains(err.Error(), c.text) {
				t.Fatalf("got %v, want %v containing %q", err, c.code, c.text)
			}
			if n, w, _ := held(r, id); n != 2 || w != 5 {
				t.Fatalf("a refused series changed the state: %d nodes, %d workloads", n, w)
			}
			if kernel.Metrics.SyncsRefused.Load() <= refused {
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

// TestDropConsentOverridesStripsPausedProbeFields checks the server's own defense in depth for a paused "probes"
// collector: even if an agent kept sending machine-identity fields, ApplySync must not let them through.
func TestDropConsentOverridesStripsPausedProbeFields(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP) // approved at tier 2
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Paused: []string{"probes"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)
	r.hub.Mu.Lock()
	r.hub.Views[id] = NewView()
	r.hub.Mu.Unlock()
	n := &continuumv1.NodeFacts{Key: "n", Name: "n", MachineId: "m", SystemUuid: "u", ProviderId: "p",
		OsImage: "o", KernelVersion: "k", ContainerRuntime: "c", KubeletVersion: "kv", Architecture: "amd64"}
	if _, _, err := r.hub.ApplySync(a, &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP}, Nodes: []*continuumv1.NodeFacts{n}}, false); err != nil {
		t.Fatal(err)
	}
	got := r.hub.Views[id].State.Nodes["n"]
	if got == nil {
		t.Fatal("node was dropped entirely, not just its probe fields")
	}
	if got.MachineId != "" || got.SystemUuid != "" || got.ProviderId != "" || got.OsImage != "" || got.KernelVersion != "" || got.ContainerRuntime != "" || got.KubeletVersion != "" || got.Architecture != "" {
		t.Fatalf("a paused probes collector's fields survived: %+v", got)
	}

	// Un-pausing lets the same fields through again (a fresh node: dropConsentOverrides mutates the one above in place).
	if _, err := r.hub.SetConsent(testkit.AsRole(accounts.RoleAdmin), "actor", id, agents.Consent{}); err != nil {
		t.Fatal(err)
	}
	n2 := &continuumv1.NodeFacts{Key: "n", Name: "n", MachineId: "m", SystemUuid: "u", ProviderId: "p",
		OsImage: "o", KernelVersion: "k", ContainerRuntime: "c", KubeletVersion: "kv", Architecture: "amd64"}
	if _, _, err := r.hub.ApplySync(a, &continuumv1.Sync{Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP}, Nodes: []*continuumv1.NodeFacts{n2}}, true); err != nil {
		t.Fatal(err)
	}
	if got := r.hub.Views[id].State.Nodes["n"]; got.MachineId != "m" || got.Architecture != "amd64" {
		t.Fatalf("un-pausing did not restore the fields: %+v", got)
	}
}

// TestDropConsentOverridesFiltersExcludedNamespaces checks the same backstop for excluded namespaces: they (and
// the workloads in them) must not reach the stored state even if the agent still reports them, and this must be
// independent from the tier check (both are tier-2 approvals here).
func TestDropConsentOverridesFiltersExcludedNamespaces(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP) // approved at tier 2
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)
	r.hub.Mu.Lock()
	r.hub.Views[id] = NewView()
	r.hub.Mu.Unlock()
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}},
		Workloads:  []*continuumv1.WorkloadFacts{{Key: "shop/Deployment/web", Namespace: "shop"}, {Key: "batch/Job/x", Namespace: "batch"}}}
	if _, _, err := r.hub.ApplySync(a, sy, false); err != nil {
		t.Fatal(err)
	}
	st := r.hub.Views[id].State
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
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = NewView()
	r.hub.Mu.Unlock()
	fb := &continuumv1.FlowBatch{WindowSeconds: 60}

	if applied, err := r.hub.noteFlows(id, 1, fb, time.Now()); applied || err != nil {
		t.Fatalf("tier 1 (workloads not approved) should be quietly ignored: applied=%v err=%v", applied, err)
	}
	if applied, err := r.hub.noteFlows(id, 2, fb, time.Now()); !applied || err != nil {
		t.Fatalf("tier 2 with no pause should apply: applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	dirty := r.hub.Views[id].flowsDirty
	r.hub.Mu.Unlock()
	if !dirty {
		t.Fatal("an applied batch did not mark flows dirty")
	}

	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.Mu.Lock()
	r.hub.Views[id].flowsDirty = false
	r.hub.Mu.Unlock()
	if applied, err := r.hub.noteFlows(id, 2, fb, time.Now()); applied || err != nil {
		t.Fatalf("a paused flow collector should be quietly ignored: applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	dirty = r.hub.Views[id].flowsDirty
	r.hub.Mu.Unlock()
	if dirty {
		t.Fatal("a paused batch was applied anyway")
	}

	bad := &continuumv1.FlowBatch{WindowSeconds: 0} // malformed: still an error, not just skipped
	if _, err := r.hub.SetConsent(testkit.AsRole(accounts.RoleAdmin), "actor", id, agents.Consent{}); err != nil {
		t.Fatal(err)
	}
	if applied, err := r.hub.noteFlows(id, 2, bad, time.Now()); applied || err == nil {
		t.Fatalf("a malformed batch must be a real error, not a quiet skip: applied=%v err=%v", applied, err)
	}
}

// TestNoteFlowsReplacesPodFlowsWholesale covers view.podFlows' own "right now" rule directly: unlike
// the observed.FlowTable's historically-accumulated edges, a new batch's pod_flows replaces what was held, never
// merges with it - so a pod pair missing from the latest batch must disappear, not linger from an
// earlier report.
func TestNoteFlowsReplacesPodFlowsWholesale(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = NewView()
	r.hub.Mu.Unlock()

	first := testkit.FlowOf(testkit.WorkloadEndpoint("a/Deployment/x"), testkit.ExternalEndpoint("1.2.3.4"), 443, 1)
	first.SrcPod = "x-abc-1"
	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60, PodFlows: []*continuumv1.Flow{first}}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	got := r.hub.Views[id].podFlows
	r.hub.Mu.Unlock()
	if len(got) != 1 || got[0].SrcPod != "x-abc-1" {
		t.Fatalf("podFlows after the first batch = %v", got)
	}

	second := testkit.FlowOf(testkit.WorkloadEndpoint("a/Deployment/x"), testkit.ExternalEndpoint("1.2.3.4"), 443, 1)
	second.SrcPod = "x-abc-2"
	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60, PodFlows: []*continuumv1.Flow{second}}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	got = r.hub.Views[id].podFlows
	r.hub.Mu.Unlock()
	if len(got) != 1 || got[0].SrcPod != "x-abc-2" {
		t.Fatalf("a second batch must replace the first pod pair, not add to it: %v", got)
	}

	if applied, err := r.hub.noteFlows(id, 2, &continuumv1.FlowBatch{WindowSeconds: 60}, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	got = r.hub.Views[id].podFlows
	r.hub.Mu.Unlock()
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
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	r.hub.Views[id] = NewView()
	r.hub.Mu.Unlock()

	pct := 55.0
	first := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{
		{Node: "n1", Method: "ebpf", LinkSaturation: []*continuumv1.LinkSaturation{{Iface: "eth0", ThroughputBps: 1, SaturationPct: &pct}}},
	}}
	if applied, err := r.hub.noteFlows(id, 2, first, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	got := r.hub.Views[id].linkSat
	r.hub.Mu.Unlock()
	if len(got["n1"]) != 1 || got["n1"][0].Iface != "eth0" {
		t.Fatalf("linkSat after the first batch = %v", got)
	}

	second := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{{Node: "n1", Method: "ebpf"}}}
	if applied, err := r.hub.noteFlows(id, 2, second, time.Now()); !applied || err != nil {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	r.hub.Mu.Lock()
	got = r.hub.Views[id].linkSat
	r.hub.Mu.Unlock()
	if len(got["n1"]) != 0 {
		t.Fatalf("a batch with no link saturation for n1 must clear what was held, not leave eth0's old reading behind: %v", got)
	}
}

// TestNoteMeasurementsRespectsPauseConsent covers the same backstop for the "measure" collector, using
// NoteMeasurements directly the way the existing tier tests use ApplySync directly.
func TestNoteMeasurementsRespectsPauseConsent(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	r.hub.Mu.Lock()
	v := NewView()
	v.Targets["t1"] = connectivity.IssuedTarget{ID: "t1", Host: "h", Port: 80, Label: "l", Source: "observed"}
	r.hub.Views[id] = v
	r.hub.Mu.Unlock()

	m := &continuumv1.Measurements{Results: []*continuumv1.PathResult{{TargetId: "t1", Samples: 5, RttMinMs: 1, RttP50Ms: 2, RttP95Ms: 3}}}
	r.hub.NoteMeasurements(id, m)
	if len(v.Paths["t1"].Rounds) != 1 {
		t.Fatalf("a measurement with no pause in force should be recorded: %+v", v.Paths["t1"])
	}

	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Paused: []string{"measure"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.NoteMeasurements(id, m)
	if len(v.Paths["t1"].Rounds) != 1 {
		t.Fatalf("a paused measure collector must not record anything new: %+v", v.Paths["t1"])
	}
}

// TestConsentDocSaysWhetherTheAgentHasConfirmedANarrowing covers consentDoc's own cross-check between
// what an administrator asked for and what the agent's diagnostics say is actually in force: unconfirmed
// until a matching report arrives, confirmed once one does, and unconfirmed again (with a fresh SetAt)
// the moment the ask changes without a new report to match it - the elapsed-time signal the
// observability-intent panel needs to show a narrowing that a stuck or too-old agent never picked up.
func TestConsentDocSaysWhetherTheAgentHasConfirmedANarrowing(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)

	// Nothing narrowed yet: trivially confirmed, nothing to time.
	doc := r.hub.ConsentOf(id)
	_ = doc
	r.hub.Mu.Lock()
	v := r.hub.ViewFor(id)
	r.hub.Mu.Unlock()
	if cd := v.consentDoc(); !cd.Confirmed || cd.SetAt != "" {
		t.Fatalf("nothing narrowed should read as confirmed with no SetAt: %+v", cd)
	}

	// Narrow it: unconfirmed until the agent is heard from, and the change is timestamped.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("a fresh narrowing with no agent report yet should be unconfirmed, with a SetAt: %+v", cd)
	}

	// The agent reports back something that does not yet match (an older report, or a different pause):
	// still unconfirmed.
	r.hub.Mu.Lock()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{}}, false, *r.now)
	r.hub.Mu.Unlock()
	if cd := v.consentDoc(); cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("a report that does not yet show the pause should stay unconfirmed: %+v", cd)
	}

	// The agent catches up: confirmed, and SetAt drops off since there is nothing stale to show any more.
	r.hub.Mu.Lock()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{"flow"}}, false, *r.now)
	r.hub.Mu.Unlock()
	if cd := v.consentDoc(); !cd.Confirmed || cd.SetAt != "" {
		t.Fatalf("a report matching the ask should be confirmed with no SetAt: %+v", cd)
	}

	// Changing the ask again, with no new report yet, goes back to unconfirmed - the agent's last report
	// matched the *previous* ask, not this one.
	*r.now = r.now.Add(time.Minute)
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Paused: []string{"flow"}, Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	cd := v.consentDoc()
	if cd.Confirmed || cd.SetAt == "" {
		t.Fatalf("widening what is asked for should be unconfirmed again until the agent reports it: %+v", cd)
	}
	if cd.SetAt != kernel.RFC(*r.now) {
		t.Fatalf("SetAt should be this change's own moment, not the earlier one: got %s want %s", cd.SetAt, kernel.RFC(*r.now))
	}

	// Excluded namespaces can only be confirmed by count (the agent never reports which ones - see
	// ExcludedNamespaces's own doc comment): a report with the right paused collector and the right count
	// of excluded namespaces reads as confirmed even though this test cannot tell the diagnostic "batch" is
	// the same namespace, which is the documented, accepted limitation.
	r.hub.Mu.Lock()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", PausedCollectors: []string{"flow"}, ExcludedNamespaces: 1}, false, *r.now)
	r.hub.Mu.Unlock()
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
	id, _, _ := r.approvedAgent(t, testkit.FP) // approved at tier 2
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)

	// Nothing has ever synced: no warning, even though nothing is known yet - there is no "reality" to
	// check against, so silence here, not a false alarm about every excluded name.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Excluded: []string{"batch"}}); err != nil {
		t.Fatal(err)
	}
	r.hub.Mu.Lock()
	v := r.hub.ViewFor(id)
	r.hub.Mu.Unlock()
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 0 {
		t.Fatalf("no sync yet: nothing should be flagged: %+v", cd)
	}

	// The agent's first picture reports "shop" only. "batch" was asked to be excluded but the agent has
	// never actually seen it - a likely typo, or one that has not rolled out yet either way.
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}}}
	if _, _, err := r.hub.ApplySync(a, sy, false); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 1 || cd.UnknownNamespaces[0] != "batch" {
		t.Fatalf("batch was never reported, should be the one namespace flagged: %+v", cd)
	}

	// Widen the exclusion to also cover "shop", which the agent does report: only "batch" stays flagged -
	// this is not a blanket "something is excluded" warning, it is specific to the name that is not real.
	if _, err := r.hub.SetConsent(r.ctx, "actor", id, agents.Consent{Excluded: []string{"batch", "shop"}}); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 1 || cd.UnknownNamespaces[0] != "batch" {
		t.Fatalf("shop is real and excluded too, only batch should stay flagged: %+v", cd)
	}

	// The agent's next picture (a delta, on top of what it already reported) includes "batch" too - even
	// though it is excluded and dropConsentOverrides strips it back out before it is ever stored, the name
	// itself was seen before that filter ran, so the warning about it clears on the very next read.
	sy2 := &continuumv1.Sync{Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/batch", Name: "batch"}}}
	if _, _, err := r.hub.ApplySync(a, sy2, false); err != nil {
		t.Fatal(err)
	}
	if cd := v.consentDoc(); len(cd.UnknownNamespaces) != 0 {
		t.Fatalf("batch has now been reported, the warning should have cleared: %+v", cd)
	}
	// And dropConsentOverrides' own job is untouched by any of this: batch is still excluded from what is
	// actually stored, only the metadata used to validate the exclusion's spelling learned about it.
	if _, ok := r.hub.Views[id].State.Namespaces["ns/batch"]; ok {
		t.Fatal("batch is still excluded and should still be stripped from stored state")
	}
}

// TestKnownNamespacesOnlyLearnsFromASyncTheServerActuallyAccepted guards against a rejected sync teaching
// agents.ViewExt.knownNamespaces about namespaces that were never actually accepted: ApplySync's own doc comment
// says a sync Check refuses "changes nothing", and that has to hold for what an excluded name is later
// checked against too, not just for the stored state itself.
func TestKnownNamespacesOnlyLearnsFromASyncTheServerActuallyAccepted(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 1, Workloads: 100, Bytes: 64 << 20}
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)

	// Two namespaces against a limit of one: refused outright, nothing merged.
	sy := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: testkit.FP},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "ns/shop", Name: "shop"}, {Key: "ns/batch", Name: "batch"}}}
	if _, _, err := r.hub.ApplySync(a, sy, false); err == nil {
		t.Fatal("a sync over the namespace limit should have been refused")
	}
	r.hub.Mu.Lock()
	known := r.hub.ViewFor(id).Ext.KnownNamespaces
	r.hub.Mu.Unlock()
	if known["batch"] || known["shop"] {
		t.Fatalf("a rejected sync should not have taught knownNamespaces anything: %v", known)
	}
}

// Who may make an agent share more is decided by the caller's role, and a caller that is not known is not an administrator.
// A path that forgot to carry the signed-in user used to be trusted here; it must not be, so a new internal caller has to say who
// is asking. Narrowing never needed the right, so it still works without one.
func TestAContextWithNoSignedInCallerMayNotWiden(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"no caller", context.Background(), false},
		{"viewer", testkit.AsRole(accounts.RoleViewer), false},
		{"editor", testkit.AsRole(accounts.RoleEditor), false},
		{"administrator", testkit.AsRole(accounts.RoleAdmin), true},
	} {
		if got := accounts.Decide(accounts.CallerFrom(tc.ctx), accounts.ActChangeAccess, accounts.Resource{Widening: true}).Allowed; got != tc.want {
			t.Errorf("%s: widening allowed = %v, want %v", tc.name, got, tc.want)
		}
	}

	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	h := r.hub
	bare := context.Background()
	if _, err := h.SetConsent(bare, "nobody", id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatalf("pausing a collector narrows, so it needs no right: %v", err)
	}
	if _, err := h.SetConsent(bare, "nobody", id, agents.Consent{}); err == nil {
		t.Fatal("a caller nobody knows lifted a pause")
	}
	if _, err := h.SetConsent(testkit.AsRole(accounts.RoleEditor), "edith", id, agents.Consent{}); err == nil {
		t.Fatal("an editor lifted a pause")
	}
	if c, err := h.SetConsent(testkit.AsRole(accounts.RoleAdmin), "adam", id, agents.Consent{}); err != nil || len(c.Paused) != 0 {
		t.Fatalf("an administrator could not lift it: %+v %v", c, err)
	}
	if _, err := h.SetAgentTier(bare, "nobody", id, 3, nil); err == nil {
		t.Fatal("a caller nobody knows raised a tier")
	}
}

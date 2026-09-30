package chart

import (
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

// otelConfig decodes the "otel-collector-config.yaml" key of a telemetry collector ConfigMap into a loosely
// typed map, since asserting against the real OTel Collector config schema isn't worth a dependency here -
// these tests check what this chart's own templates put in it, not whether the OTel Collector itself would
// accept it (that's what `helm template` succeeding, plus the real docs consulted while writing the
// templates, cover).
func otelConfig(t *testing.T, cm map[string]string) map[string]any {
	t.Helper()
	raw, ok := cm["otel-collector-config.yaml"]
	if !ok {
		t.Fatal(`ConfigMap has no "otel-collector-config.yaml" key`)
	}
	var v map[string]any
	if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("otel-collector-config.yaml did not parse as YAML: %v\n%s", err, raw)
	}
	return v
}

func TestTelemetryRBACHasNoNodesProxy(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.nodeRuntime.metrics.enabled=true")
	cr, ok := r.clusterroles["continuum-agent-telemetry"]
	if !ok {
		t.Fatal("no continuum-agent-telemetry ClusterRole rendered")
	}
	for _, rule := range cr.Rules {
		for _, res := range rule.Resources {
			if res == "nodes/proxy" {
				t.Errorf("telemetry ClusterRole grants nodes/proxy, which kubelet_stats never uses (it hits the kubelet directly): %+v", rule)
			}
		}
	}
}

func TestTelemetrySchemaRejectsScopeSelector(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set-string", "telemetry.scope.selector=app=foo")
	if err == nil {
		t.Fatalf("telemetry.scope.selector should fail the release, rendered instead:\n%s", out)
	}
	if !strings.Contains(out, "telemetry.scope.selector is not implemented") {
		t.Errorf("wrong error for telemetry.scope.selector: %s", out)
	}
}

func TestTelemetryOpampRequiresEndpoint(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.opamp.enabled=true")
	if err == nil {
		t.Fatalf("telemetry.opamp.enabled without a server endpoint should fail, rendered instead:\n%s", out)
	}
	if !strings.Contains(out, "telemetry.opamp.server.endpoint") {
		t.Errorf("wrong error for telemetry.opamp with no endpoint: %s", out)
	}
}

func TestTelemetryOpampOffByDefaultOnWhenEnabled(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cm := r.configmaps["continuum-telemetry-host-config"]
	cfg := otelConfig(t, cm.Data)
	if _, ok := cfg["extensions"]; ok {
		t.Errorf("extensions present when telemetry.opamp.enabled is false (default): %v", cfg["extensions"])
	}
	if svc, ok := cfg["service"].(map[string]any); ok {
		if _, ok := svc["extensions"]; ok {
			t.Errorf("service.extensions present when telemetry.opamp.enabled is false (default): %v", svc["extensions"])
		}
	}

	r = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.opamp.enabled=true", "--set", "telemetry.opamp.server.endpoint=wss://opamp.example.com:4320/v1/opamp")
	cm = r.configmaps["continuum-telemetry-host-config"]
	cfg = otelConfig(t, cm.Data)
	ext, ok := cfg["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("extensions missing when telemetry.opamp.enabled is true: %v", cfg["extensions"])
	}
	opamp, ok := ext["opamp"].(map[string]any)
	if !ok {
		t.Fatalf("extensions.opamp missing: %v", ext)
	}
	server, _ := opamp["server"].(map[string]any)
	ws, _ := server["ws"].(map[string]any)
	if ws["endpoint"] != "wss://opamp.example.com:4320/v1/opamp" {
		t.Errorf("opamp server.ws.endpoint = %v", ws["endpoint"])
	}
	svc, _ := cfg["service"].(map[string]any)
	exts, _ := svc["extensions"].([]any)
	if len(exts) != 1 || exts[0] != "opamp" {
		t.Errorf("service.extensions = %v, want [opamp]", svc["extensions"])
	}
}

func TestTelemetryScopeSplitsAppAndInfraPipelines(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["filter/scope"]; !ok {
		t.Fatalf("filter/scope processor missing when telemetry.scope.namespaces is set: %v", procs)
	}
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)

	infra, ok := pipelines["metrics/infra"].(map[string]any)
	if !ok {
		t.Fatal("metrics/infra pipeline missing")
	}
	if procList, _ := infra["processors"].([]any); containsAny(procList, "filter/scope") {
		t.Errorf("metrics/infra must never carry the scope filter, got %v", procList)
	}

	app, ok := pipelines["metrics/app"].(map[string]any)
	if !ok {
		t.Fatal("metrics/app pipeline missing")
	}
	if procList, _ := app["processors"].([]any); !containsAny(procList, "filter/scope") {
		t.Errorf("metrics/app must carry the scope filter, got %v", procList)
	}

	logsInfra, ok := pipelines["logs/infra"].(map[string]any)
	if !ok {
		t.Fatal("logs/infra pipeline missing")
	}
	if procList, _ := logsInfra["processors"].([]any); containsAny(procList, "filter/scope") {
		t.Errorf("logs/infra must never carry the scope filter, got %v", procList)
	}
	logsApp, ok := pipelines["logs/app"].(map[string]any)
	if !ok {
		t.Fatal("logs/app pipeline missing")
	}
	if procList, _ := logsApp["processors"].([]any); !containsAny(procList, "filter/scope") {
		t.Errorf("logs/app must carry the scope filter, got %v", procList)
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestTelemetryPerWorkloadResourcesOverrideAndFallBack(t *testing.T) {
	// No override: both workloads fall back to the shared default.
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler")
	hostReq := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0].Resources.Requests
	keplerReq := r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec.Containers[0].Resources.Requests
	if hostReq.Cpu().String() != "20m" {
		t.Errorf("host collector should fall back to telemetry.resources (20m), got %v", hostReq)
	}
	if keplerReq.Cpu().String() != "20m" {
		t.Errorf("kepler should fall back to telemetry.resources (20m), got %v", keplerReq)
	}

	// Override: each workload gets its own, and they don't leak into each other.
	r = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler",
		"--set", "telemetry.hostCollector.resources.requests.cpu=111m",
		"--set", "telemetry.energy.metrics.resources.requests.cpu=222m")
	hostReq = r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0].Resources.Requests
	keplerReq = r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec.Containers[0].Resources.Requests
	if hostReq.Cpu().String() != "111m" {
		t.Errorf("host collector override = %v, want 111m", hostReq)
	}
	if keplerReq.Cpu().String() != "222m" {
		t.Errorf("kepler override = %v, want 222m", keplerReq)
	}
}

func TestTelemetrySignalsEnvVarMatchesEnabledSignals(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler",
		"--set", "telemetry.traces.traces.enabled=true")
	c := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	v, ok := env(c, "CONTINUUM_TELEMETRY_SIGNALS")
	if !ok {
		t.Fatal("CONTINUUM_TELEMETRY_SIGNALS not set on the agent container")
	}
	if v != "energy,traces" {
		t.Errorf("CONTINUUM_TELEMETRY_SIGNALS = %q, want %q", v, "energy,traces")
	}

	// No telemetry signal enabled: the env var must be entirely absent, not present-and-empty.
	r = render(t)
	c = r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	if _, ok := env(c, "CONTINUUM_TELEMETRY_SIGNALS"); ok {
		t.Error("CONTINUUM_TELEMETRY_SIGNALS should be entirely absent when no telemetry signal is enabled")
	}
}

func TestTelemetryKubeletStatsUsesCanonicalReceiverName(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cm := r.configmaps["continuum-telemetry-host-config"]
	cfg := otelConfig(t, cm.Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	if _, ok := receivers["kubelet_stats"]; !ok {
		t.Errorf("receivers should use the canonical name kubelet_stats, got keys %v", receivers)
	}
	if _, ok := receivers["kubeletstats"]; ok {
		t.Error("the deprecated kubeletstats key should not appear")
	}
}

func TestTelemetryMemoryLimiterAlwaysFirstAndRedactionOnByDefault(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cm := r.configmaps["continuum-telemetry-host-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["memory_limiter"]; !ok {
		t.Fatalf("memory_limiter must always be rendered: %v", procs)
	}
	if _, ok := procs["redaction"]; !ok {
		t.Errorf("redaction must be on by default: %v", procs)
	}
	if _, ok := procs["resourcedetection"]; ok {
		t.Errorf("resourcedetection must be off by default: %v", procs)
	}
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	metrics, _ := pipelines["metrics"].(map[string]any)
	procList, _ := metrics["processors"].([]any)
	if len(procList) == 0 || procList[0] != "memory_limiter" {
		t.Errorf("memory_limiter must be first in the pipeline's processor list, got %v", procList)
	}
	if procList[len(procList)-1] != "batch" {
		t.Errorf("batch must be last in the pipeline's processor list, got %v", procList)
	}
}

func TestTelemetryProcessorOrderMatchesSpec(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.processors.resourceDetection.enabled=true",
		"--set", "telemetry.processors.tracesSampling.percentage=50",
		"--set", "telemetry.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	traces, ok := pipelines["traces"].(map[string]any)
	if !ok {
		t.Fatal("traces pipeline missing")
	}
	got, _ := traces["processors"].([]any)
	want := []any{"memory_limiter", "k8sattributes", "resourcedetection", "redaction", "filter/scope", "probabilistic_sampler", "batch"}
	if len(got) != len(want) {
		t.Fatalf("traces processors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("traces processors[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestTelemetryGomemlimitParsesResourceQuantities(t *testing.T) {
	cases := []struct {
		limit string
		want  string
	}{
		{"256Mi", "214748365"},
		{"1Gi", "858993459"},
		{"500M", "400000000"},
		{"500k", "400000"},
		{"1000000", "800000"},
	}
	for _, c := range cases {
		r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
			"--set", "telemetry.hostCollector.resources.limits.memory="+c.limit)
		cont := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]
		got, ok := env(cont, "GOMEMLIMIT")
		if !ok {
			t.Errorf("limit %q: GOMEMLIMIT not set", c.limit)
			continue
		}
		if got != c.want {
			t.Errorf("limit %q: GOMEMLIMIT = %q, want %q", c.limit, got, c.want)
		}
	}

	// No memory limit at all: GOMEMLIMIT must be entirely absent, not present-and-empty.
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.hostCollector.resources.requests.cpu=20m", "--set", "telemetry.hostCollector.resources.limits=null")
	cont := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]
	if _, ok := env(cont, "GOMEMLIMIT"); ok {
		t.Error("GOMEMLIMIT should be absent when there is no memory limit to compute it from")
	}
}

func TestTelemetryEgressPolicyOffByDefaultAndRequiresAllowedEgress(t *testing.T) {
	if _, ok := render(t).policies["continuum-telemetry-egress"]; ok {
		t.Fatal("the telemetry egress policy must be off by default")
	}
	if out, err := helmTemplate(t, "--set", "networkPolicy.telemetryEgress.enabled=true"); err == nil || !strings.Contains(out, "allowedEgress") {
		t.Errorf("enabling telemetry egress without allowedEgress must fail with a message, got err=%v\n%s", err, out)
	}
	p, ok := render(t,
		"--set", "networkPolicy.telemetryEgress.enabled=true",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317",
	).policies["continuum-telemetry-egress"]
	if !ok {
		t.Fatal("no telemetry egress policy rendered")
	}
	if len(p.Spec.Egress) != 2 {
		t.Fatalf("policy egress rules = %+v, want 2 (DNS + the configured destination)", p.Spec.Egress)
	}
	dest := p.Spec.Egress[1]
	if len(dest.To) != 1 || dest.To[0].IPBlock == nil || dest.To[0].IPBlock.CIDR != "203.0.113.7/32" {
		t.Errorf("destination rule = %+v", dest)
	}
}

func TestTelemetryReceiverAuthWiresExtensionAndOtlpAuth(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.applicationMetrics.metrics.enabled=true")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	if _, ok := cfg["extensions"]; ok {
		t.Errorf("extensions present when telemetry.receiver.auth.enabled is false (default): %v", cfg["extensions"])
	}

	r = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.receiver.auth.enabled=true", "--set", "telemetry.receiver.auth.secretName=tok")
	cm = r.configmaps["continuum-telemetry-cluster-config"]
	cfg = otelConfig(t, cm.Data)
	ext, ok := cfg["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("extensions missing when telemetry.receiver.auth.enabled is true: %v", cfg["extensions"])
	}
	if _, ok := ext["bearertokenauth"]; !ok {
		t.Errorf("extensions.bearertokenauth missing: %v", ext)
	}
	receivers, _ := cfg["receivers"].(map[string]any)
	otlp, _ := receivers["otlp"].(map[string]any)
	protocols, _ := otlp["protocols"].(map[string]any)
	grpcProto, _ := protocols["grpc"].(map[string]any)
	auth, _ := grpcProto["auth"].(map[string]any)
	if auth["authenticator"] != "bearertokenauth" {
		t.Errorf("otlp grpc protocol auth.authenticator = %v, want bearertokenauth", auth)
	}
	cont := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]
	if _, ok := env(cont, "CONTINUUM_TELEMETRY_RECEIVER_AUTH"); !ok {
		t.Error("CONTINUUM_TELEMETRY_RECEIVER_AUTH env var not set on the cluster collector container")
	}
}

func TestTelemetryReceiverNetworkPolicyOffByDefaultAndRequiresAllowedIngress(t *testing.T) {
	if _, ok := render(t).policies["continuum-telemetry-receiver-ingress"]; ok {
		t.Fatal("the receiver ingress policy must be off by default")
	}
	if out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.receiver.networkPolicy.enabled=true"); err == nil || !strings.Contains(out, "allowedIngress") {
		t.Errorf("enabling the receiver NetworkPolicy without allowedIngress must fail with a message, got err=%v\n%s", err, out)
	}
	p, ok := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.receiver.networkPolicy.enabled=true",
		"--set-string", "telemetry.receiver.networkPolicy.allowedIngress[0].namespaceSelector.matchLabels.team=shop",
	).policies["continuum-telemetry-receiver-ingress"]
	if !ok {
		t.Fatal("no receiver ingress policy rendered")
	}
	if len(p.Spec.Ingress) != 1 || len(p.Spec.Ingress[0].Ports) != 2 {
		t.Fatalf("policy ingress = %+v", p.Spec.Ingress)
	}
}

func TestTelemetryExtraProcessorsEscapeHatch(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.traces.traces.enabled=true",
		"--set-json", `telemetry.processors.extraProcessors={"attributes/drop_x":{"actions":[{"key":"x","action":"delete"}]}}`,
		"--set", "telemetry.processors.extraProcessorNames[0]=attributes/drop_x")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["attributes/drop_x"]; !ok {
		t.Fatalf("extraProcessors entry not merged into processors: %v", procs)
	}
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	traces, _ := pipelines["traces"].(map[string]any)
	procList, _ := traces["processors"].([]any)
	if !containsAny(procList, "attributes/drop_x") {
		t.Errorf("extraProcessorNames entry not referenced in the traces pipeline: %v", procList)
	}
	if procList[len(procList)-1] != "batch" {
		t.Errorf("batch must stay last even with an extra processor added, got %v", procList)
	}
}

// tail_sampling only implements the traces processor interface - referencing it from a metrics or logs
// pipeline makes the collector refuse to start. extraTracesProcessorNames exists precisely so a traces-only
// extra never lands in extraProcessorNames' shared, every-pipeline slot; this both proves it reaches the
// traces pipeline and guards against a regression that would put it back in a metrics/logs one too.
func TestTelemetryExtraTracesProcessorNamesOnlyInTracesPipeline(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.traces.traces.enabled=true",
		"--set-json", `telemetry.processors.extraProcessors={"tail_sampling":{"decision_wait":"10s","policies":[{"name":"errors","type":"status_code","status_code":{"status_codes":["ERROR"]}}]}}`,
		"--set", "telemetry.processors.extraTracesProcessorNames[0]=tail_sampling")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["tail_sampling"]; !ok {
		t.Fatalf("extraTracesProcessorNames entry not merged into processors: %v", procs)
	}
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)

	traces, _ := pipelines["traces"].(map[string]any)
	tracesProcs, _ := traces["processors"].([]any)
	if !containsAny(tracesProcs, "tail_sampling") {
		t.Errorf("tail_sampling not referenced in the traces pipeline: %v", tracesProcs)
	}
	if tracesProcs[len(tracesProcs)-1] != "batch" {
		t.Errorf("batch must stay last even with a traces-only extra added, got %v", tracesProcs)
	}

	for _, name := range []string{"metrics/app", "metrics/infra", "logs/app", "logs/infra"} {
		pl, ok := pipelines[name].(map[string]any)
		if !ok {
			continue
		}
		plProcs, _ := pl["processors"].([]any)
		if containsAny(plProcs, "tail_sampling") {
			t.Errorf("tail_sampling must never appear in %s (traces-only processor), got %v", name, plProcs)
		}
	}
}

func TestTelemetryAcceleratorsBundleDcgmRendersDaemonSetWithNarrowSecurityContext(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm")
	ds, ok := r.daemonsets["continuum-telemetry-dcgm"]
	if !ok {
		t.Fatal("no continuum-telemetry-dcgm DaemonSet rendered")
	}
	c := ds.Spec.Template.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
		t.Errorf("dcgm-exporter must not run privileged (unlike Kepler), got %+v", c.SecurityContext)
	}
	if c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 0 {
		t.Errorf("dcgm-exporter should runAsUser 0, got %+v", c.SecurityContext.RunAsUser)
	}
	foundSysAdmin := false
	if c.SecurityContext.Capabilities != nil {
		for _, cap := range c.SecurityContext.Capabilities.Add {
			if cap == "SYS_ADMIN" {
				foundSysAdmin = true
			}
		}
	}
	if !foundSysAdmin {
		t.Errorf("dcgm-exporter should add SYS_ADMIN, got %+v", c.SecurityContext.Capabilities)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9400 {
		t.Errorf("dcgm-exporter should expose port 9400, got %+v", c.Ports)
	}
	if ds.Spec.Template.Spec.HostPID {
		t.Error("dcgm-exporter should not need hostPID (unlike Kepler)")
	}
}

func TestTelemetryAcceleratorsNoNewRBAC(t *testing.T) {
	// Compare against another already-enabled infra signal (energy/Kepler), not a fully-disabled baseline:
	// the telemetry ClusterRole always carries the k8sattributes rule once ANY signal is on (see
	// agent.telemetryK8sAttrsEnabled) - what this test isolates is whether accelerators specifically adds
	// anything beyond that, which it should not (dcgm-exporter reads GPU hardware and the kubelet
	// pod-resources socket directly, never the Kubernetes API, exactly like Kepler).
	withEnergy := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler").clusterroles["continuum-agent-telemetry"]
	withEnergyAndAccel := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm").clusterroles["continuum-agent-telemetry"]
	if len(withEnergy.Rules) != len(withEnergyAndAccel.Rules) {
		t.Errorf("enabling telemetry.accelerators changed the telemetry ClusterRole's rule count: %d -> %d", len(withEnergy.Rules), len(withEnergyAndAccel.Rules))
	}
}

func TestTelemetryAcceleratorsScrapeJobIsolatedFromEnergy(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	prom, _ := receivers["prometheus/infra"].(map[string]any)
	config, _ := prom["config"].(map[string]any)
	scrapeConfigs, _ := config["scrape_configs"].([]any)
	var jobs []string
	for _, sc := range scrapeConfigs {
		m, _ := sc.(map[string]any)
		jobs = append(jobs, m["job_name"].(string))
	}
	if len(jobs) != 1 || jobs[0] != "dcgm-exporter" {
		t.Errorf("expected only the dcgm-exporter scrape job with energy disabled, got %v (energy.metrics.source defaults to bundle-kepler, which must not leak in when energy itself is off)", jobs)
	}
}

func TestTelemetryAcceleratorsSignalNameAndExistingEndpointValidation(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm")
	c := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	if v, ok := env(c, "CONTINUUM_TELEMETRY_SIGNALS"); !ok || v != "accelerators" {
		t.Errorf("CONTINUUM_TELEMETRY_SIGNALS = %q, ok=%v, want \"accelerators\"", v, ok)
	}

	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing")
	if err == nil || !strings.Contains(out, "telemetry.accelerators.metrics.existing.prometheusEndpoint") {
		t.Errorf("telemetry.accelerators.metrics.source=existing without an endpoint must fail with a message, got err=%v\n%s", err, out)
	}
}

func TestTelemetryCollectorsUseOwnServiceAccountIsolatedFromAccessTier(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "access.tier=2")

	host := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	cluster := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec
	if host.ServiceAccountName != "continuum-agent-telemetry" {
		t.Errorf("host collector ServiceAccountName = %q, want continuum-agent-telemetry", host.ServiceAccountName)
	}
	if cluster.ServiceAccountName != "continuum-agent-telemetry" {
		t.Errorf("cluster collector ServiceAccountName = %q, want continuum-agent-telemetry", cluster.ServiceAccountName)
	}
	// The discovery agent's own Deployment must be unaffected: still its own ServiceAccount, not the
	// telemetry one and vice versa - the whole point is that these are two separate identities now.
	if agentSA := r.deployments["continuum-agent"].Spec.Template.Spec.ServiceAccountName; agentSA != "continuum-agent" {
		t.Errorf("discovery agent ServiceAccountName = %q, want continuum-agent (unaffected by telemetry)", agentSA)
	}

	if _, ok := r.serviceaccounts["continuum-agent-telemetry"]; !ok {
		t.Fatal("expected a dedicated continuum-agent-telemetry ServiceAccount to be rendered")
	}

	binding, ok := r.clusterrolebindings["continuum-agent-telemetry-default"]
	if !ok {
		t.Fatal("expected the telemetry ClusterRoleBinding continuum-agent-telemetry-default")
	}
	if binding.RoleRef.Name != "continuum-agent-telemetry" {
		t.Errorf("telemetry ClusterRoleBinding roleRef = %q, want continuum-agent-telemetry", binding.RoleRef.Name)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != "continuum-agent-telemetry" {
		t.Errorf("telemetry ClusterRoleBinding subjects = %+v, want exactly [continuum-agent-telemetry]", binding.Subjects)
	}

	// Isolation, the actual point of this change: with access.tier=2 the discovery agent holds t0/t1/t2
	// ClusterRoleBindings too - none of THOSE may name the telemetry ServiceAccount as a subject, or the
	// split would be cosmetic rather than a real credential boundary.
	for name, b := range r.clusterrolebindings {
		if name == "continuum-agent-telemetry-default" {
			continue
		}
		for _, subj := range b.Subjects {
			if subj.Name == "continuum-agent-telemetry" {
				t.Errorf("ClusterRoleBinding %q (roleRef %q) must not bind the telemetry ServiceAccount - that would leak access.tier permissions into the telemetry identity", name, b.RoleRef.Name)
			}
		}
	}
}

// -----------------------------------------------------------------------------------------------------
// Per-kind application scope overrides (telemetry.<kind>.scope) and the networkLatency exemption.
// -----------------------------------------------------------------------------------------------------

func TestTelemetryApplicationScopeOverrideFallsBackToGlobal(t *testing.T) {
	// No per-kind override set anywhere: every app-domain pipeline must still carry the exact same shared
	// "filter/scope" processor as before this change - this is the zero-regression guarantee for every
	// existing install, which never sets telemetry.<kind>.scope.
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["filter/scope"]; !ok {
		t.Fatalf("filter/scope processor missing: %v", procs)
	}
	for _, unwanted := range []string{"filter/scope_applicationMetrics", "filter/scope_applicationLogs", "filter/scope_traces"} {
		if _, ok := procs[unwanted]; ok {
			t.Errorf("%s should not render when no per-kind override is set", unwanted)
		}
	}
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	for _, name := range []string{"metrics/app", "logs/app", "traces"} {
		p, ok := pipelines[name].(map[string]any)
		if !ok {
			t.Fatalf("%s pipeline missing", name)
		}
		procList, _ := p["processors"].([]any)
		if !containsAny(procList, "filter/scope") {
			t.Errorf("%s must carry the shared filter/scope, got %v", name, procList)
		}
	}
}

func TestTelemetryApplicationMetricsScopeOverrideUsesOwnProcessor(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=payments",
		"--set", "telemetry.applicationMetrics.metrics.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	amProc, ok := procs["filter/scope_applicationMetrics"].(map[string]any)
	if !ok {
		t.Fatalf("filter/scope_applicationMetrics missing: %v", procs)
	}
	conds, _ := amProc["metric_conditions"].([]any)
	if len(conds) != 1 || !strings.Contains(conds[0].(string), `"^(shop)$"`) {
		t.Errorf("filter/scope_applicationMetrics should use its own override (shop), got %v", conds)
	}
	// The shared filter/scope must still exist (logs/app and traces still use it, with the global scope).
	shared, ok := procs["filter/scope"].(map[string]any)
	if !ok {
		t.Fatalf("filter/scope missing: %v", procs)
	}
	sharedConds, _ := shared["log_conditions"].([]any)
	if len(sharedConds) != 1 || !strings.Contains(sharedConds[0].(string), `"^(payments)$"`) {
		t.Errorf("filter/scope should still use the global scope (payments), got %v", sharedConds)
	}

	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	app, _ := pipelines["metrics/app"].(map[string]any)
	appProcs, _ := app["processors"].([]any)
	if !containsAny(appProcs, "filter/scope_applicationMetrics") {
		t.Errorf("metrics/app must use filter/scope_applicationMetrics, got %v", appProcs)
	}
	if containsAny(appProcs, "filter/scope") {
		t.Errorf("metrics/app must not also carry the shared filter/scope, got %v", appProcs)
	}
	logsApp, _ := pipelines["logs/app"].(map[string]any)
	logsAppProcs, _ := logsApp["processors"].([]any)
	if !containsAny(logsAppProcs, "filter/scope") {
		t.Errorf("logs/app (no override) must still use the shared filter/scope, got %v", logsAppProcs)
	}
}

func TestTelemetryNetworkLatencyExemptFromApplicationScopeFilter(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.networkLatency.metrics.enabled=true",
		"--set", "measurements.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	shared, ok := procs["filter/scope"].(map[string]any)
	if !ok {
		t.Fatalf("filter/scope missing: %v", procs)
	}
	conds, _ := shared["metric_conditions"].([]any)
	if len(conds) != 1 || !strings.Contains(conds[0].(string), `service.name"] != "continuum-network-latency"`) {
		t.Errorf("filter/scope should exempt continuum-network-latency when networkLatency is enabled, got %v", conds)
	}
}

func TestTelemetryNoNetworkLatencyExemptionWhenSignalDisabled(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=shop",
	)
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	shared, ok := procs["filter/scope"].(map[string]any)
	if !ok {
		t.Fatalf("filter/scope missing: %v", procs)
	}
	conds, _ := shared["metric_conditions"].([]any)
	if len(conds) != 1 || strings.Contains(conds[0].(string), "continuum-network-latency") {
		t.Errorf("filter/scope should carry no networkLatency exemption clause when the signal is off, got %v", conds)
	}
}

// -----------------------------------------------------------------------------------------------------
// Accelerators applyScope: off-by-default GPU-metrics namespace scoping.
// -----------------------------------------------------------------------------------------------------

func TestTelemetryAcceleratorsApplyScopeOffByDefaultRendersIdenticalConfig(t *testing.T) {
	// Explicit zero-regression check: enabling accelerators without touching applyScope must render
	// exactly as it did before this feature existed - no transform, no new filter, no new env vars.
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm",
		"--set", "telemetry.scope.namespaces[0]=shop")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	for _, unwanted := range []string{"transform/dcgm_pod", "filter/scope_accelerators"} {
		if _, ok := procs[unwanted]; ok {
			t.Errorf("%s should not render when applyScope is off (default)", unwanted)
		}
	}
	ds, ok := r.daemonsets["continuum-telemetry-dcgm"]
	if !ok {
		t.Fatal("no continuum-telemetry-dcgm DaemonSet rendered")
	}
	if env := ds.Spec.Template.Spec.Containers[0].Env; len(env) != 0 {
		t.Errorf("dcgm-exporter should have no env vars when applyScope is off, got %+v", env)
	}
}

func TestTelemetryAcceleratorsApplyScopeAddsTransformAndScopedFilter(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm",
		"--set", "telemetry.accelerators.metrics.applyScope=true",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler",
		"--set", "telemetry.scope.namespaces[0]=shop")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)

	if _, ok := procs["transform/dcgm_pod"]; !ok {
		t.Fatalf("transform/dcgm_pod missing when applyScope is on: %v", procs)
	}
	// Kepler's own transform must be untouched - accelerators' scoping must not interfere with it.
	if _, ok := procs["transform/kepler_node"]; !ok {
		t.Errorf("transform/kepler_node should still render alongside transform/dcgm_pod")
	}

	accFilter, ok := procs["filter/scope_accelerators"].(map[string]any)
	if !ok {
		t.Fatalf("filter/scope_accelerators missing when applyScope is on: %v", procs)
	}
	conds, _ := accFilter["metric_conditions"].([]any)
	if len(conds) != 1 {
		t.Fatalf("filter/scope_accelerators should have exactly one condition, got %v", conds)
	}
	cond := conds[0].(string)
	if !strings.Contains(cond, `"^(shop)$"`) {
		t.Errorf("filter/scope_accelerators should use the global scope (shop), got %q", cond)
	}
	if !strings.Contains(cond, `service.name"] == "dcgm-exporter"`) {
		t.Errorf("filter/scope_accelerators must gate on service.name == dcgm-exporter so it never touches Kepler's records, got %q", cond)
	}

	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	infra, _ := pipelines["metrics/infra"].(map[string]any)
	infraProcs, _ := infra["processors"].([]any)
	if !containsAny(infraProcs, "transform/dcgm_pod") || !containsAny(infraProcs, "filter/scope_accelerators") {
		t.Errorf("metrics/infra must carry both transform/dcgm_pod and filter/scope_accelerators, got %v", infraProcs)
	}
}

func TestTelemetryAcceleratorsApplyScopeSetsDcgmEnvVars(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm",
		"--set", "telemetry.accelerators.metrics.applyScope=true")
	ds, ok := r.daemonsets["continuum-telemetry-dcgm"]
	if !ok {
		t.Fatal("no continuum-telemetry-dcgm DaemonSet rendered")
	}
	env := map[string]string{}
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["DCGM_EXPORTER_KUBERNETES"] != "true" {
		t.Errorf("DCGM_EXPORTER_KUBERNETES = %q, want true", env["DCGM_EXPORTER_KUBERNETES"])
	}
	if env["DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"] != "true" {
		t.Errorf("DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS = %q, want true", env["DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"])
	}
}

func TestTelemetryAcceleratorsApplyScopeNoopWithoutGlobalScope(t *testing.T) {
	// applyScope with no telemetry.scope set at all: nothing to filter by, so no filter/scope_accelerators
	// should render (an empty metric_conditions list would be a no-op filter, not worth defining).
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm",
		"--set", "telemetry.accelerators.metrics.applyScope=true")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["filter/scope_accelerators"]; ok {
		t.Error("filter/scope_accelerators should not render when telemetry.scope is empty")
	}
	if _, ok := procs["transform/dcgm_pod"]; !ok {
		t.Error("transform/dcgm_pod should still render (pod-identity enrichment doesn't depend on scope being set)")
	}
}

func TestTelemetryExporterMTLSWiresCertAndKey(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=collector.example:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=op-export-mtls")

	for _, cm := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cfg := otelConfig(t, r.configmaps[cm].Data)
		exporters, _ := cfg["exporters"].(map[string]any)
		otlp, _ := exporters["otlp"].(map[string]any)
		tls, _ := otlp["tls"].(map[string]any)
		if tls["cert_file"] != "/export-mtls/tls.crt" || tls["key_file"] != "/export-mtls/tls.key" || tls["ca_file"] != "/export-mtls/ca.crt" {
			t.Fatalf("%s exporter tls = %+v", cm, tls)
		}
	}

	host := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	cluster := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec

	foundHostMount, foundHostVol := false, false
	for _, m := range host.Containers[0].VolumeMounts {
		if m.Name == "export-mtls" && m.MountPath == "/export-mtls" && m.ReadOnly {
			foundHostMount = true
		}
	}
	for _, v := range host.Volumes {
		if v.Name == "export-mtls" && v.Secret != nil && v.Secret.SecretName == "op-export-mtls" {
			foundHostVol = true
		}
	}
	if !foundHostMount || !foundHostVol {
		t.Fatalf("host collector export-mtls volume/mount missing: mounts=%+v volumes=%+v", host.Containers[0].VolumeMounts, host.Volumes)
	}

	foundClusterMount, foundClusterVol := false, false
	for _, m := range cluster.Containers[0].VolumeMounts {
		if m.Name == "export-mtls" && m.MountPath == "/export-mtls" && m.ReadOnly {
			foundClusterMount = true
		}
	}
	for _, v := range cluster.Volumes {
		if v.Name == "export-mtls" && v.Secret != nil && v.Secret.SecretName == "op-export-mtls" {
			foundClusterVol = true
		}
	}
	if !foundClusterMount || !foundClusterVol {
		t.Fatalf("cluster collector export-mtls volume/mount missing: mounts=%+v volumes=%+v", cluster.Containers[0].VolumeMounts, cluster.Volumes)
	}
}

func TestTelemetryExporterMTLSRequiresSecretName(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.export.otlp.tls.mtls.enabled=true")
	if err == nil {
		t.Fatalf("telemetry.export.otlp.tls.mtls.enabled without a secretName should fail, rendered instead:\n%s", out)
	}
	if !strings.Contains(out, "telemetry.export.otlp.tls.mtls.secretName") {
		t.Errorf("wrong error for mtls with no secretName: %s", out)
	}
}

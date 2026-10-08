package chart

import (
	"fmt"
	"regexp"
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
	// health_check is always there (the pod's probes ask it); opamp is not.
	if ext, _ := cfg["extensions"].(map[string]any); len(ext) != 1 || ext["health_check"] == nil {
		t.Errorf("extensions = %v when telemetry.opamp.enabled is false (default), want only health_check", cfg["extensions"])
	}
	if svc, ok := cfg["service"].(map[string]any); ok {
		if exts, _ := svc["extensions"].([]any); len(exts) != 1 || exts[0] != "health_check" {
			t.Errorf("service.extensions = %v when telemetry.opamp.enabled is false (default), want [health_check]", svc["extensions"])
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
	if len(exts) != 2 || exts[0] != "health_check" || exts[1] != "opamp" {
		t.Errorf("service.extensions = %v, want [health_check opamp]", svc["extensions"])
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
	// Kepler has a profile of its own (upstream's manifest asks for 400Mi / 100m; the collectors' shared 64Mi / 20m is far
	// below what it needs) - it falls back to the shared default only when its own is emptied.
	if keplerReq.Cpu().String() != "100m" || keplerReq.Memory().String() != "400Mi" {
		t.Errorf("kepler default requests = %v, want its own 100m / 400Mi", keplerReq)
	}
	r = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true",
		"--set", "telemetry.energy.metrics.source=bundle-kepler", "--set", "telemetry.energy.metrics.resources=null")
	if got := r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "100m" {
		// null removes the key in Helm; agent.telemetryDefaults then restores the chart's own default for it.
		t.Errorf("kepler with its own resources unset should get the chart default (100m), got %v", got)
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

// TestTelemetryEffectiveConfigEnvVars covers the three things telemetry-intent.md names as missing from the
// agent's self-report: the effective export destination, the effective processor settings (redaction,
// resourcedetection, traces sampling), and which source backs energy/accelerators - rendered as siblings of
// CONTINUUM_TELEMETRY_SIGNALS on the agent container, not the collector's own ConfigMap, so the agent can
// report them in Diagnostics the same way it already reports CONTINUUM_TIER.
func TestTelemetryEffectiveConfigEnvVars(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=otel-collector.example:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing",
		"--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=dcgm.example:9400",
		"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.processors.tracesSampling.percentage=20",
		"--set", "telemetry.processors.resourceDetection.enabled=true",
	)
	c := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	want := map[string]string{
		"CONTINUUM_TELEMETRY_DESTINATION":         "otel-collector.example:4317",
		"CONTINUUM_TELEMETRY_REDACTION":           "true", // on by default
		"CONTINUUM_TELEMETRY_RESOURCE_DETECTION":  "true",
		"CONTINUUM_TELEMETRY_TRACES_SAMPLING":     "20",
		"CONTINUUM_TELEMETRY_ENERGY_SOURCE":       "bundle-kepler",
		"CONTINUUM_TELEMETRY_ACCELERATORS_SOURCE": "existing",
	}
	for name, wantVal := range want {
		v, ok := env(c, name)
		if !ok {
			t.Errorf("%s not set on the agent container", name)
			continue
		}
		if v != wantVal {
			t.Errorf("%s = %q, want %q", name, v, wantVal)
		}
	}

	// Redaction turned off explicitly: the env var must follow, not stay stuck at the chart's own default.
	r2 := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.processors.redaction.enabled=false")
	c2 := r2.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	if v, ok := env(c2, "CONTINUUM_TELEMETRY_REDACTION"); !ok || v != "false" {
		t.Errorf("CONTINUUM_TELEMETRY_REDACTION = %q, ok=%v, want \"false\"", v, ok)
	}
	// Neither traces nor energy nor accelerators is enabled here, so their own env vars must be absent too.
	for _, name := range []string{"CONTINUUM_TELEMETRY_TRACES_SAMPLING", "CONTINUUM_TELEMETRY_ENERGY_SOURCE", "CONTINUUM_TELEMETRY_ACCELERATORS_SOURCE"} {
		if _, ok := env(c2, name); ok {
			t.Errorf("%s should be absent when its own signal is not enabled", name)
		}
	}

	// No telemetry signal enabled at all: the whole family of env vars is entirely absent, matching
	// CONTINUUM_TELEMETRY_SIGNALS's own "absent, not present-and-empty" contract.
	r3 := render(t)
	c3 := r3.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	for name := range want {
		if _, ok := env(c3, name); ok {
			t.Errorf("%s should be entirely absent when no telemetry signal is enabled", name)
		}
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
	// The scope filter right after k8sattributes: redaction and detection then only ever work on what is kept, and no
	// redaction pattern can alter the namespace the scope reads.
	want := []any{"memory_limiter", "k8sattributes", "filter/scope", "resourcedetection", "redaction", "probabilistic_sampler", "resource/continuum", "batch"}
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
	if ext, _ := cfg["extensions"].(map[string]any); ext["bearertokenauth"] != nil {
		t.Errorf("bearertokenauth present when telemetry.receiver.auth.enabled is false (default): %v", cfg["extensions"])
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
	// Everything but SYS_ADMIN itself should be as narrow as every other container in this chart:
	// nothing extra in the capability bounding set, and no privilege-escalation path.
	if c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("dcgm-exporter should drop ALL before adding SYS_ADMIN back, got %+v", c.SecurityContext.Capabilities)
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("dcgm-exporter should set allowPrivilegeEscalation: false, got %+v", c.SecurityContext.AllowPrivilegeEscalation)
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
		"--set", "telemetry.scope.namespaces[0]=shop", "--set", "telemetry.accelerators.metrics.applyScope=false")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	for _, unwanted := range []string{"transform/dcgm_pod", "filter/scope_accelerators"} {
		if _, ok := procs[unwanted]; ok {
			t.Errorf("%s should not render when applyScope is off (default)", unwanted)
		}
	}
	if _, ok := procs["transform/dcgm_node"]; !ok {
		t.Error("transform/dcgm_node (takes the exporter pod's own identity off the GPU data) renders whenever dcgm is bundled")
	}
	ds, ok := r.daemonsets["continuum-telemetry-dcgm"]
	if !ok {
		t.Fatal("no continuum-telemetry-dcgm DaemonSet rendered")
	}
	// DCGM_EXPORTER_LISTEN and NODE_NAME are always set (rv2 D); only the pod-attribution switch belongs to applyScope.
	for _, e := range ds.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "DCGM_EXPORTER_KUBERNETES" {
			t.Errorf("dcgm-exporter must not enable pod attribution when applyScope is off, got %+v", e)
		}
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

	if _, ok := procs["transform/dcgm_pod"]; ok {
		t.Errorf("transform/dcgm_pod promoted a data point's namespace to the node's one shared resource (last pod wins); it must be gone: %v", procs)
	}
	if _, ok := procs["transform/dcgm_node"]; !ok {
		t.Fatalf("transform/dcgm_node missing: %v", procs)
	}
	// Kepler's own transform must be untouched - accelerators' scoping must not interfere with it.
	if _, ok := procs["transform/kepler_node"]; !ok {
		t.Errorf("transform/kepler_node should still render alongside transform/dcgm_node")
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
	if !strings.Contains(cond, `datapoint.attributes["namespace"]`) || strings.Contains(cond, `resource.attributes["k8s.namespace.name"]`) {
		t.Errorf("filter/scope_accelerators must read each data point's own namespace label, not the shared resource's, got %q", cond)
	}
	if !strings.Contains(cond, `service.name"] == "dcgm-exporter"`) {
		t.Errorf("filter/scope_accelerators must gate on service.name == dcgm-exporter so it never touches Kepler's records, got %q", cond)
	}

	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	infra, _ := pipelines["metrics/infra"].(map[string]any)
	infraProcs, _ := infra["processors"].([]any)
	if !containsAny(infraProcs, "transform/dcgm_node") || !containsAny(infraProcs, "filter/scope_accelerators") {
		t.Errorf("metrics/infra must carry both transform/dcgm_node and filter/scope_accelerators, got %v", infraProcs)
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
	// namespace/pod/container come from the kubelet pod-resources mapping (DCGM_EXPORTER_KUBERNETES) alone; this one adds the
	// pod's own labels as extra metric labels and reads the Kubernetes API, which this pod has no token for.
	if v, ok := env["DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"]; ok {
		t.Errorf("DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS = %q, must not be set", v)
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
	if _, ok := procs["transform/dcgm_node"]; !ok {
		t.Error("transform/dcgm_node should still render (it does not depend on a scope being set)")
	}
}

func TestTelemetryExporterMTLSWiresCertAndKey(t *testing.T) {
	// resourceUsage alone only brings up the host DaemonSet collector (agent.telemetryHostEnabled) - this test
	// checks mTLS wiring on both collectors below, so kubernetesState (a cluster-scoped signal) is also on,
	// to bring up the cluster Deployment collector too (agent.telemetryClusterEnabled).
	r := render(t, "--set", "telemetry.export.otlp.endpoint=collector.example:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.kubernetesState.metrics.enabled=true",
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

// Before this test existed, telemetry.export.otlp.protocol was read by nothing in the templates at all -
// every destination rendered under the gRPC-only "otlp" exporter regardless of what was asked for, so an
// httpOnly destination (Grafana Cloud, Datadog - see exportPresets.ts) silently got a pipeline that could
// never actually reach it. This locks in the fix: protocol=http renders "otlphttp" instead, with a full
// scheme-qualified endpoint (confighttp has no separate plaintext toggle - the URL scheme IS that choice),
// and every pipeline's own exporters: [...] reference follows the same name, not a literal "otlp".
func TestTelemetryExportProtocolHTTPUsesOtlphttpExporter(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=otlp-gateway.example.com/otlp", "--set", "telemetry.export.otlp.protocol=http",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true")

	for _, cm := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cfg := otelConfig(t, r.configmaps[cm].Data)
		exporters, _ := cfg["exporters"].(map[string]any)
		if _, ok := exporters["otlp"]; ok {
			t.Errorf("%s: a gRPC-only otlp exporter rendered alongside protocol=http", cm)
		}
		otlphttp, ok := exporters["otlphttp"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no otlphttp exporter rendered: %+v", cm, exporters)
		}
		if otlphttp["endpoint"] != "https://otlp-gateway.example.com/otlp" {
			t.Errorf("%s: otlphttp endpoint = %v, want a scheme-qualified https:// URL", cm, otlphttp["endpoint"])
		}
		if _, ok := otlphttp["tls"]; ok {
			t.Errorf("%s: otlphttp has no insecure toggle to carry - an empty tls stanza shouldn't render: %+v", cm, otlphttp["tls"])
		}

		pipelines, _ := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
		if len(pipelines) == 0 {
			t.Fatalf("%s: no pipelines rendered", cm)
		}
		for name, raw := range pipelines {
			p, _ := raw.(map[string]any)
			exp, _ := p["exporters"].([]any)
			if len(exp) != 1 || exp[0] != "otlphttp" {
				t.Errorf("%s pipeline %q: exporters = %v, want exactly [otlphttp]", cm, name, exp)
			}
		}
	}

	// Plaintext (insecure) still switches the URL scheme, the only lever confighttp actually has for it.
	ri := render(t, "--set", "telemetry.export.otlp.endpoint=collector.local:4318", "--set", "telemetry.export.otlp.protocol=http",
		"--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cfg := otelConfig(t, ri.configmaps["continuum-telemetry-host-config"].Data)
	exporters, _ := cfg["exporters"].(map[string]any)
	otlphttp, _ := exporters["otlphttp"].(map[string]any)
	if otlphttp["endpoint"] != "http://collector.local:4318" {
		t.Errorf("insecure otlphttp endpoint = %v, want http:// scheme", otlphttp["endpoint"])
	}

	// grpc (the default) is unaffected: still the plain "otlp" exporter, referenced by the same name.
	rg := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cfgG := otelConfig(t, rg.configmaps["continuum-telemetry-host-config"].Data)
	expG, _ := cfgG["exporters"].(map[string]any)
	if _, ok := expG["otlp"]; !ok {
		t.Fatalf("default protocol dropped the plain otlp exporter: %+v", expG)
	}
	if _, ok := expG["otlphttp"]; ok {
		t.Error("default protocol should not render an otlphttp exporter")
	}
}

// -----------------------------------------------------------------------------------------------------
// resource/continuum: Ikhnos's own org/cluster/intent provenance, stamped last before batch so it
// always wins over a user's own telemetry.processors.extraProcessors (action: upsert, last in the list).
// -----------------------------------------------------------------------------------------------------

func TestTelemetryContinuumProvenanceProcessorStampsResourceAttributes(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.resource.orgId=org-1",
		"--set", "telemetry.resource.clusterId=cluster-1",
		"--set", "telemetry.resource.intentId=intent-1",
	)

	checkAttrs := func(t *testing.T, cmName string, attrs []any) {
		t.Helper()
		want := map[string]string{
			"continuum.org.id":     "org-1",
			"continuum.cluster.id": "cluster-1",
			"continuum.intent.id":  "intent-1",
		}
		if len(attrs) != len(want) {
			t.Fatalf("%s: resource/continuum attributes = %v, want %d entries", cmName, attrs, len(want))
		}
		for _, a := range attrs {
			m, _ := a.(map[string]any)
			key, _ := m["key"].(string)
			wantVal, ok := want[key]
			if !ok {
				t.Errorf("%s: unexpected attribute key %q in resource/continuum: %v", cmName, key, m)
				continue
			}
			if m["value"] != wantVal {
				t.Errorf("%s: resource/continuum[%s].value = %v, want %q", cmName, key, m["value"], wantVal)
			}
			if m["action"] != "upsert" {
				t.Errorf("%s: resource/continuum[%s].action = %v, want upsert", cmName, key, m["action"])
			}
			delete(want, key)
		}
		if len(want) != 0 {
			t.Errorf("%s: resource/continuum missing attribute keys: %v", cmName, want)
		}
	}

	for _, cmName := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cfg := otelConfig(t, r.configmaps[cmName].Data)
		procs, _ := cfg["processors"].(map[string]any)
		rc, ok := procs["resource/continuum"].(map[string]any)
		if !ok {
			t.Fatalf("%s: resource/continuum processor missing: %v", cmName, procs)
		}
		attrs, _ := rc["attributes"].([]any)
		checkAttrs(t, cmName, attrs)

		svc, _ := cfg["service"].(map[string]any)
		pipelines, _ := svc["pipelines"].(map[string]any)
		if len(pipelines) == 0 {
			t.Fatalf("%s: no pipelines rendered", cmName)
		}
		for name, raw := range pipelines {
			p, _ := raw.(map[string]any)
			procList, _ := p["processors"].([]any)
			if len(procList) < 2 || procList[len(procList)-1] != "batch" || procList[len(procList)-2] != "resource/continuum" {
				t.Errorf("%s pipeline %q processors = %v, want resource/continuum immediately before batch", cmName, name, procList)
			}
		}
	}
}

func TestTelemetryContinuumProvenanceAlwaysAfterUsersExtraProcessor(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.resource.orgId=org-1", "--set", "telemetry.resource.clusterId=cluster-1", "--set", "telemetry.resource.intentId=intent-1",
		"--set-json", `telemetry.processors.extraProcessors={"attributes/spoof_org":{"actions":[{"key":"continuum.org.id","value":"attacker","action":"upsert"}]}}`,
		"--set", "telemetry.processors.extraProcessorNames[0]=attributes/spoof_org")
	cm := r.configmaps["continuum-telemetry-cluster-config"]
	cfg := otelConfig(t, cm.Data)
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	traces, ok := pipelines["traces"].(map[string]any)
	if !ok {
		t.Fatal("traces pipeline missing")
	}
	procList, _ := traces["processors"].([]any)

	spoofIdx, provIdx := -1, -1
	for i, p := range procList {
		if p == "attributes/spoof_org" {
			spoofIdx = i
		}
		if p == "resource/continuum" {
			provIdx = i
		}
	}
	if spoofIdx == -1 || provIdx == -1 {
		t.Fatalf("expected both attributes/spoof_org and resource/continuum in traces processors, got %v", procList)
	}
	// Position is what proves the point: processors run in list order and a later action:upsert on the
	// same key wins, so resource/continuum must sit after the user's own extra processor, not before it.
	if provIdx <= spoofIdx {
		t.Errorf("resource/continuum (index %d) must come after the user's own extra processor (index %d) so its action:upsert wins, got %v", provIdx, spoofIdx, procList)
	}
	if procList[len(procList)-1] != "batch" {
		t.Errorf("batch must stay last even with resource/continuum added, got %v", procList)
	}
	if procList[len(procList)-2] != "resource/continuum" {
		t.Errorf("resource/continuum must sit immediately before batch, got %v", procList)
	}
}

func TestTelemetryContinuumProvenanceDefaultsToEmptyAttributesAndStillRenders(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cm := r.configmaps["continuum-telemetry-host-config"]
	cfg := otelConfig(t, cm.Data)
	procs, _ := cfg["processors"].(map[string]any)
	rc, ok := procs["resource/continuum"].(map[string]any)
	if !ok {
		t.Fatalf("resource/continuum processor missing even with telemetry.resource left at its empty-string defaults: %v", procs)
	}
	attrs, _ := rc["attributes"].([]any)
	// Org and cluster are always listed; the intent and the scope only when there is one to say (an empty
	// continuum.intent.id on every record would be noise, not provenance).
	if len(attrs) != 2 {
		t.Fatalf("resource/continuum attributes = %v, want the org and cluster entries even with empty values", attrs)
	}
	for _, a := range attrs {
		m, _ := a.(map[string]any)
		if m["value"] != "" {
			t.Errorf("resource/continuum attribute %v should default to an empty string, not be hand-typed", m)
		}
		if m["action"] != "upsert" {
			t.Errorf("resource/continuum attribute %v should still use action: upsert at defaults", m)
		}
	}
}

// -----------------------------------------------------------------------------------------------------
// telemetry.resource.attributes (tags), continuum.scope, and the debug exporter.
// -----------------------------------------------------------------------------------------------------

// pipelineLists returns every pipeline's processors and exporters across both collector ConfigMaps.
func pipelineLists(t *testing.T, r rendered) map[string][2][]any {
	t.Helper()
	out := map[string][2][]any{}
	for _, name := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cm, ok := r.configmaps[name]
		if !ok {
			continue
		}
		cfg := otelConfig(t, cm.Data)
		svc, _ := cfg["service"].(map[string]any)
		pipelines, _ := svc["pipelines"].(map[string]any)
		for pn, raw := range pipelines {
			p, _ := raw.(map[string]any)
			procs, _ := p["processors"].([]any)
			exps, _ := p["exporters"].([]any)
			out[name+"/"+pn] = [2][]any{procs, exps}
		}
	}
	return out
}

func TestTelemetryResourceTagsAreInsertedAfterUserProcessorsAndBeforeProvenance(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set-json", `telemetry.resource.attributes=[{"key":"team","value":"payments"},{"key":"k8s.cluster.name","value":"prod-eu"}]`,
		"--set", "telemetry.resource.scope=shop; payments: api+worker",
		"--set", "telemetry.resource.orgId=org-1", "--set", "telemetry.resource.clusterId=cl-1", "--set", "telemetry.resource.intentId=ti-1",
	)
	for _, name := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cfg := otelConfig(t, r.configmaps[name].Data)
		procs, _ := cfg["processors"].(map[string]any)
		tags, ok := procs["resource/tags"].(map[string]any)
		if !ok {
			t.Fatalf("%s: resource/tags missing: %v", name, procs)
		}
		got := map[string]string{}
		for _, a := range tags["attributes"].([]any) {
			m := a.(map[string]any)
			// insert, never upsert: a key an application already set keeps the application's value.
			if m["action"] != "insert" {
				t.Errorf("%s: tag %v must use action: insert", name, m)
			}
			got[m["key"].(string)] = m["value"].(string)
		}
		if got["team"] != "payments" || got["k8s.cluster.name"] != "prod-eu" || len(got) != 2 {
			t.Errorf("%s: tags = %v", name, got)
		}
		rc := procs["resource/continuum"].(map[string]any)
		keys := map[string]string{}
		for _, a := range rc["attributes"].([]any) {
			m := a.(map[string]any)
			keys[m["key"].(string)] = m["value"].(string)
		}
		if keys["continuum.scope"] != "shop; payments: api+worker" || keys["continuum.intent.id"] != "ti-1" {
			t.Errorf("%s: provenance = %v, want scope and intent stamped", name, keys)
		}
	}
	for pn, pe := range pipelineLists(t, r) {
		procs := pe[0]
		n := len(procs)
		if n < 3 || procs[n-1] != "batch" || procs[n-2] != "resource/continuum" || procs[n-3] != "resource/tags" {
			t.Errorf("%s processors = %v, want ... resource/tags, resource/continuum, batch", pn, procs)
		}
	}
}

func TestTelemetryWithoutTagsRendersNoTagProcessor(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	if _, ok := cfg["processors"].(map[string]any)["resource/tags"]; ok {
		t.Error("resource/tags rendered with no telemetry.resource.attributes")
	}
	for pn, pe := range pipelineLists(t, r) {
		for _, p := range pe[0] {
			if p == "resource/tags" {
				t.Errorf("%s lists resource/tags with no tags set: %v", pn, pe[0])
			}
		}
	}
	// And no scope/intent entries when there is nothing to say.
	rc := cfg["processors"].(map[string]any)["resource/continuum"].(map[string]any)
	for _, a := range rc["attributes"].([]any) {
		k := a.(map[string]any)["key"]
		if k == "continuum.scope" || k == "continuum.intent.id" {
			t.Errorf("%v stamped with an empty value", k)
		}
	}
}

func TestTelemetryTagsRefuseTheReservedPrefixAndTooManyTags(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set-json", `telemetry.resource.attributes=[{"key":"continuum.org.id","value":"attacker"}]`)
	if err == nil || !strings.Contains(out, "reserved") {
		t.Errorf("a continuum.* tag must be refused as reserved, got err=%v out=%s", err, out)
	}
	var many []string
	for i := 0; i < 11; i++ {
		many = append(many, fmt.Sprintf(`{"key":"tag%d","value":"v"}`, i))
	}
	out, err = helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set-json", "telemetry.resource.attributes=["+strings.Join(many, ",")+"]")
	if err == nil || !strings.Contains(out, "at most 10") {
		t.Errorf("11 tags must be refused, got err=%v out=%s", err, out)
	}
}

func TestTelemetryTagsRefuseADuplicateKey(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set-json", `telemetry.resource.attributes=[{"key":"team","value":"a"},{"key":"team","value":"b"}]`)
	if err == nil || !strings.Contains(out, "listed twice") {
		t.Errorf("a duplicated tag key must be refused, got err=%v out=%s", err, out)
	}
}

func TestTelemetryDebugExporterIsOffByDefaultAndOnEveryPipelineWhenSet(t *testing.T) {
	base := []string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.traces.traces.enabled=true"}

	off := render(t, base...)
	for _, name := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		cfg := otelConfig(t, off.configmaps[name].Data)
		if _, ok := cfg["exporters"].(map[string]any)["debug"]; ok {
			t.Errorf("%s: a debug exporter is configured with telemetry.debug.verbosity unset", name)
		}
	}
	for pn, pe := range pipelineLists(t, off) {
		if len(pe[1]) != 1 {
			t.Errorf("%s exporters = %v, want only the real destination", pn, pe[1])
		}
	}

	for _, verbosity := range []string{"basic", "detailed"} {
		on := render(t, append(append([]string{}, base...), "--set", "telemetry.debug.verbosity="+verbosity)...)
		for _, name := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
			cfg := otelConfig(t, on.configmaps[name].Data)
			d, ok := cfg["exporters"].(map[string]any)["debug"].(map[string]any)
			if !ok || d["verbosity"] != verbosity {
				t.Errorf("%s: debug exporter = %v, want verbosity %s", name, d, verbosity)
			}
		}
		for pn, pe := range pipelineLists(t, on) {
			if len(pe[1]) != 2 || pe[1][0] == "debug" || pe[1][1] != "debug" {
				t.Errorf("%s exporters = %v, want the real destination then debug", pn, pe[1])
			}
		}
	}
}

func TestTelemetryDebugVerbosityRefusesAnythingElse(t *testing.T) {
	out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.debug.verbosity=verbose")
	if err == nil {
		t.Errorf("telemetry.debug.verbosity=verbose must be refused, got:\n%s", out)
	}
}

// conditionsOf returns the condition list of one filter processor under the given key
// (metric_conditions, log_conditions, trace_conditions), as plain strings.
func conditionsOf(t *testing.T, procs map[string]any, name, key string) []string {
	t.Helper()
	p, ok := procs[name].(map[string]any)
	if !ok {
		t.Fatalf("processor %s missing: %v", name, procs)
	}
	raw, _ := p[key].([]any)
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		out = append(out, c.(string))
	}
	return out
}

func TestTelemetryWorkloadScopeKeepsOnlyChosenWorkloadsOfANamespace(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set-json", `telemetry.scope.namespaces=["checkout","payments"]`,
		"--set-json", `telemetry.scope.workloads=[{"namespace":"checkout","names":["cart","payment-api"]}]`,
	)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	procs, _ := cfg["processors"].(map[string]any)
	conds := conditionsOf(t, procs, "filter/scope", "trace_conditions")
	if len(conds) != 2 {
		t.Fatalf("want the namespace allow-list plus one workload condition, got %v", conds)
	}
	if !strings.Contains(conds[0], `not IsMatch(resource.attributes["k8s.namespace.name"], "^(checkout|payments)$")`) {
		t.Errorf("namespace condition changed: %s", conds[0])
	}
	w := conds[1]
	for _, want := range []string{`resource.attributes["k8s.namespace.name"] == "checkout"`, `not (`, `k8s.deployment.name`, `k8s.statefulset.name`, `k8s.daemonset.name`, `^(cart|payment-api)$`} {
		if !strings.Contains(w, want) {
			t.Errorf("workload condition lacks %q: %s", want, w)
		}
	}
	if strings.Contains(w, "payments") {
		t.Errorf("a namespace not listed under workloads must stay whole: %s", w)
	}
}

func TestTelemetryWorkloadScopeEmptyListKeepsNothingOfThatNamespace(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set-json", `telemetry.scope.workloads=[{"namespace":"checkout","names":[]}]`,
	)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	procs, _ := cfg["processors"].(map[string]any)
	conds := conditionsOf(t, procs, "filter/scope", "trace_conditions")
	if len(conds) != 1 || conds[0] != `(resource.attributes["k8s.namespace.name"] == "checkout")` {
		t.Fatalf("got %v", conds)
	}
}

func TestTelemetryWorkloadNamesAreExtracted(t *testing.T) {
	meta := func(r rendered) []any {
		cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
		procs, _ := cfg["processors"].(map[string]any)
		k, _ := procs["k8sattributes"].(map[string]any)
		ex, _ := k["extract"].(map[string]any)
		m, _ := ex["metadata"].([]any)
		return m
	}
	base := []string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.traces.traces.enabled=true"}
	// The dashboards match an application's pods on these, whatever the scope: StatefulSet and DaemonSet pods are otherwise invisible.
	m := meta(render(t, base...))
	for _, want := range []string{"k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name"} {
		if !containsAny(m, want) {
			t.Errorf("default install should extract %s: %v", want, m)
		}
	}
	if containsAny(m, "k8s.job.name") {
		t.Errorf("job names extracted with no workload scope: %v", m)
	}
	// The host collector's k8sattributes is what labels the kubelet's pod metrics and the container logs.
	h := hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	hp, _ := h["processors"].(map[string]any)
	hk, _ := hp["k8sattributes"].(map[string]any)
	hx, _ := hk["extract"].(map[string]any)
	if hm, _ := hx["metadata"].([]any); !containsAny(hm, "k8s.statefulset.name") || !containsAny(hm, "k8s.daemonset.name") {
		t.Errorf("host collector should extract StatefulSet and DaemonSet names: %v", hm)
	}
	m = meta(render(t, append(base, "--set-json", `telemetry.traces.traces.scope.workloads=[{"namespace":"shop","names":["a"]}]`)...))
	for _, want := range []string{"k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name", "k8s.job.name", "k8s.cronjob.name"} {
		if !containsAny(m, want) {
			t.Errorf("per-signal workload scope should extract %s: %v", want, m)
		}
	}
}

func TestTelemetryPerSignalWorkloadScopeGetsItsOwnProcessor(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set-json", `telemetry.traces.traces.scope.workloads=[{"namespace":"shop","names":["api"]}]`,
	)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	procs, _ := cfg["processors"].(map[string]any)
	if _, ok := procs["filter/scope_traces"]; !ok {
		t.Fatalf("traces with its own workload scope should get filter/scope_traces: %v", procs)
	}
	if _, ok := procs["filter/scope_applicationLogs"]; ok {
		t.Errorf("logs set nothing of its own")
	}
}

func TestTelemetryWorkloadNamesMustBeSafeToPutInARegex(t *testing.T) {
	for _, bad := range []string{`[{"namespace":"shop","names":["a|b"]}]`, `[{"namespace":"shop","names":["A"]}]`, `[{"namespace":"Shop","names":["a"]}]`, `[{"namespace":"shop","names":["a.b"]}]`, `[{"names":["a"]}]`} {
		_, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set-json", "telemetry.scope.workloads="+bad)
		if err == nil {
			t.Errorf("workloads %s should be refused", bad)
		}
	}
}

func TestTelemetryInfraScopeOffByDefaultRendersNoInfraFilters(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.resourceUsage.metrics.enabled=true",
	)
	for _, cm := range []string{"continuum-telemetry-cluster-config", "continuum-telemetry-host-config"} {
		if strings.Contains(r.configmaps[cm].Data["otel-collector-config.yaml"], "scope_infra") {
			t.Errorf("%s renders an infra scope filter by default", cm)
		}
	}
}

func TestTelemetryInfraScopeNarrowsOnlyRecordsThatCarryANamespace(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.systemLogs.logs.enabled=true",
		"--set-json", `telemetry.scope.infra.namespaces=["checkout"]`,
		"--set-json", `telemetry.scope.infra.workloads=[{"namespace":"checkout","names":["cart"]}]`,
	)
	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	procs, _ := cluster["processors"].(map[string]any)
	state := conditionsOf(t, procs, "filter/scope_infra", "metric_conditions")
	if len(state) != 2 || !strings.HasPrefix(state[0], `(resource.attributes["k8s.namespace.name"] != nil and not IsMatch(`) {
		t.Errorf("a record with no namespace must be kept, got %v", state)
	}
	events := conditionsOf(t, procs, "filter/scope_infra_events", "log_conditions")
	// OTTL wants every path to start with its context ("log.body", not "body"): the collector refuses to start on a bare
	// "body[...]" ("path's first segment must be a valid context name").
	if len(events) != 2 || !strings.Contains(events[1], `log.body["object"]["metadata"]["namespace"] != nil`) {
		t.Errorf("events are narrowed by namespace, also from the event object: %v", events)
	}
	for _, c := range events {
		if regexp.MustCompile(`(^|[^.\w"])body\[`).MatchString(c) {
			t.Errorf("a bare body path without its log. context: %s", c)
		}
	}
	for _, c := range events {
		if strings.Contains(c, "deployment") {
			t.Errorf("an event has no workload, got %s", c)
		}
	}
	pipelines := cluster["service"].(map[string]any)["pipelines"].(map[string]any)
	if !containsAny(pipelines["metrics/infra"].(map[string]any)["processors"].([]any), "filter/scope_infra") {
		t.Errorf("metrics/infra lacks the infra filter")
	}
	if !containsAny(pipelines["logs/infra"].(map[string]any)["processors"].([]any), "filter/scope_infra_events") {
		t.Errorf("logs/infra lacks the events filter")
	}

	host := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	hp, _ := host["processors"].(map[string]any)
	if got := conditionsOf(t, hp, "filter/scope_infra", "metric_conditions"); len(got) != 2 {
		t.Errorf("host metrics filter: %v", got)
	}
	hpipes := host["service"].(map[string]any)["pipelines"].(map[string]any)
	metricsProcs := hpipes["metrics"].(map[string]any)["processors"].([]any)
	if len(metricsProcs) < 3 || metricsProcs[0] != "memory_limiter" || metricsProcs[1] != "k8sattributes" || metricsProcs[2] != "filter/scope_infra" {
		t.Errorf("the filter goes right after k8sattributes, got %v", metricsProcs)
	}
	if containsAny(hpipes["logs"].(map[string]any)["processors"].([]any), "filter/scope_infra") {
		t.Errorf("the infra metrics filter does not belong on system logs, which have their own (filter/scope_system_logs)")
	}
}

// Application metrics with no scrape targets must not render the Prometheus receiver at all: the upstream receiver
// refuses to load with an empty scrape_configs, which would take the whole cluster collector down with it. The app's
// own OTLP receiver keeps working. With targets the receiver is there, and the relabel's capture group is escaped
// ($$) so the collector's own ${...} expansion does not eat it.
func TestApplicationMetricsWithoutScrapeTargetsRendersNoPrometheusReceiver(t *testing.T) {
	base := []string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.applicationMetrics.metrics.enabled=true"}
	cfg := func(args ...string) map[string]any {
		r := render(t, append(append([]string{}, base...), args...)...)
		return otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	}
	c := cfg()
	receivers, _ := c["receivers"].(map[string]any)
	if _, ok := receivers["prometheus/app"]; ok {
		t.Fatalf("prometheus/app rendered with no scrape targets: %v", keys(receivers))
	}
	pipes, _ := c["service"].(map[string]any)["pipelines"].(map[string]any)
	app, _ := pipes["metrics/app"].(map[string]any)
	for _, r := range app["receivers"].([]any) {
		if r == "prometheus/app" {
			t.Fatalf("metrics/app still names prometheus/app: %v", app["receivers"])
		}
	}
	found := false
	for _, r := range app["receivers"].([]any) {
		found = found || r == "otlp"
	}
	if !found {
		t.Fatalf("metrics/app lost its OTLP receiver: %v", app["receivers"])
	}

	c = cfg("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].jobName=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=shop",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].port=9102")
	receivers, _ = c["receivers"].(map[string]any)
	prom, ok := receivers["prometheus/app"].(map[string]any)
	if !ok {
		t.Fatalf("prometheus/app missing with a scrape target: %v", keys(receivers))
	}
	if raw := fmt.Sprint(prom); !strings.Contains(raw, "$${1}:9102") {
		t.Fatalf("the relabel replacement is not escaped: %s", raw)
	}
}

// Kepler (release-0.7.x) listens on 0.0.0.0:8888 unless BIND_ADDRESS says otherwise. The container declares 9103 - which is
// what the cluster collector's pod-based scrape job connects to - so without the variable every scrape was "connection
// refused" while the pod reported Ready, and the collector's only output was its own `up` series (found on a real cluster).
func TestKeplerListensOnThePortItDeclaresAndIsNotReadyUntilItDoes(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler")
	c := r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec.Containers[0]
	var port int32
	for _, p := range c.Ports {
		if p.Name == "metrics" {
			port = p.ContainerPort
		}
	}
	if port == 0 {
		t.Fatal("Kepler declares no metrics port")
	}
	bind := ""
	for _, e := range c.Env {
		if e.Name == "BIND_ADDRESS" {
			bind = e.Value
		}
	}
	// ":port", not "0.0.0.0:port": Kepler hands it to net/http unchanged, and a 0.0.0.0 bind is IPv4-only (see
	// telemetry_rv2_K_test.go).
	if want := fmt.Sprintf(":%d", port); bind != want {
		t.Errorf("BIND_ADDRESS = %q, want %q (the declared metrics port, which is what gets scraped)", bind, want)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.TCPSocket == nil {
		t.Error("Kepler has no readiness probe on its metrics port, so a pod that is not serving looks healthy")
	}
}

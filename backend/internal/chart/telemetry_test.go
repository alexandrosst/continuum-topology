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

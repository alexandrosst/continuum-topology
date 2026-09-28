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

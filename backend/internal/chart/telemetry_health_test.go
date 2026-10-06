package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// telemetry.health: each collector serves its own export counters on its pod's address, the agent is told
// where to find every pod, and only the agent may reach that port.

func healthReader(t *testing.T, cfg map[string]any) (host string, port any, ok bool) {
	t.Helper()
	svc, _ := cfg["service"].(map[string]any)
	tel, _ := svc["telemetry"].(map[string]any)
	metrics, _ := tel["metrics"].(map[string]any)
	readers, _ := metrics["readers"].([]any)
	if len(readers) != 1 {
		return "", nil, false
	}
	pull, _ := readers[0].(map[string]any)["pull"].(map[string]any)
	prom, _ := pull["exporter"].(map[string]any)["prometheus"].(map[string]any)
	h, _ := prom["host"].(string)
	return h, prom["port"], true
}

func TestTelemetryHealthIsOnWithAnySignalAndReachesBothCollectors(t *testing.T) {
	args := append([]string{"--set", "telemetry.export.otlp.endpoint=gw:4317"}, allSignals...)
	r := render(t, args...)

	for name, cm := range map[string]string{"host": "continuum-telemetry-host-config", "cluster": "continuum-telemetry-cluster-config"} {
		h, port, ok := healthReader(t, otelConfig(t, r.configmaps[cm].Data))
		if !ok {
			t.Errorf("%s collector: no metrics reader for its own counters", name)
			continue
		}
		// Bound to the pod's own address (not 0.0.0.0, which exposes it on every interface, and not the
		// default localhost, which the agent could not reach).
		if h != "${env:POD_IP}" || port != float64(8888) {
			t.Errorf("%s collector: counters served at %v:%v, want ${env:POD_IP}:8888", name, h, port)
		}
	}
	for name, env := range map[string]map[string]string{
		"host":    podIPFieldPath(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]),
		"cluster": podIPFieldPath(r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]),
	} {
		if env["POD_IP"] != "status.podIP" {
			t.Errorf("%s collector: POD_IP = %q, want the downward-API status.podIP", name, env["POD_IP"])
		}
	}

	agent := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	got, _ := env(agent, "CONTINUUM_TELEMETRY_HEALTH_TARGETS")
	// One headless-Service name per collector; the namespace is whatever the test's helm run used.
	targets := strings.Split(got, ",")
	if len(targets) != 2 || !strings.HasPrefix(targets[0], "continuum-telemetry-host-metrics.") || !strings.HasPrefix(targets[1], "continuum-telemetry-cluster-metrics.") || !strings.HasSuffix(targets[1], ".svc:8888") {
		t.Errorf("CONTINUUM_TELEMETRY_HEALTH_TARGETS = %q, want one headless-Service name per collector", got)
	}

	out, err := helmTemplate(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"continuum-telemetry-host-metrics", "continuum-telemetry-cluster-metrics"} {
		if !strings.Contains(out, "name: "+svc+"\n") {
			t.Errorf("no headless Service %s rendered", svc)
		}
	}
	if strings.Count(out, "clusterIP: None") < 2 {
		t.Errorf("the metrics Services must be headless so the agent resolves every pod")
	}

	// The host collector has no receiver, so it is isolated: only the agent reaches the counters.
	p, ok := r.policies["continuum-telemetry-host-health"]
	if !ok {
		t.Fatal("no NetworkPolicy for the host collector's counters")
	}
	if len(p.Spec.Ingress) != 1 || len(p.Spec.Ingress[0].From) != 1 || len(p.Spec.Ingress[0].Ports) != 1 {
		t.Fatalf("host health policy ingress = %+v", p.Spec.Ingress)
	}
	if p.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-agent" {
		t.Errorf("host health policy admits %+v, want only the agent", p.Spec.Ingress[0].From[0].PodSelector)
	}
	// ...but the cluster collector also takes OTLP from applications, so no policy of ours may isolate it.
	if _, ok := r.policies["continuum-telemetry-cluster-health"]; ok {
		t.Error("a NetworkPolicy on the cluster collector would cut off its OTLP receiver")
	}
}

// podIPFieldPath is the downward-API field each environment variable of c reads (POD_IP -> status.podIP).
func podIPFieldPath(c corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			out[e.Name] = e.ValueFrom.FieldRef.FieldPath
		}
	}
	return out
}

func TestTelemetryHealthAbsentWithoutTelemetryOrWhenOff(t *testing.T) {
	for name, args := range map[string][]string{
		"no signal": {},
		"disabled":  append([]string{"--set", "telemetry.export.otlp.endpoint=gw:4317", "--set", "telemetry.health.enabled=false"}, allSignals...),
	} {
		r := render(t, args...)
		agent := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
		if _, ok := env(agent, "CONTINUUM_TELEMETRY_HEALTH_TARGETS"); ok {
			t.Errorf("%s: the agent was told where to read counters", name)
		}
		if _, ok := r.policies["continuum-telemetry-host-health"]; ok {
			t.Errorf("%s: a health NetworkPolicy was rendered", name)
		}
		if cm, ok := r.configmaps["continuum-telemetry-host-config"]; ok {
			if _, _, has := healthReader(t, otelConfig(t, cm.Data)); has {
				t.Errorf("%s: the host collector serves counters", name)
			}
		}
		out, err := helmTemplate(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "self-metrics") {
			t.Errorf("%s: a metrics Service was rendered", name)
		}
	}
}

func TestTelemetryHealthOnlyNamesCollectorsThatExist(t *testing.T) {
	// Host signals only: there is no cluster collector to read.
	r := render(t, "--set", "telemetry.export.otlp.endpoint=gw:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true")
	agent := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	got, _ := env(agent, "CONTINUUM_TELEMETRY_HEALTH_TARGETS")
	if strings.Contains(got, "cluster") || !strings.Contains(got, "continuum-telemetry-host-metrics") {
		t.Errorf("targets = %q, want just the host collector", got)
	}
}

func TestTelemetryHealthPortIsConfigurableAndSchemaChecked(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=gw:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.health.port=9464")
	if _, port, _ := healthReader(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)); port != float64(9464) {
		t.Errorf("port = %v, want 9464", port)
	}
	if _, err := helmTemplate(t, "--set", "telemetry.health.port=0"); err == nil {
		t.Error("port 0 must be rejected by the schema")
	}
}

// A namespace that already isolates the cluster collector (telemetry.receiver.networkPolicy) and an agent whose
// egress is locked down (networkPolicy.egress) each need one more rule, or the counters are unreachable.
func TestTelemetryHealthIsAllowedThroughTheOtherNetworkPolicies(t *testing.T) {
	r := render(t,
		"--set", "telemetry.export.otlp.endpoint=gw:4317", "--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "telemetry.receiver.networkPolicy.enabled=true", "--set-string", "telemetry.receiver.networkPolicy.allowedIngress[0].namespaceSelector.matchLabels.team=shop",
		"--set", "networkPolicy.egress.enabled=true", "--set", "networkPolicy.egress.apiServerCIDRs={10.0.0.1/32}", "--set", "networkPolicy.egress.serverCIDRs={10.0.0.2/32}",
	)
	in := r.policies["continuum-telemetry-receiver-ingress"].Spec.Ingress
	if len(in) != 2 || len(in[1].Ports) != 1 || in[1].Ports[0].Port.IntValue() != 8888 || in[1].From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-agent" {
		t.Errorf("receiver ingress = %+v, want the application rule and then the agent's access to port 8888", in)
	}
	var found bool
	for _, e := range r.policies["continuum-agent-egress"].Spec.Egress {
		for _, p := range e.Ports {
			if p.Port != nil && p.Port.IntValue() == 8888 {
				found = true
			}
		}
	}
	if !found {
		t.Error("the agent's egress policy does not allow reading the collectors' counters")
	}
}

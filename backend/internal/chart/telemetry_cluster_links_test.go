package chart

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// decodeInto converts a loosely typed manifest into a typed object.
func decodeInto(t *testing.T, d doc, out any) {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}

// clusterLinksArgs turns the signal on with what it needs, on top of a telemetry destination.
func clusterLinksArgs(extra ...string) []string {
	args := append([]string{}, agentTel...)
	args = append(args, "--set", "telemetry.clusterLinks.metrics.enabled=true", "--set", "flowObserver.enabled=true")
	for _, e := range extra {
		args = append(args, "--set", e)
	}
	return args
}

func TestClusterLinksAreOffByDefaultAndRenderNothing(t *testing.T) {
	r := render(t, append(append([]string{}, agentTel...), "--set", "flowObserver.enabled=true")...)
	for name, d := range r.deployments {
		for _, c := range d.Spec.Template.Spec.Containers {
			for _, e := range c.Env {
				if e.Name == "CONTINUUM_LINK_METRICS_LISTEN" {
					t.Errorf("%s sets %s while the signal is off", name, e.Name)
				}
			}
			for _, p := range c.Ports {
				if p.Name == "link-metrics" {
					t.Errorf("%s opens the link metrics port while the signal is off", name)
				}
			}
		}
	}
	for _, d := range renderDocs(t, append(append([]string{}, agentTel...), "--set", "flowObserver.enabled=true")...) {
		if d.name() == "continuum-agent-link-metrics" {
			t.Error("the link metrics Service exists while the signal is off")
		}
	}
}

func TestClusterLinksNeedTheFlowObserver(t *testing.T) {
	out, err := helmTemplate(t, append(append([]string{}, agentTel...), "--set", "telemetry.clusterLinks.metrics.enabled=true")...)
	if err == nil {
		t.Fatalf("rendered without the flow observer:\n%s", out)
	}
	if !strings.Contains(out, "flowObserver.enabled") {
		t.Fatalf("the failure does not say what to turn on:\n%s", out)
	}
}

func TestClusterLinksAreServedByTheAgentAndScrapedByTheClusterCollector(t *testing.T) {
	args := clusterLinksArgs()
	r := render(t, args...)

	var agentContainer *corev1.Container
	if d, ok := r.deployments["continuum-agent"]; ok {
		for i := range d.Spec.Template.Spec.Containers {
			if d.Spec.Template.Spec.Containers[i].Name == "agent" {
				agentContainer = &d.Spec.Template.Spec.Containers[i]
			}
		}
	}
	if agentContainer == nil {
		t.Fatal("no agent container rendered")
	}
	env := map[string]string{}
	for _, e := range agentContainer.Env {
		env[e.Name] = e.Value
	}
	if env["CONTINUUM_LINK_METRICS_LISTEN"] != ":9104" {
		t.Errorf("CONTINUUM_LINK_METRICS_LISTEN = %q", env["CONTINUUM_LINK_METRICS_LISTEN"])
	}
	if env["CONTINUUM_FLOW_SECRET_FILE"] == "" {
		t.Error("the agent has no flow secret to take reports with")
	}
	if got := env["CONTINUUM_TELEMETRY_SIGNALS"]; !strings.Contains(got, "clusterLinks") {
		t.Errorf("the signal is not reported in CONTINUUM_TELEMETRY_SIGNALS: %q", got)
	}
	var port int32
	for _, p := range agentContainer.Ports {
		if p.Name == "link-metrics" {
			port = p.ContainerPort
		}
	}
	if port != 9104 {
		t.Errorf("link-metrics container port = %d", port)
	}

	var svc *corev1.Service
	var cluster map[string]any
	for _, d := range renderDocs(t, args...) {
		switch {
		case d.kind() == "Service" && d.name() == "continuum-agent-link-metrics":
			svc = &corev1.Service{}
			decodeInto(t, d, svc)
		case d.kind() == "ConfigMap" && d.name() == "continuum-telemetry-cluster-config":
			cluster = otelConfig(t, map[string]string{"otel-collector-config.yaml": d["data"].(map[string]any)["otel-collector-config.yaml"].(string)})
		}
	}
	if svc == nil || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 9104 || svc.Spec.Ports[0].TargetPort.StrVal != "link-metrics" {
		t.Fatalf("service = %+v", svc)
	}
	if svc.Spec.Selector["app.kubernetes.io/name"] != "continuum-agent" {
		t.Errorf("the Service selects %v, not the agent", svc.Spec.Selector)
	}
	if cluster == nil {
		t.Fatal("no cluster collector config")
	}
	recv := sub(t, cluster, "receivers")
	infra, ok := recv["prometheus/infra"].(map[string]any)
	if !ok {
		t.Fatalf("no infra Prometheus receiver in %v", sortedKeys(recv))
	}
	jobs := infra["config"].(map[string]any)["scrape_configs"].([]any)
	var job map[string]any
	for _, j := range jobs {
		if j.(map[string]any)["job_name"] == "cluster-links" {
			job = j.(map[string]any)
		}
	}
	if job == nil {
		t.Fatalf("no cluster-links scrape job in %v", jobs)
	}
	target := job["static_configs"].([]any)[0].(map[string]any)["targets"].([]any)[0].(string)
	if !strings.HasPrefix(target, "continuum-agent-link-metrics.") || !strings.HasSuffix(target, ".svc:9104") {
		t.Errorf("scrape target = %q", target)
	}
	if job["scrape_interval"] != "30s" {
		t.Errorf("scrape_interval = %v", job["scrape_interval"])
	}
	// It is cluster-level data, so it travels on the infra metrics pipeline that telemetry.scope never filters.
	pipe := sub(t, cluster, "service", "pipelines")
	infraPipe, ok := pipe["metrics/infra"].(map[string]any)
	if !ok {
		t.Fatalf("no metrics/infra pipeline in %v", sortedKeys(pipe))
	}
	if !containsAny(infraPipe["receivers"].([]any), "prometheus/infra") {
		t.Errorf("metrics/infra receivers = %v", infraPipe["receivers"])
	}
}

func TestClusterLinksIntervalCanBeChangedAndIsChecked(t *testing.T) {
	out, err := helmTemplate(t, clusterLinksArgs("telemetry.clusterLinks.metrics.interval=10s")...)
	if err != nil || !strings.Contains(out, `scrape_interval: "10s"`) {
		t.Fatalf("err %v\n%s", err, out)
	}
	if _, err := helmTemplate(t, clusterLinksArgs("telemetry.clusterLinks.metrics.interval=often")...); err == nil {
		t.Fatal("a nonsense interval was accepted")
	}
	if _, err := helmTemplate(t, clusterLinksArgs("telemetry.clusterLinks.metrics.source=bundle-something")...); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestClusterLinksArePermittedThroughTheNetworkPolicies(t *testing.T) {
	r := render(t, clusterLinksArgs(
		"flowObserver.networkPolicy=true", "flowObserver.nodeCIDRs[0]=10.0.0.0/8",
		"networkPolicy.telemetryEgress.enabled=true", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317")...)
	ingress, ok := r.policies["continuum-agent-probe"]
	if !ok {
		t.Fatal("no agent ingress policy")
	}
	found := false
	for _, rule := range ingress.Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9104 {
				found = true
				if len(rule.From) != 1 || rule.From[0].PodSelector == nil || rule.From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-telemetry-cluster" {
					t.Errorf("port 9104 is open to %+v, want only the cluster collector", rule.From)
				}
			}
		}
	}
	if !found {
		t.Error("the agent's ingress policy does not let the collector reach port 9104, so the scrape would be refused")
	}
	scrape, ok := r.policies["continuum-telemetry-scrape-egress"]
	if !ok {
		t.Fatal("no scrape egress policy")
	}
	found = false
	for _, rule := range scrape.Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9104 {
				found = true
				if len(rule.To) != 1 || rule.To[0].PodSelector == nil || rule.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-agent" {
					t.Errorf("the collector may reach %+v on 9104, want only the agent", rule.To)
				}
			}
		}
	}
	if !found {
		t.Error("the collector's egress policy does not allow the agent's port 9104")
	}
}

// The agent is told its own pod addresses when the link metrics are on, from the pod's status, so that the connections it makes
// itself are not counted as the link's traffic; with the signal off the variable is not there.
func TestTheAgentIsToldItsOwnAddresses(t *testing.T) {
	has := func(args []string) (corev1.EnvVar, bool) {
		for _, c := range render(t, args...).deployments["continuum-agent"].Spec.Template.Spec.Containers {
			for _, e := range c.Env {
				if e.Name == "CONTINUUM_LINK_OWN_ADDRESSES" {
					return e, true
				}
			}
		}
		return corev1.EnvVar{}, false
	}
	e, ok := has(clusterLinksArgs())
	if !ok || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "status.podIPs" {
		t.Errorf("CONTINUUM_LINK_OWN_ADDRESSES = %+v (found %v), want the pod's status.podIPs", e, ok)
	}
	if _, ok := has(append(append([]string{}, agentTel...), "--set", "flowObserver.enabled=true")); ok {
		t.Error("the own-addresses variable is set while the link metrics are off")
	}
}

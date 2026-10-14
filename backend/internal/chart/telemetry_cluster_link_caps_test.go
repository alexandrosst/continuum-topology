package chart

import "testing"

// agentEnv is the agent container's environment as rendered.
func agentEnv(t *testing.T, r rendered) map[string]string {
	t.Helper()
	d, ok := r.deployments["continuum-agent"]
	if !ok {
		t.Fatal("no agent deployment rendered")
	}
	env := map[string]string{}
	for _, e := range containerNamed(t, d.Spec.Template.Spec.Containers, "agent").Env {
		env[e.Name] = e.Value
	}
	return env
}

// The agent reports what its install can do for the cluster-link telemetry (Heartbeat.link_capabilities), so the Sites screen can say
// which step of setting a pair up is missing. Three of the five facts come from the chart: whether the iperf3 capacity tests are
// part of the install, and whether what the cluster measures leaves it through the cluster collector. Probing is the existing
// CONTINUUM_LINK_SERVE_PEERS and the metrics being served is CONTINUUM_LINK_METRICS_LISTEN.

func TestTheAgentIsToldWhetherTheLinkMetricsLeaveTheCluster(t *testing.T) {
	env := agentEnv(t, render(t, clusterLinksArgs()...))
	if env["CONTINUUM_LINK_TELEMETRY_EXPORT"] != "true" {
		t.Errorf("CONTINUUM_LINK_TELEMETRY_EXPORT = %q with the link metrics on and a destination set", env["CONTINUUM_LINK_TELEMETRY_EXPORT"])
	}
	if _, ok := env["CONTINUUM_LINK_CAPACITY"]; ok {
		t.Error("CONTINUUM_LINK_CAPACITY is set without the capacity tests")
	}
	if env["CONTINUUM_LINK_SERVE_PEERS"] != "" {
		t.Errorf("probing reported on without the probe: %q", env["CONTINUUM_LINK_SERVE_PEERS"])
	}
}

func TestTheLinkMetricsThatHaveOnlyTheirOwnRouteAreStillExported(t *testing.T) {
	// No default destination at all: the link metrics go out through the route of their signal type alone.
	r := render(t, "--set", "telemetry.export.routes.metrics.endpoint=metrics.example:4317", "--set", "telemetry.clusterLinks.metrics.enabled=true", "--set", "flowObserver.enabled=true")
	if got := agentEnv(t, r)["CONTINUUM_LINK_TELEMETRY_EXPORT"]; got != "true" {
		t.Errorf("CONTINUUM_LINK_TELEMETRY_EXPORT = %q with a route for the metrics", got)
	}
}

func TestNothingIsReportedWithoutClusterLinks(t *testing.T) {
	// Another signal sends telemetry, but the link metrics are off: the cluster does not export link metrics.
	r := render(t, append(append([]string{}, agentTel...), "--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	env := agentEnv(t, r)
	for _, k := range []string{"CONTINUUM_LINK_TELEMETRY_EXPORT", "CONTINUUM_LINK_CAPACITY", "CONTINUUM_LINK_METRICS_LISTEN"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s = %q while Cluster links is off", k, v)
		}
	}
	r = render(t) // no telemetry at all
	for _, k := range []string{"CONTINUUM_LINK_TELEMETRY_EXPORT", "CONTINUUM_LINK_CAPACITY"} {
		if v, ok := agentEnv(t, r)[k]; ok {
			t.Errorf("%s = %q with no telemetry", k, v)
		}
	}
}

func TestTheAgentIsToldWhenTheCapacityTestsArePartOfTheInstall(t *testing.T) {
	env := agentEnv(t, render(t, capArgs()...))
	if env["CONTINUUM_LINK_CAPACITY"] != "true" || env["CONTINUUM_LINK_SERVE_PEERS"] != "true" || env["CONTINUUM_LINK_TELEMETRY_EXPORT"] != "true" {
		t.Errorf("capacity %q, probing %q, export %q: all three should be on", env["CONTINUUM_LINK_CAPACITY"], env["CONTINUUM_LINK_SERVE_PEERS"], env["CONTINUUM_LINK_TELEMETRY_EXPORT"])
	}
	env = agentEnv(t, render(t, probeArgs()...))
	if _, ok := env["CONTINUUM_LINK_CAPACITY"]; ok || env["CONTINUUM_LINK_SERVE_PEERS"] != "true" {
		t.Errorf("probing without capacity: capacity %q, probing %q", env["CONTINUUM_LINK_CAPACITY"], env["CONTINUUM_LINK_SERVE_PEERS"])
	}
}

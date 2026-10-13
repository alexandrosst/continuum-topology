package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// probeArgs turns the active link probe on, with what it needs.
func probeArgs(extra ...string) []string {
	return clusterLinksArgs(append([]string{"telemetry.clusterLinks.probe.enabled=true", "access.tier=2"}, extra...)...)
}

func TestTheLinkProbeIsOffByDefaultAndRendersNothing(t *testing.T) {
	r := render(t, clusterLinksArgs()...)
	for _, n := range []string{"continuum-link-prober"} {
		if _, ok := r.deployments[n]; ok {
			t.Errorf("%s rendered while the probe is off", n)
		}
	}
	if _, ok := r.daemonsets["continuum-link-responder"]; ok {
		t.Error("the responder rendered while the probe is off")
	}
	for _, e := range r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CONTINUUM_LINK_SERVE_PEERS" {
			t.Error("the agent serves peers while the probe is off")
		}
	}
}

func TestTheLinkProbeNeedsTheLinkMetrics(t *testing.T) {
	out, err := helmTemplate(t, append(append([]string{}, agentTel...), "--set", "telemetry.clusterLinks.probe.enabled=true", "--set", "flowObserver.enabled=true")...)
	if err == nil || !strings.Contains(out, "telemetry.clusterLinks.metrics.enabled") {
		t.Fatalf("rendered without the link metrics (err %v):\n%s", err, out)
	}
}

func TestTheProberAndResponderAreSeparateLeastPrivilegeWorkloads(t *testing.T) {
	r := render(t, probeArgs()...)

	prober, ok := r.deployments["continuum-link-prober"]
	if !ok {
		t.Fatal("no prober")
	}
	if *prober.Spec.Replicas != 1 {
		t.Errorf("replicas = %d: a second prober would measure every link twice", *prober.Spec.Replicas)
	}
	ps := prober.Spec.Template.Spec
	if ps.HostNetwork || (ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken) {
		t.Error("the prober must not use the host network or have a ServiceAccount token")
	}
	c := ps.Containers[0]
	if strings.Join(c.Args, " ") != "link-prober" {
		t.Errorf("args = %v", c.Args)
	}
	if !strings.Contains(c.Image, "link") {
		t.Errorf("image = %q: the prober needs the link image, which has OWAMP", c.Image)
	}
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem || len(c.SecurityContext.Capabilities.Drop) == 0 {
		t.Errorf("security context = %+v", c.SecurityContext)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if !strings.HasPrefix(env["CONTINUUM_LINK_AGENT_URL"], "http://continuum-agent-link-metrics.") || !strings.HasSuffix(env["CONTINUUM_LINK_AGENT_URL"], ".svc:9104") {
		t.Errorf("agent url = %q", env["CONTINUUM_LINK_AGENT_URL"])
	}
	for k, want := range map[string]string{"CONTINUUM_LINK_PACKETS": "100", "CONTINUUM_LINK_SPACING": "100ms", "CONTINUUM_LINK_INTERVAL": "1m", "CONTINUUM_LINK_RESPONDER_PORT": "8861"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}

	resp, ok := r.daemonsets["continuum-link-responder"]
	if !ok {
		t.Fatal("no responder")
	}
	rs := resp.Spec.Template.Spec
	if !rs.HostNetwork {
		t.Error("the responder must be in the host network: peers are given node addresses")
	}
	if rs.AutomountServiceAccountToken == nil || *rs.AutomountServiceAccountToken {
		t.Error("the responder has a ServiceAccount token")
	}
	rc := rs.Containers[0]
	if len(rc.Command) != 1 || rc.Command[0] != "/usr/local/bin/owampd" {
		t.Errorf("command = %v", rc.Command)
	}
	if rc.SecurityContext.Capabilities.Drop[0] != "ALL" || len(rc.SecurityContext.Capabilities.Add) != 0 {
		t.Errorf("capabilities = %+v: the responder needs none (its port is above 1024)", rc.SecurityContext.Capabilities)
	}
	if rs.SecurityContext == nil || rs.SecurityContext.RunAsNonRoot == nil || !*rs.SecurityContext.RunAsNonRoot {
		t.Error("the responder runs as root")
	}
	if !strings.Contains(strings.Join(rc.Args, " "), ":8861") {
		t.Errorf("args = %v", rc.Args)
	}
	if len(rs.Tolerations) == 0 {
		t.Error("the responder does not tolerate taints, so tainted nodes would not answer")
	}
	// the agent serves the peer list to the prober
	env = map[string]string{}
	for _, e := range r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["CONTINUUM_LINK_SERVE_PEERS"] != "true" {
		t.Error("the agent does not answer the prober")
	}
}

func TestTheResponderIsOpenOrLimitedToTheNetworksYouName(t *testing.T) {
	open := render(t, probeArgs()...).configmaps["continuum-link-responder"].Data["owamp-server.limits"]
	if !strings.Contains(open, "assign default regular") || strings.Contains(open, "refused") {
		t.Errorf("default limits:\n%s", open)
	}
	limited := render(t, probeArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24", "telemetry.clusterLinks.probe.responder.allowFrom[1]=198.51.100.7/32")...).configmaps["continuum-link-responder"].Data["owamp-server.limits"]
	for _, want := range []string{"assign default refused", "assign net 203.0.113.0/24 regular", "assign net 198.51.100.7/32 regular"} {
		if !strings.Contains(limited, want) {
			t.Errorf("missing %q in:\n%s", want, limited)
		}
	}
	if strings.Contains(limited, "assign default regular") {
		t.Errorf("a limited responder still has an open default:\n%s", limited)
	}
	if _, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=not-a-cidr")...); err == nil {
		t.Error("a non-CIDR was accepted")
	}
}

func TestTheProbeSettingsAreChecked(t *testing.T) {
	for name, set := range map[string]string{
		"packets too few":   "telemetry.clusterLinks.probe.packets=2",
		"dscp out of range": "telemetry.clusterLinks.probe.dscp=64",
		"bad spacing":       "telemetry.clusterLinks.probe.spacing=fast",
		"bad path interval": "telemetry.clusterLinks.probe.pathInterval=often",
		"privileged port":   "telemetry.clusterLinks.probe.responder.port=861",
		"one test port":     "telemetry.clusterLinks.probe.responder.testPorts=18900-18900",
		"reversed ports":    "telemetry.clusterLinks.probe.responder.testPorts=18999-18900",
		"ports not a range": "telemetry.clusterLinks.probe.responder.testPorts=18900",
		"bad digest":        "telemetry.clusterLinks.probe.image.digest=latest",
	} {
		if out, err := helmTemplate(t, probeArgs(set)...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
	if out, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.interval=30s", "telemetry.clusterLinks.probe.packets=50")...); err != nil || !strings.Contains(out, `value: "30s"`) {
		t.Fatalf("valid settings refused: %v\n%s", err, out)
	}
}

func TestTheProbeImageHonoursTheGlobalRegistryAndADigest(t *testing.T) {
	r := render(t, probeArgs("global.imageRegistry=registry.corp/mirror")...)
	if got := r.deployments["continuum-link-prober"].Spec.Template.Spec.Containers[0].Image; !strings.HasPrefix(got, "registry.corp/mirror/") {
		t.Errorf("image = %q", got)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	r = render(t, probeArgs("telemetry.clusterLinks.probe.image.digest="+digest)...)
	if got := r.daemonsets["continuum-link-responder"].Spec.Template.Spec.Containers[0].Image; !strings.HasSuffix(got, "@"+digest) {
		t.Errorf("image = %q", got)
	}
}

func TestTheProbeIsScrapedAndPermittedThroughTheNetworkPolicies(t *testing.T) {
	args := probeArgs(
		"flowObserver.networkPolicy=true", "flowObserver.nodeCIDRs[0]=10.0.0.0/8",
		"networkPolicy.telemetryEgress.enabled=true", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317")
	r := render(t, args...)

	// the prober may ask the agent on 9104; the collector may scrape the prober on 9105
	from := map[string]bool{}
	for _, rule := range r.policies["continuum-agent-probe"].Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9104 {
				for _, f := range rule.From {
					if f.PodSelector != nil {
						from[f.PodSelector.MatchLabels["app.kubernetes.io/name"]] = true
					}
				}
			}
		}
	}
	if !from["continuum-link-prober"] || !from["continuum-telemetry-cluster"] || len(from) != 2 {
		t.Errorf("port 9104 is open to %v, want the cluster collector and the prober only", from)
	}
	scraped := false
	for _, rule := range r.policies["continuum-telemetry-scrape-egress"].Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9105 && len(rule.To) == 1 && rule.To[0].PodSelector != nil && rule.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] == "continuum-link-prober" {
				scraped = true
			}
		}
	}
	if !scraped {
		t.Error("the collector's egress policy does not allow the prober's port 9105")
	}

	var svc *corev1.Service
	var cfg map[string]any
	for _, d := range renderDocs(t, args...) {
		switch {
		case d.kind() == "Service" && d.name() == "continuum-link-prober":
			svc = &corev1.Service{}
			decodeInto(t, d, svc)
		case d.kind() == "ConfigMap" && d.name() == "continuum-telemetry-cluster-config":
			cfg = otelConfig(t, map[string]string{"otel-collector-config.yaml": d["data"].(map[string]any)["otel-collector-config.yaml"].(string)})
		}
	}
	if svc == nil || svc.Spec.Ports[0].Port != 9105 {
		t.Fatalf("service = %+v", svc)
	}
	jobs := sub(t, cfg, "receivers")["prometheus/infra"].(map[string]any)["config"].(map[string]any)["scrape_configs"].([]any)
	found := false
	for _, j := range jobs {
		if j.(map[string]any)["job_name"] == "cluster-links-probe" {
			found = true
			target := j.(map[string]any)["static_configs"].([]any)[0].(map[string]any)["targets"].([]any)[0].(string)
			if !strings.HasPrefix(target, "continuum-link-prober.") || !strings.HasSuffix(target, ".svc:9105") {
				t.Errorf("target = %q", target)
			}
		}
	}
	if !found {
		t.Errorf("no cluster-links-probe job in %v", jobs)
	}
}

func TestThePreflightNamesTheResponderWhereHostAccessIsRefused(t *testing.T) {
	out, err := helmTemplate(t, probeArgs("preflight.assumeEnforce=baseline")...)
	if err == nil || !strings.Contains(out, "the link responder") {
		t.Fatalf("a namespace that refuses host networking was not warned about the responder (err %v):\n%s", err, out)
	}
}

func TestTheRouteTraceIntervalReachesTheProberAndCanBeSwitchedOff(t *testing.T) {
	env := func(args ...string) map[string]string {
		out := map[string]string{}
		for _, e := range render(t, probeArgs(args...)...).deployments["continuum-link-prober"].Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	if got := env()["CONTINUUM_LINK_PATH_INTERVAL"]; got != "10m" {
		t.Errorf("default = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.pathInterval=30m")["CONTINUUM_LINK_PATH_INTERVAL"]; got != "30m" {
		t.Errorf("set = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.pathInterval=0")["CONTINUUM_LINK_PATH_INTERVAL"]; got != "0" {
		t.Errorf("off = %q", got)
	}
}

func TestTheConnectTimingIsOnByDefaultAndTheSettingsReachTheProber(t *testing.T) {
	env := func(args ...string) map[string]string {
		out := map[string]string{}
		for _, e := range render(t, probeArgs(args...)...).deployments["continuum-link-prober"].Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	e := env()
	if e["CONTINUUM_LINK_TCP_INTERVAL"] != "15s" || e["CONTINUUM_LINK_TCP_TIMEOUT"] != "3s" {
		t.Errorf("defaults: %v", e)
	}
	if got := env("telemetry.clusterLinks.probe.tcpConnect.interval=0")["CONTINUUM_LINK_TCP_INTERVAL"]; got != "0" {
		t.Errorf("off = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.tcpConnect.interval=1m")["CONTINUUM_LINK_TCP_INTERVAL"]; got != "1m" {
		t.Errorf("set = %q", got)
	}
	if _, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.tcpConnect.interval=often")...); err == nil {
		t.Error("a bad interval was accepted")
	}
}

func TestThePingBaselineIsOffByDefaultAndWhenOnNeedsOnlyASysctlAndAScratchDirectory(t *testing.T) {
	off := render(t, probeArgs()...).deployments["continuum-link-prober"].Spec.Template.Spec
	if len(off.SecurityContext.Sysctls) != 0 {
		t.Errorf("sysctls without pings: %v", off.SecurityContext.Sysctls)
	}
	for _, e := range off.Containers[0].Env {
		if strings.HasPrefix(e.Name, "CONTINUUM_LINK_ICMP") {
			t.Errorf("%s set while pings are off", e.Name)
		}
	}
	on := render(t, probeArgs("telemetry.clusterLinks.probe.icmp.enabled=true", "telemetry.clusterLinks.probe.icmp.hostsPerPeer=2", "telemetry.clusterLinks.probe.icmp.interval=5s")...).deployments["continuum-link-prober"].Spec.Template.Spec
	if got := on.SecurityContext.Sysctls; len(got) != 1 || got[0].Name != "net.ipv4.ping_group_range" || got[0].Value != "0 2147483647" {
		t.Errorf("sysctls = %v", got)
	}
	env := map[string]string{}
	for _, e := range on.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["CONTINUUM_LINK_ICMP"] != "true" || env["CONTINUUM_LINK_ICMP_HOSTS"] != "2" || env["CONTINUUM_LINK_ICMP_INTERVAL"] != "5s" || env["CONTINUUM_LINK_ICMP_DIR"] != "/icmp" {
		t.Errorf("env = %v", env)
	}
	// the pod stays unprivileged: no capability, a read-only root, and the only writable place is the small scratch volume
	c := on.Containers[0]
	if c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem || c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Add) != 0 {
		t.Errorf("the prober must not gain privileges for pings: %+v", c.SecurityContext)
	}
	mounted := false
	for _, m := range c.VolumeMounts {
		mounted = mounted || (m.Name == "icmp" && m.MountPath == "/icmp" && !m.ReadOnly)
	}
	if !mounted {
		t.Errorf("no scratch directory for the ping configuration: %v", c.VolumeMounts)
	}
	for name, set := range map[string]string{
		"size too small": "telemetry.clusterLinks.probe.icmp.size=10",
		"too many hosts": "telemetry.clusterLinks.probe.icmp.hostsPerPeer=20",
		"bad interval":   "telemetry.clusterLinks.probe.icmp.interval=fast",
	} {
		if out, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.icmp.enabled=true", set)...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
}

// The agent leaves the prober's own traffic out of the link metrics, so it is told which ports that is: the control port,
// the test-packet range, and the throughput port when those tests are on. Without the probe it is told nothing.
func TestTheAgentIsToldTheProbersPorts(t *testing.T) {
	agentEnv := func(r rendered) map[string]string {
		env := map[string]string{}
		for _, e := range r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0].Env {
			env[e.Name] = e.Value
		}
		return env
	}
	if got := agentEnv(render(t, probeArgs()...))["CONTINUUM_LINK_PROBER_PORTS"]; got != "8861,18900-18999" {
		t.Errorf("probe on: ports = %q", got)
	}
	r := render(t, capArgs("telemetry.clusterLinks.probe.responder.port=9100", "telemetry.clusterLinks.probe.responder.testPorts=20000-20009", "telemetry.clusterLinks.probe.capacity.port=9000")...)
	if got := agentEnv(r)["CONTINUUM_LINK_PROBER_PORTS"]; got != "9100,20000-20009,9000" {
		t.Errorf("throughput tests on: ports = %q", got)
	}
	if _, ok := agentEnv(render(t, clusterLinksArgs()...))["CONTINUUM_LINK_PROBER_PORTS"]; ok {
		t.Error("the agent is told the prober's ports with the probe off")
	}
}

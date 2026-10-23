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
	for _, n := range []string{"ikhnos-link-prober"} {
		if _, ok := r.deployments[n]; ok {
			t.Errorf("%s rendered while the probe is off", n)
		}
	}
	if _, ok := r.daemonsets["ikhnos-link-responder"]; ok {
		t.Error("the responder rendered while the probe is off")
	}
	for _, e := range r.deployments["ikhnos-agent"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "IKHNOS_LINK_SERVE_PEERS" {
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

	prober, ok := r.deployments["ikhnos-link-prober"]
	if !ok {
		t.Fatal("no prober")
	}
	if *prober.Spec.Replicas != 1 {
		t.Errorf("replicas = %d: a second prober would measure every link twice", *prober.Spec.Replicas)
	}
	ps := prober.Spec.Template.Spec
	// OWAMP in open mode compares the address owping announces with the one the responder sees; behind a CNI's masquerade a pod's
	// address is never that one, so every reverse test is refused and every forward test counts all packets lost.
	if !ps.HostNetwork || ps.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Errorf("hostNetwork = %v, dnsPolicy = %q: the prober must be in the host network (its agent URL is a Service name)", ps.HostNetwork, ps.DNSPolicy)
	}
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Error("the prober has a ServiceAccount token")
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
	if !strings.HasPrefix(env["IKHNOS_LINK_AGENT_URL"], "http://ikhnos-agent-link-metrics.") || !strings.HasSuffix(env["IKHNOS_LINK_AGENT_URL"], ".svc:9104") {
		t.Errorf("agent url = %q", env["IKHNOS_LINK_AGENT_URL"])
	}
	for k, want := range map[string]string{"IKHNOS_LINK_PACKETS": "100", "IKHNOS_LINK_SPACING": "100ms", "IKHNOS_LINK_INTERVAL": "1m", "IKHNOS_LINK_RESPONDER_PORT": "8861"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}

	resp, ok := r.daemonsets["ikhnos-link-responder"]
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
	for _, e := range r.deployments["ikhnos-agent"].Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["IKHNOS_LINK_SERVE_PEERS"] != "true" {
		t.Error("the agent does not answer the prober")
	}
}

func TestTheResponderIsOpenOrLimitedToTheNetworksYouName(t *testing.T) {
	open := render(t, probeArgs()...).configmaps["ikhnos-link-responder"].Data["owamp-server.limits"]
	if !strings.Contains(open, "assign default regular") || strings.Contains(open, "refused") {
		t.Errorf("default limits:\n%s", open)
	}
	limited := render(t, probeArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24", "telemetry.clusterLinks.probe.responder.allowFrom[1]=198.51.100.7/32")...).configmaps["ikhnos-link-responder"].Data["owamp-server.limits"]
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
	if got := r.deployments["ikhnos-link-prober"].Spec.Template.Spec.Containers[0].Image; !strings.HasPrefix(got, "registry.corp/mirror/") {
		t.Errorf("image = %q", got)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	r = render(t, probeArgs("telemetry.clusterLinks.probe.image.digest="+digest)...)
	if got := r.daemonsets["ikhnos-link-responder"].Spec.Template.Spec.Containers[0].Image; !strings.HasSuffix(got, "@"+digest) {
		t.Errorf("image = %q", got)
	}
}

func TestTheProbeIsScrapedAndPermittedThroughTheNetworkPolicies(t *testing.T) {
	args := probeArgs(
		"flowObserver.networkPolicy=true", "flowObserver.nodeCIDRs[0]=10.0.0.0/8",
		"networkPolicy.telemetryEgress.enabled=true", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317")
	r := render(t, args...)

	// The prober and the iperf3 pods run in the host network, so the agent allows them by source address, not by pod selector; the
	// collector scrapes the prober by node address and the port the prober listens on.
	from := map[string]bool{}
	cidrs := map[string]bool{}
	for _, rule := range r.policies["ikhnos-agent-probe"].Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9104 {
				for _, f := range rule.From {
					if f.PodSelector != nil {
						from[f.PodSelector.MatchLabels["app.kubernetes.io/name"]] = true
					}
					if f.IPBlock != nil {
						cidrs[f.IPBlock.CIDR] = true
					}
				}
			}
		}
	}
	if !from["ikhnos-telemetry-cluster"] || from["ikhnos-link-prober"] || len(from) != 1 {
		t.Errorf("port 9104 is open by pod selector to %v, want the cluster collector only (the prober has no pod identity)", from)
	}
	if !cidrs["10.0.0.0/8"] || len(cidrs) != 1 {
		t.Errorf("port 9104 is open by address to %v, want flowObserver.nodeCIDRs for the prober", cidrs)
	}
	scraped := false
	for _, rule := range r.policies["ikhnos-telemetry-scrape-egress"].Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9105 && len(rule.To) == 1 && rule.To[0].PodSelector == nil && rule.To[0].IPBlock != nil && rule.To[0].IPBlock.CIDR == "10.0.0.0/8" {
				scraped = true
			}
		}
	}
	if !scraped {
		t.Error("the collector's egress policy does not allow the prober's port 9105 by node address")
	}

	var svc *corev1.Service
	var cfg map[string]any
	for _, d := range renderDocs(t, args...) {
		switch {
		case d.kind() == "Service" && d.name() == "ikhnos-link-prober":
			svc = &corev1.Service{}
			decodeInto(t, d, svc)
		case d.kind() == "ConfigMap" && d.name() == "ikhnos-telemetry-cluster-config":
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
			if !strings.HasPrefix(target, "ikhnos-link-prober.") || !strings.HasSuffix(target, ".svc:9105") {
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
		for _, e := range render(t, probeArgs(args...)...).deployments["ikhnos-link-prober"].Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	if got := env()["IKHNOS_LINK_PATH_INTERVAL"]; got != "10m" {
		t.Errorf("default = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.pathInterval=30m")["IKHNOS_LINK_PATH_INTERVAL"]; got != "30m" {
		t.Errorf("set = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.pathInterval=0")["IKHNOS_LINK_PATH_INTERVAL"]; got != "0" {
		t.Errorf("off = %q", got)
	}
}

func TestTheConnectTimingIsOnByDefaultAndTheSettingsReachTheProber(t *testing.T) {
	env := func(args ...string) map[string]string {
		out := map[string]string{}
		for _, e := range render(t, probeArgs(args...)...).deployments["ikhnos-link-prober"].Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	e := env()
	if e["IKHNOS_LINK_TCP_INTERVAL"] != "15s" || e["IKHNOS_LINK_TCP_TIMEOUT"] != "3s" {
		t.Errorf("defaults: %v", e)
	}
	if got := env("telemetry.clusterLinks.probe.tcpConnect.interval=0")["IKHNOS_LINK_TCP_INTERVAL"]; got != "0" {
		t.Errorf("off = %q", got)
	}
	if got := env("telemetry.clusterLinks.probe.tcpConnect.interval=1m")["IKHNOS_LINK_TCP_INTERVAL"]; got != "1m" {
		t.Errorf("set = %q", got)
	}
	if _, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.tcpConnect.interval=often")...); err == nil {
		t.Error("a bad interval was accepted")
	}
}

// The pings were tried and dropped: the connect timing and the one-way probe cover what they would say. Their values are refused,
// so a values file written for them fails at render time rather than being ignored.
func TestThePingBaselineIsGone(t *testing.T) {
	if out, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.icmp.enabled=true")...); err == nil {
		t.Errorf("icmp values accepted\n%s", out)
	}
	if len(render(t, probeArgs()...).deployments["ikhnos-link-prober"].Spec.Template.Spec.SecurityContext.Sysctls) != 0 {
		t.Error("the prober pod sets a sysctl")
	}
}

// The agent leaves the prober's own traffic out of the link metrics, so it is told which ports that is: the control port,
// the test-packet range, and the throughput port when those tests are on. Without the probe it is told nothing.
func TestTheAgentIsToldTheProbersPorts(t *testing.T) {
	agentEnv := func(r rendered) map[string]string {
		env := map[string]string{}
		for _, e := range r.deployments["ikhnos-agent"].Spec.Template.Spec.Containers[0].Env {
			env[e.Name] = e.Value
		}
		return env
	}
	if got := agentEnv(render(t, probeArgs()...))["IKHNOS_LINK_PROBER_PORTS"]; got != "8861,18900-18919" {
		t.Errorf("probe on: ports = %q", got)
	}
	r := render(t, capArgs("telemetry.clusterLinks.probe.responder.port=9100", "telemetry.clusterLinks.probe.responder.testPorts=20000-20009", "telemetry.clusterLinks.probe.capacity.port=9000")...)
	if got := agentEnv(r)["IKHNOS_LINK_PROBER_PORTS"]; got != "9100,20000-20009,9000" {
		t.Errorf("throughput tests on: ports = %q", got)
	}
	if _, ok := agentEnv(render(t, clusterLinksArgs()...))["IKHNOS_LINK_PROBER_PORTS"]; ok {
		t.Error("the agent is told the prober's ports with the probe off")
	}
}

// The prober runs measurement tools that parse what other clusters' responders send, so it must not hold the secret that
// signs flow reports: it gets a secret of its own, used only to ask the agent for the peers.
func TestTheProberMountsItsOwnSecretAndNotTheFlowSecret(t *testing.T) {
	r := render(t, probeArgs()...)
	mounted := func(spec corev1.PodSpec) (secrets []string) {
		for _, v := range spec.Volumes {
			if v.Secret != nil {
				secrets = append(secrets, v.Secret.SecretName)
			}
		}
		return secrets
	}
	ps := r.deployments["ikhnos-link-prober"].Spec.Template.Spec
	if got := mounted(ps); len(got) != 1 || got[0] != "ikhnos-agent-link-peers" {
		t.Errorf("the prober mounts secrets %v, want only ikhnos-agent-link-peers", got)
	}
	for _, v := range ps.Volumes {
		if v.Secret != nil && v.Secret.SecretName == "ikhnos-agent-flows" {
			t.Errorf("the prober mounts the flow secret: %+v", v)
		}
	}
	for _, c := range ps.Containers {
		for _, e := range c.Env {
			if e.Name == "IKHNOS_FLOW_SECRET_FILE" || (e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "ikhnos-agent-flows") {
				t.Errorf("the prober is handed the flow secret: %+v", e)
			}
			if e.Name == "IKHNOS_LINK_PEERS_SECRET_FILE" && e.Value != "/secret/secret" {
				t.Errorf("peers secret file = %q", e.Value)
			}
		}
	}
	if _, ok := r.deployments["ikhnos-link-prober"].Spec.Template.Annotations["checksum/flow-secret"]; ok {
		t.Error("the prober pod rolls on the flow secret, which it does not read")
	}
	if r.deployments["ikhnos-link-prober"].Spec.Template.Annotations["checksum/link-peers-secret"] == "" {
		t.Error("the prober does not roll when its secret is replaced")
	}

	// the agent verifies with that secret, still reads the flow secret for the collectors, and the Secret is the chart's own
	ag := r.deployments["ikhnos-agent"].Spec.Template
	env := map[string]string{}
	for _, e := range ag.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["IKHNOS_LINK_PEERS_SECRET_FILE"] != "/link-peers/secret" || env["IKHNOS_FLOW_SECRET_FILE"] != "/flows/secret" {
		t.Errorf("agent env: peers %q, flow %q", env["IKHNOS_LINK_PEERS_SECRET_FILE"], env["IKHNOS_FLOW_SECRET_FILE"])
	}
	if got := mounted(ag.Spec); len(got) != 2 || got[0] != "ikhnos-agent-flows" || got[1] != "ikhnos-agent-link-peers" {
		t.Errorf("the agent mounts secrets %v", got)
	}
	mounts := map[string]string{}
	for _, m := range ag.Spec.Containers[0].VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	if mounts["link-peers-secret"] != "/link-peers" || mounts["flow-secret"] != "/flows" {
		t.Errorf("agent mounts = %v", mounts)
	}
	if ag.Annotations["checksum/link-peers-secret"] == "" {
		t.Error("the agent does not roll when the peers secret is replaced")
	}
	peers, flows := r.secrets["ikhnos-agent-link-peers"], r.secrets["ikhnos-agent-flows"]
	if len(peers.StringData["secret"]) < 32 || peers.StringData["secret"] == flows.StringData["secret"] {
		t.Errorf("peers secret %q, flow secret %q: the first must be generated and different", peers.StringData["secret"], flows.StringData["secret"])
	}

	// with the probe off there is no such Secret, no mount and no variable
	off := render(t, clusterLinksArgs()...)
	if _, ok := off.secrets["ikhnos-agent-link-peers"]; ok {
		t.Error("the peers secret exists with the probe off")
	}
	offAgent := off.deployments["ikhnos-agent"].Spec.Template
	if got := mounted(offAgent.Spec); len(got) != 1 || got[0] != "ikhnos-agent-flows" {
		t.Errorf("probe off: the agent mounts %v", got)
	}
	if offAgent.Annotations["checksum/link-peers-secret"] != "" {
		t.Error("probe off: the agent has a peers checksum")
	}
}

func TestThePeersSecretIsGivenKeptOnUpgradeAndNeverTheFlowSecret(t *testing.T) {
	r := render(t, probeArgs("telemetry.clusterLinks.probe.peersSecret=an-operator-chosen-peers-secret-1234")...)
	if got := r.secrets["ikhnos-agent-link-peers"].StringData["secret"]; got != "an-operator-chosen-peers-secret-1234" {
		t.Errorf("secret = %q", got)
	}
	a := render(t, probeArgs("telemetry.clusterLinks.probe.peersSecret=one-secret-of-sixteen-plus")...)
	b := render(t, probeArgs("telemetry.clusterLinks.probe.peersSecret=two-secret-of-sixteen-plus")...)
	if a.deployments["ikhnos-agent"].Spec.Template.Annotations["checksum/link-peers-secret"] == b.deployments["ikhnos-agent"].Spec.Template.Annotations["checksum/link-peers-secret"] ||
		a.deployments["ikhnos-link-prober"].Spec.Template.Annotations["checksum/link-peers-secret"] == b.deployments["ikhnos-link-prober"].Spec.Template.Annotations["checksum/link-peers-secret"] {
		t.Error("a new peers secret does not roll the agent and the prober")
	}
	x := render(t, probeArgs()...).deployments["ikhnos-agent"].Spec.Template.Annotations["checksum/link-peers-secret"]
	y := render(t, probeArgs()...).deployments["ikhnos-agent"].Spec.Template.Annotations["checksum/link-peers-secret"]
	if x != y {
		t.Error("a generated peers secret changes the checksum on every render, so every upgrade would roll the pods")
	}

	out, err := helmTemplate(t, probeArgs("telemetry.clusterLinks.probe.peersSecret=the-very-same-secret-value-1", "flowObserver.secret=the-very-same-secret-value-1")...)
	if err == nil || !strings.Contains(out, "must differ from flowObserver.secret") {
		t.Errorf("rendered with one secret for both (err %v):\n%s", err, out)
	}

	// an upgrade keeps the generated value that is already in the cluster
	k, kc := startFakeKubernetes(t)
	k.set("ikhnos-agent-link-peers", map[string]string{"secret": "kept-from-the-first-install-0123456789"})
	args := append(append([]string{}, baseSet...), probeArgs()...)
	if out := helmTemplateInCluster(t, Agent, kc, args...); !strings.Contains(out, "kept-from-the-first-install-0123456789") {
		t.Errorf("the existing peers secret was not kept:\n%s", out)
	}
}

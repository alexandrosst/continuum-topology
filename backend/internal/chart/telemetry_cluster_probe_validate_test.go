package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// proberEnvWith is the prober container's environment of a render.
func proberEnvWith(t *testing.T, args ...string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, e := range render(t, probeArgs(args...)...).deployments["continuum-link-prober"].Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			env[e.Name] = "fieldRef:" + e.ValueFrom.FieldRef.FieldPath
		}
	}
	return env
}

// The responder is the other half of the host-network pair: it stays there, in OWAMP's open mode, on the configured port.
func TestTheResponderKeepsTheHostNetworkAndTheOpenModeArguments(t *testing.T) {
	rs := render(t, probeArgs()...).daemonsets["continuum-link-responder"].Spec.Template.Spec
	if !rs.HostNetwork || rs.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Errorf("hostNetwork = %v, dnsPolicy = %q", rs.HostNetwork, rs.DNSPolicy)
	}
	if got, want := strings.Join(rs.Containers[0].Args, " "), "-Z -a O -c /etc/owamp -d /data -R /run/owamp -S :8861"; got != want {
		t.Errorf("owampd args = %q, want %q", got, want)
	}
	rs = render(t, probeArgs("telemetry.clusterLinks.probe.responder.port=9861")...).daemonsets["continuum-link-responder"].Spec.Template.Spec
	if got := strings.Join(rs.Containers[0].Args, " "); !strings.HasSuffix(got, "-Z -a O -c /etc/owamp -d /data -R /run/owamp -S :9861") && !strings.HasSuffix(got, "-S :9861") {
		t.Errorf("owampd args = %q: the port is not followed", got)
	}
}

// The prober's metrics port is a port on the node it runs on, bound to that node's address (the pod IP of a host-network pod),
// and it can be moved when something else on the nodes has 9105.
func TestTheProberServesItsMetricsOnTheNodeAddressAtTheListenPort(t *testing.T) {
	env := proberEnvWith(t)
	if env["POD_IP"] != "fieldRef:status.podIP" || env["CONTINUUM_LINK_PROBE_LISTEN"] != "$(POD_IP):9105" {
		t.Errorf("default: POD_IP = %q, listen = %q", env["POD_IP"], env["CONTINUUM_LINK_PROBE_LISTEN"])
	}

	args := probeArgs("telemetry.clusterLinks.probe.listenPort=9200",
		"flowObserver.networkPolicy=true", "flowObserver.nodeCIDRs[0]=10.0.0.0/8",
		"networkPolicy.telemetryEgress.enabled=true", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317")
	r := render(t, args...)
	c := r.deployments["continuum-link-prober"].Spec.Template.Spec.Containers[0]
	got := map[string]string{}
	for _, e := range c.Env {
		got[e.Name] = e.Value
	}
	if got["CONTINUUM_LINK_PROBE_LISTEN"] != "$(POD_IP):9200" {
		t.Errorf("listen = %q", got["CONTINUUM_LINK_PROBE_LISTEN"])
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9200 || c.ReadinessProbe == nil || c.ReadinessProbe.TCPSocket == nil {
		t.Errorf("ports = %+v, readiness = %+v", c.Ports, c.ReadinessProbe)
	}
	// The Service keeps 9105, which is the scrape target; it forwards to the port the pod listens on.
	for _, d := range renderDocs(t, args...) {
		if d.kind() == "Service" && d.name() == "continuum-link-prober" {
			var svc corev1.Service
			decodeInto(t, d, &svc)
			if p := svc.Spec.Ports[0]; p.Port != 9105 || p.TargetPort.IntValue() != 9200 {
				t.Errorf("service port %d -> %v, want 9105 -> 9200", p.Port, p.TargetPort)
			}
		}
	}
	// Policies see the backend's address and port, not the Service's.
	scraped := false
	for _, rule := range r.policies["continuum-telemetry-scrape-egress"].Spec.Egress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9200 && len(rule.To) == 1 && rule.To[0].IPBlock != nil {
				scraped = true
			}
			if p.Port != nil && p.Port.IntValue() == 9105 {
				t.Errorf("egress still allows the Service port 9105: %+v", rule)
			}
		}
	}
	if !scraped {
		t.Error("the collector may not scrape the prober's listen port")
	}

	for name, set := range map[string][]string{
		"responder port":  {"telemetry.clusterLinks.probe.listenPort=8861"},
		"capacity port":   {"telemetry.clusterLinks.probe.capacity.enabled=true", "telemetry.clusterLinks.probe.listenPort=8862"},
		"privileged port": {"telemetry.clusterLinks.probe.listenPort=905"},
	} {
		if out, err := helmTemplate(t, probeArgs(set...)...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
}

// With no NetworkPolicy asked for on the node collectors the agent's policy (made for the node probe here) still has to let the
// prober in, from any address, as the other host-network senders are.
func TestTheAgentLetsTheHostNetworkProberInFromAnyAddressWhenNoNodeCIDRsAreSet(t *testing.T) {
	r := render(t, probeArgs("nodeProbe.enabled=true", "nodeProbe.networkPolicy=true", "nodeProbe.hostNetwork=false")...)
	pol, ok := r.policies["continuum-agent-probe"]
	if !ok {
		t.Fatal("no agent policy")
	}
	open := false
	for _, rule := range pol.Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntValue() == 9104 {
				for _, f := range rule.From {
					if f.IPBlock != nil && f.IPBlock.CIDR == "0.0.0.0/0" {
						open = true
					}
					if f.PodSelector != nil && f.PodSelector.MatchLabels["app.kubernetes.io/name"] == "continuum-link-prober" {
						t.Error("the prober is still named by pod selector")
					}
				}
			}
		}
	}
	if !open {
		t.Errorf("port 9104 is not open to the prober by address: %+v", pol.Spec.Ingress)
	}
}

// The images are Linux images. The responder tolerates every taint, so without a selector it would be scheduled on Windows nodes
// and sit in ContainerCreating there for good.
func TestTheProberAndTheResponderRunOnLinuxNodesOnly(t *testing.T) {
	r := render(t, probeArgs()...)
	for name, sel := range map[string]map[string]string{
		"prober":    r.deployments["continuum-link-prober"].Spec.Template.Spec.NodeSelector,
		"responder": r.daemonsets["continuum-link-responder"].Spec.Template.Spec.NodeSelector,
	} {
		if sel["kubernetes.io/os"] != "linux" {
			t.Errorf("%s nodeSelector = %v", name, sel)
		}
	}
	// A selector of your own is added to it, not instead of it.
	r = render(t, probeArgs("telemetry.clusterLinks.probe.nodeSelector.zone=edge", "telemetry.clusterLinks.probe.responder.nodeSelector.zone=edge")...)
	for name, sel := range map[string]map[string]string{
		"prober":    r.deployments["continuum-link-prober"].Spec.Template.Spec.NodeSelector,
		"responder": r.daemonsets["continuum-link-responder"].Spec.Template.Spec.NodeSelector,
	} {
		if sel["kubernetes.io/os"] != "linux" || sel["zone"] != "edge" {
			t.Errorf("%s nodeSelector = %v", name, sel)
		}
	}
}

func TestThePreflightNamesTheResponderAndTheProber(t *testing.T) {
	out, err := helmTemplate(t, probeArgs("preflight.assumeEnforce=baseline")...)
	if err == nil || !strings.Contains(out, "the link responder and prober") {
		t.Fatalf("the Pod Security pre-flight does not name the prober (err %v):\n%s", err, out)
	}
}

// Each of these passes the schema's patterns and the old template checks, and then makes the prober exit at start-up (or, for a
// CIDR, writes a line owampd cannot read into the limits file of every responder).
func TestTheProbeSettingsTheProberWouldRefuseAreRefusedAtRender(t *testing.T) {
	for name, set := range map[string][]string{
		"interval below 10s":                    {"telemetry.clusterLinks.probe.interval=5s"},
		"a test that does not fit the interval": {"telemetry.clusterLinks.probe.packets=300"},
		"one packet too many":                   {"telemetry.clusterLinks.probe.packets=251"},
		"spacing below 10ms":                    {"telemetry.clusterLinks.probe.spacing=5ms"},
		"spacing above 5s":                      {"telemetry.clusterLinks.probe.spacing=6s", "telemetry.clusterLinks.probe.packets=10", "telemetry.clusterLinks.probe.interval=2m"},
		"connect timeout above its interval":    {"telemetry.clusterLinks.probe.tcpConnect.timeout=20s"},
		"connect timeout below 500ms":           {"telemetry.clusterLinks.probe.tcpConnect.timeout=100ms"},
		"connect timeout above 30s":             {"telemetry.clusterLinks.probe.tcpConnect.timeout=31s", "telemetry.clusterLinks.probe.tcpConnect.interval=1m"},
		"connect interval below 5s":             {"telemetry.clusterLinks.probe.tcpConnect.interval=2s", "telemetry.clusterLinks.probe.tcpConnect.timeout=1s"},
		"throughput interval below 10m":         {"telemetry.clusterLinks.probe.capacity.enabled=true", "telemetry.clusterLinks.probe.capacity.interval=1m"},
		"route trace below 1m":                  {"telemetry.clusterLinks.probe.pathInterval=10s"},
		"CIDR with octets above 255":            {"telemetry.clusterLinks.probe.responder.allowFrom[0]=999.999.999.999/99"},
		"CIDR with a prefix above 32":           {"telemetry.clusterLinks.probe.responder.allowFrom[0]=10.0.0.0/33"},
		"CIDR with one octet above 255":         {"telemetry.clusterLinks.probe.responder.allowFrom[0]=10.0.256.0/24"},
		"IPv6 CIDR with a prefix above 128":     {"telemetry.clusterLinks.probe.responder.allowFrom[0]=2001:db8::/129"},
		"digits that are not an address":        {"telemetry.clusterLinks.probe.responder.allowFrom[0]=1234/8"},
		"link scrape interval of 1s":            {"telemetry.clusterLinks.probe.scrapeInterval=1s"},
		"link scrape interval of 6h":            {"telemetry.clusterLinks.probe.scrapeInterval=6h"},
		"link scrape interval just above 2m":    {"telemetry.clusterLinks.probe.scrapeInterval=121s"},
		"agent link metrics interval of 1s":     {"telemetry.clusterLinks.metrics.interval=1s"},
		"agent link metrics interval of 6h":     {"telemetry.clusterLinks.metrics.interval=6h"},
		"agent link metrics interval of 5m":     {"telemetry.clusterLinks.metrics.interval=5m"},
	} {
		if out, err := helmTemplate(t, probeArgs(set...)...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
}

func TestTheProbeSettingsAtTheEdgeOfWhatTheProberAcceptsRender(t *testing.T) {
	for name, set := range map[string][]string{
		"a test that exactly fills the interval":  {"telemetry.clusterLinks.probe.packets=250"},
		"short packets in a short interval":       {"telemetry.clusterLinks.probe.interval=11s", "telemetry.clusterLinks.probe.packets=10", "telemetry.clusterLinks.probe.spacing=10ms"},
		"a compound duration":                     {"telemetry.clusterLinks.probe.interval=1m30s"},
		"route trace off":                         {"telemetry.clusterLinks.probe.pathInterval=0"},
		"route trace every minute":                {"telemetry.clusterLinks.probe.pathInterval=1m"},
		"connect timing off":                      {"telemetry.clusterLinks.probe.tcpConnect.interval=0", "telemetry.clusterLinks.probe.tcpConnect.timeout=20s"},
		"connect timeout just under its interval": {"telemetry.clusterLinks.probe.tcpConnect.timeout=14s"},
		"throughput every 10m":                    {"telemetry.clusterLinks.probe.capacity.enabled=true", "telemetry.clusterLinks.probe.capacity.interval=10m"},
		"throughput interval while it is off":     {"telemetry.clusterLinks.probe.capacity.interval=1m"},
		"IPv4 CIDRs":                              {"telemetry.clusterLinks.probe.responder.allowFrom[0]=255.255.255.255/32", "telemetry.clusterLinks.probe.responder.allowFrom[1]=0.0.0.0/0", "telemetry.clusterLinks.probe.responder.allowFrom[2]=100.64.0.0/10"},
		"an IPv6 CIDR":                            {"telemetry.clusterLinks.probe.responder.allowFrom[0]=2001:db8::/32"},
		"scrape and agent interval of 10s":        {"telemetry.clusterLinks.probe.scrapeInterval=10s", "telemetry.clusterLinks.metrics.interval=10s"},
		"scrape and agent interval of 2m":         {"telemetry.clusterLinks.probe.scrapeInterval=2m", "telemetry.clusterLinks.metrics.interval=2m"},
		"scrape interval of 90s":                  {"telemetry.clusterLinks.probe.scrapeInterval=90s"},
		"scrape interval of 1m30s":                {"telemetry.clusterLinks.probe.scrapeInterval=1m30s"},
	} {
		if out, err := helmTemplate(t, probeArgs(set...)...); err != nil {
			t.Errorf("%s: refused: %v\n%s", name, err, out)
		}
	}
}

// An open responder is allowed, but the notes say so. `helm template` does not print NOTES.txt, so (as
// telemetry_cluster_tmp_test.go does) the notes go through a ConfigMap.
func TestTheNotesWarnWhenTheResponderIsOpen(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), "continuum-agent")
	copyTree(t, "continuum-agent", dir)
	text, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"_notes.tpl": `{{- define "test.notes" -}}` + string(text) + `{{- end -}}`,
		"notes.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: {{ include \"test.notes\" . | quote }}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notes := func(args []string) string {
		full := append(append([]string{"template", "ct", dir, "--show-only", "templates/notes.yaml"}, baseSet...), args...)
		out, err := exec.Command(h, full...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template of the notes: %v\n%s", err, out)
		}
		return string(out)
	}
	const warn = "responder.allowFrom is empty"
	if got := notes(probeArgs()); !strings.Contains(got, warn) {
		t.Errorf("no warning for an open responder:\n%s", got)
	}
	if got := notes(probeArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24")); strings.Contains(got, warn) {
		t.Errorf("warning although the responder is limited:\n%s", got)
	}
	if got := notes(clusterLinksArgs()); strings.Contains(got, warn) {
		t.Errorf("warning with the probe off:\n%s", got)
	}
}

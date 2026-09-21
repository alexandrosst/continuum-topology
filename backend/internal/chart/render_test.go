package chart

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// rendered is what `helm template` produced, by kind.
type rendered struct {
	deployments map[string]appsv1.Deployment
	daemonsets  map[string]appsv1.DaemonSet
	secrets     map[string]corev1.Secret
	policies    map[string]networkingv1.NetworkPolicy
}

var baseSet = []string{"--set", "server.address=a.example:8443", "--set", "server.caPin=ab", "--set", "enrollment.token=t"}

// helmTemplate renders the chart with the given extra arguments. It skips the test when helm is not installed.
func helmTemplate(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := Package()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tgz := filepath.Join(dir, Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"template", "ct", tgz}, baseSet...), extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	return string(out), err
}

func render(t *testing.T, extra ...string) rendered {
	t.Helper()
	out, err := helmTemplate(t, extra...)
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", extra, err, out)
	}
	r := rendered{map[string]appsv1.Deployment{}, map[string]appsv1.DaemonSet{}, map[string]corev1.Secret{}, map[string]networkingv1.NetworkPolicy{}}
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Kind == "" {
			continue // an empty document (only comments)
		}
		into := func(v any) {
			if err := json.Unmarshal(raw, v); err != nil {
				t.Fatal(err)
			}
		}
		name := meta.Metadata.Name
		switch meta.Kind {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deployments[name] = d
		case "DaemonSet":
			var d appsv1.DaemonSet
			into(&d)
			r.daemonsets[name] = d
		case "Secret":
			var s corev1.Secret
			into(&s)
			r.secrets[name] = s
		case "NetworkPolicy":
			var p networkingv1.NetworkPolicy
			into(&p)
			r.policies[name] = p
		}
	}
	return r
}

func env(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestOneImageThreeRoles(t *testing.T) {
	r := render(t, "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true")
	agent := r.deployments["continuum-agent"].Spec.Template
	probe := r.daemonsets["continuum-node-probe"].Spec.Template
	flow := r.daemonsets["continuum-flow-collector"].Spec.Template
	want := "continuum/continuum:0.1.0-dev"
	for name, p := range map[string]corev1.PodTemplateSpec{"agent": agent, "probe": probe, "flow": flow} {
		if len(p.Spec.Containers) != 1 {
			t.Fatalf("%s: %d containers", name, len(p.Spec.Containers))
		}
		if img := p.Spec.Containers[0].Image; img != want {
			t.Errorf("%s runs %q, want the one image %q", name, img, want)
		}
	}
	// The role is the first argument; the rest are what the separate binaries were given before.
	for name, c := range map[string]struct {
		got  []string
		want []string
	}{
		"agent": {agent.Spec.Containers[0].Args, []string{"agent"}},
		"probe": {probe.Spec.Containers[0].Args, []string{"probe", "--interval=3m"}},
		"flow":  {flow.Spec.Containers[0].Args, []string{"flow", "--interval=30s", "--method=auto"}},
	} {
		if strings.Join(c.got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s args = %v, want %v", name, c.got, c.want)
		}
	}
	for name, p := range map[string]corev1.PodTemplateSpec{"agent": agent, "probe": probe, "flow": flow} {
		if len(p.Spec.Containers[0].Command) != 0 {
			t.Errorf("%s overrides the image's entrypoint: %v", name, p.Spec.Containers[0].Command)
		}
	}
	// Only the flow collector runs as root, and it says so in the pod spec (the image's own user is 65532).
	if u := flow.Spec.Containers[0].SecurityContext.RunAsUser; u == nil || *u != 0 {
		t.Errorf("the flow collector must state runAsUser: 0, got %v", u)
	}
	if flow.Spec.SecurityContext != nil && flow.Spec.SecurityContext.RunAsNonRoot != nil && *flow.Spec.SecurityContext.RunAsNonRoot {
		t.Error("the flow pod must not demand a non-root user")
	}
	for name, p := range map[string]corev1.PodTemplateSpec{"agent": agent, "probe": probe} {
		sc := p.Spec.SecurityContext
		if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
			t.Errorf("%s must run as non-root 65532", name)
		}
	}
	// Scheduling: Linux nodes only, by default, everywhere.
	for name, p := range map[string]corev1.PodTemplateSpec{"agent": agent, "probe": probe, "flow": flow} {
		if p.Spec.NodeSelector["kubernetes.io/os"] != "linux" {
			t.Errorf("%s nodeSelector = %v, want kubernetes.io/os=linux", name, p.Spec.NodeSelector)
		}
	}
}

func TestImageDigestWinsOverTag(t *testing.T) {
	d := "sha256:" + strings.Repeat("ab", 32)
	r := render(t, "--set", "image.repository=reg.example.com:8443/team/continuum", "--set", "image.tag=1.2.3", "--set", "image.digest="+d, "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true")
	want := "reg.example.com:8443/team/continuum@" + d
	if got := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0].Image; got != want {
		t.Errorf("agent image = %q, want %q", got, want)
	}
	if got := r.daemonsets["continuum-node-probe"].Spec.Template.Spec.Containers[0].Image; got != want {
		t.Errorf("probe image = %q", got)
	}
	if got := r.daemonsets["continuum-flow-collector"].Spec.Template.Spec.Containers[0].Image; got != want {
		t.Errorf("flow image = %q", got)
	}
	r = render(t, "--set", "image.repository=reg.example.com/team/continuum", "--set", "image.tag=1.2.3")
	if got := r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0].Image; got != "reg.example.com/team/continuum:1.2.3" {
		t.Errorf("tag form = %q", got)
	}
}

func TestAgentPodHygiene(t *testing.T) {
	r := render(t, "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true")
	d := r.deployments["continuum-agent"]
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("the agent must be one replica with Recreate: %+v replicas=%v", d.Spec.Strategy, d.Spec.Replicas)
	}
	c := d.Spec.Template.Spec.Containers[0]
	if v, _ := env(c, "CONTINUUM_HEALTH_LISTEN"); v != ":8082" {
		t.Errorf("CONTINUUM_HEALTH_LISTEN = %q", v)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" || c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("liveness/readiness probes are wrong: %+v / %+v", c.LivenessProbe, c.ReadinessProbe)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "agent") {
		t.Error("no role argument")
	}
	// Checksums: present, and stable across renders even though the probe and flow secrets are random on a first install.
	a1 := d.Spec.Template.Annotations
	for _, k := range []string{"checksum/enrollment", "checksum/probe-secret", "checksum/flow-secret"} {
		if a1[k] == "" {
			t.Errorf("annotation %s missing", k)
		}
	}
	a2 := render(t, "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true").deployments["continuum-agent"].Spec.Template.Annotations
	for k, v := range a1 {
		if a2[k] != v {
			t.Errorf("%s changed between two identical renders: the pods would restart on every upgrade", k)
		}
	}
	// ...and they do move when what they stand for moves.
	for _, ch := range []struct{ key, set string }{{"checksum/enrollment", "enrollment.token=other"}, {"checksum/probe-secret", "nodeProbe.secret=0123456789abcdef0123"}, {"checksum/flow-secret", "flowObserver.secret=0123456789abcdef0123"}} {
		got := render(t, "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true", "--set", ch.set).deployments["continuum-agent"].Spec.Template.Annotations[ch.key]
		if got == a1[ch.key] {
			t.Errorf("%s did not change with --set %s", ch.key, ch.set)
		}
	}
	// A pending token is its own file, so its checksum is over the Secret alone.
	if _, ok := r.secrets["continuum-agent-enrollment"]; !ok {
		t.Error("the enrollment Secret is missing")
	}
	// The probe and flow pods roll with their secret too.
	if r.daemonsets["continuum-node-probe"].Spec.Template.Annotations["checksum/secret"] == "" || r.daemonsets["continuum-flow-collector"].Spec.Template.Annotations["checksum/secret"] == "" {
		t.Error("the DaemonSets carry no secret checksum")
	}
}

func TestSchedulingAndProxyOptions(t *testing.T) {
	r := render(t, "--set", "podLabels.team=a", "--set", "priorityClassName=high", "--set", "extraEnv[0].name=FOO", "--set", "extraEnv[0].value=bar",
		"--set", "proxy.httpsProxy=http://proxy:3128", "--set", "proxy.noProxy=10.43.0.1", "--set", "nodeSelector.disk=ssd", "--set", "nodeProbe.enabled=true")
	p := r.deployments["continuum-agent"].Spec.Template
	if p.Labels["team"] != "a" || p.Labels["app.kubernetes.io/name"] != "continuum-agent" || p.Spec.PriorityClassName != "high" {
		t.Errorf("labels/priority: %v %q", p.Labels, p.Spec.PriorityClassName)
	}
	if p.Spec.NodeSelector["disk"] != "ssd" || p.Spec.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Errorf("the user's nodeSelector must merge with the default: %v", p.Spec.NodeSelector)
	}
	c := p.Spec.Containers[0]
	if v, _ := env(c, "FOO"); v != "bar" {
		t.Error("extraEnv missing")
	}
	if v, _ := env(c, "HTTPS_PROXY"); v != "http://proxy:3128" {
		t.Error("HTTPS_PROXY missing")
	}
	if v, _ := env(c, "NO_PROXY"); v != "localhost,127.0.0.1,.svc,.cluster.local,10.43.0.1" {
		t.Errorf("NO_PROXY = %q", v)
	}
	// The probe reports to the agent inside the cluster: no proxy there.
	pc := r.daemonsets["continuum-node-probe"].Spec.Template.Spec.Containers[0]
	if _, ok := env(pc, "HTTPS_PROXY"); ok {
		t.Error("the node probe must not get the proxy")
	}
	if v, _ := env(r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0], "HTTP_PROXY"); v != "" {
		t.Error("HTTP_PROXY set although only httpsProxy was given")
	}
	// Nothing set: no proxy variables at all.
	c = render(t).deployments["continuum-agent"].Spec.Template.Spec.Containers[0]
	for _, n := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if _, ok := env(c, n); ok {
			t.Errorf("%s set by default", n)
		}
	}
}

func TestEgressPolicy(t *testing.T) {
	if _, ok := render(t).policies["continuum-agent-egress"]; ok {
		t.Fatal("the egress policy must be off by default")
	}
	if out, err := helmTemplate(t, "--set", "networkPolicy.egress.enabled=true"); err == nil || !strings.Contains(out, "apiServerCIDRs") {
		t.Errorf("enabling egress without the API server's address must fail with a message, got err=%v\n%s", err, out)
	}
	if out, err := helmTemplate(t, "--set", "networkPolicy.egress.enabled=true", "--set", "networkPolicy.egress.apiServerCIDRs={10.43.0.1/32}"); err == nil || !strings.Contains(out, "serverCIDRs") {
		t.Errorf("enabling egress without the server's address must fail with a message, got err=%v\n%s", err, out)
	}
	p, ok := render(t, "--set", "networkPolicy.egress.enabled=true", "--set", "networkPolicy.egress.apiServerCIDRs={10.43.0.1/32,192.168.1.10/32}", "--set", "networkPolicy.egress.serverCIDRs={203.0.113.7/32}").policies["continuum-agent-egress"]
	if !ok {
		t.Fatal("no egress policy")
	}
	if len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress || len(p.Spec.Egress) != 3 {
		t.Fatalf("policy = %+v", p.Spec)
	}
	dns, api, srv := p.Spec.Egress[0], p.Spec.Egress[1], p.Spec.Egress[2]
	if len(dns.Ports) != 2 || dns.Ports[0].Port.IntValue() != 53 {
		t.Errorf("DNS rule: %+v", dns)
	}
	if len(api.To) != 2 || api.To[1].IPBlock.CIDR != "192.168.1.10/32" || len(api.Ports) != 2 {
		t.Errorf("API rule: %+v", api)
	}
	if len(srv.To) != 1 || srv.To[0].IPBlock.CIDR != "203.0.113.7/32" || len(srv.Ports) != 1 || srv.Ports[0].Port.IntValue() != 8443 {
		t.Errorf("server rule (the port comes from server.address): %+v", srv)
	}
}

func TestFlowReceiverPolicyIsHonest(t *testing.T) {
	// Off by default: a node-address list nobody gave would silently drop every collector.
	r := render(t, "--set", "flowObserver.enabled=true")
	if len(r.policies) != 0 {
		t.Errorf("flow only, defaults: no policy expected, got %d", len(r.policies))
	}
	if out, err := helmTemplate(t, "--set", "flowObserver.enabled=true", "--set", "flowObserver.networkPolicy=true"); err == nil || !strings.Contains(out, "nodeCIDRs") {
		t.Errorf("networkPolicy=true without nodeCIDRs must fail, got err=%v\n%s", err, out)
	}
	p := render(t, "--set", "flowObserver.enabled=true", "--set", "flowObserver.networkPolicy=true", "--set", "flowObserver.nodeCIDRs={10.42.0.0/16}").policies["continuum-agent-probe"]
	found := false
	for _, in := range p.Spec.Ingress {
		for _, f := range in.From {
			if f.IPBlock != nil && f.IPBlock.CIDR == "10.42.0.0/16" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("nodeCIDRs not in the receiver policy: %+v", p.Spec.Ingress)
	}
}

// The values an install command sets (server.Admin.installCommand) must pass the chart's schema, and the schema must still
// catch the typos that matter.
func TestSchemaAcceptsTheInstallCommandAndCatchesTypos(t *testing.T) {
	d := "sha256:" + strings.Repeat("0f", 32)
	for _, extra := range [][]string{
		{"--set", "access.tier=1", "--set", "image.repository=reg.example.com/team/continuum", "--set", "image.tag=0.1.0-dev", "--set", "image.digest=" + d},
		{"--set", "access.tier=0", "--set", "image.repository=alexandrosst/continuum"},
		{"--set", "access.tier=2", "--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true"}, // what the wizard's checkboxes append
		{"--set", "image.tag=1"},                        // helm turns a bare number into an integer
		{"--set", "enrollment.token=1234567"},           // ...and so could a token
		{"--set-string", "access.tier=1"},               // a string tier is accepted too
		{"--set", "nodeProbe.image.repository=x/probe"}, // an older server's command: accepted, ignored
	} {
		if out, err := helmTemplate(t, extra...); err != nil {
			t.Errorf("%v rejected: %v\n%s", extra, err, out)
		}
	}
	for _, bad := range [][]string{
		{"--set", "access.teir=1"},
		{"--set", "access.tier=3"},
		{"--set", "image.digest=sha256:abc"},
		{"--set", "nodeProbe.enabled=yes"},
		{"--set", "flowObserver.method=magic"},
		{"--set", "servr.address=x"},
	} {
		if out, err := helmTemplate(t, bad...); err == nil {
			t.Errorf("%v accepted, want a schema error\n%s", bad, out)
		} else if !strings.Contains(out, "schema") && !strings.Contains(out, "digest") {
			t.Errorf("%v failed, but not on the schema:\n%s", bad, out)
		}
	}
}

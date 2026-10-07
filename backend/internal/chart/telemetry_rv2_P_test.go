package chart

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// The platform layer: what a cluster's admission (Pod Security, Azure Policy), its mesh injection, its network
// policy engine and its image-registry policy do to the telemetry pods. Every test renders the chart; the "admission" functions
// below encode, from the primary documentation named next to each, the rules these specs can run into.

// everyWorkload turns on one signal for each kind of pod this chart can create for telemetry: the host collector with
// resource usage and container logs, the cluster collector, Kepler and dcgm-exporter, plus the node probe and the traffic observer.
func everyWorkload(extra ...string) []string {
	a := append([]string{}, agentTel...)
	a = append(a, allSignals...)
	a = append(a, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true",
		"--set", "nodeProbe.enabled=true", "--set", "flowObserver.enabled=true")
	return append(a, extra...)
}

// pod is one rendered pod template with its workload's name.
type pod struct {
	name string
	spec corev1.PodSpec
	meta map[string]string // labels
	anno map[string]string
}

func podsOf(r rendered) []pod {
	var out []pod
	for n, d := range r.daemonsets {
		out = append(out, pod{n, d.Spec.Template.Spec, d.Spec.Template.Labels, d.Spec.Template.Annotations})
	}
	for n, d := range r.deployments {
		out = append(out, pod{n, d.Spec.Template.Spec, d.Spec.Template.Labels, d.Spec.Template.Annotations})
	}
	return out
}

func podNamed(t *testing.T, r rendered, name string) corev1.PodSpec {
	t.Helper()
	for _, p := range podsOf(r) {
		if p.name == name {
			return p.spec
		}
	}
	t.Fatalf("no workload %s in the render", name)
	return corev1.PodSpec{}
}

// pssBaselineViolation: the part of the Pod Security "baseline" profile (kubernetes.io/docs/concepts/security/pod-security-standards/)
// these pods can hit: host namespaces, privileged, capabilities outside the baseline list, hostPath volumes, host ports, an
// SELinux type outside the allowed four, an Unconfined seccomp profile.
func pssBaselineViolation(ps corev1.PodSpec) string {
	if ps.HostNetwork || ps.HostPID || ps.HostIPC {
		return "host namespaces"
	}
	for _, v := range ps.Volumes {
		if v.HostPath != nil {
			return "hostPath volume " + v.HostPath.Path
		}
	}
	allowedCaps := map[string]bool{"AUDIT_WRITE": true, "CHOWN": true, "DAC_OVERRIDE": true, "FOWNER": true, "FSETID": true, "KILL": true, "MKNOD": true,
		"NET_BIND_SERVICE": true, "SETFCAP": true, "SETGID": true, "SETPCAP": true, "SETUID": true, "SYS_CHROOT": true}
	allowedSE := map[string]bool{"": true, "container_t": true, "container_init_t": true, "container_kvm_t": true, "container_engine_t": true}
	if ps.SecurityContext != nil {
		if o := ps.SecurityContext.SELinuxOptions; o != nil && !allowedSE[o.Type] {
			return "seLinuxOptions.type " + o.Type
		}
		if sp := ps.SecurityContext.SeccompProfile; sp != nil && sp.Type == corev1.SeccompProfileTypeUnconfined {
			return "seccomp Unconfined"
		}
	}
	for _, c := range ps.Containers {
		sc := c.SecurityContext
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				return c.Name + ": hostPort"
			}
		}
		if sc == nil {
			continue
		}
		if sc.Privileged != nil && *sc.Privileged {
			return c.Name + ": privileged"
		}
		if sc.Capabilities != nil {
			for _, add := range sc.Capabilities.Add {
				if !allowedCaps[string(add)] {
					return c.Name + ": capability " + string(add)
				}
			}
		}
		if sc.SELinuxOptions != nil && !allowedSE[sc.SELinuxOptions.Type] {
			return c.Name + ": seLinuxOptions.type " + sc.SELinuxOptions.Type
		}
		if sc.SeccompProfile != nil && sc.SeccompProfile.Type == corev1.SeccompProfileTypeUnconfined {
			return c.Name + ": seccomp Unconfined"
		}
	}
	return ""
}

// The matrix itself, for the README and the lead: for every admission regime this chart can meet, which of its pods the regime
// admits. Rows are the pods of a render with everything on; the regime functions are the ones above. A change to a pod spec that
// moves a cell fails here and has to be a decision.
func TestAdmissionMatrix(t *testing.T) {
	r := render(t, everyWorkload()...)
	rn := render(t, withTel("--set", "telemetry.nodeRuntime.metrics.enabled=true")...)

	ok := func(v string) bool { return v == "" }
	cases := []struct {
		pod        corev1.PodSpec
		name       string
		restricted bool // admitted by Pod Security restricted
		baseline   bool // admitted by Pod Security baseline
	}{
		{podNamed(t, r, "continuum-telemetry-cluster"), "cluster collector", true, true},
		{podNamed(t, rn, "continuum-telemetry-host"), "host collector, nodeRuntime only", true, true},
		{podNamed(t, r, "continuum-telemetry-host"), "host collector, resourceUsage + container logs", false, false},
		{podNamed(t, r, "continuum-telemetry-kepler"), "Kepler", false, false},
		{podNamed(t, r, "continuum-telemetry-dcgm"), "dcgm-exporter", false, false},
		{podNamed(t, r, "continuum-agent"), "agent", true, true},
	}
	for _, c := range cases {
		if got := ok(violatesRestricted(c.pod)); got != c.restricted {
			t.Errorf("%s: admitted by Pod Security restricted = %v, want %v (%s)", c.name, got, c.restricted, violatesRestricted(c.pod))
		}
		if got := ok(pssBaselineViolation(c.pod)); got != c.baseline {
			t.Errorf("%s: admitted by Pod Security baseline = %v, want %v (%s)", c.name, got, c.baseline, pssBaselineViolation(c.pod))
		}
	}
}

// Every pod that needs host access is named by the pre-flight list, and the ones that do not are not: the list decides the
// failure of the Pod Security pre-flight and the SCC bindings, so a pod missing from it would be refused without a word.
func TestHostAccessListIsExactlyThePodsPodSecurityRefuses(t *testing.T) {
	r := render(t, everyWorkload()...)
	byName := map[string]string{
		"the host collector": "continuum-telemetry-host", "Kepler": "continuum-telemetry-kepler", "dcgm-exporter": "continuum-telemetry-dcgm",
		"the node probe": "continuum-node-probe", "the traffic observer": "continuum-flow-collector",
	}
	refused := map[string]bool{}
	for _, p := range podsOf(r) {
		if pssBaselineViolation(p.spec) != "" && p.name != "" {
			refused[p.name] = true
		}
	}
	for who, ds := range byName {
		if !refused[ds] {
			t.Errorf("%s (%s) is in the host-access list but baseline admits it", who, ds)
		}
		delete(refused, ds)
	}
	for ds := range refused {
		t.Errorf("%s is refused by Pod Security baseline but is not in the host-access list (agent.hostAccessPods)", ds)
	}
	// dcgm-exporter needs no hostPath without applyScope, but SYS_ADMIN alone is outside baseline: it must still be listed
	d := render(t, withTel("--set", "telemetry.accelerators.metrics.enabled=true")...)
	if v := pssBaselineViolation(podNamed(t, d, "continuum-telemetry-dcgm")); !strings.Contains(v, "SYS_ADMIN") {
		t.Errorf("dcgm-exporter without applyScope should be refused by baseline for SYS_ADMIN, got %q", v)
	}
}

// dcgm-exporter has its own ServiceAccount: an SCC (or any policy) is granted to an identity, and the namespace's shared `default`
// one - which RKE2's CIS profile also locks down - is not the thing to hand privileges to. It keeps no token and no RBAC.
func TestDcgmHasItsOwnTokenlessServiceAccount(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.accelerators.metrics.enabled=true")...)
	ps := podNamed(t, r, "continuum-telemetry-dcgm")
	if ps.ServiceAccountName != "continuum-agent-dcgm" {
		t.Fatalf("dcgm-exporter runs as %q, want continuum-agent-dcgm", ps.ServiceAccountName)
	}
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Errorf("dcgm-exporter must not mount a token")
	}
	sa, ok := r.serviceaccounts["continuum-agent-dcgm"]
	if !ok || sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Errorf("ServiceAccount continuum-agent-dcgm missing or mounting a token: %+v", sa)
	}
	for n, b := range r.clusterrolebindings {
		for _, s := range b.Subjects {
			if s.Name == "continuum-agent-dcgm" {
				t.Errorf("ClusterRoleBinding %s binds dcgm-exporter's account: it needs no API access", n)
			}
		}
	}
	// and nothing is rendered for it when it is not bundled
	if _, ok := render(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true")...).serviceaccounts["continuum-agent-dcgm"]; ok {
		t.Errorf("the dcgm ServiceAccount is rendered without dcgm-exporter")
	}
}

// What typical admission policies check, on every container of every telemetry workload (and the agent): AKS Deployment Safeguards
// (learn.microsoft.com/azure/aks/deployment-safeguards: resource requests, BOTH liveness and readiness probes, no `latest`
// image tag), Kyverno/Gatekeeper's usual "require requests and limits", "disallow latest tag", "disallow privilege escalation",
// "require drop ALL", "require runAsNonRoot unless a documented exception", and a ResourceQuota (needs requests, and a
// limit where it quotas limits). Root and extra capabilities are only for the pods that say why.
func TestEveryContainerSatisfiesTheUsualAdmissionPolicies(t *testing.T) {
	r := render(t, everyWorkload()...)
	rootOK := map[string]string{ // pod -> why it is allowed to be root
		"continuum-telemetry-host": "container log files are root-owned", "continuum-telemetry-kepler": "privileged by design", "continuum-telemetry-dcgm": "SYS_ADMIN by design",
		"continuum-flow-collector": "loads eBPF",
	}
	probeOK := map[string]bool{"continuum-node-probe": true, "continuum-flow-collector": true} // not telemetry; neither serves a port
	for _, p := range podsOf(r) {
		// Only Kepler's node pre-flight is an init container; it is held to the same checks that can apply to a container that
		// is not meant to serve (requests, a pinned tag), and it is privileged like Kepler itself.
		for _, c := range p.spec.InitContainers {
			who := p.name + "/init:" + c.Name
			if p.name != "continuum-telemetry-kepler" {
				t.Errorf("%s: only Kepler's pre-flight may be an init container", who)
			}
			if c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() || c.Resources.Limits.Memory().IsZero() {
				t.Errorf("%s: needs cpu+memory requests and a memory limit: %+v", who, c.Resources)
			}
			if tag := c.Image[strings.LastIndex(c.Image, ":")+1:]; !strings.Contains(c.Image, "@sha256:") && (tag == "latest" || !strings.Contains(c.Image[strings.LastIndex(c.Image, "/")+1:], ":")) {
				t.Errorf("%s: image %q has no explicit non-latest tag", who, c.Image)
			}
			if sc := c.SecurityContext; sc == nil || sc.Privileged == nil || !*sc.Privileged {
				t.Errorf("%s: the pre-flight probes what Kepler needs, so it runs as privileged as Kepler", who)
			}
		}
		for _, c := range p.spec.Containers {
			who := p.name + "/" + c.Name
			if c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() || c.Resources.Limits.Memory().IsZero() {
				t.Errorf("%s: needs cpu+memory requests and a memory limit: %+v", who, c.Resources)
			}
			if tag := c.Image[strings.LastIndex(c.Image, ":")+1:]; !strings.Contains(c.Image, "@sha256:") && (tag == "latest" || !strings.Contains(c.Image[strings.LastIndex(c.Image, "/")+1:], ":")) {
				t.Errorf("%s: image %q has no explicit non-latest tag", who, c.Image)
			}
			if !probeOK[p.name] && (c.LivenessProbe == nil || c.ReadinessProbe == nil) {
				t.Errorf("%s: AKS Deployment Safeguards require both a liveness and a readiness probe", who)
			}
			sc := c.SecurityContext
			if sc != nil && sc.Privileged != nil && *sc.Privileged {
				if p.name != "continuum-telemetry-kepler" {
					t.Errorf("%s: privileged, which only Kepler may be", who)
				}
				continue // privileged implies every other setting
			}
			if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
				t.Errorf("%s: allowPrivilegeEscalation must be false", who)
				continue
			}
			if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
				t.Errorf("%s: must drop ALL capabilities", who)
			}
			if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
				if p.name != "continuum-telemetry-dcgm" { // not verified on a GPU node: see the comment in telemetry.yaml
					t.Errorf("%s: root filesystem should be read-only", who)
				}
			}
			root := (sc.RunAsUser != nil && *sc.RunAsUser == 0) || (p.spec.SecurityContext != nil && p.spec.SecurityContext.RunAsUser != nil && *p.spec.SecurityContext.RunAsUser == 0)
			if root && rootOK[p.name] == "" {
				t.Errorf("%s: runs as root with no documented reason", who)
			}
		}
		if p.spec.EnableServiceLinks == nil || *p.spec.EnableServiceLinks {
			if strings.HasPrefix(p.name, "continuum-telemetry") {
				t.Errorf("%s: enableServiceLinks should be false", p.name)
			}
		}
		if strings.HasPrefix(p.name, "continuum-telemetry") && p.meta["app.kubernetes.io/component"] == "" {
			t.Errorf("%s: pod lacks app.kubernetes.io/component", p.name)
		}
		for _, k := range []string{"app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/part-of"} {
			if p.meta[k] == "" {
				t.Errorf("%s: pod lacks %s", p.name, k)
			}
		}
	}
}

// The telemetry pods are kept out of a mesh's sidecar injection; an operator who sets the same key in podLabels / podAnnotations
// keeps theirs (a duplicate key would otherwise be a render error), and inherit changes nothing.
func TestTelemetryPodsOptOutOfMeshInjection(t *testing.T) {
	telemetryPods := func(r rendered) []pod {
		var out []pod
		for _, p := range podsOf(r) {
			if strings.HasPrefix(p.name, "continuum-telemetry") {
				out = append(out, p)
			}
		}
		return out
	}
	r := render(t, everyWorkload()...)
	if n := len(telemetryPods(r)); n != 4 {
		t.Fatalf("want the four telemetry workloads, got %d", n)
	}
	for _, p := range telemetryPods(r) {
		if p.meta["sidecar.istio.io/inject"] != "false" || p.anno["linkerd.io/inject"] != "disabled" {
			t.Errorf("%s: labels %v annotations %v: want istio label false and linkerd annotation disabled", p.name, p.meta, p.anno)
		}
	}
	r = render(t, everyWorkload("--set", "mesh.telemetryInjection=inherit")...)
	for _, p := range telemetryPods(r) {
		if _, ok := p.meta["sidecar.istio.io/inject"]; ok || p.anno["linkerd.io/inject"] != "" {
			t.Errorf("%s: inherit must not write injection settings: %v %v", p.name, p.meta, p.anno)
		}
	}
	r = render(t, everyWorkload("--set-string", "podLabels.sidecar\\.istio\\.io/inject=true", "--set-string", "podAnnotations.linkerd\\.io/inject=enabled")...)
	for _, p := range telemetryPods(r) {
		if p.meta["sidecar.istio.io/inject"] != "true" || p.anno["linkerd.io/inject"] != "enabled" {
			t.Errorf("%s: the operator's own values must win: %v %v", p.name, p.meta, p.anno)
		}
	}
}

// One registry for every image. The repository's own host is replaced and the rest of the path kept, for all four images (the
// agent image included) and the regional operator; digests are untouched.
func TestGlobalImageRegistryReachesEveryImage(t *testing.T) {
	images := func(r rendered) map[string]string {
		m := map[string]string{}
		for _, p := range podsOf(r) {
			for _, c := range p.spec.Containers {
				m[p.name] = c.Image
			}
		}
		return m
	}
	def := images(render(t, everyWorkload()...))
	got := images(render(t, everyWorkload("--set", "global.imageRegistry=registry.corp.example:5000/mirror/")...))
	want := map[string]string{
		"continuum-telemetry-host":    "registry.corp.example:5000/mirror/otel/opentelemetry-collector-contrib:0.160.0",
		"continuum-telemetry-cluster": "registry.corp.example:5000/mirror/otel/opentelemetry-collector-contrib:0.160.0",
		"continuum-telemetry-kepler":  "registry.corp.example:5000/mirror/sustainable_computing_io/kepler:release-0.7.12",
		"continuum-telemetry-dcgm":    "registry.corp.example:5000/mirror/nvidia/k8s/dcgm-exporter:4.6.0-4.8.3-distroless",
		"continuum-agent":             "registry.corp.example:5000/mirror/continuum/continuum:0.2.0-dev",
		"continuum-node-probe":        "registry.corp.example:5000/mirror/continuum/continuum:0.2.0-dev",
		"continuum-flow-collector":    "registry.corp.example:5000/mirror/continuum/continuum:0.2.0-dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("images with global.imageRegistry:\n got %v\nwant %v", got, want)
	}
	for k, v := range def {
		if strings.Contains(v, "registry.corp") {
			t.Errorf("%s: default image %q leaks the override", k, v)
		}
	}
	if def["continuum-telemetry-kepler"] != "quay.io/sustainable_computing_io/kepler:release-0.7.12" || def["continuum-telemetry-dcgm"] != "nvcr.io/nvidia/k8s/dcgm-exporter:4.6.0-4.8.3-distroless" {
		t.Errorf("default images changed: %v", def)
	}
	// a digest is kept, the host is replaced
	dig := "sha256:" + strings.Repeat("a", 64)
	g := images(render(t, everyWorkload("--set", "global.imageRegistry=r.example", "--set", "telemetry.energy.metrics.keplerImage.digest="+dig, "--set", "telemetry.collectorImage.digest="+dig)...))
	if g["continuum-telemetry-kepler"] != "r.example/sustainable_computing_io/kepler@"+dig || g["continuum-telemetry-host"] != "r.example/otel/opentelemetry-collector-contrib@"+dig {
		t.Errorf("digests: %v", g)
	}
	// an image the operator spelled out itself still goes through the override; localhost counts as a host
	g = images(render(t, withTel("--set", "global.imageRegistry=r.example", "--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.collectorImage.repository=localhost/otelcol")...))
	if g["continuum-telemetry-cluster"] != "r.example/otelcol:0.160.0" {
		t.Errorf("localhost host not replaced: %v", g)
	}

	// the regional operator
	o := operatorRender(t, "--set", "global.imageRegistry=registry.corp.example")
	if img := o.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0].Image; img != "registry.corp.example/otel/opentelemetry-collector-contrib:0.160.0" {
		t.Errorf("regional operator image = %q", img)
	}
}

// The DNS rule of every egress policy this chart writes reaches kube-dns and the NodeLocal
// DNSCache address, and the operator's own dnsCIDRs, once each.
func TestEgressPoliciesReachEveryKindOfClusterDNS(t *testing.T) {
	check := func(who string, p networkingv1.NetworkPolicy, extraCIDR string) {
		t.Helper()
		if len(p.Spec.Egress) == 0 {
			t.Fatalf("%s: no egress rules", who)
		}
		dns := p.Spec.Egress[0]
		var kubeDNS bool
		cidrs := map[string]int{}
		for _, to := range dns.To {
			if to.NamespaceSelector != nil && to.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "kube-system" && to.PodSelector != nil && to.PodSelector.MatchLabels["k8s-app"] == "kube-dns" {
				kubeDNS = true
			}
			if to.IPBlock != nil {
				cidrs[to.IPBlock.CIDR]++
			}
		}
		if !kubeDNS || cidrs["169.254.20.10/32"] != 1 {
			t.Errorf("%s: DNS peers kube-dns=%v nodelocal=%d", who, kubeDNS, cidrs["169.254.20.10/32"])
		}
		if extraCIDR != "" && cidrs[extraCIDR] != 1 {
			t.Errorf("%s: dnsCIDRs entry %s appears %d times", who, extraCIDR, cidrs[extraCIDR])
		}
		ports := map[string]bool{}
		for _, pt := range dns.Ports {
			ports[string(*pt.Protocol)+pt.Port.String()] = true
		}
		for _, w := range []string{"UDP53", "TCP53"} {
			if !ports[w] {
				t.Errorf("%s: DNS port %s missing (%v)", who, w, ports)
			}
		}
	}
	r := render(t, withTel("--set", "networkPolicy.telemetryEgress.enabled=true", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "networkPolicy.telemetryEgress.dnsCIDRs[0]=169.254.20.10/32", "--set", "networkPolicy.telemetryEgress.dnsCIDRs[1]=10.96.0.10/32")...)
	check("telemetry egress", r.policies["continuum-telemetry-egress"], "10.96.0.10/32")
	r = render(t, "--set", "networkPolicy.egress.enabled=true", "--set", "networkPolicy.egress.apiServerCIDRs[0]=10.0.0.1/32", "--set", "networkPolicy.egress.serverCIDRs[0]=203.0.113.7/32")
	check("agent egress", r.policies["continuum-agent-egress"], "")
	o := operatorRender(t, "--set", "networkPolicy.egress.enabled=true", "--set", "networkPolicy.egress.allowedEgress[0].cidr=203.0.113.7/32", "--set", "networkPolicy.egress.allowedEgress[0].ports[0]=4317")
	for name, p := range o.policies {
		if strings.HasSuffix(name, "-egress") {
			check("operator egress", p, "")
		}
	}
}

// The Pod Security pre-flight: a release that needs host access, in a namespace that enforces baseline or restricted, stops with the
// kubectl command that fixes it, unless told to warn or stay silent. It is a no-op wherever nothing can be looked up.
func TestPodSecurityPreflight(t *testing.T) {
	kepler := withTel("--set", "telemetry.energy.metrics.enabled=true", "-n", "continuum-system")
	for _, level := range []string{"baseline", "restricted"} {
		out, err := helmTemplate(t, append(kepler, "--set", "preflight.assumeEnforce="+level)...)
		if err == nil {
			t.Fatalf("%s: the render must fail, Kepler cannot be admitted", level)
		}
		for _, s := range []string{"kubectl label namespace continuum-system pod-security.kubernetes.io/enforce=privileged --overwrite", level, "Kepler"} {
			if !strings.Contains(out, s) {
				t.Errorf("%s: message lacks %q:\n%s", level, s, out)
			}
		}
	}
	// no lookup possible (helm template): renders
	if _, err := helmTemplate(t, kepler...); err != nil {
		t.Errorf("nothing known about the namespace, the render must not fail: %v", err)
	}
	// privileged namespace, or a signal set that fits restricted: fine
	for name, args := range map[string][]string{
		"privileged":                    append(kepler, "--set", "preflight.assumeEnforce=privileged"),
		"warn":                          append(kepler, "--set", "preflight.assumeEnforce=restricted", "--set", "preflight.podSecurity=warn"),
		"off":                           append(kepler, "--set", "preflight.assumeEnforce=restricted", "--set", "preflight.podSecurity=off"),
		"cluster collector only":        withTel("--set", "preflight.assumeEnforce=restricted", "--set", "telemetry.kubernetesState.metrics.enabled=true"),
		"nodeRuntime only":              withTel("--set", "preflight.assumeEnforce=restricted", "--set", "telemetry.nodeRuntime.metrics.enabled=true"),
		"restricted, no telemetry":      {"--set", "preflight.assumeEnforce=restricted"},
		"baseline, host collector off":  withTel("--set", "preflight.assumeEnforce=baseline", "--set", "telemetry.kubernetesEvents.logs.enabled=true"),
		"unknown level (not enforcing)": append(kepler, "--set", "preflight.assumeEnforce=privileged"),
	} {
		if out, err := helmTemplate(t, args...); err != nil {
			t.Errorf("%s: unexpected failure: %v\n%s", name, err, out)
		}
	}
	// every host-access signal trips it, and the node probe / traffic observer too
	for name, set := range map[string][]string{
		"resourceUsage": {"telemetry.resourceUsage.metrics.enabled=true"},
		"systemLogs":    {"telemetry.systemLogs.logs.enabled=true"},
		"dcgm":          {"telemetry.accelerators.metrics.enabled=true"},
		"node probe":    {"nodeProbe.enabled=true"},
		"flow observer": {"flowObserver.enabled=true"},
	} {
		args := withTel("--set", "preflight.assumeEnforce=baseline")
		for _, s := range set {
			args = append(args, "--set", s)
		}
		if _, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%s: baseline namespace, the render must fail", name)
		}
	}
	// the NOTES carry the message in warn mode
	notes := renderNotes(t, append(kepler, "--set", "preflight.assumeEnforce=restricted", "--set", "preflight.podSecurity=warn")...)
	if !strings.Contains(notes, "PRE-FLIGHT") || !strings.Contains(notes, "kubectl label namespace continuum-system") {
		t.Errorf("warn mode: NOTES lack the pre-flight message:\n%s", notes)
	}
	if n := renderNotes(t, kepler...); strings.Contains(n, "PRE-FLIGHT") {
		t.Errorf("no pre-flight message when nothing is known:\n%s", n)
	}
}

// renderNotes renders NOTES.txt (which `helm template` does not print) by wrapping it in a ConfigMap of a copy of the chart.
func renderNotes(t *testing.T, extra ...string) string {
	t.Helper()
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
	args := append(append([]string{"template", "ct", dir, "--show-only", "templates/notes.yaml"}, baseSetForNotes...), extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template of the notes: %v\n%s", err, out)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := sigsyaml.Unmarshal(out, &cm); err != nil {
		t.Fatal(err)
	}
	return cm.Data["notes"]
}

var baseSetForNotes = baseSet

// The pre-flight against a real (fake) API server, through Helm's own `lookup`: `helm template --dry-run=server` reads the
// namespace's label from an API server, here an httptest one that answers discovery and the Namespace GET. Covers: a labelled
// namespace stops the install, a privileged or unlabelled one does not, a namespace the server does not know does not, and a
// Forbidden answer - which Helm turns into a render error - is escaped with preflight.namespaceLookup=false.
func TestPodSecurityPreflightReadsTheNamespaceLabelThroughLookup(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	// The handler runs on the server's goroutines while the test changes what it answers: guarded by a mutex.
	var mu sync.Mutex
	var label string
	var forbidden, missing bool
	set := func(l string, f, m bool) {
		mu.Lock()
		defer mu.Unlock()
		label, forbidden, missing = l, f, m
	}
	state := func() (string, bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		return label, forbidden, missing
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(code int, v any) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		status := func(code int, reason string) {
			write(code, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": reason, "code": code, "message": reason})
		}
		p := r.URL.Path
		label, forbidden, missing := state()
		switch {
		case p == "/version":
			write(200, map[string]any{"major": "1", "minor": "31", "gitVersion": "v1.31.0"})
		case p == "/api":
			write(200, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		case p == "/apis":
			write(200, map[string]any{"kind": "APIGroupList", "groups": []any{}})
		case p == "/api/v1":
			write(200, map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{
				map[string]any{"name": "namespaces", "singularName": "namespace", "namespaced": false, "kind": "Namespace", "verbs": []string{"get", "list"}},
				map[string]any{"name": "secrets", "singularName": "secret", "namespaced": true, "kind": "Secret", "verbs": []string{"get", "list"}},
			}})
		case strings.HasPrefix(p, "/api/v1/namespaces/") && strings.Count(p, "/") == 4:
			if forbidden {
				status(403, "Forbidden")
			} else if missing {
				status(404, "NotFound")
			} else {
				labels := map[string]string{}
				if label != "" {
					labels["pod-security.kubernetes.io/enforce"] = label
				}
				write(200, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": strings.TrimPrefix(p, "/api/v1/namespaces/"), "labels": labels}})
			}
		default:
			status(404, "NotFound")
		}
	}))
	defer srv.Close()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\nclusters: [{name: f, cluster: {server: \""+srv.URL+"\"}}]\ncontexts: [{name: f, context: {cluster: f, user: u}}]\nusers: [{name: u, user: {}}]\ncurrent-context: f\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) (string, error) {
		args := append(append([]string{"template", "ct", tgz, "--dry-run=server"}, baseSet...), extra...)
		args = append(args, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true")
		cmd := exec.Command(h, args...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kc)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	set("restricted", false, false)
	if out, err := run(); err == nil || !strings.Contains(out, "kubectl label namespace default pod-security.kubernetes.io/enforce=privileged") {
		t.Errorf("a namespace labelled enforce=restricted must stop the install with the fix, got err=%v\n%s", err, out)
	}
	if out, err := run("--set", "preflight.podSecurity=warn"); err != nil {
		t.Errorf("warn must not stop it: %v\n%s", err, out)
	}
	set("baseline", false, false)
	if _, err := run(); err == nil {
		t.Errorf("enforce=baseline must stop the install")
	}
	set("privileged", false, false)
	if out, err := run(); err != nil {
		t.Errorf("enforce=privileged: %v\n%s", err, out)
	}
	set("", false, false)
	if out, err := run(); err != nil {
		t.Errorf("no label: %v\n%s", err, out)
	}
	set("", false, true)
	if out, err := run(); err != nil {
		t.Errorf("a namespace that does not exist yet (--create-namespace) must not fail: %v\n%s", err, out)
	}
	set("", true, false)
	if out, err := run(); err == nil || !strings.Contains(out, "forbidden") && !strings.Contains(out, "Forbidden") {
		t.Errorf("Helm turns a Forbidden lookup into a render error; got err=%v\n%s", err, out)
	}
	if out, err := run("--set", "preflight.namespaceLookup=false"); err != nil {
		t.Errorf("preflight.namespaceLookup=false must escape a Forbidden namespace lookup: %v\n%s", err, out)
	}
	// and a declared level still works with the lookup off
	if _, err := run("--set", "preflight.namespaceLookup=false", "--set", "preflight.assumeEnforce=baseline"); err == nil {
		t.Errorf("assumeEnforce must be honoured with the lookup off")
	}
}

// The values this layer reads are read with a default, so a release upgraded with --reuse-values from before these blocks existed
// renders, and exactly like one that has the defaults.
func TestPlatformValuesMayBeAbsent(t *testing.T) {
	fixed := []string{"--set-string", "nodeProbe.secret=p", "--set-string", "flowObserver.secret=f"} // generated ones differ per render
	want, err := helmTemplate(t, everyWorkload(fixed...)...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := helmTemplate(t, everyWorkload(append(fixed, "--set", "podSecurity=null", "--set", "preflight=null", "--set", "global=null",
		"--set", "mesh.telemetryInjection=null")...)...)
	if err != nil {
		t.Fatalf("render without the platform blocks: %v\n%s", err, got)
	}
	if got != want {
		t.Errorf("a render without the platform blocks differs from one with the defaults")
	}
	// and the defaults written in the templates are the ones in values.yaml
	b, err := os.ReadFile("continuum-agent/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := sigsyaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	wantVals := map[string]any{
		"podSecurity": map[string]any{"runAsUser": float64(65532)},
		"preflight":   map[string]any{"podSecurity": "fail", "namespaceLookup": true, "assumeEnforce": ""},
		"global":      map[string]any{"imageRegistry": ""},
	}
	for k, w := range wantVals {
		if !reflect.DeepEqual(v[k], w) {
			t.Errorf("values.yaml %s = %v, the templates assume %v", k, v[k], w)
		}
	}
	if v["mesh"].(map[string]any)["telemetryInjection"] != "disabled" {
		t.Errorf("mesh.telemetryInjection default differs from the templates'")
	}
	// the regional operator, the same
	ow, err := operatorHelmTemplate(t)
	if err != nil {
		t.Fatal(err)
	}
	og, err := operatorHelmTemplate(t, "--set", "podSecurity=null", "--set", "global=null", "--set", "mesh=null")
	if err != nil || og != ow {
		t.Errorf("operator chart without the platform blocks differs or fails: %v", err)
	}
}

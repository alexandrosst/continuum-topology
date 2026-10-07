package chart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// These tests pin what the chart assumes about the two bundled exporters (Kepler release-0.7.12 and
// dcgm-exporter 4.6.0-4.8.3) to what those programs really do, and how the cluster collector finds them.

var (
	energyOn = []string{"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=bundle-kepler"}
	accelOn = []string{"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm"}
)

func both() []string {
	return append(append([]string{}, energyOn...), "--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm")
}

func envSource(c corev1.Container, name string) *corev1.EnvVarSource {
	for _, e := range c.Env {
		if e.Name == name {
			return e.ValueFrom
		}
	}
	return nil
}

// dcgm-exporter's tags carry a variant suffix: 4.6.0-4.8.3 exists only as -distroless (and -ubuntu22.04 was
// dropped in that release), so the unsuffixed tag this chart used to ship pulls nothing.
func TestDcgmDefaultImageIsATagThatExists(t *testing.T) {
	ds := render(t, accelOn...).daemonsets["continuum-telemetry-dcgm"]
	img := ds.Spec.Template.Spec.Containers[0].Image
	if want := "nvcr.io/nvidia/k8s/dcgm-exporter:4.6.0-4.8.3-distroless"; img != want {
		t.Errorf("dcgm-exporter image = %q, want %q (the unsuffixed 4.6.0-4.8.3 tag is not published)", img, want)
	}
}

// Kepler 0.7.x starts a pod watcher through rest.InClusterConfig and lists/watches pods filtered on
// spec.nodeName. Without a token it logs the failure and labels every container "system_processes", so
// the per-pod energy the operator turned the signal on for is never produced.
func TestKeplerGetsItsOwnServiceAccountWithOnlyPodReadAccess(t *testing.T) {
	r := render(t, energyOn...)
	ds := r.daemonsets["continuum-telemetry-kepler"]
	pod := ds.Spec.Template.Spec
	if pod.ServiceAccountName != "continuum-agent-kepler" {
		t.Fatalf("Kepler serviceAccountName = %q, want its own continuum-agent-kepler (never the agent's or the collectors')", pod.ServiceAccountName)
	}
	if pod.AutomountServiceAccountToken == nil || !*pod.AutomountServiceAccountToken {
		t.Error("Kepler must mount a token: its pod watcher uses the in-cluster config")
	}
	if _, ok := r.serviceaccounts["continuum-agent-kepler"]; !ok {
		t.Fatal("ServiceAccount continuum-agent-kepler not rendered")
	}
	cr, ok := r.clusterroles["continuum-agent-kepler"]
	if !ok {
		t.Fatal("ClusterRole continuum-agent-kepler not rendered")
	}
	if len(cr.Rules) != 1 {
		t.Fatalf("Kepler ClusterRole rules = %+v, want exactly one", cr.Rules)
	}
	rule := cr.Rules[0]
	if strings.Join(rule.Resources, ",") != "pods" || strings.Join(rule.Verbs, ",") != "get,list,watch" ||
		strings.Join(rule.APIGroups, ",") != "" {
		t.Errorf("Kepler rule = %+v, want core pods get/list/watch only", rule)
	}
	b, ok := r.clusterrolebindings["continuum-agent-kepler-default"]
	if !ok {
		t.Fatalf("no ClusterRoleBinding for Kepler, have %v", keys(r.clusterrolebindings))
	}
	if b.RoleRef.Name != "continuum-agent-kepler" || len(b.Subjects) != 1 || b.Subjects[0].Name != "continuum-agent-kepler" || b.Subjects[0].Namespace != "default" {
		t.Errorf("binding = %+v", b)
	}
	// Without NODE_NAME Kepler falls back to os.Hostname(), which on a node whose hostname differs from
	// its Node name selects no pods at all.
	if src := envSource(ds.Spec.Template.Spec.Containers[0], "NODE_NAME"); src == nil || src.FieldRef == nil || src.FieldRef.FieldPath != "spec.nodeName" {
		t.Errorf("NODE_NAME must come from spec.nodeName, got %+v", src)
	}
}

func TestKeplerRBACOnlyWhenKeplerIsBundled(t *testing.T) {
	for name, args := range map[string][]string{
		"energy off":      {"--set", "telemetry.export.otlp.endpoint=x:4317"},
		"energy existing": {"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing", "--set", "telemetry.energy.metrics.existing.prometheusEndpoint=k:9102"},
		"accelerators":    accelOn,
	} {
		r := render(t, args...)
		if _, ok := r.clusterroles["continuum-agent-kepler"]; ok {
			t.Errorf("%s: Kepler ClusterRole rendered", name)
		}
		if _, ok := r.serviceaccounts["continuum-agent-kepler"]; ok {
			t.Errorf("%s: Kepler ServiceAccount rendered", name)
		}
		if _, ok := r.clusterrolebindings["continuum-agent-kepler-default"]; ok {
			t.Errorf("%s: Kepler ClusterRoleBinding rendered", name)
		}
	}
}

// The default nodeSelector used to pin kubernetes.io/arch: amd64 on the claim that the image is amd64-only. The
// release-0.7.12 image is a multi-arch manifest (amd64 and arm64, checked against quay.io's registry API), so that kept Kepler
// off arm64 nodes.
func TestKeplerDefaultNodeSelectorDoesNotExcludeArm64(t *testing.T) {
	ds := render(t, energyOn...).daemonsets["continuum-telemetry-kepler"]
	ns := ds.Spec.Template.Spec.NodeSelector
	if _, ok := ns["kubernetes.io/arch"]; ok {
		t.Errorf("Kepler nodeSelector = %v: it must not pin an architecture", ns)
	}
	if ns["kubernetes.io/os"] != "linux" {
		t.Errorf("Kepler nodeSelector = %v, want kubernetes.io/os=linux", ns)
	}
}

// Kepler 0.7.12 with its eBPF maps and per-container series needs well over the 20Mi/64Mi-class defaults
// the chart fell back to; an OOM-killed privileged DaemonSet is a crash loop on every node.
func TestKeplerDefaultResourcesLeaveRoomForItsMaps(t *testing.T) {
	c := render(t, energyOn...).daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec.Containers[0]
	req := c.Resources.Requests.Memory()
	lim := c.Resources.Limits.Memory()
	if req.Value() < 400<<20 {
		t.Errorf("Kepler memory request = %s, want >= 400Mi", req)
	}
	if lim.Cmp(*req) < 0 || lim.IsZero() {
		t.Errorf("Kepler memory limit = %s must be set and >= the request %s", lim, req)
	}
}

func TestDcgmDefaultResources(t *testing.T) {
	c := render(t, accelOn...).daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec.Containers[0]
	if c.Resources.Requests.Memory().Value() < 128<<20 || c.Resources.Limits.Memory().Value() < 512<<20 {
		t.Errorf("dcgm-exporter resources = %+v, want at least NVIDIA's chart defaults (128Mi request, 512Mi limit)", c.Resources)
	}
}

func TestBundledExportersHonourPriorityClassAndPodAnnotations(t *testing.T) {
	r := render(t, append(both(), "--set", "priorityClassName=system-node-critical", "--set", "podAnnotations.team=obs")...)
	for _, n := range []string{"continuum-telemetry-kepler", "continuum-telemetry-dcgm"} {
		ds := r.daemonsets[n]
		if ds.Spec.Template.Spec.PriorityClassName != "system-node-critical" {
			t.Errorf("%s priorityClassName = %q", n, ds.Spec.Template.Spec.PriorityClassName)
		}
		if ds.Spec.Template.Annotations["team"] != "obs" {
			t.Errorf("%s pod annotations = %v", n, ds.Spec.Template.Annotations)
		}
	}
}

// dcgm-exporter publishes /health (200 once collection works) and serves nothing useful before its first
// cycle; NVIDIA's own chart gates readiness on it after 45s.
func TestDcgmIsReadyOnItsHealthEndpoint(t *testing.T) {
	c := render(t, accelOn...).daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec.Containers[0]
	p := c.ReadinessProbe
	if p == nil || p.HTTPGet == nil || p.HTTPGet.Path != "/health" || p.HTTPGet.Port.String() != "metrics" {
		t.Fatalf("dcgm readiness probe = %+v, want httpGet /health on the metrics port", p)
	}
	if p.InitialDelaySeconds < 30 {
		t.Errorf("dcgm initialDelaySeconds = %d; the exporter needs time to start the DCGM engine", p.InitialDelaySeconds)
	}
}

// Under k3s (and any containerd that does not default to the nvidia runtime) a GPU pod only sees NVML when it runs
// with runtimeClassName: nvidia, so the chart needs a knob for it; it is off by default because on clusters whose default
// runtime is nvidia the RuntimeClass need not exist and naming it would leave the pod unschedulable.
func TestDcgmRuntimeClassIsOptIn(t *testing.T) {
	if rc := render(t, accelOn...).daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec.RuntimeClassName; rc != nil {
		t.Errorf("runtimeClassName defaults to %q, want unset", *rc)
	}
	rc := render(t, append(accelOn, "--set", "telemetry.accelerators.metrics.runtimeClassName=nvidia")...).daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec.RuntimeClassName
	if rc == nil || *rc != "nvidia" {
		t.Errorf("runtimeClassName = %v, want nvidia", rc)
	}
	if out, err := helmTemplate(t, append(accelOn, "--set", "telemetry.accelerators.metrics.runtimeClassName=Not_A_Name")...); err == nil {
		t.Errorf("an invalid runtimeClassName must be refused by the schema:\n%s", out)
	}
}

// DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS makes dcgm-exporter call the Kubernetes API for pod labels; the pod runs with no token (and
// no RBAC), so the chart used to set a variable that could only fail. The pod/namespace/container labels come from
// DCGM_EXPORTER_KUBERNETES + the kubelet pod-resources socket, which is only needed (and only mounted) when
// applyScope has to know which namespace a GPU belongs to.
func TestDcgmPodAttributionOnlyWithApplyScope(t *testing.T) {
	for _, applyScope := range []bool{false, true} {
		args := append(append([]string{}, accelOn...), "--set", "telemetry.scope.namespaces={a}", "--set", fmt.Sprintf("telemetry.accelerators.metrics.applyScope=%v", applyScope))
		pod := render(t, args...).daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec
		c := pod.Containers[0]
		for _, e := range c.Env {
			if strings.Contains(e.Name, "ENABLE_POD_LABELS") {
				t.Errorf("applyScope=%v: %s needs API access the pod does not have", applyScope, e.Name)
			}
		}
		_, k8s := env(c, "DCGM_EXPORTER_KUBERNETES")
		hasVol := false
		for _, v := range pod.Volumes {
			hasVol = hasVol || v.Name == "pod-resources"
		}
		hasMount := false
		for _, m := range c.VolumeMounts {
			hasMount = hasMount || m.MountPath == "/var/lib/kubelet/pod-resources"
		}
		if k8s != applyScope || hasVol != applyScope || hasMount != applyScope {
			t.Errorf("applyScope=%v: DCGM_EXPORTER_KUBERNETES=%v volume=%v mount=%v, want all %v", applyScope, k8s, hasVol, hasMount, applyScope)
		}
	}
}

func scrapeConfigs(t *testing.T, r rendered) map[string]map[string]any {
	t.Helper()
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	prom, _ := cfg["receivers"].(map[string]any)["prometheus/infra"].(map[string]any)
	out := map[string]map[string]any{}
	for _, sc := range prom["config"].(map[string]any)["scrape_configs"].([]any) {
		m := sc.(map[string]any)
		out[m["job_name"].(string)] = m
	}
	return out
}

// Pod discovery with no namespace/selector makes the receiver cache every pod of the cluster for each job, and,
// since the old relabel only matched the app label, scraped another release's Kepler and dcgm (same label) a
// second time and stamped it with this release's data.
func TestBundledExportersAreDiscoveredOnlyInThisReleasesNamespaceAndInstance(t *testing.T) {
	jobs := scrapeConfigs(t, render(t, append(both(), "--namespace", "mon")...))
	for job, name := range map[string]string{"kepler": "continuum-telemetry-kepler", "dcgm-exporter": "continuum-telemetry-dcgm"} {
		sc, ok := jobs[job]
		if !ok {
			t.Fatalf("no %s job in %v", job, keys(jobs))
		}
		sd := sc["kubernetes_sd_configs"].([]any)[0].(map[string]any)
		if got := sd["namespaces"].(map[string]any)["names"].([]any); len(got) != 1 || got[0] != "mon" {
			t.Errorf("%s SD namespaces = %v, want [mon]", job, got)
		}
		sel := sd["selectors"].([]any)[0].(map[string]any)
		if sel["role"] != "pod" || sel["label"] != "app.kubernetes.io/name="+name+",app.kubernetes.io/instance=ct" {
			t.Errorf("%s SD selector = %v", job, sel)
		}
		var keepsInstance, setsNode bool
		for _, rc := range sc["relabel_configs"].([]any) {
			m := rc.(map[string]any)
			src := m["source_labels"].([]any)[0]
			keepsInstance = keepsInstance || (m["action"] == "keep" && src == "__meta_kubernetes_pod_label_app_kubernetes_io_instance" && m["regex"] == "ct")
			setsNode = setsNode || (m["target_label"] == "node" && src == "__meta_kubernetes_pod_node_name")
		}
		if !keepsInstance || !setsNode {
			t.Errorf("%s relabel_configs lack the instance keep (%v) or the node label (%v)", job, keepsInstance, setsNode)
		}
	}
}

func TestScrapeIntervalOfAcceleratorsIsWiredAndValidated(t *testing.T) {
	jobs := scrapeConfigs(t, render(t, append(accelOn, "--set", "telemetry.accelerators.metrics.interval=45s")...))
	if jobs["dcgm-exporter"]["scrape_interval"] != "45s" {
		t.Errorf("dcgm scrape_interval = %v, want 45s (a 1m default would drop most of what its 30s collection produces)", jobs["dcgm-exporter"]["scrape_interval"])
	}
	for _, bad := range []string{"30", "abc", "-5s", "0s", "1.5s"} {
		if out, err := helmTemplate(t, append(accelOn, "--set", "telemetry.accelerators.metrics.interval="+bad)...); err == nil {
			t.Errorf("interval %q must be refused:\n%s", bad, out)
		}
	}
}

// What `source: existing` accepts: the receiver's static target must be a bare host[:port] ("/" is invalid
// there, which made the whole cluster collector refuse to start), the scheme picks http/https, and the path
// goes into metrics_path.
func TestExistingEndpointIsSplitIntoTargetSchemeAndPath(t *testing.T) {
	cases := []struct{ in, target, scheme, path string }{
		{"kepler.monitoring.svc:9102", "kepler.monitoring.svc:9102", "", "/metrics"},
		{"kepler.monitoring.svc:9102/metrics", "kepler.monitoring.svc:9102", "", "/metrics"},
		{"10.0.0.5:9102/custom/metrics", "10.0.0.5:9102", "", "/custom/metrics"},
		{"http://dcgm.gpu.svc:9400", "dcgm.gpu.svc:9400", "", "/metrics"},
		{"https://dcgm.gpu.svc:9400/m", "dcgm.gpu.svc:9400", "https", "/m"},
		{"[fd00::1]:9400", "[fd00::1]:9400", "", "/metrics"},
		{"dcgm", "dcgm", "", "/metrics"},
	}
	for _, signal := range []struct{ key, job string }{{"energy", "energy-existing"}, {"accelerators", "accelerators-existing"}} {
		for _, c := range cases {
			r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
				"--set", "telemetry."+signal.key+".metrics.enabled=true", "--set", "telemetry."+signal.key+".metrics.source=existing",
				"--set", "telemetry."+signal.key+".metrics.existing.prometheusEndpoint="+c.in)
			sc, ok := scrapeConfigs(t, r)[signal.job]
			if !ok {
				t.Fatalf("%s %q: no %s job", signal.key, c.in, signal.job)
			}
			tg := sc["static_configs"].([]any)[0].(map[string]any)["targets"].([]any)
			if len(tg) != 1 || tg[0] != c.target {
				t.Errorf("%s %q: target = %v, want %q", signal.key, c.in, tg, c.target)
			}
			if sch, _ := sc["scheme"].(string); sch != c.scheme {
				t.Errorf("%s %q: scheme = %q, want %q", signal.key, c.in, sch, c.scheme)
			}
			if sc["metrics_path"] != c.path {
				t.Errorf("%s %q: metrics_path = %v, want %q", signal.key, c.in, sc["metrics_path"], c.path)
			}
		}
	}
}

func TestExistingEndpointRejectsWhatTheCollectorCannotScrape(t *testing.T) {
	for _, bad := range []string{"http://x:1/m?a=b", "x y", "x:1/a#b", "ftp://x:1", "x:99999x", "http://", "x:1/a\"b", "x:1/a\\\\b", "/metrics", ":9102"} {
		out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317",
			"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing",
			"--set", "telemetry.energy.metrics.existing.prometheusEndpoint="+strings.ReplaceAll(bad, ",", "\\,"))
		if err == nil {
			t.Errorf("endpoint %q must be refused, rendered:\n%s", bad, out)
		} else if !strings.Contains(out, "prometheusEndpoint") {
			t.Errorf("endpoint %q: the failure does not name the value: %s", bad, out)
		}
	}
}

// With applyScope, a GPU's namespace is the namespace label of its own data point (the resource is the exporter pod /
// node); the condition must read that, only touch dcgm-exporter's own records and never drop the scrape's `up`.
func TestApplyScopeFiltersOnTheDataPointNamespace(t *testing.T) {
	r := render(t, append(accelOn, "--set", "telemetry.scope.namespaces={a,b}", "--set", "telemetry.accelerators.metrics.applyScope=true")...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	f := cfg["processors"].(map[string]any)["filter/scope_accelerators"].(map[string]any)
	if _, bad := f["datapoint_conditions"]; bad {
		t.Fatal("datapoint_conditions is not a filterprocessor key")
	}
	conds := f["metric_conditions"].([]any)
	if len(conds) != 1 {
		t.Fatalf("conditions = %v", conds)
	}
	c := conds[0].(string)
	for _, want := range []string{`datapoint.attributes["namespace"]`, `^(a|b)$`, `resource.attributes["service.name"] == "dcgm-exporter"`, `metric.name != "up"`} {
		if !strings.Contains(c, want) {
			t.Errorf("condition %q lacks %q", c, want)
		}
	}
	if strings.Contains(c, `resource.attributes["k8s.namespace.name"]`) {
		t.Errorf("condition %q reads the exporter pod's namespace, not the GPU's", c)
	}
}

// The prometheus receiver stamps each scraped target's resource with the scraped pod's namespace/pod/container.
// For Kepler and dcgm-exporter that is the exporter's own, not the workloads they report on, and telemetry.scope.infra
// would then drop the whole energy signal for any scope that excludes the release namespace.
func TestExporterIdentityIsRemovedFromKeplerAndDcgmResources(t *testing.T) {
	cfg := otelConfig(t, render(t, both()...).configmaps["continuum-telemetry-cluster-config"].Data)
	procs := cfg["processors"].(map[string]any)
	for proc, svc := range map[string]string{"transform/kepler_node": "kepler", "transform/dcgm_node": "dcgm-exporter"} {
		p, ok := procs[proc].(map[string]any)
		if !ok {
			t.Fatalf("%s not defined", proc)
		}
		var resourceStmts string
		for _, s := range p["metric_statements"].([]any) {
			m := s.(map[string]any)
			if m["context"] == "resource" {
				for _, st := range m["statements"].([]any) {
					resourceStmts += st.(string) + "\n"
				}
			}
		}
		for _, a := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.container.name"} {
			if !strings.Contains(resourceStmts, `delete_key(attributes, "`+a+`") where attributes["service.name"] == "`+svc+`"`) {
				t.Errorf("%s does not delete %s from %s resources:\n%s", proc, a, svc, resourceStmts)
			}
		}
	}
	pl := pipelineLists(t, render(t, both()...))
	infra := pl["continuum-telemetry-cluster-config/metrics/infra"][0]
	for _, want := range []string{"transform/kepler_node", "transform/dcgm_node"} {
		if !containsAny(infra, want) {
			t.Errorf("metrics/infra processors %v lack %s", infra, want)
		}
	}
}

// The cluster collector scrapes the exporters from inside the telemetry egress policy, so the policy must let it.
func TestTelemetryEgressAllowsTheBundledExporterPorts(t *testing.T) {
	args := append(both(), "--set", "networkPolicy.telemetryEgress.enabled=true",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32")
	p := render(t, args...).policies["continuum-telemetry-scrape-egress"] // its own policy, selecting the cluster collector only
	found := map[string]int32{}
	for _, rule := range p.Spec.Egress {
		for _, to := range rule.To {
			if to.PodSelector == nil || to.NamespaceSelector != nil {
				continue
			}
			n := to.PodSelector.MatchLabels["app.kubernetes.io/name"]
			if to.PodSelector.MatchLabels["app.kubernetes.io/instance"] != "ct" {
				t.Errorf("exporter egress to %v is not limited to this release", to.PodSelector.MatchLabels)
			}
			if len(rule.Ports) == 1 && rule.Ports[0].Port != nil {
				found[n] = rule.Ports[0].Port.IntVal
			}
		}
	}
	if found["continuum-telemetry-kepler"] != 9103 || found["continuum-telemetry-dcgm"] != 9400 {
		t.Errorf("egress to the exporters = %v, want kepler 9103 and dcgm 9400", found)
	}
	// And nothing extra when they are not bundled.
	q := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "networkPolicy.telemetryEgress.enabled=true", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32")
	if _, ok := q.policies["continuum-telemetry-scrape-egress"]; ok {
		t.Errorf("a scrape-egress policy with nothing bundled")
	}
	for _, rule := range q.policies["continuum-telemetry-egress"].Spec.Egress {
		for _, to := range rule.To {
			if to.PodSelector != nil && to.NamespaceSelector == nil {
				t.Errorf("unexpected pod egress rule in the shared policy: %+v", rule)
			}
		}
	}
}

// Everything above checks the chart's own idea of the config; this asks the real collector to load it, for the
// combinations that differ most. Skipped when otelcol-contrib is not available (OTELCOL_CONTRIB or PATH).
func TestClusterCollectorConfigLoadsInTheRealCollector(t *testing.T) {
	bin := os.Getenv("OTELCOL_CONTRIB")
	if bin == "" {
		bin, _ = exec.LookPath("otelcol-contrib")
	}
	if bin == "" {
		t.Skip("otelcol-contrib is not available")
	}
	combos := map[string][]string{
		"bundled both + scope + applyScope": append(both(), "--set", "telemetry.scope.namespaces={a}", "--set", "telemetry.scope.infra.namespaces={a}", "--set", "telemetry.accelerators.metrics.applyScope=true"),
		"existing https + path": {"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing",
			"--set", "telemetry.energy.metrics.existing.prometheusEndpoint=https://k.monitoring.svc:9102/m",
			"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing",
			"--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=[fd00::1]:9400/x/metrics"},
		"existing bare host and path": {"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing",
			"--set", "telemetry.energy.metrics.existing.prometheusEndpoint=k.monitoring.svc:9102/metrics"},
	}
	for name, args := range combos {
		cm := render(t, args...).configmaps["continuum-telemetry-cluster-config"]
		f := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(f, []byte(cm.Data["otel-collector-config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "validate", "--config", f)
		cmd.Env = append(os.Environ(), "POD_IP=127.0.0.1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: the collector refuses the rendered config: %v\n%s", name, err, out)
		}
	}
}

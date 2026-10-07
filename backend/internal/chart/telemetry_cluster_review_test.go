package chart

import (
	"fmt"
	"strings"
	"testing"
)

// These tests pin defects found by running the rendered cluster collector configuration through the real
// OpenTelemetry Collector Contrib v0.160.0 (see the findings that came with them). Each one fails on the chart as it
// was before the fix.

func clusterCfg(t *testing.T, args ...string) (rendered, map[string]any) {
	t.Helper()
	r := render(t, append([]string{"--set", "telemetry.export.otlp.endpoint=x:4317"}, args...)...)
	return r, otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
}

func scrapeJob(t *testing.T, cfg map[string]any, receiver, job string) map[string]any {
	t.Helper()
	rcv, _ := cfg["receivers"].(map[string]any)
	p, _ := rcv[receiver].(map[string]any)
	pc, _ := p["config"].(map[string]any)
	jobs, _ := pc["scrape_configs"].([]any)
	for _, j := range jobs {
		if m, _ := j.(map[string]any); m["job_name"] == job {
			return m
		}
	}
	t.Fatalf("scrape job %s missing from %s: %v", job, receiver, jobs)
	return nil
}

// A Prometheus static target is a bare host:port. The documented (and wizard-suggested) "host:port/path" made the
// collector refuse to start - '"kepler.monitoring:9102/metrics" is not a valid hostname' - and with it every other
// signal of the cluster collector.
func TestExistingPrometheusEndpointPathBecomesMetricsPath(t *testing.T) {
	for _, c := range []struct{ in, target, scheme, path string }{
		{"kepler.monitoring:9102/metrics", "kepler.monitoring:9102", "http", "/metrics"},
		{"kepler.monitoring:9102", "kepler.monitoring:9102", "http", "/metrics"},
		{"https://gpu.example:9400/custom/m", "gpu.example:9400", "https", "/custom/m"},
		{"[fd00::5]:9102/x", "[fd00::5]:9102", "http", "/x"},
	} {
		_, cfg := clusterCfg(t, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing",
			"--set", "telemetry.energy.metrics.existing.prometheusEndpoint="+c.in,
			"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing",
			"--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint="+c.in)
		for _, job := range []string{"energy-existing", "accelerators-existing"} {
			j := scrapeJob(t, cfg, "prometheus/infra", job)
			sc, _ := j["static_configs"].([]any)
			targets, _ := sc[0].(map[string]any)["targets"].([]any)
			if len(targets) != 1 || targets[0] != c.target {
				t.Errorf("%s from %q: targets = %v, want [%s] (a path is not part of a Prometheus target)", job, c.in, targets, c.target)
			}
			gotScheme, _ := j["scheme"].(string)
			if gotScheme == "" {
				gotScheme = "http" // the scrape default is left out when it is http
			}
			if j["metrics_path"] != c.path || gotScheme != c.scheme {
				t.Errorf("%s from %q: metrics_path/scheme = %v/%v, want %s/%s", job, c.in, j["metrics_path"], j["scheme"], c.path, c.scheme)
			}
		}
	}
}

func TestExistingPrometheusEndpointThatIsNotAHostIsRefusedAtRenderTime(t *testing.T) {
	for _, bad := range []string{"bad host:9", "a:b:c", "kepler:9102/m?x=1", "kepler:99999999", "ftp://kepler:9102"} {
		out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true",
			"--set", "telemetry.energy.metrics.source=existing", "--set", "telemetry.energy.metrics.existing.prometheusEndpoint="+bad)
		if err == nil || !strings.Contains(out, "prometheusEndpoint") {
			t.Errorf("endpoint %q should fail the release with a message naming the setting, got err=%v\n%s", bad, err, out)
		}
	}
}

// The scope filters build a regular expression and an OTTL string out of these names. A name that is not a DNS-1123
// label either changes what the filter keeps ("shop.*", ".*", "a|b") or is invalid OTTL that stops the collector from
// starting (a double quote), so the schema has to refuse it everywhere a scope is taken.
func TestScopeNamespacesMustBeNamespaceNames(t *testing.T) {
	places := []string{
		"telemetry.scope.namespaces", "telemetry.scope.exclude",
		"telemetry.scope.infra.namespaces", "telemetry.scope.infra.exclude",
		"telemetry.applicationMetrics.metrics.scope.namespaces", "telemetry.applicationMetrics.metrics.scope.exclude",
		"telemetry.applicationLogs.logs.scope.namespaces", "telemetry.applicationLogs.logs.scope.exclude",
		"telemetry.traces.traces.scope.namespaces", "telemetry.traces.traces.scope.exclude",
	}
	for _, place := range places {
		for _, bad := range []string{`"shop.*"`, `".*"`, `"a|b"`, `"sh\"op"`, `""`, `"Shop"`, `" shop"`, `"back\\slash"`} {
			out, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set-json", fmt.Sprintf("%s=[%s]", place, bad))
			if err == nil {
				t.Errorf("%s=[%s] should be refused, rendered instead:\n%.300s", place, bad, out)
			}
		}
		if _, err := helmTemplate(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set-json", fmt.Sprintf(`%s=["shop","kube-system","team-a1"]`, place)); err != nil {
			t.Errorf("%s: real namespace names must be accepted: %v", place, err)
		}
	}
}

// With telemetryEgress on, a NetworkPolicy selects the cluster collector and denies whatever it does not list, so the
// scrapes it makes (bundled Kepler and dcgm-exporter by pod, applicationMetrics targets by namespace and port) need a
// rule of their own or they time out while every pod stays Ready.
func TestTelemetryEgressPolicyLetsTheClusterCollectorScrape(t *testing.T) {
	base := []string{"--set", "networkPolicy.telemetryEgress.enabled=true", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports={4317}"}
	r := render(t, append([]string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.kubernetesState.metrics.enabled=true"}, base...)...)
	if _, ok := r.policies["continuum-telemetry-scrape-egress"]; ok {
		t.Error("a scrape policy is rendered although nothing is scraped")
	}

	r = render(t, append([]string{"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].jobName=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=shop",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].port=9102"}, base...)...)
	p, ok := r.policies["continuum-telemetry-scrape-egress"]
	if !ok {
		t.Fatal("no NetworkPolicy lets the cluster collector scrape Kepler, dcgm-exporter and the scrape targets")
	}
	if got := p.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"]; got != "continuum-telemetry-cluster" {
		t.Errorf("the scrape policy must select only the cluster collector, selects %q", got)
	}
	ports := map[int32]string{}
	for _, e := range p.Spec.Egress {
		for _, pt := range e.Ports {
			var to string
			if len(e.To) == 1 && e.To[0].PodSelector != nil {
				to = e.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"]
			} else if len(e.To) == 1 && e.To[0].NamespaceSelector != nil {
				to = "ns:" + e.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
			}
			ports[pt.Port.IntVal] = to
		}
	}
	want := map[int32]string{9103: "continuum-telemetry-kepler", 9400: "continuum-telemetry-dcgm", 9102: "ns:shop"}
	for port, to := range want {
		if ports[port] != to {
			t.Errorf("egress to port %d goes to %q, want %q (all: %v)", port, ports[port], to, ports)
		}
	}
	// And only when the egress lockdown itself is on.
	r = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true")
	if _, ok := r.policies["continuum-telemetry-scrape-egress"]; ok {
		t.Error("a scrape policy is rendered while telemetryEgress is off")
	}
}

// Kubernetes stores an Event about a Node in the "default" namespace (a Node has none). A namespace scope that does not
// list "default" therefore dropped every node event, against what the wizard says ("Node events stay").
func TestInfraEventScopeKeepsNodeEvents(t *testing.T) {
	for _, scope := range [][]string{
		{"--set-json", `telemetry.scope.infra.namespaces=["shop"]`},
		{"--set-json", `telemetry.scope.infra.exclude=["default"]`},
	} {
		_, cfg := clusterCfg(t, append([]string{"--set", "telemetry.kubernetesEvents.logs.enabled=true"}, scope...)...)
		procs, _ := cfg["processors"].(map[string]any)
		conds := conditionsOf(t, procs, "filter/scope_infra_events", "log_conditions")
		if len(conds) == 0 {
			t.Fatalf("no event conditions for %v", scope)
		}
		for _, c := range conds {
			if !strings.HasSuffix(c, ` and log.body["object"]["involvedObject"]["kind"] != "Node"`) {
				t.Errorf("an event condition can drop a node event: %s", c)
			}
		}
	}
}

// The Service applications push to routes to the pod as soon as it is Ready; with no probe that is when the container
// starts, before its receivers listen. The collector's own health_check extension answers once its pipelines are up.
func TestClusterCollectorIsReadyOnlyWhenItsPipelinesRun(t *testing.T) {
	r, cfg := clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true")
	c := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Fatal("the cluster collector has no HTTP readiness probe")
	}
	ext, _ := cfg["extensions"].(map[string]any)
	hc, _ := ext["health_check"].(map[string]any)
	if hc == nil {
		t.Fatalf("health_check extension missing: %v", ext)
	}
	endpoint, _ := hc["endpoint"].(string)
	if !strings.HasPrefix(endpoint, "0.0.0.0:") && !strings.HasPrefix(endpoint, "${env:POD_IP}:") && !strings.HasPrefix(endpoint, ":") {
		t.Errorf("health_check endpoint %q is not reachable by the kubelet (the extension defaults to localhost)", endpoint)
	}
	want := fmt.Sprint(c.ReadinessProbe.HTTPGet.Port.IntVal)
	if c.ReadinessProbe.HTTPGet.Port.StrVal != "" { // a named port: resolve it against the container's ports
		for _, p := range c.Ports {
			if p.Name == c.ReadinessProbe.HTTPGet.Port.StrVal {
				want = fmt.Sprint(p.ContainerPort)
			}
		}
	}
	if !strings.HasSuffix(endpoint, ":"+want) {
		t.Errorf("readiness probe port %s does not match health_check endpoint %s", want, endpoint)
	}
	svc, _ := cfg["service"].(map[string]any)
	if exts, _ := svc["extensions"].([]any); !containsAny(exts, "health_check") {
		t.Errorf("health_check is configured but not enabled in service.extensions: %v", svc["extensions"])
	}
	// kubernetesState alone (no OTLP receiver, no Service) is probed too.
	r, _ = clusterCfg(t, "--set", "telemetry.kubernetesState.metrics.enabled=true")
	if r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0].ReadinessProbe == nil {
		t.Error("no readiness probe when only cluster state is collected")
	}
}

// A scope filter for a signal that is off (or a sampler with no traces pipeline) sat in the ConfigMap, unreferenced.
func TestClusterConfigDefinesOnlyProcessorsAPipelineUses(t *testing.T) {
	for _, args := range [][]string{
		{"--set", "telemetry.energy.metrics.enabled=true", "--set-json", `telemetry.scope.namespaces=["shop"]`, "--set", "telemetry.processors.tracesSampling.percentage=50",
			"--set-json", `telemetry.applicationLogs.logs.scope.namespaces=["x"]`, "--set-json", `telemetry.traces.traces.scope.exclude=["y"]`, "--set-json", `telemetry.scope.infra.namespaces=["shop"]`},
		{"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set-json", `telemetry.scope.namespaces=["shop"]`, "--set-json", `telemetry.applicationMetrics.metrics.scope.exclude=["y"]`,
			"--set-json", `telemetry.scope.infra.namespaces=["shop"]`},
		{"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.processors.tracesSampling.percentage=10", "--set-json", `telemetry.scope.namespaces=["shop"]`},
	} {
		_, cfg := clusterCfg(t, args...)
		procs, _ := cfg["processors"].(map[string]any)
		pipes := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
		used := map[string]bool{}
		for _, p := range pipes {
			for _, name := range p.(map[string]any)["processors"].([]any) {
				used[name.(string)] = true
			}
		}
		for name := range procs {
			if !used[name] {
				t.Errorf("processor %s is defined but no pipeline uses it (%v)", name, args)
			}
		}
	}
}

// Both bundled scrape jobs used an unrestricted pod role: one extra watch on every pod in the cluster each, only to
// discard nearly all of them with a relabel rule. The API server can do the selecting (as the scrapeTargets job does).
func TestBundledScrapeJobsOnlyDiscoverTheirOwnPods(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true")
	for job, name := range map[string]string{"kepler": "continuum-telemetry-kepler", "dcgm-exporter": "continuum-telemetry-dcgm"} {
		j := scrapeJob(t, cfg, "prometheus/infra", job)
		sd, _ := j["kubernetes_sd_configs"].([]any)
		if len(sd) != 1 {
			t.Fatalf("%s: kubernetes_sd_configs = %v", job, sd)
		}
		c := sd[0].(map[string]any)
		ns, _ := c["namespaces"].(map[string]any)
		if names, _ := ns["names"].([]any); len(names) != 1 || names[0] != "default" {
			t.Errorf("%s discovers pods outside the release namespace: %v", job, c["namespaces"])
		}
		sel, _ := c["selectors"].([]any)
		if len(sel) != 1 || !strings.HasPrefix(fmt.Sprint(sel[0].(map[string]any)["label"]), "app.kubernetes.io/name="+name) {
			t.Errorf("%s has no server-side label selector for its own pods: %v", job, c["selectors"])
		}
	}
}

// telemetry.accelerators.metrics.interval was documented and never read: every scrape ran at Prometheus' default minute.
func TestAcceleratorsIntervalIsUsedByItsScrapeJobs(t *testing.T) {
	for _, src := range []string{"bundle-dcgm", "existing"} {
		args := []string{"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=" + src, "--set", "telemetry.accelerators.metrics.interval=45s"}
		job := "dcgm-exporter"
		if src == "existing" {
			args = append(args, "--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=gpu:9400")
			job = "accelerators-existing"
		}
		_, cfg := clusterCfg(t, args...)
		if got := scrapeJob(t, cfg, "prometheus/infra", job)["scrape_interval"]; got != "45s" {
			t.Errorf("%s: scrape_interval = %v, want 45s", src, got)
		}
	}
}

// k8sattributes only watches nodes to read a node's labels, annotations or uid. This chart extracts none of them, so a
// release that does not collect cluster state has no use for the permission.
func TestTelemetryRoleReadsNodesOnlyForClusterState(t *testing.T) {
	has := func(args ...string) bool {
		r := render(t, append([]string{"--set", "telemetry.export.otlp.endpoint=x:4317"}, args...)...)
		for _, rule := range r.clusterroles["continuum-agent-telemetry"].Rules {
			for _, res := range rule.Resources {
				if res == "nodes" {
					return true
				}
			}
		}
		return false
	}
	if has("--set", "telemetry.applicationLogs.logs.enabled=true", "--set", "telemetry.resourceUsage.metrics.enabled=true") {
		t.Error("the telemetry ClusterRole grants nodes without a signal that reads them")
	}
	if !has("--set", "telemetry.kubernetesState.metrics.enabled=true") {
		t.Error("k8s_cluster lists nodes: the ClusterRole must grant it with kubernetesState on")
	}
}

// Redaction (six regular expressions over every attribute) ran before the scope filter, so a narrow scope paid for
// masking the data it then threw away: replaying 60k records of which 98% were out of scope took ~2.7 s of CPU in the
// real collector with redaction first and ~0.9 s with the filter first. It also let a blocked_key_patterns entry that
// happens to match "k8s.namespace.name" turn the namespace into "****" ahead of the filter that reads it.
func TestScopeFilterRunsBeforeRedactionInEveryAppPipeline(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.processors.resourceDetection.enabled=true", "--set-json", `telemetry.scope.namespaces=["shop"]`)
	pipes := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	for _, name := range []string{"metrics/app", "logs/app", "traces"} {
		procs := pipes[name].(map[string]any)["processors"].([]any)
		idx := map[any]int{}
		for i, p := range procs {
			idx[p] = i
		}
		f, ok := idx["filter/scope"]
		if !ok {
			t.Fatalf("%s has no scope filter: %v", name, procs)
		}
		if f != idx["k8sattributes"]+1 {
			t.Errorf("%s: the scope filter must directly follow k8sattributes, got %v", name, procs)
		}
		if f > idx["redaction"] || f > idx["resourcedetection"] {
			t.Errorf("%s: scope filter runs after redaction/resourcedetection: %v", name, procs)
		}
	}
}

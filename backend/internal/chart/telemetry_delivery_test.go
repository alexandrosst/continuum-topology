package chart

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// What the agent's collectors do on a renewed certificate, an unreachable destination, a kubelet that has to be
// verified, container logs that have to respect the scope, and scraped data that has to carry its pod: the settings
// that decide whether telemetry keeps flowing unattended and stays inside what was approved.

var agentTel = []string{"--set", "telemetry.export.otlp.endpoint=x:4317"}

func withTel(extra ...string) []string { return append(append([]string{}, agentTel...), extra...) }

func hostConfig(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	return otelConfig(t, render(t, extra...).configmaps["continuum-telemetry-host-config"].Data)
}

func clusterConfig(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	return otelConfig(t, render(t, extra...).configmaps["continuum-telemetry-cluster-config"].Data)
}

func stringsOf(t *testing.T, v any) []string {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = fmt.Sprint(x)
	}
	return out
}

func indexOf(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

func containerEnv(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

// Every client-certificate exporter re-reads its certificate; one with no certificate has nothing to re-read.
func TestAgentExportersReloadTheirClientCertificate(t *testing.T) {
	// Metrics are routed to their own (http) destination; logs on both collectors use the default (grpc) one.
	both := withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=op-client",
		"--set", "telemetry.export.routes.metrics.endpoint=prom.example:9090", "--set", "telemetry.export.routes.metrics.protocol=http",
		"--set", "telemetry.export.routes.metrics.tls.mtls.enabled=true", "--set", "telemetry.export.routes.metrics.tls.mtls.secretName=m-client")
	r := render(t, both...)
	for _, cm := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		ex := sub(t, otelConfig(t, r.configmaps[cm].Data), "exporters")
		if got := sub(t, ex, "otlp", "tls")["reload_interval"]; got != "1h" {
			t.Errorf("%s: grpc exporter reload_interval = %v, want 1h", cm, got)
		}
		if got := sub(t, ex, "otlphttp/metrics", "tls")["reload_interval"]; got != "1h" {
			t.Errorf("%s: http exporter reload_interval = %v, want 1h", cm, got)
		}
	}
	plain := sub(t, hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...), "exporters", "otlp")
	if tls, _ := plain["tls"].(map[string]any); tls["reload_interval"] != nil {
		t.Errorf("reload_interval with no certificate: %v", tls)
	}
}

func TestAgentExportersHaveABoundedQueueAndARetryWindow(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.export.routes.traces.endpoint=tempo.example:9411", "--set", "telemetry.export.routes.traces.protocol=zipkin",
		"--set", "telemetry.traces.traces.enabled=true")...)
	check := func(cm, name string, size float64, window string) {
		ex := sub(t, otelConfig(t, r.configmaps[cm].Data), "exporters", name)
		if rt := sub(t, ex, "retry_on_failure"); rt["enabled"] != true || rt["max_elapsed_time"] != window {
			t.Errorf("%s %s retry_on_failure = %v, want enabled, %s", cm, name, rt, window)
		}
		if q := sub(t, ex, "sending_queue"); q["enabled"] != true || q["queue_size"] != size || q["storage"] != nil {
			t.Errorf("%s %s sending_queue = %v, want a memory queue of %v", cm, name, q, size)
		}
	}
	check("continuum-telemetry-host-config", "otlp", 256, "30m")
	check("continuum-telemetry-cluster-config", "otlp", 256, "30m")
	check("continuum-telemetry-cluster-config", "zipkin/traces", 256, "30m")

	r = render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.queue.size=64", "--set", "telemetry.export.queue.retryMaxElapsedTime=10m")...)
	check("continuum-telemetry-host-config", "otlp", 64, "10m")
}

func TestAgentPersistentQueueIsOptInAndUsesAnEmptyDirOnBothCollectors(t *testing.T) {
	base := withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.opamp.enabled=true", "--set", "telemetry.opamp.server.endpoint=wss://opamp.example/v1")
	r := render(t, base...)
	for _, v := range r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Volumes {
		if v.Name == "queue" {
			t.Fatalf("a queue volume by default")
		}
	}
	if ext := stringsOf(t, sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "service")["extensions"]); len(ext) != 2 || ext[0] != "health_check" || ext[1] != "opamp" {
		t.Errorf("default service.extensions = %v, want health_check (the readiness probe) and opamp, no queue storage", ext)
	}

	r = render(t, append(append([]string{}, base...), "--set", "telemetry.export.queue.persistent.enabled=true", "--set", "telemetry.export.queue.persistent.sizeLimit=2Gi")...)
	pods := map[string]corev1.PodSpec{
		"continuum-telemetry-host-config":    r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec,
		"continuum-telemetry-cluster-config": r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec,
	}
	for cm, pod := range pods {
		var vol *corev1.Volume
		for i := range pod.Volumes {
			if pod.Volumes[i].Name == "queue" {
				vol = &pod.Volumes[i]
			}
		}
		if vol == nil || vol.EmptyDir == nil || vol.EmptyDir.SizeLimit == nil || vol.EmptyDir.SizeLimit.String() != "2Gi" {
			t.Errorf("%s: queue volume = %+v, want an emptyDir limited to 2Gi", cm, vol)
		}
		mounted := false
		for _, m := range pod.Containers[0].VolumeMounts {
			mounted = mounted || (m.Name == "queue" && m.MountPath == "/queue" && !m.ReadOnly)
		}
		if !mounted {
			t.Errorf("%s: /queue is not mounted writable", cm)
		}
		if !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem {
			t.Errorf("%s: the persistent queue must not need a writable root filesystem", cm)
		}
		cfg := otelConfig(t, r.configmaps[cm].Data)
		if dir := sub(t, cfg, "extensions", "file_storage/queue")["directory"]; dir != "/queue" {
			t.Errorf("%s: file_storage directory = %v", cm, dir)
		}
		if ext := stringsOf(t, sub(t, cfg, "service")["extensions"]); indexOf(ext, "file_storage/queue") < 0 || indexOf(ext, "opamp") < 0 {
			t.Errorf("%s: service.extensions = %v, want opamp and file_storage/queue", cm, ext)
		}
		if got := sub(t, cfg, "exporters", "otlp", "sending_queue")["storage"]; got != "file_storage/queue" {
			t.Errorf("%s: queue storage = %v", cm, got)
		}
	}
}

// With a receiver token on, the cluster collector's extension list also names bearertokenauth; the queue's
// file_storage must sit beside it, not replace it.
func TestAgentClusterExtensionsKeepBearerAuthBesideTheQueue(t *testing.T) {
	cfg := clusterConfig(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.receiver.auth.enabled=true", "--set", "telemetry.receiver.auth.secretName=tok",
		"--set", "telemetry.export.queue.persistent.enabled=true")...)
	ext := stringsOf(t, sub(t, cfg, "service")["extensions"])
	if indexOf(ext, "bearertokenauth") < 0 || indexOf(ext, "file_storage/queue") < 0 {
		t.Errorf("extensions = %v", ext)
	}
	defs := sub(t, cfg, "extensions")
	if defs["bearertokenauth"] == nil || defs["file_storage/queue"] == nil {
		t.Errorf("an extension is listed but not defined: %v", defs)
	}
}

func TestAgentBatchSizesAreExplicitAndChecked(t *testing.T) {
	for _, cfg := range []map[string]any{
		hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...),
		clusterConfig(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true")...),
	} {
		b := sub(t, cfg, "processors", "batch")
		if b["send_batch_size"] != float64(2048) || b["send_batch_max_size"] != float64(4096) || b["timeout"] != "5s" {
			t.Errorf("default batch = %v", b)
		}
	}
	b := sub(t, hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.processors.batch.sendBatchSize=10",
		"--set", "telemetry.processors.batch.sendBatchMaxSize=20", "--set", "telemetry.processors.batch.timeout=1s")...), "processors", "batch")
	if b["send_batch_size"] != float64(10) || b["send_batch_max_size"] != float64(20) || b["timeout"] != "1s" {
		t.Errorf("batch overrides not honoured: %v", b)
	}
	out, err := helmTemplate(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.processors.batch.sendBatchSize=500", "--set", "telemetry.processors.batch.sendBatchMaxSize=100")...)
	if err == nil || !strings.Contains(out, "sendBatchMaxSize") {
		t.Errorf("a maximum below the batch size must be refused, got err=%v\n%s", err, out)
	}
}

// kubelet_stats dials the node's own IP (an IP always resolves; a node name does not always) and verifies the
// kubelet unless that is switched off on purpose.
func TestAgentKubeletStatsUsesTheHostIPAndVerifiesByDefault(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	ks := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "kubelet_stats")
	if ks["endpoint"] != "https://${env:NODE_IP}:10250" {
		t.Errorf("kubelet_stats endpoint = %v", ks["endpoint"])
	}
	if ks["insecure_skip_verify"] != false {
		t.Errorf("insecure_skip_verify = %v, want false", ks["insecure_skip_verify"])
	}
	ip := containerEnv(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_IP")
	if ip == nil || ip.ValueFrom == nil || ip.ValueFrom.FieldRef == nil || ip.ValueFrom.FieldRef.FieldPath != "status.hostIP" {
		t.Errorf("NODE_IP must come from status.hostIP, got %+v", ip)
	}

	opt := render(t, withTel("--set", "telemetry.nodeRuntime.metrics.enabled=true", "--set", "telemetry.kubelet.insecureSkipVerify=true")...)
	if got := sub(t, otelConfig(t, opt.configmaps["continuum-telemetry-host-config"].Data), "receivers", "kubelet_stats")["insecure_skip_verify"]; got != true {
		t.Errorf("telemetry.kubelet.insecureSkipVerify=true not honoured: %v", got)
	}
	if containerEnv(opt.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_IP") == nil {
		t.Errorf("nodeRuntime alone also runs kubelet_stats and needs NODE_IP")
	}

	// A collector with no kubelet_stats has no use for the address.
	logs := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...)
	if containerEnv(logs.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_IP") != nil {
		t.Errorf("NODE_IP set although kubelet_stats is not running")
	}
}

// The host collector only ever sees its own node's pods, so it only needs to know about them; the cluster collector
// sees records from anywhere.
func TestAgentHostK8sAttributesIsFilteredToItsNode(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	host := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "processors", "k8sattributes")
	if f := sub(t, host, "filter"); f["node_from_env_var"] != "NODE_NAME" {
		t.Errorf("host k8sattributes filter = %v, want node_from_env_var NODE_NAME", f)
	}
	nn := containerEnv(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_NAME")
	if nn == nil || nn.ValueFrom == nil || nn.ValueFrom.FieldRef == nil || nn.ValueFrom.FieldRef.FieldPath != "spec.nodeName" {
		t.Errorf("the filter reads NODE_NAME, which must come from spec.nodeName, got %+v", nn)
	}
	cluster := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data), "processors", "k8sattributes")
	if _, ok := cluster["filter"]; ok {
		t.Errorf("the cluster collector must see every node's pods, got a filter: %v", cluster["filter"])
	}
}

// Container logs are application output, so the scope applies to them, and the collectors' own logs are never read.
func TestAgentSystemLogsDefaultToNoJournalAndNeverReadTheirOwnPods(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	rec := sub(t, cfg, "receivers")
	if _, ok := rec["journald"]; ok {
		t.Errorf("journald is on by default; the collector image has no journalctl and runs unprivileged")
	}
	logs := sub(t, cfg, "service", "pipelines", "logs")
	if got := stringsOf(t, logs["receivers"]); len(got) != 1 || got[0] != "filelog/containers" {
		t.Errorf("logs receivers = %v, want only filelog/containers", got)
	}
	pod := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	for _, v := range pod.Volumes {
		if v.Name == "journal" {
			t.Errorf("the journal is mounted by default")
		}
	}
	fl := sub(t, rec, "filelog/containers")
	if inc := stringsOf(t, fl["include"]); len(inc) != 1 || inc[0] != "/var/log/pods/*/*/*.log" {
		t.Errorf("include = %v", inc)
	}
	// The release's own namespace, pods named continuum-*: the DaemonSet, the Deployment, the agent itself.
	if exc := stringsOf(t, fl["exclude"]); len(exc) != 1 || exc[0] != "/var/log/pods/default_continuum-*/*/*.log" {
		t.Errorf("exclude = %v, want the release's own pods", exc)
	}
	// The debug exporter writes to the collector's own log: with it on, reading that log back would be a feedback loop.
	dbg := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.debug.verbosity=basic")...)
	dfl := sub(t, otelConfig(t, dbg.configmaps["continuum-telemetry-host-config"].Data), "receivers", "filelog/containers")
	if exc := stringsOf(t, dfl["exclude"]); indexOf(exc, "/var/log/pods/default_continuum-*/*/*.log") < 0 {
		t.Errorf("with the debug exporter on, the collectors' own logs are read back: exclude = %v", exc)
	}
	// With no scope there is no filter to run.
	if _, ok := sub(t, cfg, "processors")["filter/scope_system_logs"]; ok {
		t.Errorf("a scope filter with no scope")
	}

	// The journal is still there for whoever opts in.
	j := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.journaling=journald")...)
	if _, ok := sub(t, otelConfig(t, j.configmaps["continuum-telemetry-host-config"].Data), "receivers")["journald"]; !ok {
		t.Errorf("journaling=journald no longer enables the journald receiver")
	}

	// The release's own namespace follows the release, not a constant.
	out, err := helmTemplate(t, append(withTel("--set", "telemetry.systemLogs.logs.enabled=true"), "--namespace", "ikhnos-system")...)
	if err != nil || !strings.Contains(out, `/var/log/pods/ikhnos-system_continuum-*/*/*.log`) {
		t.Errorf("the own-pods exclusion does not follow the release namespace (err=%v)", err)
	}
}

func TestAgentSystemLogsFollowTheScope(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set-json", `telemetry.scope.exclude=["kube-system","monitoring"]`)...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	fl := sub(t, cfg, "receivers", "filelog/containers")
	exc := stringsOf(t, fl["exclude"])
	for _, want := range []string{"/var/log/pods/default_continuum-*/*/*.log", "/var/log/pods/kube-system_*/*/*.log", "/var/log/pods/monitoring_*/*/*.log"} {
		if indexOf(exc, want) < 0 {
			t.Errorf("exclude %v lacks %s", exc, want)
		}
	}
	conds := conditionsOf(t, sub(t, cfg, "processors"), "filter/scope_system_logs", "log_conditions")
	if len(conds) != 1 || !strings.Contains(conds[0], `IsMatch(resource.attributes["k8s.namespace.name"], "^(kube-system|monitoring)$")`) {
		t.Errorf("filter conditions = %v, want a drop of the excluded namespaces", conds)
	}
	procs := stringsOf(t, sub(t, cfg, "service", "pipelines", "logs")["processors"])
	if i := indexOf(procs, "filter/scope_system_logs"); i < 0 || procs[i-1] != "k8sattributes" {
		t.Errorf("the scope filter belongs on the logs pipeline right after k8sattributes: %v", procs)
	}

	// A namespace allow-list becomes include globs; excluded ones still win.
	r = render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set-json", `telemetry.scope.namespaces=["shop","payments"]`, "--set-json", `telemetry.scope.exclude=["payments"]`)...)
	cfg = otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	fl = sub(t, cfg, "receivers", "filelog/containers")
	inc := stringsOf(t, fl["include"])
	if len(inc) != 2 || inc[0] != "/var/log/pods/shop_*/*/*.log" || inc[1] != "/var/log/pods/payments_*/*/*.log" {
		t.Errorf("include = %v, want one glob per kept namespace", inc)
	}
	if exc := stringsOf(t, fl["exclude"]); indexOf(exc, "/var/log/pods/payments_*/*/*.log") < 0 {
		t.Errorf("exclude = %v lacks the excluded namespace", exc)
	}
	conds = conditionsOf(t, sub(t, cfg, "processors"), "filter/scope_system_logs", "log_conditions")
	if len(conds) != 2 || !strings.HasPrefix(conds[0], `(resource.attributes["k8s.namespace.name"] != nil and not IsMatch(`) {
		t.Errorf("a record with no namespace (the node's journal) must be kept by the allow-list, got %v", conds)
	}

	// telemetry.scope.infra wins for the namespaces to keep and adds to the ones to drop.
	r = render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set-json", `telemetry.scope.namespaces=["shop"]`,
		"--set-json", `telemetry.scope.infra.namespaces=["ops"]`, "--set-json", `telemetry.scope.exclude=["a"]`, "--set-json", `telemetry.scope.infra.exclude=["b"]`)...)
	fl = sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "filelog/containers")
	if inc := stringsOf(t, fl["include"]); len(inc) != 1 || inc[0] != "/var/log/pods/ops_*/*/*.log" {
		t.Errorf("include = %v, want the infra namespaces", inc)
	}
	exc = stringsOf(t, fl["exclude"])
	if indexOf(exc, "/var/log/pods/a_*/*/*.log") < 0 || indexOf(exc, "/var/log/pods/b_*/*/*.log") < 0 {
		t.Errorf("exclude = %v, want both exclude lists", exc)
	}
}

// Scraped application data carries none of the pod's identity as resource attributes. The relabelling keeps it as
// labels and the transform promotes it, ahead of k8sattributes and the scope filter that both need it.
func TestAgentScrapedMetricsCarryTheirPodIdentity(t *testing.T) {
	target := []string{"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].jobName=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=shop",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app=shop", "--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].port=9102"}
	cfg := clusterConfig(t, withTel(append(target, "--set-json", `telemetry.scope.namespaces=["shop"]`)...)...)
	scrape := fmt.Sprint(sub(t, cfg, "receivers", "prometheus/app"))
	for _, label := range []string{"__meta_kubernetes_namespace", "__meta_kubernetes_pod_name", "__meta_kubernetes_pod_uid", "__meta_kubernetes_pod_node_name"} {
		if !strings.Contains(scrape, label) {
			t.Errorf("prometheus/app does not keep %s", label)
		}
	}
	if !strings.Contains(scrape, "$${1}:9102") {
		t.Errorf("the address relabel lost its escape: %s", scrape)
	}
	stmts := fmt.Sprint(sub(t, cfg, "processors", "transform/scrape_k8s"))
	for _, attr := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.node.name"} {
		if !strings.Contains(stmts, `resource.attributes["`+attr+`"]`) {
			t.Errorf("transform/scrape_k8s does not set %s", attr)
		}
	}
	procs := stringsOf(t, sub(t, cfg, "service", "pipelines", "metrics/app")["processors"])
	ti, ki, fi := indexOf(procs, "transform/scrape_k8s"), indexOf(procs, "k8sattributes"), indexOf(procs, "filter/scope")
	if procs[0] != "memory_limiter" || ti != 1 || ki != 2 || fi < ki {
		t.Errorf("metrics/app processors = %v, want memory_limiter, transform/scrape_k8s, k8sattributes ... filter/scope", procs)
	}
	// k8sattributes' first association rule is the pod uid, which is what the transform just set.
	assoc := fmt.Sprint(sub(t, cfg, "processors", "k8sattributes")["pod_association"])
	if !strings.Contains(assoc, "k8s.pod.uid") {
		t.Errorf("k8sattributes does not associate on k8s.pod.uid: %s", assoc)
	}

	// No scrape targets: no receiver, and nothing of the above either (the transform has nothing to promote).
	none := clusterConfig(t, withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true")...)
	if _, ok := sub(t, none, "receivers")["prometheus/app"]; ok {
		t.Errorf("prometheus/app with no scrape targets")
	}
	if _, ok := sub(t, none, "processors")["transform/scrape_k8s"]; ok {
		t.Errorf("transform/scrape_k8s with no scrape targets")
	}
	if p := stringsOf(t, sub(t, none, "service", "pipelines", "metrics/app")["processors"]); indexOf(p, "transform/scrape_k8s") >= 0 {
		t.Errorf("metrics/app names transform/scrape_k8s with no scrape targets: %v", p)
	}
}

// The cluster collector holds every object in the cluster in memory and terminates every application's OTLP: its
// default is sized for that, and GOMEMLIMIT follows it. The host collector keeps the shared default (512Mi).
func TestAgentClusterCollectorDefaultsAreSizedForTheClusterAndGOMEMLIMITFollows(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	cl := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]
	if got := cl.Resources.Limits.Memory().String(); got != "1Gi" {
		t.Errorf("cluster collector memory limit = %s, want 1Gi", got)
	}
	if got := cl.Resources.Requests.Memory().String(); got != "256Mi" {
		t.Errorf("cluster collector memory request = %s, want 256Mi", got)
	}
	if got := cl.Resources.Requests.Cpu().String(); got != "100m" {
		t.Errorf("cluster collector cpu request = %s, want 100m", got)
	}
	if g := containerEnv(cl, "GOMEMLIMIT"); g == nil || g.Value != "858993459" {
		t.Errorf("cluster GOMEMLIMIT = %+v, want 858993459 (80%% of 1Gi)", g)
	}
	host := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]
	if got := host.Resources.Limits.Memory().String(); got != "512Mi" {
		t.Errorf("host collector memory limit = %s, want the shared 512Mi (sized by measurement: see telemetry.resources in values.yaml)", got)
	}
	o := render(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.clusterCollector.resources.limits.memory=2Gi")...)
	if got := o.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String(); got != "2Gi" {
		t.Errorf("an override of the cluster collector's limit = %s, want 2Gi", got)
	}
}

func TestAgentSchemaChecksTheDeliveryValues(t *testing.T) {
	for _, bad := range [][]string{
		{"telemetry.export.queue.size=0"},
		{"telemetry.export.queue.retryMaxElapsedTime=soon"},
		{"telemetry.export.queue.persistent.sizeLimit=lots"},
		{"telemetry.export.queue.persistnt.enabled=true"},
		{"telemetry.processors.batch.timeout=fast"},
		{"telemetry.processors.batch.sendBatchSize=0"},
		{"telemetry.kubelet.insecureSkipVerify=maybe"},
		{"telemetry.kubelet.verify=false"},
		{"telemetry.rolloutOnSecretChange=sometimes"},
		{"telemetry.systemLogs.logs.journaling=both"},
	} {
		args := withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", bad[0])
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%v rendered, want a schema error\n%s", bad, out)
		}
	}
}

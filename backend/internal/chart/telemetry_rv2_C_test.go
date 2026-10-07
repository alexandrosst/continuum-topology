package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Cluster collector robustness across distributions and cluster sizes (review round 2, agent C). Each test pins one
// behaviour that was verified against the real collector v0.160.0 and a real kube-apiserver.

const rv2CCluster = "continuum-telemetry-cluster"

func rv2CDeployment(t *testing.T, args ...string) appsv1.Deployment {
	t.Helper()
	// Some signal the cluster collector carries must be on for it to exist.
	d, ok := render(t, append([]string{"--set", "telemetry.kubernetesState.metrics.enabled=true"}, args...)...).deployments[rv2CCluster]
	if !ok {
		t.Fatalf("no %s Deployment rendered for %v", rv2CCluster, args)
	}
	return d
}

func rv2CService(t *testing.T, args ...string) map[string]any {
	t.Helper()
	for _, d := range renderDocs(t, args...) {
		if d.kind() == "Service" && d.name() == rv2CCluster {
			return d["spec"].(map[string]any)
		}
	}
	t.Fatalf("no %s Service rendered for %v", rv2CCluster, args)
	return nil
}

func rv2CRBACResources(t *testing.T, args ...string) map[string][]string {
	t.Helper()
	got := map[string][]string{} // "group/resource" -> verbs
	for _, rule := range render(t, args...).clusterroles["continuum-agent-telemetry"].Rules {
		for _, g := range rule.APIGroups {
			for _, res := range rule.Resources {
				got[g+"/"+res] = rule.Verbs
			}
		}
	}
	return got
}

func rv2CMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func rv2CList(v any) []any { l, _ := v.([]any); return l }

// The ClusterRole of the cluster-state signal holds exactly the 13 kinds the receiver informs on in v0.160.0
// (each one removed in turn made the receiver log "forbidden" while the pod stayed Ready: partial data, no error), and
// nothing else; the event signal alone needs only events (and the pods rule every collector's k8sattributes has).
func TestRV2CClusterStateRBACIsExactlyTheInformedKinds(t *testing.T) {
	got := rv2CRBACResources(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	want := []string{"/nodes", "/namespaces", "/pods", "/replicationcontrollers", "/resourcequotas", "/services",
		"apps/deployments", "apps/replicasets", "apps/statefulsets", "apps/daemonsets", "batch/jobs", "batch/cronjobs",
		"autoscaling/horizontalpodautoscalers"}
	var have []string
	for k, verbs := range got {
		have = append(have, k)
		for _, v := range verbs {
			if v != "get" && v != "list" && v != "watch" {
				t.Errorf("%s: verb %s is not read-only", k, v)
			}
		}
		if !reflect.DeepEqual(verbs, []string{"get", "list", "watch"}) {
			t.Errorf("%s: verbs = %v, want get, list, watch", k, verbs)
		}
	}
	sort.Strings(have)
	sort.Strings(want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("cluster-state ClusterRole resources = %v, want %v", have, want)
	}

	ev := rv2CRBACResources(t, withTel("--set", "telemetry.kubernetesEvents.logs.enabled=true")...)
	if len(ev) != 2 || ev["/events"] == nil || ev["/pods"] == nil {
		t.Errorf("events-only ClusterRole = %v, want events and pods only", ev)
	}
}

func TestRV2CClusterStateIntervalAndSchema(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.kubernetesState.metrics.enabled=true")
	if got := rv2CMap(rv2CMap(cfg["receivers"])["k8s_cluster"])["collection_interval"]; got != "30s" {
		t.Errorf("default collection_interval = %v, want 30s", got)
	}
	_, cfg = clusterCfg(t, "--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.interval=2m")
	if got := rv2CMap(rv2CMap(cfg["receivers"])["k8s_cluster"])["collection_interval"]; got != "2m" {
		t.Errorf("collection_interval = %v, want 2m", got)
	}
	for _, bad := range []string{"telemetry.kubernetesState.metrics.interval=often", "telemetry.kubernetesState.metrics.source=other",
		"telemetry.clusterCollector.autoSize.nodes=-1"} {
		if out, err := helmTemplate(t, withTel("--set", bad)...); err == nil {
			t.Errorf("%s accepted:\n%s", bad, out)
		}
	}
}

// k8s_cluster and k8sobjects hold the object graph in memory: measured on a synthetic 500-node, 15,200-pod cluster the
// default 1Gi sat at memory_limiter's refusal point ("Refusing data", RSS ~957MiB). The memory limit follows the node
// count (never lowered), and GOMEMLIMIT follows the limit.
func TestRV2CClusterCollectorMemoryFollowsTheNodeCount(t *testing.T) {
	cases := []struct {
		nodes      string
		limit, req string
	}{
		{"0", "1Gi", "256Mi"},
		{"300", "1Gi", "256Mi"},
		{"301", "2Gi", "1Gi"},
		{"700", "3Gi", "1536Mi"},
		{"1500", "5Gi", "2560Mi"},
		{"3000", "8Gi", "4Gi"},
	}
	for _, c := range cases {
		d := rv2CDeployment(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.clusterCollector.autoSize.nodes="+c.nodes)...)
		res := d.Spec.Template.Spec.Containers[0].Resources
		if got := res.Limits.Memory(); got.Cmp(resource.MustParse(c.limit)) != 0 {
			t.Errorf("%s nodes: memory limit %s, want %s", c.nodes, got, c.limit)
		}
		if got := res.Requests.Memory(); got.Cmp(resource.MustParse(c.req)) != 0 {
			t.Errorf("%s nodes: memory request %s, want %s", c.nodes, got, c.req)
		}
		var gml string
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "GOMEMLIMIT" {
				gml = e.Value
			}
		}
		lim := res.Limits.Memory().Value()
		want := resource.NewQuantity(lim/10*8, resource.BinarySI).Value()
		if q, err := resource.ParseQuantity(gml); err != nil || q.Value() < want-lim/100 || q.Value() > want+lim/100 {
			t.Errorf("%s nodes: GOMEMLIMIT %q is not ~80%% of the %s limit", c.nodes, gml, res.Limits.Memory())
		}
	}
	// A limit the operator set higher is kept; autoSize.enabled=false pins exactly what resources says.
	d := rv2CDeployment(t, withTel("--set", "telemetry.clusterCollector.autoSize.nodes=700", "--set", "telemetry.clusterCollector.resources.limits.memory=6Gi")...)
	if got := d.Spec.Template.Spec.Containers[0].Resources.Limits.Memory(); got.Cmp(resource.MustParse("6Gi")) != 0 {
		t.Errorf("a configured 6Gi limit became %s", got)
	}
	d = rv2CDeployment(t, withTel("--set", "telemetry.clusterCollector.autoSize.nodes=700", "--set", "telemetry.clusterCollector.autoSize.enabled=false")...)
	if got := d.Spec.Template.Spec.Containers[0].Resources.Limits.Memory(); got.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Errorf("autoSize disabled still changed the limit to %s", got)
	}
}

// Each k8sattributes instance keeps its own copy of every pod. The collector's feature gate shares one; it exists from
// 0.150.0, and an older collector refuses to start on a gate it does not know, so it is passed only to such a tag.
func TestRV2CShareWatchesFeatureGate(t *testing.T) {
	const gate = "--feature-gates=processor.k8sattributes.ShareProcessorBetweenPipelines"
	has := func(args ...string) bool {
		for _, a := range rv2CDeployment(t, withTel(args...)...).Spec.Template.Spec.Containers[0].Args {
			if a == gate {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Error("default render does not share the pod cache between pipelines")
	}
	for name, args := range map[string][]string{
		"switched off":          {"--set", "telemetry.clusterCollector.shareWatches=false"},
		"collector 0.149":       {"--set", "telemetry.collectorImage.tag=0.149.0"},
		"unreadable tag":        {"--set", "telemetry.collectorImage.tag=latest"},
		"a tag of another kind": {"--set", "telemetry.collectorImage.tag=1.2.3"},
	} {
		if has(args...) {
			t.Errorf("%s: the feature gate is passed", name)
		}
	}
	if !has("--set", "telemetry.collectorImage.tag=v0.151.2") {
		t.Error("a later collector does not get the feature gate")
	}
	args := rv2CDeployment(t, withTel()...).Spec.Template.Spec.Containers[0].Args
	if len(args) == 0 || args[0] != "--config=/conf/otel-collector-config.yaml" {
		t.Errorf("args = %v, want the config flag first", args)
	}
}

// The pod runs as a fixed non-root user (podSecurity.runAsUser, 65532 by default), user and group alike.
func TestRV2CClusterCollectorUserIDIsFixedAndNonRoot(t *testing.T) {
	uid := func(args ...string) (*int64, *int64, *bool) {
		sc := rv2CDeployment(t, withTel(args...)...).Spec.Template.Spec.SecurityContext
		return sc.RunAsUser, sc.RunAsGroup, sc.RunAsNonRoot
	}
	u, g, nr := uid()
	if u == nil || *u != 65532 || g == nil || *g != 65532 || nr == nil || !*nr {
		t.Errorf("default security context: user %v group %v nonroot %v, want 65532/65532/true", u, g, nr)
	}
	u, g, _ = uid("--set", "podSecurity.runAsUser=1234")
	if u == nil || *u != 1234 || g == nil || *g != 1234 {
		t.Errorf("podSecurity.runAsUser=1234: user %v group %v, want 1234", u, g)
	}
}

// A sidecar proxy makes every connection reach the collector from the proxy (the source address no longer names the
// application pod, so attribution and a namespace scope silently fail), and may not be up yet when the collector first
// calls the API server. The opt-out is a label and an annotation, each of which an operator's own value replaces.
func TestRV2CClusterCollectorOptsOutOfMeshInjection(t *testing.T) {
	d := rv2CDeployment(t, withTel()...)
	if d.Spec.Template.Labels["sidecar.istio.io/inject"] != "false" || d.Spec.Template.Annotations["linkerd.io/inject"] != "disabled" {
		t.Errorf("pod labels %v annotations %v: mesh opt-out missing", d.Spec.Template.Labels, d.Spec.Template.Annotations)
	}
	d = rv2CDeployment(t, withTel("--set", "mesh.telemetryInjection=inherit")...)
	if _, ok := d.Spec.Template.Labels["sidecar.istio.io/inject"]; ok {
		t.Error("mesh.telemetryInjection=inherit still sets the Istio label")
	}
	if _, ok := d.Spec.Template.Annotations["linkerd.io/inject"]; ok {
		t.Error("mesh.telemetryInjection=inherit still sets the Linkerd annotation")
	}
	d = rv2CDeployment(t, withTel("--set-string", `podLabels.sidecar\.istio\.io/inject=true`, "--set-string", `podAnnotations.linkerd\.io/inject=enabled`)...)
	if d.Spec.Template.Labels["sidecar.istio.io/inject"] != "true" || d.Spec.Template.Annotations["linkerd.io/inject"] != "enabled" {
		t.Errorf("the operator's own values did not win: %v %v", d.Spec.Template.Labels, d.Spec.Template.Annotations)
	}
}

// The receiver listens on every address of the pod (":port"). The gRPC server must tolerate the
// pings keepalive-enabled clients send (grpc-go's default closes the connection with GOAWAY too_many_pings).
func TestRV2COtlpReceiverBindsAllAddressesAndToleratesKeepalive(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true")
	protos := rv2CMap(rv2CMap(rv2CMap(cfg["receivers"])["otlp"])["protocols"])
	grpc, http := rv2CMap(protos["grpc"]), rv2CMap(protos["http"])
	if grpc["endpoint"] != ":4317" || http["endpoint"] != ":4318" {
		t.Errorf("endpoints = %v, %v; want :4317 and :4318", grpc["endpoint"], http["endpoint"])
	}
	pol := rv2CMap(rv2CMap(grpc["keepalive"])["enforcement_policy"])
	if pol["min_time"] != "10s" || pol["permit_without_stream"] != true {
		t.Errorf("keepalive enforcement policy = %v", pol)
	}
	if _, ok := grpc["tls"]; ok {
		t.Error("TLS configured on the receiver without telemetry.receiver.tls")
	}
}

func TestRV2CReceiverTLSIsMountedAndWired(t *testing.T) {
	args := withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.receiver.tls.secretName=app-otlp-tls")
	r, cfg := clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.receiver.tls.secretName=app-otlp-tls")
	protos := rv2CMap(rv2CMap(rv2CMap(cfg["receivers"])["otlp"])["protocols"])
	for _, p := range []string{"grpc", "http"} {
		tls := rv2CMap(rv2CMap(protos[p])["tls"])
		if tls["cert_file"] != "/receiver-tls/tls.crt" || tls["key_file"] != "/receiver-tls/tls.key" || tls["reload_interval"] != "1h" {
			t.Errorf("%s tls = %v", p, tls)
		}
		if _, ok := tls["client_ca_file"]; ok {
			t.Errorf("%s: client certificates required without clientCAKey", p)
		}
	}
	ps := r.deployments[rv2CCluster].Spec.Template.Spec
	var vol, mount bool
	for _, v := range ps.Volumes {
		if v.Name == "receiver-tls" && v.Secret != nil && v.Secret.SecretName == "app-otlp-tls" {
			vol = true
		}
	}
	for _, m := range ps.Containers[0].VolumeMounts {
		if m.Name == "receiver-tls" && m.MountPath == "/receiver-tls" && m.ReadOnly {
			mount = true
		}
	}
	if !vol || !mount {
		t.Errorf("receiver-tls volume %v / read-only mount %v missing", vol, mount)
	}
	// TLS on: the Service does not claim a plain protocol to a proxy that would try to parse it.
	for _, p := range rv2CList(rv2CService(t, args...)["ports"]) {
		if _, ok := rv2CMap(p)["appProtocol"]; ok {
			t.Errorf("port %v declares appProtocol though the receiver speaks TLS", p)
		}
	}
	// mTLS.
	_, cfg = clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.receiver.tls.secretName=app-otlp-tls", "--set", "telemetry.receiver.tls.clientCAKey=ca.crt")
	protos = rv2CMap(rv2CMap(rv2CMap(cfg["receivers"])["otlp"])["protocols"])
	for _, p := range []string{"grpc", "http"} {
		if got := rv2CMap(rv2CMap(protos[p])["tls"])["client_ca_file"]; got != "/receiver-tls/ca.crt" {
			t.Errorf("%s client_ca_file = %v", p, got)
		}
	}
	// Off: nothing mounted.
	for _, v := range rv2CDeployment(t, withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true")...).Spec.Template.Spec.Volumes {
		if v.Name == "receiver-tls" {
			t.Error("receiver-tls volume without telemetry.receiver.tls")
		}
	}
	if out, err := helmTemplate(t, withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.receiver.tls.clientCAKey=ca.crt")...); err == nil || !strings.Contains(out, "clientCAKey") {
		t.Errorf("clientCAKey without secretName accepted: %v\n%s", err, out)
	}
}

func TestRV2COtlpServiceNamesItsProtocols(t *testing.T) {
	spec := rv2CService(t, withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true")...)
	got := map[string]string{}
	for _, p := range rv2CList(spec["ports"]) {
		m := rv2CMap(p)
		got[m["name"].(string)], _ = m["appProtocol"].(string)
	}
	if !reflect.DeepEqual(got, map[string]string{"otlp-grpc": "grpc", "otlp-http": "http"}) {
		t.Errorf("ports and appProtocol = %v", got)
	}
}

func rv2CTarget(extra ...string) []string {
	return withTel(append([]string{"--set", "telemetry.applicationMetrics.metrics.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].jobName=shop-api",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=shop",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app=api",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].port=9090"}, extra...)...)
}

// The scrape address is the entry's port on the pod's IP. The discovered address is one target per declared container
// port (the pod scraped on each, duplicate series); finished pods keep their IP and would be scraped forever.
func TestRV2CScrapeJobAddressesThePodIPAndSkipsFinishedPods(t *testing.T) {
	_, cfg := clusterCfg(t, rv2CTarget()[2:]...)
	j := scrapeJob(t, cfg, "prometheus/app", "shop-api")
	var rules []map[string]any
	for _, r := range rv2CList(j["relabel_configs"]) {
		rules = append(rules, rv2CMap(r))
	}
	if len(rules) < 2 {
		t.Fatalf("relabel rules = %v", rules)
	}
	drop := rules[0]
	if drop["action"] != "drop" || drop["regex"] != "Succeeded|Failed" || !reflect.DeepEqual(drop["source_labels"], []any{"__meta_kubernetes_pod_phase"}) {
		t.Errorf("first rule = %v, want a drop of Succeeded|Failed pods", drop)
	}
	v4 := rules[1]
	if v4["regex"] != "([^:]+)" || v4["replacement"] != "$${1}:9090" || v4["target_label"] != "__address__" || !reflect.DeepEqual(v4["source_labels"], []any{"__meta_kubernetes_pod_ip"}) {
		t.Errorf("address rule = %v", v4)
	}
	if j["metrics_path"] != "/metrics" {
		t.Errorf("metrics_path = %v", j["metrics_path"])
	}
	if _, ok := j["scheme"]; ok {
		t.Errorf("scheme set without being asked: %v", j["scheme"])
	}
	if _, ok := j["tls_config"]; ok {
		t.Errorf("tls_config set without being asked: %v", j["tls_config"])
	}
	for _, r := range rules {
		if reflect.DeepEqual(r["source_labels"], []any{"__address__"}) {
			t.Errorf("rule %v starts from the discovered address", r)
		}
	}
}

func TestRV2CScrapeTargetSchemeAndPath(t *testing.T) {
	_, cfg := clusterCfg(t, rv2CTarget("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].scheme=https",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].path=stats/prom")[2:]...)
	j := scrapeJob(t, cfg, "prometheus/app", "shop-api")
	if j["scheme"] != "https" || j["metrics_path"] != "/stats/prom" {
		t.Errorf("scheme/metrics_path = %v/%v, want https and a leading slash", j["scheme"], j["metrics_path"])
	}
	if _, ok := j["tls_config"]; ok {
		t.Errorf("certificate verification switched off without tlsInsecureSkipVerify: %v", j["tls_config"])
	}
	_, cfg = clusterCfg(t, rv2CTarget("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].scheme=https",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].tlsInsecureSkipVerify=true")[2:]...)
	if tc := rv2CMap(scrapeJob(t, cfg, "prometheus/app", "shop-api")["tls_config"]); tc["insecure_skip_verify"] != true {
		t.Errorf("tls_config = %v", tc)
	}
	// tlsInsecureSkipVerify means nothing over http.
	_, cfg = clusterCfg(t, rv2CTarget("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].tlsInsecureSkipVerify=true")[2:]...)
	if _, ok := scrapeJob(t, cfg, "prometheus/app", "shop-api")["tls_config"]; ok {
		t.Error("tls_config on a plain http job")
	}
	if out, err := helmTemplate(t, rv2CTarget("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].scheme=ftp")...); err == nil {
		t.Errorf("scheme ftp accepted:\n%s", out)
	}
}

// Each of these made the collector refuse to start (taking every other signal with it) or left a job that silently found
// nothing; they are refused when the chart is rendered instead.
func TestRV2CScrapeTargetsAreValidatedAtRenderTime(t *testing.T) {
	for name, c := range map[string]struct{ set, want string }{
		"namespace not a name": {"telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=Shop Prod", "namespace"},
		"selector with quote":  {`telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app="x"`, "podLabelSelector"},
		"path with query":      {"telemetry.applicationMetrics.metrics.scrapeTargets[0].path=/m?x=1", "path"},
	} {
		out, err := helmTemplate(t, rv2CTarget("--set", c.set)...)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: accepted or wrong message: %v\n%s", name, err, out)
		}
	}
	out, err := helmTemplate(t, rv2CTarget(
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[1].jobName=shop-api",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[1].namespace=shop",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[1].podLabelSelector=app=b",
		"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[1].port=9091")...)
	if err == nil || !strings.Contains(out, "used twice") {
		t.Errorf("duplicate jobName accepted: %v\n%s", err, out)
	}
	if out, err := helmTemplate(t, rv2CTarget("--set", `telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=tier in (a\,b)`)...); err != nil {
		t.Errorf("a set-based selector is refused: %v\n%s", err, out)
	}
}

// Event records carry no pod uid, name or IP, so k8sattributes adds nothing to them; it would still hold a full pod cache.
// The API server also reports every Event's deletion an hour after its last update, which is a second record per Event.
func TestRV2CEventsPipelineIsLeanAndDeliversEachEventOnce(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.kubernetesEvents.logs.enabled=true")
	procs := rv2CList(rv2CMap(rv2CMap(rv2CMap(cfg["service"])["pipelines"])["logs/infra"])["processors"])
	for _, p := range procs {
		if p == "k8sattributes" {
			t.Errorf("logs/infra processors = %v", procs)
		}
	}
	if len(procs) < 2 || procs[0] != "memory_limiter" || procs[len(procs)-1] != "batch" {
		t.Errorf("logs/infra processors = %v, want memory_limiter first and batch last", procs)
	}
	objs := rv2CList(rv2CMap(rv2CMap(cfg["receivers"])["k8sobjects"])["objects"])
	o := rv2CMap(objs[0])
	if !reflect.DeepEqual(o["exclude_watch_type"], []any{"DELETED"}) || o["mode"] != "watch" {
		t.Errorf("events object = %v", o)
	}
	// The app pipelines keep it.
	_, cfg = clusterCfg(t, "--set", "telemetry.applicationLogs.logs.enabled=true")
	if procs := rv2CList(rv2CMap(rv2CMap(rv2CMap(cfg["service"])["pipelines"])["logs/app"])["processors"]); len(procs) < 2 || procs[1] != "k8sattributes" {
		t.Errorf("logs/app processors = %v", procs)
	}
	// The same extension twice made the list invalid in a collector that checks.
	exts := rv2CList(rv2CMap(cfg["service"])["extensions"])
	seen := map[any]bool{}
	for _, e := range exts {
		if seen[e] {
			t.Errorf("extension %v listed twice: %v", e, exts)
		}
		seen[e] = true
	}
}

// A processor that is "ready" before it has the pods attributes nothing for the first seconds, and a namespace allow-list
// then drops that data; one that can never list pods must not leave a Ready pod. The association order is: what the
// data says about itself first, the connection last.
func TestRV2CK8sAttributesWaitsForPodsAndAssociatesInOrder(t *testing.T) {
	_, cfg := clusterCfg(t, "--set", "telemetry.applicationMetrics.metrics.enabled=true")
	ka := rv2CMap(rv2CMap(cfg["processors"])["k8sattributes"])
	if ka["wait_for_metadata"] != true || ka["wait_for_metadata_timeout"] != "4m" || ka["watch_sync_period"] != "0s" {
		t.Errorf("k8sattributes = %v", ka)
	}
	var order []string
	for _, a := range rv2CList(ka["pod_association"]) {
		var names []string
		for _, s := range rv2CList(rv2CMap(a)["sources"]) {
			m := rv2CMap(s)
			if m["from"] == "connection" {
				names = append(names, "connection")
			} else {
				names = append(names, m["name"].(string))
			}
		}
		order = append(order, strings.Join(names, "+"))
	}
	want := []string{"k8s.pod.uid", "k8s.pod.name+k8s.namespace.name", "k8s.pod.ip", "connection"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("pod_association = %v, want %v", order, want)
	}
	// 4m is inside what the startup probe allows (60 x 5s), or the kubelet would kill a collector that was still waiting.
	d := rv2CDeployment(t, withTel("--set", "telemetry.applicationMetrics.metrics.enabled=true")...)
	p := d.Spec.Template.Spec.Containers[0].StartupProbe
	if p == nil || int(p.PeriodSeconds)*int(p.FailureThreshold) <= 4*60 {
		t.Errorf("startup probe %+v does not outlast wait_for_metadata_timeout", p)
	}
}

// The collector itself must accept what the templates render for the new settings.
func TestRV2CRenderedClusterConfigsLoadInTheRealCollector(t *testing.T) {
	bin := os.Getenv("OTELCOL_CONTRIB")
	if bin == "" {
		bin = os.Getenv("CONTINUUM_OTELCOL")
	}
	if bin == "" {
		bin, _ = exec.LookPath("otelcol-contrib")
	}
	if bin == "" {
		t.Skip("otelcol-contrib is not available")
	}
	all := []string{"--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true"}
	for name, args := range map[string][]string{
		"tls + mtls + auth": append(append([]string{"--set", "telemetry.receiver.tls.secretName=s",
			"--set", "telemetry.receiver.tls.clientCAKey=ca.crt", "--set", "telemetry.receiver.auth.enabled=true", "--set", "telemetry.receiver.auth.secretName=t"}, all...),
			"--set", "telemetry.scope.namespaces={shop}", "--set", "telemetry.scope.infra.namespaces={shop}"),
		"https scrape target": append(rv2CTarget("--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].scheme=https",
			"--set", "telemetry.applicationMetrics.metrics.scrapeTargets[0].tlsInsecureSkipVerify=true"), all...),
		"events only": {"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.kubernetesEvents.logs.enabled=true"},
	} {
		cm := render(t, withTel(args...)...).configmaps[rv2CCluster+"-config"]
		f := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(f, []byte(cm.Data["otel-collector-config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "validate", "--config", f)
		cmd.Env = append(os.Environ(), "POD_IP=127.0.0.1", "NODE_NAME=n1", "CONTINUUM_TELEMETRY_RECEIVER_AUTH=t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: the collector refuses the rendered config: %v\n%s", name, err, out)
		}
	}
}

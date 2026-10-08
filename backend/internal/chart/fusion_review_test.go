package chart

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"continuum/internal/fusionapi"
)

// The central gateway must not lose data silently when a store is down, and must not let a sender write the
// provenance of its data anywhere but where the stores read it from.

func TestFusionCentralQueuePushesBackAndCutsRequestsToTheStoreLimit(t *testing.T) {
	cfg := centralConfig(t)
	ex := cfg["exporters"].(map[string]any)
	for _, name := range []string{"otlphttp/metrics", "otlphttp/logs", "otlp/traces"} {
		q, _ := ex[name].(map[string]any)["sending_queue"].(map[string]any)
		if q["block_on_overflow"] != true {
			t.Errorf("%s: a full queue must block (otherwise the batch is dropped after the sender was told 200): %v", name, q)
		}
		b, _ := q["batch"].(map[string]any)
		if b["sizer"] != "bytes" || b["max_size"] != float64(3145728) {
			t.Errorf("%s: requests are not cut to 3 MiB (Loki and Tempo take 4): %v", name, q)
		}
	}
	cfg = centralConfig(t, "--set", "central.queue.maxRequestBytes=0")
	q := cfg["exporters"].(map[string]any)["otlphttp/logs"].(map[string]any)["sending_queue"].(map[string]any)
	if _, has := q["batch"]; has {
		t.Errorf("maxRequestBytes=0 should turn the cut off: %v", q)
	}
}

func TestFusionCentralDropsProvenanceWrittenBelowTheResource(t *testing.T) {
	cfg := centralConfig(t)
	tr, _ := cfg["processors"].(map[string]any)["transform/provenance"].(map[string]any)
	if tr == nil {
		t.Fatal("no transform/provenance processor")
	}
	for key, ctx := range map[string]string{"metric_statements": "datapoint", "log_statements": "log", "trace_statements": "span"} {
		st, _ := tr[key].([]any)
		if len(st) != 1 || st[0].(map[string]any)["context"] != ctx {
			t.Errorf("%s = %v, want one %s statement", key, st, ctx)
			continue
		}
		if s := st[0].(map[string]any)["statements"].([]any); len(s) != 1 || !strings.Contains(s[0].(string), `delete_matching_keys(attributes, "^continuum\\..*")`) {
			t.Errorf("%s statements = %v", key, s)
		}
	}
	pipes := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	for sig, p := range pipes {
		procs := p.(map[string]any)["processors"].([]any)
		pos := map[string]int{}
		for i, x := range procs {
			pos[x.(string)] = i
		}
		l, okL := pos["memory_limiter"]
		s, okS := pos["transform/provenance"]
		b, okB := pos["batch"]
		if !okL || !okS || !okB || !(l < s && s < b) {
			t.Errorf("%s processors = %v: want memory_limiter, transform/provenance, ..., batch", sig, procs)
		}
	}
}

func TestFusionStoresGetTheirMemoryCeilingAndTheLimitsOfAWholeFleet(t *testing.T) {
	r := fusionRender(t, "f")
	for _, n := range []string{"f-fusion-loki", "f-fusion-tempo", "f-fusion-prometheus"} {
		var found bool
		for _, e := range r.sets[n].Spec.Template.Spec.Containers[0].Env {
			if e.Name == "GOMEMLIMIT" && e.Value != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no GOMEMLIMIT", n)
		}
	}
	// With the server managing the stores the retention env must still be there next to it.
	r = fusionRender(t, "f", "--set", "switch.managed=true")
	var haveRetention bool
	for _, e := range r.sets["f-fusion-loki"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "LOKI_RETENTION" {
			haveRetention = true
		}
	}
	if !haveRetention {
		t.Error("switch.managed lost the LOKI_RETENTION env")
	}
	loki := r.configs["f-fusion-loki"].Data["loki.yaml"]
	for _, want := range []string{"ingestion_rate_mb: 32", "ingestion_burst_size_mb: 64", "max_global_streams_per_user: 100000"} {
		if !strings.Contains(loki, want) {
			t.Errorf("loki.yaml lacks %q", want)
		}
	}
	if tempo := r.configs["f-fusion-tempo"].Data["tempo.yaml"]; !strings.Contains(tempo, "max_duration: 24h") {
		t.Errorf("tempo.yaml does not let TraceQL metrics queries span a day:\n%s", tempo)
	}
}

func TestFusionSchemaRefusesToDropAnAttributeTheDashboardsJoinOn(t *testing.T) {
	_, err := fusionTemplate(t, "f", "--set", "prometheus.promoteResourceAttributes={service.name,k8s.pod.name}")
	if err == nil {
		t.Fatal("promoting no namespace and no cluster id was accepted")
	}
}

// Every signal is stamped with its category at the gateway, by the rule of the API's category filter (fusionapi/category.go):
// the same words, so a plot, a LogQL selector and an API call never disagree about what is system. The metric rule is checked
// by running the gateway's own patterns against names written the OpenTelemetry way (dots), which Prometheus turns into the
// underscore names the API's rule reads.
func TestFusionCentralStampsEachSignalWithItsCategoryByTheAPIsRule(t *testing.T) {
	cfg := centralConfig(t)
	tr, _ := sub(t, cfg, "processors")["transform/category"].(map[string]any)
	if tr == nil {
		t.Fatal("no transform/category processor")
	}
	stmts := func(key string) []string {
		var out []string
		for _, c := range tr[key].([]any) {
			for _, s := range c.(map[string]any)["statements"].([]any) {
				out = append(out, s.(string))
			}
		}
		return out
	}
	isMatch := regexp.MustCompile(`^set\(attributes\["ikhnos\.category"\], "(\w+)"\)(?: where IsMatch\(metric\.name, "(.*)"\))?$`)
	cats := map[string]*regexp.Regexp{}
	for _, s := range stmts("metric_statements") {
		m := isMatch.FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("unexpected metric statement %q", s)
		}
		if m[2] != "" {
			cats[m[1]] = regexp.MustCompile(m[2])
		} else if m[1] != fusionapi.CategoryApplication {
			t.Errorf("the default is %q, want application", m[1])
		}
	}
	if cats[fusionapi.CategorySystem] == nil || cats[fusionapi.CategoryKubernetes] == nil {
		t.Fatalf("metric patterns: %v", cats)
	}
	// Statements apply in order, the later overriding the earlier: application, then kubernetes, then system.
	got := func(name string) string {
		c := fusionapi.CategoryApplication
		if cats[fusionapi.CategoryKubernetes].MatchString(name) {
			c = fusionapi.CategoryKubernetes
		}
		if cats[fusionapi.CategorySystem].MatchString(name) {
			c = fusionapi.CategorySystem
		}
		return c
	}
	for _, name := range []string{"system.cpu.time", "system.memory.usage", "process.runtime.jvm.memory.usage", "process.cpu.time", "node_cpu_seconds_total",
		"kepler_node_platform_joules_total", "kepler.container.joules", "dcgm_gpu_utilization", "DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_PROF_GR_ENGINE_ACTIVE", "otelcol_exporter_sent_spans", "otelcol.receiver.accepted",
		"scrape_duration_seconds", "up", "target_info", "k8s.pod.cpu.usage", "k8s.node.condition_ready", "container.cpu.usage", "kube_pod_info",
		"http.server.request.duration", "http_server_request_count_total", "jvm.memory.used", "systemd_units", "uploads_total", "uptime", "node", "process",
		"system", "k8sx.thing", "containerd_thing", "fusion_demo_requests", "ikhnos_application_info", "queue.depth"} {
		if want := fusionapi.MetricCategory(strings.ReplaceAll(name, ".", "_")); got(name) != want {
			t.Errorf("%q is %s at the gateway and %s by the API's rule", name, got(name), want)
		}
	}
	// Logs and spans: the namespace of the pod. A log with none is the host's; a span with none belongs to no k8s object.
	for key, wantSystem := range map[string]bool{"log_statements": true, "trace_statements": false} {
		all := strings.Join(stmts(key), "\n")
		for _, ns := range fusionapi.SystemNamespaces {
			if !strings.Contains(all, `== "`+ns+`"`) {
				t.Errorf("%s does not treat %s as the cluster's own namespace", key, ns)
			}
		}
		if has := strings.Contains(all, `"system")`); has != wantSystem {
			t.Errorf("%s: system category present = %v, want %v", key, has, wantSystem)
		}
	}
	if !strings.Contains(strings.Join(stmts("log_statements"), "\n"), `attributes["k8s.namespace.name"] == nil`) {
		t.Error("a log line with no namespace must be a system one")
	}
	// It runs on every signal, after provenance is cleaned and before the batch, and replaces what a sender wrote.
	for sig, p := range cfg["service"].(map[string]any)["pipelines"].(map[string]any) {
		procs := p.(map[string]any)["processors"].([]any)
		pos := map[string]int{}
		for i, x := range procs {
			pos[x.(string)] = i
		}
		if !(pos["transform/provenance"] < pos["transform/category"] && pos["transform/category"] < pos["batch"]) {
			t.Errorf("%s processors = %v: want transform/category after transform/provenance and before batch", sig, procs)
		}
	}
	for _, key := range []string{"metric_statements", "log_statements", "trace_statements"} {
		if first := stmts(key)[0]; !strings.Contains(first, `"application")`) || strings.Contains(first, "where") {
			t.Errorf("%s starts with %q: the unconditional default must come first so a sender's own value is replaced", key, first)
		}
	}
}

// The API takes ranges of up to fusionapi.MaxWindow, so the stores must too (Loki stops at 721h and a Tempo search at 168h
// unless told otherwise). Where the data came from must also be a Loki stream label: left as structured metadata, two
// clusters whose pods share a name (a StatefulSet's web-0) are one stream and a per-cluster query reads all of them.
func TestFusionStoresAcceptTheLongestRangeAndIndexTheCluster(t *testing.T) {
	long := func(v any, what string) {
		t.Helper()
		d, err := time.ParseDuration(strings.TrimSpace(v.(string)))
		if err != nil || d < fusionapi.MaxWindow {
			t.Errorf("%s = %v, want at least the API's %v", what, v, fusionapi.MaxWindow)
		}
	}
	for _, set := range [][]string{nil, {"--set", "tempo.traceqlMetrics=false"}} {
		r := fusionRender(t, "f", set...)
		tempo := yamlInto(t, r.configs["f-fusion-tempo"].Data["tempo.yaml"])
		long(tempo["query_frontend"].(map[string]any)["search"].(map[string]any)["max_duration"], "tempo query_frontend.search.max_duration")
	}
	loki := yamlInto(t, fusionRender(t, "f").configs["f-fusion-loki"].Data["loki.yaml"])
	limits := loki["limits_config"].(map[string]any)
	long(limits["max_query_length"], "loki limits_config.max_query_length")
	attrs := limits["otlp_config"].(map[string]any)["resource_attributes"].(map[string]any)["attributes_config"].([]any)
	indexed := map[string]bool{}
	for _, a := range attrs {
		if a.(map[string]any)["action"] == "index_label" {
			for _, n := range a.(map[string]any)["attributes"].([]any) {
				indexed[n.(string)] = true
			}
		}
	}
	for _, n := range []string{"continuum.cluster.id", "continuum.org.id"} {
		if !indexed[n] {
			t.Errorf("loki does not index %s as a stream label: %v", n, indexed)
		}
	}
	if loki["pattern_ingester"].(map[string]any)["enabled"] != true {
		t.Error("Drilldown > Logs needs Loki's pattern ingester")
	}
}

// Nothing scrapes the gateway, so a store that is down or a queue that is full was invisible until data was already gone.
// It pushes its own counters to Prometheus over OTLP, and the delivery dashboard shows them without narrowing by cluster
// (the gateway serves all of them and its series carry no cluster id).
func TestFusionGatewayReportsItsOwnHealth(t *testing.T) {
	readers := func(extra ...string) []any {
		tel, _ := sub(t, centralConfig(t, extra...), "service")["telemetry"].(map[string]any)
		m, _ := tel["metrics"].(map[string]any)
		r, _ := m["readers"].([]any)
		return r
	}
	r := readers()
	if len(r) != 1 {
		t.Fatalf("the gateway has %d metric readers, want one pushing to Prometheus", len(r))
	}
	otlp := r[0].(map[string]any)["periodic"].(map[string]any)["exporter"].(map[string]any)["otlp"].(map[string]any)
	if ep, _ := otlp["endpoint"].(string); !strings.HasPrefix(ep, "http://f-fusion-prometheus.") || !strings.HasSuffix(ep, ".svc:9090/api/v1/otlp/v1/metrics") {
		t.Errorf("the gateway pushes its metrics to %v, not Prometheus' OTLP path", ep)
	}
	if got := readers("--set", "prometheus.enabled=false"); got != nil {
		t.Errorf("without Prometheus the gateway still has readers: %v", got)
	}
	var d struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(fusionRender(t, "f").configs["f-fusion-grafana-dashboards"].Data["delivery.json"]), &d); err != nil {
		t.Fatal(err)
	}
	var found int
	for _, p := range d.Panels {
		for _, q := range p.Targets {
			if !strings.Contains(q.Expr, "otelcol_") {
				continue
			}
			found++
			if strings.Contains(q.Expr, "continuum_cluster_id") {
				t.Errorf("%q narrows the gateway's own metrics by cluster, which they do not carry: %s", p.Title, q.Expr)
			}
			// rate() or increase() of a selector on several metric names collides on identical label sets.
			if regexp.MustCompile(`(rate|increase)\(\{__name__=~`).MatchString(q.Expr) {
				t.Errorf("%q takes rate() of several metric names, which Prometheus refuses: %s", p.Title, q.Expr)
			}
		}
	}
	if found < 3 {
		t.Errorf("the delivery dashboard has %d gateway queries, want its health row", found)
	}
}

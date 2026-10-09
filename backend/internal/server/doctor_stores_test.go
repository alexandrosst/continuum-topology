package server

import (
	"fmt"
	"regexp"
	sigyaml "sigs.k8s.io/yaml"
	"slices"
	"strconv"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/fusionapi"
)

// lokiIndexedByDefault is what Loki indexes as a stream label out of an OTLP resource without being told (limits_config.
// otlp_config.resource_attributes, ignore_defaults off); everything else it keeps as structured metadata, which a stream
// selector cannot name.
var lokiIndexedByDefault = []string{
	"service.name", "service.namespace", "service.instance.id", "deployment.environment", "deployment.environment.name",
	"cloud.region", "cloud.availability_zone", "k8s.cluster.name", "k8s.namespace.name", "k8s.pod.name", "k8s.container.name",
	"container.name", "k8s.replicaset.name", "k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name",
	"k8s.cronjob.name", "k8s.job.name",
}

// fusionRender is the FUSION chart as `helm template` renders it, with the configuration file of each store parsed.
func fusionRender(t *testing.T, flags ...string) renderedChart {
	t.Helper()
	r, stderr, err := helmRenderChart(t, chart.Fusion, flags...)
	if err != nil {
		t.Fatalf("the FUSION chart does not render: %v\n%s", err, stderr)
	}
	return r
}

// fileOf is one file of a rendered ConfigMap, parsed as YAML.
func fileOf(t *testing.T, r renderedChart, configMap, file string) map[string]any {
	t.Helper()
	d := r["ConfigMap/"+configMap]
	raw, ok := asMap(d["data"])[file].(string)
	if !ok {
		t.Fatalf("ConfigMap %s has no %s: %v", configMap, file, keys(r))
	}
	var out map[string]any
	if err := sigyaml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%s/%s: %v", configMap, file, err)
	}
	return out
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func doctorStores(t *testing.T) {
	t.Run("a collector's pod scrape yields one target per pod, never a second one with no port", func(t *testing.T) {
		// Pod discovery makes a target for every container, and for a container that declares no port the target is the pod IP
		// alone, scraped on port 80 and refused: up=0 next to the working target.
		variants := map[string]string{
			"the bundled Kepler and DCGM exporters": "--set telemetry.energy.metrics.enabled=true --set telemetry.energy.metrics.source=bundle-kepler " +
				"--set telemetry.accelerators.metrics.enabled=true --set telemetry.accelerators.metrics.source=bundle-dcgm",
			"an exporter already in the cluster": "--set telemetry.accelerators.metrics.enabled=true --set telemetry.accelerators.metrics.source=existing " +
				"--set telemetry.accelerators.metrics.existing.pods.labelSelector=app=dcgm",
			"an application's own target": `--set telemetry.applicationMetrics.metrics.enabled=true --set-json 'telemetry.applicationMetrics.metrics.scrapeTargets=[{"jobName":"api","namespace":"shop","podLabelSelector":"app=api","port":9100}]'`,
		}
		for name, flags := range variants {
			t.Run(name, func(t *testing.T) {
				jobs := 0
				for cm, cfg := range agentRender(t, staleNothing, "--set telemetry.export.otlp.endpoint=x:4317 "+flags).collectors(t) {
					for rname, rcv := range asMap(cfg["receivers"]) {
						for _, j := range asList(asMap(asMap(rcv)["config"])["scrape_configs"]) {
							job := asMap(j)
							if !slices.ContainsFunc(asList(job["kubernetes_sd_configs"]), func(c any) bool { return asMap(c)["role"] == "pod" }) {
								continue
							}
							jobs++
							ok := false
							for _, rc := range asList(job["relabel_configs"]) {
								m := asMap(rc)
								onPort := m["action"] == "keep" && slices.Contains(asList(m["source_labels"]), any("__meta_kubernetes_pod_container_port_name"))
								ok = ok || onPort || m["target_label"] == "__address__"
							}
							if !ok {
								t.Errorf("%s/%s: job %v discovers pods but neither keeps the one named port nor sets the address, so a container with no port is a target of its own: %v", cm, rname, job["job_name"], job["relabel_configs"])
							}
						}
					}
				}
				if jobs == 0 {
					t.Fatalf("no pod-discovering scrape job rendered for %s; the check has nothing to look at", name)
				}
			})
		}
	})

	t.Run("the gateway leaves the provenance on the resource and stamps what the vocabulary says it stamps", func(t *testing.T) {
		cfg := onlyCollector(t, fusionRender(t))
		procs := asMap(cfg["processors"])
		signals := []string{"metric_statements", "log_statements", "trace_statements"}
		statements := func(processor, signal string) (out []string) {
			for _, s := range asList(asMap(procs[processor])[signal]) {
				for _, st := range asList(asMap(s)["statements"]) {
					out = append(out, fmt.Sprintf("%v %v", asMap(s)["context"], st))
				}
			}
			return out
		}

		// A sender's continuum.* on a data point, a record or a span would sit next to the resource's own as a second value;
		// the resource is where the stores and the dashboards read it, so it is the one level not stripped.
		strip := regexp.MustCompile(`^(\w+) delete_matching_keys\(attributes, "(.*)"\)$`)
		for _, signal := range signals {
			levels := map[string]bool{}
			for _, st := range statements("transform/provenance", signal) {
				m := strip.FindStringSubmatch(st)
				if m == nil {
					t.Errorf("transform/provenance does something other than strip a sender's attributes: %s", st)
					continue
				}
				levels[m[1]] = true
				pattern, err := strconv.Unquote(`"` + m[2] + `"`) // an OTTL string: \\. is the regular expression's \.
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range fusionapi.Vocabulary {
					if strings.HasPrefix(l.Attr, "continuum.") && !regexp.MustCompile(pattern).MatchString(l.Attr) {
						t.Errorf("transform/provenance (%s) leaves %s from a sender on the %s", m[2], l.Attr, m[1])
					}
				}
			}
			if levels["resource"] || len(levels) != 1 {
				t.Errorf("transform/provenance strips %v for %s; want exactly the data point, record or span, never the resource", keys(levels), signal)
			}
			for _, l := range fusionapi.Vocabulary {
				if l.By == "gateway" && !slices.ContainsFunc(statements("transform/category", signal), func(st string) bool { return strings.Contains(st, `["`+l.Attr+`"]`) }) {
					t.Errorf("the vocabulary says the gateway stamps %s, and transform/category never sets it for %s", l.Attr, signal)
				}
			}
		}
		for name, p := range pipelines(cfg) {
			for _, want := range []string{"transform/provenance", "transform/category"} {
				if !slices.Contains(p, want) {
					t.Errorf("the %s pipeline does not run %s: %v", name, want, p)
				}
			}
		}
	})

	t.Run("Prometheus promotes and Loki indexes exactly the attributes the vocabulary says, as rendered", func(t *testing.T) {
		r := fusionRender(t)
		prom := fileOf(t, r, "ct-fusion-prometheus", "prometheus.yml")
		promoted := asList(asMap(prom["otlp"])["promote_resource_attributes"])
		loki := fileOf(t, r, "ct-fusion-loki", "loki.yaml")
		indexed := slices.Clone(lokiIndexedByDefault)
		for _, c := range asList(asMap(asMap(asMap(loki["limits_config"])["otlp_config"])["resource_attributes"])["attributes_config"]) {
			if asMap(c)["action"] == "index_label" {
				for _, a := range asList(asMap(c)["attributes"]) {
					indexed = append(indexed, a.(string))
				}
			}
		}
		for _, l := range fusionapi.Vocabulary {
			if got := slices.Contains(promoted, any(l.Attr)); got != l.Promote {
				t.Errorf("%s: the vocabulary says Promote=%v, the rendered Prometheus promotes it: %v", l.Attr, l.Promote, got)
			}
			if got := slices.Contains(indexed, l.Attr); got != l.Index {
				t.Errorf("%s: the vocabulary says Index=%v, the rendered Loki indexes it: %v (otherwise it is structured metadata)", l.Attr, l.Index, got)
			}
		}
	})

	t.Run("a receiver that requires a client certificate keeps reading the CA it trusts", func(t *testing.T) {
		a := newIntentRig(t)
		cl := a.approvedCluster(t, "8f3c2a9e-1002-4222-8333-944455556666")
		doc := a.createOperatorDoc(t, a.cookie, extBody("athens", cl))
		for name, cfg := range map[string]map[string]any{
			"the central gateway": onlyCollector(t, fusionRender(t)),
			"a regional operator": onlyCollector(t, operatorRender(t, doc["install"].(string))),
		} {
			n := 0
			for _, proto := range asMap(asMap(asMap(asMap(cfg["receivers"])["otlp"])["protocols"])) {
				tls := asMap(asMap(proto)["tls"])
				if tls["client_ca_file"] == nil {
					continue
				}
				n++
				if tls["client_ca_file_reload"] != true {
					t.Errorf("%s: an OTLP receiver requires client certificates and never re-reads the CA that signs them (tls: %v)", name, tls)
				}
			}
			if n == 0 {
				t.Errorf("%s: no receiver requires a client certificate, so there is nothing to check", name)
			}
		}
	})
}

// containerArgs is the arguments of the first container of a rendered workload.
func containerArgs(d map[string]any) (out []string) {
	pod := asMap(asMap(asMap(d["spec"])["template"])["spec"])
	for _, a := range asList(asMap(asList(pod["containers"])[0])["args"]) {
		out = append(out, a.(string))
	}
	return out
}

func doctorLinks(t *testing.T) {
	t.Run("the gateway sends to Services the stores listen behind, and each store takes what it is sent", func(t *testing.T) {
		r := fusionRender(t)
		exporters := asMap(onlyCollector(t, r)["exporters"])
		hasService := func(endpoint string) bool {
			host, port, _ := strings.Cut(strings.TrimSuffix(regexp.MustCompile(`^\w+://|/.*$`).ReplaceAllString(endpoint, ""), "/"), ":")
			for _, p := range asList(asMap(asMap(r["Service/"+strings.SplitN(host, ".", 2)[0]])["spec"])["ports"]) {
				if fmt.Sprint(asMap(p)["port"]) == port {
					return true
				}
			}
			return false
		}
		for name, e := range exporters {
			if endpoint, _ := asMap(e)["endpoint"].(string); !hasService(endpoint) {
				t.Errorf("exporter %s sends to %s, which is no Service port of this release", name, endpoint)
			}
		}
		// What each of them needs switched on to take it.
		if args := containerArgs(r["StatefulSet/ct-fusion-prometheus"]); !slices.Contains(args, "--web.enable-otlp-receiver") {
			t.Errorf("Prometheus is sent OTLP and does not run its OTLP receiver: %v", args)
		}
		if loki := fileOf(t, r, "ct-fusion-loki", "loki.yaml"); asMap(loki["limits_config"])["allow_structured_metadata"] != true {
			t.Error("Loki is sent OTLP logs, whose context is structured metadata, and does not allow it")
		}
		tempo := fileOf(t, r, "ct-fusion-tempo", "tempo.yaml")
		if asMap(asMap(asMap(asMap(tempo["distributor"])["receivers"])["otlp"])["protocols"])["grpc"] == nil {
			t.Error("Tempo is sent OTLP over gRPC and has no such receiver")
		}
	})

	t.Run("a datasource link leads to a datasource that is provisioned, and to data something produces", func(t *testing.T) {
		names := vocabNames()
		for name, flags := range map[string][]string{
			"all three stores": nil,
			"no traces":        {"--set", "tempo.enabled=false"},
			"no logs":          {"--set", "loki.enabled=false"},
			"metrics only":     {"--set", "tempo.enabled=false", "--set", "loki.enabled=false"},
		} {
			t.Run(name, func(t *testing.T) {
				r := fusionRender(t, flags...)
				provisioned := map[string]map[string]any{}
				for _, d := range asList(fileOf(t, r, "ct-fusion-grafana-provisioning", "datasources.yaml")["datasources"]) {
					provisioned[asMap(d)["uid"].(string)] = asMap(d)
				}
				// Each link names the datasource it leads to by uid.
				var walk func(v any, where string)
				walk = func(v any, where string) {
					switch v := v.(type) {
					case map[string]any:
						if uid, ok := v["datasourceUid"].(string); ok && provisioned[uid] == nil {
							t.Errorf("%s leads to datasource %q, which is not provisioned", where, uid)
						}
						for k, c := range v {
							walk(c, where+"."+k)
						}
					case []any:
						for _, c := range v {
							walk(c, where)
						}
					}
				}
				for uid, d := range provisioned {
					walk(d["jsonData"], uid)
				}
				// A trace's link to its logs selects a stream by the tags it maps, so each must be an index label of Loki.
				if traces := provisioned["fusion-traces"]; traces != nil {
					for _, tag := range asList(asMap(asMap(traces["jsonData"])["tracesToLogsV2"])["tags"]) {
						key, value := asMap(tag)["key"].(string), asMap(tag)["value"].(string)
						if l, ok := names[value]; !ok || l.Attr != key || !l.Index {
							t.Errorf("the trace-to-logs link maps %s to %s, which is not a Loki stream label of the same attribute in the vocabulary", key, value)
						}
					}
					// The features Grafana can link to only when something writes the data they read.
					generated := fileOf(t, r, "ct-fusion-tempo", "tempo.yaml")
					processors := asList(asMap(asMap(asMap(generated["overrides"])["defaults"])["metrics_generator"])["processors"])
					for link, processor := range map[string]string{"serviceMap": "service-graphs", "tracesToMetrics": "span-metrics"} {
						if asMap(traces["jsonData"])[link] != nil && !slices.Contains(processors, any(processor)) {
							t.Errorf("the traces datasource has a %s link and Tempo does not run its %s processor, so there is nothing to link to", link, processor)
						}
					}
				}
				if prom := provisioned["fusion-metrics"]; prom != nil && asMap(prom["jsonData"])["exemplarTraceIdDestinations"] != nil {
					if args := containerArgs(r["StatefulSet/ct-fusion-prometheus"]); !slices.Contains(args, "--enable-feature=exemplar-storage") {
						t.Errorf("the metrics datasource follows exemplars to traces and Prometheus does not store any: %v", args)
					}
				}
			})
		}
	})

	t.Run("the delivery dashboard's pod patterns match the pods that run the collectors", func(t *testing.T) {
		a := newIntentRig(t)
		cl := a.approvedCluster(t, "8f3c2a9e-1003-4222-8333-944455556666")
		doc := a.createOperatorDoc(t, a.cookie, extBody("athens", cl))
		var patterns []string
		queries, _ := dashboards(t)
		for _, q := range queries {
			for _, m := range matchersIn(q.expr) {
				if q.file == "delivery.json" && m.label == "k8s_pod_name" && m.op == "=~" {
					patterns = append(patterns, m.value)
				}
			}
		}
		if len(patterns) == 0 {
			t.Fatal("the delivery dashboard selects no pod by name; the check has nothing to look at")
		}
		for what, r := range map[string]renderedChart{
			"the agent's collectors": agentRender(t, staleNothing, "--set telemetry.export.otlp.endpoint=x:4317"),
			"a regional operator's":  operatorRender(t, doc["install"].(string)),
		} {
			configs := r.collectors(t)
			n := 0
			for key, d := range r {
				if !strings.HasPrefix(key, "Deployment/") && !strings.HasPrefix(key, "DaemonSet/") {
					continue
				}
				if !slices.ContainsFunc(keys(configs), func(cm string) bool { return strings.Contains(fmt.Sprint(d), cm) }) {
					continue // the discovery agent itself, not a collector
				}
				n++
				pod := strings.SplitN(key, "/", 2)[1] + "-7d9f8b-x2k4p"
				if !slices.ContainsFunc(patterns, func(re string) bool { return regexp.MustCompile("^(?:" + re + ")$").MatchString(pod) }) {
					t.Errorf("%s: pod %s (%s) is matched by none of the delivery dashboard's patterns %q", what, pod, key, patterns)
				}
			}
			if n == 0 {
				t.Errorf("%s: no workload runs a collector configuration", what)
			}
		}
	})
}

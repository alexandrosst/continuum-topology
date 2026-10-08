package chart

import (
	"strings"
	"testing"
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

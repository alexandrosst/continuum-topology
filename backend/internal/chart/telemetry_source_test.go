package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ikhnos.source says which extractor produced a record (hostmetrics, kepler, link-prober, ...). Two properties are pinned:
// every pipeline of both collectors carries a source processor, and it runs AFTER the person's own extraProcessors (so a
// processor of theirs cannot make a record claim another source) and right before the tail (tags, provenance, batch).

func pipelineProcessors(t *testing.T, cfg map[string]any) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	pipes := rv2CMap(rv2CMap(cfg["service"])["pipelines"])
	for name, p := range pipes {
		for _, x := range rv2CList(rv2CMap(p)["processors"]) {
			out[name] = append(out[name], x.(string))
		}
	}
	return out
}

func requireSourceBeforeTail(t *testing.T, who string, pipelines map[string][]string, wantProc map[string]string) {
	t.Helper()
	if len(pipelines) == 0 {
		t.Fatalf("%s: no pipelines rendered", who)
	}
	for name, procs := range pipelines {
		want := wantProc[name]
		if want == "" {
			t.Errorf("%s: pipeline %s has no expected source processor (test table out of date)", who, name)
			continue
		}
		idx, extra := -1, -1
		for i, p := range procs {
			if p == want {
				idx = i
			}
			if p == "mine" {
				extra = i
			}
		}
		if idx < 0 {
			t.Errorf("%s: pipeline %s lacks %s: %v", who, name, want, procs)
			continue
		}
		if extra >= 0 && extra > idx {
			t.Errorf("%s: pipeline %s runs the person's processor after the source: %v", who, name, procs)
		}
		// Only the tail may follow: resource/tags (when tags are set), resource/ikhnos, batch.
		for _, p := range procs[idx+1:] {
			if p != "resource/tags" && p != "resource/ikhnos" && p != "batch" {
				t.Errorf("%s: pipeline %s has %s after the source processor: %v", who, name, p, procs)
			}
		}
		if procs[len(procs)-1] != "batch" || procs[len(procs)-2] != "resource/ikhnos" && procs[len(procs)-2] != "resource/tags" {
			t.Errorf("%s: pipeline %s does not end with provenance, batch: %v", who, name, procs)
		}
	}
}

func TestEveryPipelineStampsItsSourceAfterThePersonsProcessors(t *testing.T) {
	extra := []string{
		"--set", "telemetry.processors.extraProcessorNames={mine}",
		"--set", "telemetry.processors.extraTracesProcessorNames={}",
		"--set", "telemetry.processors.extraProcessors.mine.attributes[0].key=ikhnos.source",
		"--set", "telemetry.processors.extraProcessors.mine.attributes[0].value=forged",
		"--set", "telemetry.processors.extraProcessors.mine.attributes[0].action=upsert",
	}
	cluster := append(rv2CTarget(
		"--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.clusterLinks.metrics.enabled=true", "--set", "flowObserver.enabled=true",
	), extra...)
	cm := render(t, withTel(cluster...)...).configmaps[rv2CCluster+"-config"]
	cfg := otelConfig(t, cm.Data)
	requireSourceBeforeTail(t, "cluster", pipelineProcessors(t, cfg), map[string]string{
		"metrics/infra": "transform/ikhnos_source_infra", "metrics/app": "transform/ikhnos_source_app",
		"logs/infra": "transform/ikhnos_source_logs_infra", "logs/app": "transform/ikhnos_source_logs_app",
		"traces": "transform/ikhnos_source_traces",
	})
	host := append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.systemLogs.logs.enabled=true"}, extra...)
	hcm := render(t, withTel(host...)...).configmaps["ikhnos-telemetry-host-config"]
	requireSourceBeforeTail(t, "host", pipelineProcessors(t, otelConfig(t, hcm.Data)), map[string]string{
		"metrics": "transform/ikhnos_source_metrics", "logs": "transform/ikhnos_source_logs",
	})
}

// The statements are OTTL: a wrong path is only found when the collector parses them. Both configs load in the real collector.
func TestSourceProcessorsLoadInTheRealCollector(t *testing.T) {
	bin := collectorBinary(t)
	cases := map[string]struct {
		cm   string
		args []string
	}{
		"cluster": {rv2CCluster + "-config", rv2CTarget(
			"--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
			"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
			"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.clusterLinks.metrics.enabled=true", "--set", "flowObserver.enabled=true",
			"--set", "telemetry.networkLatency.metrics.enabled=true", "--set", "measurements.enabled=true")},
		"host": {"ikhnos-telemetry-host-config", []string{"--set", "telemetry.resourceUsage.metrics.enabled=true",
			"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.journaling=host"}},
	}
	for name, c := range cases {
		cm := render(t, withTel(c.args...)...).configmaps[c.cm]
		f := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(f, []byte(collectorConfig(t, cm.Data["otel-collector-config.yaml"])), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "validate", "--config", f)
		cmd.Env = append(os.Environ(), "POD_IP=127.0.0.1", "NODE_NAME=n1", "NODE_IP=127.0.0.1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: the collector refuses the rendered config: %v\n%s", name, err, out)
		}
	}
}

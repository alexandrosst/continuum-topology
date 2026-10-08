package chart

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

// How FUSION copes with load it was not sized for: Prometheus must stop before its volume is full, and the gateway
// must hold data while a store is down, convert what Prometheus cannot ingest and keep to its memory limit.

func prometheusArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	r := fusionRender(t, "f", extra...)
	set, ok := r.sets["f-fusion-prometheus"]
	if !ok {
		t.Fatalf("no Prometheus StatefulSet (have %v)", mapKeys(r.sets))
	}
	return set.Spec.Template.Spec.Containers[0].Args
}

func argValue(args []string, flag string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

// A time-only retention lets a busy install fill the volume, and a full volume stops Prometheus ingesting. The size
// limit is 85% of the volume, worked out from the quantity the volume is asked for.
func TestFusionPrometheusRetentionSizeIsDerivedFromTheVolume(t *testing.T) {
	for _, c := range []struct{ storage, want string }{
		{"10Gi", "8704MB"},  // 10240 MiB * 85%
		{"500Mi", "425MB"},  // 500 MiB * 85%
		{"1Ti", "891289MB"}, // 1048576 MiB * 85%
		{"10G", "8105MB"},   // 10^10 bytes is 9536 MiB: decimal units are not binary ones
		{"2.5Gi", "2176MB"}, // 2560 MiB * 85%
		{"1073741824", "870MB"},
	} {
		got, ok := argValue(prometheusArgs(t, "--set-string", "prometheus.storage="+c.storage), "--storage.tsdb.retention.size")
		if !ok || got != c.want {
			t.Errorf("storage %s: retention.size = %q (present %v), want %s", c.storage, got, ok, c.want)
		}
	}
	// The time limit stays beside it.
	if got, _ := argValue(prometheusArgs(t), "--storage.tsdb.retention.time"); got != "15d" {
		t.Errorf("retention.time = %q", got)
	}
}

func TestFusionPrometheusRetentionSizeCanBeOverriddenOrTurnedOff(t *testing.T) {
	if got, _ := argValue(prometheusArgs(t, "--set", "prometheus.retentionSize=5GB"), "--storage.tsdb.retention.size"); got != "5GB" {
		t.Errorf("override = %q, want 5GB", got)
	}
	if got, ok := argValue(prometheusArgs(t, "--set-string", "prometheus.retentionSize=0"), "--storage.tsdb.retention.size"); !ok || got != "0" {
		t.Errorf("an explicit 0 must reach Prometheus (it means no size limit), got %q present %v", got, ok)
	}
	// `--set prometheus.retentionSize=0` is the natural way to write that, and it arrives as the number 0 (not the string):
	// a template truthiness test would take it for "unset" and quietly apply the 85% limit instead.
	if got, ok := argValue(prometheusArgs(t, "--set", "prometheus.retentionSize=0"), "--storage.tsdb.retention.size"); !ok || got != "0" {
		t.Errorf("--set retentionSize=0 (a number) must turn the size limit off, got %q present %v", got, ok)
	}
	// An emptyDir has no size of its own to take a share of.
	if got, ok := argValue(prometheusArgs(t, "--set", "persistence.enabled=false"), "--storage.tsdb.retention.size"); ok {
		t.Errorf("a size limit %q was derived with no durable volume", got)
	}
	if got, _ := argValue(prometheusArgs(t, "--set", "persistence.enabled=false", "--set", "prometheus.retentionSize=2GB"), "--storage.tsdb.retention.size"); got != "2GB" {
		t.Errorf("an explicit size without a volume = %q, want 2GB", got)
	}
	for _, bad := range []string{"8Gi", "lots", "-1GB", "8 GB"} {
		out, err := fusionTemplate(t, "f", "--set", "prometheus.retentionSize="+bad)
		if err == nil {
			t.Errorf("retentionSize %q must be refused\n%s", bad, out)
		}
	}
}

func TestFusionPrometheusStorageStillAcceptsEverythingTheSchemaDoes(t *testing.T) {
	for _, q := range []string{"10", "10k", "10M", "10T", "10Ki", "0.5Gi"} {
		if out, err := fusionTemplate(t, "f", "--set-string", "prometheus.storage="+q); err != nil {
			t.Errorf("storage %s: %v\n%s", q, err, out)
		}
	}
}

func centralPipelines(t *testing.T, extra ...string) map[string][]string {
	t.Helper()
	cfg := centralConfig(t, extra...)
	out := map[string][]string{}
	for name, p := range sub(t, cfg, "service", "pipelines") {
		out[name] = stringsOf(t, p.(map[string]any)["processors"])
	}
	return out
}

// Prometheus ingests cumulative series only, so a sender's delta metrics are converted at the gateway. The processor
// is the contrib one by its name in the pinned collector, only on the metrics pipeline, after the limiter and before
// the batch.
func TestFusionCentralConvertsDeltaMetricsBeforeBatching(t *testing.T) {
	p := centralPipelines(t)
	want := []string{"memory_limiter", "transform/provenance", "transform/category", "delta_to_cumulative", "batch"}
	if got := p["metrics"]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("metrics processors = %v, want %v", got, want)
	}
	for _, sig := range []string{"logs", "traces"} {
		if indexOf(p[sig], "delta_to_cumulative") >= 0 {
			t.Errorf("%s pipeline has the metrics-only delta_to_cumulative processor: %v", sig, p[sig])
		}
	}
	proc := sub(t, centralConfig(t), "processors", "delta_to_cumulative")
	if proc["max_stale"] != "5m" || proc["max_streams"] != float64(100000) {
		t.Errorf("delta_to_cumulative = %v", proc)
	}
	// The processor type is the one the pinned image ships under that name.
	if !strings.Contains(fusionImageTag(t), "0.160.0") {
		t.Errorf("the central image moved to %s: re-check that delta_to_cumulative is still the processor's name there", fusionImageTag(t))
	}

	off := centralPipelines(t, "--set", "central.deltaToCumulative.enabled=false")
	if got := off["metrics"]; strings.Join(got, ",") != "memory_limiter,transform/provenance,transform/category,batch" {
		t.Errorf("with the conversion off, metrics processors = %v", got)
	}
	if _, ok := sub(t, centralConfig(t, "--set", "central.deltaToCumulative.enabled=false"), "processors")["delta_to_cumulative"]; ok {
		t.Errorf("the processor is defined although it is off")
	}
	// Prometheus off: there is no metrics pipeline to put it in.
	if _, ok := centralPipelines(t, "--set", "prometheus.enabled=false")["metrics"]; ok {
		t.Errorf("a metrics pipeline without Prometheus")
	}
	if _, ok := sub(t, centralConfig(t, "--set", "prometheus.enabled=false"), "processors")["delta_to_cumulative"]; ok {
		t.Errorf("delta_to_cumulative is defined with no metrics pipeline to use it")
	}
}

func fusionImageTag(t *testing.T) string {
	t.Helper()
	r := fusionRender(t, "f")
	return r.deploys["f-fusion-central"].Spec.Template.Spec.Containers[0].Image
}

func TestFusionCentralBatchSizesAreExplicitAndChecked(t *testing.T) {
	b := sub(t, centralConfig(t), "processors", "batch")
	if b["send_batch_size"] != float64(2048) || b["send_batch_max_size"] != float64(4096) || b["timeout"] != "5s" {
		t.Errorf("default batch = %v", b)
	}
	b = sub(t, centralConfig(t, "--set", "central.batch.sendBatchSize=10", "--set", "central.batch.sendBatchMaxSize=20"), "processors", "batch")
	if b["send_batch_size"] != float64(10) || b["send_batch_max_size"] != float64(20) {
		t.Errorf("overrides not honoured: %v", b)
	}
	if out, err := fusionTemplate(t, "f", "--set", "central.batch.sendBatchSize=500", "--set", "central.batch.sendBatchMaxSize=100"); err == nil || !strings.Contains(out, "sendBatchMaxSize") {
		t.Errorf("a maximum below the batch size must be refused, got err=%v\n%s", err, out)
	}
}

func TestFusionCentralExportersHoldDataWhileAStoreIsDown(t *testing.T) {
	cfg := centralConfig(t)
	for _, name := range []string{"otlphttp/metrics", "otlphttp/logs", "otlp/traces"} {
		ex := sub(t, cfg, "exporters", name)
		if rt := sub(t, ex, "retry_on_failure"); rt["enabled"] != true || rt["max_elapsed_time"] != "30m" {
			t.Errorf("%s retry_on_failure = %v", name, rt)
		}
		if q := sub(t, ex, "sending_queue"); q["enabled"] != true || q["queue_size"] != float64(64) || q["storage"] != nil {
			t.Errorf("%s sending_queue = %v", name, q)
		}
	}
	cfg = centralConfig(t, "--set", "central.queue.size=32", "--set", "central.queue.retryMaxElapsedTime=1h")
	if q := sub(t, cfg, "exporters", "otlp/traces", "sending_queue"); q["queue_size"] != float64(32) {
		t.Errorf("central.queue.size not honoured: %v", q)
	}
	if rt := sub(t, cfg, "exporters", "otlphttp/logs", "retry_on_failure"); rt["max_elapsed_time"] != "1h" {
		t.Errorf("central.queue.retryMaxElapsedTime not honoured: %v", rt)
	}
}

func TestFusionCentralPersistentQueueIsOptInAndUsesAnEmptyDir(t *testing.T) {
	r := fusionRender(t, "f")
	for _, v := range r.deploys["f-fusion-central"].Spec.Template.Spec.Volumes {
		if v.Name == "queue" {
			t.Fatalf("a queue volume by default")
		}
	}
	if got := stringsOf(t, sub(t, centralConfig(t), "service")["extensions"]); len(got) != 1 || got[0] != "health_check" {
		t.Errorf("default extensions = %v", got)
	}

	args := []string{"--set", "central.queue.persistent.enabled=true", "--set", "central.queue.persistent.sizeLimit=3Gi"}
	r = fusionRender(t, "f", args...)
	pod := r.deploys["f-fusion-central"].Spec.Template.Spec
	found := false
	for _, v := range pod.Volumes {
		if v.Name == "queue" && v.EmptyDir != nil && v.EmptyDir.SizeLimit != nil && v.EmptyDir.SizeLimit.String() == "3Gi" {
			found = true
		}
	}
	if !found {
		t.Errorf("no 3Gi emptyDir for the queue: %+v", pod.Volumes)
	}
	mounted := false
	for _, m := range pod.Containers[0].VolumeMounts {
		mounted = mounted || (m.Name == "queue" && m.MountPath == "/queue" && !m.ReadOnly)
	}
	if !mounted {
		t.Errorf("/queue is not mounted writable")
	}
	if !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("the persistent queue must not need a writable root filesystem")
	}
	cfg := centralConfig(t, args...)
	if dir := sub(t, cfg, "extensions", "file_storage/queue")["directory"]; dir != "/queue" {
		t.Errorf("file_storage directory = %v", dir)
	}
	if ext := stringsOf(t, sub(t, cfg, "service")["extensions"]); indexOf(ext, "file_storage/queue") < 0 || indexOf(ext, "health_check") < 0 {
		t.Errorf("extensions = %v", ext)
	}
	for _, name := range []string{"otlphttp/metrics", "otlphttp/logs", "otlp/traces"} {
		if got := sub(t, cfg, "exporters", name, "sending_queue")["storage"]; got != "file_storage/queue" {
			t.Errorf("%s queue storage = %v", name, got)
		}
	}
}

func TestFusionCentralGOMEMLIMITFollowsTheMemoryLimit(t *testing.T) {
	gml := func(d appsv1.Deployment) string {
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "GOMEMLIMIT" {
				return e.Value
			}
		}
		return ""
	}
	if got := gml(fusionRender(t, "f").deploys["f-fusion-central"]); got != "322122547" { // 80% of 384Mi
		t.Errorf("GOMEMLIMIT = %q, want 322122547 (80%% of 384Mi)", got)
	}
	if got := gml(fusionRender(t, "f", "--set", "central.resources.limits.memory=2Gi").deploys["f-fusion-central"]); got != "1717986918" {
		t.Errorf("GOMEMLIMIT = %q, want 1717986918 (80%% of 2Gi)", got)
	}
	// No memory limit to take a share of: no env, and the render does not fail.
	if got := gml(fusionRender(t, "f", "--set", "central.resources.limits=null").deploys["f-fusion-central"]); got != "" {
		t.Errorf("GOMEMLIMIT = %q with no memory limit", got)
	}
}

func TestFusionSchemaChecksTheSizingValues(t *testing.T) {
	for _, bad := range [][]string{
		{"central.queue.size=0"},
		{"central.queue.retryMaxElapsedTime=soon"},
		{"central.queue.persistent.sizeLimit=lots"},
		{"central.queue.persistnt.enabled=true"},
		{"central.batch.timeout=fast"},
		{"central.batch.sendBatchSize=0"},
		{"central.deltaToCumulative.enabled=maybe"},
		{"central.deltaToCumulative.maxStale=a-while"},
		{"central.deltaToCumulative.maxStreams=0"},
	} {
		if out, err := fusionTemplate(t, "f", "--set", bad[0]); err == nil {
			t.Errorf("%v rendered, want a schema error\n%s", bad, out)
		}
	}
}

// The memory limiter is one for the whole gateway: past its soft limit it refuses metrics, logs and traces alike. A single
// stalled store must therefore fill its own queue, and block its senders, before it can push the process that far.
func TestFusionCentralOneStalledStoreCannotTripTheMemoryLimiter(t *testing.T) {
	r := fusionRender(t, "f")
	limit := r.deploys["f-fusion-central"].Spec.Template.Spec.Containers[0].Resources.Limits.Memory().Value()
	lim := sub(t, centralConfig(t), "processors", "memory_limiter")
	soft := float64(limit) * (lim["limit_percentage"].(float64) - lim["spike_limit_percentage"].(float64)) / 100
	q := sub(t, centralConfig(t), "exporters", "otlp/traces", "sending_queue")
	worst := q["queue_size"].(float64) * sub(t, q, "batch")["max_size"].(float64)
	if worst >= soft {
		t.Errorf("one full queue holds up to %.0f MiB, past the limiter's soft limit of %.0f MiB", worst/(1<<20), soft/(1<<20))
	}
}

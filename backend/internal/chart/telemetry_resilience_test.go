package chart

import (
	"strings"
	"testing"
)

// The agent's export path has the same two defects the regional operator's had, and the same fixes (see
// regional_operator_resilience_test.go): a full queue silently dropped what the receiver had already accepted, and a batch
// capped in items rather than bytes was refused whole by a hop with a 4 MiB limit.

func TestAgentFullQueuePushesBackAndRequestsAreCutByBytes(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.export.routes.logs.endpoint=loki.example:3100", "--set", "telemetry.export.routes.logs.protocol=http",
		"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.export.queue.persistent.enabled=true")...)
	check := func(cm, name string) {
		q := sub(t, otelConfig(t, r.configmaps[cm].Data), "exporters", name, "sending_queue")
		if q["block_on_overflow"] != true {
			t.Errorf("%s %s sending_queue = %v: a full queue must block, not drop", cm, name, q)
		}
		b := sub(t, q, "batch")
		if b["sizer"] != "bytes" || b["max_size"] != float64(3145728) || b["min_size"] != float64(1) || b["flush_timeout"] != "1s" {
			t.Errorf("%s %s sending_queue.batch = %v, want bytes, min 1, max 3 MiB", cm, name, b)
		}
	}
	check("continuum-telemetry-host-config", "otlp")
	check("continuum-telemetry-host-config", "otlphttp/logs")
	check("continuum-telemetry-cluster-config", "otlp")

	cfg := hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.queue.maxRequestBytes=1048576")...)
	if got := sub(t, cfg, "exporters", "otlp", "sending_queue", "batch")["max_size"]; got != float64(1048576) {
		t.Errorf("telemetry.export.queue.maxRequestBytes is not honoured: %v", got)
	}
	cfg = hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.queue.maxRequestBytes=0")...)
	if _, ok := sub(t, cfg, "exporters", "otlp", "sending_queue")["batch"]; ok {
		t.Errorf("maxRequestBytes=0 must leave the cut off")
	}
}

func TestAgentRefusesARetryWindowTheCollectorWouldNotStartWith(t *testing.T) {
	for _, bad := range []string{"10s", "29s", "500ms"} {
		out, err := helmTemplate(t, withTel("--set", "telemetry.export.queue.retryMaxElapsedTime="+bad)...)
		if err == nil || !strings.Contains(out, "retryMaxElapsedTime") || !strings.Contains(out, "30s") {
			t.Errorf("retryMaxElapsedTime=%s rendered (err %v):\n%s", bad, err, firstLines(out, 4))
		}
	}
	for _, ok := range []string{"30s", "1m", "30m", "0s"} {
		cfg := hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.queue.retryMaxElapsedTime="+ok)...)
		if got := sub(t, cfg, "exporters", "otlp", "retry_on_failure")["max_elapsed_time"]; got != ok {
			t.Errorf("retryMaxElapsedTime=%s rendered as %v", ok, got)
		}
	}
}

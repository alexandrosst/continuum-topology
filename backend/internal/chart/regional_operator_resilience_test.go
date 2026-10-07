package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What the regional operator does when it cannot deliver, and how anyone can tell. Each of these was measured against
// real collector processes (see the review notes): the settings below are the ones that decide whether data is lost
// silently.

// A full queue must push back on the senders. The collector's default (block_on_overflow false) rejects the batch inside
// the exporter, the batch processor in front of it only logs a warning, and the receiver has long since answered 200: 36000
// log records were lost with nobody upstream told.
func TestRegionalOperatorFullQueuePushesBackOnItsSenders(t *testing.T) {
	cfg := operatorConfig(t,
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.protocol=http",
		"--set", "export.queue.persistent.enabled=true")
	for _, name := range []string{"otlp", "otlphttp/logs"} {
		q := sub(t, cfg, "exporters", name, "sending_queue")
		if q["block_on_overflow"] != true {
			t.Errorf("%s sending_queue = %v: a full queue must block, not drop", name, q)
		}
	}
}

// processors.batch counts items, not bytes: 4096 log records of 2 KiB are 8 MiB, past the 4 MiB a collector's OTLP receiver
// accepts, and the whole batch was refused with a permanent error and dropped. Every exporter therefore cuts what it sends
// to a byte size.
func TestRegionalOperatorCutsEveryRequestToAByteSize(t *testing.T) {
	cfg := operatorConfig(t,
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.protocol=http",
		"--set", "export.queue.persistent.enabled=true")
	for _, name := range []string{"otlp", "otlphttp/logs"} {
		b := sub(t, cfg, "exporters", name, "sending_queue", "batch")
		if b["sizer"] != "bytes" || b["max_size"] != float64(3145728) || b["min_size"] != float64(1) || b["flush_timeout"] != "1s" {
			t.Errorf("%s sending_queue.batch = %v, want bytes, min 1, max 3 MiB", name, b)
		}
	}
	cfg = operatorConfig(t, "--set", "export.queue.maxRequestBytes=1048576")
	if got := sub(t, cfg, "exporters", "otlp", "sending_queue", "batch")["max_size"]; got != float64(1048576) {
		t.Errorf("export.queue.maxRequestBytes is not honoured: %v", got)
	}
	cfg = operatorConfig(t, "--set", "export.queue.maxRequestBytes=0")
	if _, ok := sub(t, cfg, "exporters", "otlp", "sending_queue")["batch"]; ok {
		t.Errorf("maxRequestBytes=0 must leave the cut off")
	}
	// The heartbeat is not a relay: it keeps its own tiny queue.
	cfg = operatorConfig(t, hbOn...)
	if _, ok := sub(t, cfg, "exporters", "otlphttp/heartbeat", "sending_queue")["batch"]; ok {
		t.Errorf("the heartbeat's queue must not be cut")
	}
	if _, ok := sub(t, cfg, "exporters", "otlphttp/heartbeat", "sending_queue")["block_on_overflow"]; ok {
		t.Errorf("a stale heartbeat must be dropped, not waited for")
	}
}

// The collector refuses to start when retry_on_failure.max_elapsed_time is shorter than max_interval (30s), and nothing in
// the schema's duration pattern says so: the pod crash-looped on a value that looked fine.
func TestRegionalOperatorRefusesARetryWindowTheCollectorWouldNotStartWith(t *testing.T) {
	for _, bad := range []string{"10s", "29s", "500ms", "1s"} {
		out, err := operatorHelmTemplate(t, "--set", "export.queue.retryMaxElapsedTime="+bad)
		if err == nil || !strings.Contains(out, "retryMaxElapsedTime") || !strings.Contains(out, "30s") {
			t.Errorf("retryMaxElapsedTime=%s rendered (err %v):\n%s", bad, err, firstLines(out, 4))
		}
	}
	for _, ok := range []string{"30s", "31s", "1m", "30m", "2h", "0s", "0m", "30000ms"} {
		cfg := operatorConfig(t, "--set", "export.queue.retryMaxElapsedTime="+ok)
		if got := sub(t, cfg, "exporters", "otlp", "retry_on_failure")["max_elapsed_time"]; got != ok {
			t.Errorf("retryMaxElapsedTime=%s rendered as %v", ok, got)
		}
	}
}

func firstLines(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, "\n")
}

// The collector refuses to start for these too, and a render that cannot start is better refused here.
func TestRegionalOperatorRefusesSettingsThatCrashTheCollector(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--set", "processors.memoryLimiter.spikeLimitPercentage=80", "--set", "processors.memoryLimiter.limitPercentage=80"}, "spikeLimitPercentage"},
		{[]string{"--set", "processors.memoryLimiter.spikeLimitPercentage=90"}, "spikeLimitPercentage"},
		{[]string{"--set", "health.port=4317"}, "health.port 4317"},
		{[]string{"--set", "selfMetrics.port=4318"}, "selfMetrics.port 4318"},
		{[]string{"--set", "selfMetrics.port=13133"}, "must differ"},
	} {
		out, err := operatorHelmTemplate(t, tc.args...)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Errorf("%v: want a refusal naming %q, got err=%v\n%s", tc.args, tc.want, err, firstLines(out, 4))
		}
	}
	// The same ports are fine when they do not clash, and a clashing metrics port does not matter while the metrics are off.
	if _, err := operatorHelmTemplate(t, "--set", "selfMetrics.enabled=false", "--set", "selfMetrics.port=4317"); err != nil {
		t.Errorf("selfMetrics.port is unused while selfMetrics is off: %v", err)
	}
}

// The health check cannot see a failing exporter (measured against the pinned collector: check_collector_pipeline is
// ignored, and the component-status mode stays healthy while the exporters retry and drop), so the one place a destination
// that cannot be reached shows up as a number is the collector's own metrics. They are on, on the pod's own address.
func TestRegionalOperatorSelfMetricsAreOnByDefaultOnThePodAddress(t *testing.T) {
	r := operatorRender(t)
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	readers, _ := sub(t, cfg, "service", "telemetry", "metrics")["readers"].([]any)
	if len(readers) != 1 {
		t.Fatalf("the default render has no self-metrics reader: %v", sub(t, cfg, "service"))
	}
	prom := sub(t, readers[0].(map[string]any), "pull", "exporter", "prometheus")
	if prom["host"] != "${env:POD_IP}" || prom["port"] != float64(8888) {
		t.Errorf("self metrics bind to %v, want the pod's address on 8888", prom)
	}
	c := r.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0]
	ip := containerEnv(c, "POD_IP")
	if ip == nil || ip.ValueFrom == nil || ip.ValueFrom.FieldRef == nil || ip.ValueFrom.FieldRef.FieldPath != "status.podIP" {
		t.Errorf("POD_IP is not the downward-API pod address: %+v", ip)
	}
	found := false
	for _, p := range c.Ports {
		if p.Name == "metrics" && p.ContainerPort == 8888 {
			found = true
		}
	}
	if !found {
		t.Errorf("no metrics container port: %+v", c.Ports)
	}
	// Opting out removes all of it.
	off := operatorRender(t, "--set", "selfMetrics.enabled=false")
	if svc, _ := otelConfig(t, off.configmaps["op-regional-operator-config"].Data)["service"].(map[string]any); svc["telemetry"] != nil {
		t.Errorf("selfMetrics.enabled=false still rendered service.telemetry: %v", svc["telemetry"])
	}
	for _, e := range off.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "POD_IP" {
			t.Errorf("POD_IP without self metrics")
		}
	}
}

// The probes must not claim more than they know: the chart does not turn on the health check settings that look as if they
// watch the exporters but do not, because a probe that is green while nothing is delivered is worse than one that says it
// only knows the process is up.
func TestRegionalOperatorHealthCheckDoesNotPretendToWatchTheExporters(t *testing.T) {
	cfg := operatorConfig(t)
	hc := sub(t, cfg, "extensions", "health_check")
	for _, k := range []string{"check_collector_pipeline", "use_v2", "component_health", "http", "grpc"} {
		if _, ok := hc[k]; ok {
			t.Errorf("health_check sets %q: %v - it is ignored (or reports nothing) for exporters in the pinned collector", k, hc)
		}
	}
	text, err := os.ReadFile("continuum-regional-operator/templates/_telemetry.tpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "never report a") {
		t.Errorf("_telemetry.tpl no longer says why the health check does not follow the exporters")
	}
}

// Several replicas exist to stay available: they are replaced one at a time and a node drain may not take them all.
// A single replica has neither (a budget of "none unavailable" would stop its node from ever being drained).
func TestRegionalOperatorReplicasUpdateOneAtATimeAndHaveABudget(t *testing.T) {
	one, err := operatorHelmTemplate(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(one, "PodDisruptionBudget") {
		t.Errorf("a PodDisruptionBudget for one replica")
	}
	r := operatorRender(t)
	if s := r.deployments["op-regional-operator"].Spec.Strategy; s.Type != "Recreate" {
		t.Errorf("one replica: strategy = %+v, want Recreate", s)
	}

	three, err := operatorHelmTemplate(t, "--set", "replicaCount=3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(three, "kind: PodDisruptionBudget") || !strings.Contains(three, "maxUnavailable: 1") {
		t.Errorf("no budget for three replicas:\n%s", three)
	}
	r = operatorRender(t, "--set", "replicaCount=3")
	s := r.deployments["op-regional-operator"].Spec.Strategy
	if s.Type != "RollingUpdate" || s.RollingUpdate == nil || s.RollingUpdate.MaxUnavailable.IntValue() != 0 || s.RollingUpdate.MaxSurge.IntValue() != 1 {
		t.Errorf("three replicas: strategy = %+v, want RollingUpdate with maxUnavailable 0 and maxSurge 1", s)
	}
	off, err := operatorHelmTemplate(t, "--set", "replicaCount=3", "--set", "podDisruptionBudget.enabled=false")
	if err != nil || strings.Contains(off, "PodDisruptionBudget") {
		t.Errorf("podDisruptionBudget.enabled=false still renders one (err %v)", err)
	}
	if out, err := operatorHelmTemplate(t, "--set", "podDisruptionBudget.maxUnavailable=0"); err == nil {
		t.Errorf("a budget of 0 was accepted:\n%s", out)
	}
}

// A bearer token and a credential header reach the collector as environment variables, which a running container never
// sees change: rotating one and running the install command again must restart the pod, like a renewed certificate does.
func TestRegionalOperatorChecksumCoversTheTokenSecrets(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("recv-token", map[string]string{"token": "t1"})
	k.set("dest-token", map[string]string{"token": "d1"})
	k.set("logs-token", map[string]string{"token": "l1"})
	k.set("hb-auth", map[string]string{"token": "h1"})
	args := []string{"--set", "export.otlp.endpoint=collector.example:4317",
		"--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=recv-token",
		"--set", "export.otlp.auth.secretName=dest-token",
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.auth.secretName=logs-token",
		"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=https://continuum.example.com/api/v1/operator-heartbeat",
		"--set", "heartbeat.auth.secretName=hb-auth"}
	read := func() string {
		return podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, kc, args...))["rel-regional-operator"]["checksum/mtls"]
	}
	prev := read()
	if prev == "" {
		t.Fatal("no checksum although the token Secrets exist")
	}
	for _, name := range []string{"recv-token", "dest-token", "logs-token", "hb-auth"} {
		k.set(name, map[string]string{"token": "rotated-" + name})
		got := read()
		if got == prev || got == "" {
			t.Errorf("rotating %s did not change the checksum", name)
		}
		prev = got
	}
}

// The notes are what a person reads right after installing: they say a green pod proves nothing about delivery, how to
// see it, and warn about a protocol on the other protocol's port (a gRPC client on 4318 is refused for good and every
// batch dropped).
func TestRegionalOperatorNotesSayHowToSeeDeliveryAndWarnAboutTheWrongPort(t *testing.T) {
	notes := func(extra ...string) string {
		h, err := exec.LookPath("helm")
		if err != nil {
			t.Skip("helm is not installed")
		}
		dir := filepath.Join(t.TempDir(), "continuum-regional-operator")
		copyTree(t, "continuum-regional-operator", dir)
		text, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"_notes.tpl": `{{- define "test.notes" -}}` + string(text) + `{{- end -}}`,
			"notes.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: {{ include \"test.notes\" . | quote }}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		args := append([]string{"template", "op", dir, "--show-only", "templates/notes.yaml", "--set", "export.otlp.endpoint=collector.example:4317"}, extra...)
		out, err := exec.Command(h, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template of the notes: %v\n%s", err, out)
		}
		return string(out)
	}
	n := notes()
	for _, want := range []string{"Is it delivering?", "port-forward deploy/op-regional-operator 8888", "otelcol_exporter_queue_size", "otelcol_exporter_send_failed"} {
		if !strings.Contains(n, want) {
			t.Errorf("the notes lack %q:\n%s", want, n)
		}
	}
	if strings.Contains(n, "conventional OTLP") {
		t.Errorf("a warning for a correct endpoint:\n%s", n)
	}
	// The wrong port for the protocol (4318 with grpc, 4317 with http) used to be a warning here; it now fails the render
	// (export.checkPorts=false allows it) - see TestOperatorRv2WrongPortFailsTheRender in telemetry_rv2_X_test.go.
	if w := notes("--set", "export.otlp.endpoint=collector.example:4318", "--set", "export.otlp.protocol=http"); strings.Contains(w, "conventional OTLP") {
		t.Errorf("a warning for http on 4318:\n%s", w)
	}
}

package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// What a regional operator does with a certificate that is renewed, a next hop that is down and a Service that is
// exposed: the settings that decide whether it keeps working unattended.

// operatorConfig is the collector config of a default render plus extra values.
func operatorConfig(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	r := operatorRender(t, extra...)
	cm, ok := r.configmaps["op-regional-operator-config"]
	if !ok {
		t.Fatalf("no config ConfigMap (have %v)", keys(r.configmaps))
	}
	return otelConfig(t, cm.Data)
}

func sub(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	for _, p := range path {
		next, ok := m[p].(map[string]any)
		if !ok {
			t.Fatalf("no %q under %v in %v", p, path, m)
		}
		m = next
	}
	return m
}

var operatorReceiverTLS = []string{"--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=op-receiver-tls"}

// A renewed certificate reaches a running collector without a restart only if every TLS block that carries a
// certificate says how often to look again.
func TestRegionalOperatorReloadsEveryCertificateItPresentsOrServes(t *testing.T) {
	cfg := operatorConfig(t, append(append([]string{}, operatorReceiverTLS...),
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=op-client",
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.protocol=http",
		"--set", "export.routes.logs.tls.mtls.enabled=true", "--set", "export.routes.logs.tls.mtls.secretName=logs-client",
		"--set", "export.routes.traces.endpoint=tempo.example:4317", "--set", "export.routes.traces.tls.caFile=/some/ca.crt")...)
	for _, proto := range []string{"grpc", "http"} {
		if got := sub(t, cfg, "receivers", "otlp", "protocols", proto, "tls")["reload_interval"]; got != "1h" {
			t.Errorf("receiver %s tls reload_interval = %v, want 1h", proto, got)
		}
	}
	for _, name := range []string{"otlp", "otlphttp/logs"} {
		if got := sub(t, cfg, "exporters", name, "tls")["reload_interval"]; got != "1h" {
			t.Errorf("exporter %s tls reload_interval = %v, want 1h", name, got)
		}
	}
	// A block with no certificate of its own has nothing to reload.
	if _, ok := sub(t, cfg, "exporters", "otlp/traces", "tls")["reload_interval"]; ok {
		t.Errorf("a CA-only exporter has no certificate to reload")
	}
	// And nothing is added where there is no TLS block at all.
	plain := operatorConfig(t)
	if tls, ok := sub(t, plain, "exporters", "otlp")["tls"].(map[string]any); ok && tls["reload_interval"] != nil {
		t.Errorf("reload_interval on an exporter with no certificate: %v", tls)
	}
}

// Every exporter has its retry window and queue spelled out, and they are the same ones whichever exporter it is.
func TestRegionalOperatorExportersHaveABoundedQueueAndARetryWindow(t *testing.T) {
	cfg := operatorConfig(t,
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.protocol=http")
	for _, name := range []string{"otlp", "otlphttp/logs"} {
		ex := sub(t, cfg, "exporters", name)
		retry := sub(t, ex, "retry_on_failure")
		if retry["enabled"] != true || retry["max_elapsed_time"] != "30m" {
			t.Errorf("%s retry_on_failure = %v, want enabled with max_elapsed_time 30m", name, retry)
		}
		q := sub(t, ex, "sending_queue")
		if q["enabled"] != true || q["queue_size"] != float64(256) {
			t.Errorf("%s sending_queue = %v, want enabled with queue_size 256", name, q)
		}
		if _, ok := q["storage"]; ok {
			t.Errorf("%s queue is persistent by default: %v", name, q)
		}
	}
	cfg = operatorConfig(t, "--set", "export.queue.size=40", "--set", "export.queue.retryMaxElapsedTime=2h")
	if q := sub(t, cfg, "exporters", "otlp", "sending_queue"); q["queue_size"] != float64(40) {
		t.Errorf("export.queue.size is not honoured: %v", q)
	}
	if r := sub(t, cfg, "exporters", "otlp", "retry_on_failure"); r["max_elapsed_time"] != "2h" {
		t.Errorf("export.queue.retryMaxElapsedTime is not honoured: %v", r)
	}
	// The heartbeat keeps its own short window: it must not queue stale "alive" messages.
	cfg = operatorConfig(t, hbOn...)
	if r := sub(t, cfg, "exporters", "otlphttp/heartbeat", "retry_on_failure"); r["max_elapsed_time"] != "90s" {
		t.Errorf("the heartbeat's retry window changed: %v", r)
	}
	if q := sub(t, cfg, "exporters", "otlphttp/heartbeat", "sending_queue"); q["queue_size"] != float64(3) {
		t.Errorf("the heartbeat's queue changed: %v", q)
	}
}

// The persistent queue is opt-in and needs a writable place: the root filesystem is read-only, so an emptyDir.
func TestRegionalOperatorPersistentQueueIsOptInAndUsesAnEmptyDir(t *testing.T) {
	r := operatorRender(t)
	c := r.deployments["op-regional-operator"].Spec.Template.Spec
	for _, v := range c.Volumes {
		if v.Name == "queue" {
			t.Fatalf("a queue volume by default")
		}
	}
	if ext := sub(t, operatorConfig(t), "extensions"); ext["file_storage/queue"] != nil {
		t.Errorf("file_storage by default: %v", ext)
	}

	args := []string{"--set", "export.queue.persistent.enabled=true", "--set", "export.queue.persistent.sizeLimit=2Gi",
		"--set", "export.routes.logs.endpoint=loki.example:3100", "--set", "export.routes.logs.protocol=http"}
	r = operatorRender(t, args...)
	pod := r.deployments["op-regional-operator"].Spec.Template.Spec
	var vol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "queue" {
			vol = &pod.Volumes[i]
		}
	}
	if vol == nil || vol.EmptyDir == nil || vol.EmptyDir.SizeLimit == nil || vol.EmptyDir.SizeLimit.String() != "2Gi" {
		t.Fatalf("queue volume = %+v, want an emptyDir limited to 2Gi", vol)
	}
	mounted := false
	for _, m := range pod.Containers[0].VolumeMounts {
		if m.Name == "queue" && m.MountPath == "/queue" && !m.ReadOnly {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("/queue is not mounted writable: %+v", pod.Containers[0].VolumeMounts)
	}
	if !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("the persistent queue must not need a writable root filesystem")
	}
	cfg := operatorConfig(t, args...)
	if dir := sub(t, cfg, "extensions", "file_storage/queue")["directory"]; dir != "/queue" {
		t.Errorf("file_storage directory = %v", dir)
	}
	if !containsAny(sub(t, cfg, "service")["extensions"].([]any), "file_storage/queue") {
		t.Errorf("service.extensions lacks file_storage/queue: %v", sub(t, cfg, "service")["extensions"])
	}
	for _, name := range []string{"otlp", "otlphttp/logs"} {
		if got := sub(t, cfg, "exporters", name, "sending_queue")["storage"]; got != "file_storage/queue" {
			t.Errorf("%s queue storage = %v", name, got)
		}
	}
}

func TestRegionalOperatorBatchSizesAreExplicitAndChecked(t *testing.T) {
	b := sub(t, operatorConfig(t), "processors", "batch")
	if b["send_batch_size"] != float64(2048) || b["send_batch_max_size"] != float64(4096) || b["timeout"] != "5s" {
		t.Errorf("default batch = %v", b)
	}
	b = sub(t, operatorConfig(t, "--set", "processors.batch.sendBatchSize=100", "--set", "processors.batch.sendBatchMaxSize=200", "--set", "processors.batch.timeout=1s"), "processors", "batch")
	if b["send_batch_size"] != float64(100) || b["send_batch_max_size"] != float64(200) || b["timeout"] != "1s" {
		t.Errorf("batch overrides not honoured: %v", b)
	}
	if out, err := operatorHelmTemplate(t, "--set", "processors.batch.sendBatchSize=500", "--set", "processors.batch.sendBatchMaxSize=100"); err == nil || !strings.Contains(out, "sendBatchMaxSize") {
		t.Errorf("a maximum below the batch size must be refused, got err=%v\n%s", err, out)
	}
}

// GOMEMLIMIT is 80% of the memory limit, so the Go runtime collects harder before the container is killed.
func TestRegionalOperatorGOMEMLIMITFollowsTheMemoryLimit(t *testing.T) {
	gml := func(extra ...string) string {
		r := operatorRender(t, extra...)
		for _, e := range r.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0].Env {
			if e.Name == "GOMEMLIMIT" {
				return e.Value
			}
		}
		return ""
	}
	if got := gml(); got != "322122547" { // 80% of 384Mi
		t.Errorf("GOMEMLIMIT = %q, want 322122547 (80%% of 384Mi)", got)
	}
	if got := gml("--set", "resources.limits.memory=1Gi"); got != "858993459" {
		t.Errorf("GOMEMLIMIT = %q, want 858993459 (80%% of 1Gi)", got)
	}
}

// A NodePort Service listens on a port Kubernetes picks at random each time it is created, so an address recorded for
// the operator breaks whenever the Service is recreated, unless the port is pinned.
func TestRegionalOperatorNodePortCanBePinned(t *testing.T) {
	port := func(extra ...string) (int32, bool) {
		r := operatorRender(t, extra...)
		for _, p := range r.services["op-regional-operator"].Spec.Ports {
			if p.Name == "otlp-grpc" {
				return p.NodePort, true
			}
		}
		return 0, false
	}
	if got, ok := port("--set", "service.type=NodePort", "--set", "service.nodePort=30443"); !ok || got != 30443 {
		t.Errorf("pinned NodePort = %d, want 30443", got)
	}
	if got, _ := port("--set", "service.type=NodePort"); got != 0 {
		t.Errorf("an unpinned NodePort must be left to Kubernetes, got %d", got)
	}
	if got, _ := port("--set", "service.type=LoadBalancer"); got != 0 {
		t.Errorf("a LoadBalancer Service got nodePort %d", got)
	}
	for _, bad := range [][]string{
		{"--set", "service.type=ClusterIP", "--set", "service.nodePort=30443"},
		{"--set", "service.type=LoadBalancer", "--set", "service.nodePort=30443"},
	} {
		if out, err := operatorHelmTemplate(t, bad...); err == nil || !strings.Contains(out, "service.nodePort") {
			t.Errorf("%v must be refused, got err=%v\n%s", bad, err, out)
		}
	}
	if out, err := operatorHelmTemplate(t, "--set", "service.type=NodePort", "--set", "service.nodePort=abc"); err == nil {
		t.Errorf("a non-numeric nodePort must be refused by the schema\n%s", out)
	}
}

// The heartbeat can verify a server whose certificate is signed by a private CA: the Secret is mounted and named in
// the exporter. Pinned here because the install command for a self-signed server depends on it.
func TestRegionalOperatorHeartbeatCASecretIsMountedAndUsed(t *testing.T) {
	args := append(append([]string{}, hbOn...), "--set", "heartbeat.tls.caSecretName=server-ca", "--set", "heartbeat.tls.caSecretKey=root.pem")
	r := operatorRender(t, args...)
	pod := r.deployments["op-regional-operator"].Spec.Template.Spec
	found := false
	for _, v := range pod.Volumes {
		found = found || (v.Name == "heartbeat-ca" && v.Secret != nil && v.Secret.SecretName == "server-ca")
	}
	if !found {
		t.Errorf("heartbeat CA Secret is not mounted: %+v", pod.Volumes)
	}
	if got := sub(t, operatorConfig(t, args...), "exporters", "otlphttp/heartbeat", "tls")["ca_file"]; got != "/heartbeat-ca/root.pem" {
		t.Errorf("heartbeat ca_file = %v", got)
	}
}

func TestRegionalOperatorSchemaChecksTheDeliveryValues(t *testing.T) {
	for _, bad := range [][]string{
		{"--set", "export.queue.size=0"},
		{"--set", "export.queue.retryMaxElapsedTime=soon"},
		{"--set", "export.queue.persistent.sizeLimit=lots"},
		{"--set", "export.queue.persistnt.enabled=true"},
		{"--set", "processors.batch.timeout=fast"},
		{"--set", "processors.batch.sendBatchSize=0"},
		{"--set", "rolloutOnSecretChange=sometimes"},
	} {
		if out, err := operatorHelmTemplate(t, bad...); err == nil {
			t.Errorf("%v rendered, want a schema error\n%s", bad, out)
		}
	}
}

package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// telemetry.export.routes.<signal>: each signal type can go to its own destination, as its own exporter, and
// no pipeline names an exporter that is not its own signal type's.

var allSignals = []string{
	"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
	"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
	"--set", "telemetry.traces.traces.enabled=true",
}

func routeFlags(signal, endpoint, protocol string, more ...string) []string {
	p := "telemetry.export.routes." + signal + "."
	out := []string{"--set-string", p + "endpoint=" + endpoint, "--set", p + "protocol=" + protocol}
	for _, m := range more {
		out = append(out, "--set", p+m)
	}
	return out
}

func pipelineExporters(t *testing.T, cfg map[string]any) map[string][]string {
	t.Helper()
	pipelines, _ := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	out := map[string][]string{}
	for name, raw := range pipelines {
		p, _ := raw.(map[string]any)
		for _, e := range p["exporters"].([]any) {
			out[name] = append(out[name], e.(string))
		}
	}
	return out
}

func envOf(c corev1.Container) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		m[e.Name] = e
	}
	return m
}

// Metrics to Prometheus, logs to Loki, traces to Zipkin: three destinations, three exporters, no default one.
func TestTelemetryRoutesSendEachSignalTypeToItsOwnExporter(t *testing.T) {
	args := append([]string{}, allSignals...)
	args = append(args, routeFlags("metrics", "prometheus.obs.svc:9090/api/v1/otlp", "http", "tls.insecure=true")...)
	args = append(args, routeFlags("logs", "loki.obs.svc:3100/otlp", "http", "tls.insecure=true")...)
	args = append(args, routeFlags("traces", "zipkin.obs.svc:9411", "zipkin", "tls.insecure=true")...)
	r := render(t, args...)

	host := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	for name, cfg := range map[string]map[string]any{"host": host, "cluster": cluster} {
		exporters, _ := cfg["exporters"].(map[string]any)
		for _, absent := range []string{"otlp", "otlphttp", "zipkin"} {
			if _, ok := exporters[absent]; ok {
				t.Errorf("%s: default exporter %q rendered though every signal has a route", name, absent)
			}
		}
		want := map[string]string{
			"otlphttp/metrics": "http://prometheus.obs.svc:9090/api/v1/otlp",
			"otlphttp/logs":    "http://loki.obs.svc:3100/otlp",
		}
		// The per-node collector carries no traces, so it gets no traces route (nor its credential).
		if name == "cluster" {
			want["zipkin/traces"] = "http://zipkin.obs.svc:9411/api/v2/spans"
		}
		if len(exporters) != len(want) {
			t.Errorf("%s: exporters = %+v, want exactly %d routes", name, exporters, len(want))
		}
		for exp, endpoint := range want {
			e, ok := exporters[exp].(map[string]any)
			if !ok {
				t.Errorf("%s: no %s exporter: %+v", name, exp, exporters)
				continue
			}
			if e["endpoint"] != endpoint {
				t.Errorf("%s: %s endpoint = %v, want %s", name, exp, e["endpoint"], endpoint)
			}
		}
		for pipeline, exp := range pipelineExporters(t, cfg) {
			sig := strings.SplitN(pipeline, "/", 2)[0]
			if len(exp) != 1 || !strings.HasSuffix(exp[0], "/"+sig) {
				t.Errorf("%s pipeline %s exporters = %v, want only the %s route's", name, pipeline, exp, sig)
			}
		}
	}
}

// A route for one signal type only: the others keep the default destination, and both exist side by side.
// A route's credential and certificate go only to the collector that carries that signal type: the
// per-node DaemonSet never sees the traces route's.
func TestTelemetryRouteSecretsReachOnlyTheCollectorThatUsesThem(t *testing.T) {
	args := append([]string{"--set", "telemetry.export.otlp.endpoint=gw:4317"}, allSignals...)
	args = append(args, routeFlags("traces", "tempo:4317", "grpc", "auth.secretName=tempo-token", "tls.mtls.enabled=true", "tls.mtls.secretName=tempo-cert")...)
	r := render(t, args...)
	host := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	for _, e := range host.Containers[0].Env {
		if strings.Contains(e.Name, "AUTH") {
			t.Errorf("the host DaemonSet got %s", e.Name)
		}
	}
	for _, v := range host.Volumes {
		if v.Secret != nil {
			t.Errorf("the host DaemonSet mounts Secret %s", v.Secret.SecretName)
		}
	}
	cl := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec
	if _, ok := envOf(cl.Containers[0])["CONTINUUM_TELEMETRY_AUTH_TRACES"]; !ok {
		t.Error("the cluster collector is missing the traces credential")
	}
}

func TestTelemetryRouteForOneTypeLeavesTheRestOnTheDefault(t *testing.T) {
	args := append([]string{"--set", "telemetry.export.otlp.endpoint=gw.example.com:4317"}, allSignals...)
	args = append(args, routeFlags("traces", "zipkin.obs.svc:9411", "zipkin", "tls.insecure=true")...)
	r := render(t, args...)
	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ := cluster["exporters"].(map[string]any)
	if _, ok := exporters["otlp"]; !ok {
		t.Fatalf("the default otlp exporter is missing: %+v", exporters)
	}
	if _, ok := exporters["zipkin/traces"]; !ok {
		t.Fatalf("the traces route is missing: %+v", exporters)
	}
	for pipeline, exp := range pipelineExporters(t, cluster) {
		want := "otlp"
		if pipeline == "traces" {
			want = "zipkin/traces"
		}
		if len(exp) != 1 || exp[0] != want {
			t.Errorf("pipeline %s exporters = %v, want [%s]", pipeline, exp, want)
		}
	}
	// The host collector carries no traces, so it has the default only.
	host := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	hexp, _ := host["exporters"].(map[string]any)
	if _, ok := hexp["zipkin/traces"]; ok {
		t.Errorf("the host collector rendered a traces route though it has no traces pipeline")
	}
}

// Each route reads its own credential from its own variable, and mounts its own client certificate.
func TestTelemetryRoutesKeepCredentialsAndCertificatesSeparate(t *testing.T) {
	args := append([]string{}, allSignals...)
	args = append(args, routeFlags("metrics", "prom.example.com:443", "http", "auth.secretName=prom-token", "auth.headerName=X-Prom")...)
	args = append(args, routeFlags("logs", "op-eu1.continuum-system.svc:4317", "grpc", "tls.mtls.enabled=true", "tls.mtls.secretName=op-eu1-client")...)
	args = append(args, routeFlags("traces", "tempo.example.com:4317", "grpc", "auth.secretName=tempo-token", "auth.secretKey=key")...)
	r := render(t, args...)

	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ := cluster["exporters"].(map[string]any)
	metrics, _ := exporters["otlphttp/metrics"].(map[string]any)
	if h, _ := metrics["headers"].(map[string]any); h["X-Prom"] != "${env:CONTINUUM_TELEMETRY_AUTH_METRICS}" {
		t.Errorf("metrics headers = %+v", metrics["headers"])
	}
	logs, _ := exporters["otlp/logs"].(map[string]any)
	if tls, _ := logs["tls"].(map[string]any); tls["cert_file"] != "/export-mtls-logs/tls.crt" || tls["ca_file"] != "/export-mtls-logs/ca.crt" {
		t.Errorf("logs tls = %+v", logs["tls"])
	}
	traces, _ := exporters["otlp/traces"].(map[string]any)
	if h, _ := traces["headers"].(map[string]any); h["Authorization"] != "${env:CONTINUUM_TELEMETRY_AUTH_TRACES}" {
		t.Errorf("traces headers = %+v", traces["headers"])
	}

	dep := r.deployments["continuum-telemetry-cluster"]
	if dep.Name == "" {
		t.Fatalf("no cluster deployment rendered: %v", keys(r.deployments))
	}
	c := dep.Spec.Template.Spec.Containers[0]
	env := envOf(c)
	for name, want := range map[string][2]string{
		"CONTINUUM_TELEMETRY_AUTH_METRICS": {"prom-token", "token"},
		"CONTINUUM_TELEMETRY_AUTH_TRACES":  {"tempo-token", "key"},
	} {
		ref := env[name].ValueFrom
		if ref == nil || ref.SecretKeyRef.Name != want[0] || ref.SecretKeyRef.Key != want[1] {
			t.Errorf("%s = %+v, want secret %s key %s", name, env[name], want[0], want[1])
		}
	}
	if _, ok := env["CONTINUUM_TELEMETRY_AUTH"]; ok {
		t.Error("the default credential variable is set though no default destination is in use")
	}
	if _, ok := env["CONTINUUM_TELEMETRY_AUTH_LOGS"]; ok {
		t.Error("the logs route has no credential but got a variable")
	}
	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	if mounts["export-mtls-logs"] != "/export-mtls-logs" || mounts["export-mtls"] != "" {
		t.Errorf("mounts = %v", mounts)
	}
	secrets := map[string]string{}
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Secret != nil {
			secrets[v.Name] = v.Secret.SecretName
		}
	}
	if secrets["export-mtls-logs"] != "op-eu1-client" {
		t.Errorf("volumes = %v", secrets)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// With the default alone nothing about routes shows: same exporter names, same variable, same mount.
func TestTelemetryWithoutRoutesIsExactlyTheDefault(t *testing.T) {
	args := append([]string{"--set", "telemetry.export.otlp.endpoint=gw:4317", "--set", "telemetry.export.otlp.auth.secretName=tok",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=cli"}, allSignals...)
	r := render(t, args...)
	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ := cluster["exporters"].(map[string]any)
	if len(exporters) != 1 {
		t.Errorf("exporters = %+v, want just otlp", exporters)
	}
	c := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]
	if _, ok := envOf(c)["CONTINUUM_TELEMETRY_AUTH"]; !ok {
		t.Error("the default credential variable is missing")
	}
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == "export-mtls" && m.MountPath == "/export-mtls" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("the default client certificate is not mounted at /export-mtls")
	}
}

func TestTelemetryRouteValidation(t *testing.T) {
	zip := func(signal string) []string { return routeFlags(signal, "z:9411", "zipkin") }
	for name, args := range map[string][]string{
		"a zipkin route for metrics":             append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true"}, zip("metrics")...),
		"a route mtls with no secret":            append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true"}, routeFlags("metrics", "a:4317", "grpc", "tls.mtls.enabled=true")...),
		"no default and an unrouted signal type": append(append([]string{}, allSignals...), routeFlags("traces", "a:4317", "grpc")...),
		"default zipkin with unrouted metrics":   {"--set", "telemetry.export.otlp.endpoint=z:9411", "--set", "telemetry.export.otlp.protocol=zipkin", "--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.resourceUsage.metrics.enabled=true"},
	} {
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%s: rendered, want a refusal:\n%.300s", name, out)
		}
	}
	// Routing the metrics elsewhere is exactly what makes a default zipkin legal.
	ok := []string{"--set", "telemetry.export.otlp.endpoint=z:9411", "--set", "telemetry.export.otlp.protocol=zipkin",
		"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.resourceUsage.metrics.enabled=true"}
	ok = append(ok, routeFlags("metrics", "prom:9090/api/v1/otlp", "http")...)
	if out, err := helmTemplate(t, ok...); err != nil {
		t.Errorf("default zipkin with the metrics routed away should render: %v\n%.400s", err, out)
	}
	// All three routed: no default endpoint is required at all.
	all := append([]string{}, allSignals...)
	all = append(all, routeFlags("metrics", "a:4317", "grpc")...)
	all = append(all, routeFlags("logs", "b:4317", "grpc")...)
	all = append(all, routeFlags("traces", "c:4317", "grpc")...)
	if out, err := helmTemplate(t, all...); err != nil {
		t.Errorf("every signal routed, no default endpoint, should render: %v\n%.400s", err, out)
	}
	// An unknown route name is the schema's to refuse.
	if out, err := helmTemplate(t, "--set", "telemetry.export.routes.events.endpoint=x:1"); err == nil {
		t.Errorf("an unknown route rendered:\n%.300s", out)
	}
}

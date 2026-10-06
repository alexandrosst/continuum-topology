package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// operatorHelmTemplateNoDefault renders the chart with ONLY the given values: unlike operatorHelmTemplate it does
// not add export.otlp.endpoint, since these tests are about when that may be left out.
func operatorHelmTemplateNoDefault(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), RegionalOperator.Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(h, append([]string{"template", "op", tgz}, extra...)...).CombinedOutput()
	return string(out), err
}

// A regional operator sends each signal type to the destination it is given: the default (export.otlp), or its
// own route. FUSION is three routes: metrics to Prometheus and logs to Loki over OTLP/HTTP, traces to Tempo over
// gRPC.

type operatorCollector struct {
	exporters map[string]map[string]any
	pipelines map[string][]string
}

func operatorCollectorConfig(t *testing.T, extra ...string) operatorCollector {
	t.Helper()
	r := operatorRender(t, extra...)
	var cm string
	for name, c := range r.configmaps {
		if strings.HasSuffix(name, "-config") {
			cm = c.Data["otel-collector-config.yaml"]
		}
	}
	var cfg struct {
		Exporters map[string]map[string]any `json:"exporters"`
		Service   struct {
			Pipelines map[string]struct {
				Exporters []string `json:"exporters"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := sigsyaml.Unmarshal([]byte(cm), &cfg); err != nil {
		t.Fatal(err)
	}
	out := operatorCollector{exporters: cfg.Exporters, pipelines: map[string][]string{}}
	for n, p := range cfg.Service.Pipelines {
		out.pipelines[n] = p.Exporters
	}
	return out
}

var fusionRoutes = []string{
	"--set", "export.routes.metrics.endpoint=f-fusion-prometheus.obs.svc:9090/api/v1/otlp", "--set", "export.routes.metrics.protocol=http", "--set", "export.routes.metrics.tls.insecure=true",
	"--set", "export.routes.logs.endpoint=f-fusion-loki.obs.svc:3100/otlp", "--set", "export.routes.logs.protocol=http", "--set", "export.routes.logs.tls.insecure=true",
	"--set", "export.routes.traces.endpoint=f-fusion-tempo.obs.svc:4317", "--set", "export.routes.traces.tls.insecure=true",
}

func TestOperatorRoutesSendEachSignalWhereItIsPointed(t *testing.T) {
	c := operatorCollectorConfig(t, fusionRoutes...)
	for sig, want := range map[string]string{"metrics": "otlphttp/metrics", "logs": "otlphttp/logs", "traces": "otlp/traces"} {
		if got := c.pipelines[sig]; len(got) != 1 || got[0] != want {
			t.Errorf("%s pipeline exports to %v, want [%s]", sig, got, want)
		}
	}
	// HTTP destinations get a scheme (tls.insecure picks http) and the collector appends /v1/<signal> itself, so
	// the endpoint stops at the store's OTLP base path.
	if got := c.exporters["otlphttp/metrics"]["endpoint"]; got != "http://f-fusion-prometheus.obs.svc:9090/api/v1/otlp" {
		t.Errorf("metrics endpoint = %v", got)
	}
	if got := c.exporters["otlphttp/logs"]["endpoint"]; got != "http://f-fusion-loki.obs.svc:3100/otlp" {
		t.Errorf("logs endpoint = %v", got)
	}
	if got := c.exporters["otlp/traces"]["endpoint"]; got != "f-fusion-tempo.obs.svc:4317" {
		t.Errorf("traces endpoint = %v", got)
	}
	if tls, _ := c.exporters["otlp/traces"]["tls"].(map[string]any); tls["insecure"] != true {
		t.Errorf("traces tls = %v, want insecure (in-cluster)", tls)
	}
	// With a route for every type nothing uses the default, so it is not rendered at all.
	for _, name := range []string{"otlp", "otlphttp"} {
		if _, ok := c.exporters[name]; ok {
			t.Errorf("the default exporter %q is rendered though every signal has its own route", name)
		}
	}
}

func TestOperatorDefaultDestinationStillServesWhatHasNoRoute(t *testing.T) {
	c := operatorCollectorConfig(t, "--set", "export.routes.traces.endpoint=tempo:4317")
	if got := c.pipelines["traces"]; len(got) != 1 || got[0] != "otlp/traces" {
		t.Errorf("traces exports to %v", got)
	}
	for _, sig := range []string{"metrics", "logs"} {
		if got := c.pipelines[sig]; len(got) != 1 || got[0] != "otlp" {
			t.Errorf("%s exports to %v, want the default [otlp]", sig, got)
		}
	}
	if _, ok := c.exporters["otlp"]; !ok {
		t.Error("the default exporter is missing though two signals use it")
	}
}

// export.otlp.protocol used to be accepted and ignored: http rendered the gRPC exporter at an HTTP endpoint.
func TestOperatorDefaultProtocolHTTPIsHonoured(t *testing.T) {
	c := operatorCollectorConfig(t, "--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=backend.example:4318", "--set", "export.otlp.tls.insecure=true")
	for _, sig := range []string{"metrics", "logs", "traces"} {
		if got := c.pipelines[sig]; len(got) != 1 || got[0] != "otlphttp" {
			t.Errorf("%s exports to %v, want [otlphttp]", sig, got)
		}
	}
	if got := c.exporters["otlphttp"]["endpoint"]; got != "http://backend.example:4318" {
		t.Errorf("endpoint = %v, want a scheme added from tls.insecure", got)
	}
	// Secure by default: no insecure toggle means https.
	c = operatorCollectorConfig(t, "--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=backend.example:4318")
	if got := c.exporters["otlphttp"]["endpoint"]; got != "https://backend.example:4318" {
		t.Errorf("endpoint = %v, want https unless told otherwise", got)
	}
	// A scheme the user wrote is kept, not doubled.
	c = operatorCollectorConfig(t, "--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=https://backend.example/otlp")
	if got := c.exporters["otlphttp"]["endpoint"]; got != "https://backend.example/otlp" {
		t.Errorf("endpoint = %v", got)
	}
}

func TestOperatorEachRouteHasItsOwnCredential(t *testing.T) {
	args := append([]string{}, fusionRoutes...)
	args = append(args,
		"--set", "export.routes.metrics.auth.secretName=prom-token", "--set", "export.routes.metrics.auth.headerName=X-Token",
		"--set", "export.routes.traces.auth.secretName=tempo-token", "--set", "export.routes.traces.auth.secretKey=key",
	)
	r := operatorRender(t, args...)
	var dep = r.deployments
	if len(dep) != 1 {
		t.Fatalf("%d deployments", len(dep))
	}
	env := map[string][2]string{}
	for _, d := range dep {
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				env[e.Name] = [2]string{e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key}
			}
		}
	}
	if env["CONTINUUM_OPERATOR_EXPORT_AUTH_METRICS"] != [2]string{"prom-token", "token"} || env["CONTINUUM_OPERATOR_EXPORT_AUTH_TRACES"] != [2]string{"tempo-token", "key"} {
		t.Errorf("credential env = %v", env)
	}
	if _, ok := env["CONTINUUM_OPERATOR_EXPORT_AUTH_LOGS"]; ok {
		t.Error("logs has no credential but got an env entry")
	}
	if _, ok := env["CONTINUUM_OPERATOR_EXPORT_AUTH"]; ok {
		t.Error("the default destination is unused, so its credential must not be injected")
	}
	c := operatorCollectorConfig(t, args...)
	headers, _ := c.exporters["otlphttp/metrics"]["headers"].(map[string]any)
	if headers["X-Token"] != "${env:CONTINUUM_OPERATOR_EXPORT_AUTH_METRICS}" {
		t.Errorf("metrics headers = %v", headers)
	}
	// The value itself never reaches the config, only a reference to the variable.
	if _, ok := c.exporters["otlphttp/logs"]["headers"]; ok {
		t.Error("logs got a header without a credential")
	}
}

func TestOperatorStillNeedsADefaultUnlessEverySignalIsRouted(t *testing.T) {
	// No default and only one route: metrics and logs have nowhere to go.
	out, err := operatorHelmTemplateNoDefault(t, "--set", "export.routes.metrics.endpoint=prom:9090")
	if err == nil || !strings.Contains(out, "every signal type") {
		t.Errorf("want a refusal explaining a destination per signal type is needed, got err=%v\n%s", err, out)
	}
	// All three routed: the default may be empty.
	if out, err := operatorHelmTemplateNoDefault(t, fusionRoutes...); err != nil {
		t.Errorf("a fully routed operator must not need export.otlp.endpoint: %v\n%s", err, out)
	}
}

func TestOperatorRoutesAreSchemaChecked(t *testing.T) {
	for _, set := range []string{"export.routes.metrics.protocol=zipkin", "export.routes.metrics.endpont=x", "export.routes.profiles.endpoint=x"} {
		if out, err := operatorHelmTemplate(t, "--set", set); err == nil {
			t.Errorf("--set %s was accepted:\n%s", set, out)
		}
	}
}

// A regional operator can present a client certificate to its destination (another operator, or FUSION's central
// operator, whose receiver requires one): the Secret is mounted per destination and the exporter points at it.
func TestOperatorExportMTLSMountsClientCertificate(t *testing.T) {
	r := operatorRender(t,
		"--set", "export.otlp.endpoint=central.obs.svc:4317",
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=to-central")
	c := operatorCollectorConfig(t,
		"--set", "export.otlp.endpoint=central.obs.svc:4317",
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=to-central")
	tls, _ := c.exporters["otlp"]["tls"].(map[string]any)
	if tls["cert_file"] != "/export-mtls/tls.crt" || tls["key_file"] != "/export-mtls/tls.key" || tls["ca_file"] != "/export-mtls/ca.crt" {
		t.Errorf("exporter tls = %v, want the mounted client certificate", tls)
	}
	var dep string
	for _, d := range r.deployments {
		b, _ := sigsyaml.Marshal(d)
		dep = string(b)
	}
	if !strings.Contains(dep, "mountPath: /export-mtls") || !strings.Contains(dep, "secretName: to-central") {
		t.Errorf("deployment does not mount the client certificate Secret:\n%s", dep)
	}
}

func TestOperatorExportMTLSWorksPerRouteOverHTTP(t *testing.T) {
	c := operatorCollectorConfig(t, append(append([]string{}, fusionRoutes...),
		"--set", "export.routes.metrics.tls.mtls.enabled=true", "--set", "export.routes.metrics.tls.mtls.secretName=m")...)
	tls, _ := c.exporters["otlphttp/metrics"]["tls"].(map[string]any)
	if tls["cert_file"] != "/export-mtls-metrics/tls.crt" {
		t.Errorf("metrics route tls = %v", tls)
	}
	if tls, ok := c.exporters["otlphttp/logs"]["tls"]; ok {
		t.Errorf("the logs route has no client certificate but renders tls %v", tls)
	}
}

func TestOperatorExportMTLSRequiresASecret(t *testing.T) {
	out, err := operatorHelmTemplateNoDefault(t, "--set", "export.otlp.endpoint=x:4317", "--set", "export.otlp.tls.mtls.enabled=true")
	if err == nil || !strings.Contains(out, "export.otlp.tls.mtls.secretName") {
		t.Errorf("want a refusal naming the missing secretName, got err=%v out=%s", err, out)
	}
}

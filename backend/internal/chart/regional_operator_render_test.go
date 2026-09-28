package chart

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// operatorRendered is what `helm template` produced for the regional-operator chart, by kind.
type operatorRendered struct {
	deployments map[string]appsv1.Deployment
	services    map[string]corev1.Service
	configmaps  map[string]corev1.ConfigMap
	policies    map[string]networkingv1.NetworkPolicy
}

// operatorHelmTemplate renders the regional-operator chart. It skips the test when helm is not installed,
// the same convention render_test.go's own helmTemplate uses for the agent chart.
func operatorHelmTemplate(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tgz := filepath.Join(dir, RegionalOperator.Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	base := []string{"--set", "export.otlp.endpoint=collector.example:4317"}
	args := append(append([]string{"template", "op", tgz}, base...), extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	return string(out), err
}

func operatorRender(t *testing.T, extra ...string) operatorRendered {
	t.Helper()
	out, err := operatorHelmTemplate(t, extra...)
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", extra, err, out)
	}
	r := operatorRendered{map[string]appsv1.Deployment{}, map[string]corev1.Service{}, map[string]corev1.ConfigMap{}, map[string]networkingv1.NetworkPolicy{}}
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Kind == "" {
			continue
		}
		into := func(v any) {
			if err := json.Unmarshal(raw, v); err != nil {
				t.Fatal(err)
			}
		}
		name := meta.Metadata.Name
		switch meta.Kind {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deployments[name] = d
		case "Service":
			var s corev1.Service
			into(&s)
			r.services[name] = s
		case "ConfigMap":
			var c corev1.ConfigMap
			into(&c)
			r.configmaps[name] = c
		case "NetworkPolicy":
			var p networkingv1.NetworkPolicy
			into(&p)
			r.policies[name] = p
		}
	}
	return r
}

func TestRegionalOperatorReceiverAndExporterRender(t *testing.T) {
	r := operatorRender(t, "--set", "export.otlp.endpoint=collector.example:4317", "--set", "export.otlp.tls.insecure=true")
	cfg := otelConfig(t, r.configmaps["continuum-regional-operator-config"].Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	if _, ok := receivers["otlp"]; !ok {
		t.Fatalf("no otlp receiver: %+v", cfg)
	}
	exporters, _ := cfg["exporters"].(map[string]any)
	otlp, ok := exporters["otlp"].(map[string]any)
	if !ok || otlp["endpoint"] != "collector.example:4317" {
		t.Fatalf("exporters.otlp = %+v", exporters)
	}
	tls, _ := otlp["tls"].(map[string]any)
	if tls["insecure"] != true {
		t.Fatalf("tls.insecure not set: %+v", otlp)
	}
	svc, ok := r.services["continuum-regional-operator"]
	if !ok {
		t.Fatal("no Service rendered")
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("expected two ports (grpc+http), got %+v", svc.Spec.Ports)
	}
}

func TestRegionalOperatorProcessorOrderMatchesSpec(t *testing.T) {
	extra := `{"filter/drop_debug":{"error_mode":"ignore"}}`
	r := operatorRender(t, "--set", "processors.resourceDetection.enabled=true", "--set-json", "processors.extraProcessors="+extra,
		"--set", "processors.extraProcessorNames[0]=filter/drop_debug", "--set", "processors.tracesSampling.percentage=50",
		"--set", "processors.extraTracesProcessorNames[0]=tail_sampling")
	cfg := otelConfig(t, r.configmaps["continuum-regional-operator-config"].Data)
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)

	metrics, _ := pipelines["metrics"].(map[string]any)
	gotMetrics := toStrings(metrics["processors"])
	wantMetrics := []string{"memory_limiter", "resourcedetection", "redaction", "filter/drop_debug", "batch"}
	if strings.Join(gotMetrics, ",") != strings.Join(wantMetrics, ",") {
		t.Fatalf("metrics processors = %v, want %v", gotMetrics, wantMetrics)
	}

	traces, _ := pipelines["traces"].(map[string]any)
	gotTraces := toStrings(traces["processors"])
	wantTraces := []string{"memory_limiter", "resourcedetection", "redaction", "probabilistic_sampler", "filter/drop_debug", "tail_sampling", "batch"}
	if strings.Join(gotTraces, ",") != strings.Join(wantTraces, ",") {
		t.Fatalf("traces processors = %v, want %v", gotTraces, wantTraces)
	}

	processors, _ := cfg["processors"].(map[string]any)
	if _, ok := processors["filter/drop_debug"]; !ok {
		t.Fatalf("extraProcessors body not merged: %+v", processors)
	}
}

func toStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, len(l))
	for i, x := range l {
		out[i], _ = x.(string)
	}
	return out
}

func TestRegionalOperatorReceiverAuthWiresBearerToken(t *testing.T) {
	r := operatorRender(t, "--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=op-receiver-auth")
	cfg := otelConfig(t, r.configmaps["continuum-regional-operator-config"].Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	otlp, _ := receivers["otlp"].(map[string]any)
	protocols, _ := otlp["protocols"].(map[string]any)
	grpc, _ := protocols["grpc"].(map[string]any)
	auth, _ := grpc["auth"].(map[string]any)
	if auth["authenticator"] != "bearertokenauth" {
		t.Fatalf("grpc receiver auth = %+v", grpc)
	}
	extensions, _ := cfg["extensions"].(map[string]any)
	if _, ok := extensions["bearertokenauth"]; !ok {
		t.Fatalf("no bearertokenauth extension: %+v", cfg)
	}

	dep := r.deployments["continuum-regional-operator"]
	found := false
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CONTINUUM_OPERATOR_RECEIVER_AUTH" {
			found = true
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil || e.ValueFrom.SecretKeyRef.Name != "op-receiver-auth" {
				t.Fatalf("receiver auth env not wired to the secret: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("CONTINUUM_OPERATOR_RECEIVER_AUTH env var missing")
	}
}

func TestRegionalOperatorRequiresExportEndpoint(t *testing.T) {
	_, err := operatorHelmTemplate(t, "--set", "export.otlp.endpoint=")
	if err == nil {
		t.Fatal("expected helm template to fail with no export.otlp.endpoint")
	}
}

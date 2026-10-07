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

// operatorHelmTemplateNamed is operatorHelmTemplate with the release name as a parameter, instead of the
// fixed "op" - used to check that two different releases of this chart never collide on object names
// (see operator.name in _helpers.tpl: this chart is designed to run many instances per namespace, unlike
// continuum-agent, which is 1:1 with a cluster).
func operatorHelmTemplateNamed(t *testing.T, release string, extra ...string) (string, error) {
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
	args := append(append([]string{"template", release, tgz}, base...), extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	return string(out), err
}

func operatorObjectNames(t *testing.T, out string) []string {
	t.Helper()
	var names []string
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
		names = append(names, meta.Kind+"/"+meta.Metadata.Name)
	}
	return names
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
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
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
	svc, ok := r.services["op-regional-operator"]
	if !ok {
		t.Fatal("no Service rendered")
	}
	var ports []string
	for _, p := range svc.Spec.Ports {
		ports = append(ports, p.Name)
	}
	// grpc and http, and the collector's own metrics (on by default: see TestRegionalOperatorSelfMetricsAreOnByDefaultOnThePodAddress).
	if strings.Join(ports, ",") != "otlp-grpc,otlp-http,metrics" {
		t.Fatalf("expected the grpc, http and metrics ports, got %+v", svc.Spec.Ports)
	}
}

func TestRegionalOperatorProcessorOrderMatchesSpec(t *testing.T) {
	extra := `{"filter/drop_debug":{"error_mode":"ignore"}}`
	r := operatorRender(t, "--set", "processors.resourceDetection.enabled=true", "--set-json", "processors.extraProcessors="+extra,
		"--set", "processors.extraProcessorNames[0]=filter/drop_debug", "--set", "processors.tracesSampling.percentage=50",
		"--set", "processors.extraTracesProcessorNames[0]=tail_sampling")
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
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
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
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

	dep := r.deployments["op-regional-operator"]
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

func TestRegionalOperatorReceiverTLSWiresCertAndMTLS(t *testing.T) {
	r := operatorRender(t, "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=op-receiver-tls")
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	otlp, _ := receivers["otlp"].(map[string]any)
	protocols, _ := otlp["protocols"].(map[string]any)
	for _, proto := range []string{"grpc", "http"} {
		p, _ := protocols[proto].(map[string]any)
		tls, _ := p["tls"].(map[string]any)
		if tls["cert_file"] != "/receiver-tls/tls.crt" || tls["key_file"] != "/receiver-tls/tls.key" {
			t.Fatalf("%s tls = %+v", proto, tls)
		}
		// mtls defaults to true (see values.yaml), so client_ca_file should be set without an explicit --set.
		if tls["client_ca_file"] != "/receiver-tls/ca.crt" {
			t.Fatalf("%s tls.client_ca_file = %+v, want /receiver-tls/ca.crt (mtls defaults to true)", proto, tls)
		}
	}
	dep := r.deployments["op-regional-operator"]
	foundMount, foundVol := false, false
	for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "receiver-tls" && m.MountPath == "/receiver-tls" && m.ReadOnly {
			foundMount = true
		}
	}
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == "receiver-tls" && v.Secret != nil && v.Secret.SecretName == "op-receiver-tls" {
			foundVol = true
		}
	}
	if !foundMount || !foundVol {
		t.Fatalf("receiver-tls volume/mount missing: mounts=%+v volumes=%+v", dep.Spec.Template.Spec.Containers[0].VolumeMounts, dep.Spec.Template.Spec.Volumes)
	}
}

func TestRegionalOperatorReceiverTLSWithoutMTLSOmitsClientCAFile(t *testing.T) {
	r := operatorRender(t, "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=op-receiver-tls", "--set", "receiver.tls.mtls=false")
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	receivers, _ := cfg["receivers"].(map[string]any)
	otlp, _ := receivers["otlp"].(map[string]any)
	protocols, _ := otlp["protocols"].(map[string]any)
	grpc, _ := protocols["grpc"].(map[string]any)
	tls, _ := grpc["tls"].(map[string]any)
	if _, ok := tls["client_ca_file"]; ok {
		t.Fatalf("client_ca_file should be absent when mtls is off: %+v", tls)
	}
	if tls["cert_file"] != "/receiver-tls/tls.crt" {
		t.Fatalf("server cert should still be set: %+v", tls)
	}
}

func TestRegionalOperatorReceiverTLSRequiresSecretName(t *testing.T) {
	_, err := operatorHelmTemplate(t, "--set", "receiver.tls.enabled=true")
	if err == nil {
		t.Fatal("expected helm template to fail with receiver.tls.enabled and no secretName")
	}
}

// TestRegionalOperatorObjectNamesDontCollideAcrossReleases guards the regression this chart actually hit:
// operator.name used to be a hardcoded literal ("continuum-regional-operator"), so every ServiceAccount,
// ConfigMap, Service and Deployment from two operators installed into the same namespace collided byte
// for byte - the second `helm install` would silently adopt (and then fight over) the first operator's
// objects. Unlike continuum-agent (1:1 with a cluster, so a fixed name is safe), this chart is designed
// to run many instances per namespace, so object names must be derived from .Release.Name.
func TestRegionalOperatorObjectNamesDontCollideAcrossReleases(t *testing.T) {
	outA, err := operatorHelmTemplateNamed(t, "op-aaa111")
	if err != nil {
		t.Fatalf("helm template op-aaa111: %v\n%s", err, outA)
	}
	outB, err := operatorHelmTemplateNamed(t, "op-bbb222")
	if err != nil {
		t.Fatalf("helm template op-bbb222: %v\n%s", err, outB)
	}
	namesA := operatorObjectNames(t, outA)
	namesB := operatorObjectNames(t, outB)
	if len(namesA) == 0 || len(namesB) == 0 {
		t.Fatalf("expected rendered objects, got A=%v B=%v", namesA, namesB)
	}
	if len(namesA) != len(namesB) {
		t.Fatalf("the two releases rendered a different number of objects: A=%v B=%v", namesA, namesB)
	}
	seenA := map[string]bool{}
	for _, n := range namesA {
		seenA[n] = true
	}
	for _, n := range namesB {
		if seenA[n] {
			t.Fatalf("object %q collides between release op-aaa111 and op-bbb222:\nA=%v\nB=%v", n, namesA, namesB)
		}
	}
	// And a release name that already contains "regional-operator" must not be doubled up.
	outC, err := operatorHelmTemplateNamed(t, "my-regional-operator")
	if err != nil {
		t.Fatalf("helm template my-regional-operator: %v\n%s", err, outC)
	}
	if !strings.Contains(outC, "name: my-regional-operator\n") {
		t.Fatalf("expected the dedup convention (release name already contains the suffix) to apply, got:\n%s", outC)
	}
	if strings.Contains(outC, "my-regional-operator-regional-operator") {
		t.Fatalf("release name suffix was doubled up:\n%s", outC)
	}
}

// TestRegionalOperatorHealthCheckWiredToLivenessReadiness confirms the health_check extension the
// otel/opentelemetry-collector-contrib image ships for free is actually turned on - both in the generated
// OTel Collector config (the top-level "extensions" map AND the "service.extensions" activation list,
// which are two different things in this config format - a pipeline itself has no "extensions" field of
// its own) and in the Deployment's livenessProbe/readinessProbe, on the same configurable port.
func TestRegionalOperatorHealthCheckWiredToLivenessReadiness(t *testing.T) {
	r := operatorRender(t, "--set", "health.port=13199")
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)

	extensions, _ := cfg["extensions"].(map[string]any)
	hc, ok := extensions["health_check"].(map[string]any)
	if !ok {
		t.Fatalf("no health_check extension defined: %+v", cfg)
	}
	if hc["endpoint"] != "0.0.0.0:13199" {
		t.Fatalf("health_check endpoint = %+v, want 0.0.0.0:13199", hc["endpoint"])
	}

	svc, _ := cfg["service"].(map[string]any)
	active := toStrings(svc["extensions"])
	found := false
	for _, e := range active {
		if e == "health_check" {
			found = true
		}
	}
	if !found {
		t.Fatalf("health_check missing from service.extensions (the activation list): %v", active)
	}

	dep := r.deployments["op-regional-operator"]
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected one container, got %d", len(containers))
	}
	var healthPort int32
	portFound := false
	for _, p := range containers[0].Ports {
		if p.Name == "health" {
			healthPort = p.ContainerPort
			portFound = true
		}
	}
	if !portFound || healthPort != 13199 {
		t.Fatalf("health containerPort not wired to health.port=13199: %+v", containers[0].Ports)
	}
	lp := containers[0].LivenessProbe
	if lp == nil || lp.HTTPGet == nil || lp.HTTPGet.Path != "/" || lp.HTTPGet.Port.StrVal != "health" {
		t.Fatalf("livenessProbe not wired to the health port: %+v", lp)
	}
	rp := containers[0].ReadinessProbe
	if rp == nil || rp.HTTPGet == nil || rp.HTTPGet.Path != "/" || rp.HTTPGet.Port.StrVal != "health" {
		t.Fatalf("readinessProbe not wired to the health port: %+v", rp)
	}
}

// TestRegionalOperatorSelfMetricsOptIn confirms the self-metrics reader is on by default (it is the only way to see a
// destination that cannot be reached), goes away with selfMetrics.enabled=false, lands in service.telemetry (not a
// pipeline), and gets its own container/Service port, wherever selfMetrics.port puts it.
func TestRegionalOperatorSelfMetricsOptIn(t *testing.T) {
	off := operatorRender(t, "--set", "selfMetrics.enabled=false")
	cfgOff := otelConfig(t, off.configmaps["op-regional-operator-config"].Data)
	if svc, _ := cfgOff["service"].(map[string]any); svc["telemetry"] != nil {
		t.Fatalf("selfMetrics.enabled=false, but service.telemetry was rendered: %+v", svc["telemetry"])
	}
	for _, p := range off.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0].Ports {
		if p.Name == "metrics" {
			t.Fatalf("a metrics port with selfMetrics off: %+v", p)
		}
	}

	on := operatorRender(t, "--set", "selfMetrics.port=9999")
	cfgOn := otelConfig(t, on.configmaps["op-regional-operator-config"].Data)
	svcOn, _ := cfgOn["service"].(map[string]any)
	telemetry, _ := svcOn["telemetry"].(map[string]any)
	metrics, _ := telemetry["metrics"].(map[string]any)
	readers, _ := metrics["readers"].([]any)
	if len(readers) != 1 {
		t.Fatalf("expected one self-metrics reader, got %+v", metrics)
	}
	dep := on.deployments["op-regional-operator"]
	found := false
	for _, p := range dep.Spec.Template.Spec.Containers[0].Ports {
		if p.Name == "metrics" && p.ContainerPort == 9999 {
			found = true
		}
	}
	if !found {
		t.Fatalf("metrics containerPort not wired to selfMetrics.port=9999: %+v", dep.Spec.Template.Spec.Containers[0].Ports)
	}
	svcPort := on.services["op-regional-operator"]
	foundSvc := false
	for _, p := range svcPort.Spec.Ports {
		if p.Name == "metrics" && p.Port == 9999 {
			foundSvc = true
		}
	}
	if !foundSvc {
		t.Fatalf("Service has no metrics port: %+v", svcPort.Spec.Ports)
	}
}

// The ConfigMap is named in two places, the ConfigMap itself and the Deployment volume that mounts it; for a long
// release name the 63-character cut used to apply to the first alone, so the Deployment asked for a ConfigMap that did
// not exist and its pod never started.
func TestRegionalOperatorLongNameMountsTheConfigMapItRenders(t *testing.T) {
	for _, release := range []string{
		"eu-west-production-cluster-number-0001-x",     // 40 characters: the name is 58, the ConfigMap's 65
		"a-release-name-that-is-as-long-as-helm-allow", // 44: the ConfigMap name is cut to the operator's own
	} {
		out, err := operatorHelmTemplateNamed(t, release)
		if err != nil {
			t.Fatalf("helm template %s: %v\n%s", release, err, out)
		}
		var cm string
		var mounted []string
		dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			var o struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if json.Unmarshal(raw, &o) != nil {
				continue
			}
			switch o.Kind {
			case "ConfigMap":
				cm = o.Metadata.Name
			case "Deployment":
				var d appsv1.Deployment
				if err := json.Unmarshal(raw, &d); err != nil {
					t.Fatal(err)
				}
				for _, v := range d.Spec.Template.Spec.Volumes {
					if v.Name == "config" && v.ConfigMap != nil {
						mounted = append(mounted, v.ConfigMap.Name)
					}
				}
			}
		}
		if len(cm) == 0 || len(cm) > 63 {
			t.Errorf("release %q: ConfigMap name %q is empty or longer than 63 characters", release, cm)
		}
		if len(mounted) != 1 || mounted[0] != cm {
			t.Errorf("release %q: the Deployment mounts ConfigMap %v, but the chart renders %q", release, mounted, cm)
		}
	}
}

// A refused TLS handshake (no client certificate, another operator's CA, an expired certificate) is logged by the receiver
// only at debug level, so the log level is a value: nothing is rendered at the default, and the level reaches the collector
// whether or not its self-metrics are on.
func TestRegionalOperatorLogLevelIsOptIn(t *testing.T) {
	logs := func(args ...string) any {
		t.Helper()
		cfg := otelConfig(t, operatorRender(t, args...).configmaps["op-regional-operator-config"].Data)
		svc, _ := cfg["service"].(map[string]any)
		tel, _ := svc["telemetry"].(map[string]any)
		return tel["logs"]
	}
	if got := logs(); got != nil {
		t.Errorf("the default renders a log setting: %v", got)
	}
	for _, args := range [][]string{{"--set", "selfMetrics.logLevel=debug"}, {"--set", "selfMetrics.logLevel=debug", "--set", "selfMetrics.enabled=false"}} {
		l, _ := logs(args...).(map[string]any)
		if l["level"] != "debug" {
			t.Errorf("%v: service.telemetry.logs = %v", args, l)
		}
	}
	if _, err := operatorHelmTemplate(t, "--set", "selfMetrics.logLevel=verbose"); err == nil {
		t.Error("an unknown log level was accepted")
	}
}

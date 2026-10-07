package chart

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The operator heartbeat is OPT-IN: this chart's other promise - it never dials the Ikhnos server - holds
// for every release that does not turn heartbeat.enabled on, and these tests pin that, then pin exactly what
// turning it on adds.

const hbBase = "--set"

// hbOn is the minimum that enables the heartbeat.
var hbOn = []string{
	"--set", "heartbeat.enabled=true",
	"--set", "heartbeat.url=https://continuum.example.com/api/v1/operator-heartbeat",
	"--set", "heartbeat.auth.secretName=op-1-heartbeat-auth",
}

func TestRegionalOperatorDefaultRenderIsByteIdenticalToBeforeTheHeartbeat(t *testing.T) {
	want, err := os.ReadFile("testdata/regional_operator_default.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// testdata/regional_operator_default.golden.yaml is `helm template op <chart> --set
	// export.otlp.endpoint=collector.example:4317`. It was first rendered from the chart as it was BEFORE the
	// heartbeat existed, and is regenerated (the same command) whenever the default pipeline itself changes on
	// purpose - last for the explicit batch sizes and the exporter queue and retry settings. With the defaults,
	// adding or setting the heartbeat must not change a single byte of it.
	got, err := operatorHelmTemplate(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if got != string(want) {
		t.Fatalf("the default render changed: the heartbeat must be invisible unless enabled.\n%s", firstDiff(string(want), got))
	}
	// Setting the heartbeat's other values while it stays off changes nothing either.
	got, err = operatorHelmTemplate(t, "--set", "heartbeat.enabled=false",
		"--set", "heartbeat.url=https://continuum.example.com/api/v1/operator-heartbeat",
		"--set", "heartbeat.auth.secretName=whatever", "--set", "heartbeat.tls.caSecretName=my-ca", "--set", "heartbeat.intervalSeconds=15")
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if got != string(want) {
		t.Fatalf("heartbeat values leaked into a render with heartbeat.enabled=false.\n%s", firstDiff(string(want), got))
	}
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n  want: " + a + "\n  got:  " + b
		}
	}
	return "no line differs (trailing whitespace?)"
}

func TestRegionalOperatorHeartbeatAddsOneSelfContainedPipeline(t *testing.T) {
	off := operatorRender(t, "--set", "health.port=13199", "--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=op-receiver-auth")
	on := operatorRender(t, append([]string{"--set", "health.port=13199", "--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=op-receiver-auth", "--set", "heartbeat.intervalSeconds=30"}, hbOn...)...)
	cfgOff := otelConfig(t, off.configmaps["op-regional-operator-config"].Data)
	cfgOn := otelConfig(t, on.configmaps["op-regional-operator-config"].Data)

	sub := func(cfg map[string]any, path ...string) map[string]any {
		m := cfg
		for _, p := range path {
			m, _ = m[p].(map[string]any)
		}
		return m
	}

	// The probe: the collector's own health_check endpoint, on the configured health port, on loopback.
	hc := sub(cfgOn, "receivers", "httpcheck/heartbeat")
	if hc["collection_interval"] != "30s" {
		t.Fatalf("collection_interval = %v, want 30s", hc["collection_interval"])
	}
	targets, _ := hc["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("httpcheck targets = %v", hc["targets"])
	}
	target, _ := targets[0].(map[string]any)
	if target["endpoint"] != "http://127.0.0.1:13199/" || target["method"] != "GET" {
		t.Fatalf("httpcheck target = %v, want the collector's own health_check at 127.0.0.1:13199", target)
	}
	if hcExt := sub(cfgOn, "extensions", "health_check"); hcExt["endpoint"] != "0.0.0.0:13199" {
		t.Fatalf("health_check extension = %v", hcExt)
	}

	// The exporter: its own, to the heartbeat URL, with the bearer header from the env var.
	exp := sub(cfgOn, "exporters", "otlphttp/heartbeat")
	if exp["metrics_endpoint"] != "https://continuum.example.com/api/v1/operator-heartbeat" {
		t.Fatalf("metrics_endpoint = %v", exp["metrics_endpoint"])
	}
	if h, _ := exp["headers"].(map[string]any); h["Authorization"] != "Bearer ${env:CONTINUUM_OPERATOR_HEARTBEAT_AUTH}" || len(h) != 1 {
		t.Fatalf("heartbeat headers = %v", exp["headers"])
	}
	if _, has := exp["tls"]; has {
		t.Fatalf("no tls block expected without heartbeat.tls.caSecretName (system roots verify the server): %v", exp["tls"])
	}
	for _, k := range []string{"insecure", "insecure_skip_verify"} {
		if strings.Contains(on.configmaps["op-regional-operator-config"].Data["otel-collector-config.yaml"], k+": true") {
			t.Fatalf("%s: true rendered", k)
		}
	}

	// The pipeline is fed by nothing but the probe, and drains to nothing but the heartbeat exporter.
	pipes := sub(cfgOn, "service", "pipelines")
	hb := pipes["metrics/heartbeat"].(map[string]any)
	if got := toStrings(hb["receivers"]); !reflect.DeepEqual(got, []string{"httpcheck/heartbeat"}) {
		t.Fatalf("heartbeat pipeline receivers = %v - it must carry only the health probe", got)
	}
	if got := toStrings(hb["exporters"]); !reflect.DeepEqual(got, []string{"otlphttp/heartbeat"}) {
		t.Fatalf("heartbeat pipeline exporters = %v", got)
	}
	if got := toStrings(hb["processors"]); !reflect.DeepEqual(got, []string{"filter/heartbeat"}) {
		t.Fatalf("heartbeat pipeline processors = %v", got)
	}
	filter := sub(cfgOn, "processors", "filter/heartbeat")
	if !strings.Contains(toJSON(t, filter), `name != \"httpcheck.status\"`) {
		t.Fatalf("filter/heartbeat does not keep only httpcheck.status: %v", filter)
	}
	// ... and nothing else uses the heartbeat's receiver, processor or exporter.
	for name, p := range pipes {
		if name == "metrics/heartbeat" {
			continue
		}
		pm := p.(map[string]any)
		for _, bad := range []string{"httpcheck/heartbeat", "otlphttp/heartbeat", "filter/heartbeat"} {
			for _, k := range []string{"receivers", "processors", "exporters"} {
				for _, got := range toStrings(pm[k]) {
					if got == bad {
						t.Fatalf("pipeline %s shares %s with the heartbeat", name, bad)
					}
				}
			}
		}
	}

	// Everything that existed is untouched: same pipelines, same exporter, same receivers/processors,
	// same active extensions.
	for _, name := range []string{"metrics", "logs", "traces"} {
		if !reflect.DeepEqual(sub(cfgOff, "service", "pipelines")[name], pipes[name]) {
			t.Fatalf("enabling the heartbeat changed the %s pipeline: %v -> %v", name, sub(cfgOff, "service", "pipelines")[name], pipes[name])
		}
	}
	if !reflect.DeepEqual(sub(cfgOff, "exporters", "otlp"), sub(cfgOn, "exporters", "otlp")) {
		t.Fatal("enabling the heartbeat changed the relay exporter")
	}
	if !reflect.DeepEqual(sub(cfgOff, "receivers", "otlp"), sub(cfgOn, "receivers", "otlp")) {
		t.Fatal("enabling the heartbeat changed the OTLP receiver")
	}
	if !reflect.DeepEqual(sub(cfgOff, "extensions"), sub(cfgOn, "extensions")) {
		t.Fatal("enabling the heartbeat changed the extensions")
	}
	if !reflect.DeepEqual(cfgOff["service"].(map[string]any)["extensions"], cfgOn["service"].(map[string]any)["extensions"]) {
		t.Fatal("enabling the heartbeat changed the active extension list")
	}
	if len(pipes) != len(sub(cfgOff, "service", "pipelines"))+1 {
		t.Fatalf("expected exactly one more pipeline, got %v", pipes)
	}

	// The credential comes from its own Secret via its own env var - not the receiver's.
	dep := on.deployments["op-regional-operator"]
	env := map[string]corev1.EnvVar{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e
	}
	hbEnv, ok := env["CONTINUUM_OPERATOR_HEARTBEAT_AUTH"]
	if !ok || hbEnv.ValueFrom == nil || hbEnv.ValueFrom.SecretKeyRef == nil || hbEnv.ValueFrom.SecretKeyRef.Name != "op-1-heartbeat-auth" || hbEnv.ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("heartbeat env = %+v", hbEnv)
	}
	if rcv := env["CONTINUUM_OPERATOR_RECEIVER_AUTH"]; rcv.ValueFrom == nil || rcv.ValueFrom.SecretKeyRef.Name != "op-receiver-auth" {
		t.Fatalf("receiver env disturbed: %+v", rcv)
	}
	if strings.Contains(on.configmaps["op-regional-operator-config"].Data["otel-collector-config.yaml"], "cnh_") {
		t.Fatal("a secret value is in the ConfigMap")
	}
	// No CA volume without a CA Secret.
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == "heartbeat-ca" {
			t.Fatalf("heartbeat-ca volume without heartbeat.tls.caSecretName")
		}
	}
	// And no new ports or Services: the heartbeat only dials out.
	if len(on.services["op-regional-operator"].Spec.Ports) != len(off.services["op-regional-operator"].Spec.Ports) || len(dep.Spec.Template.Spec.Containers[0].Ports) != len(off.deployments["op-regional-operator"].Spec.Template.Spec.Containers[0].Ports) {
		t.Fatal("the heartbeat added a listening port")
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRegionalOperatorHeartbeatVerifiesTheServerWithAnOptionalPrivateCA(t *testing.T) {
	r := operatorRender(t, append([]string{"--set", "heartbeat.tls.caSecretName=ca-bundle", "--set", "heartbeat.tls.caSecretKey=root.pem"}, hbOn...)...)
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	exp := cfg["exporters"].(map[string]any)["otlphttp/heartbeat"].(map[string]any)
	tls, _ := exp["tls"].(map[string]any)
	if tls["ca_file"] != "/heartbeat-ca/root.pem" || len(tls) != 1 {
		t.Fatalf("heartbeat tls = %v, want only ca_file=/heartbeat-ca/root.pem", exp["tls"])
	}
	c := r.deployments["op-regional-operator"].Spec.Template.Spec
	var vol, mount bool
	for _, v := range c.Volumes {
		if v.Name == "heartbeat-ca" && v.Secret != nil && v.Secret.SecretName == "ca-bundle" {
			vol = true
		}
	}
	for _, m := range c.Containers[0].VolumeMounts {
		if m.Name == "heartbeat-ca" && m.MountPath == "/heartbeat-ca" && m.ReadOnly {
			mount = true
		}
	}
	if !vol || !mount {
		t.Fatalf("CA Secret not mounted read-only: volume=%v mount=%v", vol, mount)
	}
}

func TestRegionalOperatorHeartbeatValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring of the failure; "" = must render
	}{
		{"url required", []string{"--set", "heartbeat.enabled=true", "--set", "heartbeat.auth.secretName=s"}, "heartbeat.url"},
		{"secret required", []string{"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=https://c.example/h"}, "heartbeat.auth.secretName"},
		{"plain http refused", []string{"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=http://c.example/h", "--set", "heartbeat.auth.secretName=s"}, "must be https://"},
		{"plain http allowed explicitly", []string{"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=http://c.example/h", "--set", "heartbeat.auth.secretName=s", "--set", "heartbeat.allowPlainHTTP=true"}, ""},
		{"CA over plain http is meaningless", []string{"--set", "heartbeat.enabled=true", "--set", "heartbeat.url=http://c.example/h", "--set", "heartbeat.auth.secretName=s", "--set", "heartbeat.allowPlainHTTP=true", "--set", "heartbeat.tls.caSecretName=ca"}, "only applies to an https"},
		{"https with defaults", hbOn, ""},
	}
	for _, c := range cases {
		out, err := operatorHelmTemplate(t, c.args...)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v\n%s", c.name, err, out)
			}
			continue
		}
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: want a failure mentioning %q, got err=%v\n%s", c.name, c.want, err, out)
		}
	}
}

func TestRegionalOperatorHeartbeatSchemaRejectsMalformedValues(t *testing.T) {
	for name, set := range map[string][]string{
		"interval below 10": {"heartbeat.intervalSeconds=5"},
		"interval above 60 (the server's offline threshold is 3x60s)": {"heartbeat.intervalSeconds=300"},
		"interval not a number":       {"heartbeat.intervalSeconds=soon"},
		"enabled not a boolean":       {"heartbeat.enabled=yes"},
		"url not a url":               {"heartbeat.url=continuum.example.com"},
		"url with a scheme we refuse": {"heartbeat.url=ftp://continuum.example.com/x"},
		"unknown key":                 {"heartbeat.verifyTLS=false"},
		"unknown nested key":          {"heartbeat.tls.insecureSkipVerify=true"},
		"empty secret key":            {"heartbeat.auth.secretKey="},
	} {
		args := []string{}
		for _, s := range set {
			args = append(args, "--set", s)
		}
		out, err := operatorHelmTemplate(t, append(args, "--set", "heartbeat.enabled=true", "--set", "heartbeat.url=https://c.example/h", "--set", "heartbeat.auth.secretName=s")...)
		if name == "enabled not a boolean" || name == "url not a url" || name == "url with a scheme we refuse" {
			// the later --set of the same key wins in Helm, so re-run without the defaults that would mask it
			out, err = operatorHelmTemplate(t, append([]string{"--set", "heartbeat.auth.secretName=s"}, args...)...)
		}
		if err == nil {
			t.Errorf("%s: rendered, want a schema failure\n%s", name, out)
		} else if !strings.Contains(out, "values don't meet the specifications of the schema") {
			t.Errorf("%s: failed, but not at the schema: %s", name, out)
		}
	}
}

func TestRegionalOperatorChartYamlSaysWhatTheHeartbeatIsAndIsNot(t *testing.T) {
	b, err := operatorFiles.ReadFile("continuum-regional-operator/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"By default it never dials the Ikhnos server", "opt-in heartbeat.enabled", "no telemetry", "separate heartbeat secret"} {
		if !strings.Contains(s, want) {
			t.Errorf("Chart.yaml description lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Never dials the Ikhnos server - its only credential") {
		t.Error("Chart.yaml still makes the unconditional 'never dials' claim")
	}
}

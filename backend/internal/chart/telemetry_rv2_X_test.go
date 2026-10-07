package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The export and connectivity path (review round 2, agent X): a cluster behind a corporate proxy, an endpoint written in
// a shape the collector cannot use, a certificate setting that would never be used, and the receiving side's limits. The
// failure each test pins is one the person would otherwise meet as a Ready pod that delivers nothing.

func envNamed(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func envIndex(c corev1.Container, name string) int {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return i
		}
	}
	return -1
}

// chartNotes renders a chart's NOTES.txt through a ConfigMap wrapper (helm template does not print NOTES), exactly as
// TestRegionalOperatorNotesSayHowToSeeDeliveryAndWarnAboutTheWrongPort does.
func chartNotes(t *testing.T, chartDir, release string, args ...string) string {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), chartDir)
	copyTree(t, chartDir, dir)
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
	out, err := exec.Command(h, append([]string{"template", release, dir, "--show-only", "templates/notes.yaml"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template of the notes %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func agentNotes(t *testing.T, extra ...string) string {
	return chartNotes(t, "continuum-agent", "ct", append(append([]string{}, baseSet...), extra...)...)
}

func operatorNotes(t *testing.T, extra ...string) string {
	return chartNotes(t, "continuum-regional-operator", "op", append([]string{"--set", "export.otlp.endpoint=collector.example:4317"}, extra...)...)
}

// Behind a proxy the collectors read HTTPS_PROXY / HTTP_PROXY / NO_PROXY from their environment (OTLP/HTTP and OTLP/gRPC
// both do; the Prometheus receiver's scrapes do not). Without a NO_PROXY of the chart's own, the proxy would also be asked
// for the Kubernetes API (client-go honours it), the kubelet at the node's IP and every in-cluster Service, and refuse them:
// a Ready pod with no k8s attributes, no kubelet metrics and a failing scrape.
func TestAgentRv2XProxyEnvOnBothCollectors(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.export.proxy.httpsProxy=http://proxy.corp:3128", "--set", "telemetry.export.proxy.httpProxy=http://proxy.corp:3128",
		"--set", "telemetry.export.proxy.noProxy=.corp.example",
		"--set", "telemetry.extraEnv[0].name=SSL_CERT_FILE", "--set", "telemetry.extraEnv[0].value=/etc/ssl/corp.pem")...)
	host := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]
	cluster := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0]
	for name, c := range map[string]corev1.Container{"host": host, "cluster": cluster} {
		if v := envNamed(c, "HTTPS_PROXY"); v == nil || v.Value != "http://proxy.corp:3128" {
			t.Errorf("%s HTTPS_PROXY = %v", name, v)
		}
		if v := envNamed(c, "HTTP_PROXY"); v == nil || v.Value != "http://proxy.corp:3128" {
			t.Errorf("%s HTTP_PROXY = %v", name, v)
		}
		no := envNamed(c, "NO_PROXY")
		if no == nil {
			t.Fatalf("%s has no NO_PROXY", name)
		}
		for _, want := range []string{"localhost", "127.0.0.1", "::1", ".svc", ".cluster.local", "$(KUBERNETES_SERVICE_HOST)", "$(POD_IP)", ".corp.example"} {
			if !strings.Contains(","+no.Value+",", ","+want+",") {
				t.Errorf("%s NO_PROXY %q lacks %q", name, no.Value, want)
			}
		}
		// extraEnv comes after everything the chart sets, so one named like a chart variable replaces it.
		if envIndex(c, "SSL_CERT_FILE") < envIndex(c, "NO_PROXY") || envIndex(c, "NO_PROXY") < envIndex(c, "GOMEMLIMIT") {
			t.Errorf("%s env order is not GOMEMLIMIT, proxy, extraEnv: %v", name, c.Env)
		}
	}
	// The kubelet is reached at the node's address (status.hostIP): direct, which only the host collector needs.
	if no := envNamed(host, "NO_PROXY"); !strings.Contains(no.Value, "$(NODE_IP)") {
		t.Errorf("host NO_PROXY %q lacks the node address the kubelet is reached at", no.Value)
	}
	if no := envNamed(cluster, "NO_PROXY"); strings.Contains(no.Value, "NODE_IP") {
		t.Errorf("cluster NO_PROXY %q names NODE_IP, which that pod does not define (the text would reach NO_PROXY unexpanded)", no.Value)
	}
	// NODE_IP and POD_IP are defined before NO_PROXY: $(VAR) only expands variables defined earlier in the list.
	for _, v := range []string{"NODE_IP", "POD_IP"} {
		if envIndex(host, v) < 0 || envIndex(host, v) > envIndex(host, "NO_PROXY") {
			t.Errorf("host: %s must be defined before NO_PROXY uses it", v)
		}
	}
	// A host collector that reads no kubelet does not name NODE_IP either.
	logsOnly := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.export.proxy.httpsProxy=http://proxy.corp:3128")...)
	if no := envNamed(logsOnly.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NO_PROXY"); no == nil || strings.Contains(no.Value, "NODE_IP") {
		t.Errorf("a host collector without kubelet metrics has NO_PROXY %v", no)
	}
}

func TestAgentRv2XNoProxyConfiguredMeansNoProxyVariables(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	for name, c := range map[string]corev1.Container{
		"host":    r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0],
		"cluster": r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0],
	} {
		for _, v := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
			if envNamed(c, v) != nil {
				t.Errorf("%s has %s without a proxy being configured", name, v)
			}
		}
	}
}

// A proxy URL with user:password@ would sit in clear text in the pod spec and in `helm get values`; the Secret form keeps it
// out. HTTPS_PROXY is required (a missing key is CreateContainerConfigError, visible); HTTP_PROXY is optional.
func TestAgentRv2XProxyFromASecret(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.proxy.secretName=corp-proxy")...)
	c := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0]
	https := envNamed(c, "HTTPS_PROXY")
	if https == nil || https.ValueFrom == nil || https.ValueFrom.SecretKeyRef == nil || https.ValueFrom.SecretKeyRef.Name != "corp-proxy" ||
		https.ValueFrom.SecretKeyRef.Key != "HTTPS_PROXY" || (https.ValueFrom.SecretKeyRef.Optional != nil && *https.ValueFrom.SecretKeyRef.Optional) {
		t.Errorf("HTTPS_PROXY = %+v, want a required key of Secret corp-proxy", https)
	}
	plain := envNamed(c, "HTTP_PROXY")
	if plain == nil || plain.ValueFrom == nil || plain.ValueFrom.SecretKeyRef == nil || plain.ValueFrom.SecretKeyRef.Optional == nil || !*plain.ValueFrom.SecretKeyRef.Optional {
		t.Errorf("HTTP_PROXY = %+v, want an optional key", plain)
	}
	// A plain value next to the Secret wins for its own variable.
	r = render(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.export.proxy.secretName=corp-proxy", "--set", "telemetry.export.proxy.httpsProxy=http://plain:3128")...)
	if v := envNamed(r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0], "HTTPS_PROXY"); v == nil || v.Value != "http://plain:3128" {
		t.Errorf("HTTPS_PROXY next to a Secret = %+v", v)
	}
	if n := agentNotes(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.proxy.secretName=corp-proxy")...); !strings.Contains(n, "corp-proxy (HTTPS_PROXY, HTTP_PROXY optional)") {
		t.Errorf("NOTES do not list the proxy Secret among those needed:\n%s", n)
	}
}

// ndots 5 (the Kubernetes default) makes "otlp.example.com" go through every search domain first; dnsConfig is the way out.
func TestAgentRv2XDNSConfigReachesBothPods(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set-string", "telemetry.dnsConfig.options[0].name=ndots", "--set-string", "telemetry.dnsConfig.options[0].value=2")...)
	for name, spec := range map[string]corev1.PodSpec{
		"host": r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec, "cluster": r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec,
	} {
		if spec.DNSConfig == nil || len(spec.DNSConfig.Options) != 1 || spec.DNSConfig.Options[0].Name != "ndots" || *spec.DNSConfig.Options[0].Value != "2" {
			t.Errorf("%s dnsConfig = %+v", name, spec.DNSConfig)
		}
	}
	r = render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	if r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.DNSConfig != nil {
		t.Error("a dnsConfig without being asked for")
	}
}

func TestAgentRv2XTimeoutAndKeepaliveOnTheExporters(t *testing.T) {
	// Not set: the collector's own defaults, nothing rendered.
	def := sub(t, hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...), "exporters", "otlp")
	if _, ok := def["timeout"]; ok {
		t.Errorf("timeout rendered by default: %v", def)
	}
	if _, ok := def["keepalive"]; ok {
		t.Errorf("keepalive rendered by default: %v", def)
	}
	set := []string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.timeout=30s",
		"--set", "telemetry.export.keepalive.time=30s", "--set", "telemetry.export.routes.logs.endpoint=https://logs.example:4318",
		"--set", "telemetry.export.routes.logs.protocol=http", "--set", "telemetry.systemLogs.logs.enabled=true"}
	ex := sub(t, hostConfig(t, withTel(set...)...), "exporters")
	g := sub(t, ex, "otlp")
	if g["timeout"] != "30s" {
		t.Errorf("grpc timeout = %v", g["timeout"])
	}
	ka := sub(t, g, "keepalive")
	if ka["time"] != "30s" || ka["timeout"] != "10s" || ka["permit_without_stream"] != true {
		t.Errorf("grpc keepalive = %v, want time 30s, timeout 10s, permit_without_stream", ka)
	}
	// The HTTP exporter has a timeout but no gRPC keepalive: the collector would refuse the unknown key.
	h := sub(t, ex, "otlphttp/logs")
	if h["timeout"] != "30s" {
		t.Errorf("http timeout = %v", h["timeout"])
	}
	if _, ok := h["keepalive"]; ok {
		t.Errorf("an http exporter got a grpc keepalive: %v", h)
	}
}

// Appending /v1/<signal> to an address that already ends in it is a 404 on every request (.../v1/traces/v1/traces).
func TestAgentRv2XFullUrlAndSignalSuffixRoutes(t *testing.T) {
	traces := []string{"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.export.routes.traces.protocol=http"}
	// fullUrl: posted as written, for a path that is not <base>/v1/traces (Splunk Observability).
	ex := sub(t, clusterConfig(t, withTel(append(traces, "--set", "telemetry.export.routes.traces.endpoint=https://ingest.us1.signalfx.com/v2/trace/otlp",
		"--set", "telemetry.export.routes.traces.fullUrl=true")...)...), "exporters", "otlphttp/traces")
	if ex["traces_endpoint"] != "https://ingest.us1.signalfx.com/v2/trace/otlp" {
		t.Errorf("fullUrl route = %v", ex)
	}
	if _, ok := ex["endpoint"]; ok {
		t.Errorf("fullUrl route also has a base endpoint: %v", ex)
	}
	// An endpoint that ends in its own signal's path is read as the whole URL without fullUrl.
	ex = sub(t, clusterConfig(t, withTel(append(traces, "--set", "telemetry.export.routes.traces.endpoint=https://tempo.example/otlp/v1/traces")...)...), "exporters", "otlphttp/traces")
	if ex["traces_endpoint"] != "https://tempo.example/otlp/v1/traces" {
		t.Errorf("route ending in /v1/traces = %v", ex)
	}
	// A base URL still gets the collector's own suffix.
	ex = sub(t, clusterConfig(t, withTel(append(traces, "--set", "telemetry.export.routes.traces.endpoint=https://tempo.example/otlp")...)...), "exporters", "otlphttp/traces")
	if ex["endpoint"] != "https://tempo.example/otlp" || ex["traces_endpoint"] != nil {
		t.Errorf("route with a base URL = %v", ex)
	}
}

// Every case below is something the collector either refuses to start with or accepts and then cannot deliver through. The
// render says which value to change instead.
func TestAgentRv2XUndeliverableDestinationsFailTheRender(t *testing.T) {
	ep := func(e string, more ...string) []string {
		return append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.endpoint=" + e}, more...)
	}
	http := []string{"--set", "telemetry.export.otlp.protocol=http"}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"grpc with a path", ep("otlp.example.com:443/v1"), "has a path, but a gRPC endpoint is host:port only"},
		{"grpc without a port", ep("otlp.example.com"), "is not host:port"},
		{"grpc https with insecure", ep("https://otlp.example.com:443", "--set", "telemetry.export.otlp.tls.insecure=true"), "says https:// but telemetry.export.otlp.tls.insecure is true"},
		{"grpc http with TLS on", ep("http://otlp.example.com:4317"), "says http:// (plaintext) but telemetry.export.otlp.tls.insecure is false"},
		{"grpc on the http port", ep("otlp.example.com:4318"), "conventional OTLP/HTTP port but telemetry.export.otlp.protocol is grpc"},
		{"http on the grpc port", ep("otlp.example.com:4317", http...), "conventional OTLP/gRPC port but telemetry.export.otlp.protocol is http"},
		{"http with a query", ep("https://otlp.example.com:4318/x?a=b", http...), "has a query or fragment"},
		{"http with the signal suffix on the default", ep("https://otlp.example.com:4318/v1/metrics", http...), "carries several signals"},
		{"http with a scheme the collector cannot use", ep("grpc://otlp.example.com:4318", http...), "an OTLP/HTTP endpoint is http(s)://"},
		{"http with a bad host", ep("https://[fd00::5:4318", http...), "has no valid host"},
		{"whitespace", ep("otlp.example.com:4317 "), "has whitespace"},
		{"client certificate with TLS off (grpc)", ep("otlp.example.com:4317", "--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=m"), "TLS is off"},
		{"client certificate with TLS off (http)", ep("http://otlp.example.com:4318", append(http, "--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=m")...), "TLS is off"},
		{"fullUrl on grpc", ep("otlp.example.com:4317", "--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.export.routes.logs.endpoint=l.example:4317", "--set", "telemetry.export.routes.logs.fullUrl=true"), "fullUrl only applies to protocol=http"},
		{"route for another signal's path", ep("otlp.example.com:4317", "--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.export.routes.logs.endpoint=https://l.example/v1/traces", "--set", "telemetry.export.routes.logs.protocol=http"), "ends in /v1/traces but this route carries logs"},
		{"proxy that is not a URL", ep("otlp.example.com:4317", "--set", "telemetry.export.proxy.httpsProxy=ftp://proxy.corp:3128"), "is not a proxy URL"},
	}
	for _, c := range cases {
		out, err := helmTemplate(t, c.args...)
		if err == nil {
			t.Errorf("%s: rendered, want a failure naming %q", c.name, c.want)
			continue
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: failure does not say %q:\n%s", c.name, c.want, out)
		}
	}
	// And what is fine stays fine: IPv6, dns:///, a scheme that agrees with tls.insecure, a base path, the escape hatch.
	for name, args := range map[string][]string{
		"ipv6":                  ep("[fd00::5]:4317", "--set", "telemetry.export.otlp.tls.insecure=true"),
		"dns scheme":            ep("dns:///otlp.example.com:443"),
		"https scheme":          ep("https://otlp.example.com:443"),
		"http scheme + plain":   ep("http://otlp.example.com:4317", "--set", "telemetry.export.otlp.tls.insecure=true"),
		"http base path":        ep("https://otlp.example.com/otlp", http...),
		"http on 443":           ep("otlp.example.com:443", http...),
		"http ipv6":             ep("https://[fd00::5]:4318", http...),
		"grpc on 4318 allowed":  ep("otlp.example.com:4318", "--set", "telemetry.export.checkPorts=false"),
		"http on 4317 allowed":  ep("otlp.example.com:4317", append(http, "--set", "telemetry.export.checkPorts=false")...),
		"proxy with creds":      ep("otlp.example.com:4317", "--set", "telemetry.export.proxy.httpsProxy=http://u:p@proxy.corp:3128"),
		"socks proxy":           ep("otlp.example.com:4317", "--set", "telemetry.export.proxy.httpsProxy=socks5://proxy.corp:1080"),
		"route with own suffix": ep("otlp.example.com:4317", "--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.export.routes.logs.endpoint=https://l.example/otlp/v1/logs", "--set", "telemetry.export.routes.logs.protocol=http"),
	} {
		if out, err := helmTemplate(t, args...); err != nil {
			t.Errorf("%s was refused:\n%s", name, out)
		}
	}
}

// The install command Ikhnos prints for a source cluster leaves a placeholder default destination behind when every signal
// type has a route of its own; it is never rendered, so it must not be judged (placeholder:4317 with protocol http would be).
func TestAgentRv2XUnusedDefaultDestinationIsNotJudged(t *testing.T) {
	routes := []string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.endpoint=placeholder:4317", "--set", "telemetry.export.otlp.protocol=http",
		"--set", "telemetry.export.routes.metrics.endpoint=m.example:4317", "--set", "telemetry.export.routes.logs.endpoint=l.example:4317", "--set", "telemetry.export.routes.traces.endpoint=t.example:4317"}
	if out, err := helmTemplate(t, routes...); err != nil {
		t.Errorf("an unused default destination failed the render:\n%s", out)
	}
	// ... and the same default, once a signal type does go there, is judged.
	if out, err := helmTemplate(t, append(append([]string{}, routes[:len(routes)-2]...), "--set", "telemetry.traces.traces.enabled=true")...); err == nil || !strings.Contains(out, "conventional OTLP/gRPC port but telemetry.export.otlp.protocol is http") {
		t.Errorf("a used default destination was not judged: %v\n%s", err, out)
	}
}

// NOTES is where the person looks right after installing. It lists where data goes and over what, what Secrets must exist
// first, and warns about settings that are accepted but will not do what was meant.
func TestAgentRv2XNotesSayWhereDataGoesAndWarn(t *testing.T) {
	n := agentNotes(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.tls.insecure=true",
		"--set", "telemetry.export.otlp.auth.secretName=tok")...)
	for _, want := range []string{"Sends otlp -> x:4317 (grpc, PLAINTEXT, header", "from Secret tok", "Troubleshooting export",
		"sends its credential header", "over a connection without TLS"} {
		if !strings.Contains(n, want) {
			t.Errorf("the notes lack %q:\n%s", want, n)
		}
	}
	// A proxy that gRPC will not use, with credentials in the URL, with the egress lockdown on.
	n = agentNotes(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.proxy.httpProxy=http://u:p@proxy.corp:3128",
		"--set", "networkPolicy.telemetryEgress.enabled=true", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8")...)
	for _, want := range []string{"which only ever uses httpsProxy", "carries credentials", "must allow the proxy's address and port"} {
		if !strings.Contains(n, want) {
			t.Errorf("the notes lack the warning %q:\n%s", want, n)
		}
	}
	// A CA with TLS off: allowed (a throwaway test destination) but never what was meant by naming a CA.
	n = agentNotes(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.export.otlp.tls.caFile=/etc/ca.pem")...)
	if !strings.Contains(n, "names a CA (tls.caSecretName / tls.caFile) but TLS is off") {
		t.Errorf("no warning for a CA with TLS off:\n%s", n)
	}
	// Nothing to warn about on a plain, correct setup.
	n = agentNotes(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	if strings.Contains(n, "proxy") || strings.Contains(n, "without TLS") {
		t.Errorf("a warning for a correct setup:\n%s", n)
	}
}

func TestAgentRv2XSchemaRefusesUnknownExportKeys(t *testing.T) {
	for _, key := range []string{"telemetry.export.proxy.no=x", "telemetry.export.keepalive.interval=1s", "telemetry.export.proxy.httpsProxy=has space"} {
		if out, err := helmTemplate(t, withTel("--set", key)...); err == nil {
			t.Errorf("%s was accepted:\n%s", key, out)
		}
	}
}

// ---- the regional operator ----

func TestOperatorRv2XProxyExtraEnvAndDNS(t *testing.T) {
	r := operatorRender(t, "--set", "export.proxy.httpsProxy=http://proxy.corp:3128", "--set", "export.proxy.noProxy=.corp.example",
		"--set", "extraEnv[0].name=SSL_CERT_FILE", "--set", "extraEnv[0].value=/etc/ssl/corp.pem",
		"--set-string", "dnsConfig.options[0].name=ndots", "--set-string", "dnsConfig.options[0].value=2")
	d := r.deployments["op-regional-operator"]
	c := d.Spec.Template.Spec.Containers[0]
	if v := envNamed(c, "HTTPS_PROXY"); v == nil || v.Value != "http://proxy.corp:3128" {
		t.Errorf("HTTPS_PROXY = %v", v)
	}
	if v := envNamed(c, "HTTP_PROXY"); v != nil {
		t.Errorf("HTTP_PROXY set although only httpsProxy was: %v", v)
	}
	no := envNamed(c, "NO_PROXY")
	if no == nil || no.Value != "localhost,127.0.0.1,::1,.svc,.cluster.local,.corp.example" {
		t.Errorf("NO_PROXY = %v", no)
	}
	if envIndex(c, "SSL_CERT_FILE") < envIndex(c, "NO_PROXY") || envIndex(c, "NO_PROXY") < envIndex(c, "GOMEMLIMIT") {
		t.Errorf("env order is not GOMEMLIMIT, proxy, extraEnv: %v", c.Env)
	}
	if dc := d.Spec.Template.Spec.DNSConfig; dc == nil || len(dc.Options) != 1 || dc.Options[0].Name != "ndots" {
		t.Errorf("dnsConfig = %+v", dc)
	}
	// Defaults: none of it.
	c = operatorRender(t).deployments["op-regional-operator"].Spec.Template.Spec.Containers[0]
	for _, v := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if envNamed(c, v) != nil {
			t.Errorf("%s without a proxy being configured", v)
		}
	}
	// A Secret.
	c = operatorRender(t, "--set", "export.proxy.secretName=corp-proxy").deployments["op-regional-operator"].Spec.Template.Spec.Containers[0]
	if v := envNamed(c, "HTTPS_PROXY"); v == nil || v.ValueFrom == nil || v.ValueFrom.SecretKeyRef.Name != "corp-proxy" || v.ValueFrom.SecretKeyRef.Key != "HTTPS_PROXY" {
		t.Errorf("HTTPS_PROXY from a Secret = %+v", v)
	}
	if v := envNamed(c, "HTTP_PROXY"); v == nil || v.ValueFrom == nil || v.ValueFrom.SecretKeyRef.Optional == nil || !*v.ValueFrom.SecretKeyRef.Optional {
		t.Errorf("HTTP_PROXY from a Secret = %+v, want optional", v)
	}
}

func TestOperatorRv2XTimeoutKeepaliveAndFullUrl(t *testing.T) {
	c := operatorCollectorConfig(t, "--set", "export.timeout=30s", "--set", "export.keepalive.time=30s",
		"--set", "export.routes.traces.endpoint=https://ingest.us1.signalfx.com/v2/trace/otlp", "--set", "export.routes.traces.protocol=http", "--set", "export.routes.traces.fullUrl=true",
		"--set", "export.routes.logs.endpoint=https://loki.example/otlp/v1/logs", "--set", "export.routes.logs.protocol=http")
	g := c.exporters["otlp"]
	if g["timeout"] != "30s" {
		t.Errorf("grpc timeout = %v", g["timeout"])
	}
	if ka, _ := g["keepalive"].(map[string]any); ka["time"] != "30s" || ka["timeout"] != "10s" || ka["permit_without_stream"] != true {
		t.Errorf("grpc keepalive = %v", g["keepalive"])
	}
	tr := c.exporters["otlphttp/traces"]
	if tr["traces_endpoint"] != "https://ingest.us1.signalfx.com/v2/trace/otlp" || tr["endpoint"] != nil || tr["timeout"] != "30s" || tr["keepalive"] != nil {
		t.Errorf("fullUrl route = %v", tr)
	}
	if lg := c.exporters["otlphttp/logs"]; lg["logs_endpoint"] != "https://loki.example/otlp/v1/logs" || lg["endpoint"] != nil {
		t.Errorf("route ending in /v1/logs = %v", lg)
	}
	if def := operatorCollectorConfig(t).exporters["otlp"]; def["timeout"] != nil || def["keepalive"] != nil {
		t.Errorf("timeout or keepalive rendered by default: %v", def)
	}
}

// Receiving side: a source cluster that sets telemetry.export.keepalive pings the operator while idle. grpc-go's default
// policy answers any ping more often than every 5 minutes with GOAWAY too_many_pings (measured, collector 0.160.0), so the
// receiver says 30s, also without a call in flight. An mTLS receiver re-reads the clients' CA, or a renewed CA is not
// trusted until the pod restarts.
func TestOperatorRv2XReceiverKeepaliveAndClientCAReload(t *testing.T) {
	r := operatorRender(t, "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=rt", "--set", "receiver.tls.mtls=true")
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	grpc := sub(t, cfg, "receivers", "otlp", "protocols", "grpc")
	ep := sub(t, grpc, "keepalive", "enforcement_policy")
	if ep["min_time"] != "30s" || ep["permit_without_stream"] != true {
		t.Errorf("receiver enforcement_policy = %v", ep)
	}
	for _, p := range []string{"grpc", "http"} {
		if tls := sub(t, cfg, "receivers", "otlp", "protocols", p, "tls"); tls["client_ca_file_reload"] != true {
			t.Errorf("%s receiver tls = %v: the clients' CA is read once", p, tls)
		}
	}
	// Without mTLS there is no client CA to reload.
	cfg = otelConfig(t, operatorRender(t, "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=rt", "--set", "receiver.tls.mtls=false").configmaps["op-regional-operator-config"].Data)
	if _, ok := sub(t, cfg, "receivers", "otlp", "protocols", "grpc", "tls")["client_ca_file_reload"]; ok {
		t.Error("client_ca_file_reload without mTLS")
	}
}

// A protocol on the other protocol's conventional port is refused for good by the destination, with a Ready pod and every
// batch dropped. It used to be a warning in NOTES; it now fails the render, and export.checkPorts=false is for the
// destination that really does serve it.
func TestOperatorRv2WrongPortFailsTheRender(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"grpc on 4318":       {[]string{"--set", "export.otlp.endpoint=collector.example:4318"}, "export.otlp.endpoint \"collector.example:4318\" is the conventional OTLP/HTTP port but export.otlp.protocol is grpc"},
		"http on 4317":       {[]string{"--set", "export.otlp.protocol=http"}, "conventional OTLP/gRPC port but export.otlp.protocol is http"},
		"a route":            {[]string{"--set", "export.routes.logs.endpoint=loki.example:4318", "--set", "export.routes.logs.protocol=grpc"}, "export.routes.logs.endpoint \"loki.example:4318\""},
		"grpc with a path":   {[]string{"--set", "export.otlp.endpoint=collector.example:4317/x"}, "has a path, but a gRPC endpoint is host:port only"},
		"no port":            {[]string{"--set", "export.otlp.endpoint=collector.example"}, "is not host:port"},
		"https and insecure": {[]string{"--set", "export.otlp.endpoint=https://collector.example:443", "--set", "export.otlp.tls.insecure=true"}, "says https:// but export.otlp.tls.insecure is true"},
		"mtls without TLS":   {[]string{"--set", "export.otlp.tls.insecure=true", "--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=m"}, "TLS is off"},
		"fullUrl on default": {[]string{"--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=https://x.example:4318/y", "--set", "export.otlp.fullUrl=true"}, "export.otlp.fullUrl does not apply to the default destination"},
		"bad proxy":          {[]string{"--set", "export.proxy.httpsProxy=ftp://proxy.corp:3128"}, "is not a proxy URL"},
	} {
		out, err := operatorHelmTemplate(t, c.args...)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: err=%v, want a failure saying %q:\n%s", name, err, c.want, out)
		}
	}
	for name, args := range map[string][]string{
		"grpc on 4318 allowed": {"--set", "export.otlp.endpoint=collector.example:4318", "--set", "export.checkPorts=false"},
		"http on 4317 allowed": {"--set", "export.otlp.protocol=http", "--set", "export.checkPorts=false"},
		"http on 4318":         {"--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=collector.example:4318"},
	} {
		if out, err := operatorHelmTemplate(t, args...); err != nil {
			t.Errorf("%s was refused:\n%s", name, out)
		}
	}
}

func TestOperatorRv2XNotesListDestinationsSecretsAndWarnings(t *testing.T) {
	n := operatorNotes(t, "--set", "export.otlp.tls.insecure=true", "--set", "export.otlp.auth.secretName=tok",
		"--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=rt", "--set", "receiver.tls.mtls=true",
		"--set", "export.proxy.httpProxy=http://u:p@proxy.corp:3128", "--set", "networkPolicy.egress.enabled=true",
		"--set", "networkPolicy.egress.allowedEgress[0].cidr=10.0.0.0/8")
	for _, want := range []string{"Sends otlp -> collector.example:4317 (grpc, PLAINTEXT, header", "from Secret tok", "rt (tls.crt, tls.key, ca.crt)",
		"sends its credential header", "which only ever uses httpsProxy", "carries credentials", "must allow the proxy's address and port"} {
		if !strings.Contains(n, want) {
			t.Errorf("the notes lack %q:\n%s", want, n)
		}
	}
	n = operatorNotes(t, "--set", "export.otlp.tls.insecure=true", "--set", "export.otlp.tls.caFile=/etc/ca.pem")
	if !strings.Contains(n, "names a CA (tls.caSecretName / tls.caFile) but TLS is off") {
		t.Errorf("no warning for a CA with TLS off:\n%s", n)
	}
	// checkPorts=false is a statement that the port is right: no warning left behind in NOTES.
	n = operatorNotes(t, "--set", "export.otlp.endpoint=collector.example:4318", "--set", "export.checkPorts=false")
	if strings.Contains(n, "conventional OTLP") {
		t.Errorf("a port warning although export.checkPorts=false:\n%s", n)
	}
}

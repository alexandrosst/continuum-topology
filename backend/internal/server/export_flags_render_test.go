package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"continuum/internal/chart"
	"continuum/internal/store"
)

// The flags the server prints for a source cluster are applied with --reset-then-reuse-values, on top of whatever an
// earlier destination left in the release. Rendered through the real agent chart, the exporter must be exactly the one
// the flags describe: its schema accepts every flag (it rejects unknown keys), and nothing of the earlier destination is
// left over - not its protocol, its insecure setting, its CA, its Secrets, its server name, its credential header.

// flagWords splits what setFlag/shellArg print into words: single quotes group, nothing else is special in them.
func flagWords(t *testing.T, s string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote, have = !inQuote, true
		case c == ' ' && !inQuote:
			if have {
				words = append(words, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		words = append(words, cur.String())
	}
	return words
}

// staleDestination is what an earlier destination of the same release could have left behind, for every field the flags
// state, on the default exporter or on one route (prefix).
func staleDestination(prefix string) []string {
	return []string{
		"--set", prefix + ".endpoint=old.example:4318",
		"--set", prefix + ".protocol=http",
		"--set", prefix + ".tls.insecure=true",
		"--set", prefix + ".tls.caFile=/old/ca.crt",
		"--set", prefix + ".tls.serverName=old.name.example",
		"--set", prefix + ".tls.mtls.enabled=true",
		"--set", prefix + ".tls.mtls.secretName=old-client-cert",
		"--set", prefix + ".auth.secretName=old-token",
	}
}

func agentExporters(t *testing.T, cm string, extra []string) map[string]any {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkg, err := chart.Agent.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), chart.Agent.Filename())
	if err := os.WriteFile(tgz, pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"template", "ct", tgz, "--set", "server.address=a.example:8443", "--set", "server.caPin=ab", "--set", "enrollment.token=t",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.systemLogs.logs.enabled=true"}, extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", extra, err, out)
	}
	for _, doc := range strings.Split(string(out), "\n---") {
		var o struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil || o.Kind != "ConfigMap" || o.Metadata.Name != cm {
			continue
		}
		var cfg struct {
			Exporters map[string]any `json:"exporters"`
		}
		if err := yaml.Unmarshal([]byte(o.Data["otel-collector-config.yaml"]), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Exporters
	}
	t.Fatalf("no ConfigMap %s in the render", cm)
	return nil
}

func mustMap(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not a map: %v", what, v)
	}
	return m
}

func TestExportFlagsTakeEffectInTheRenderedExporter(t *testing.T) {
	regional := store.Operator{ID: "op-abc123", ReceiverAuth: store.ReceiverAuthMTLS}
	advertised := regional
	advertised.Address = "ops.example.com:4317"
	central := store.Operator{ID: CentralOperatorID, ReceiverAuth: store.ReceiverAuthMTLS}

	type want struct {
		endpoint, serverName, dir string
	}
	cases := []struct {
		name string
		op   store.Operator
		ep   string
		want want
	}{
		{"regional operator in the same cluster", regional, operatorInClusterEndpoint(regional), want{"op-abc123-regional-operator.continuum-system.svc:4317", "op-abc123.continuum-system.svc", ""}},
		{"regional operator at an advertised address", advertised, advertised.Address, want{"ops.example.com:4317", "op-abc123.continuum-system.svc", ""}},
		{"central operator in its own cluster", central, "continuum-fusion-central.continuum-system.svc:4317", want{"continuum-fusion-central.continuum-system.svc:4317", "", ""}},
	}
	for _, tc := range cases {
		// The default exporter, and one route (logs).
		for _, route := range []bool{false, true} {
			prefix, exporter, dir, cm := "telemetry.export.otlp", "otlp", "/export-mtls", "continuum-telemetry-host-config"
			if route {
				prefix, exporter, dir = "telemetry.export.routes.logs", "otlp/logs", "/export-mtls-logs"
			}
			name := tc.name
			if route {
				name += " (route)"
			}
			t.Run(name, func(t *testing.T) {
				extra := staleDestination(prefix)
				if route {
					extra = append(extra, "--set", "telemetry.export.otlp.endpoint=elsewhere.example:4317", "--set", "telemetry.export.otlp.tls.insecure=true")
				}
				extra = append(extra, flagWords(t, exportBlockFlags(prefix, tc.op, tc.ep, true))...)
				ex := mustMap(t, agentExporters(t, cm, extra)[exporter], "exporter "+exporter)
				if ex["endpoint"] != tc.want.endpoint {
					t.Errorf("endpoint = %v, want %v", ex["endpoint"], tc.want.endpoint)
				}
				tls := mustMap(t, ex["tls"], "tls")
				for k, v := range map[string]any{"insecure": false, "ca_file": dir + "/ca.crt", "cert_file": dir + "/tls.crt", "key_file": dir + "/tls.key", "reload_interval": "5m"} {
					if tls[k] != v {
						t.Errorf("tls.%s = %v, want %v", k, tls[k], v)
					}
				}
				if got, _ := tls["server_name_override"].(string); got != tc.want.serverName {
					t.Errorf("tls.server_name_override = %q, want %q", got, tc.want.serverName)
				}
				if _, ok := ex["headers"]; ok {
					t.Errorf("a credential header is left over from the earlier destination: %v", ex["headers"])
				}
				if strings.Contains(exporter, "http") {
					t.Errorf("the protocol of the earlier destination is left over: %s", exporter)
				}
			})
		}
	}
}

// A bearer operator reached without a client certificate: the certificate settings are switched off and the credential
// header is left to the person, who holds the token (and, for the operator's private CA, the CA Secret they set).
func TestExportFlagsWithoutAClientCertificateSwitchTheCertificateOff(t *testing.T) {
	op := store.Operator{ID: "op-abc123", ReceiverAuth: store.ReceiverAuthBearer}
	extra := append(staleDestination("telemetry.export.otlp"), "--set", "telemetry.export.otlp.auth.secretName=my-token")
	extra = append(extra, flagWords(t, exportBlockFlags("telemetry.export.otlp", op, operatorInClusterEndpoint(op), false))...)
	ex := mustMap(t, agentExporters(t, "continuum-telemetry-host-config", extra)["otlp"], "exporter otlp")
	tls := mustMap(t, ex["tls"], "tls")
	if tls["cert_file"] != nil || tls["key_file"] != nil || tls["insecure"] != false {
		t.Errorf("tls = %v: the client certificate of the earlier destination is still on", tls)
	}
	if tls["ca_file"] != nil {
		t.Errorf("tls.ca_file = %v: a path of the earlier destination is still trusted", tls["ca_file"])
	}
	if tls["server_name_override"] != "op-abc123.continuum-system.svc" {
		t.Errorf("tls.server_name_override = %v", tls["server_name_override"])
	}
	h := mustMap(t, ex["headers"], "headers")
	if h["Authorization"] != "${env:CONTINUUM_TELEMETRY_AUTH}" {
		t.Errorf("the person's credential header was cleared: %v", h)
	}
}

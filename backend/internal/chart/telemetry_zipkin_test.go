package chart

import "testing"

// protocol=zipkin renders the collector's own zipkin exporter, posting JSON spans to Zipkin's v2 endpoint,
// and the traces pipeline (the only one that can exist with it) names that exporter and no other.
func TestTelemetryExportProtocolZipkinRendersTheZipkinExporter(t *testing.T) {
	for endpoint, want := range map[string]string{
		"zipkin.observability.svc:9411":                "https://zipkin.observability.svc:9411/api/v2/spans",
		"http://zipkin.observability.svc:9411":         "http://zipkin.observability.svc:9411/api/v2/spans",
		"http://zipkin.observability.svc:9411/":        "http://zipkin.observability.svc:9411/api/v2/spans",
		"https://zipkin.example.com/custom/path/spans": "https://zipkin.example.com/custom/path/spans",
	} {
		r := render(t, "--set", "telemetry.export.otlp.endpoint="+endpoint, "--set", "telemetry.export.otlp.protocol=zipkin",
			"--set", "telemetry.traces.traces.enabled=true")
		cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
		exporters, _ := cfg["exporters"].(map[string]any)
		if _, ok := exporters["otlp"]; ok {
			t.Errorf("%s: an otlp exporter rendered alongside protocol=zipkin", endpoint)
		}
		if _, ok := exporters["otlphttp"]; ok {
			t.Errorf("%s: an otlphttp exporter rendered alongside protocol=zipkin", endpoint)
		}
		z, ok := exporters["zipkin"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no zipkin exporter rendered: %+v", endpoint, exporters)
		}
		if z["endpoint"] != want || z["format"] != "json" {
			t.Errorf("%s: zipkin = %+v, want endpoint %q and format json", endpoint, z, want)
		}
		pipelines, _ := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
		if len(pipelines) != 1 {
			t.Fatalf("%s: pipelines = %+v, want only traces", endpoint, pipelines)
		}
		traces, _ := pipelines["traces"].(map[string]any)
		if exp, _ := traces["exporters"].([]any); len(exp) != 1 || exp[0] != "zipkin" {
			t.Errorf("%s: traces exporters = %v, want exactly [zipkin]", endpoint, exp)
		}
	}
}

// Insecure picks http:// for a bare host, a credential header and a CA bundle are carried like otlphttp's,
// and the debug exporter still sits beside the real one.
func TestTelemetryZipkinExporterCarriesInsecureAuthTLSAndDebug(t *testing.T) {
	r := render(t, "--set", "telemetry.export.otlp.endpoint=zipkin.svc:9411", "--set", "telemetry.export.otlp.protocol=zipkin",
		"--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.traces.traces.enabled=true")
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ := cfg["exporters"].(map[string]any)
	if z, _ := exporters["zipkin"].(map[string]any); z["endpoint"] != "http://zipkin.svc:9411/api/v2/spans" {
		t.Errorf("insecure zipkin endpoint = %v, want http://", z["endpoint"])
	}

	r = render(t, "--set", "telemetry.export.otlp.endpoint=zipkin.example.com:443", "--set", "telemetry.export.otlp.protocol=zipkin",
		"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.debug.verbosity=basic",
		"--set", "telemetry.export.otlp.tls.caFile=/ca/ca.crt",
		"--set", "telemetry.export.otlp.auth.headerName=Authorization", "--set", "telemetry.export.otlp.auth.secretName=zipkin-token", "--set", "telemetry.export.otlp.auth.secretKey=token")
	cfg = otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ = cfg["exporters"].(map[string]any)
	z, _ := exporters["zipkin"].(map[string]any)
	tls, _ := z["tls"].(map[string]any)
	headers, _ := z["headers"].(map[string]any)
	if tls["ca_file"] != "/ca/ca.crt" || headers["Authorization"] != "${env:CONTINUUM_TELEMETRY_AUTH}" {
		t.Errorf("zipkin tls/headers = %+v / %+v", tls, headers)
	}
	if _, ok := exporters["debug"]; !ok {
		t.Errorf("the debug exporter is missing beside zipkin: %+v", exporters)
	}
}

// Zipkin cannot receive metrics or logs: any other signal with it is refused at render time, not silently
// sent to an endpoint that would drop it, and so is zipkin with no traces at all.
func TestTelemetryZipkinRefusesEverythingButTraces(t *testing.T) {
	base := []string{"--set", "telemetry.export.otlp.endpoint=zipkin.svc:9411", "--set", "telemetry.export.otlp.protocol=zipkin"}
	for name, extra := range map[string][]string{
		"host metrics":     {"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.resourceUsage.metrics.enabled=true"},
		"system logs":      {"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.systemLogs.logs.enabled=true"},
		"cluster state":    {"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true"},
		"application logs": {"--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true"},
		"no traces at all": {"--set", "telemetry.kubernetesEvents.logs.enabled=true"},
	} {
		if out, err := helmTemplate(t, append(append([]string{}, base...), extra...)...); err == nil {
			t.Errorf("%s: rendered, want a refusal:\n%.400s", name, out)
		}
	}
	// And with telemetry off entirely the protocol value alone is harmless.
	if out, err := helmTemplate(t, base...); err != nil {
		t.Errorf("protocol=zipkin with no telemetry enabled should still render: %v\n%.400s", err, out)
	}
}

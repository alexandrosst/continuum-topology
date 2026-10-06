package chart

import "testing"

// The receiver's Service is ClusterIP unless an exposure is asked for, and only the three known types are accepted.
func TestOperatorServiceTypeFollowsTheExposureAndDefaultsToClusterIP(t *testing.T) {
	if r := operatorRender(t); r.services["op"].Spec.Type != "" && string(r.services["op"].Spec.Type) != "ClusterIP" {
		t.Fatalf("default Service type = %q", r.services["op"].Spec.Type)
	}
	for _, typ := range []string{"LoadBalancer", "NodePort"} {
		r := operatorRender(t, "--set", "service.type="+typ)
		found := false
		for _, s := range r.services {
			if string(s.Spec.Type) == typ {
				found = true
			}
		}
		if !found {
			t.Errorf("service.type=%s did not reach the Service: %+v", typ, r.services)
		}
	}
	if out, err := operatorHelmTemplate(t, "--set", "service.type=ExternalName"); err == nil {
		t.Fatalf("service.type=ExternalName was accepted:\n%s", out)
	}
}

// An exporter that reaches a regional operator at an advertised address verifies its certificate against the name
// the certificate has, not the address: the operator chart's default and routes, each its own exporter.
func TestOperatorExporterCanVerifyAStableServerName(t *testing.T) {
	c := operatorCollectorConfig(t, "--set", "export.otlp.endpoint=203.0.113.7:4317",
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=x",
		"--set", "export.otlp.tls.serverName=op-1.continuum-system.svc")
	tls, _ := c.exporters["otlp"]["tls"].(map[string]any)
	if tls["server_name_override"] != "op-1.continuum-system.svc" || tls["ca_file"] == nil {
		t.Fatalf("exporter tls = %v", tls)
	}
	// Left alone it is not rendered, so nothing that already works changes.
	c = operatorCollectorConfig(t, "--set", "export.otlp.endpoint=c:4317")
	if tls, _ := c.exporters["otlp"]["tls"].(map[string]any); tls["server_name_override"] != nil {
		t.Fatalf("a server name rendered by default: %v", tls)
	}
}

func TestAgentExportersCanVerifyAStableServerName(t *testing.T) {
	args := append([]string{}, allSignals...)
	args = append(args, "--set-string", "telemetry.export.otlp.endpoint=203.0.113.7:4317", "--set", "telemetry.export.otlp.tls.mtls.enabled=true",
		"--set", "telemetry.export.otlp.tls.mtls.secretName=x", "--set", "telemetry.export.otlp.tls.serverName=op-1.continuum-system.svc")
	args = append(args, routeFlags("traces", "203.0.113.7:4317", "grpc", "tls.mtls.enabled=true", "tls.mtls.secretName=x", "tls.serverName=op-1.continuum-system.svc")...)
	r := render(t, args...)
	cluster := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	exporters, _ := cluster["exporters"].(map[string]any)
	for _, name := range []string{"otlp", "otlp/traces"} {
		e, _ := exporters[name].(map[string]any)
		tls, _ := e["tls"].(map[string]any)
		if tls["server_name_override"] != "op-1.continuum-system.svc" {
			t.Errorf("%s: tls = %v (exporters %v)", name, tls, keys(exporters))
		}
	}
	// And not by default.
	pr := render(t, append(append([]string{}, allSignals...), "--set-string", "telemetry.export.otlp.endpoint=c:4317")...)
	plain := otelConfig(t, pr.configmaps["continuum-telemetry-cluster-config"].Data)
	pe, _ := plain["exporters"].(map[string]any)["otlp"].(map[string]any)
	if tls, _ := pe["tls"].(map[string]any); tls["server_name_override"] != nil {
		t.Fatalf("a server name rendered by default: %v", tls)
	}
}

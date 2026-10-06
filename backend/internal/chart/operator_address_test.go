package chart

import (
	"strings"
	"testing"
)

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

// An exposed Service publishes only the gRPC port clusters elsewhere use, never the HTTP or self-metrics ports, and
// the keep-it-private settings reach it; the default stays exactly what it was.
func TestAnExposedOperatorServicePublishesOnlyTheGRPCPort(t *testing.T) {
	ports := func(r operatorRendered) []string {
		var out []string
		for _, s := range r.services {
			for _, p := range s.Spec.Ports {
				out = append(out, p.Name)
			}
		}
		return out
	}
	if got := strings.Join(ports(operatorRender(t, "--set", "selfMetrics.enabled=true")), ","); got != "otlp-grpc,otlp-http,metrics" {
		t.Fatalf("default ports = %s", got)
	}
	r := operatorRender(t, "--set", "selfMetrics.enabled=true", "--set", "service.type=LoadBalancer",
		"--set", "service.loadBalancerSourceRanges={203.0.113.0/24}",
		"--set-string", `service.annotations.service\.beta\.kubernetes\.io/aws-load-balancer-internal=true`)
	if got := strings.Join(ports(r), ","); got != "otlp-grpc" {
		t.Fatalf("exposed ports = %s", got)
	}
	for _, s := range r.services {
		if len(s.Spec.LoadBalancerSourceRanges) != 1 || s.Annotations["service.beta.kubernetes.io/aws-load-balancer-internal"] != "true" {
			t.Fatalf("source ranges / annotations missing: %+v", s)
		}
	}
	// Source ranges mean nothing to a NodePort Service and are not rendered there.
	for _, s := range operatorRender(t, "--set", "service.type=NodePort", "--set", "service.loadBalancerSourceRanges={203.0.113.0/24}").services {
		if len(s.Spec.LoadBalancerSourceRanges) != 0 {
			t.Fatalf("a NodePort Service got source ranges: %+v", s)
		}
	}
}

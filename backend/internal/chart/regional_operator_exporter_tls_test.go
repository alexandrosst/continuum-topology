package chart

import "testing"

// The http protocol's TLS block takes the same server name as the grpc one, and an endpoint that already carries a
// scheme is used as written instead of getting a second one.
func TestRegionalOperatorHTTPExporterKeepsSchemeAndServerName(t *testing.T) {
	r := operatorRender(t, "--set", "export.otlp.protocol=http", "--set", "export.otlp.endpoint=https://collector.example:4318",
		"--set", "export.otlp.tls.serverName=collector.internal")
	exp := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)["exporters"].(map[string]any)
	o, ok := exp["otlphttp"].(map[string]any)
	if !ok {
		t.Fatalf("no otlphttp exporter: %+v", exp)
	}
	if o["endpoint"] != "https://collector.example:4318" {
		t.Errorf("endpoint = %v, want it unchanged", o["endpoint"])
	}
	tls, _ := o["tls"].(map[string]any)
	if tls["server_name_override"] != "collector.internal" {
		t.Errorf("http exporter tls = %+v, want server_name_override collector.internal", tls)
	}
	// The route form of the same options.
	r = operatorRender(t, "--set", "export.routes.logs.protocol=http", "--set", "export.routes.logs.endpoint=http://logs.example:4318",
		"--set", "export.routes.logs.tls.serverName=logs.internal")
	exp = otelConfig(t, r.configmaps["op-regional-operator-config"].Data)["exporters"].(map[string]any)
	l, _ := exp["otlphttp/logs"].(map[string]any)
	if l["endpoint"] != "http://logs.example:4318" {
		t.Errorf("route endpoint = %v", l["endpoint"])
	}
	if tls, _ := l["tls"].(map[string]any); tls["server_name_override"] != "logs.internal" {
		t.Errorf("route tls = %+v", tls)
	}
}

// caSecretName is how a CA bundle gets into the collector: the Secret is mounted read-only (the root filesystem is not
// writable, so a bare caFile path has nothing behind it) and the exporter's ca_file points into the mount.
func TestRegionalOperatorCASecretIsMountedAndTrusted(t *testing.T) {
	for _, proto := range []string{"grpc", "http"} {
		r := operatorRender(t, "--set", "export.otlp.protocol="+proto, "--set", "export.otlp.tls.caSecretName=dest-ca",
			"--set", "export.routes.traces.endpoint=traces.example:4317", "--set", "export.routes.traces.tls.caSecretName=traces-ca")
		cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
		exp := cfg["exporters"].(map[string]any)
		def := map[string]string{"grpc": "otlp", "http": "otlphttp"}[proto]
		for name, want := range map[string]string{def: "/export-ca/ca.crt", "otlp/traces": "/export-ca-traces/ca.crt"} {
			e, _ := exp[name].(map[string]any)
			if tls, _ := e["tls"].(map[string]any); tls["ca_file"] != want {
				t.Errorf("%s: exporter %s tls = %+v, want ca_file %s", proto, name, tls, want)
			}
		}
		spec := r.deployments["op-regional-operator"].Spec.Template.Spec
		for vol, secret := range map[string]string{"export-ca": "dest-ca", "export-ca-traces": "traces-ca"} {
			found := false
			for _, v := range spec.Volumes {
				found = found || (v.Name == vol && v.Secret != nil && v.Secret.SecretName == secret)
			}
			if !found {
				t.Errorf("%s: no volume %s from Secret %s: %+v", proto, vol, secret, spec.Volumes)
			}
			mounted := false
			for _, m := range spec.Containers[0].VolumeMounts {
				mounted = mounted || (m.Name == vol && m.ReadOnly && m.MountPath == "/"+vol)
			}
			if !mounted {
				t.Errorf("%s: %s is not mounted read-only at /%s", proto, vol, vol)
			}
		}
	}
	// With a client certificate the destination is verified against that Secret's ca.crt, so the CA Secret is not mounted.
	r := operatorRender(t, "--set", "export.otlp.tls.caSecretName=dest-ca", "--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=client")
	for _, v := range r.deployments["op-regional-operator"].Spec.Template.Spec.Volumes {
		if v.Name == "export-ca" {
			t.Errorf("the CA Secret is mounted although mtls supplies the CA: %+v", v)
		}
	}
}

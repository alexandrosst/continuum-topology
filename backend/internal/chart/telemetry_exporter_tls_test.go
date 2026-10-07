package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// The http (and zipkin) exporters take the same tls.serverName the grpc one does, and an endpoint that already carries
// a scheme is used as written instead of getting a second one prepended.
func TestAgentHTTPExporterKeepsSchemeAndServerName(t *testing.T) {
	r := render(t, "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.endpoint=https://collector.example:4318",
		"--set", "telemetry.export.otlp.protocol=http", "--set", "telemetry.export.otlp.tls.serverName=collector.internal",
		"--set", "telemetry.export.routes.logs.endpoint=logs.example:4318", "--set", "telemetry.export.routes.logs.protocol=http",
		"--set", "telemetry.export.routes.logs.tls.serverName=logs.internal", "--set", "telemetry.systemLogs.logs.enabled=true")
	ex := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "exporters")
	if got := sub(t, ex, "otlphttp")["endpoint"]; got != "https://collector.example:4318" {
		t.Errorf("endpoint with a scheme = %v, want it unchanged", got)
	}
	if got := sub(t, ex, "otlphttp", "tls")["server_name_override"]; got != "collector.internal" {
		t.Errorf("http exporter server_name_override = %v, want collector.internal", got)
	}
	// A bare host:port still gets the scheme tls.insecure picks.
	if got := sub(t, ex, "otlphttp/logs")["endpoint"]; got != "https://logs.example:4318" {
		t.Errorf("route endpoint without a scheme = %v, want https://logs.example:4318", got)
	}
	if got := sub(t, ex, "otlphttp/logs", "tls")["server_name_override"]; got != "logs.internal" {
		t.Errorf("route server_name_override = %v, want logs.internal", got)
	}
	// With no server name and no certificate there is still no tls block to set.
	plain := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.protocol=http", "--set", "telemetry.export.otlp.endpoint=x:4318")...)
	if o := sub(t, otelConfig(t, plain.configmaps["continuum-telemetry-host-config"].Data), "exporters", "otlphttp"); o["tls"] != nil {
		t.Errorf("an http exporter with nothing to say about TLS has a tls block: %v", o["tls"])
	}
}

// caSecretName is how a CA bundle gets into the collectors: the Secret is mounted read-only (the root filesystem is not
// writable, so a bare caFile path has nothing behind it) and the exporter's ca_file points into the mount.
func TestAgentCASecretIsMountedOnBothCollectorsAndTrusted(t *testing.T) {
	r := render(t, "--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "telemetry.export.otlp.endpoint=x:4318", "--set", "telemetry.export.otlp.protocol=http", "--set", "telemetry.export.otlp.tls.caSecretName=dest-ca",
		"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.export.routes.logs.endpoint=logs.example:4317", "--set", "telemetry.export.routes.logs.tls.caSecretName=logs-ca")
	for _, cm := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
		ex := sub(t, otelConfig(t, r.configmaps[cm].Data), "exporters")
		if got := sub(t, ex, "otlphttp", "tls")["ca_file"]; got != "/export-ca/ca.crt" {
			t.Errorf("%s: default ca_file = %v, want /export-ca/ca.crt", cm, got)
		}
		if got := sub(t, ex, "otlp/logs", "tls")["ca_file"]; got != "/export-ca-logs/ca.crt" {
			t.Errorf("%s: logs route ca_file = %v, want /export-ca-logs/ca.crt", cm, got)
		}
	}
	pods := map[string]corev1.PodSpec{
		"host":    r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec,
		"cluster": r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec,
	}
	for name, pod := range pods {
		for vol, secret := range map[string]string{"export-ca": "dest-ca", "export-ca-logs": "logs-ca"} {
			if got := caVolumeSecret(pod, vol); got != secret {
				t.Errorf("%s collector: volume %s comes from Secret %q, want %q", name, vol, got, secret)
			}
			mounted := false
			for _, m := range pod.Containers[0].VolumeMounts {
				mounted = mounted || (m.Name == vol && m.ReadOnly && m.MountPath == "/"+vol)
			}
			if !mounted {
				t.Errorf("%s collector: %s is not mounted read-only at /%s", name, vol, vol)
			}
		}
	}
	// With a client certificate the destination is verified against that Secret's ca.crt, so the CA Secret is not mounted.
	m := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.export.otlp.tls.caSecretName=dest-ca",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=client")...)
	if got := caVolumeSecret(m.daemonsets["continuum-telemetry-host"].Spec.Template.Spec, "export-ca"); got != "" {
		t.Errorf("the CA Secret %q is mounted although mtls supplies the CA", got)
	}
}

// caVolumeSecret is the Secret a pod's volume of that name reads, or "" when there is no such volume.
func caVolumeSecret(pod corev1.PodSpec, name string) string {
	for _, v := range pod.Volumes {
		if v.Name == name && v.Secret != nil {
			return v.Secret.SecretName
		}
	}
	return ""
}

// The locked-down telemetry policy still lets the collectors do their jobs: reach the Kubernetes API server (the
// addresses networkPolicy.egress already names, unless the policy has its own) and the node's kubelet on 10250, as
// well as the export destination.
func TestAgentTelemetryEgressAllowsAPIServerAndKubelet(t *testing.T) {
	base := []string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.resourceUsage.metrics.enabled=true",
		"--set", "networkPolicy.telemetryEgress.enabled=true",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317"}
	cidrsAndPorts := func(rules []networkingv1.NetworkPolicyEgressRule, port int32) (cidrs []string) {
		for _, r := range rules {
			for _, p := range r.Ports {
				if p.Port != nil && p.Port.IntVal == port {
					for _, to := range r.To {
						if to.IPBlock != nil {
							cidrs = append(cidrs, to.IPBlock.CIDR)
						}
					}
				}
			}
		}
		return cidrs
	}

	// Nothing to name for the API server: no rule for it, but the kubelet and the destination are there.
	p := render(t, base...).policies["continuum-telemetry-egress"]
	if got := cidrsAndPorts(p.Spec.Egress, 6443); len(got) != 0 {
		t.Errorf("API server rule without any CIDR: %v", got)
	}
	if got := cidrsAndPorts(p.Spec.Egress, 10250); len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Errorf("kubelet rule = %v, want any address on 10250 when kubeletCIDRs is empty", got)
	}
	if got := cidrsAndPorts(p.Spec.Egress, 4317); len(got) != 1 || got[0] != "203.0.113.7/32" {
		t.Errorf("destination rule = %v", got)
	}

	// The agent's own egress list is reused.
	p = render(t, append(append([]string{}, base...), "--set", "networkPolicy.egress.apiServerCIDRs[0]=10.43.0.1/32", "--set", "networkPolicy.egress.apiServerPorts[0]=6443",
		"--set", "networkPolicy.telemetryEgress.kubeletCIDRs[0]=192.168.1.0/24")...).policies["continuum-telemetry-egress"]
	if got := cidrsAndPorts(p.Spec.Egress, 6443); len(got) != 1 || got[0] != "10.43.0.1/32" {
		t.Errorf("API server rule = %v, want networkPolicy.egress.apiServerCIDRs on its ports", got)
	}
	if got := cidrsAndPorts(p.Spec.Egress, 443); len(got) != 0 {
		t.Errorf("port 443 is allowed although apiServerPorts is [6443]: %v", got)
	}
	if got := cidrsAndPorts(p.Spec.Egress, 10250); len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Errorf("kubelet rule = %v, want kubeletCIDRs", got)
	}

	// The policy's own list wins over the agent's.
	p = render(t, append(append([]string{}, base...), "--set", "networkPolicy.egress.apiServerCIDRs[0]=10.43.0.1/32",
		"--set", "networkPolicy.telemetryEgress.apiServerCIDRs[0]=10.99.0.1/32")...).policies["continuum-telemetry-egress"]
	if got := cidrsAndPorts(p.Spec.Egress, 443); len(got) != 1 || got[0] != "10.99.0.1/32" {
		t.Errorf("API server rule = %v, want telemetryEgress.apiServerCIDRs", got)
	}

	// No host collector metrics, no kubelet to reach.
	p = render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.kubernetesState.metrics.enabled=true",
		"--set", "networkPolicy.telemetryEgress.enabled=true",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317").policies["continuum-telemetry-egress"]
	if got := cidrsAndPorts(p.Spec.Egress, 10250); len(got) != 0 {
		t.Errorf("kubelet allowed although nothing reads it: %v", got)
	}
	// The refusal says what allowedEgress is for, and no longer implies it is the only thing to list.
	if out, err := helmTemplate(t, "--set", "networkPolicy.telemetryEgress.enabled=true"); err == nil || !strings.Contains(out, "apiServerCIDRs") || !strings.Contains(out, "kubeletCIDRs") {
		t.Errorf("the refusal does not mention the API server and kubelet settings: %v\n%s", err, out)
	}
}

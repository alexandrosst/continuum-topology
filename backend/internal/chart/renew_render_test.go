package chart

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// The certificate renewer: what the charts render so that a certificate is replaced before it runs out without anyone
// doing anything, and how little that is allowed to touch.

const testCAPin = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

var operatorRenewOn = []string{"--set", "renew.enabled=true", "--set", "renew.server=ikhnos.example:8443", "--set", "renew.caPin=" + testCAPin}

// renderedRoles and renderedDeployments split a render into the objects the tests below look at.
func renderedRoles(t *testing.T, out string) map[string]rbacv1.Role {
	t.Helper()
	roles := map[string]rbacv1.Role{}
	for _, doc := range strings.Split(out, "\n---") {
		var r rbacv1.Role
		if err := sigsyaml.Unmarshal([]byte(doc), &r); err != nil {
			t.Fatalf("not YAML: %v\n%s", err, doc)
		}
		if r.Kind == "Role" {
			roles[r.Name] = r
		}
	}
	return roles
}

func renderedDeployments(t *testing.T, out string) map[string]appsv1.Deployment {
	t.Helper()
	ds := map[string]appsv1.Deployment{}
	for _, doc := range strings.Split(out, "\n---") {
		var d appsv1.Deployment
		if err := sigsyaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatalf("not YAML: %v\n%s", err, doc)
		}
		if d.Kind == "Deployment" {
			ds[d.Name] = d
		}
	}
	return ds
}

func TestRegionalOperatorRenewerIsOffUnlessAskedFor(t *testing.T) {
	out, err := operatorHelmTemplate(t, append([]string{}, operatorReceiverTLS...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{"cert-renew", "kube-api-access", "CONTINUUM_RENEW_SECRETS", "kind: Role"} {
		if strings.Contains(out, s) {
			t.Errorf("a render without renew.enabled contains %q", s)
		}
	}
}

func TestRegionalOperatorRenewerGetsOnlyWhatItNeeds(t *testing.T) {
	args := append(append(append([]string{}, operatorReceiverTLS...), operatorRenewOn...),
		"--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=op-client-tls")
	out, err := operatorHelmTemplate(t, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// Exactly the two Secrets the operator presents or serves, get/update/patch only: no list, no create, no wildcard.
	roles := renderedRoles(t, out)
	role, ok := roles["op-regional-operator-cert-renew"]
	if !ok || len(roles) != 1 || len(role.Rules) != 1 {
		t.Fatalf("want one Role with one rule, got %v", roles)
	}
	rule := role.Rules[0]
	if !sameSet(rule.ResourceNames, []string{"op-receiver-tls", "op-client-tls"}) || !sameSet(rule.Verbs, []string{"get", "update", "patch"}) ||
		!sameSet(rule.Resources, []string{"secrets"}) || !sameSet(rule.APIGroups, []string{""}) {
		t.Errorf("rule = %+v", rule)
	}
	if strings.Contains(out, "kind: ClusterRole") {
		t.Error("a ClusterRole is rendered")
	}

	// The token is mounted into the sidecar and not into the collector.
	d := renderedDeployments(t, out)["op-regional-operator"]
	if d.Spec.Template.Spec.AutomountServiceAccountToken == nil || *d.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Error("the pod automounts the service-account token: only the sidecar may have it")
	}
	var renewer, collector bool
	for _, c := range d.Spec.Template.Spec.Containers {
		mounts := false
		for _, m := range c.VolumeMounts {
			if m.Name == "kube-api-access" {
				mounts = true
			}
		}
		switch c.Name {
		case "cert-renew":
			renewer = true
			if !mounts {
				t.Error("the sidecar has no token")
			}
			if strings.Join(c.Args, " ") != "cert-renew --every=1h" {
				t.Errorf("sidecar args = %v", c.Args)
			}
			env := map[string]string{}
			for _, e := range c.Env {
				env[e.Name] = e.Value
			}
			if env["CONTINUUM_SERVER"] != "ikhnos.example:8443" || env["CONTINUUM_CA_PIN"] != testCAPin ||
				env["CONTINUUM_RENEW_SECRETS"] != "op-receiver-tls,op-client-tls" {
				t.Errorf("sidecar env = %v", env)
			}
			if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem ||
				c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
				t.Errorf("sidecar security context = %+v", c.SecurityContext)
			}
			if !strings.Contains(c.Image, "continuum") {
				t.Errorf("sidecar image = %s: it is the continuum image, not the collector's", c.Image)
			}
		case "otel-collector":
			collector = true
			if mounts {
				t.Error("the collector container has the service-account token")
			}
		}
	}
	if !renewer || !collector {
		t.Fatalf("containers: renewer=%v collector=%v", renewer, collector)
	}
}

func TestRegionalOperatorRenewerNeedsItsServerAndPin(t *testing.T) {
	out, err := operatorHelmTemplate(t, append(append([]string{}, operatorReceiverTLS...), "--set", "renew.enabled=true")...)
	if err == nil || !strings.Contains(out, "renew.server") {
		t.Errorf("renew.enabled without a server rendered: err=%v\n%s", err, out)
	}
	out, err = operatorHelmTemplate(t, "--set", "renew.enabled=true", "--set", "renew.server=nonsense", "--set", "renew.caPin=x")
	if err == nil {
		t.Errorf("a malformed renew.server / renew.caPin passed the schema:\n%s", out)
	}
	// With nothing to renew (no TLS at all) the sidecar is not rendered even when asked for.
	out, err = operatorHelmTemplate(t, operatorRenewOn...)
	if err != nil || strings.Contains(out, "cert-renew") {
		t.Errorf("a sidecar with no Secret to renew: err=%v", err)
	}
}

func TestRegionalOperatorReceiverIsTLS13Only(t *testing.T) {
	cfg := operatorConfig(t, operatorReceiverTLS...)
	for _, proto := range []string{"grpc", "http"} {
		if got := sub(t, cfg, "receivers", "otlp", "protocols", proto, "tls")["min_version"]; got != "1.3" {
			t.Errorf("receiver %s min_version = %v", proto, got)
		}
	}
	// An exporter dials somebody else's server: it keeps the library default.
	cfg = operatorConfig(t, "--set", "export.otlp.tls.mtls.enabled=true", "--set", "export.otlp.tls.mtls.secretName=c")
	if _, ok := sub(t, cfg, "exporters", "otlp", "tls")["min_version"]; ok {
		t.Error("an exporter pins min_version")
	}
}

// A renewed certificate and key replace the old ones about every 10 days by themselves, and the collector re-reads them:
// they must not restart the pod. The CA still does.
func TestRegionalOperatorDoesNotRestartForARenewedCertificate(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("op-receiver-tls", map[string]string{"tls.crt": "cert-1", "tls.key": "key-1", "ca.crt": "ca-1"})
	args := append(append([]string{"--set", "export.otlp.endpoint=collector.example:4317"}, operatorReceiverTLS...), operatorRenewOn...)
	sum := func() string {
		return podAnnotations(t, helmTemplateInCluster(t, RegionalOperator, kc, args...))["rel-regional-operator"]["checksum/mtls"]
	}
	first := sum()
	if first == "" {
		t.Fatal("no checksum/mtls although the Secret exists")
	}
	k.set("op-receiver-tls", map[string]string{"tls.crt": "cert-2", "tls.key": "key-2", "ca.crt": "ca-1"})
	if got := sum(); got != first {
		t.Error("a renewed certificate and key changed the checksum: the pod would restart for it")
	}
	k.set("op-receiver-tls", map[string]string{"tls.crt": "cert-2", "tls.key": "key-2", "ca.crt": "ca-2"})
	if got := sum(); got == first {
		t.Error("a replaced CA did not change the checksum")
	}
}

func TestAgentRenewsItsClientCertificatesAndNothingElse(t *testing.T) {
	mtls := append(append([]string{}, baseSet...),
		"--set", "telemetry.export.otlp.endpoint=collector.example:4317",
		"--set", "telemetry.export.otlp.tls.mtls.enabled=true", "--set", "telemetry.export.otlp.tls.mtls.secretName=op-client-tls",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
		"--set", "telemetry.export.routes.logs.endpoint=loki.example:4317",
		"--set", "telemetry.export.routes.logs.tls.mtls.enabled=true", "--set", "telemetry.export.routes.logs.tls.mtls.secretName=logs-client-tls")
	out, err := helmTemplate(t, mtls...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	role, ok := renderedRoles(t, out)["continuum-agent-cert-renew"]
	if !ok || len(role.Rules) != 1 {
		t.Fatalf("no cert-renew Role with one rule in the render")
	}
	r := role.Rules[0]
	if !sameSet(r.ResourceNames, []string{"op-client-tls", "logs-client-tls"}) || !sameSet(r.Verbs, []string{"get", "update", "patch"}) {
		t.Errorf("rule = %+v", r)
	}
	if !strings.Contains(out, `CONTINUUM_RENEW_SECRETS, value: "op-client-tls,logs-client-tls"`) && !strings.Contains(out, `CONTINUUM_RENEW_SECRETS, value: "logs-client-tls,op-client-tls"`) {
		t.Errorf("the agent is not told which Secrets to renew:\n%s", grepLines(out, "RENEW"))
	}

	// No client certificate in play: no Role, no variable.
	out, err = helmTemplate(t, baseSet...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "cert-renew") || strings.Contains(out, "CONTINUUM_RENEW_SECRETS") {
		t.Error("a render without a client certificate has a renewer")
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

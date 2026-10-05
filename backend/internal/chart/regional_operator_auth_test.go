package chart

import (
	"strings"
	"testing"
)

// mtlsOnly is exactly the receiver values the server's install command sets for an operator created today:
// the client certificate (signed by the CA in the receiver TLS Secret) is the ONLY gate.
var mtlsOnly = []string{
	"--set", "receiver.auth.enabled=false",
	"--set", "receiver.requireAuth=true",
	"--set", "receiver.tls.enabled=true",
	"--set", "receiver.tls.secretName=op-1-receiver-tls",
	"--set", "receiver.tls.mtls=true",
}

func TestRegionalOperatorMTLSOnlyRendersTheClientCertificateAsTheOnlyGate(t *testing.T) {
	r := operatorRender(t, mtlsOnly...)
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	protocols := cfg["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)
	for _, proto := range []string{"grpc", "http"} {
		p := protocols[proto].(map[string]any)
		tls, _ := p["tls"].(map[string]any)
		if tls["client_ca_file"] != "/receiver-tls/ca.crt" || tls["cert_file"] != "/receiver-tls/tls.crt" || tls["key_file"] != "/receiver-tls/tls.key" {
			t.Fatalf("%s receiver tls = %v - the required client certificate is the only gate, it must be there", proto, p["tls"])
		}
		if _, has := p["auth"]; has {
			t.Fatalf("%s receiver still has a bearer auth block: %v", proto, p["auth"])
		}
	}
	if _, has := cfg["extensions"].(map[string]any)["bearertokenauth"]; has {
		t.Fatal("bearertokenauth extension rendered for an mTLS-only operator")
	}
	dep := r.deployments["op-regional-operator"].Spec.Template.Spec
	for _, e := range dep.Containers[0].Env {
		if e.Name == "CONTINUUM_OPERATOR_RECEIVER_AUTH" {
			t.Fatal("receiver token env var present for an mTLS-only operator (there is no such Secret)")
		}
	}
	var mounted bool
	for _, v := range dep.Volumes {
		if v.Name == "receiver-tls" && v.Secret != nil && v.Secret.SecretName == "op-1-receiver-tls" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("the receiver TLS Secret (server cert and the client CA) is not mounted")
	}
}

// receiver.requireAuth is what makes a server-generated operator impossible to render open: it refuses
// unless there is a bearer token or a required client certificate.
func TestRegionalOperatorRequireAuthRefusesAnOpenReceiver(t *testing.T) {
	refused := map[string][]string{
		"nothing at all":                {"--set", "receiver.requireAuth=true"},
		"auth explicitly off":           {"--set", "receiver.requireAuth=true", "--set", "receiver.auth.enabled=false"},
		"tls without the client check":  {"--set", "receiver.requireAuth=true", "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=s", "--set", "receiver.tls.mtls=false"},
		"client check but tls is off":   {"--set", "receiver.requireAuth=true", "--set", "receiver.tls.mtls=true"},
		"mtls only with the check lost": {"--set", "receiver.requireAuth=true", "--set", "receiver.auth.enabled=false", "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=op-1-receiver-tls", "--set", "receiver.tls.mtls=false"},
	}
	for name, args := range refused {
		out, err := operatorHelmTemplate(t, args...)
		if err == nil || !strings.Contains(out, "Refusing to render an open receiver") {
			t.Errorf("%s: want a refusal, got err=%v\n%.300s", name, err, out)
		}
	}
	allowed := map[string][]string{
		"bearer token":  {"--set", "receiver.requireAuth=true", "--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=s"},
		"mtls only":     mtlsOnly,
		"both (legacy)": {"--set", "receiver.requireAuth=true", "--set", "receiver.auth.enabled=true", "--set", "receiver.auth.secretName=s", "--set", "receiver.tls.enabled=true", "--set", "receiver.tls.secretName=t"},
		"guard not set": {},
	}
	for name, args := range allowed {
		if out, err := operatorHelmTemplate(t, args...); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
}

func TestRegionalOperatorRequireAuthSchema(t *testing.T) {
	out, err := operatorHelmTemplate(t, "--set", "receiver.requireAuth=maybe")
	if err == nil || !strings.Contains(out, "values don't meet the specifications of the schema") {
		t.Fatalf("a non-boolean requireAuth was accepted: %v\n%.300s", err, out)
	}
}

func TestRegionalOperatorDocsDoNotClaimABearerTokenIsAlwaysRequired(t *testing.T) {
	for _, f := range []string{"Chart.yaml", "values.yaml", "README.md", "templates/NOTES.txt"} {
		b, err := operatorFiles.ReadFile("continuum-regional-operator/" + f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, stale := range []string{"its only credential is the receiver bearer token", "Its only credential is a receiver bearer token", "every regional operator this\n# server creates is installed with auth on"} {
			if strings.Contains(s, stale) {
				t.Errorf("%s still says %q", f, stale)
			}
		}
	}
	b, _ := operatorFiles.ReadFile("continuum-regional-operator/Chart.yaml")
	if !strings.Contains(string(b), "mTLS client certificate") {
		t.Error("Chart.yaml does not mention the client certificate gate")
	}
}

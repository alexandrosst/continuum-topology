package chart

import (
	"strings"
	"testing"
)

// The central gateway is the one door into FUSION: mTLS in, one store per signal type out. The server turns the
// whole set on and off by scaling these workloads, so their names and replica rules are pinned here.

func centralConfig(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	r := fusionRender(t, "f", extra...)
	cm, ok := r.configs["f-fusion-central"]
	if !ok {
		t.Fatalf("no central ConfigMap (have %v)", mapKeys(r.configs))
	}
	return yamlInto(t, cm.Data["otel-collector-config.yaml"])
}

func TestFusionCentralGatewayRequiresAClientCertificateAndFeedsEachStore(t *testing.T) {
	r := fusionRender(t, "f")
	for _, n := range []string{"f-fusion-central"} {
		if _, ok := r.deploys[n]; !ok {
			t.Errorf("no Deployment %s (have %v)", n, mapKeys(r.deploys))
		}
		if _, ok := r.services[n]; !ok {
			t.Errorf("no Service %s", n)
		}
	}
	if _, ok := r.secrets["f-fusion-central-receiver-tls"]; !ok {
		t.Errorf("no receiver TLS Secret (have %v)", mapKeys(r.secrets))
	}
	cfg := yamlInto(t, r.configs["f-fusion-central"].Data["otel-collector-config.yaml"])
	grpc := cfg["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)["grpc"].(map[string]any)
	tls, _ := grpc["tls"].(map[string]any)
	if tls["client_ca_file"] != "/receiver-tls/ca.crt" || tls["cert_file"] != "/receiver-tls/tls.crt" {
		t.Errorf("receiver does not require a client certificate: %v", tls)
	}
	http := cfg["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)["http"].(map[string]any)
	if htls, _ := http["tls"].(map[string]any); htls["client_ca_file"] != "/receiver-tls/ca.crt" {
		t.Errorf("the http receiver is not behind the client certificate: %v", http)
	}
	ex := cfg["exporters"].(map[string]any)
	for name, want := range map[string]string{
		"otlphttp/metrics": "http://f-fusion-prometheus.observability.svc:9090/api/v1/otlp",
		"otlphttp/logs":    "http://f-fusion-loki.observability.svc:3100/otlp",
		"otlp/traces":      "f-fusion-tempo.observability.svc:4317",
	} {
		if got := ex[name].(map[string]any)["endpoint"]; got != want {
			t.Errorf("%s endpoint = %v, want %s", name, got, want)
		}
	}
	pipes := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	for sig, exp := range map[string]string{"metrics": "otlphttp/metrics", "logs": "otlphttp/logs", "traces": "otlp/traces"} {
		got := pipes[sig].(map[string]any)["exporters"].([]any)
		if len(got) != 1 || got[0] != exp {
			t.Errorf("%s pipeline exports to %v, want [%s]", sig, got, exp)
		}
	}
}

func TestFusionCentralOnlyFeedsStoresThatAreDeployed(t *testing.T) {
	cfg := centralConfig(t, "--set", "loki.enabled=false")
	pipes := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	if _, ok := pipes["logs"]; ok {
		t.Error("a logs pipeline is rendered though Loki is not deployed")
	}
	if _, ok := pipes["metrics"]; !ok {
		t.Error("the metrics pipeline is missing")
	}
}

func TestFusionCentralCanBeLeftOut(t *testing.T) {
	r := fusionRender(t, "f", "--set", "central.enabled=false")
	// Grafana's own admin Secret is not the gateway's, and stays.
	if _, ok := r.secrets["f-fusion-central-receiver-tls"]; len(r.deploys) != 0 || ok || len(r.secrets) != 1 {
		t.Errorf("central objects rendered though disabled: %v %v", mapKeys(r.deploys), mapKeys(r.secrets))
	}
	if off := fusionRender(t, "f", "--set", "grafana.enabled=false"); len(off.secrets) != 1 {
		t.Errorf("secrets with Grafana off = %v, want the gateway's alone", mapKeys(off.secrets))
	}
}

func replicasOf(r fusionRendered) map[string]int32 {
	out := map[string]int32{}
	for n, s := range r.sets {
		out[n] = *s.Spec.Replicas
	}
	for n, d := range r.deploys {
		out[n] = *d.Spec.Replicas
	}
	return out
}

// A plain install runs everything; a server-managed one starts standing by, and only the server turns it on.
func TestFusionReplicasFollowTheSwitch(t *testing.T) {
	for n, v := range replicasOf(fusionRender(t, "f")) {
		if v != 1 {
			t.Errorf("%s replicas = %d on a plain install, want 1", n, v)
		}
	}
	got := replicasOf(fusionRender(t, "f", "--set", "switch.managed=true", "--set", "switch.initialReplicas=0"))
	if len(got) != 5 {
		t.Fatalf("want the three stores, Grafana and the gateway, got %v", got)
	}
	for n, v := range got {
		if v != 0 {
			t.Errorf("%s replicas = %d when managed and standing by, want 0", n, v)
		}
	}
}

func TestFusionNetworkPolicyLeavesTheGatewayReachable(t *testing.T) {
	r := fusionRender(t, "f")
	p := r.policies["f-fusion"]
	var found bool
	for _, e := range p.Spec.PodSelector.MatchExpressions {
		if e.Key == "app.kubernetes.io/component" && e.Operator == "In" && strings.Join(e.Values, ",") == "prometheus,loki,tempo" {
			found = true
		}
	}
	if !found {
		t.Errorf("the store policy is not limited to the three stores (it would also isolate the gateway): %+v", p.Spec.PodSelector)
	}
}

// The gateway reloads its certificate files (the server renews them in place), and an exposed gateway publishes only
// the gRPC port other clusters use, keeping the keep-it-private settings.
func TestFusionCentralReloadsItsCertificateAndPublishesOnlyGRPCWhenExposed(t *testing.T) {
	cfg := centralConfig(t)
	grpc := cfg["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)["grpc"].(map[string]any)
	if tls, _ := grpc["tls"].(map[string]any); tls["reload_interval"] != "1h" {
		t.Errorf("the gateway does not reload its certificate: %v", tls)
	}
	ports := func(r fusionRendered) string {
		var out []string
		for _, p := range r.services["f-fusion-central"].Spec.Ports {
			out = append(out, p.Name)
		}
		return strings.Join(out, ",")
	}
	if got := ports(fusionRender(t, "f")); got != "otlp-grpc,otlp-http" {
		t.Errorf("in-cluster ports = %s", got)
	}
	r := fusionRender(t, "f", "--set", "central.service.type=LoadBalancer", "--set", "central.service.loadBalancerSourceRanges={203.0.113.0/24}")
	if got := ports(r); got != "otlp-grpc" {
		t.Errorf("exposed ports = %s", got)
	}
	if rs := r.services["f-fusion-central"].Spec.LoadBalancerSourceRanges; len(rs) != 1 || rs[0] != "203.0.113.0/24" {
		t.Errorf("source ranges = %v", rs)
	}
}

// The gateway's TLS Secret is named by the chart alone, because the Ikhnos server patches exactly
// <name>-central-receiver-tls: it cannot be renamed by a value, and the retired value is refused unless empty (the
// empty string an earlier release stored must not break a --reuse-values upgrade).
func TestFusionCentralTLSSecretNameIsNotConfigurable(t *testing.T) {
	r := fusionRender(t, "f")
	var mounted string
	for _, v := range r.deploys["f-fusion-central"].Spec.Template.Spec.Volumes {
		if v.Name == "receiver-tls" && v.Secret != nil {
			mounted = v.Secret.SecretName
		}
	}
	if mounted != "f-fusion-central-receiver-tls" {
		t.Errorf("the gateway mounts Secret %q, want f-fusion-central-receiver-tls", mounted)
	}
	if out, err := fusionTemplate(t, "f", "--set", "central.receiver.tlsSecretName=mine"); err == nil {
		t.Errorf("a renamed gateway TLS Secret was accepted, which the server would not fill in:\n%s", out)
	}
	if out, err := fusionTemplate(t, "f", "--set", "central.receiver.tlsSecretName="); err != nil {
		t.Errorf("the empty value an earlier release stored is refused: %v\n%s", err, out)
	}
}

// A NodePort Service listens on a port Kubernetes picks at random each time it is created, and never on 4317, so the
// address recorded as the gateway's "Reachable at" (<node address>:<node port>) breaks whenever the Service is
// recreated unless the port can be pinned - as the regional operator chart already allows.
func TestFusionCentralNodePortCanBePinned(t *testing.T) {
	port := func(extra ...string) int32 {
		r := fusionRender(t, "f", extra...)
		for _, p := range r.services["f-fusion-central"].Spec.Ports {
			if p.Name == "otlp-grpc" {
				return p.NodePort
			}
		}
		t.Fatal("no otlp-grpc port on the gateway Service")
		return 0
	}
	if got := port("--set", "central.service.type=NodePort", "--set", "central.service.nodePort=30317"); got != 30317 {
		t.Errorf("pinned NodePort = %d, want 30317", got)
	}
	if got := port("--set", "central.service.type=NodePort"); got != 0 {
		t.Errorf("an unpinned NodePort must be left to Kubernetes, got %d", got)
	}
	if got := port("--set", "central.service.type=LoadBalancer", "--set", "central.service.nodePort=30317"); got != 0 {
		t.Errorf("a LoadBalancer Service got nodePort %d", got)
	}
}

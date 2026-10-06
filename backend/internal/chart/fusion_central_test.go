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
	if len(r.deploys) != 0 || len(r.secrets) != 0 {
		t.Errorf("central objects rendered though disabled: %v %v", mapKeys(r.deploys), mapKeys(r.secrets))
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
	if len(got) != 4 {
		t.Fatalf("want the three stores and the gateway, got %v", got)
	}
	for n, v := range got {
		if v != 0 {
			t.Errorf("%s replicas = %d when managed and standing by, want 0", n, v)
		}
	}
}

func TestFusionNetworkPolicyLeavesTheGatewayReachable(t *testing.T) {
	r := fusionRender(t, "f", "--set", "networkPolicy.enabled=true")
	p := r.policies["f-fusion"]
	var found bool
	for _, e := range p.Spec.PodSelector.MatchExpressions {
		if e.Key == "app.kubernetes.io/component" && strings.Join(e.Values, ",") == "central" {
			found = true
		}
	}
	if !found {
		t.Errorf("the store policy would also isolate the gateway: %+v", p.Spec.PodSelector)
	}
}

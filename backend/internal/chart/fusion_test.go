package chart

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// continuum-fusion: Prometheus, Loki and Tempo, one durable pod per signal type. A regional operator is pointed at
// the Services by name, so the names are a contract these tests pin down.

type fusionRendered struct {
	sets     map[string]appsv1.StatefulSet
	services map[string]corev1.Service
	configs  map[string]corev1.ConfigMap
	policies map[string]networkingv1.NetworkPolicy
	accounts map[string]corev1.ServiceAccount
	deploys  map[string]appsv1.Deployment
	secrets  map[string]corev1.Secret
}

func fusionTemplate(t *testing.T, release string, extra ...string) (string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := Fusion.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), Fusion.Filename())
	if err := os.WriteFile(tgz, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(h, append([]string{"template", release, tgz, "--namespace", "observability"}, extra...)...).CombinedOutput()
	return string(out), err
}

func fusionRender(t *testing.T, release string, extra ...string) fusionRendered {
	t.Helper()
	out, err := fusionTemplate(t, release, extra...)
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", extra, err, out)
	}
	r := fusionRendered{map[string]appsv1.StatefulSet{}, map[string]corev1.Service{}, map[string]corev1.ConfigMap{}, map[string]networkingv1.NetworkPolicy{}, map[string]corev1.ServiceAccount{}, map[string]appsv1.Deployment{}, map[string]corev1.Secret{}}
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Kind == "" {
			continue
		}
		into := func(v any) {
			if err := json.Unmarshal(raw, v); err != nil {
				t.Fatal(err)
			}
		}
		n := meta.Metadata.Name
		switch meta.Kind {
		case "StatefulSet":
			var s appsv1.StatefulSet
			into(&s)
			r.sets[n] = s
		case "Service":
			var s corev1.Service
			into(&s)
			r.services[n] = s
		case "ConfigMap":
			var c corev1.ConfigMap
			into(&c)
			r.configs[n] = c
		case "NetworkPolicy":
			var p networkingv1.NetworkPolicy
			into(&p)
			r.policies[n] = p
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deploys[n] = d
		case "Secret":
			var sec corev1.Secret
			into(&sec)
			r.secrets[n] = sec
		case "ServiceAccount":
			var a corev1.ServiceAccount
			into(&a)
			r.accounts[n] = a
		}
	}
	return r
}

func yamlInto(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := sigsyaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("config does not parse: %v\n%s", err, s)
	}
	return m
}

func TestFusionPackageIsLintableAndComplete(t *testing.T) {
	b, err := Fusion.Package()
	if err != nil {
		t.Fatal(err)
	}
	if Fusion.Filename() != "continuum-fusion-"+Fusion.Version()+".tgz" || Fusion.Version() == "0.0.0" {
		t.Fatalf("filename/version wrong: %s %s", Fusion.Filename(), Fusion.Version())
	}
	if h, err := exec.LookPath("helm"); err == nil {
		tgz := filepath.Join(t.TempDir(), Fusion.Filename())
		os.WriteFile(tgz, b, 0o644)
		if out, err := exec.Command(h, "lint", tgz).CombinedOutput(); err != nil {
			t.Fatalf("helm lint: %v\n%s", err, out)
		}
	}
}

// The Service names are what the server prints into a regional operator's command: a release already called
// "...fusion" is not doubled, and any other gets "-fusion" appended.
func TestFusionServiceNamesAreTheContract(t *testing.T) {
	for release, prefix := range map[string]string{"f": "f-fusion", "continuum-fusion": "continuum-fusion", "prod": "prod-fusion"} {
		r := fusionRender(t, release)
		for _, c := range []string{"prometheus", "loki", "tempo"} {
			if _, ok := r.services[prefix+"-"+c]; !ok {
				t.Errorf("release %q: no Service %s-%s (have %v)", release, prefix, c, mapKeys(r.services))
			}
			if _, ok := r.sets[prefix+"-"+c]; !ok {
				t.Errorf("release %q: no StatefulSet %s-%s", release, prefix, c)
			}
		}
	}
	r := fusionRender(t, "f")
	want := map[string][]int32{"f-fusion-prometheus": {9090}, "f-fusion-loki": {3100}, "f-fusion-tempo": {4317, 4318, 3200}}
	for name, ports := range want {
		var got []int32
		for _, p := range r.services[name].Spec.Ports {
			got = append(got, p.Port)
		}
		if len(got) != len(ports) {
			t.Errorf("%s ports = %v, want %v", name, got, ports)
			continue
		}
		for i := range ports {
			if got[i] != ports[i] {
				t.Errorf("%s ports = %v, want %v", name, got, ports)
			}
		}
		if r.services[name].Spec.Type != "" && r.services[name].Spec.Type != corev1.ServiceTypeClusterIP {
			t.Errorf("%s is %s: the stores have no login and must stay ClusterIP", name, r.services[name].Spec.Type)
		}
	}
}

func mapKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestFusionPrometheusTakesOTLPAndKeepsTheJoinLabels(t *testing.T) {
	r := fusionRender(t, "f", "--set", "prometheus.retention=30d")
	args := strings.Join(r.sets["f-fusion-prometheus"].Spec.Template.Spec.Containers[0].Args, " ")
	for _, want := range []string{"--web.enable-otlp-receiver", "--storage.tsdb.retention.time=30d", "--config.file=/etc/prometheus/prometheus.yml"} {
		if !strings.Contains(args, want) {
			t.Errorf("prometheus args %q lack %q", args, want)
		}
	}
	cfg := yamlInto(t, r.configs["f-fusion-prometheus"].Data["prometheus.yml"])
	promoted, _ := cfg["otlp"].(map[string]any)["promote_resource_attributes"].([]any)
	have := map[string]bool{}
	for _, p := range promoted {
		have[p.(string)] = true
	}
	// What lets a metric be joined to a span or a log line later.
	for _, k := range []string{"service.name", "k8s.pod.name", "k8s.namespace.name", "continuum.cluster.id"} {
		if !have[k] {
			t.Errorf("resource attribute %s is not promoted to a label", k)
		}
	}
	if _, scraping := cfg["scrape_configs"]; scraping {
		t.Error("nothing is scraped: every sample is pushed over OTLP")
	}
}

func TestFusionLokiAndTempoConfigs(t *testing.T) {
	r := fusionRender(t, "f", "--set", "loki.retention=72h", "--set", "tempo.retention=48h")
	loki := yamlInto(t, r.configs["f-fusion-loki"].Data["loki.yaml"])
	limits := loki["limits_config"].(map[string]any)
	if limits["retention_period"] != "72h" {
		t.Errorf("loki retention = %v", limits["retention_period"])
	}
	// The OTLP endpoint stores most of a log's context as structured metadata; without this Loki refuses it.
	if limits["allow_structured_metadata"] != true {
		t.Error("loki must allow structured metadata, or its OTLP endpoint rejects logs")
	}
	if loki["compactor"].(map[string]any)["retention_enabled"] != true {
		t.Error("loki retention is only applied when the compactor enforces it")
	}
	tempo := yamlInto(t, r.configs["f-fusion-tempo"].Data["tempo.yaml"])
	if got := tempo["compactor"].(map[string]any)["compaction"].(map[string]any)["block_retention"]; got != "48h" {
		t.Errorf("tempo retention = %v", got)
	}
	protocols := tempo["distributor"].(map[string]any)["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)
	for _, p := range []string{"grpc", "http"} {
		if _, ok := protocols[p]; !ok {
			t.Errorf("tempo has no OTLP %s receiver", p)
		}
	}
}

func TestFusionPodsAreHardened(t *testing.T) {
	r := fusionRender(t, "f")
	for name, s := range r.sets {
		spec := s.Spec.Template.Spec
		if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
			t.Errorf("%s mounts a service-account token: no store needs the Kubernetes API", name)
		}
		if sc := spec.SecurityContext; sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.SeccompProfile == nil {
			t.Errorf("%s pod securityContext = %+v", name, sc)
		}
		c := spec.Containers[0]
		sc := c.SecurityContext
		if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("%s container securityContext = %+v", name, sc)
		}
		if sc != nil && (sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL") {
			t.Errorf("%s does not drop every capability", name)
		}
		if c.ReadinessProbe == nil || c.LivenessProbe == nil {
			t.Errorf("%s has no probes", name)
		}
	}
	for name, a := range r.accounts {
		if a.AutomountServiceAccountToken == nil || *a.AutomountServiceAccountToken {
			t.Errorf("service account %s automounts its token", name)
		}
	}
}

func TestFusionVolumesAreDurableUnlessTurnedOff(t *testing.T) {
	r := fusionRender(t, "f", "--set", "persistence.storageClass=fast", "--set", "loki.storage=50Gi")
	for name, s := range r.sets {
		if len(s.Spec.VolumeClaimTemplates) != 1 {
			t.Errorf("%s has %d volume claims, want 1", name, len(s.Spec.VolumeClaimTemplates))
			continue
		}
		if sc := s.Spec.VolumeClaimTemplates[0].Spec.StorageClassName; sc == nil || *sc != "fast" {
			t.Errorf("%s storage class = %v", name, sc)
		}
	}
	size := r.sets["f-fusion-loki"].Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	if size.String() != "50Gi" {
		t.Errorf("loki volume = %s, want 50Gi", size.String())
	}

	r = fusionRender(t, "f", "--set", "persistence.enabled=false")
	for name, s := range r.sets {
		if len(s.Spec.VolumeClaimTemplates) != 0 {
			t.Errorf("%s still claims a volume with persistence off", name)
		}
		var ephemeral bool
		for _, v := range s.Spec.Template.Spec.Volumes {
			if v.Name == "data" && v.EmptyDir != nil {
				ephemeral = true
			}
		}
		if !ephemeral {
			t.Errorf("%s has no emptyDir for its data with persistence off", name)
		}
	}
}

func TestFusionStoresCanBeLeftOutButNotAll(t *testing.T) {
	r := fusionRender(t, "f", "--set", "loki.enabled=false")
	if _, ok := r.sets["f-fusion-loki"]; ok {
		t.Error("loki is rendered though disabled")
	}
	if _, ok := r.services["f-fusion-loki"]; ok {
		t.Error("loki's Service is rendered though disabled")
	}
	if _, ok := r.sets["f-fusion-tempo"]; !ok {
		t.Error("tempo vanished with loki")
	}
	if out, err := fusionTemplate(t, "f", "--set", "prometheus.enabled=false", "--set", "loki.enabled=false", "--set", "tempo.enabled=false"); err == nil || !strings.Contains(out, "enable at least one") {
		t.Errorf("an empty FUSION must be refused in words: %v\n%s", err, out)
	}
}

func TestFusionRefusesRetentionsTheStoresCannotApply(t *testing.T) {
	// Some are caught by the schema (a unit the store cannot read), some by the template (a whole number of
	// days, which the schema cannot express). Either way the refusal must name the setting.
	for _, tc := range []struct {
		name, set string
		want      []string
	}{
		{"prometheus", "prometheus.retention=forever", []string{"/prometheus/retention", "prometheus.retention"}},
		{"loki in days", "loki.retention=7d", []string{"/loki/retention", "loki.retention"}},
		{"loki not whole days", "loki.retention=36h", []string{"whole number of days"}},
		{"loki under a day", "loki.retention=12h", []string{"whole number of days"}},
		{"tempo in days", "tempo.retention=3d", []string{"/tempo/retention", "tempo.retention"}},
	} {
		out, err := fusionTemplate(t, "f", "--set", tc.set)
		if err == nil {
			t.Errorf("%s: --set %s was accepted", tc.name, tc.set)
			continue
		}
		var named bool
		for _, w := range tc.want {
			named = named || strings.Contains(out, w)
		}
		if !named {
			t.Errorf("%s: the refusal does not name the setting (wanted one of %v):\n%s", tc.name, tc.want, out)
		}
	}
}

// The stores authenticate nothing, so isolating them is on by default: the other pods of the release and the regional
// operators may reach them, nobody else. Grafana, which trusts the server's say-so about who is signed in, gets a policy of
// its own that only this release's pods pass - the regional operators do not.
func TestFusionNetworkPolicyIsOnByDefaultAndAdmitsTheOperator(t *testing.T) {
	r := fusionRender(t, "f")
	p, ok := r.policies["f-fusion"]
	if !ok {
		t.Fatal("no NetworkPolicy by default")
	}
	from := p.Spec.Ingress[0].From
	if len(from) != 2 || from[0].PodSelector == nil || from[1].PodSelector == nil || from[1].NamespaceSelector == nil ||
		from[1].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-regional-operator" {
		t.Errorf("default ingress sources = %+v, want the release's own pods and the regional operators", from)
	}
	g, ok := r.policies["f-fusion-grafana"]
	if !ok {
		t.Fatal("no NetworkPolicy for Grafana")
	}
	if len(g.Spec.Ingress[0].From) != 1 || g.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "grafana" {
		t.Errorf("Grafana policy = %+v, want only this release's pods", g.Spec)
	}
	if r := fusionRender(t, "f", "--set", "networkPolicy.enabled=false"); len(r.policies) != 0 {
		t.Errorf("networkPolicy.enabled=false still renders: %v", mapKeys(r.policies))
	}
	r = fusionRender(t, "f", "--set", "networkPolicy.enabled=true", "--set-json", `networkPolicy.allowedIngress=[{"namespaceSelector":{"matchLabels":{"team":"ops"}}}]`)
	p, ok = r.policies["f-fusion"]
	if !ok {
		t.Fatal("no NetworkPolicy")
	}
	if len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("policy types = %v, want Ingress only", p.Spec.PolicyTypes)
	}
	from = p.Spec.Ingress[0].From
	if len(from) != 2 || from[0].PodSelector == nil || from[1].NamespaceSelector == nil {
		t.Errorf("ingress sources = %+v, want the release's own pods and the allowed namespace", from)
	}
}

func TestFusionSchemaCatchesTypos(t *testing.T) {
	for _, set := range []string{"promethus.enabled=true", "loki.retenton=48h", "persistence.size=1Gi"} {
		if out, err := fusionTemplate(t, "f", "--set", set); err == nil {
			t.Errorf("--set %s was accepted:\n%s", set, out)
		}
	}
}

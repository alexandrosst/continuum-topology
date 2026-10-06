package chart

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// The server chart ships FUSION as a dependency and gives the server a Role to switch it. The Role names the
// FUSION workloads by the same rule the subchart names them, and nothing but this test notices if the two drift.

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

type serverRender struct {
	roles       map[string]rbacv1.Role
	bindings    map[string]rbacv1.RoleBinding
	deployments map[string]appsv1.Deployment
	sets        map[string]appsv1.StatefulSet
	policies    map[string]networkingv1.NetworkPolicy
	services    map[string]corev1.Service
}

// serverRenderWith renders deploy/helm/continuum-server with the FUSION subchart in place, as `helm package -u` would
// assemble it, so the test does not write into the source tree.
func serverRenderWith(t *testing.T, release string, extra ...string) serverRender {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), "continuum-server")
	copyTree(t, filepath.Join("..", "..", "..", "deploy", "helm", "continuum-server"), dir)
	copyTree(t, "continuum-fusion", filepath.Join(dir, "charts", "continuum-fusion"))
	args := append([]string{"template", release, dir, "-n", "continuum",
		"--set", "agent.publicAddress=192.0.2.10:30443", "--set", "agent.service.type=NodePort", "--set", "admin.behindTlsProxy=true"}, extra...)
	out, err := exec.Command(h, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", extra, err, out)
	}
	r := serverRender{map[string]rbacv1.Role{}, map[string]rbacv1.RoleBinding{}, map[string]appsv1.Deployment{}, map[string]appsv1.StatefulSet{}, map[string]networkingv1.NetworkPolicy{}, map[string]corev1.Service{}}
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(out)), 4096)
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
		if json.Unmarshal(raw, &meta) != nil {
			continue
		}
		n := meta.Metadata.Name
		switch meta.Kind {
		case "Role":
			var v rbacv1.Role
			json.Unmarshal(raw, &v)
			r.roles[n] = v
		case "RoleBinding":
			var v rbacv1.RoleBinding
			json.Unmarshal(raw, &v)
			r.bindings[n] = v
		case "Deployment":
			var v appsv1.Deployment
			json.Unmarshal(raw, &v)
			r.deployments[n] = v
		case "StatefulSet":
			var v appsv1.StatefulSet
			json.Unmarshal(raw, &v)
			r.sets[n] = v
		case "NetworkPolicy":
			var v networkingv1.NetworkPolicy
			json.Unmarshal(raw, &v)
			r.policies[n] = v
		case "Service":
			var v corev1.Service
			json.Unmarshal(raw, &v)
			r.services[n] = v
		}
	}
	return r
}

func fusionWorkloads(r serverRender, release string) (sets, deploys []string) {
	for n, v := range r.sets {
		if v.Labels["app.kubernetes.io/name"] == "continuum-fusion" {
			sets = append(sets, n)
		}
	}
	for n, v := range r.deployments {
		if v.Labels["app.kubernetes.io/name"] == "continuum-fusion" {
			deploys = append(deploys, n)
		}
	}
	sort.Strings(sets)
	sort.Strings(deploys)
	return
}

func TestServerChartBundlesFusionStandingBy(t *testing.T) {
	r := serverRenderWith(t, "continuum")
	sets, deploys := fusionWorkloads(r, "continuum")
	if strings.Join(sets, ",") != "continuum-fusion-grafana,continuum-fusion-loki,continuum-fusion-prometheus,continuum-fusion-tempo" || strings.Join(deploys, ",") != "continuum-fusion-central" {
		t.Fatalf("FUSION workloads = %v %v", sets, deploys)
	}
	for _, n := range sets {
		if *r.sets[n].Spec.Replicas != 0 {
			t.Errorf("%s replicas = %d, want 0 (standing by until the UI turns FUSION on)", n, *r.sets[n].Spec.Replicas)
		}
	}
	if *r.deployments["continuum-fusion-central"].Spec.Replicas != 0 {
		t.Error("the central gateway is not standing by")
	}
}

// Whatever the release is called, the Role covers exactly the workloads and Secret the subchart rendered, and
// nothing else.
func TestServerFusionRoleCoversExactlyWhatTheSubchartNames(t *testing.T) {
	for _, release := range []string{"continuum", "prod", "my-fusion", "relname-relname-relname-relname-relname-relname-abcde"} {
		r := serverRenderWith(t, release)
		sets, deploys := fusionWorkloads(r, release)
		if len(sets) != 4 || len(deploys) != 1 { // Prometheus, Loki, Tempo and Grafana, and the gateway
			t.Fatalf("release %q: workloads %v %v", release, sets, deploys)
		}
		var role rbacv1.Role
		for n, v := range r.roles {
			if strings.HasSuffix(n, "-fusion-switch") {
				role = v
			}
		}
		if len(role.Rules) == 0 {
			t.Fatalf("release %q: no fusion-switch Role", release)
		}
		got := map[string][]string{}
		for _, rule := range role.Rules {
			if len(rule.ResourceNames) == 0 {
				t.Errorf("release %q: a rule without resourceNames: %+v", release, rule)
			}
			for _, v := range rule.Verbs {
				if v != "get" && v != "patch" {
					t.Errorf("release %q: verb %q is more than the switch needs (%+v)", release, v, rule)
				}
			}
			for _, res := range rule.Resources {
				if res == "secrets" && strings.Join(rule.Verbs, ",") != "patch" {
					t.Errorf("release %q: the Role can read a Secret: %+v", release, rule)
				}
				got[res] = append(got[res], rule.ResourceNames...)
			}
		}
		for _, res := range []string{"statefulsets", "statefulsets/scale"} {
			names := append([]string{}, got[res]...)
			sort.Strings(names)
			if strings.Join(names, ",") != strings.Join(sets, ",") {
				t.Errorf("release %q: Role %s = %v, subchart renders %v", release, res, names, sets)
			}
		}
		for _, res := range []string{"deployments", "deployments/scale"} {
			if strings.Join(got[res], ",") != strings.Join(deploys, ",") {
				t.Errorf("release %q: Role %s = %v, subchart renders %v", release, res, got[res], deploys)
			}
		}
		if want := deploys[0] + "-receiver-tls"; len(got["secrets"]) != 1 || got["secrets"][0] != want {
			t.Errorf("release %q: Role secrets = %v, want [%s]", release, got["secrets"], want)
		}

		// What the server reads to say why a store is not starting: each StatefulSet's one pod, and the volume claim its
		// template makes for it (<template>-<pod>) - the names the server's code derives (storePod, storeClaim) - and
		// nothing else. Read-only.
		var pods, claims []string
		for _, n := range sets {
			pod := n + "-0"
			pods = append(pods, pod)
			for _, vct := range r.sets[n].Spec.VolumeClaimTemplates {
				claims = append(claims, vct.Name+"-"+pod)
			}
		}
		for res, want := range map[string][]string{"pods": pods, "persistentvolumeclaims": claims, "services": {deploys[0]}} {
			names := append([]string{}, got[res]...)
			sort.Strings(names)
			sort.Strings(want)
			if strings.Join(names, ",") != strings.Join(want, ",") {
				t.Errorf("release %q: Role %s = %v, the subchart renders %v", release, res, names, want)
			}
			for _, rule := range role.Rules {
				for _, rr := range rule.Resources {
					if rr == res && strings.Join(rule.Verbs, ",") != "get" {
						t.Errorf("release %q: %s can be more than read: %+v", release, res, rule)
					}
				}
			}
		}
		// The service the Role names is the one the subchart makes for the gateway, and carries the OTLP port the server looks for.
		svc, ok := r.services[deploys[0]]
		if !ok {
			t.Errorf("release %q: no Service %s", release, deploys[0])
		}
		has4317 := false
		for _, p := range svc.Spec.Ports {
			has4317 = has4317 || p.Port == 4317
		}
		if !has4317 {
			t.Errorf("release %q: the gateway's Service has no port 4317: %+v", release, svc.Spec.Ports)
		}
	}
}

func serverContainer(t *testing.T, r serverRender) (args []string, automount *bool) {
	t.Helper()
	for _, d := range r.deployments {
		if d.Labels["app.kubernetes.io/name"] == "continuum-fusion" || d.Labels["app.kubernetes.io/component"] != "server" {
			continue
		}
		return d.Spec.Template.Spec.Containers[0].Args, d.Spec.Template.Spec.AutomountServiceAccountToken
	}
	t.Fatal("no server Deployment")
	return
}

func TestServerGetsATokenAndTheFusionNameOnlyWhenItControlsFusion(t *testing.T) {
	r := serverRenderWith(t, "continuum")
	args, auto := serverContainer(t, r)
	if auto == nil || !*auto {
		t.Error("the server pod gets no API token, so it cannot switch FUSION")
	}
	if !containsStr(args, "--fusion-name=continuum-fusion") {
		t.Errorf("args = %v, want --fusion-name", args)
	}
	if len(r.bindings) == 0 {
		t.Error("no RoleBinding")
	}

	for _, off := range [][]string{{"--set", "fusionControl.enabled=false"}, {"--set", "fusion.enabled=false"}} {
		r = serverRenderWith(t, "continuum", off...)
		args, auto = serverContainer(t, r)
		if auto != nil && *auto {
			t.Errorf("%v: the pod still gets an API token", off)
		}
		// The name is passed whenever FUSION is bundled: the shared data API reads the stores by it, with or without the
		// switch. Only without the bundled FUSION is there nothing to name.
		hasName := false
		for _, a := range args {
			hasName = hasName || strings.HasPrefix(a, "--fusion-name")
		}
		if want := off[1] == "fusionControl.enabled=false"; hasName != want {
			t.Errorf("%v: --fusion-name passed = %v, want %v", off, hasName, want)
		}
		for n := range r.roles {
			if strings.HasSuffix(n, "-fusion-switch") {
				t.Errorf("%v: Role %s is rendered", off, n)
			}
		}
	}
	// Without the bundled FUSION nothing of it is rendered at all.
	r = serverRenderWith(t, "continuum", "--set", "fusion.enabled=false")
	if sets, deploys := fusionWorkloads(r, "continuum"); len(sets)+len(deploys) != 0 {
		t.Errorf("FUSION workloads rendered though fusion.enabled=false: %v %v", sets, deploys)
	}
}

// The shared data API reads the three stores from the server pod, so a server under its own egress policy must be
// allowed to reach them - and only them, never the central gateway.
func TestServerEgressPolicyReachesTheFusionStoresOnly(t *testing.T) {
	r := serverRenderWith(t, "continuum", "--set", "networkPolicy.enabled=true", "--set", "networkPolicy.egress.enabled=true")
	var found bool
	for _, p := range r.policies {
		if p.Labels["app.kubernetes.io/component"] != "server" {
			continue
		}
		for _, e := range p.Spec.Egress {
			if len(e.To) != 1 || e.To[0].PodSelector == nil || e.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-fusion" {
				continue
			}
			found = true
			var ports []int32
			for _, pt := range e.Ports {
				ports = append(ports, pt.Port.IntVal)
			}
			if len(ports) != 4 || ports[0] != 9090 || ports[1] != 3100 || ports[2] != 3200 || ports[3] != 3000 {
				t.Errorf("ports = %v, want the Prometheus, Loki, Tempo and Grafana ports", ports)
			}
			ex := e.To[0].PodSelector.MatchExpressions
			if len(ex) != 1 || strings.Join(ex[0].Values, ",") != "prometheus,loki,tempo,grafana" {
				t.Errorf("the rule is not limited to the stores: %+v", ex)
			}
		}
	}
	if !found {
		t.Error("the server's egress policy has no rule to FUSION's stores")
	}
	r = serverRenderWith(t, "continuum", "--set", "networkPolicy.enabled=true", "--set", "networkPolicy.egress.enabled=true", "--set", "fusion.enabled=false")
	for _, p := range r.policies {
		for _, e := range p.Spec.Egress {
			if len(e.To) == 1 && e.To[0].PodSelector != nil && e.To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] == "continuum-fusion" {
				t.Error("a rule to FUSION's stores without FUSION")
			}
		}
	}
}

// Without Grafana the Role has no grant for its pod or claim.
func TestServerFusionRoleLeavesOutGrafanaWhenItIsOff(t *testing.T) {
	r := serverRenderWith(t, "continuum", "--set", "fusion.grafana.enabled=false")
	for n, role := range r.roles {
		if !strings.HasSuffix(n, "-fusion-switch") {
			continue
		}
		for _, rule := range role.Rules {
			for _, name := range rule.ResourceNames {
				if strings.Contains(name, "grafana") {
					t.Errorf("Grafana is off but the Role names %s (%+v)", name, rule)
				}
			}
		}
	}
}

// The stores authenticate nothing: their NetworkPolicy is on by default in the server chart, and turning it off is said
// out loud in the install notes.
func TestServerChartKeepsTheStoresNetworkPolicyOnAndSaysWhenItIsOff(t *testing.T) {
	r := serverRenderWith(t, "continuum")
	if _, ok := r.policies["continuum-fusion"]; !ok {
		t.Fatalf("the stores have no NetworkPolicy by default: %v", policyNames(r.policies))
	}
	r = serverRenderWith(t, "continuum", "--set", "fusion.networkPolicy.enabled=false")
	if _, ok := r.policies["continuum-fusion"]; ok {
		t.Error("the stores' NetworkPolicy is rendered though it was turned off")
	}

	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	// `helm template` does not print NOTES.txt (and `helm install --dry-run` wants a cluster), so the notes are rendered
	// through a ConfigMap: the same text, the same values.
	dir := filepath.Join(t.TempDir(), "continuum-server")
	copyTree(t, filepath.Join("..", "..", "..", "deploy", "helm", "continuum-server"), dir)
	copyTree(t, "continuum-fusion", filepath.Join(dir, "charts", "continuum-fusion"))
	text, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wrap := map[string]string{
		"_notes.tpl": `{{- define "test.notes" -}}` + string(text) + `{{- end -}}`,
		"notes.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: {{ include \"test.notes\" . | quote }}\n",
	}
	for name, body := range wrap {
		if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notes := func(extra ...string) string {
		args := append([]string{"template", "continuum", dir, "-n", "continuum", "--show-only", "templates/notes.yaml", "--set", "agent.publicAddress=192.0.2.10:30443",
			"--set", "agent.service.type=NodePort", "--set", "admin.behindTlsProxy=true"}, extra...)
		out, err := exec.Command(h, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template of the notes: %v\n%s", err, out)
		}
		return string(out)
	}
	const warning = "fusion.networkPolicy.enabled is false"
	if out := notes(); strings.Contains(out, warning) {
		t.Error("the default install warns about the stores' NetworkPolicy")
	}
	if out := notes("--set", "fusion.networkPolicy.enabled=false"); !strings.Contains(out, warning) {
		t.Errorf("no warning with the stores' NetworkPolicy off:\n%s", out)
	}
	if out := notes("--set", "fusion.networkPolicy.enabled=false", "--set", "fusion.enabled=false"); strings.Contains(out, warning) {
		t.Error("a warning about stores that are not installed")
	}
}

func policyNames[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// admin.publicURL reaches the server as CONTINUUM_PUBLIC_URL (what the heartbeat address of an operator's install command is
// built from), and only when it is set; a value that is not an http(s) URL is refused by the schema.
func TestServerPassesItsPublicURLOnlyWhenOneIsGiven(t *testing.T) {
	publicEnv := func(r serverRender) string {
		for _, d := range r.deployments {
			if d.Labels["app.kubernetes.io/name"] == "continuum-fusion" || d.Labels["app.kubernetes.io/component"] != "server" {
				continue
			}
			for _, e := range d.Spec.Template.Spec.Containers[0].Env {
				if e.Name == "CONTINUUM_PUBLIC_URL" {
					return e.Value
				}
			}
		}
		return ""
	}
	if got := publicEnv(serverRenderWith(t, "continuum")); got != "" {
		t.Errorf("CONTINUUM_PUBLIC_URL = %q with none given", got)
	}
	if got := publicEnv(serverRenderWith(t, "continuum", "--set", "admin.publicURL=https://ikhnos.example.com/ui")); got != "https://ikhnos.example.com/ui" {
		t.Errorf("CONTINUUM_PUBLIC_URL = %q", got)
	}
}

// A private CA for the server's own certificate reaches the server only when the administrator names the mounted file.
func TestServerPassesTheHeartbeatCAFileOnlyWhenSet(t *testing.T) {
	has := func(r serverRender) (string, bool) {
		for _, d := range r.deployments {
			if d.Labels["app.kubernetes.io/component"] != "server" {
				continue
			}
			for _, e := range d.Spec.Template.Spec.Containers[0].Env {
				if e.Name == "CONTINUUM_HEARTBEAT_CA_FILE" {
					return e.Value, true
				}
			}
		}
		return "", false
	}
	if _, ok := has(serverRenderWith(t, "continuum")); ok {
		t.Error("the CA file is passed with nothing configured")
	}
	if v, ok := has(serverRenderWith(t, "continuum", "--set", "admin.heartbeatCAFile=/etc/continuum/hb-ca/ca.crt")); !ok || v != "/etc/continuum/hb-ca/ca.crt" {
		t.Errorf("CONTINUUM_HEARTBEAT_CA_FILE = %q, %v", v, ok)
	}
}

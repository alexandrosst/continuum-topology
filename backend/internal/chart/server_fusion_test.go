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
	r := serverRender{map[string]rbacv1.Role{}, map[string]rbacv1.RoleBinding{}, map[string]appsv1.Deployment{}, map[string]appsv1.StatefulSet{}, map[string]networkingv1.NetworkPolicy{}}
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
	if strings.Join(sets, ",") != "continuum-fusion-loki,continuum-fusion-prometheus,continuum-fusion-tempo" || strings.Join(deploys, ",") != "continuum-fusion-central" {
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
		if len(sets) != 3 || len(deploys) != 1 {
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
			if len(ports) != 3 || ports[0] != 9090 || ports[1] != 3100 || ports[2] != 3200 {
				t.Errorf("ports = %v, want the Prometheus, Loki and Tempo query ports", ports)
			}
			ex := e.To[0].PodSelector.MatchExpressions
			if len(ex) != 1 || strings.Join(ex[0].Values, ",") != "prometheus,loki,tempo" {
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

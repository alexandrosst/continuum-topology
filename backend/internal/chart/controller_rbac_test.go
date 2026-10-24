package chart

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"

	"ikhnos/internal/controller"
	"ikhnos/internal/install"
)

// The controller installs the agent chart as a Helm release, and Kubernetes lets nobody create a Role or a ClusterRole that grants
// what they do not hold. So the controller's permissions have to be, at once, enough to create every object the agent chart makes
// (and to hold what the Roles among them grant), and no more than that. These tests render both charts and hold the one to the other,
// for every choice an IkhnosAgent can make: a new object in the agent chart that the controller cannot create, or a permission the
// controller holds that nothing asks for, fails here and not in a cluster.

const ctlNamespace = "ikhnos-system"

// object is one rendered Kubernetes object, as far as these tests need it.
type object struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Rules    []rbacv1.PolicyRule `json:"rules"`
	RoleRef  rbacv1.RoleRef      `json:"roleRef"`
	Subjects []rbacv1.Subject    `json:"subjects"`
}

func (o object) String() string {
	return fmt.Sprintf("%s %s/%s", o.Kind, o.Metadata.Namespace, o.Metadata.Name)
}

func helmObjects(t *testing.T, chartPath string, args ...string) []object {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	all := append([]string{"template", "ct", chartPath, "--namespace", ctlNamespace}, args...)
	out, err := exec.Command(h, all...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", all, err, out)
	}
	var objs []object
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(out)), 4096)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var o object
		if json.Unmarshal(raw, &o) != nil || o.Kind == "" {
			continue
		}
		objs = append(objs, o)
	}
	return objs
}

func controllerChart() string {
	return filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-controller")
}

// choice is one thing an IkhnosAgent can ask for.
type choice struct {
	tier                            int
	nodeProbe, flow, measure, purge bool
	// telemetry turns on every signal the resource can, so that every object and permission telemetry makes is in the render.
	telemetry bool
}

func (c choice) String() string {
	return fmt.Sprintf("tier %d probe=%v flow=%v measure=%v purge=%v telemetry=%v", c.tier, c.nodeProbe, c.flow, c.measure, c.purge, c.telemetry)
}

// everyTelemetry is a resource's telemetry with every signal the choice can have on: cluster links need the traffic observer's tier,
// and network latency the measurements.
func everyTelemetry(c choice) *install.ResourceTelemetry {
	return &install.ResourceTelemetry{
		Signals: install.ResourceSignals{ResourceUsage: true, Energy: true, KubernetesState: true, NodeRuntime: true, NetworkLatency: c.measure, ClusterLinks: c.tier >= 2,
			ApplicationMetrics: true, SystemLogs: true, KubernetesEvents: true, ApplicationLogs: true, Traces: true, Accelerators: true},
		ClusterLinks: &install.ResourceClusterLinks{Probe: true, Throughput: true},
		Destination:  &install.ResourceDestination{Endpoint: "otel.example.com:4317", AuthSecret: "otel-token"},
	}
}

// agentObjects renders the agent chart the way the controller would install it for these choices. The telemetry ones are rendered from
// the values a resource makes (controller.Spec.Values), so that the test holds the controller to what it really installs.
func agentObjects(t *testing.T, c choice) []object {
	t.Helper()
	b, err := Package()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tgz := filepath.Join(dir, Filename())
	if err := writeFile(tgz, b); err != nil {
		t.Fatal(err)
	}
	if c.telemetry {
		spec := controller.Spec{
			Server: controller.ServerSpec{Address: "a.example:8443", CAPin: "sha256/ab"}, AccessTier: &c.tier,
			Collectors:            install.Collectors{NodeProbe: c.nodeProbe, FlowObserver: c.flow, Measurements: c.measure},
			PurgeIdentityOnDelete: c.purge, Telemetry: everyTelemetry(c),
		}
		vals, err := spec.Values("t")
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		j, err := json.Marshal(vals)
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, "values.json")
		if err := writeFile(f, j); err != nil {
			t.Fatal(err)
		}
		return helmObjects(t, tgz, "-f", f)
	}
	args := append([]string{}, baseSet...)
	args = append(args, "--set", fmt.Sprintf("access.tier=%d", c.tier))
	for _, k := range []struct {
		on  bool
		key string
	}{{c.nodeProbe, "nodeProbe.enabled"}, {c.flow, "flowObserver.enabled"}, {c.measure, "measurements.enabled"}, {c.purge, "identity.purgeOnUninstall"}} {
		if k.on {
			args = append(args, "--set", k.key+"=true")
		}
	}
	return helmObjects(t, tgz, args...)
}

// choices is every combination of choices an IkhnosAgent can make that the chart accepts, at each tier, and the telemetry ones.
func choices() (out []choice) {
	for tier := 0; tier <= 2; tier++ {
		for mask := 0; mask < 16; mask++ {
			np, fl, me, pu := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
			if np && tier < 1 || fl && tier < 2 || me && tier < 2 {
				continue // the resource refuses these (install.AgentChoices)
			}
			out = append(out, choice{tier: tier, nodeProbe: np, flow: fl, measure: me, purge: pu})
		}
		// Telemetry: with none of the collectors, and with all of those the tier allows.
		out = append(out, choice{tier: tier, telemetry: true})
		out = append(out, choice{tier: tier, nodeProbe: tier >= 1, flow: tier >= 2, measure: tier >= 2, purge: true, telemetry: true})
	}
	return out
}

func telemetryChoices() (out []choice) {
	for _, c := range choices() {
		if c.telemetry {
			out = append(out, c)
		}
	}
	return out
}

// ---- what a set of rules allows ----

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x || v == "*" {
			return true
		}
	}
	return false
}

// allows says whether one rule lets its holder do verb on resource (in group) to the object called name; an empty name is "any".
func allows(r rbacv1.PolicyRule, group, resource, verb, name string) bool {
	if !has(r.APIGroups, group) || !has(r.Resources, resource) || !has(r.Verbs, verb) {
		return false
	}
	if len(r.ResourceNames) == 0 {
		return true
	}
	return name != "" && has(r.ResourceNames, name)
}

func anyAllows(rules []rbacv1.PolicyRule, group, resource, verb, name string) bool {
	for _, r := range rules {
		if allows(r, group, resource, verb, name) {
			return true
		}
	}
	return false
}

// missing lists what the target rule grants that the holder's rules do not: Kubernetes' own test of whether a Role may be created.
func missing(holder []rbacv1.PolicyRule, target rbacv1.PolicyRule) []string {
	var out []string
	names := target.ResourceNames
	if len(names) == 0 {
		names = []string{""}
	}
	for _, g := range target.APIGroups {
		for _, res := range target.Resources {
			for _, v := range target.Verbs {
				for _, n := range names {
					if !anyAllows(holder, g, res, v, n) {
						out = append(out, fmt.Sprintf("%s %s/%s %s", v, g, res, n))
					}
				}
			}
		}
	}
	return out
}

// ---- what the controller chart gives the controller ----

type perms struct {
	cluster []rbacv1.PolicyRule
	ns      map[string][]rbacv1.PolicyRule
}

func (p perms) in(namespace string) []rbacv1.PolicyRule {
	if namespace == "" {
		return p.cluster
	}
	return append(append([]rbacv1.PolicyRule{}, p.cluster...), p.ns[namespace]...)
}

func controllerPerms(t *testing.T, objs []object) perms {
	t.Helper()
	p := perms{ns: map[string][]rbacv1.PolicyRule{}}
	roles := map[string]object{}
	for _, o := range objs {
		switch o.Kind {
		case "Role":
			roles["Role/"+o.Metadata.Namespace+"/"+o.Metadata.Name] = o
		case "ClusterRole":
			roles["ClusterRole//"+o.Metadata.Name] = o
		}
	}
	isController := func(o object) bool {
		for _, s := range o.Subjects {
			if s.Kind == "ServiceAccount" && s.Name == "ikhnos-controller" && s.Namespace == ctlNamespace {
				return true
			}
		}
		return false
	}
	for _, o := range objs {
		if !isController(o) {
			continue
		}
		switch o.Kind {
		case "ClusterRoleBinding":
			r, ok := roles["ClusterRole//"+o.RoleRef.Name]
			if !ok {
				t.Fatalf("%s binds a ClusterRole this chart does not render", o)
			}
			p.cluster = append(p.cluster, r.Rules...)
		case "RoleBinding":
			key := "Role/" + o.Metadata.Namespace + "/" + o.RoleRef.Name
			if o.RoleRef.Kind == "ClusterRole" {
				key = "ClusterRole//" + o.RoleRef.Name
			}
			r, ok := roles[key]
			if !ok {
				t.Fatalf("%s binds a role this chart does not render (%s)", o, key)
			}
			p.ns[o.Metadata.Namespace] = append(p.ns[o.Metadata.Namespace], r.Rules...)
		}
	}
	return p
}

// resourceOf is the resource a rendered kind is stored as. A kind the agent chart starts to make that is not here fails the tests, so
// that its permission is decided and not forgotten.
var resourceOf = map[string][2]string{
	"v1/ServiceAccount":                               {"", "serviceaccounts"},
	"v1/Secret":                                       {"", "secrets"},
	"v1/Service":                                      {"", "services"},
	"v1/ConfigMap":                                    {"", "configmaps"},
	"networking.k8s.io/v1/NetworkPolicy":              {"networking.k8s.io", "networkpolicies"},
	"apps/v1/Deployment":                              {"apps", "deployments"},
	"apps/v1/DaemonSet":                               {"apps", "daemonsets"},
	"batch/v1/Job":                                    {"batch", "jobs"},
	"rbac.authorization.k8s.io/v1/Role":               {"rbac.authorization.k8s.io", "roles"},
	"rbac.authorization.k8s.io/v1/RoleBinding":        {"rbac.authorization.k8s.io", "rolebindings"},
	"rbac.authorization.k8s.io/v1/ClusterRole":        {"rbac.authorization.k8s.io", "clusterroles"},
	"rbac.authorization.k8s.io/v1/ClusterRoleBinding": {"rbac.authorization.k8s.io", "clusterrolebindings"},
}

func clusterScoped(kind string) bool { return kind == "ClusterRole" || kind == "ClusterRoleBinding" }

func (o object) namespace() string {
	if clusterScoped(o.Kind) {
		return ""
	}
	if o.Metadata.Namespace != "" {
		return o.Metadata.Namespace
	}
	return ctlNamespace
}

// ---- the tests ----

func TestTheControllerCanCreateChangeAndRemoveEveryObjectTheAgentChartMakes(t *testing.T) {
	ctl := controllerPerms(t, helmObjects(t, controllerChart(), "--set", "maxAccessTier=2", "--set", "allowTelemetry=true"))
	for _, c := range choices() {
		for _, o := range agentObjects(t, c) {
			res, ok := resourceOf[o.APIVersion+"/"+o.Kind]
			if !ok {
				t.Fatalf("the agent chart makes a %s/%s, and the controller's permissions do not say what to do about that kind: decide in templates/rbac.yaml, then name it in resourceOf", o.APIVersion, o.Kind)
			}
			rules := ctl.in(o.namespace())
			verbs := []string{"get", "create", "update", "patch", "delete"}
			if o.Kind == "Job" {
				verbs = append(verbs, "list", "watch") // a hook, which Helm watches until it is done
			}
			for _, v := range verbs {
				if !anyAllows(rules, res[0], res[1], v, o.Metadata.Name) {
					t.Errorf("%s: the controller cannot %s %s", c, v, o)
				}
			}
		}
	}
}

func TestTheControllerHoldsEverythingTheAgentsRolesGrantSoKubernetesLetsItCreateThem(t *testing.T) {
	ctl := controllerPerms(t, helmObjects(t, controllerChart(), "--set", "maxAccessTier=2", "--set", "allowTelemetry=true"))
	for _, c := range choices() {
		for _, o := range agentObjects(t, c) {
			if o.Kind != "Role" && o.Kind != "ClusterRole" {
				continue
			}
			held := ctl.in(o.namespace())
			for _, r := range o.Rules {
				if m := missing(held, r); len(m) > 0 {
					t.Errorf("%s: %s grants what the controller does not hold, so Kubernetes would refuse to create it: %v", c, o, m)
				}
			}
		}
	}
}

// The ceiling is real: a controller given tier c holds nothing that only a higher tier's ClusterRoles need, and cannot create them.
func TestACeilingBelowTheTopLeavesTheControllerUnableToCreateTheTiersAboveIt(t *testing.T) {
	for ceiling := 0; ceiling <= 2; ceiling++ {
		ctl := controllerPerms(t, helmObjects(t, controllerChart(), "--set", fmt.Sprintf("maxAccessTier=%d", ceiling)))
		for tier := 0; tier <= 2; tier++ {
			var unheld []string
			for _, o := range agentObjects(t, choice{tier: tier}) {
				if o.Kind != "ClusterRole" {
					continue
				}
				for _, r := range o.Rules {
					unheld = append(unheld, missing(ctl.cluster, r)...)
				}
			}
			if tier <= ceiling && len(unheld) > 0 {
				t.Errorf("ceiling %d, agent tier %d: the controller lacks %v", ceiling, tier, unheld)
			}
			if tier > ceiling && len(unheld) == 0 {
				t.Errorf("ceiling %d, agent tier %d: the controller holds all of it, so the ceiling limits nothing", ceiling, tier)
			}
		}
	}
}

// Nothing is held that nothing asks for: every permission the controller has is for a kind the agent chart renders, for what its roles
// grant, or one of the two things only the controller does (its own resource, and the Secrets Helm keeps releases in).
func TestTheControllerHoldsNothingThatNoRenderedObjectOrRoleAsksFor(t *testing.T) {
	var agentRules []rbacv1.PolicyRule // everything the agent's roles grant, over every choice
	kinds := map[string]bool{}         // group/resource of every rendered kind
	names := map[string]map[string]bool{}
	for _, c := range choices() {
		for _, o := range agentObjects(t, c) {
			res := resourceOf[o.APIVersion+"/"+o.Kind]
			kinds[res[0]+"/"+res[1]] = true
			if names[res[1]] == nil {
				names[res[1]] = map[string]bool{}
			}
			names[res[1]][o.Metadata.Name] = true
			agentRules = append(agentRules, o.Rules...)
		}
	}
	onlyTheControllers := map[string]bool{"ikhnos.io/ikhnosagents": true, "ikhnos.io/ikhnosagents/status": true}

	for _, withTelemetry := range []bool{false, true} {
		for ceiling := 0; ceiling <= 2; ceiling++ {
			objs := helmObjects(t, controllerChart(), "--set", fmt.Sprintf("maxAccessTier=%d", ceiling), "--set", fmt.Sprintf("allowTelemetry=%v", withTelemetry))
			for _, o := range objs {
				if o.Kind != "Role" && o.Kind != "ClusterRole" {
					continue
				}
				for _, r := range o.Rules {
					// A rule limited to names names only objects that are made, of one of the kinds it is for.
					for _, n := range r.ResourceNames {
						made, objectsMade := false, false
						for _, g := range r.APIGroups {
							for _, res := range r.Resources {
								made = made || names[res][n]
								objectsMade = objectsMade || kinds[g+"/"+res]
							}
						}
						// (A name on a kind the chart does not make, such as kube-system, is an object that is already there.)
						if objectsMade && !made {
							t.Errorf("ceiling %d telemetry=%v: %s names %q on %v, which the agent chart never makes", ceiling, withTelemetry, o, n, r.Resources)
						}
					}
					for _, g := range r.APIGroups {
						for _, res := range r.Resources {
							key := g + "/" + res
							asked := kinds[key] || onlyTheControllers[key]
							// What an agent role grants is held so that it can be created; that is a reason of its own.
							held := len(missing(agentRules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: []string{res}, Verbs: r.Verbs, ResourceNames: r.ResourceNames})) == 0
							if !asked && !held {
								t.Errorf("ceiling %d telemetry=%v: %s holds %v on %s and nothing the agent chart makes or grants asks for it", ceiling, withTelemetry, o, r.Verbs, key)
							}
						}
					}
				}
			}
		}
	}
}

// What is read cluster-wide is exactly what the agent's ClusterRoles grant up to the ceiling, and no wider: with telemetry allowed, those
// of its collectors too; without, none of them.
func TestTheControllersClusterWideReadsAreExactlyWhatTheAgentsClusterRolesGrant(t *testing.T) {
	for _, withTelemetry := range []bool{false, true} {
		for ceiling := 0; ceiling <= 2; ceiling++ {
			ctl := controllerPerms(t, helmObjects(t, controllerChart(), "--set", fmt.Sprintf("maxAccessTier=%d", ceiling), "--set", fmt.Sprintf("allowTelemetry=%v", withTelemetry)))
			var agent []rbacv1.PolicyRule
			renders := []choice{{tier: ceiling}}
			if withTelemetry {
				renders = append(renders, choice{tier: ceiling, telemetry: true})
			}
			for _, c := range renders {
				for _, o := range agentObjects(t, c) {
					if o.Kind == "ClusterRole" {
						agent = append(agent, o.Rules...)
					}
				}
			}
			for _, r := range ctl.cluster {
				if has(r.Resources, "clusterroles") || has(r.Resources, "clusterrolebindings") {
					continue
				}
				if m := missing(agent, r); len(m) > 0 {
					t.Errorf("ceiling %d telemetry=%v: the controller reads more cluster-wide than the agent's ClusterRoles grant: %v", ceiling, withTelemetry, m)
				}
			}
		}
	}
}

// Telemetry is a ceiling of its own: a controller not given it cannot create the telemetry's ClusterRole, and one given it can; and
// whichever it is, it can still remove the roles by name (so that taking telemetry off a release works), and holds nothing that is only
// for telemetry's namespaced objects.
func TestTelemetryIsACeilingOfItsOwnAndAControllerWithoutItCannotCreateWhatItNeeds(t *testing.T) {
	without := controllerPerms(t, helmObjects(t, controllerChart(), "--set", "maxAccessTier=2"))
	with := controllerPerms(t, helmObjects(t, controllerChart(), "--set", "maxAccessTier=2", "--set", "allowTelemetry=true"))
	for _, c := range telemetryChoices() {
		var needsTelemetry bool
		for _, o := range agentObjects(t, c) {
			res := resourceOf[o.APIVersion+"/"+o.Kind]
			if o.Kind == "ClusterRole" {
				for _, r := range o.Rules {
					if len(missing(without.cluster, r)) > 0 {
						needsTelemetry = true
						if m := missing(with.cluster, r); len(m) > 0 {
							t.Errorf("%s: with telemetry allowed the controller still lacks %v", c, m)
						}
					}
				}
			}
			if o.Kind == "ConfigMap" || o.Kind == "NetworkPolicy" {
				needsTelemetry = true
				if anyAllows(without.in(o.namespace()), res[0], res[1], "create", o.Metadata.Name) {
					t.Errorf("%s: without telemetry the controller can create %s", c, o)
				}
				if !anyAllows(with.in(o.namespace()), res[0], res[1], "create", o.Metadata.Name) {
					t.Errorf("%s: with telemetry the controller cannot create %s", c, o)
				}
			}
		}
		if !needsTelemetry {
			t.Errorf("%s: nothing in this render needs the telemetry ceiling, so the test proves nothing", c)
		}
	}
	// The roles can be removed by name whatever the ceiling.
	for name, p := range map[string]perms{"without": without, "with": with} {
		for _, role := range []string{"ikhnos-agent-telemetry", "ikhnos-agent-kepler", "ikhnos-agent-telemetry-" + ctlNamespace, "ikhnos-agent-kepler-" + ctlNamespace} {
			for _, res := range []string{"clusterroles", "clusterrolebindings"} {
				if !anyAllows(p.cluster, "rbac.authorization.k8s.io", res, "delete", role) {
					t.Errorf("%s telemetry: the controller cannot delete %s %s", name, res, role)
				}
			}
		}
	}
}

func TestTheControllersOwnResourceIsTheOneItsDefinitionDeclares(t *testing.T) {
	ctl := controllerPerms(t, helmObjects(t, controllerChart()))
	rules := ctl.in(ctlNamespace)
	for _, v := range []string{"get", "list", "watch", "update", "patch"} {
		if !anyAllows(rules, controller.Group, controller.Resource, v, "") {
			t.Errorf("the controller cannot %s %s.%s", v, controller.Resource, controller.Group)
		}
	}
	for _, v := range []string{"get", "update", "patch"} {
		if !anyAllows(rules, controller.Group, controller.Resource+"/status", v, "") {
			t.Errorf("the controller cannot %s the status of %s", v, controller.Resource)
		}
	}
	for _, v := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		if !anyAllows(rules, "", "secrets", v, "") {
			t.Errorf("the controller cannot %s Secrets in its namespace (Helm's release records, the token)", v)
		}
	}
	// It does not manage its own definition, and it holds nothing on other namespaces' Secrets.
	if anyAllows(ctl.cluster, "", "secrets", "get", "") || anyAllows(ctl.cluster, "apiextensions.k8s.io", "customresourcedefinitions", "create", "") {
		t.Error("the controller holds cluster-wide Secrets or CRD permissions")
	}
}

func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }

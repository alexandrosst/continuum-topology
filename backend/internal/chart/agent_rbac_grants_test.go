package chart

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

// This file renders continuum-agent's rbac.yaml for every implemented access tier (0, 1, 2) and checks the
// real ClusterRole/Role rules against the same claims src/lib/types.ts's ACCESS_TIER_GRANTS makes to a
// person picking a tier in the UI (surfaced by TierLevels.tsx's "What this grants" disclosure, wherever a
// tier is picked or viewed: ApprovalCard, ConnectClusterWizard, AgentInsight). ACCESS_TIER_GRANTS is
// necessarily hand-written prose describing this Helm template - nothing wires the two together at build
// time - so whoever edits rbac.yaml without updating both ACCESS_TIER_GRANTS and this test's own
// expectations below finds out here, in CI, rather than from an administrator who trusted what the UI told
// them about a ServiceAccount they just granted cluster-wide read access to.

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// sameSet reports whether a and b contain the same strings, order and duplicates aside - PolicyRule's own
// Verbs/Resources/APIGroups/ResourceNames are themselves unordered sets in Kubernetes' RBAC semantics.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !containsStr(b, v) {
			return false
		}
	}
	return true
}

// hasExactRule reports whether rules contains one whose APIGroups/Resources include apiGroup/resource and
// whose Verbs are exactly verbs (as a set). resourceNames, when non-nil, must also match as a set -
// pass nil to not care (every tier-1/2 rule below is cluster-wide, with no resourceNames to narrow it).
func hasExactRule(rules []rbacv1.PolicyRule, apiGroup, resource string, resourceNames []string, verbs ...string) bool {
	for _, r := range rules {
		if !containsStr(r.APIGroups, apiGroup) || !containsStr(r.Resources, resource) {
			continue
		}
		if !sameSet(r.Verbs, verbs) {
			continue
		}
		if resourceNames != nil && !sameSet(r.ResourceNames, resourceNames) {
			continue
		}
		return true
	}
	return false
}

// TestAccessTier0GrantsMatchRBAC mirrors ACCESS_TIER_GRANTS[0]: "nothing beyond proving which cluster this
// is" - get on exactly the one resourceName "kube-system", and no t1/t2 ClusterRole at all.
func TestAccessTier0GrantsMatchRBAC(t *testing.T) {
	r := render(t, "--set", "access.tier=0")
	t0, ok := r.clusterroles["continuum-agent-t0"]
	if !ok {
		t.Fatal("no continuum-agent-t0 ClusterRole rendered")
	}
	if len(t0.Rules) != 1 {
		t.Errorf("tier 0 ClusterRole has %d rules, want exactly 1 (ACCESS_TIER_GRANTS[0] claims nothing beyond cluster identity): %+v", len(t0.Rules), t0.Rules)
	}
	if !hasExactRule(t0.Rules, "", "namespaces", []string{"kube-system"}, "get") {
		t.Errorf("tier 0 ClusterRole does not grant exactly get on namespaces/kube-system: %+v", t0.Rules)
	}
	if _, ok := r.clusterroles["continuum-agent-t1"]; ok {
		t.Error("access.tier=0 must not render a t1 ClusterRole")
	}
	if _, ok := r.clusterroles["continuum-agent-t2"]; ok {
		t.Error("access.tier=0 must not render a t2 ClusterRole")
	}
}

// TestAccessTier1GrantsMatchRBAC mirrors ACCESS_TIER_GRANTS[1]: tier 0 unchanged, plus read-only on nodes,
// storage classes and ingress classes - "what the cluster is made of", matching ACCESS_TIER_CAPTIONS[1]'s
// own wording too.
func TestAccessTier1GrantsMatchRBAC(t *testing.T) {
	r := render(t, "--set", "access.tier=1")
	if _, ok := r.clusterroles["continuum-agent-t0"]; !ok {
		t.Error("access.tier=1 must still render (and bind) the t0 ClusterRole")
	}
	t1, ok := r.clusterroles["continuum-agent-t1"]
	if !ok {
		t.Fatal("no continuum-agent-t1 ClusterRole rendered")
	}
	for _, want := range []struct{ group, resource string }{
		{"", "nodes"},
		{"storage.k8s.io", "storageclasses"},
		{"networking.k8s.io", "ingressclasses"},
	} {
		if !hasExactRule(t1.Rules, want.group, want.resource, nil, "get", "list", "watch") {
			t.Errorf("tier 1 ClusterRole missing get/list/watch on %s/%s (ACCESS_TIER_GRANTS[1] claims it): %+v", want.group, want.resource, t1.Rules)
		}
	}
	if _, ok := r.clusterroles["continuum-agent-t2"]; ok {
		t.Error("access.tier=1 must not render a t2 ClusterRole")
	}
}

// TestAccessTier2GrantsMatchRBAC mirrors ACCESS_TIER_GRANTS[2]: tier 1 unchanged, plus read-only on the
// workload/service/policy objects the caption calls "what runs on it", including the mesh.readPolicy
// Istio rule (on by default) and that it is removed when that flag is off.
func TestAccessTier2GrantsMatchRBAC(t *testing.T) {
	r := render(t, "--set", "access.tier=2")
	t2, ok := r.clusterroles["continuum-agent-t2"]
	if !ok {
		t.Fatal("no continuum-agent-t2 ClusterRole rendered")
	}
	for _, want := range []struct{ group, resource string }{
		{"", "namespaces"},
		{"", "pods"},
		{"", "services"},
		{"", "persistentvolumeclaims"},
		{"", "persistentvolumes"},
		{"apps", "deployments"},
		{"apps", "statefulsets"},
		{"apps", "daemonsets"},
		{"apps", "replicasets"},
		{"networking.k8s.io", "ingresses"},
		{"autoscaling", "horizontalpodautoscalers"},
		{"policy", "poddisruptionbudgets"},
	} {
		if !hasExactRule(t2.Rules, want.group, want.resource, nil, "get", "list", "watch") {
			t.Errorf("tier 2 ClusterRole missing get/list/watch on %s/%s (ACCESS_TIER_GRANTS[2] claims it): %+v", want.group, want.resource, t2.Rules)
		}
	}
	if !hasExactRule(t2.Rules, "security.istio.io", "peerauthentications", nil, "get", "list") {
		t.Errorf("tier 2 ClusterRole missing get+list (not watch) on security.istio.io/peerauthentications under the default mesh.readPolicy=true (ACCESS_TIER_GRANTS[2] claims it): %+v", t2.Rules)
	}

	rNoMesh := render(t, "--set", "access.tier=2", "--set", "mesh.readPolicy=false")
	if hasExactRule(rNoMesh.clusterroles["continuum-agent-t2"].Rules, "security.istio.io", "peerauthentications", nil, "get", "list") {
		t.Error("mesh.readPolicy=false must drop the PeerAuthentication rule tier 2's grant list calls out as conditional")
	}
}

// TestAccessTierGrantsAreReadOnly is the one fact ACCESS_TIER_GRANTS' disclosure states unconditionally for
// every tier: every verb granted by any of t0/t1/t2 is get, list or watch - never a write, delete or exec.
// Confirmed here instead of just asserted in prose, per the task that added ACCESS_TIER_GRANTS.
func TestAccessTierGrantsAreReadOnly(t *testing.T) {
	r := render(t, "--set", "access.tier=2")
	for _, name := range []string{"continuum-agent-t0", "continuum-agent-t1", "continuum-agent-t2"} {
		cr, ok := r.clusterroles[name]
		if !ok {
			t.Fatalf("no %s ClusterRole rendered at access.tier=2", name)
		}
		for _, rule := range cr.Rules {
			for _, v := range rule.Verbs {
				if v != "get" && v != "list" && v != "watch" {
					t.Errorf("%s grants verb %q on %v/%v - ACCESS_TIER_GRANTS claims every tier is read-only (get/list/watch only)", name, v, rule.APIGroups, rule.Resources)
				}
			}
		}
	}
}

// TestAccessTierBaselineGrantMatchesRBAC mirrors ACCESS_TIER_BASELINE_GRANT: the two fixed rules every
// install carries regardless of access.tier - the agent's own identity Secret (get/update/patch, by
// resourceName, nothing else), and, unless access.resolveApiEndpoint=false, get on the "kubernetes"
// Endpoints object in the default namespace.
func TestAccessTierBaselineGrantMatchesRBAC(t *testing.T) {
	r := render(t, "--set", "access.tier=0")
	identity, ok := r.roles["continuum-agent-identity"]
	if !ok {
		t.Fatal("no continuum-agent-identity Role rendered")
	}
	if !hasExactRule(identity.Rules, "", "secrets", []string{"continuum-agent-identity"}, "get", "update", "patch") {
		t.Errorf("identity Role does not grant exactly get/update/patch on secrets/continuum-agent-identity: %+v", identity.Rules)
	}

	// access.resolveApiEndpoint defaults to true (see values.yaml): the endpoint Role is rendered unless
	// it is explicitly turned off.
	ep, ok := r.roles["continuum-agent-api-endpoint"]
	if !ok {
		t.Fatal("no continuum-agent-api-endpoint Role rendered under the default access.resolveApiEndpoint=true")
	}
	if !hasExactRule(ep.Rules, "", "endpoints", []string{"kubernetes"}, "get") {
		t.Errorf("api-endpoint Role does not grant exactly get on endpoints/kubernetes: %+v", ep.Rules)
	}

	rOff := render(t, "--set", "access.tier=0", "--set", "access.resolveApiEndpoint=false")
	if _, ok := rOff.roles["continuum-agent-api-endpoint"]; ok {
		t.Error("access.resolveApiEndpoint=false must drop the api-endpoint Role entirely")
	}
}

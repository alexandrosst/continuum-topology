package agent

import (
	"context"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The agent is told its own ceiling once, at startup, as --tier (Config.Tier): a number baked into the container by
// whoever ran `helm install`. Nothing ever asks the cluster whether that number still matches what RBAC actually
// grants. Usually it does. It stops matching when a `helm upgrade --set access.tier=N` meant to narrow an install
// was never run, or was run but failed partway through (a webhook rejection, a quota, a network blip between
// Helm applying the Deployment and pruning the old ClusterRole) - the container comes back up believing the lower
// tier while the cluster still binds the ServiceAccount to the higher tier's ClusterRole. That gap matters beyond
// bookkeeping: whatever the binding grants is exactly what an attacker gets from this ServiceAccount's token if the
// pod is ever compromised, regardless of what tier the server or this agent believes is in force. rbacCheckLoop
// notices that gap by asking the cluster directly, on a slow interval, rather than trusting the number it was
// launched with.

const defaultRBACCheckEvery = 10 * time.Minute

// rbacCheckLoop periodically compares what the cluster actually grants this ServiceAccount against what Tier
// declares, and keeps diagState's cached ceiling current so diagnostics() (which must stay cheap and call nothing)
// can read it without touching the network. It only runs when the caller asked for it (Config.RBACSelfCheck) and
// there is a real cluster to ask.
func (r *runner) rbacCheckLoop(ctx context.Context) {
	every := r.cfg.RBACCheckEvery
	if every <= 0 {
		every = defaultRBACCheckEvery
	}
	r.checkRBACCeiling(ctx) // once at once, not only after the first interval
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.checkRBACCeiling(ctx)
		}
	}
}

// rbacProbes are one representative, cluster-scoped resource per tier above the baseline (tier 0 needs nothing
// beyond reading kube-system, which every agent can already do by the time it is enrolled). They mirror the chart's
// own ClusterRoles (rbac.yaml): tier 1 adds nodes, tier 2 adds pods. Kubernetes evaluates SelfSubjectAccessReview
// without needing any permission itself, so this never fails for lack of RBAC the way an actual list or watch would.
var rbacProbes = []struct {
	tier     int
	resource string
}{
	{1, "nodes"},
	{2, "pods"},
}

// checkRBACCeiling asks the cluster, not the agent's own config, how far this ServiceAccount can currently read,
// and records the highest tier that is actually granted. A review that errors (the authorization API itself is
// unreachable, say) leaves the cached value as it was: silence about a failed check is safer than guessing.
func (r *runner) checkRBACCeiling(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ceiling := 0
	for _, p := range rbacProbes {
		allowed, err := r.probeTierAllowed(cctx, p.resource)
		if err != nil {
			return // the check itself failed; say nothing rather than report a false ceiling
		}
		if allowed && p.tier > ceiling {
			ceiling = p.tier
		}
	}
	r.dg.mu.Lock()
	r.dg.rbacCeiling, r.dg.rbacCheckedAt = ceiling, time.Now()
	r.dg.mu.Unlock()
}

// probeTierAllowed asks a single SelfSubjectAccessReview, cluster-wide, exactly mirroring what the chart's
// ClusterRole would grant (cluster mode, the default). rbac.mode=namespaced never grants a cluster-wide list for
// tier 2 by design (a Role cannot), so that alone would always come back false there and could never flag a real
// leftover: an administrator who meant to narrow access.tier back down, but whose per-namespace Role/RoleBinding
// from before was never removed (a partial `helm upgrade`, same as the ClusterRoleBinding case this check exists
// for), would see nothing. When the cluster-wide ask for "pods" comes back false under namespaced RBAC, this also
// asks each namespace the install names (Scope.Include, the same list rbac.yaml grants a Role in) and reports
// allowed if any one of them is. The cluster-wide ask always runs first and unconditionally, so a stray
// ClusterRole leaking into a namespaced-mode install (wider still, and the more dangerous case) is still caught.
func (r *runner) probeTierAllowed(ctx context.Context, resource string) (bool, error) {
	check := func(ns string) (bool, error) {
		attrs := &authorizationv1.ResourceAttributes{Verb: "list", Resource: resource, Namespace: ns}
		rev, err := r.cfg.Kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		return rev.Status.Allowed, nil
	}
	allowed, err := check("")
	if err != nil || allowed || resource != "pods" || !r.cfg.RBACNamespaced || r.cfg.Scope == nil {
		return allowed, err
	}
	for _, ns := range r.cfg.Scope.Include {
		a, err := check(ns)
		if err != nil {
			return false, err
		}
		if a {
			return true, nil
		}
	}
	return false, nil
}

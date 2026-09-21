package server

import (
	"fmt"
	"net/http"
	"strings"

	"continuum/internal/chart"
)

// releaseTarget is the namespace and Helm release name a command should target: the agent's own, as it reported in its
// last Hello, or a guess when either is not yet known (an agent older than these fields, or one that has never
// connected to this server). Every object the chart creates has a fixed name (see agent.name in _helpers.tpl) regardless
// of the release name, so only the two `helm` commands need it; `kubectl` commands only ever need the namespace. The
// guesses are the chart's own documentation defaults, not facts, so callers that show them to a person should say so.
func releaseTarget(namespace, release string) (ns, name string, guessed bool) {
	ns, name = namespace, release
	if ns == "" {
		ns, guessed = "continuum-system", true
	}
	if name == "" {
		name, guessed = "continuum-agent", true
	}
	return ns, name, guessed
}

// upgradeCommand is what the owner of a cluster runs there to change the ceiling of an agent's install to a tier: raise it
// (the only way widening ever happens; the server cannot do it) or, just as validly, lower it for real, which the tier
// slider in the UI on its own does not do (see setAgentTier's doc comment).
func (a *Admin) upgradeCommand(img ImageConfig, tier int, namespace, release string) string {
	ref, version := a.chartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + chart.Version()
	}
	ns, name, _ := releaseTarget(namespace, release)
	return fmt.Sprintf("helm upgrade %s %s%s --namespace %s --reuse-values --set access.tier=%d", name, ref, version, ns, tier)
}

// teardownCommands is what the owner of a cluster runs there to remove an agent's install for good: helm uninstall drops
// the ServiceAccount, the RBAC and the workloads, but not the identity Secret (the chart marks it
// helm.sh/resource-policy: keep, so an accidental uninstall cannot orphan re-enrollment or need a new CA trust); the second
// command is only needed if the Secret itself should go too, e.g. before installing fresh in the same namespace. The
// Secret's own name never depends on the release name (every chart object has a fixed name), only on the namespace.
func teardownCommands(namespace, release string) (helm, secret string, guessed bool) {
	ns, name, g := releaseTarget(namespace, release)
	return fmt.Sprintf("helm uninstall %s --namespace %s", name, ns),
		fmt.Sprintf("kubectl delete secret continuum-agent-identity --namespace %s", ns), g
}

// setAgentTier changes the access an approved agent has been given, within what its install allows.
func (a *Admin) setAgentTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier *int `json:"tier"`
	}
	if err := decode(r, &req); err != nil || req.Tier == nil {
		a.fail(w, errf(KindInvalid, "invalid request body: expected {\"tier\": N}"))
		return
	}
	c := a.core(r)
	img := a.images(c)
	hub := a.tn(r).Hub
	ns, rel := hub.NamespaceOf(r.PathValue("id")), hub.ReleaseNameOf(r.PathValue("id"))
	ag, err := hub.SetAgentTier(r.Context(), actor(r), r.PathValue("id"), *req.Tier, func(t int) string { return a.upgradeCommand(img, t, ns, rel) })
	if err != nil {
		a.fail(w, err)
		return
	}
	resp := map[string]any{"accessTier": ag.AccessTier, "installedTier": ag.InstalledTier}
	// A tier below the ceiling changes what the agent reports right away, but not what the cluster's own RBAC still
	// grants: that install-time ClusterRoleBinding stays until someone lowers access.tier there too. Hand back the
	// exact command for whoever wants that narrower report to also be a narrower Kubernetes permission.
	if ag.AccessTier < ag.InstalledTier {
		resp["hardenHelm"] = a.upgradeCommand(img, ag.AccessTier, ns, rel)
	}
	writeJSON(w, 200, resp)
}

// setAgentConsent replaces the overrides on an agent: the optional collectors it is asked to stop and the namespaces it is asked to
// leave out. It can only reduce what is shared.
func (a *Admin) setAgentConsent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PausedCollectors   []string `json:"pausedCollectors"`
		ExcludedNamespaces []string `json:"excludedNamespaces"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	got, err := a.tn(r).Hub.SetConsent(r.Context(), actor(r), r.PathValue("id"), Consent{Paused: req.PausedCollectors, Excluded: req.ExcludedNamespaces})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, ConsentDoc{PausedCollectors: append([]string{}, got.Paused...), ExcludedNamespaces: append([]string{}, got.Excluded...)})
}

// withoutDiagnostics removes what only people who can change an agent's access may see.
func withoutDiagnostics(doc *StateDoc) {
	for i := range doc.Agents {
		doc.Agents[i].Diagnostics, doc.Agents[i].Consent, doc.Agents[i].Teardown = nil, nil, nil
	}
}

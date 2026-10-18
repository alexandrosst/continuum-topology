package server

import (
	"net/http"
	"regexp"
	"strconv"

	"continuum/internal/install"
)

// validKubeName is a DNS-1123 label, which is what both a namespace and a Helm release name must be.
var validKubeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// releaseTarget is the namespace and Helm release name a command should target: the agent's own, as it reported in its
// last Hello, or a guess when either is not yet known (an agent older than these fields, or one that has never
// connected to this server). Every object the chart creates has a fixed name (see agent.name in _helpers.tpl) regardless
// of the release name, so only the two `helm` commands need it; `kubectl` commands only ever need the namespace. The
// guesses are the chart's own documentation defaults, not facts, so callers that show them to a person should say so.
func releaseTarget(namespace, release string) (ns, name string, guessed bool) {
	// What an agent reported about itself ends up inside a command a person pastes into a shell, so anything
	// that is not a plain Kubernetes / Helm name is treated as not reported at all.
	if !validKubeName.MatchString(namespace) {
		namespace = ""
	}
	if !validKubeName.MatchString(release) {
		release = ""
	}
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
//
// Every upgrade command this server prints uses --reset-then-reuse-values (Helm 3.14 or newer), never --reuse-values. The
// older flag replaces the NEW chart's defaults with the values the OLD release was installed with, so a value block a
// newer chart added after the release was installed does not exist at all and a template that reads it fails with a
// nil-pointer error. The newer flag starts from the new chart's defaults and then puts the person's own values from the old
// release on top, which is what "keep what I set" was always meant to say.
func (a *Admin) upgradeCommand(img ImageConfig, tier int, namespace, release string) (string, error) {
	ref, version := a.agentChart(img)
	ns, name, _ := releaseTarget(namespace, release)
	return install.Helm{Verb: install.Upgrade, Release: name, Chart: ref, Version: version, Namespace: ns, ResetThenReuse: true,
		Values: []install.Value{install.Set("access.tier", strconv.Itoa(tier))}}.Render()
}

// teardownCommands is what the owner of a cluster runs there to remove an agent's install for good: helm uninstall drops
// the ServiceAccount, the RBAC and the workloads, but not the identity Secret (the chart marks it
// helm.sh/resource-policy: keep, so an accidental uninstall cannot orphan re-enrollment or need a new CA trust); the second
// command is only needed if the Secret itself should go too, e.g. before installing fresh in the same namespace. The
// Secret's own name never depends on the release name (every chart object has a fixed name), only on the namespace.
func teardownCommands(namespace, release string) (helm, secret string, guessed bool, err error) {
	ns, name, guessed := releaseTarget(namespace, release)
	if helm, err = (install.Uninstall{Release: name, Namespace: ns}).Render(); err != nil {
		return "", "", guessed, err
	}
	if secret, err = (install.DeleteSecret{Name: "continuum-agent-identity", Namespace: ns}).Render(); err != nil {
		return "", "", guessed, err
	}
	return helm, secret, guessed, nil
}

// setAgentTier changes the access an approved agent has been given, within what its install allows.
func (a *Admin) setAgentTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier *int `json:"tier"`
	}
	if err := decode(r, &req); err != nil || req.Tier == nil {
		a.fail(w, Errf(KindInvalid, "invalid request body: expected {\"tier\": N}"))
		return
	}
	c := a.core(r)
	img := a.images(c)
	hub := a.tn(r).Hub
	ns, rel := hub.NamespaceOf(r.PathValue("id")), hub.ReleaseNameOf(r.PathValue("id"))
	ag, err := hub.SetAgentTier(r.Context(), actor(r), r.PathValue("id"), *req.Tier, func(t int) string {
		cmd, err := a.upgradeCommand(img, t, ns, rel)
		if err != nil {
			a.installConfigError(err) // logged; the audit entry is written without the command
		}
		return cmd
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	resp := map[string]any{"accessTier": ag.AccessTier, "installedTier": ag.InstalledTier}
	// A tier below the ceiling changes what the agent reports right away, but not what the cluster's own RBAC still
	// grants: that install-time ClusterRoleBinding stays until someone lowers access.tier there too. Hand back the
	// exact command for whoever wants that narrower report to also be a narrower Kubernetes permission.
	if ag.AccessTier < ag.InstalledTier {
		cmd, err := a.upgradeCommand(img, ag.AccessTier, ns, rel)
		if err != nil {
			a.fail(w, a.installConfigError(err))
			return
		}
		resp["hardenHelm"] = cmd
	}
	writeJSON(w, 200, resp)
}

// setAgentConsent replaces the overrides on an agent: the optional collectors it is asked to stop and the namespaces it is asked to
// leave out. An editor may add to them; lifting one takes an administrator.
func (a *Admin) setAgentConsent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PausedCollectors   []string `json:"pausedCollectors"`
		ExcludedNamespaces []string `json:"excludedNamespaces"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	hub := a.tn(r).Hub
	id := r.PathValue("id")
	if _, err := hub.SetConsent(r.Context(), actor(r), id, Consent{Paused: req.PausedCollectors, Excluded: req.ExcludedNamespaces}); err != nil {
		a.fail(w, err)
		return
	}
	// The same projection the agent list already carries (see AgentDoc.Consent), not a bare echo of what was
	// asked for: right after a save this is unconfirmed with a fresh SetAt (nothing has had time to apply
	// yet), exactly as a client that reloaded the page a second later would also be told.
	writeJSON(w, 200, hub.ConsentDocFor(id))
}

// withoutDiagnostics removes what only people who can change an agent's access may see.
func withoutDiagnostics(doc *StateDoc) {
	for i := range doc.Agents {
		doc.Agents[i].Diagnostics, doc.Agents[i].Consent, doc.Agents[i].Teardown = nil, nil, nil
	}
}

// startAddressTests answers POST .../agents/{id}/address-tests: the cluster this agent is in tries each address.
func (a *Admin) startAddressTests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	tests, err := a.tn(r).Hub.StartAddressTests(r.Context(), actor(r), r.PathValue("id"), req.Addresses)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"tests": tests})
}

// listAddressTests answers GET .../agents/{id}/address-tests: the tests still held and what they found so far.
func (a *Admin) listAddressTests(w http.ResponseWriter, r *http.Request) {
	tests, err := a.tn(r).Hub.AddressTests(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tests": tests})
}

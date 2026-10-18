package server

import (
	"net/http"

	"continuum/internal/agents"
	"continuum/internal/estate"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// upgradeCommand is the command that changes the ceiling of an agent's install to a tier (installcfg's AgentUpgradeCommand), for the
// namespace and release the agent reported, or the chart's documented defaults when it reported none we can trust in a command.
func (a *Admin) upgradeCommand(img kernel.ImageConfig, tier int, namespace, release string) (string, error) {
	ns, name, _ := agents.ReleaseTarget(namespace, release)
	return a.AgentUpgradeCommand(img, tier, ns, name)
}

// setAgentTier changes the access an approved agent has been given, within what its install allows.
func (a *Admin) setAgentTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier *int `json:"tier"`
	}
	if err := httpkit.Decode(r, &req); err != nil || req.Tier == nil {
		a.fail(w, kernel.Errf(kernel.KindInvalid, "invalid request body: expected {\"tier\": N}"))
		return
	}
	c := a.core(r)
	img := a.images(c)
	hub := a.tn(r).Hub
	ns, rel := hub.NamespaceOf(r.PathValue("id")), hub.ReleaseNameOf(r.PathValue("id"))
	ag, err := hub.SetAgentTier(r.Context(), httpkit.Actor(r), r.PathValue("id"), *req.Tier, func(t int) string {
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
	httpkit.WriteJSON(w, 200, resp)
}

// setAgentConsent replaces the overrides on an agent: the optional collectors it is asked to stop and the namespaces it is asked to
// leave out. An editor may add to them; lifting one takes an administrator.
func (a *Admin) setAgentConsent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PausedCollectors   []string `json:"pausedCollectors"`
		ExcludedNamespaces []string `json:"excludedNamespaces"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	hub := a.tn(r).Hub
	id := r.PathValue("id")
	if _, err := hub.SetConsent(r.Context(), httpkit.Actor(r), id, agents.Consent{Paused: req.PausedCollectors, Excluded: req.ExcludedNamespaces}); err != nil {
		a.fail(w, err)
		return
	}
	// The same projection the agent list already carries (see AgentDoc.Consent), not a bare echo of what was
	// asked for: right after a save this is unconfirmed with a fresh SetAt (nothing has had time to apply
	// yet), exactly as a client that reloaded the page a second later would also be told.
	httpkit.WriteJSON(w, 200, hub.ConsentDocFor(id))
}

// withoutDiagnostics removes what only people who can change an agent's access may see.
func withoutDiagnostics(doc *estate.StateDoc) {
	for i := range doc.Agents {
		doc.Agents[i].Diagnostics, doc.Agents[i].Consent, doc.Agents[i].Teardown = nil, nil, nil
	}
}

// startAddressTests answers POST .../agents/{id}/address-tests: the cluster this agent is in tries each address.
func (a *Admin) startAddressTests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	tests, err := a.tn(r).Hub.StartAddressTests(r.Context(), httpkit.Actor(r), r.PathValue("id"), req.Addresses)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 202, map[string]any{"tests": tests})
}

// listAddressTests answers GET .../agents/{id}/address-tests: the tests still held and what they found so far.
func (a *Admin) listAddressTests(w http.ResponseWriter, r *http.Request) {
	tests, err := a.tn(r).Hub.AddressTests(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"tests": tests})
}

package estate

import (
	"net/http"
	"strings"

	"continuum/internal/agents"
	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/installcfg"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// API is the estate's routes: what a person does with the agents of an organisation (the tokens that enrol them, approving, rejecting and
// revoking, the access and consent they are given, the tests of the addresses they are reached on) and the commands that install and
// change them.
type API struct {
	// For is the hub of the organisation a request is for (the shell finds it from what its guard put in the request).
	For func(r *http.Request) *Hub
	// Settings are the install settings the commands are written from, and CAPin the pin of the certificate authority the agents verify
	// the server against (the commands carry it).
	Settings *installcfg.Settings
	CAPin    func() string
}

func (h *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpkit.Fail(w, h.For(r).Log, err)
}

// Mount puts the routes on r.
func (h *API) Mount(r httpkit.Router) {
	const o = httpkit.OrgPath
	r.Handle("GET "+o+"/tokens", httpkit.AdminRole, h.listTokens)
	r.Handle("POST "+o+"/tokens", httpkit.AdminRole, h.createToken)
	// Writes the command that changes an installed agent. Pure computation (nothing is stored or issued), so any member may ask.
	r.Handle("POST "+o+"/commands/agent-upgrade", httpkit.MemberRole, h.agentUpgradeCommand)
	r.Handle("DELETE "+o+"/tokens/{id}", httpkit.AdminRole, h.deleteToken)
	r.Handle("POST "+o+"/agents/{id}/approve", httpkit.AdminRole, h.approve)
	r.Handle("POST "+o+"/agents/{id}/reject", httpkit.AdminRole, h.reject)
	r.Handle("POST "+o+"/agents/{id}/revoke", httpkit.AdminRole, h.revoke)
	r.Handle("POST "+o+"/agents/{id}/address-tests", httpkit.AdminRole, h.startAddressTests)
	r.Handle("GET "+o+"/agents/{id}/address-tests", httpkit.AdminRole, h.listAddressTests)
	// Narrowing an agent's tier or consent is an editor's; widening either is an administrator's, like approving (checked in the hub).
	r.Handle("POST "+o+"/agents/{id}/tier", httpkit.EditorRole, h.setAgentTier)
	r.Handle("POST "+o+"/agents/{id}/consent", httpkit.EditorRole, h.setAgentConsent)
}

// UpgradeCommand is the command that changes the ceiling of an agent's install to a tier (installcfg's AgentUpgradeCommand), for the
// namespace and release the agent reported, or the chart's documented defaults when it reported none we can trust in a command.
func UpgradeCommand(s *installcfg.Settings, img kernel.ImageConfig, tier int, namespace, release string) (string, error) {
	ns, name, _ := agents.ReleaseTarget(namespace, release)
	return s.AgentUpgradeCommand(img, tier, ns, name)
}

// installConfigError is a command this server cannot write, because of how it was set up: logged in full, told without the setting.
func (h *API) installConfigError(r *http.Request, err error) error {
	return installcfg.ConfigError(h.For(r).Log, err)
}

type tokenDoc struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tier      int    `json:"tier"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	Used      bool   `json:"used"`
	UsedBy    string `json:"usedBy,omitempty"`
	// ExpectedCluster is the cluster (kube-system UID) the token is bound to; absent for a token any cluster may use.
	ExpectedCluster string `json:"expectedCluster,omitempty"`
}

func toTokenDoc(t store.Token) tokenDoc {
	return tokenDoc{ID: t.ID, Name: t.Label, Tier: t.AccessTier, CreatedAt: kernel.RFC(t.CreatedAt), ExpiresAt: kernel.RFC(t.ExpiresAt), Used: t.UsedAt != nil, UsedBy: t.UsedBy, ExpectedCluster: t.ExpectedFingerprint}
}

func (h *API) listTokens(w http.ResponseWriter, r *http.Request) {
	c := h.For(r).C
	ts, err := c.Store.ListTokens(r.Context(), c.OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []tokenDoc{}
	for _, t := range ts {
		out = append(out, toTokenDoc(t))
	}
	httpkit.WriteJSON(w, 200, out)
}

func (h *API) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Tier int    `json:"tier"`
		// ExpectedCluster optionally binds the token to one cluster: the UID of its kube-system namespace.
		ExpectedCluster string `json:"expectedCluster"`
		// Collectors are the optional per-node collectors the install switches on, and Scope the namespaces it may report. They are
		// parts of the command and of nothing else: the token does not record them.
		Collectors install.Collectors `json:"collectors"`
		Scope      install.Scope      `json:"scope"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	extra, err := installChoices(req.Tier, req.Collectors, req.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The token is shown once, with the command that uses it: a command that cannot be written (the chart this server was told to
	// name is not one Helm can be asked for) is found before the token exists, not after it is spent.
	c := h.For(r).C
	img := h.Settings.Images(c.Core)
	if _, err := h.Settings.AgentInstallCommand(img, h.CAPin(), "check", req.Tier, extra...); err != nil {
		h.fail(w, r, h.installConfigError(r, err))
		return
	}
	secret, t, err := c.Agents.CreateTokenFor(r.Context(), httpkit.Actor(r), req.Name, req.Tier, req.ExpectedCluster)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cmd, err := h.Settings.AgentInstallCommand(img, h.CAPin(), secret, t.AccessTier, extra...)
	if err != nil {
		h.fail(w, r, h.installConfigError(r, err))
		return
	}
	httpkit.WriteJSON(w, 201, map[string]any{"token": secret, "meta": toTokenDoc(t), "install": cmd})
}

func (h *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := h.For(r).C.Agents.DeleteToken(r.Context(), httpkit.Actor(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Code is the approval code the agent printed in its log. Confirm is the older fingerprint prefix,
		// still accepted for an agent that enrolled without a code.
		Code    string `json:"code"`
		Confirm string `json:"confirm"`
		Tier    int    `json:"tier"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	proof := strings.TrimSpace(req.Code)
	if proof == "" {
		proof = strings.TrimSpace(req.Confirm)
	}
	if err := h.For(r).C.Agents.Approve(r.Context(), httpkit.Actor(r), r.PathValue("id"), proof, req.Tier); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) reject(w http.ResponseWriter, r *http.Request) {
	httpkit.Reasoned(w, r, h.For(r).Log, func(reason string) error {
		return h.For(r).C.Agents.Reject(r.Context(), httpkit.Actor(r), r.PathValue("id"), reason)
	})
}

func (h *API) revoke(w http.ResponseWriter, r *http.Request) {
	httpkit.Reasoned(w, r, h.For(r).Log, func(reason string) error {
		return h.For(r).C.Agents.Revoke(r.Context(), httpkit.Actor(r), r.PathValue("id"), reason)
	})
}

// setAgentTier changes the access an approved agent has been given, within what its install allows.
func (h *API) setAgentTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier *int `json:"tier"`
	}
	if err := httpkit.Decode(r, &req); err != nil || req.Tier == nil {
		h.fail(w, r, kernel.Errf(kernel.KindInvalid, "invalid request body: expected {\"tier\": N}"))
		return
	}
	hub := h.For(r)
	img := h.Settings.Images(hub.C.Core)
	ns, rel := hub.NamespaceOf(r.PathValue("id")), hub.ReleaseNameOf(r.PathValue("id"))
	ag, err := hub.SetAgentTier(r.Context(), httpkit.Actor(r), r.PathValue("id"), *req.Tier, func(t int) string {
		cmd, err := UpgradeCommand(h.Settings, img, t, ns, rel)
		if err != nil {
			h.installConfigError(r, err) // logged; the audit entry is written without the command
		}
		return cmd
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := map[string]any{"accessTier": ag.AccessTier, "installedTier": ag.InstalledTier}
	// A tier below the ceiling changes what the agent reports right away, but not what the cluster's own RBAC still
	// grants: that install-time ClusterRoleBinding stays until someone lowers access.tier there too. Hand back the
	// exact command for whoever wants that narrower report to also be a narrower Kubernetes permission.
	if ag.AccessTier < ag.InstalledTier {
		cmd, err := UpgradeCommand(h.Settings, img, ag.AccessTier, ns, rel)
		if err != nil {
			h.fail(w, r, h.installConfigError(r, err))
			return
		}
		resp["hardenHelm"] = cmd
	}
	httpkit.WriteJSON(w, 200, resp)
}

// setAgentConsent replaces the overrides on an agent: the optional collectors it is asked to stop and the namespaces it is asked to
// leave out. An editor may add to them; lifting one takes an administrator.
func (h *API) setAgentConsent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PausedCollectors   []string `json:"pausedCollectors"`
		ExcludedNamespaces []string `json:"excludedNamespaces"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	hub := h.For(r)
	id := r.PathValue("id")
	if _, err := hub.SetConsent(r.Context(), httpkit.Actor(r), id, agents.Consent{Paused: req.PausedCollectors, Excluded: req.ExcludedNamespaces}); err != nil {
		h.fail(w, r, err)
		return
	}
	// The same projection the agent list already carries (see AgentDoc.Consent), not a bare echo of what was
	// asked for: right after a save this is unconfirmed with a fresh SetAt (nothing has had time to apply
	// yet), exactly as a client that reloaded the page a second later would also be told.
	httpkit.WriteJSON(w, 200, hub.ConsentDocFor(id))
}

// startAddressTests answers POST .../agents/{id}/address-tests: the cluster this agent is in tries each address.
func (h *API) startAddressTests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	tests, err := h.For(r).StartAddressTests(r.Context(), httpkit.Actor(r), r.PathValue("id"), req.Addresses)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 202, map[string]any{"tests": tests})
}

// listAddressTests answers GET .../agents/{id}/address-tests: the tests still held and what they found so far.
func (h *API) listAddressTests(w http.ResponseWriter, r *http.Request) {
	tests, err := h.For(r).AddressTests(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"tests": tests})
}

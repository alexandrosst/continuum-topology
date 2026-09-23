package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/store"

	"google.golang.org/protobuf/proto"
)

// Consent lives with the cluster's owner. What an agent may read is set by the chart it was installed with (access.tier,
// its RBAC, its scope): that is a CEILING inside the cluster which this server can never exceed, and only `helm upgrade`
// can move. What the server can do is narrow within it, at any time, and that is all this file does: approve a lower tier,
// pause an optional collector, leave more namespaces out. None of it can be used to see more. The agent checks that for
// itself and refuses anything that would widen (override_ignored), so a buggy or hostile server gains nothing by trying.

// The names of the optional collectors an agent may have and the server may pause. Kept in step with the agent's.
var collectorNames = []string{"probes", "flow", "measure"}

// Consent is the set of narrowing overrides an administrator has put on one agent. It is kept per agent, survives
// restarts of the server and reconnects of the agent, and is pushed to the agent in every Config.
type Consent struct {
	// Paused are the optional collectors the agent is asked to stop ("probes", "flow", "measure").
	Paused []string `json:"paused,omitempty"`
	// Excluded are namespaces the agent is asked to leave out on top of its own scope.
	Excluded []string `json:"excluded,omitempty"`
}

func (c Consent) has(name string) bool {
	for _, p := range c.Paused {
		if p == name {
			return true
		}
	}
	return false
}

func (c Consent) empty() bool { return len(c.Paused) == 0 && len(c.Excluded) == 0 }

// consentKey is where an agent's overrides are stored: next to its snapshot, under a name no agent id can have.
func consentKey(agentID string) string { return agentID + "#consent" }

// maxExcluded bounds the namespaces one agent may be told to leave out.
const maxExcluded = 200

var nsNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func systemNS(ns string) bool {
	return ns == "kube-system" || ns == "kube-public" || ns == "kube-node-lease"
}

// cleanConsent validates an administrator's input and puts it in a canonical form (sorted, no repeats).
func cleanConsent(in Consent) (Consent, error) {
	var out Consent
	seen := map[string]bool{}
	for _, p := range in.Paused {
		p = strings.ToLower(strings.TrimSpace(p))
		known := false
		for _, k := range collectorNames {
			known = known || k == p
		}
		if !known {
			return out, errf(KindInvalid, "%q is not a collector an agent can have (probes, flow or measure)", printable(p, 40))
		}
		if !seen[p] {
			seen[p] = true
			out.Paused = append(out.Paused, p)
		}
	}
	seen = map[string]bool{}
	for _, ns := range in.Excluded {
		ns = strings.TrimSpace(ns)
		switch {
		case ns == "":
			continue
		case !nsNameRe.MatchString(ns):
			return out, errf(KindInvalid, "%q is not a valid namespace name (lowercase letters, digits and dashes, at most 63 characters)", printable(ns, 64))
		case systemNS(ns):
			return out, errf(KindInvalid, "%s is a system namespace: the agent always reads those to recognise the cluster's own components", ns)
		}
		if !seen[ns] {
			seen[ns] = true
			out.Excluded = append(out.Excluded, ns)
		}
	}
	if len(out.Excluded) > maxExcluded {
		return out, errf(KindInvalid, "at most %d namespaces can be left out from here; use the scope of the agent's install (helm) for more", maxExcluded)
	}
	sort.Strings(out.Paused)
	sort.Strings(out.Excluded)
	return out, nil
}

// viewExt is what the server keeps per agent about its own account of itself and the overrides pushed to it.
type viewExt struct {
	diag        *continuumv1.Diagnostics
	diagAt      time.Time
	diagPartial bool // only what Hello carried: the ceiling, before the agent had looked at the cluster
	consent     Consent
	consentLoad bool
	// namespace is this agent's own Kubernetes namespace, from its Hello (empty before an agent that reports it has
	// connected at least once since this server started; not persisted, same as diagnostics). Used to print a
	// helm command against the release that is actually running instead of guessing continuum-system.
	namespace string
	// releaseName is the Helm release this agent's Hello says it was installed as (empty under the same conditions as
	// namespace). Every object the chart creates has a fixed name regardless of the release, so this only matters for
	// the two `helm` commands themselves; guessed as "continuum-agent" when unknown, same as namespace.
	releaseName string
}

// viewFor returns the agent's view, making an empty one if the agent has never connected. Caller holds h.mu.
func (h *Hub) viewFor(id string) *view {
	v := h.views[id]
	if v == nil {
		v = newView()
		h.views[id] = v
	}
	return v
}

// ensureConsent loads an agent's overrides from the store the first time they are needed. Do not hold h.mu.
func (h *Hub) ensureConsent(id string, v *view) {
	h.mu.Lock()
	loaded := v.ext.consentLoad
	h.mu.Unlock()
	if loaded {
		return
	}
	var c Consent
	if data, _, err := h.C.Store.LoadSnapshot(bg(), consentKey(id)); err == nil {
		if json.Unmarshal(data, &c) != nil {
			c = Consent{}
		} else if cc, err := cleanConsent(c); err == nil {
			c = cc // stored data is not trusted more than a request
		} else {
			c = Consent{}
		}
	}
	h.mu.Lock()
	if !v.ext.consentLoad {
		v.ext.consent, v.ext.consentLoad = c, true
	}
	h.mu.Unlock()
}

// NamespaceOf returns the Kubernetes namespace an agent last reported for itself, or "" if it never has (an older
// agent, or one that has not connected since this server started).
func (h *Hub) NamespaceOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[id]; v != nil {
		return v.ext.namespace
	}
	return ""
}

// ReleaseNameOf returns the Helm release an agent last reported it was installed as, or "" under the same conditions
// as NamespaceOf.
func (h *Hub) ReleaseNameOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[id]; v != nil {
		return v.ext.releaseName
	}
	return ""
}

// consentOf returns an agent's overrides.
func (h *Hub) consentOf(id string) Consent {
	h.mu.Lock()
	v := h.viewFor(id)
	h.mu.Unlock()
	h.ensureConsent(id, v)
	h.mu.Lock()
	defer h.mu.Unlock()
	return v.ext.consent
}

// SetAgentTier changes what a human approved for an agent that is already approved. Anything up to the ceiling the agent's
// install has is allowed, narrower or wider than before (widening goes back up to what the owner installed, never past it);
// a tier above the ceiling is refused with the exact command the cluster's owner has to run to raise it.
// upgradeCmd builds that command for a tier.
func (h *Hub) SetAgentTier(ctx context.Context, actor, agentID string, tier int, upgradeCmd func(tier int) string) (store.Agent, error) {
	a, err := h.C.agentInOrg(ctx, agentID)
	if err != nil {
		return a, err
	}
	if a.Status != store.StatusApproved {
		return a, errf(KindConflict, "agent is %s: only an approved agent's access can be changed", a.Status)
	}
	if tier < 0 || tier > MaxTier {
		return a, errf(KindInvalid, "access tier must be 0-%d", ImplementedTier)
	}
	if tier == a.AccessTier {
		return a, nil
	}
	if tier > ImplementedTier {
		return a, errf(KindInvalid, "access tier %d is not available in this release (the highest is %d)", tier, ImplementedTier)
	}
	if tier > a.InstalledTier {
		cmd := ""
		if upgradeCmd != nil {
			cmd = upgradeCmd(tier)
		}
		e := errf(KindInvalid, "this agent's install allows at most tier %d (its Helm value access.tier), and only the cluster's owner can raise that. Run this in that cluster, then set the tier here:\n%s", a.InstalledTier, cmd)
		e.Data = map[string]any{"installedTier": a.InstalledTier, "helm": cmd}
		return a, e
	}
	if tier > a.TierCap {
		return a, errf(KindInvalid, "access tier %d is above what the enrollment token allowed for this agent (%d)", tier, a.TierCap)
	}
	old := a.AccessTier
	detail := fmt.Sprintf("%q: access tier %d (%s) to %d (%s); the agent's install allows up to %d", a.Name, old, tierName(old), tier, tierName(tier), a.InstalledTier)
	if err := h.C.audited(ctx, actor, "agent-tier-changed", "agent", a.ID, detail, func() error {
		err := h.C.Store.SetAccessTier(ctx, a.ID, tier)
		if errors.Is(err, store.ErrBadState) {
			return errf(KindConflict, "agent changed state while its access was being changed")
		}
		return err
	}); err != nil {
		return a, err
	}
	Metrics.tierChanges.Add(1)
	a.AccessTier = tier
	if tier < old {
		h.narrowState(a.ID, tier) // what the server holds above the new tier goes now, not at the agent's next full picture
	}
	h.pushOne(a.ID)
	return a, nil
}

// SetConsent replaces an agent's overrides.
func (h *Hub) SetConsent(ctx context.Context, actor, agentID string, in Consent) (Consent, error) {
	a, err := h.C.agentInOrg(ctx, agentID)
	if err != nil {
		return Consent{}, err
	}
	if a.Status != store.StatusApproved {
		return Consent{}, errf(KindConflict, "agent is %s: only an approved agent can be given overrides", a.Status)
	}
	next, err := cleanConsent(in)
	if err != nil {
		return Consent{}, err
	}
	cur := h.consentOf(a.ID)
	if strings.Join(cur.Paused, ",") == strings.Join(next.Paused, ",") && strings.Join(cur.Excluded, ",") == strings.Join(next.Excluded, ",") {
		return cur, nil
	}
	detail := fmt.Sprintf("%q: paused collectors [%s] to [%s]; extra namespaces left out [%s] to [%s]", a.Name,
		strings.Join(cur.Paused, ", "), strings.Join(next.Paused, ", "), strings.Join(cur.Excluded, ", "), strings.Join(next.Excluded, ", "))
	if err := h.C.audited(ctx, actor, "agent-consent-changed", "agent", a.ID, detail, func() error {
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return h.C.Store.SaveSnapshot(ctx, consentKey(a.ID), data, h.C.Now())
	}); err != nil {
		return Consent{}, err
	}
	Metrics.consentChanges.Add(1)
	h.mu.Lock()
	v := h.viewFor(a.ID)
	v.ext.consent, v.ext.consentLoad = next, true
	h.mu.Unlock()
	h.pushOne(a.ID)
	return next, nil
}

// dropAboveTier removes from a picture whatever the approved tier does not cover. The agent already sends no more than
// it is approved for; this is the server not taking the agent's word for it.
func dropAboveTier(tier int, s *continuumv1.Sync) {
	if tier < 1 {
		s.Nodes, s.DeletedNodes = nil, nil
		if s.Cluster != nil { // storage and ingress classes are tier-1 facts
			s.Cluster.StorageClasses, s.Cluster.IngressClasses = nil, nil
		}
	}
	if tier < 2 {
		s.Namespaces, s.DeletedNamespaces, s.Workloads, s.DeletedWorkloads = nil, nil, nil, nil
		// Pod-derived node figures (pods placed, resources promised to them) come from reading pods,
		// which this approval does not cover; leave them unknown rather than reporting zero.
		for _, n := range s.Nodes {
			n.PodCount, n.CpuRequestedMillis, n.MemoryRequestedBytes = nil, 0, 0
		}
	}
}

// narrowState drops what the server holds for an agent above a tier that was just lowered, by replacing its picture with
// the same picture cut down to that tier. The agent reconnects and sends a full one at the lower tier straight after.
func (h *Hub) narrowState(id string, tier int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[id]
	if v == nil || v.state == nil || v.lastSync.IsZero() {
		return
	}
	st := v.state
	s := &continuumv1.Sync{Full: true, Seq: st.Seq}
	if st.Cluster != nil {
		s.Cluster = proto.Clone(st.Cluster).(*continuumv1.ClusterFacts)
	}
	for _, n := range st.Nodes {
		s.Nodes = append(s.Nodes, proto.Clone(n).(*continuumv1.NodeFacts))
	}
	for _, n := range st.Namespaces {
		s.Namespaces = append(s.Namespaces, n)
	}
	for _, w := range st.Workloads {
		s.Workloads = append(s.Workloads, w)
	}
	dropAboveTier(tier, s)
	st.Apply(s)
	v.dirtyAt = h.C.Now()
}

// noteCeiling records the ceiling an agent's install says it has (the Hello carries it every time, so a `helm upgrade` shows up at
// the next connection). When the owner has lowered it below what an administrator approved, the approval follows it down: the
// agent can no longer collect what was approved, and the server should not claim otherwise. Raising it changes nothing by
// itself: widening is a decision an administrator makes here, up to the new ceiling. Returns the agent as it now is.
func (h *Hub) noteCeiling(ctx context.Context, agent, fresh store.Agent, reported int) store.Agent {
	if reported <= 0 && fresh.InstalledTier > 0 && !h.reportsCeiling(agent.ID) {
		return agent // an older agent that does not say: keep what enrollment recorded
	}
	ceiling := min(max(reported, 0), MaxTier)
	actor := "agent:" + agent.ID
	if ceiling != fresh.InstalledTier {
		if err := h.C.Store.SetInstalledTier(ctx, agent.ID, ceiling); err != nil {
			if !errors.Is(err, store.ErrBadState) {
				Metrics.storeErrors.Add(1)
				h.Log.Warn("could not record the agent's installed tier", "agent", agent.ID, "err", err)
			}
			return agent
		}
		h.C.audit(ctx, actor, "agent-ceiling-changed", "agent", agent.ID, fmt.Sprintf("%q: the install now allows up to tier %d (%s), was %d", agent.Name, ceiling, tierName(ceiling), fresh.InstalledTier))
		agent.InstalledTier = ceiling
	}
	if fresh.AccessTier > ceiling {
		detail := fmt.Sprintf("%q: access tier %d (%s) to %d (%s), because the agent's install (Helm access.tier) now allows at most %d", agent.Name, fresh.AccessTier, tierName(fresh.AccessTier), ceiling, tierName(ceiling), ceiling)
		if err := h.C.audited(ctx, actor, "agent-tier-changed", "agent", agent.ID, detail, func() error { return h.C.Store.SetAccessTier(ctx, agent.ID, ceiling) }); err == nil {
			agent.AccessTier = ceiling
			h.narrowState(agent.ID, ceiling)
		}
	}
	return agent
}

// reportsCeiling says whether this agent's Hello carried diagnostics, which is how a version that states its ceiling is told apart.
func (h *Hub) reportsCeiling(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[id]
	return v != nil && v.ext.diag != nil
}

// ---- what the state document says ----

// ConsentDoc is what an administrator has asked an agent to leave out, next to what the agent says is in force.
type ConsentDoc struct {
	PausedCollectors   []string `json:"pausedCollectors"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
}

type AgentCollectorDoc struct {
	Name           string `json:"name"`
	Configured     bool   `json:"configured"`
	Enabled        bool   `json:"enabled"`
	PausedByServer bool   `json:"pausedByServer,omitempty"`
	Producing      bool   `json:"producing"`
	Reporting      int    `json:"reporting"`
	Expected       int    `json:"expected"`
	Note           string `json:"note,omitempty"`
	LastData       string `json:"lastData,omitempty"`
}

type InformerDoc struct {
	Name      string `json:"name"`
	Module    string `json:"module,omitempty"`
	Synced    bool   `json:"synced"`
	Objects   int64  `json:"objects"`
	LastError string `json:"lastError,omitempty"`
}

type ProblemDoc struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // info | warn | error
	Message  string `json:"message"`
	Since    string `json:"since,omitempty"`
}

// DiagnosticsDoc is an agent's account of itself, as the Agents page shows it. It is only in the state document for people
// who may change an agent's access (editors and administrators).
type DiagnosticsDoc struct {
	ReportedAt string `json:"reportedAt"`
	// Partial: only the ceiling and what the install enables were reported so far (the agent had not yet looked at the cluster).
	Partial       bool   `json:"partial,omitempty"`
	AgentVersion  string `json:"agentVersion"`
	Arch          string `json:"arch,omitempty"`
	OS            string `json:"os,omitempty"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	// InstalledTier is the ceiling the cluster's owner set; ApprovedTier what an administrator approved; EffectiveTier what the
	// agent is actually collecting now (the lower of the two).
	InstalledTier int `json:"installedTier"`
	ApprovedTier  int `json:"approvedTier"`
	EffectiveTier int `json:"effectiveTier"`
	// Scope is the rule in force, as counts; OwnExcluded how many namespaces the install itself leaves out by name.
	Scope       *ScopeDoc `json:"scope,omitempty"`
	OwnExcluded int       `json:"ownExcludedNamespaces,omitempty"`
	// Overrides in force on the agent right now (what it says, not what was asked for).
	PausedCollectors   []string            `json:"pausedCollectors"`
	ExcludedNamespaces int                 `json:"excludedNamespaces"`
	FlowDropped        uint64              `json:"flowDropped,omitempty"`
	Collectors         []AgentCollectorDoc `json:"collectors"`
	Informers          []InformerDoc       `json:"informers"`
	Problems           []ProblemDoc        `json:"problems"`
}

// maxDiag* bound what an agent may make the server hold about its own diagnostics.
const (
	maxDiagCollectors = 8
	maxDiagInformers  = 64
	maxDiagProblems   = 32
	maxDiagBytes      = 128 << 10
)

func tierName(t int) string {
	switch t {
	case 0:
		return "registered only"
	case 1:
		return "infrastructure"
	case 2:
		return "services"
	}
	return fmt.Sprintf("tier %d", t)
}

// noteDiagnostics keeps an agent's latest account of itself. It never refuses one: a diagnostics message that is too big is
// cut down, because an agent that is in trouble is the one whose account is most wanted. Caller holds h.mu.
func noteDiagnostics(v *view, d *continuumv1.Diagnostics, partial bool, at time.Time) {
	if d == nil {
		return
	}
	if proto.Size(d) > maxDiagBytes {
		return // keep the previous account rather than hold an oversized one
	}
	d = proto.Clone(d).(*continuumv1.Diagnostics)
	if len(d.Collectors) > maxDiagCollectors {
		d.Collectors = d.Collectors[:maxDiagCollectors]
	}
	if len(d.Informers) > maxDiagInformers {
		d.Informers = d.Informers[:maxDiagInformers]
	}
	if len(d.Problems) > maxDiagProblems {
		d.Problems = d.Problems[:maxDiagProblems]
	}
	if len(d.PausedCollectors) > maxDiagCollectors {
		d.PausedCollectors = d.PausedCollectors[:maxDiagCollectors]
	}
	v.ext.diag, v.ext.diagAt, v.ext.diagPartial = d, at, partial
}

// consentDoc is what has been asked of the agent. Caller holds h.mu and has loaded the consent.
func (v *view) consentDoc() *ConsentDoc {
	return &ConsentDoc{PausedCollectors: append([]string{}, v.ext.consent.Paused...), ExcludedNamespaces: append([]string{}, v.ext.consent.Excluded...)}
}

// diagDoc turns the stored message into its document. Every string is cut to a sensible length: it came from the agent.
// Callers already gate this to editors/administrators before calling it (see DiagnosticsDoc's own doc comment) - there
// is no further, per-tier redaction here, so it takes no tier of its own.
func (v *view) diagDoc() *DiagnosticsDoc {
	d := v.ext.diag
	if d == nil {
		return nil
	}
	out := &DiagnosticsDoc{
		ReportedAt: rfc(v.ext.diagAt), Partial: v.ext.diagPartial, AgentVersion: printable(d.AgentVersion, 40), Arch: printable(d.Arch, 16), OS: printable(d.Os, 16),
		UptimeSeconds: d.UptimeSeconds, InstalledTier: int(d.InstalledTier), ApprovedTier: int(d.ApprovedTier), EffectiveTier: int(d.EffectiveTier),
		OwnExcluded: int(d.OwnExcludedNamespaces), ExcludedNamespaces: int(d.ExcludedNamespaces), FlowDropped: d.FlowDropped,
		PausedCollectors: []string{}, Collectors: []AgentCollectorDoc{}, Informers: []InformerDoc{}, Problems: []ProblemDoc{},
	}
	if sc := d.Scope; sc != nil {
		out.Scope = &ScopeDoc{Description: printable(sc.Description, 300), Total: int(sc.NamespacesTotal), InScope: int(sc.NamespacesInScope)}
	}
	for _, p := range d.PausedCollectors {
		out.PausedCollectors = append(out.PausedCollectors, printable(p, 20))
	}
	for _, c := range d.Collectors {
		cd := AgentCollectorDoc{Name: printable(c.Name, 20), Configured: c.Configured, Enabled: c.Enabled, PausedByServer: c.PausedByServer, Producing: c.Producing,
			Reporting: int(c.Reporting), Expected: int(c.Expected), Note: printable(c.Note, 200)}
		if c.LastData != nil {
			cd.LastData = rfc(c.LastData.AsTime())
		}
		out.Collectors = append(out.Collectors, cd)
	}
	for _, i := range d.Informers {
		out.Informers = append(out.Informers, InformerDoc{Name: printable(i.Name, 80), Module: printable(i.Module, 20), Synced: i.Synced, Objects: i.Objects, LastError: printable(i.LastError, 300)})
	}
	for _, p := range d.Problems {
		pd := ProblemDoc{Code: printable(p.Code, 40), Severity: map[continuumv1.Problem_Severity]string{continuumv1.Problem_INFO: "info", continuumv1.Problem_WARN: "warn", continuumv1.Problem_ERROR: "error"}[p.Severity], Message: printable(p.Message, 700)}
		if p.Since != nil {
			pd.Since = rfc(p.Since.AsTime())
		}
		if pd.Severity == "" {
			pd.Severity = "info"
		}
		out.Problems = append(out.Problems, pd)
	}
	return out
}

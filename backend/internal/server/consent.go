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
	consentAt   time.Time // when this consent was last changed (zero if it never has been); drives the staleness signal in consentDoc
	consentLoad bool
	// namespace is this agent's own Kubernetes namespace, from its Hello (empty before an agent that reports it has
	// connected at least once since this server started; not persisted, same as diagnostics). Used to print a
	// helm command against the release that is actually running instead of guessing continuum-system.
	namespace string
	// releaseName is the Helm release this agent's Hello says it was installed as (empty under the same conditions as
	// namespace). Every object the chart creates has a fixed name regardless of the release, so this only matters for
	// the two `helm` commands themselves; guessed as "continuum-agent" when unknown, same as namespace.
	releaseName string
	// knownNamespaces are the Kubernetes namespace names this agent has actually reported, captured from a Sync
	// after the tier ceiling narrows it but before an admin's own exclusion list does - so an excluded name can
	// still be checked against what is real, which is the whole point of tracking it (see ConsentDoc.UnknownNamespaces).
	// Only names are kept, never a namespace's own facts, so nothing more survives its exclusion than the fact it
	// exists. Best-effort like the rest of viewExt: not persisted, rebuilt from the agent's next full picture. A
	// full sync replaces this outright; a delta sync only adds to it, so a namespace that genuinely disappears
	// between two full syncs stays "known" until the next one rather than needing DeletedNamespaces (which carries
	// keys, not names) resolved back to a name here too - an acceptable staleness for a signal that only ever warns.
	knownNamespaces map[string]bool
}

// noteNamespaces folds a batch of namespace names an agent just reported into what it is known to have,
// for later checking an excluded name against. full replaces the set outright (mirrors facts.State.Apply's
// own handling of a full sync); otherwise names are only ever added.
func (e *viewExt) noteNamespaces(names []string, full bool) {
	if full {
		e.knownNamespaces = make(map[string]bool, len(names))
	} else if e.knownNamespaces == nil {
		e.knownNamespaces = make(map[string]bool, len(names))
	}
	for _, n := range names {
		e.knownNamespaces[n] = true
	}
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
	var at time.Time
	if data, savedAt, err := h.C.Store.LoadSnapshot(bg(), consentKey(id)); err == nil {
		at = savedAt
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
		v.ext.consent, v.ext.consentAt, v.ext.consentLoad = c, at, true
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

// ConsentDocFor is consentDoc for one agent by id: what has been asked of it, and whether it has confirmed
// applying it. Used wherever an HTTP handler needs to hand back the same shape the agent list already
// carries per agent (see AgentDoc.Consent) - so a client reading either one sees Confirmed and SetAt
// computed the same way, not a bare echo of what was just asked for.
func (h *Hub) ConsentDocFor(id string) *ConsentDoc {
	h.mu.Lock()
	v := h.viewFor(id)
	h.mu.Unlock()
	h.ensureConsent(id, v)
	h.mu.Lock()
	defer h.mu.Unlock()
	return v.consentDoc()
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
	// The only real ceiling below is ImplementedTier: MaxTier is the type's absolute upper bound (tiers reserved
	// for a future release), not a separate limit worth its own message, and duplicating "the highest allowed
	// tier" as two different numbers in two different checks is exactly how enroll.go's approval-time check and
	// this one drifted apart in wording before - a tier of 5+ used to be rejected citing "0-2" as the range
	// while MaxTier=4 said otherwise. Only a genuinely out-of-domain value (negative) gets its own message here.
	if tier < 0 {
		return a, errf(KindInvalid, "access tier must be 0 or more")
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
	h.C.recordAgentGraph(ctx, a, "agent-tier-changed", detail)
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
	now := h.C.Now()
	if err := h.C.audited(ctx, actor, "agent-consent-changed", "agent", a.ID, detail, func() error {
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return h.C.Store.SaveSnapshot(ctx, consentKey(a.ID), data, now)
	}); err != nil {
		return Consent{}, err
	}
	Metrics.consentChanges.Add(1)
	h.mu.Lock()
	v := h.viewFor(a.ID)
	v.ext.consent, v.ext.consentAt, v.ext.consentLoad = next, now, true
	h.mu.Unlock()
	if consentNarrows(cur, next) {
		h.narrowConsent(a.ID, next)
	}
	h.C.recordAgentGraph(ctx, a, "agent-consent-changed", detail)
	h.pushOne(a.ID)
	return next, nil
}

// dropConsentOverrides removes from a picture whatever this agent has been asked to narrow beyond its tier: a
// paused node-probe collector's machine-identifying fields, and any namespace (or workload in one) on the excluded
// list. The agent already applies both itself and should never send them; this is dropAboveTier's own defense in
// depth, extended to the two narrowing dimensions a tier check does not cover. flow and measure are dropped
// whole, before they are ever applied, at the AgentMessage_Flows/AgentMessage_Measurements cases in Hub's stream
// loop -- they arrive as their own message type, so there is nothing to filter here.
//
// A dependency a paused-but-still-arriving flow batch named, or a flow naming a workload in an excluded
// namespace, is not separately hunted down here: the workload it would have pointed at is never recorded (this
// function already drops it), so the edge has nothing to attach to and stays inert rather than invisible. Worth
// tightening later if that residual is ever a problem in practice; not attempted here because it would mean
// parsing a flow key's namespace reliably enough to trust filtering by it.
func (h *Hub) dropConsentOverrides(id string, s *continuumv1.Sync) {
	applyConsent(h.consentOf(id), s)
}

// applyConsent cuts a picture down to what an agent's overrides leave it: the node probe's machine-identifying
// fields when that collector is paused, and every namespace (and workload in one) on the excluded list.
func applyConsent(c Consent, s *continuumv1.Sync) {
	if c.has("probes") {
		for _, n := range s.Nodes {
			n.MachineId, n.SystemUuid, n.ProviderId = "", "", ""
			n.OsImage, n.KernelVersion, n.ContainerRuntime, n.KubeletVersion, n.Architecture = "", "", "", "", ""
		}
	}
	if len(c.Excluded) == 0 {
		return
	}
	excluded := make(map[string]bool, len(c.Excluded))
	for _, ns := range c.Excluded {
		excluded[ns] = true
	}
	namespaces := make([]*continuumv1.NamespaceFacts, 0, len(s.Namespaces))
	for _, n := range s.Namespaces {
		if !excluded[n.Name] {
			namespaces = append(namespaces, n)
		}
	}
	s.Namespaces = namespaces
	workloads := make([]*continuumv1.WorkloadFacts, 0, len(s.Workloads))
	for _, w := range s.Workloads {
		if !excluded[w.Namespace] {
			workloads = append(workloads, w)
		}
	}
	s.Workloads = workloads
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

// narrowConsent is narrowState for the overrides: what the server already holds for an agent that has just had a
// collector paused or a namespace left out goes now, not whenever the agent next sends a full picture (which a stuck,
// disconnected or out-of-date agent never would). The picture is replaced by itself cut down to the new overrides.
func (h *Hub) narrowConsent(id string, c Consent) {
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
		s.Nodes = append(s.Nodes, proto.Clone(n).(*continuumv1.NodeFacts)) // cloned: applyConsent blanks fields in place
	}
	for _, n := range st.Namespaces {
		s.Namespaces = append(s.Namespaces, n)
	}
	for _, w := range st.Workloads {
		s.Workloads = append(s.Workloads, w)
	}
	applyConsent(c, s)
	st.Apply(s)
	v.dirtyAt = h.C.Now()
}

// consentNarrows is whether next takes anything away that cur still allowed: a collector newly paused, or a
// namespace newly left out. Widening needs no purge (there is nothing held to drop).
func consentNarrows(cur, next Consent) bool {
	for _, p := range next.Paused {
		if !cur.has(p) {
			return true
		}
	}
	had := make(map[string]bool, len(cur.Excluded))
	for _, n := range cur.Excluded {
		had[n] = true
	}
	for _, n := range next.Excluded {
		if !had[n] {
			return true
		}
	}
	return false
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
		ceilingDetail := fmt.Sprintf("%q: the install now allows up to tier %d (%s), was %d", agent.Name, ceiling, tierName(ceiling), fresh.InstalledTier)
		h.C.audit(ctx, actor, "agent-ceiling-changed", "agent", agent.ID, ceilingDetail)
		agent.InstalledTier = ceiling
		h.C.recordAgentGraph(ctx, agent, "agent-ceiling-changed", ceilingDetail)
	}
	if fresh.AccessTier > ceiling {
		detail := fmt.Sprintf("%q: access tier %d (%s) to %d (%s), because the agent's install (Helm access.tier) now allows at most %d", agent.Name, fresh.AccessTier, tierName(fresh.AccessTier), ceiling, tierName(ceiling), ceiling)
		if err := h.C.audited(ctx, actor, "agent-tier-changed", "agent", agent.ID, detail, func() error { return h.C.Store.SetAccessTier(ctx, agent.ID, ceiling) }); err == nil {
			agent.AccessTier = ceiling
			h.narrowState(agent.ID, ceiling)
			h.C.recordAgentGraph(ctx, agent, "agent-tier-changed", detail)
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

// ConsentDoc is what an administrator has asked an agent to leave out, next to whether the agent has
// caught up with it.
type ConsentDoc struct {
	PausedCollectors   []string `json:"pausedCollectors"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
	// Confirmed is true once the agent's own diagnostics show it applying this narrowing (or there is
	// nothing narrowed to confirm in the first place). False means either nothing has been heard from the
	// agent since the change, or its last report still shows the narrowing not yet in force - a paused
	// stream, a stuck rollout, a version too old to report it at all.
	Confirmed bool `json:"confirmed"`
	// SetAt is when this narrowing was last changed, present only while it is not yet Confirmed: the
	// observability-intent panel's staleness signal - an elapsed-time indicator the UI can show next to a
	// narrowing that has been waiting on the agent, rather than leaving the person to guess whether "not
	// yet confirmed" means five seconds or five days.
	SetAt string `json:"setAt,omitempty"`
	// UnknownNamespaces are excluded namespace names this agent has never actually reported, checked once
	// it has reported anything at all to check against. cleanConsent only rejects a name that cannot be a
	// namespace (bad syntax, a system namespace); it cannot know whether "checkout-v2" is a typo for
	// "checkout" or a namespace that simply has not rolled out yet, and an administrator may legitimately
	// pre-declare one before its first workload appears - so this warns instead of blocking the request
	// that set it. Recomputed on every read against the agent's current picture rather than judged once at
	// set-time, so a name clears itself the moment the agent reports it, and a typo keeps showing up rather
	// than being forgotten after the one response that first flagged it.
	UnknownNamespaces []string `json:"unknownNamespaces,omitempty"`
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
	// InstalledTelemetry is which telemetry signals this agent's chart install enabled (telemetry.*.enabled),
	// by name - purely informational, exactly like InstalledTier: this server never pushes or changes it, and
	// it never affects discovery. Empty: no telemetry signal installed, or an agent older than this field.
	InstalledTelemetry []string `json:"installedTelemetry,omitempty"`
	// InstalledTelemetryConfig is the effective configuration behind InstalledTelemetry above - not just
	// which signals are on, but how each is actually configured (export destination, the processor settings
	// that matter most for "safe by default", and which source backs energy/accelerators). Same purely
	// informational, never-pushed-by-this-server treatment as InstalledTier/InstalledTelemetry. Nil: no
	// telemetry signal installed, or an agent older than this field.
	InstalledTelemetryConfig *TelemetryConfigDoc `json:"installedTelemetryConfig,omitempty"`
	// ExportHealth is whether each telemetry destination is actually receiving data, as the agent last read
	// it from the collectors' own export counters. Nil: the agent is not reading them (telemetry not
	// installed, health turned off in the chart, or an agent older than this field).
	ExportHealth *ExportHealthDoc `json:"exportHealth,omitempty"`
}

// ExportHealthDoc is DiagnosticsDoc.ExportHealth: the state the agent worked out for each route, relayed
// as it is. The agent decides because only it reads the counters in order against one clock (see the
// exporthealth package); this server only bounds and labels what it is told. PodsReached 0 with PodsFailed
// above 0 means the counters cannot be read at all, which says nothing about whether data is flowing.
type ExportHealthDoc struct {
	ScrapedAt   string                 `json:"scrapedAt,omitempty"`
	PodsReached int                    `json:"podsReached"`
	PodsFailed  int                    `json:"podsFailed"`
	LastError   string                 `json:"lastError,omitempty"`
	Routes      []ExportRouteHealthDoc `json:"routes"`
}

// ExportRouteHealthDoc is one exporter's state for one signal type. State is waiting, exporting, silent or
// failing; Sent and Failed are summed over the collector pods since each started (so they can go down when a
// pod restarts), and the times are when the agent last saw each grow.
type ExportRouteHealthDoc struct {
	Exporter     string `json:"exporter"`
	Signal       string `json:"signal"`
	State        string `json:"state"`
	Sent         uint64 `json:"sent"`
	Failed       uint64 `json:"failed"`
	LastSentAt   string `json:"lastSentAt,omitempty"`
	LastFailedAt string `json:"lastFailedAt,omitempty"`
}

// exportHealthStates names the agent's states; one this server does not know is shown as waiting, the one
// that claims nothing.
var exportHealthStates = map[continuumv1.ExportRouteHealth_State]string{
	continuumv1.ExportRouteHealth_WAITING:   "waiting",
	continuumv1.ExportRouteHealth_EXPORTING: "exporting",
	continuumv1.ExportRouteHealth_SILENT:    "silent",
	continuumv1.ExportRouteHealth_FAILING:   "failing",
}

// TelemetryConfigDoc is the effective configuration behind DiagnosticsDoc.InstalledTelemetryConfig - see
// InstalledTelemetryConfig's own doc comment in agent.proto for why each field is shaped as it is.
// EnergySource/AcceleratorsSource are plain strings (empty already means "that signal is not enabled", the
// same meaning their absence would carry) rather than pointers; TracesSamplingPercent is a pointer because
// 0 is a real, meaningful percentage (drop every span) that must stay distinguishable from "traces is not
// enabled" - the same ambiguous-zero problem SelfTelemetrySample.CPUPct's own doc comment names.
type TelemetryConfigDoc struct {
	ExportEndpoint           string  `json:"exportEndpoint"`
	RedactionEnabled         bool    `json:"redactionEnabled"`
	ResourceDetectionEnabled bool    `json:"resourceDetectionEnabled"`
	TracesSamplingPercent    *uint32 `json:"tracesSamplingPercent,omitempty"`
	EnergySource             string  `json:"energySource,omitempty"`
	AcceleratorsSource       string  `json:"acceleratorsSource,omitempty"`
}

// maxDiag* bound what an agent may make the server hold about its own diagnostics.
const (
	maxDiagCollectors       = 8
	maxDiagInformers        = 64
	maxDiagProblems         = 32
	maxDiagBytes            = 128 << 10
	maxDiagTelemetrySignals = 16 // ten known signal names today; generous headroom, not unbounded
	maxDiagExportRoutes     = 32 // one per exporter and signal type: a handful in practice
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
	if len(d.InstalledTelemetrySignals) > maxDiagTelemetrySignals {
		d.InstalledTelemetrySignals = d.InstalledTelemetrySignals[:maxDiagTelemetrySignals]
	}
	if h := d.ExportHealth; h != nil && len(h.Routes) > maxDiagExportRoutes {
		h.Routes = h.Routes[:maxDiagExportRoutes]
	}
	v.ext.diag, v.ext.diagAt, v.ext.diagPartial = d, at, partial
}

// consentDoc is what has been asked of the agent, and whether it has confirmed applying it. Caller holds
// h.mu and has loaded the consent.
func (v *view) consentDoc() *ConsentDoc {
	out := &ConsentDoc{PausedCollectors: append([]string{}, v.ext.consent.Paused...), ExcludedNamespaces: append([]string{}, v.ext.consent.Excluded...)}
	if len(out.PausedCollectors) == 0 && len(out.ExcludedNamespaces) == 0 {
		out.Confirmed = true // nothing narrowed here, so there is nothing for the agent to confirm
		return out
	}
	if !v.lastSync.IsZero() {
		// v.ext.knownNamespaces, not v.state.Namespaces: the latter has already had any currently-excluded
		// namespace stripped out of it by dropConsentOverrides by the time it is stored, so it can never
		// contain the very names this exists to check (see viewExt.knownNamespaces's own doc comment).
		for _, ns := range out.ExcludedNamespaces {
			if !v.ext.knownNamespaces[ns] {
				out.UnknownNamespaces = append(out.UnknownNamespaces, ns)
			}
		}
	}
	d := v.ext.diag
	if d != nil {
		// Collector names are matched exactly - the agent reports the actual list, sorted the same way
		// cleanConsent already sorts what was asked for. Excluded namespaces can only be matched by count:
		// the agent's diagnostics carry how many extra namespaces it is leaving out, never their names (see
		// Diagnostics.ExcludedNamespaces's own doc comment), so an agent that coincidentally excludes a
		// different set of the same size would misreport as confirmed here. Good enough for the case this
		// exists for - a narrowing a stuck or too-old agent never picked up at all - and tightening it
		// further needs a proto change to carry the names, not just a count.
		pausedConfirmed := sameStrings(d.PausedCollectors, v.ext.consent.Paused)
		excludedConfirmed := int(d.ExcludedNamespaces) == len(v.ext.consent.Excluded)
		out.Confirmed = pausedConfirmed && excludedConfirmed
	}
	// out.Confirmed is false here either because a report arrived that does not yet match, or because none
	// has arrived at all (d == nil) - both are "not yet confirmed" and both deserve the same elapsed-time
	// signal, so SetAt is filled in the same way regardless of which.
	if !out.Confirmed && !v.ext.consentAt.IsZero() {
		out.SetAt = rfc(v.ext.consentAt)
	}
	return out
}

// sameStrings says whether a and b hold the same strings, regardless of order or duplicates.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
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
	for _, s := range d.InstalledTelemetrySignals {
		out.InstalledTelemetry = append(out.InstalledTelemetry, printable(s, 40))
	}
	if tc := d.InstalledTelemetryConfig; tc != nil {
		cfg := &TelemetryConfigDoc{
			ExportEndpoint:           printable(tc.ExportEndpoint, 300),
			RedactionEnabled:         tc.RedactionEnabled,
			ResourceDetectionEnabled: tc.ResourceDetectionEnabled,
			EnergySource:             printable(tc.GetEnergySource(), 40),
			AcceleratorsSource:       printable(tc.GetAcceleratorsSource(), 40),
		}
		if tc.TracesSamplingPercentage != nil {
			v := *tc.TracesSamplingPercentage
			cfg.TracesSamplingPercent = &v
		}
		out.InstalledTelemetryConfig = cfg
	}
	if h := d.ExportHealth; h != nil {
		doc := &ExportHealthDoc{PodsReached: int(h.PodsReached), PodsFailed: int(h.PodsFailed), LastError: printable(h.LastError, 300), Routes: []ExportRouteHealthDoc{}}
		if h.ScrapedAt != nil {
			doc.ScrapedAt = rfc(h.ScrapedAt.AsTime())
		}
		for _, r := range h.Routes {
			state := exportHealthStates[r.State]
			if state == "" {
				state = "waiting"
			}
			rd := ExportRouteHealthDoc{Exporter: printable(r.Exporter, 80), Signal: printable(r.Signal, 20), State: state, Sent: r.Sent, Failed: r.Failed}
			if r.LastSentAt != nil {
				rd.LastSentAt = rfc(r.LastSentAt.AsTime())
			}
			if r.LastFailedAt != nil {
				rd.LastFailedAt = rfc(r.LastFailedAt.AsTime())
			}
			doc.Routes = append(doc.Routes, rd)
		}
		out.ExportHealth = doc
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

// ---- versioning an agent's own state in the graph ----

// AgentSnapshot is what the graph remembers about an agent over time: never an identity secret (CSR,
// poll secret, leaf certificate, approval hash - those stay in the inner store, which is where trust is
// actually checked and which works whether or not a graph is configured at all), only what a person
// looking at this agent's history would want to see - its access, and the discovery intent narrowing it.
type AgentSnapshot struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	ClusterID     string `json:"clusterId,omitempty"`
	InstalledTier int    `json:"installedTier"`
	TierCap       int    `json:"tierCap"`
	AccessTier    int    `json:"accessTier"`
	Version       string `json:"version,omitempty"`
	K8sVersion    string `json:"k8sVersion,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	ApprovedAt    string `json:"approvedAt,omitempty"`
	ApprovedBy    string `json:"approvedBy,omitempty"`
	RevokedAt     string `json:"revokedAt,omitempty"`
	Reason        string `json:"reason,omitempty"`
	LastSeen      string `json:"lastSeen,omitempty"`
	// PausedCollectors and ExcludedNamespaces are the discovery intent: what an administrator has asked
	// this agent to leave out, on top of whatever its own install (Helm RBAC/scope) already leaves out.
	PausedCollectors   []string `json:"pausedCollectors,omitempty"`
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
}

// HistoricAgent is an agent's graph-recorded state as of some past moment, in the shape the frontend
// overlays onto its live Agent type to answer "what did this agent look like then" -- AgentSnapshot's
// fields (deliberately narrower than the live type: never an identity secret) plus the id that ties it
// back to a specific agent, which AgentSnapshot itself does not carry since the graph already keys its
// Version nodes by id separately from the doc.
type HistoricAgent struct {
	ID string `json:"id"`
	AgentSnapshot
}

func agentSnapshot(a store.Agent, c Consent) AgentSnapshot {
	s := AgentSnapshot{
		Name: a.Name, Status: string(a.Status), ClusterID: a.ClusterID,
		InstalledTier: a.InstalledTier, TierCap: a.TierCap, AccessTier: a.AccessTier,
		Version: a.Version, K8sVersion: a.K8sVersion, Reason: a.Reason,
		PausedCollectors: c.Paused, ExcludedNamespaces: c.Excluded,
	}
	if !a.CreatedAt.IsZero() {
		s.CreatedAt = a.CreatedAt.UTC().Format(time.RFC3339)
	}
	if a.ApprovedAt != nil {
		s.ApprovedAt, s.ApprovedBy = a.ApprovedAt.UTC().Format(time.RFC3339), a.ApprovedBy
	}
	if a.RevokedAt != nil {
		s.RevokedAt = a.RevokedAt.UTC().Format(time.RFC3339)
	}
	if a.LastSeen != nil {
		s.LastSeen = a.LastSeen.UTC().Format(time.RFC3339)
	}
	return s
}

// consentFor loads an agent's overrides straight from the store, for a caller that is not the hub's live
// view of a connected agent (recording history is one). Falls back to none rather than failing: a graph
// version with no discovery intent recorded is far better than one skipped entirely over this.
func consentFor(ctx context.Context, st store.Store, agentID string) Consent {
	data, _, err := st.LoadSnapshot(ctx, consentKey(agentID))
	if err != nil {
		return Consent{}
	}
	var c Consent
	if json.Unmarshal(data, &c) != nil {
		return Consent{}
	}
	if cc, err := cleanConsent(c); err == nil {
		return cc
	}
	return Consent{}
}

// entityRecorder is what a store adds when it keeps a graph: the ability to version one entity's state
// directly, the moment something about it changed (see graph.DB.RecordEntity), rather than waiting to be
// noticed by comparing two periodic scans. A plain store does not implement it, and that is fine - this
// is enrichment on top of the audit trail, never a gate on it.
type entityRecorder interface {
	RecordEntity(ctx context.Context, org string, at time.Time, kind, id, name, status, cluster string, doc any) error
}

// recordAgentGraph versions an agent's non-secret state in the graph, right after something changed it -
// its tier, its consent, its status - and, when the caller names one, records why: an Event with the same
// kind and detail already written to the audit trail a moment ago, linked to the version it produced (see
// graph.DB.LinkEventChanges) exactly the way the polled kinds' own differ links its events. kind is a
// dash-named action ("agent-tier-changed", "agent-consent-changed", ...), the same vocabulary the audit
// trail already uses; pass "" to version the state without an event, for a caller with nothing worth
// explaining (there is none among the ones in this codebase, but a future one may only need the snapshot).
// Best-effort and silent on failure by design: the action itself is already durably audited (see audited)
// before this ever runs, so a graph outage narrows the timeline view of the change, never the change
// itself.
func (c *Core) recordAgentGraph(ctx context.Context, a store.Agent, kind, detail string) {
	er, ok := c.Store.(entityRecorder)
	if !ok {
		return
	}
	now := c.Now()
	snap := agentSnapshot(a, consentFor(ctx, c.Store, a.ID))
	if err := er.RecordEntity(ctx, c.OrgID, now, "agent", a.ID, a.Name, string(a.Status), a.ClusterID, snap); err != nil {
		c.Log.Warn("history: could not record the agent's state", "agent", a.ID, "err", err)
		return
	}
	if kind == "" {
		return
	}
	ev := store.Event{At: now, Kind: kind, TargetKind: "agent", TargetID: a.ID, Name: a.Name, ClusterID: a.ClusterID, Detail: detail, Severity: "info"}
	if err := c.Store.AddEvents(ctx, c.OrgID, []store.Event{ev}); err != nil {
		c.Log.Warn("history: could not record why the agent's state changed", "agent", a.ID, "err", err)
		return
	}
	if el, ok := c.Store.(eventLinker); ok {
		if err := el.LinkEventChanges(ctx, c.OrgID, now, []store.Event{ev}); err != nil {
			c.Log.Warn("history: could not link the agent's event to what it explains", "agent", a.ID, "err", err)
		}
	}
}

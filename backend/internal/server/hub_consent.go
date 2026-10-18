package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agentcaps"
	"continuum/internal/agents"
	"continuum/internal/kernel"
	"continuum/internal/store"

	"google.golang.org/protobuf/proto"
)

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
	loaded := v.Ext.ConsentLoad
	h.mu.Unlock()
	if loaded {
		return
	}
	var c agents.Consent
	var at time.Time
	if data, savedAt, err := h.C.Store.LoadSnapshot(bg(), agents.ConsentKey(id)); err == nil {
		at = savedAt
		if json.Unmarshal(data, &c) != nil {
			c = agents.Consent{}
		} else if cc, err := agents.CleanConsent(c); err == nil {
			c = cc // stored data is not trusted more than a request
		} else {
			c = agents.Consent{}
		}
	}
	h.mu.Lock()
	if !v.Ext.ConsentLoad {
		v.Ext.Consent, v.Ext.ConsentAt, v.Ext.ConsentLoad = c, at, true
	}
	h.mu.Unlock()
}

// NamespaceOf returns the Kubernetes namespace an agent last reported for itself, or "" if it never has (an older
// agent, or one that has not connected since this server started).
func (h *Hub) NamespaceOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[id]; v != nil {
		return v.Ext.Namespace
	}
	return ""
}

// ReleaseNameOf returns the Helm release an agent last reported it was installed as, or "" under the same conditions
// as NamespaceOf.
func (h *Hub) ReleaseNameOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[id]; v != nil {
		return v.Ext.ReleaseName
	}
	return ""
}

// consentOf returns an agent's overrides.
func (h *Hub) consentOf(id string) agents.Consent {
	h.mu.Lock()
	v := h.viewFor(id)
	h.mu.Unlock()
	h.ensureConsent(id, v)
	h.mu.Lock()
	defer h.mu.Unlock()
	return v.Ext.Consent
}

// ConsentDocFor is consentDoc for one agent by id: what has been asked of it, and whether it has confirmed
// applying it. Used wherever an HTTP handler needs to hand back the same shape the agent list already
// carries per agent (see AgentDoc.Consent) - so a client reading either one sees Confirmed and SetAt
// computed the same way, not a bare echo of what was just asked for.
func (h *Hub) ConsentDocFor(id string) *agents.ConsentDoc {
	h.mu.Lock()
	v := h.viewFor(id)
	h.mu.Unlock()
	h.ensureConsent(id, v)
	h.mu.Lock()
	defer h.mu.Unlock()
	return v.consentDoc()
}

// SetAgentTier changes what a human approved for an agent that is already approved. Anything up to the ceiling the agent's
// install has is allowed, narrower or wider than before (widening goes back up to what the owner installed, never past it,
// and takes an administrator); a tier above the ceiling is refused with the exact command the cluster's owner has to run to
// raise it. upgradeCmd builds that command for a tier.
func (h *Hub) SetAgentTier(ctx context.Context, actor, agentID string, tier int, upgradeCmd func(tier int) string) (store.Agent, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return a, err
	}
	if a.Status != store.StatusApproved {
		return a, kernel.Errf(kernel.KindConflict, "agent is %s: only an approved agent's access can be changed", a.Status)
	}
	// The only real ceiling below is ImplementedTier: MaxTier is the type's absolute upper bound (tiers reserved
	// for a future release), not a separate limit worth its own message, and duplicating "the highest allowed
	// tier" as two different numbers in two different checks is exactly how enroll.go's approval-time check and
	// this one drifted apart in wording before - a tier of 5+ used to be rejected citing "0-2" as the range
	// while MaxTier=4 said otherwise. Only a genuinely out-of-domain value (negative) gets its own message here.
	verdict := agents.CheckTier(tier, a.InstalledTier, a.TierCap)
	if verdict == agents.TierBelowZero {
		return a, kernel.Errf(kernel.KindInvalid, "access tier must be 0 or more")
	}
	if tier == a.AccessTier {
		return a, nil
	}
	if !accounts.Decide(accounts.CallerFrom(ctx), accounts.ActChangeAccess, accounts.Resource{Widening: tier > a.AccessTier}).Allowed {
		return a, kernel.Errf(kernel.KindForbidden, "raising an agent's access takes an administrator, as approving it did; narrowing it does not")
	}
	if verdict == agents.TierNotImplemented {
		return a, kernel.Errf(kernel.KindInvalid, "access tier %d is not available in this release (the highest is %d)", tier, kernel.ImplementedTier)
	}
	if verdict == agents.TierAboveInstall {
		cmd := ""
		if upgradeCmd != nil {
			cmd = upgradeCmd(tier)
		}
		e := kernel.Errf(kernel.KindInvalid, "this agent's install allows at most tier %d (its Helm value access.tier), and only the cluster's owner can raise that. Run this in that cluster, then set the tier here:\n%s", a.InstalledTier, cmd)
		e.Data = map[string]any{"installedTier": a.InstalledTier, "helm": cmd}
		return a, e
	}
	if verdict == agents.TierAboveToken {
		return a, kernel.Errf(kernel.KindInvalid, "access tier %d is above what the enrollment token allowed for this agent (%d)", tier, a.TierCap)
	}
	old := a.AccessTier
	detail := fmt.Sprintf("%q: access tier %d (%s) to %d (%s); the agent's install allows up to %d", a.Name, old, agents.TierName(old), tier, agents.TierName(tier), a.InstalledTier)
	err = h.C.Store.SetAccessTier(ctx, a.ID, tier, store.WithAudit(h.C.AuditRow(h.C.OrgID, actor, "agent-tier-changed", "agent", a.ID, detail)))
	if errors.Is(err, store.ErrBadState) {
		err = kernel.Errf(kernel.KindConflict, "agent changed state while its access was being changed")
	}
	if err := h.C.AuditedWrite(err, "agent-tier-changed"); err != nil {
		return a, err
	}
	kernel.Metrics.TierChanges.Add(1)
	a.AccessTier = tier
	if tier < old {
		h.narrowState(a.ID, tier) // what the server holds above the new tier goes now, not at the agent's next full picture
	}
	h.C.Agents.RecordAgentGraph(ctx, a, "agent-tier-changed", detail)
	h.pushOne(a.ID)
	return a, nil
}

// SetConsent replaces an agent's overrides. Lifting a pause or an exclusion widens what is shared, so it takes an administrator.
func (h *Hub) SetConsent(ctx context.Context, actor, agentID string, in agents.Consent) (agents.Consent, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return agents.Consent{}, err
	}
	if a.Status != store.StatusApproved {
		return agents.Consent{}, kernel.Errf(kernel.KindConflict, "agent is %s: only an approved agent can be given overrides", a.Status)
	}
	next, err := agents.CleanConsent(in)
	if err != nil {
		return agents.Consent{}, err
	}
	cur := h.consentOf(a.ID)
	if !accounts.Decide(accounts.CallerFrom(ctx), accounts.ActChangeAccess, accounts.Resource{Widening: agents.ConsentNarrows(next, cur)}).Allowed { // cur has a pause or exclusion that next lifts
		return agents.Consent{}, kernel.Errf(kernel.KindForbidden, "lifting a pause or an exclusion takes an administrator; adding one does not")
	}
	if strings.Join(cur.Paused, ",") == strings.Join(next.Paused, ",") && strings.Join(cur.Excluded, ",") == strings.Join(next.Excluded, ",") {
		return cur, nil
	}
	detail := fmt.Sprintf("%q: paused collectors [%s] to [%s]; extra namespaces left out [%s] to [%s]", a.Name,
		strings.Join(cur.Paused, ", "), strings.Join(next.Paused, ", "), strings.Join(cur.Excluded, ", "), strings.Join(next.Excluded, ", "))
	now := h.C.Now()
	data, err := json.Marshal(next)
	if err != nil {
		return agents.Consent{}, err
	}
	if err := h.C.AuditedWrite(h.C.Store.SaveSnapshot(ctx, agents.ConsentKey(a.ID), data, now,
		store.WithAudit(h.C.AuditRow(h.C.OrgID, actor, "agent-consent-changed", "agent", a.ID, detail))), "agent-consent-changed"); err != nil {
		return agents.Consent{}, err
	}
	kernel.Metrics.ConsentChanges.Add(1)
	h.mu.Lock()
	v := h.viewFor(a.ID)
	v.Ext.Consent, v.Ext.ConsentAt, v.Ext.ConsentLoad = next, now, true
	h.mu.Unlock()
	if agents.ConsentNarrows(cur, next) {
		h.narrowConsent(a.ID, next)
	}
	h.C.Agents.RecordAgentGraph(ctx, a, "agent-consent-changed", detail)
	h.pushOne(a.ID)
	return next, nil
}

// dropConsentOverrides removes from a picture whatever this agent has been asked to narrow beyond its tier: a
// paused node-probe collector's machine-identifying fields, and any namespace (or workload in one) on the excluded
// list. The agent already applies both itself and should never send them; this is agents.DropAboveTier's own defense in
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
	agents.ApplyConsent(h.consentOf(id), s)
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
	agents.DropAboveTier(tier, s)
	st.Apply(s)
	v.dirtyAt = h.C.Now()
}

// narrowConsent is narrowState for the overrides: what the server already holds for an agent that has just had a
// collector paused or a namespace left out goes now, not whenever the agent next sends a full picture (which a stuck,
// disconnected or out-of-date agent never would). The picture is replaced by itself cut down to the new overrides.
func (h *Hub) narrowConsent(id string, c agents.Consent) {
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
		s.Nodes = append(s.Nodes, proto.Clone(n).(*continuumv1.NodeFacts)) // cloned: agents.ApplyConsent blanks fields in place
	}
	for _, n := range st.Namespaces {
		s.Namespaces = append(s.Namespaces, n)
	}
	for _, w := range st.Workloads {
		s.Workloads = append(s.Workloads, w)
	}
	agents.ApplyConsent(c, s)
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
	ceiling, _ := agents.ReportedTier(reported)
	actor := accounts.AgentActor(agent.ID).String()
	if ceiling != fresh.InstalledTier {
		if err := h.C.Store.SetInstalledTier(ctx, agent.ID, ceiling); err != nil {
			if !errors.Is(err, store.ErrBadState) {
				kernel.Metrics.StoreErrors.Add(1)
				h.Log.Warn("could not record the agent's installed tier", "agent", agent.ID, "err", err)
			}
			return agent
		}
		ceilingDetail := fmt.Sprintf("%q: the install now allows up to tier %d (%s), was %d", agent.Name, ceiling, agents.TierName(ceiling), fresh.InstalledTier)
		h.C.Audit(ctx, actor, "agent-ceiling-changed", "agent", agent.ID, ceilingDetail)
		agent.InstalledTier = ceiling
		h.C.Agents.RecordAgentGraph(ctx, agent, "agent-ceiling-changed", ceilingDetail)
	}
	if fresh.AccessTier > ceiling {
		detail := fmt.Sprintf("%q: access tier %d (%s) to %d (%s), because the agent's install (Helm access.tier) now allows at most %d", agent.Name, fresh.AccessTier, agents.TierName(fresh.AccessTier), ceiling, agents.TierName(ceiling), ceiling)
		if err := h.C.AuditedWrite(h.C.Store.SetAccessTier(ctx, agent.ID, ceiling,
			store.WithAudit(h.C.AuditRow(h.C.OrgID, actor, "agent-tier-changed", "agent", agent.ID, detail))), "agent-tier-changed"); err == nil {
			agent.AccessTier = ceiling
			h.narrowState(agent.ID, ceiling)
			h.C.Agents.RecordAgentGraph(ctx, agent, "agent-tier-changed", detail)
		}
	}
	return agent
}

// reportsCeiling says whether this agent states the ceiling of its install in its Hello, which is what makes a 0 there a statement
// and not a field that was never filled in. An agent that states a protocol version says so by declaring the `diagnostics`
// capability. One that does not (protocol 0) is told apart as it always was, by whether its Hello carried diagnostics.
func (h *Hub) reportsCeiling(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[id]
	if v == nil {
		return false
	}
	if v.Ext.Protocol >= 1 {
		return slices.Contains(v.Ext.Caps, agentcaps.Diagnostics)
	}
	return v.Ext.Diag != nil
}

// consentDoc is what has been asked of the agent, and whether it has confirmed applying it. Caller holds
// h.mu and has loaded the consent.
func (v *view) consentDoc() *agents.ConsentDoc {
	out := &agents.ConsentDoc{PausedCollectors: append([]string{}, v.Ext.Consent.Paused...), ExcludedNamespaces: append([]string{}, v.Ext.Consent.Excluded...)}
	if len(out.PausedCollectors) == 0 && len(out.ExcludedNamespaces) == 0 {
		out.Confirmed = true // nothing narrowed here, so there is nothing for the agent to confirm
		return out
	}
	if !v.lastSync.IsZero() {
		// v.ext.knownNamespaces, not v.state.Namespaces: the latter has already had any currently-excluded
		// namespace stripped out of it by dropConsentOverrides by the time it is stored, so it can never
		// contain the very names this exists to check (see agents.ViewExt.knownNamespaces's own doc comment).
		for _, ns := range out.ExcludedNamespaces {
			if !v.Ext.KnownNamespaces[ns] {
				out.UnknownNamespaces = append(out.UnknownNamespaces, ns)
			}
		}
	}
	d := v.Ext.Diag
	if d != nil {
		// Collector names are matched exactly - the agent reports the actual list, sorted the same way
		// agents.CleanConsent already sorts what was asked for. Excluded namespaces can only be matched by count:
		// the agent's diagnostics carry how many extra namespaces it is leaving out, never their names (see
		// Diagnostics.ExcludedNamespaces's own doc comment), so an agent that coincidentally excludes a
		// different set of the same size would misreport as confirmed here. Good enough for the case this
		// exists for - a narrowing a stuck or too-old agent never picked up at all - and tightening it
		// further needs a proto change to carry the names, not just a count.
		pausedConfirmed := agents.SameStrings(d.PausedCollectors, v.Ext.Consent.Paused)
		excludedConfirmed := int(d.ExcludedNamespaces) == len(v.Ext.Consent.Excluded)
		out.Confirmed = pausedConfirmed && excludedConfirmed
	}
	// out.Confirmed is false here either because a report arrived that does not yet match, or because none
	// has arrived at all (d == nil) - both are "not yet confirmed" and both deserve the same elapsed-time
	// signal, so SetAt is filled in the same way regardless of which.
	if !out.Confirmed && !v.Ext.ConsentAt.IsZero() {
		out.SetAt = kernel.RFC(v.Ext.ConsentAt)
	}
	return out
}

// diagDoc turns the stored message into its document. Every string is cut to a sensible length: it came from the agent.
// Callers already gate this to editors/administrators before calling it (see DiagnosticsDoc's own doc comment) - there
// is no further, per-tier redaction here, so it takes no tier of its own.
func (v *view) diagDoc() *agents.DiagnosticsDoc {
	d := v.Ext.Diag
	if d == nil {
		return nil
	}
	out := &agents.DiagnosticsDoc{
		ReportedAt: kernel.RFC(v.Ext.DiagAt), Partial: v.Ext.DiagPartial, AgentVersion: kernel.Printable(d.AgentVersion, 40), Arch: kernel.Printable(d.Arch, 16), OS: kernel.Printable(d.Os, 16),
		UptimeSeconds: d.UptimeSeconds, InstalledTier: int(d.InstalledTier), ApprovedTier: int(d.ApprovedTier), EffectiveTier: int(d.EffectiveTier),
		OwnExcluded: int(d.OwnExcludedNamespaces), ExcludedNamespaces: int(d.ExcludedNamespaces), FlowDropped: d.FlowDropped,
		PausedCollectors: []string{}, Collectors: []agents.AgentCollectorDoc{}, Informers: []agents.InformerDoc{}, Problems: []agents.ProblemDoc{},
	}
	if sc := d.Scope; sc != nil {
		out.Scope = &agents.ScopeDoc{Description: kernel.Printable(sc.Description, 300), Total: int(sc.NamespacesTotal), InScope: int(sc.NamespacesInScope)}
	}
	for _, p := range d.PausedCollectors {
		out.PausedCollectors = append(out.PausedCollectors, kernel.Printable(p, 20))
	}
	for _, s := range d.InstalledTelemetrySignals {
		out.InstalledTelemetry = append(out.InstalledTelemetry, kernel.Printable(s, 40))
	}
	if tc := d.InstalledTelemetryConfig; tc != nil {
		cfg := &agents.TelemetryConfigDoc{
			ExportEndpoint:           kernel.Printable(tc.ExportEndpoint, 300),
			RedactionEnabled:         tc.RedactionEnabled,
			ResourceDetectionEnabled: tc.ResourceDetectionEnabled,
			EnergySource:             kernel.Printable(tc.GetEnergySource(), 40),
			AcceleratorsSource:       kernel.Printable(tc.GetAcceleratorsSource(), 40),
		}
		if tc.TracesSamplingPercentage != nil {
			v := *tc.TracesSamplingPercentage
			cfg.TracesSamplingPercent = &v
		}
		out.InstalledTelemetryConfig = cfg
	}
	if h := d.ExportHealth; h != nil {
		doc := &agents.ExportHealthDoc{PodsReached: int(h.PodsReached), PodsFailed: int(h.PodsFailed), LastError: kernel.Printable(h.LastError, 300), Routes: []agents.ExportRouteHealthDoc{}}
		if h.ScrapedAt != nil {
			doc.ScrapedAt = kernel.RFC(h.ScrapedAt.AsTime())
		}
		for _, r := range h.Routes {
			state := agents.ExportHealthStates[r.State]
			if state == "" {
				state = "waiting"
			}
			rd := agents.ExportRouteHealthDoc{Exporter: kernel.Printable(r.Exporter, 80), Signal: kernel.Printable(r.Signal, 20), State: state, Sent: r.Sent, Failed: r.Failed}
			if r.LastSentAt != nil {
				rd.LastSentAt = kernel.RFC(r.LastSentAt.AsTime())
			}
			if r.LastFailedAt != nil {
				rd.LastFailedAt = kernel.RFC(r.LastFailedAt.AsTime())
			}
			doc.Routes = append(doc.Routes, rd)
		}
		out.ExportHealth = doc
	}
	for _, c := range d.Collectors {
		cd := agents.AgentCollectorDoc{Name: kernel.Printable(c.Name, 20), Configured: c.Configured, Enabled: c.Enabled, PausedByServer: c.PausedByServer, Producing: c.Producing,
			Reporting: int(c.Reporting), Expected: int(c.Expected), Note: kernel.Printable(c.Note, 200)}
		if c.LastData != nil {
			cd.LastData = kernel.RFC(c.LastData.AsTime())
		}
		out.Collectors = append(out.Collectors, cd)
	}
	for _, i := range d.Informers {
		out.Informers = append(out.Informers, agents.InformerDoc{Name: kernel.Printable(i.Name, 80), Module: kernel.Printable(i.Module, 20), Synced: i.Synced, Objects: i.Objects, LastError: kernel.Printable(i.LastError, 300)})
	}
	for _, p := range d.Problems {
		pd := agents.ProblemDoc{Code: kernel.Printable(p.Code, 40), Severity: map[continuumv1.Problem_Severity]string{continuumv1.Problem_INFO: "info", continuumv1.Problem_WARN: "warn", continuumv1.Problem_ERROR: "error"}[p.Severity], Message: kernel.Printable(p.Message, 700)}
		if p.Since != nil {
			pd.Since = kernel.RFC(p.Since.AsTime())
		}
		if pd.Severity == "" {
			pd.Severity = "info"
		}
		out.Problems = append(out.Problems, pd)
	}
	return out
}

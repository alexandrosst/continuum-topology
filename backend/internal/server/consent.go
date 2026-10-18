package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/kernel"
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
			return out, kernel.Errf(kernel.KindInvalid, "%q is not a collector an agent can have (probes, flow or measure)", kernel.Printable(p, 40))
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
		case !kernel.NsNameRe.MatchString(ns):
			return out, kernel.Errf(kernel.KindInvalid, "%q is not a valid namespace name (lowercase letters, digits and dashes, at most 63 characters)", kernel.Printable(ns, 64))
		case systemNS(ns):
			return out, kernel.Errf(kernel.KindInvalid, "%s is a system namespace: the agent always reads those to recognise the cluster's own components", ns)
		}
		if !seen[ns] {
			seen[ns] = true
			out.Excluded = append(out.Excluded, ns)
		}
	}
	if len(out.Excluded) > kernel.MaxExcluded {
		return out, kernel.Errf(kernel.KindInvalid, "at most %d namespaces can be left out from here; use the scope of the agent's install (helm) for more", kernel.MaxExcluded)
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
	// linkCaps is what this agent's heartbeat last said its install can do for the cluster-link telemetry (see linkCaps).
	linkCaps    linkCaps
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
	// protocol and caps are what the agent's last Hello said about the protocol it speaks and what it does (see
	// internal/agentcaps). They are replaced by every Hello, so a stream that says nothing records protocol 0 and no names and
	// not what an earlier stream said. In memory only: nothing about them is stored, so a restored database carries nothing
	// from a Hello. Nothing the server sends depends on them.
	protocol uint32
	caps     []string
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

// ActingAs returns ctx carrying a signed-in caller with the given role, the way the HTTP layer puts one there. It is for code
// that calls the hub or the core directly and has to say who is asking (a test that drives a real server, a tool): nothing is
// checked about the role, so it is never called with a role taken from a request.
func ActingAs(ctx context.Context, role string) context.Context {
	return accounts.WithCaller(ctx, accounts.Principal{Role: role})
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
func (v *agtView) noteDiagnostics(d *continuumv1.Diagnostics, partial bool, at time.Time) {
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
// Best-effort and silent on failure by design: the action itself is already durably audited (see Audited)
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

// ScopeDoc says which namespaces an agent leaves out. Only counts: the server never learns the names it was not sent.
type ScopeDoc struct {
	Description string `json:"description"`
	Total       int    `json:"namespaces"`
	InScope     int    `json:"inScope"`
}

// eventLinker is what a store adds when it keeps a graph: the ability to connect a batch of events to
// the versions they explain (see graph.DB.LinkEventChanges). A plain store does not implement it - the
// events themselves are already durably stored either way, this is enrichment on top, the same optional-
// capability pattern entityRecorder uses in consent.go.
type eventLinker interface {
	LinkEventChanges(ctx context.Context, org string, at time.Time, evs []store.Event) error
}

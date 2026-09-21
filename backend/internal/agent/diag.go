package agent

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The agent's account of itself. An agent that is half-blind must say so rather than look healthy, so everything it can
// tell about how well it is doing goes into one Diagnostics message: what it is, the ceiling its install sets against what
// is in force, which optional collectors run and whether they deliver, what each watch on the cluster can read, and a list of
// typed problems with a stable code, a plain-language message that says what to do, and since when.
//
// Problems come from two places. Derived ones are worked out from the state of the moment each time a message is built
// (a refused watch, a collector that went quiet, a scope that matches nothing): they appear when the condition holds and
// vanish when it stops, and keep their `since` in between. Sticky ones are raised where something happens (the server
// refused a picture, a Secret could not be written, a task panicked) and cleared where it is put right.

// Problem codes. They are part of the interface: the Agents page and the documentation key on them.
const (
	CodeRBACForbidden       = "rbac_forbidden"
	CodeInformerNotSynced   = "informer_not_synced"
	CodeSyncTooLarge        = "sync_too_large"
	CodeServerLimitsRefused = "server_limits_refused"
	CodeClockSkew           = "clock_skew"
	CodeCollectorSilent     = "collector_silent"
	CodeScopeEmpty          = "scope_empty"
	CodeIdentityUnwritable  = "identity_secret_unwritable"
	CodeOverrideIgnored     = "override_ignored"
	CodeFlowDropped         = "flow_dropped"
	CodeInternalError       = "internal_error"
	CodeRBACWiderThanTier   = "rbac_wider_than_tier"
	CodeRBACNamespacedMode  = "rbac_namespaced_mode"
)

const (
	collectorProbes  = "probes"
	collectorFlow    = "flow"
	collectorMeasure = "measure"

	defaultProbeEvery = 3 * time.Minute // the chart's nodeProbe.interval
	defaultFlowEvery  = 30 * time.Second
	// silentAfterIntervals is how many of a collector's own intervals may pass without a word before it is called silent.
	silentAfterIntervals    = 3
	diagRefreshEvery        = 5 * time.Minute
	diagCheckEvery          = 5 * time.Second // how often a changed account is looked for and sent between heartbeats
	flowDropRemembered      = time.Hour
	internalErrorRemembered = time.Hour
	maxProblems             = 32
	informerNotSyncedGrace  = 20 * time.Second

	helmRestoreRBACAdvice = "Check the agent's ClusterRole (kubectl get clusterrole -l app.kubernetes.io/instance=continuum-agent -o yaml), then restore the chart's permissions with `helm upgrade --reuse-values` on the same release and chart you installed with."
	scopeEmptyAdvice      = "Change scope.namespaces, scope.exclude or scope.selector with `helm upgrade --reuse-values`, or clear the server-side exclusions on this agent."
	rbacWiderAdvice       = "If a `helm upgrade --set access.tier=N` to narrow this install was run recently, it may not have finished; otherwise it was never run against this cluster, or ran only partway. Run (or re-run) it with N set to this install's declared tier to remove the wider ClusterRole/ClusterRoleBinding."
)

// CollectorNames are the optional collectors an owner can switch on in the chart and the server can pause.
var CollectorNames = []string{collectorProbes, collectorFlow, collectorMeasure}

type prob struct {
	code    string
	sev     continuumv1.Problem_Severity
	msg     string
	since   time.Time
	expires time.Time // zero: until cleared
}

// problemSet holds both kinds, keyed so that one code can appear more than once (one per refused resource, say).
type problemSet struct {
	mu      sync.Mutex
	sticky  map[string]*prob
	derived map[string]*prob
}

func newProblemSet() *problemSet {
	return &problemSet{sticky: map[string]*prob{}, derived: map[string]*prob{}}
}

// raise records a problem that stays until clear is called (or its expiry passes). Raising it again keeps its `since`.
func (p *problemSet) raise(key, code string, sev continuumv1.Problem_Severity, msg string, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := &prob{code: code, sev: sev, msg: msg, since: time.Now()}
	if old := p.sticky[key]; old != nil {
		pr.since = old.since
	}
	if ttl > 0 {
		pr.expires = time.Now().Add(ttl)
	}
	p.sticky[key] = pr
}

func (p *problemSet) clear(keys ...string) {
	p.mu.Lock()
	for _, k := range keys {
		delete(p.sticky, k)
	}
	p.mu.Unlock()
}

// replaceDerived swaps in the conditions that hold now, carrying `since` over for the ones that held before.
func (p *problemSet) replaceDerived(now []*prob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := map[string]*prob{}
	for _, pr := range now {
		k := pr.code + "|" + pr.msg
		if old := p.derived[k]; old != nil {
			pr.since = old.since
		}
		next[k] = pr
	}
	p.derived = next
}

// list is everything that is a problem now, worst first.
func (p *problemSet) list() []*continuumv1.Problem {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	var all []*prob
	for k, pr := range p.sticky {
		if !pr.expires.IsZero() && now.After(pr.expires) {
			delete(p.sticky, k)
			continue
		}
		all = append(all, pr)
	}
	for _, pr := range p.derived {
		all = append(all, pr)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sev != all[j].sev {
			return all[i].sev > all[j].sev
		}
		if all[i].code != all[j].code {
			return all[i].code < all[j].code
		}
		return all[i].msg < all[j].msg
	})
	if len(all) > maxProblems {
		all = all[:maxProblems]
	}
	out := make([]*continuumv1.Problem, 0, len(all))
	for _, pr := range all {
		out = append(out, &continuumv1.Problem{Code: pr.code, Severity: pr.sev, Message: pr.msg, Since: timestamppb.New(pr.since)})
	}
	return out
}

// diagState is what the diagnostics need to know about the stream, written by the stream loop and read when a message is built.
type diagState struct {
	mu       sync.Mutex
	approved int                  // what the server last approved, as pushed in Config (-1: not heard yet)
	paused   map[string]bool      // collectors the server has paused, in force
	excluded []string             // extra namespaces the server excludes, in force
	enabled  map[string]time.Time // when each optional collector last became enabled (for "silent since")
	// measurement rounds
	targets      int
	targetsSince time.Time
	measureEvery time.Duration
	lastRound    time.Time
	lastResults  int
	// flow drops
	flowDropSeen uint64
	flowDropAt   time.Time
	// rbacCeiling is the highest tier checkRBACCeiling last found the cluster still grants (rbac_check.go), and
	// rbacCheckedAt when: zero until the first check completes. Read by diagnostics(), which must not call the
	// cluster itself; written only by the RBAC check loop.
	rbacCeiling   int
	rbacCheckedAt time.Time
	// what was last sent, so an unchanged message is not sent again before diagRefreshEvery has passed
	sentSig []byte
	sentAt  time.Time
}

func newDiagState() *diagState {
	return &diagState{approved: -1, paused: map[string]bool{}, enabled: map[string]time.Time{}}
}

// diagnostics builds the message. It is cheap (no calls to the cluster) and safe to call from any goroutine.
func (r *runner) diagnostics() *continuumv1.Diagnostics {
	now := time.Now()
	r.mu.Lock()
	c := r.collector
	tier := r.collTier
	r.mu.Unlock()
	d := &continuumv1.Diagnostics{
		AgentVersion:  r.cfg.Version,
		Arch:          runtime.GOARCH,
		Os:            runtime.GOOS,
		UptimeSeconds: int64(now.Sub(r.started).Seconds()),
		InstalledTier: uint32(r.cfg.Tier),
		GeneratedAt:   timestamppb.New(now),
	}
	r.dg.mu.Lock()
	approved := r.dg.approved
	paused := map[string]bool{}
	for k, v := range r.dg.paused {
		paused[k] = v
	}
	excluded := append([]string(nil), r.dg.excluded...)
	r.dg.mu.Unlock()
	if approved >= 0 {
		d.ApprovedTier = uint32(approved)
	}
	d.EffectiveTier = uint32(tier)
	if c == nil {
		d.EffectiveTier = 0
	}
	for name := range paused {
		if paused[name] {
			d.PausedCollectors = append(d.PausedCollectors, name)
		}
	}
	sort.Strings(d.PausedCollectors)
	d.ExcludedNamespaces = uint32(len(excluded))
	if r.cfg.Scope != nil {
		d.OwnExcludedNamespaces = uint32(len(r.cfg.Scope.Exclude))
	}
	if c != nil {
		d.Scope = c.ScopeSummary()
	} else {
		d.Scope = &continuumv1.ScopeFacts{Description: r.cfg.Scope.Public()}
	}
	if r.cfg.Flows != nil {
		d.FlowDropped = r.cfg.Flows.Aggregator.Dropped()
	}

	var derived []*prob
	add := func(code string, sev continuumv1.Problem_Severity, msg string) {
		derived = append(derived, &prob{code: code, sev: sev, msg: msg, since: now})
	}

	// what each watch can read
	if c != nil {
		started := r.collectorStartedAt()
		for _, i := range c.Informers() {
			d.Informers = append(d.Informers, &continuumv1.InformerDiag{Name: i.Name, Synced: i.Synced, LastError: clipMsg(i.Err, 300), Objects: i.Objects, Module: i.Module})
			switch {
			case i.Forbidden:
				add(CodeRBACForbidden, continuumv1.Problem_ERROR, fmt.Sprintf("The cluster refuses this agent permission to %s %s (HTTP 403), so the agent cannot see it and reports none. %s", i.Verb, i.Name, helmRestoreRBACAdvice))
			case !i.Synced && now.Sub(started) > informerNotSyncedGrace:
				why := "it has not answered the first request yet (the Kubernetes API may be slow or unreachable)"
				if i.Err != "" {
					why = "the last error was: " + clipMsg(i.Err, 200)
				}
				add(CodeInformerNotSynced, continuumv1.Problem_ERROR, fmt.Sprintf("The agent has not finished reading %s: %s. Until it has, the agent holds back its picture of the cluster rather than send an incomplete one. Check that the pod can reach the Kubernetes API (network policy, proxy NO_PROXY).", i.Name, why))
			case i.Synced && i.Err != "":
				add(CodeInformerNotSynced, continuumv1.Problem_WARN, fmt.Sprintf("The watch on %s is failing (%s), so what the agent knows about it may be out of date. It keeps retrying.", i.Name, clipMsg(i.Err, 200)))
			}
		}
		for mod, why := range c.OptionalOff() {
			switch {
			case strings.HasPrefix(why, "not permitted"):
				add(CodeRBACForbidden, continuumv1.Problem_WARN, fmt.Sprintf("The %s module is off: the cluster refuses this agent permission to read what it needs. %s", mod, helmRestoreRBACAdvice))
			case strings.HasPrefix(why, "unavailable"):
				add(CodeInformerNotSynced, continuumv1.Problem_WARN, fmt.Sprintf("The %s module is off: %s.", mod, why))
			}
		}
		// A scope that leaves nothing in view
		if tier >= 2 && d.Scope != nil && d.Scope.NamespacesTotal > 0 && d.Scope.NamespacesInScope == 0 {
			add(CodeScopeEmpty, continuumv1.Problem_WARN, fmt.Sprintf("The scope (%s) matches none of this cluster's %d namespaces, so the agent reports no workloads. %s", d.Scope.Description, d.Scope.NamespacesTotal, scopeEmptyAdvice))
		}
		// Namespaced RBAC (rbac.mode=namespaced) trades a real capability for a narrower blast radius; said plainly
		// here, every time, rather than left for someone to discover only in the chart's own values.yaml comments.
		if tier >= 2 && r.cfg.RBACNamespaced {
			add(CodeRBACNamespacedMode, continuumv1.Problem_INFO, "This agent's tier 2 access is granted per namespace (rbac.mode=namespaced), not with one cluster-wide ClusterRole: namespace labels/creation time and persistent volumes are not read (only claims are), and a namespace added to the cluster later needs a helm upgrade with it added to scope.namespaces before this agent reports on it.")
		}
	}
	d.Collectors = r.collectorDiagnostics(c, tier, paused, now, add)

	// the clock
	r.ex.mu.Lock()
	skew, known := r.ex.skew, r.ex.skewKnown
	r.ex.mu.Unlock()
	if known && (skew > skewWarnAfter || skew < -skewWarnAfter) {
		dir := "ahead of"
		if skew < 0 {
			dir = "behind"
		}
		add(CodeClockSkew, continuumv1.Problem_WARN, fmt.Sprintf("This machine's clock is %s %s the server's. Certificates may then look expired or not yet valid. Check time synchronisation (NTP) on the node this pod runs on.", skew.Abs().Round(time.Second), dir))
	}

	// RBAC still wider than this install declares (rbac_check.go runs the actual check; this only reads its cache)
	if r.cfg.RBACSelfCheck {
		r.dg.mu.Lock()
		ceiling, checked := r.dg.rbacCeiling, !r.dg.rbacCheckedAt.IsZero()
		r.dg.mu.Unlock()
		if checked && ceiling > r.cfg.Tier {
			add(CodeRBACWiderThanTier, continuumv1.Problem_WARN, fmt.Sprintf("The cluster still grants this agent permission to read at tier %d, wider than the tier %d this install declares (access.tier). %s", ceiling, r.cfg.Tier, rbacWiderAdvice))
		}
	}

	// traffic dropped while disconnected
	if n := d.FlowDropped; n > 0 {
		r.dg.mu.Lock()
		if n > r.dg.flowDropSeen {
			r.dg.flowDropSeen, r.dg.flowDropAt = n, now
		}
		at := r.dg.flowDropAt
		r.dg.mu.Unlock()
		if now.Sub(at) < flowDropRemembered {
			add(CodeFlowDropped, continuumv1.Problem_WARN, fmt.Sprintf("%d observed connections were dropped because the agent held more than it may while it could not reach the server (the oldest go first). Traffic figures for that time are incomplete.", n))
		}
	}
	r.probs.replaceDerived(derived)
	d.Problems = r.probs.list()
	return d
}

// collectorDiagnostics reports the three optional collectors: whether this install configured them, whether they run now,
// and whether they deliver. A collector that is switched on but has said nothing for silentAfterIntervals of its own
// interval is a problem (collector_silent): a node probe or flow collector that cannot reach the agent looks exactly
// like a quiet one otherwise.
func (r *runner) collectorDiagnostics(c *collect.Collector, tier int, paused map[string]bool, now time.Time, add func(string, continuumv1.Problem_Severity, string)) []*continuumv1.CollectorDiag {
	nodes := 0
	if c != nil {
		nodes = c.Nodes()
	}
	probeEvery, flowEvery := r.cfg.ProbeInterval, r.cfg.FlowInterval
	if probeEvery <= 0 {
		probeEvery = defaultProbeEvery
	}
	if flowEvery <= 0 {
		flowEvery = defaultFlowEvery
	}
	var out []*continuumv1.CollectorDiag
	silent := func(name string, every time.Duration, last time.Time, where string) bool {
		since := r.enabledSince(name, now)
		ref := last
		if since.After(ref) {
			ref = since
		}
		if now.Sub(ref) > silentAfterIntervals*every {
			add(CodeCollectorSilent, continuumv1.Problem_WARN, fmt.Sprintf("The %s is switched on but nothing has arrived from it for %s (it should report about every %s). %s", where, now.Sub(ref).Round(time.Second), every, silentAdvice(name)))
			return true
		}
		return false
	}

	// node probe receiver
	pd := &continuumv1.CollectorDiag{Name: collectorProbes, Configured: r.cfg.Probes != nil, PausedByServer: paused[collectorProbes]}
	pd.Enabled = pd.Configured && !pd.PausedByServer && tier >= 1
	switch {
	case !pd.Configured:
		pd.Note = "not installed (nodeProbe.enabled is false)"
	case pd.PausedByServer:
		pd.Note = "paused by the server; reports from the nodes are discarded"
	case tier < 1:
		pd.Note = "the effective tier is below 1, so node facts are not collected"
	default:
		n, last := r.cfg.Probes.Presence(silentAfterIntervals * probeEvery)
		pd.Reporting, pd.Expected, pd.Producing = uint32(n), uint32(nodes), n > 0
		if !last.IsZero() {
			pd.LastData = timestamppb.New(last)
		}
		if silent(collectorProbes, probeEvery, last, "node probe receiver") {
			pd.Producing = false
			pd.Note = "nothing has arrived from the node probes"
		} else if nodes > 0 && n < nodes {
			pd.Note = fmt.Sprintf("%d of %d nodes have reported lately", n, nodes)
		}
	}
	out = append(out, pd)

	// flow pipeline
	fd := &continuumv1.CollectorDiag{Name: collectorFlow, Configured: r.cfg.Flows != nil, PausedByServer: paused[collectorFlow]}
	fd.Enabled = fd.Configured && !fd.PausedByServer && tier >= 2
	switch {
	case !fd.Configured:
		fd.Note = "not installed (flowObserver.enabled is false)"
	case fd.PausedByServer:
		fd.Note = "paused by the server; reports from the nodes are discarded"
	case tier < 2:
		fd.Note = "observed traffic is workload-level information: it needs the effective tier to be 2"
	default:
		n, last := r.cfg.Flows.Aggregator.Presence()
		fd.Reporting, fd.Expected, fd.Producing = uint32(n), uint32(nodes), n > 0
		if !last.IsZero() {
			fd.LastData = timestamppb.New(last)
		}
		if silent(collectorFlow, flowEvery, last, "traffic observer") {
			fd.Producing = false
			fd.Note = "nothing has arrived from the node collectors"
		} else if nodes > 0 && n < nodes {
			fd.Note = fmt.Sprintf("%d of %d nodes have reported lately", n, nodes)
		}
	}
	out = append(out, fd)

	// connection timing
	r.dg.mu.Lock()
	targets, every, lastRound, results, tsince := r.dg.targets, r.dg.measureEvery, r.dg.lastRound, r.dg.lastResults, r.dg.targetsSince
	r.dg.mu.Unlock()
	md := &continuumv1.CollectorDiag{Name: collectorMeasure, Configured: r.cfg.Measure, PausedByServer: paused[collectorMeasure]}
	md.Enabled = md.Configured && !md.PausedByServer
	md.Expected = uint32(targets)
	switch {
	case !md.Configured:
		md.Note = "not installed (measurements.enabled is false)"
	case md.PausedByServer:
		md.Note = "paused by the server; no connection is timed"
	case targets == 0:
		md.Note = "the server has named nothing to time"
	default:
		md.Reporting, md.Producing = uint32(results), !lastRound.IsZero()
		if !lastRound.IsZero() {
			md.LastData = timestamppb.New(lastRound)
		}
		if every > 0 {
			ref := lastRound
			if tsince.After(ref) {
				ref = tsince
			}
			if now.Sub(ref) > silentAfterIntervals*every+10*time.Second {
				add(CodeCollectorSilent, continuumv1.Problem_WARN, fmt.Sprintf("Connection timing is switched on with %d addresses to time, but no round has completed for %s (it should run about every %s). %s", targets, now.Sub(ref).Round(time.Second), every, silentAdvice(collectorMeasure)))
				md.Producing = false
			}
		}
	}
	out = append(out, md)
	return out
}

func silentAdvice(name string) string {
	switch name {
	case collectorProbes:
		return "Check that the node probe DaemonSet is running (kubectl -n continuum-system get ds) and can reach the agent's Service on port 8081 (a NetworkPolicy may be blocking it)."
	case collectorFlow:
		return "Check that the flow collector DaemonSet is running (kubectl -n continuum-system get ds) and can reach the agent's Service on port 8081; on a node where eBPF is unavailable it falls back to conntrack, or reports nothing if neither works."
	}
	return "Check that the agent can open connections to those addresses (egress policy, firewall)."
}

// enabledSince is when a collector last became enabled, so that one that was just switched on is not called silent at once.
func (r *runner) enabledSince(name string, now time.Time) time.Time {
	r.dg.mu.Lock()
	defer r.dg.mu.Unlock()
	if t, ok := r.dg.enabled[name]; ok {
		return t
	}
	r.dg.enabled[name] = r.started
	return r.started
}

// collectorStartedAt is when the current collector began watching.
func (r *runner) collectorStartedAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.collStarted.IsZero() {
		return r.started
	}
	return r.collStarted
}

func clipMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// diagSignature is what decides whether a message differs from the last one: everything except the numbers that change
// all the time (uptime, timestamps, object counts, how long ago a collector last reported).
func diagSignature(d *continuumv1.Diagnostics) []byte {
	c := proto.Clone(d).(*continuumv1.Diagnostics)
	c.UptimeSeconds, c.GeneratedAt = 0, nil
	for _, i := range c.Informers {
		i.Objects = 0
	}
	for _, x := range c.Collectors {
		x.LastData = nil
	}
	for _, p := range c.Problems {
		p.Since = nil
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	return b
}

// diagToSend returns the diagnostics for a heartbeat: the message if it changed since the last one sent (or if refreshEvery
// has passed), nil otherwise. force sends it regardless.
func (r *runner) diagToSend(force bool) *continuumv1.Diagnostics {
	d := r.diagnostics()
	sig := diagSignature(d)
	r.dg.mu.Lock()
	defer r.dg.mu.Unlock()
	if !force && r.dg.sentSig != nil && string(sig) == string(r.dg.sentSig) && time.Since(r.dg.sentAt) < diagRefreshEvery {
		return nil
	}
	r.dg.sentSig, r.dg.sentAt = sig, time.Now()
	return d
}

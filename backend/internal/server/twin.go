package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"
	"continuum/internal/kernel"
	"continuum/internal/model"
	"continuum/internal/store"
	"continuum/internal/twin"
	"continuum/internal/workspace"
)

// What the hub keeps for the twin: the identity registry (which record id each machine was given), the tombstones
// (what disappeared, and when), the model's version, and a short-lived cache of the effective model. All of it is
// derived from observation, kept apart from the workspace, and rebuilt from the agents' next full pictures when lost.

const (
	// modelCacheTTL bounds how long a computed model is reused. States move with the clock, so it must stay short;
	// two seconds keeps a busy UI from rebuilding it on every poll.
	modelCacheTTL = 2 * time.Second
	// declaredTTL bounds how long the parsed workspace is reused if nothing announced a change.
	declaredTTL = 30 * time.Second
	// maxTombstoneDocs is how many tombstones one state document carries.
	maxTombstoneDocs = 500
)

type modelCache struct {
	at    time.Time
	gen   uint64
	stale time.Duration
	m     twin.Model
	etag  string
}

type twinRT struct {
	mu      sync.Mutex
	loaded  bool
	reg     *twin.Registry
	tombs   *twin.Tombstones
	version int64
	fp      string
	// unsaved is set while the current version has been handed out but the store has not accepted it yet. Until it
	// has, a restart would read an older number and hand it out again for different content, so every read tries
	// again.
	unsaved    bool
	cache      *modelCache
	stateCache *stateCache
	declared   workspace.Declared
	declaredA  time.Time
	declaredK  bool

	// gen changes whenever an input of the model changes without the clock (a sync, a revocation, a saved workspace).
	gen atomic.Uint64
	// build serialises model builds so versions are assigned in the order the models were made.
	build sync.Mutex

	// cacheHits/cacheMisses count how often Model below served the short-lived cache versus had to
	// rebuild - see ServerSelfStatsSample.ModelCacheHits' own doc comment for what a caller does with
	// these. Plain running totals since the server started, like every other counter in this package.
	cacheHits, cacheMisses atomic.Uint64
}

func newTwinRT() *twinRT { return &twinRT{reg: twin.NewRegistry(), tombs: twin.NewTombstones(0)} }

// retention is how long tombstones are kept for this hub: the test-only override if one is set, otherwise the
// live organisation setting, falling back to twin.DefaultRetention if that is zero (a settings row saved before
// this setting existed).
func (h *Hub) retention() time.Duration {
	if h.TombstoneRetention > 0 {
		return h.TombstoneRetention
	}
	if d := h.C.Settings().TombstoneRetentionDays; d > 0 {
		return time.Duration(d) * 24 * time.Hour
	}
	return twin.DefaultRetention
}

// twinLoad reads what was persisted, once. It is safe to call under h.mu: the store never calls back into the hub.
func (h *Hub) twinLoad(ctx context.Context) {
	tw := h.tw
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.loaded {
		return
	}
	tw.loaded = true
	tw.tombs = twin.NewTombstones(h.retention())
	org := h.C.OrgID
	if ids, err := h.C.Store.ListIdentities(ctx, org); err == nil {
		tw.reg.Load(ids)
	} else {
		h.Log.Warn("twin: identities could not be read; node ids fall back to names", "err", err)
	}
	if ts, err := h.C.Store.ListTombstones(ctx, org); err == nil {
		tw.tombs.Load(ts)
	} else {
		h.Log.Warn("twin: tombstones could not be read", "err", err)
	}
	if ms, err := h.C.Store.GetModelState(ctx, org); err == nil {
		tw.version, tw.fp = ms.Version, ms.Fingerprint
	}
}

// workspaceChanged tells the twin the declared layer was saved.
func (h *Hub) workspaceChanged(int64) {
	h.tw.mu.Lock()
	h.tw.declaredK = false
	h.tw.mu.Unlock()
	h.tw.gen.Add(1)
}

// declaredNow returns the declared layer, parsed from the saved workspace.
func (h *Hub) declaredNow(ctx context.Context, now time.Time) workspace.Declared {
	tw := h.tw
	tw.mu.Lock()
	if tw.declaredK && now.Sub(tw.declaredA) < declaredTTL {
		d := tw.declared
		tw.mu.Unlock()
		return d
	}
	tw.mu.Unlock()
	var d workspace.Declared
	if w, err := h.C.Store.GetWorkspace(ctx, h.C.OrgID); err == nil && len(w.Data) > 0 {
		if p, err := workspace.Parse(w.Data); err == nil {
			d = p
		} else {
			h.Log.Warn("twin: the workspace could not be read as a declared layer", "err", err)
		}
	}
	tw.mu.Lock()
	tw.declared, tw.declaredA, tw.declaredK = d, now, true
	tw.mu.Unlock()
	return d
}

// nodeRecords decides the record id of every node of one cluster's facts. The caller holds h.mu.
func (h *Hub) nodeRecords(clusterID string, st *facts.State, now time.Time) map[string]twin.NodeRecord {
	nodes := make([]*continuumv1.NodeFacts, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		nodes = append(nodes, n)
	}
	return h.tw.reg.Resolve(clusterID, nodes, now)
}

func nodeIDMap(rs map[string]twin.NodeRecord) map[string]string {
	out := make(map[string]string, len(rs))
	for k, r := range rs {
		out[k] = r.ID
	}
	return out
}

// noteVanished records a tombstone for every record an incoming sync is about to remove from what the server
// holds. It looks at the state as it is before the sync is applied, because that is the last known record. The
// caller holds h.mu.
func (h *Hub) noteVanished(a store.Agent, v *view, s *continuumv1.Sync, now time.Time) {
	if v == nil || v.state == nil {
		return
	}
	gone := twin.Disappeared(v.state, s, a.AccessTier)
	if len(gone) == 0 {
		return
	}
	h.twinLoad(bg())
	recs := h.nodeRecords(a.ClusterID, v.state, now)
	t := interpret.Interpret(interpret.Input{OrgID: h.C.OrgID, AgentID: a.ID, ClusterID: a.ClusterID, Name: a.Name, State: v.state, Now: v.lastSync, AccessTier: a.AccessTier, NodeIDs: nodeIDMap(recs)})
	byKey := map[string]int{}
	for i := range t.Nodes {
		byKey["node|"+t.Nodes[i].Key] = i
	}
	for i := range t.Namespaces {
		byKey["namespace|"+t.Namespaces[i].Key] = i
	}
	for i := range t.Services {
		byKey["service|"+t.Services[i].Key] = i
	}
	var add []store.Tombstone
	for _, g := range gone {
		var key string
		switch g.Kind {
		case twin.KindNode:
			key = a.ClusterID + "/node/" + g.Key
		case twin.KindNamespace:
			key = a.ClusterID + "/ns/" + g.Key
		default:
			key = a.ClusterID + "/" + g.Key
		}
		i, ok := byKey[g.Kind+"|"+key]
		if !ok {
			continue // not part of the topology (system machinery), so there is nothing to remember
		}
		var (
			raw      []byte
			id, name string
		)
		switch g.Kind {
		case twin.KindNode:
			n := t.Nodes[i]
			n.ClearObservation()
			id, name = n.ID, n.Name
			raw, _ = json.Marshal(n)
		case twin.KindNamespace:
			n := t.Namespaces[i]
			n.ClearObservation()
			id, name = n.ID, n.Name
			raw, _ = json.Marshal(n)
		default:
			sv := t.Services[i]
			if twin.Ephemeral(sv.Kind) {
				continue
			}
			sv.ClearObservation()
			id, name = sv.ID, sv.Name
			raw, _ = json.Marshal(sv)
		}
		add = append(add, store.Tombstone{Kind: g.Kind, ID: id, Name: name, ClusterID: a.ClusterID, AgentID: a.ID, GoneAt: now, LastSeen: v.lastBeat, Reason: g.Reason, Record: raw})
	}
	if len(add) > 0 {
		h.tw.tombs.Add(add...)
		h.tw.gen.Add(1)
	}
}

// reviveSeen removes the tombstones of records that are back in a topology. The caller holds h.mu.
func (h *Hub) reviveSeen(t model.Topology) {
	if h.tw.tombs.Len() == 0 {
		return
	}
	for _, n := range t.Nodes {
		h.tw.tombs.Revive(twin.KindNode, n.ID)
	}
	for _, n := range t.Namespaces {
		h.tw.tombs.Revive(twin.KindNamespace, n.ID)
	}
	for _, s := range t.Services {
		h.tw.tombs.Revive(twin.KindService, s.ID)
	}
}

// TombstoneDoc is a record that disappeared, kept for a while: what it was, and when and why it went.
type TombstoneDoc struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ClusterID string          `json:"clusterId,omitempty"`
	AgentID   string          `json:"agentId,omitempty"`
	GoneAt    string          `json:"goneAt"`
	LastSeen  string          `json:"lastSeen,omitempty"`
	Reason    string          `json:"reason"`
	Record    json.RawMessage `json:"record,omitempty"`
}

func (h *Hub) tombstoneDocs(now time.Time) []TombstoneDoc {
	out := []TombstoneDoc{}
	for _, t := range h.tw.tombs.List(now) {
		if len(out) >= maxTombstoneDocs {
			break
		}
		d := TombstoneDoc{Kind: t.Kind, ID: t.ID, Name: t.Name, ClusterID: t.ClusterID, AgentID: t.AgentID, GoneAt: kernel.RFC(t.GoneAt), Reason: t.Reason}
		if !t.LastSeen.IsZero() {
			d.LastSeen = kernel.RFC(t.LastSeen)
		}
		if len(t.Record) > 0 && json.Valid(t.Record) {
			d.Record = t.Record
		}
		out = append(out, d)
	}
	return out
}

// Tombstones lists what disappeared within the retention window, newest first.
func (h *Hub) Tombstones(ctx context.Context) []store.Tombstone {
	h.twinLoad(ctx)
	return h.tw.tombs.List(h.C.Now())
}

// Flush persists what changed in the registry and the tombstones and drops tombstones past retention.
func (h *Hub) Flush(ctx context.Context) {
	tw := h.tw
	tw.mu.Lock()
	loaded := tw.loaded
	tw.mu.Unlock()
	if !loaded {
		return
	}
	tw.tombs.Sweep(h.C.Now())
	if put, del := tw.tombs.Dirty(); len(put) > 0 || len(del) > 0 {
		if len(put) > 0 {
			if err := h.C.Store.PutTombstones(ctx, h.C.OrgID, put); err != nil {
				h.Log.Error("twin: tombstones could not be saved", "err", err)
				tw.tombs.Add(put...) // try again next tick
			}
		}
		if len(del) > 0 {
			if err := h.C.Store.DeleteTombstones(ctx, h.C.OrgID, del); err != nil {
				h.Log.Error("twin: tombstones could not be deleted", "err", err)
			}
		}
	}
	if ids := tw.reg.Dirty(); len(ids) > 0 {
		if err := h.C.Store.PutIdentities(ctx, h.C.OrgID, ids); err != nil {
			h.Log.Error("twin: identities could not be saved", "err", err)
			tw.reg.Requeue(ids)
		}
	}
}

// effectiveBuild is what the model builder needs beyond the interpreted topology.
type effectiveBuild struct {
	agents map[string]twin.AgentInfo
	obs    map[string]twin.Observation
	facts  map[string]*facts.State
	nodes  map[string]twin.NodeRecord
	window time.Duration
}

func newEffectiveBuild(window time.Duration) *effectiveBuild {
	return &effectiveBuild{agents: map[string]twin.AgentInfo{}, obs: map[string]twin.Observation{}, facts: map[string]*facts.State{}, nodes: map[string]twin.NodeRecord{}, window: window}
}

// Model is the effective model of the organisation: what agents observe, what people declared, and what
// disappeared, with the state of every record and the provenance of every attribute. It also returns the entity
// tag that identifies its version. Concurrent callers within a couple of seconds share one computation.
func (h *Hub) Model(ctx context.Context) (twin.Model, string, error) {
	h.twinLoad(ctx)
	tw := h.tw
	tw.build.Lock()
	defer tw.build.Unlock()
	now := h.C.Now()
	window := h.staleWindow()
	tw.mu.Lock()
	if c := tw.cache; c != nil && c.gen == tw.gen.Load() && c.stale == window && now.Sub(c.at) >= 0 && now.Sub(c.at) < modelCacheTTL {
		m, etag := c.m, c.etag
		h.saveModelVersion(ctx, now)
		tw.mu.Unlock()
		tw.cacheHits.Add(1)
		return m, etag, nil
	}
	tw.mu.Unlock()
	tw.cacheMisses.Add(1)

	gen := tw.gen.Load()
	decl := h.declaredNow(ctx, now)
	var m twin.Model
	_, err := h.stateFor(ctx, false, func(doc *StateDoc, asm *effectiveBuild) {
		m = twin.Build(twin.Input{
			Now: now, StaleAfter: asm.window, Retention: h.retention(), Topology: doc.Topology, Facts: asm.facts, Agents: asm.agents,
			Observations: asm.obs, Nodes: asm.nodes, Tombstones: h.tw.tombs.List(now), Declared: decl,
		})
	})
	if err != nil {
		return twin.Model{}, "", err
	}
	fp := twin.Fingerprint(m)
	tw.mu.Lock()
	if fp != tw.fp || tw.version == 0 {
		tw.version++
		tw.fp = fp
		tw.unsaved = true
	}
	h.saveModelVersion(ctx, now)
	m.ModelVersion = tw.version
	etag := fmt.Sprintf(`"m%d-%s"`, tw.version, fp[:12])
	tw.cache = &modelCache{at: now, gen: gen, stale: window, m: m, etag: etag}
	tw.mu.Unlock()
	return m, etag, nil
}

// saveModelVersion stores the current version if the store does not have it yet. The caller holds tw.mu. A version
// that could not be stored stays marked, and the next read (hit or miss) tries again, so a refused write delays the
// number reaching the store but never loses it.
func (h *Hub) saveModelVersion(ctx context.Context, at time.Time) {
	tw := h.tw
	if !tw.unsaved {
		return
	}
	st := store.ModelState{Version: tw.version, Fingerprint: tw.fp, At: at}
	if err := h.C.Store.PutModelState(ctx, h.C.OrgID, st); err != nil {
		kernel.Metrics.StoreErrors.Add(1)
		h.Log.Error("twin: the model version could not be saved; it will be tried again on the next read", "version", tw.version, "err", err)
		return
	}
	tw.unsaved = false
}

// staleWindow is how long silence is tolerated before a record is stale.
func (h *Hub) staleWindow() time.Duration {
	return time.Duration(h.C.Settings().StaleAfterBeats*HeartbeatSeconds) * time.Second
}

// revokedWithinRetention says whether a revoked agent's last known picture is still shown (as revoked).
func (h *Hub) revokedWithinRetention(a store.Agent) bool {
	if a.Status != store.StatusRevoked || a.RevokedAt == nil {
		return false
	}
	return h.C.Now().Sub(*a.RevokedAt) <= h.retention()
}

// revokedToShow picks the revoked agents whose last picture is still shown: those within the retention window, and
// only the latest per cluster, and none for a cluster an approved agent now reports (that agent replaced it).
func (h *Hub) revokedToShow(agentList []store.Agent) map[string]bool {
	owned := map[string]bool{}
	for _, a := range agentList {
		if a.Status == store.StatusApproved {
			owned[a.ClusterID] = true
		}
	}
	best := map[string]store.Agent{}
	for _, a := range agentList {
		if !h.revokedWithinRetention(a) || owned[a.ClusterID] {
			continue
		}
		if b, ok := best[a.ClusterID]; !ok || a.RevokedAt.After(*b.RevokedAt) {
			best[a.ClusterID] = a
		}
	}
	out := map[string]bool{}
	for _, a := range best {
		out[a.ID] = true
	}
	return out
}

// markObservation stamps every record of one cluster's topology with how far it can be trusted. A record is
// flagged stale when it is older than the staleness window or its agent was revoked, as it always was; the state
// says more (disconnected, revoked) and the reason says why.
func markObservation(t *model.Topology, o twin.Observation, nodes map[string]twin.NodeRecord) {
	stale := o.State == twin.Stale || o.State == twin.Revoked
	state, reason := string(o.State), o.Reason
	byID := make(map[string]twin.NodeRecord, len(nodes))
	for _, r := range nodes {
		byID[r.ID] = r
	}
	for i := range t.Clusters {
		c := &t.Clusters[i]
		c.Stale, c.State, c.StateReason = stale, state, reason
	}
	for i := range t.Nodes {
		n := &t.Nodes[i]
		n.Stale, n.State, n.StateReason = stale, state, reason
		if r, ok := byID[n.ID]; ok {
			n.IdentityBasis, n.Aliases = string(r.Identity.Basis), r.Aliases
		}
	}
	for i := range t.Namespaces {
		n := &t.Namespaces[i]
		n.Stale, n.State, n.StateReason = stale, state, reason
	}
	for i := range t.Services {
		s := &t.Services[i]
		s.Stale, s.State, s.StateReason = stale, state, reason
	}
	for i := range t.Agents {
		ag := &t.Agents[i]
		ag.Stale, ag.State, ag.StateReason = stale, state, reason
	}
}

package estate

import (
	"fmt"
	"sort"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/connectivity"
	"continuum/internal/interpret"
	"continuum/internal/kernel"
	"continuum/internal/measure"
	"continuum/internal/model"
	"continuum/internal/observed"
	"continuum/internal/store"
)

// ConfigFor builds the Config for one agent: what it may send, how often to check itself, and what
// to measure. It also records which targets were issued. The caller must not hold h.mu.
func (h *Hub) ConfigFor(a store.Agent, v *View) *continuumv1.Config {
	cfg, _ := h.configForChecked(a, v)
	return cfg
}

// configForChecked is ConfigFor, and says whether the Config is complete: false when the cluster has link pairs and the
// organisation's agents could not be read, so the peer list is empty for want of data and not because nobody is paired. A caller
// that replaces a Config the agent already has must not send an incomplete one: the agent would drop every peer until the next
// good minute.
func (h *Hub) configForChecked(a store.Agent, v *View) (*continuumv1.Config, bool) {
	complete := true
	set := h.C.Settings()
	resync := int32(set.ConsistencyMinutes * 60)
	if h.ConsistencyEvery > 0 {
		resync = int32(max(h.ConsistencyEvery/time.Second, 1))
	}
	consent := h.ConsentOf(a.ID) // before taking h.mu: it may read the store
	var orgAgents []store.Agent
	var runnersPaused map[string]bool
	if a.ClusterID != "" && len(connectivity.PeersOf(set.Links, a.ClusterID)) > 0 {
		var err error
		orgAgents, err = h.C.Store.ListAgents(bg(), h.C.OrgID) // also before h.mu, for the same reason
		if err != nil {
			orgAgents, complete = nil, false
			h.C.Log.Warn("could not read the agents to build the link peers", "agent", a.ID, "err", err)
		}
		runnersPaused = h.runnersPaused(a, orgAgents, set.Links)
	}
	creds := h.linkCredsFor(a, set.Links) // before h.mu: it may read and write the store
	facts := connectivity.ConfigFacts{
		Tier: a.AccessTier, ClusterID: a.ClusterID, Consent: consent, Heartbeat: HeartbeatSeconds, Resync: resync,
		ServerTime: h.C.Now().Unix(), MeasureSeconds: int32(set.MeasureSeconds), IperfClients: creds.Clients, IperfServer: creds.Server,
	}
	h.Mu.Lock()
	defer h.Mu.Unlock()
	facts.Peers = h.linkPeers(a, orgAgents, set.Links, consent.Has("flow"), runnersPaused)
	for _, p := range set.ProbeTargets {
		if a.ClusterID != "" && p.ClusterID == a.ClusterID {
			facts.ManualTargets = append(facts.ManualTargets, connectivity.IssuedTarget{ID: p.ID, Host: p.Host, Port: p.Port, Label: p.Label, Source: "manual"})
		}
	}
	facts.AddressTargets = h.addressTestTargets(v, h.C.Now())
	facts.DerivedTargets = connectivity.DeriveTargets(v.Flows)

	cfg, ts := connectivity.CompileConfig(facts)
	v.Targets = map[string]connectivity.IssuedTarget{}
	for _, t := range ts {
		v.Targets[t.ID] = t
	}
	v.CfgHash = cfg.Digest
	// forget paths for targets that are no longer issued
	for id := range v.Paths {
		if _, ok := v.Targets[id]; !ok {
			delete(v.Paths, id)
		}
	}
	return cfg, complete
}

// PushConfigs sends a fresh Config to every live stream whose configuration changed.
func (h *Hub) PushConfigs() {
	h.Mu.Lock()
	ids := make([]string, 0, len(h.Sessions))
	for id := range h.Sessions {
		ids = append(ids, id)
	}
	h.Mu.Unlock()
	for _, id := range ids {
		h.PushOne(id)
	}
}

// PushOne sends one agent a fresh Config if its stream is live and the Config differs from the last one sent. An administrator's
// change (a tier, an override) takes effect through this without waiting for the agent to reconnect.
//
// The agent's record and consent are read, the Config built and queued under the agent's own pushMu. Without it two pushes
// could read the record at different moments and queue in the other order, leaving the agent with the older Config until the
// next refresh found the hashes differing.
func (h *Hub) PushOne(id string) {
	h.Mu.Lock()
	v, s := h.Views[id], h.Sessions[id]
	h.Mu.Unlock()
	if v == nil || s == nil {
		return
	}
	v.PushMu.Lock()
	defer v.PushMu.Unlock()
	a, err := h.C.Store.GetAgent(bg(), h.C.OrgID, id)
	if err != nil || a.Status != store.StatusApproved {
		return
	}
	h.Mu.Lock()
	old := v.CfgHash
	h.Mu.Unlock()
	cfg, complete := h.configForChecked(a, v)
	if !complete {
		// Not a Config to send: its peer list is empty only for want of data. The agent keeps the peers it has, the hash is put
		// back so nothing counts as sent, and the next minute tries again. (At first connect the agent has nothing to lose, so
		// ConfigFor still answers there.)
		h.Mu.Lock()
		v.CfgHash = old
		h.Mu.Unlock()
		return
	}
	h.Mu.Lock()
	same := v.CfgHash == old
	h.Mu.Unlock()
	if same {
		return
	}
	select {
	case s.Push <- &continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Config{Config: cfg}}:
		h.Mu.Lock()
		v.CfgSentAt = h.C.Now()
		h.Mu.Unlock()
	default: // the stream is busy; the next refresh tries again
		h.Mu.Lock()
		v.CfgHash = old
		h.Mu.Unlock()
	}
}

func (h *Hub) settingsChanged(kernel.Settings) {
	go func() {
		h.SyncLinkCredentials() // make what new pairs need and drop what is no longer needed, before the Configs are built
		h.PushConfigs()
	}()
}

// NoteMeasurements stores what an agent measured, for the targets the server issued.
func (h *Hub) NoteMeasurements(agentID string, m *continuumv1.Measurements) {
	if h.ConsentOf(agentID).Has("measure") {
		// The agent should already have stopped sending this; the same defense-in-depth agents.DropAboveTier
		// gives the tier ceiling, extended to a paused collector.
		return
	}
	now := h.C.Now()
	h.Mu.Lock()
	defer h.Mu.Unlock()
	v := h.Views[agentID]
	if v == nil {
		return
	}
	if len(m.Results) > measure.MaxTargets {
		m.Results = m.Results[:measure.MaxTargets]
	}
	for _, r := range m.Results {
		t, ok := v.Targets[r.TargetId]
		if !ok || r.Samples == 0 || r.Samples > 100 || r.Failed > r.Samples {
			continue
		}
		if h.noteAddressTest(v, t, r.Samples, r.Failed, connectivity.Sane(r.RttMinMs), connectivity.Sane(r.RttP50Ms), connectivity.Sane(r.RttP95Ms), now) {
			continue // a test's result is read back on its own, never added to the measured paths
		}
		tr := v.Paths[t.ID]
		if tr == nil {
			tr = &connectivity.PathTrack{T: t}
			v.Paths[t.ID] = tr
		}
		tr.T = t
		tr.Add(measure.Result{ID: t.ID, Samples: int(r.Samples), Failed: int(r.Failed), Min: connectivity.Sane(r.RttMinMs), P50: connectivity.Sane(r.RttP50Ms), P95: connectivity.Sane(r.RttP95Ms)}, now)
	}
}

// pathDocs turns the measured paths of every cluster into the documents the UI reads. Called with h.mu held.
func (h *Hub) pathDocs(agentList []store.Agent, clusters []observed.Cluster, names map[string]string, now time.Time) []model.Path {
	ix := observed.BuildAddrIndex(clusters)
	staleAfter := 3 * time.Duration(h.C.Settings().MeasureSeconds) * time.Second
	if staleAfter < 5*time.Minute {
		staleAfter = 5 * time.Minute
	}
	var out []model.Path
	for _, a := range agentList {
		v := h.Views[a.ID]
		if v == nil || a.Status != store.StatusApproved {
			continue
		}
		for _, tr := range v.Paths {
			min, p50, p95, loss, samples := tr.Summary()
			p := model.Path{
				ID: "path-" + interpret.Hash(a.ClusterID, tr.T.Host, fmt.Sprint(tr.T.Port)), FromCluster: a.ClusterID, FromName: a.Name,
				Host: tr.T.Host, Port: tr.T.Port, Label: tr.T.Label, Source: tr.T.Source,
				RTTMin: min, RTTP50: p50, RTTP95: p95, LossPct: loss, Samples: samples, At: kernel.RFC(tr.At), Stale: now.Sub(tr.At) > staleAfter,
			}
			if to := ix.ClusterOf(tr.T.Host, tr.T.Port); to != "" && to != a.ClusterID {
				p.ToCluster, p.ToName = to, names[to]
			}
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ConfigDigests reports, for a connected agent, the digest of the Config last sent and the digest the agent last said is in
// force. They are equal once the agent has applied what it was sent; applied is empty until it has said (and always, for an agent
// older than the echo).
func (h *Hub) ConfigDigests(agentID string) (sent, applied string) {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	if v := h.Views[agentID]; v != nil {
		return v.CfgHash, v.CfgApplied
	}
	return "", ""
}

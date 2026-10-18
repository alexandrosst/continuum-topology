package server

import (
	"fmt"
	"sort"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"
	"continuum/internal/kernel"
	"continuum/internal/measure"
	"continuum/internal/model"
	"continuum/internal/observed"
	"continuum/internal/store"
)

// configFor builds the Config for one agent: what it may send, how often to check itself, and what
// to measure. It also records which targets were issued. The caller must not hold h.mu.
func (h *Hub) configFor(a store.Agent, v *view) *continuumv1.Config {
	cfg, _ := h.configForChecked(a, v)
	return cfg
}

// configForChecked is configFor, and says whether the Config is complete: false when the cluster has link pairs and the
// organisation's agents could not be read, so the peer list is empty for want of data and not because nobody is paired. A caller
// that replaces a Config the agent already has must not send an incomplete one: the agent would drop every peer until the next
// good minute.
func (h *Hub) configForChecked(a store.Agent, v *view) (*continuumv1.Config, bool) {
	complete := true
	set := h.C.Settings()
	resync := int32(set.ConsistencyMinutes * 60)
	if h.ConsistencyEvery > 0 {
		resync = int32(max(h.ConsistencyEvery/time.Second, 1))
	}
	consent := h.consentOf(a.ID) // before taking h.mu: it may read the store
	var orgAgents []store.Agent
	var runnersPaused map[string]bool
	if a.ClusterID != "" && len(peersOf(set.Links, a.ClusterID)) > 0 {
		var err error
		orgAgents, err = h.C.Store.ListAgents(bg(), h.C.OrgID) // also before h.mu, for the same reason
		if err != nil {
			orgAgents, complete = nil, false
			h.C.Log.Warn("could not read the agents to build the link peers", "agent", a.ID, "err", err)
		}
		runnersPaused = h.runnersPaused(a, orgAgents, set.Links)
	}
	creds := h.linkCredsFor(a, set.Links) // before h.mu: it may read and write the store
	facts := configFacts{
		Tier: a.AccessTier, ClusterID: a.ClusterID, Consent: consent, Heartbeat: HeartbeatSeconds, Resync: resync,
		ServerTime: h.C.Now().Unix(), MeasureSeconds: int32(set.MeasureSeconds), IperfClients: creds.clients, IperfServer: creds.server,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	facts.Peers = h.linkPeers(a, orgAgents, set.Links, consent.Has("flow"), runnersPaused)
	for _, p := range set.ProbeTargets {
		if a.ClusterID != "" && p.ClusterID == a.ClusterID {
			facts.ManualTargets = append(facts.ManualTargets, issuedTarget{ID: p.ID, Host: p.Host, Port: p.Port, Label: p.Label, Source: "manual"})
		}
	}
	facts.AddressTargets = h.addressTestTargets(v, h.C.Now())
	facts.DerivedTargets = deriveTargets(v.flows)

	cfg, ts := compileConfig(facts)
	v.targets = map[string]issuedTarget{}
	for _, t := range ts {
		v.targets[t.ID] = t
	}
	v.cfgHash = cfg.Digest
	// forget paths for targets that are no longer issued
	for id := range v.paths {
		if _, ok := v.targets[id]; !ok {
			delete(v.paths, id)
		}
	}
	return cfg, complete
}

// pushConfigs sends a fresh Config to every live stream whose configuration changed.
func (h *Hub) pushConfigs() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.pushOne(id)
	}
}

// pushOne sends one agent a fresh Config if its stream is live and the Config differs from the last one sent. An administrator's
// change (a tier, an override) takes effect through this without waiting for the agent to reconnect.
//
// The agent's record and consent are read, the Config built and queued under the agent's own pushMu. Without it two pushes
// could read the record at different moments and queue in the other order, leaving the agent with the older Config until the
// next refresh found the hashes differing.
func (h *Hub) pushOne(id string) {
	h.mu.Lock()
	v, s := h.views[id], h.sessions[id]
	h.mu.Unlock()
	if v == nil || s == nil {
		return
	}
	v.pushMu.Lock()
	defer v.pushMu.Unlock()
	a, err := h.C.Store.GetAgent(bg(), id)
	if err != nil || a.Status != store.StatusApproved {
		return
	}
	h.mu.Lock()
	old := v.cfgHash
	h.mu.Unlock()
	cfg, complete := h.configForChecked(a, v)
	if !complete {
		// Not a Config to send: its peer list is empty only for want of data. The agent keeps the peers it has, the hash is put
		// back so nothing counts as sent, and the next minute tries again. (At first connect the agent has nothing to lose, so
		// configFor still answers there.)
		h.mu.Lock()
		v.cfgHash = old
		h.mu.Unlock()
		return
	}
	h.mu.Lock()
	same := v.cfgHash == old
	h.mu.Unlock()
	if same {
		return
	}
	select {
	case s.push <- &continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Config{Config: cfg}}:
		h.mu.Lock()
		v.cfgSentAt = h.C.Now()
		h.mu.Unlock()
	default: // the stream is busy; the next refresh tries again
		h.mu.Lock()
		v.cfgHash = old
		h.mu.Unlock()
	}
}

func (h *Hub) settingsChanged(kernel.Settings) {
	go func() {
		h.syncLinkCredentials() // make what new pairs need and drop what is no longer needed, before the Configs are built
		h.pushConfigs()
	}()
}

// noteMeasurements stores what an agent measured, for the targets the server issued.
func (h *Hub) noteMeasurements(agentID string, m *continuumv1.Measurements) {
	if h.consentOf(agentID).Has("measure") {
		// The agent should already have stopped sending this; the same defense-in-depth agents.DropAboveTier
		// gives the tier ceiling, extended to a paused collector.
		return
	}
	now := h.C.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return
	}
	if len(m.Results) > measure.MaxTargets {
		m.Results = m.Results[:measure.MaxTargets]
	}
	for _, r := range m.Results {
		t, ok := v.targets[r.TargetId]
		if !ok || r.Samples == 0 || r.Samples > 100 || r.Failed > r.Samples {
			continue
		}
		if h.noteAddressTest(v, t, r.Samples, r.Failed, sane(r.RttMinMs), sane(r.RttP50Ms), sane(r.RttP95Ms), now) {
			continue // a test's result is read back on its own, never added to the measured paths
		}
		tr := v.paths[t.ID]
		if tr == nil {
			tr = &pathTrack{t: t}
			v.paths[t.ID] = tr
		}
		tr.t = t
		tr.add(measure.Result{ID: t.ID, Samples: int(r.Samples), Failed: int(r.Failed), Min: sane(r.RttMinMs), P50: sane(r.RttP50Ms), P95: sane(r.RttP95Ms)}, now)
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
		v := h.views[a.ID]
		if v == nil || a.Status != store.StatusApproved {
			continue
		}
		for _, tr := range v.paths {
			min, p50, p95, loss, samples := tr.summary()
			p := model.Path{
				ID: "path-" + interpret.Hash(a.ClusterID, tr.t.Host, fmt.Sprint(tr.t.Port)), FromCluster: a.ClusterID, FromName: a.Name,
				Host: tr.t.Host, Port: tr.t.Port, Label: tr.t.Label, Source: tr.t.Source,
				RTTMin: min, RTTP50: p50, RTTP95: p95, LossPct: loss, Samples: samples, At: kernel.RFC(tr.at), Stale: now.Sub(tr.at) > staleAfter,
			}
			if to := ix.ClusterOf(tr.t.Host, tr.t.Port); to != "" && to != a.ClusterID {
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[agentID]; v != nil {
		return v.cfgHash, v.cfgApplied
	}
	return "", ""
}

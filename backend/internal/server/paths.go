package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"
	"continuum/internal/measure"
	"continuum/internal/model"
	"continuum/internal/store"
)

const (
	maxObservedTargets = 16
	roundsKept         = 5
)

// issuedTarget is an address the server asked an agent to time. Results are accepted only for
// targets the server issued, so an agent cannot make up paths for places it was never asked about.
type issuedTarget struct {
	ID     string
	Host   string
	Port   int
	Label  string
	Source string // observed | manual
}

// pathTrack keeps the last few rounds of samples for one target.
type pathTrack struct {
	t      issuedTarget
	rounds []measure.Result
	at     time.Time
}

func (p *pathTrack) add(r measure.Result, at time.Time) {
	p.rounds = append(p.rounds, r)
	if len(p.rounds) > roundsKept {
		p.rounds = p.rounds[len(p.rounds)-roundsKept:]
	}
	p.at = at
}

// summary merges the kept rounds: the median of the medians, the worst 95th percentile, the best
// minimum, and the share of failed attempts. Rounds where every attempt failed do not drag the
// round-trip times toward zero.
func (p *pathTrack) summary() (min, p50, p95, lossPct float64, samples int) {
	var meds []float64
	var failed int
	for _, r := range p.rounds {
		samples += r.Samples
		failed += r.Failed
		if r.Samples-r.Failed > 0 {
			meds = append(meds, r.P50)
			if r.P95 > p95 {
				p95 = r.P95
			}
			if min == 0 || r.Min < min {
				min = r.Min
			}
		}
	}
	if len(meds) > 0 {
		sort.Float64s(meds)
		p50 = meds[len(meds)/2]
	}
	if samples > 0 {
		lossPct = float64(failed) / float64(samples) * 100
	}
	return
}

func observedTargetID(ip string) string { return "ob-" + interpret.Hash("path", ip) }

// deriveTargets picks the addresses outside a cluster that its workloads call most: what to time to
// learn how far away the places it really talks to are. Only TCP, only real (non-noise) traffic,
// one target per address.
func deriveTargets(ft *flowTable) []issuedTarget {
	if ft == nil {
		return nil
	}
	type cand struct {
		ip    string
		port  uint32
		score float64
		best  float64
	}
	byIP := map[string]*cand{}
	for _, e := range ft.edges {
		k := e.Key
		if k.Src.Kind != continuumv1.FlowEndpoint_WORKLOAD || k.Dst.Kind != continuumv1.FlowEndpoint_EXTERNAL || k.Noise != "" || k.Port == 0 {
			continue
		}
		if k.Protocol != "" && k.Protocol != "tcp" {
			continue
		}
		if measure.CheckTarget(k.Dst.Ip, int(k.Port)) != nil {
			continue
		}
		w := float64(e.Connections) + float64(e.BytesOut+e.BytesIn)/1024
		c := byIP[k.Dst.Ip]
		if c == nil {
			c = &cand{ip: k.Dst.Ip}
			byIP[k.Dst.Ip] = c
		}
		c.score += w
		if w >= c.best {
			c.best, c.port = w, k.Port
		}
	}
	all := make([]*cand, 0, len(byIP))
	for _, c := range byIP {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].ip < all[j].ip
	})
	if len(all) > maxObservedTargets {
		all = all[:maxObservedTargets]
	}
	out := make([]issuedTarget, 0, len(all))
	for _, c := range all {
		out = append(out, issuedTarget{ID: observedTargetID(c.ip), Host: c.ip, Port: int(c.port), Source: "observed"})
	}
	return out
}

// configFor builds the Config for one agent: what it may send, how often to check itself, and what
// to measure. It also records which targets were issued. The caller must not hold h.mu.
func (h *Hub) configFor(a store.Agent, v *view) *continuumv1.Config {
	set := h.C.Settings()
	resync := int32(set.ConsistencyMinutes * 60)
	if h.ConsistencyEvery > 0 {
		resync = int32(max(h.ConsistencyEvery/time.Second, 1))
	}
	consent := h.consentOf(a.ID) // before taking h.mu: it may read the store
	cfg := &continuumv1.Config{ApprovedAccessTier: uint32(a.AccessTier), HeartbeatSeconds: HeartbeatSeconds, ResyncSeconds: resync, ServerTimeUnix: h.C.Now().Unix(),
		PausedCollectors: append([]string(nil), consent.Paused...), ExcludedNamespaces: append([]string(nil), consent.Excluded...)}
	h.mu.Lock()
	defer h.mu.Unlock()
	var ts []issuedTarget
	for _, p := range set.ProbeTargets {
		if a.ClusterID != "" && p.ClusterID == a.ClusterID {
			ts = append(ts, issuedTarget{ID: p.ID, Host: p.Host, Port: p.Port, Label: p.Label, Source: "manual"})
		}
	}
	if a.AccessTier >= 2 {
		ts = append(ts, deriveTargets(v.flows)...)
	}
	if consent.has("measure") {
		ts = nil // the agent is asked to stop measuring; nothing is issued, so nothing measured is accepted either
	}
	if len(ts) > measure.MaxTargets {
		ts = ts[:measure.MaxTargets]
	}
	v.targets = map[string]issuedTarget{}
	for _, t := range ts {
		v.targets[t.ID] = t
		cfg.ProbeTargets = append(cfg.ProbeTargets, &continuumv1.ProbeTarget{Id: t.ID, Host: t.Host, Port: uint32(t.Port)})
	}
	if len(ts) > 0 {
		cfg.MeasureSeconds = int32(set.MeasureSeconds)
	}
	v.cfgHash = configHash(cfg)
	// forget paths for targets that are no longer issued
	for id := range v.paths {
		if _, ok := v.targets[id]; !ok {
			delete(v.paths, id)
		}
	}
	return cfg
}

func configHash(c *continuumv1.Config) string {
	s := fmt.Sprintf("%d|%d|%d|%d|%s|%s", c.ApprovedAccessTier, c.HeartbeatSeconds, c.ResyncSeconds, c.MeasureSeconds, strings.Join(c.PausedCollectors, ","), strings.Join(c.ExcludedNamespaces, ","))
	for _, t := range c.ProbeTargets {
		s += fmt.Sprintf("|%s@%s:%d", t.Id, t.Host, t.Port)
	}
	return interpret.Hash("cfg", s)
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
func (h *Hub) pushOne(id string) {
	a, err := h.C.Store.GetAgent(bg(), id)
	if err != nil || a.Status != store.StatusApproved {
		return
	}
	h.mu.Lock()
	v, s := h.views[id], h.sessions[id]
	old := ""
	if v != nil {
		old = v.cfgHash
	}
	h.mu.Unlock()
	if v == nil || s == nil {
		return
	}
	cfg := h.configFor(a, v)
	h.mu.Lock()
	same := v.cfgHash == old
	h.mu.Unlock()
	if same {
		return
	}
	select {
	case s.push <- &continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Config{Config: cfg}}:
	default: // the stream is busy; the next refresh tries again
		h.mu.Lock()
		v.cfgHash = old
		h.mu.Unlock()
	}
}

func (h *Hub) settingsChanged(Settings) { go h.pushConfigs() }

// noteMeasurements stores what an agent measured, for the targets the server issued.
func (h *Hub) noteMeasurements(agentID string, m *continuumv1.Measurements) {
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
		tr := v.paths[t.ID]
		if tr == nil {
			tr = &pathTrack{t: t}
			v.paths[t.ID] = tr
		}
		tr.t = t
		tr.add(measure.Result{ID: t.ID, Samples: int(r.Samples), Failed: int(r.Failed), Min: sane(r.RttMinMs), P50: sane(r.RttP50Ms), P95: sane(r.RttP95Ms)}, now)
	}
}

// sane keeps a reported time within what a TCP connect can plausibly take.
func sane(ms float64) float64 {
	if ms != ms || ms < 0 {
		return 0
	}
	if ms > 60000 {
		return 60000
	}
	return ms
}

// clusterOf says which onboarded cluster an address belongs to, when that is unambiguous.
func (ix *addrIndex) clusterOf(ip string, port int) string {
	if ts := ix.reach[fmt.Sprintf("%s:%d", ip, port)]; len(ts) > 0 {
		c := ts[0].cluster
		for _, t := range ts {
			if t.cluster != c {
				return ""
			}
		}
		return c
	}
	if c := uniq(ix.nodeIPs[ip]); len(c) == 1 {
		return c[0]
	}
	if c := uniq(ix.egress[ip]); len(c) == 1 {
		return c[0]
	}
	return ""
}

// pathDocs turns the measured paths of every cluster into the documents the UI reads. Called with h.mu held.
func (h *Hub) pathDocs(agents []store.Agent, observed []observedCluster, names map[string]string, now time.Time) []model.Path {
	ix := buildAddrIndex(observed)
	staleAfter := 3 * time.Duration(h.C.Settings().MeasureSeconds) * time.Second
	if staleAfter < 5*time.Minute {
		staleAfter = 5 * time.Minute
	}
	var out []model.Path
	for _, a := range agents {
		v := h.views[a.ID]
		if v == nil || a.Status != store.StatusApproved {
			continue
		}
		for _, tr := range v.paths {
			min, p50, p95, loss, samples := tr.summary()
			p := model.Path{
				ID: "path-" + interpret.Hash(a.ClusterID, tr.t.Host, fmt.Sprint(tr.t.Port)), FromCluster: a.ClusterID, FromName: a.Name,
				Host: tr.t.Host, Port: tr.t.Port, Label: tr.t.Label, Source: tr.t.Source,
				RTTMin: min, RTTP50: p50, RTTP95: p95, LossPct: loss, Samples: samples, At: rfc(tr.at), Stale: now.Sub(tr.at) > staleAfter,
			}
			if to := ix.clusterOf(tr.t.Host, tr.t.Port); to != "" && to != a.ClusterID {
				p.ToCluster, p.ToName = to, names[to]
			}
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

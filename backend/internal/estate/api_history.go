package estate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"continuum/internal/agents"
	"continuum/internal/history"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

func (h *API) historyIndex(w http.ResponseWriter, r *http.Request) {
	since, err1 := httpkit.ParseTime(r, "since")
	until, err2 := httpkit.ParseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	pts, err := h.For(r).C.Store.ListHistory(r.Context(), h.For(r).C.OrgID, since, until)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type point struct {
		At    string `json:"at"`
		Bytes int    `json:"bytes"`
	}
	out := make([]point, len(pts))
	for i, p := range pts {
		out[i] = point{kernel.RFC(p.At), p.Bytes}
	}
	set := h.For(r).C.Settings()
	httpkit.WriteJSON(w, 200, map[string]any{"points": out, "snapshotMinutes": set.SnapshotMinutes, "retentionDays": set.RetentionDays})
}

func (h *API) historySnapshot(w http.ResponseWriter, r *http.Request) {
	at, err := httpkit.ParseTime(r, "at")
	if err != nil || at.IsZero() {
		httpkit.WriteErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	p, data, err := h.For(r).C.Store.GetHistory(r.Context(), h.For(r).C.OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "nothing was recorded at or before that time")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	t, err := history.Decode(data)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"at": kernel.RFC(p.At), "topology": t}
	if agentList := h.historicAgents(r, p.At); agentList != nil {
		out["agents"] = agentList
	}
	httpkit.WriteJSON(w, 200, out)
}

// historicAgents projects AsOfEntities' "agent" kind into the shape the UI overlays onto live agent
// state (HistoricAgent), asked about the exact moment GetHistory just resolved to so the agents shown
// always describe the same instant as the topology returned beside them. It returns nil -- not an
// error -- whenever that is not possible: no graph configured, or nothing recorded about any agent that
// far back. The UI already treats a snapshot with no "agents" field as "show today's agents unchanged,"
// which is the honest answer when this server has no memory of what they looked like then.
func (h *API) historicAgents(r *http.Request, at time.Time) []agents.HistoricAgent {
	g := h.graph(r)
	if g == nil {
		return nil
	}
	_, entities, err := g.AsOfEntities(r.Context(), h.For(r).C.OrgID, at)
	if err != nil {
		return nil
	}
	var out []agents.HistoricAgent
	for _, e := range entities {
		if e.Kind != "agent" {
			continue
		}
		var snap agents.AgentSnapshot
		if json.Unmarshal(e.Doc, &snap) != nil {
			continue
		}
		out = append(out, agents.HistoricAgent{ID: e.ID, AgentSnapshot: snap})
	}
	return out
}

// trafficCacheTTL is how long historyTraffic's SQLite slow path reuses a computed response before
// recomputing it. Long enough to absorb a dashboard auto-refreshing this endpoint every few seconds;
// short enough that a person watching it does not see badly stale numbers.
const trafficCacheTTL = 45 * time.Second

type trafficCacheEntry struct {
	hours   int
	at      time.Time
	payload map[string]any
}

// trafficCache is a tiny per-organisation cache for historyTraffic's slow path: on a plain SQLite
// deployment (no Neo4j), that path lists up to `hours` worth of history points, downsamples to 300,
// and decodes 300 full gzip+JSON topology snapshots just to read one counter out of each - too
// expensive to redo on every request a dashboard's auto-refresh makes. It lives on the organisation's
// own *Core (see Core.ForOrg), so one organisation's cached traffic can never be served to another's.
type trafficCache struct {
	mu    sync.Mutex
	entry *trafficCacheEntry
	// deps holds what the two history endpoints read out of a stored snapshot (see snapshotDeps), so a
	// snapshot is decoded once, not once per request.
	deps map[snapshotKey]map[string]depNumbers
}

// snapshotKey names one stored snapshot. The stored size is part of it because a snapshot taken in the
// same second as an earlier one replaces it.
type snapshotKey struct {
	at    int64
	bytes int
}

// depNumbers is the part of a Dependency the history endpoints chart.
type depNumbers struct {
	bytes   uint64
	rttMs   float64
	lossPct *float64
}

// maxCachedSnapshots bounds trafficCache.deps: a little over the 300 points historyTraffic reads in a
// request. When it is full the cache starts again, which costs no more than not having one.
const maxCachedSnapshots = 320

// snapshotDeps returns the per-dependency numbers of the snapshot at p, decoding it only the first
// time. ok is false when the snapshot cannot be read or decoded.
func snapshotDeps(ctx context.Context, core *Service, p store.HistoryPoint) (map[string]depNumbers, bool) {
	c := trafficSt(core)
	key := snapshotKey{p.At.UnixMilli(), p.Bytes}
	if c != nil {
		c.mu.Lock()
		m, ok := c.deps[key]
		c.mu.Unlock()
		if ok {
			return m, true
		}
	}
	_, data, err := core.Store.GetHistory(ctx, core.OrgID, p.At)
	if err != nil {
		return nil, false
	}
	t, err := history.Decode(data)
	if err != nil {
		return nil, false
	}
	m := make(map[string]depNumbers, len(t.Dependencies))
	for _, d := range t.Dependencies {
		n := depNumbers{bytes: d.Bytes, rttMs: d.RttMs}
		if d.Stats != nil {
			n.lossPct = d.Stats.LossPct
		}
		m[d.ID] = n
	}
	if c != nil {
		c.mu.Lock()
		if c.deps == nil || len(c.deps) >= maxCachedSnapshots {
			c.deps = map[snapshotKey]map[string]depNumbers{}
		}
		c.deps[key] = m
		c.mu.Unlock()
	}
	return m, true
}

func (c *trafficCache) get(hours int, now time.Time) (map[string]any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entry == nil || c.entry.hours != hours || now.Sub(c.entry.at) > trafficCacheTTL {
		return nil, false
	}
	return c.entry.payload, true
}

func (c *trafficCache) set(hours int, now time.Time, payload map[string]any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entry = &trafficCacheEntry{hours: hours, at: now, payload: payload}
}

func (h *API) historyTraffic(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24*30 {
			httpkit.WriteErr(w, 400, "hours must be between 1 and 720")
			return
		}
		hours = n
	}
	core := h.For(r).C
	now := core.Now()
	if ts, ok := core.Store.(interface {
		TrafficSamples(context.Context, string, time.Time, time.Time) ([]history.Sample, bool)
	}); ok {
		if samples, ok := ts.TrafficSamples(r.Context(), core.OrgID, now.Add(-time.Duration(hours)*time.Hour), time.Time{}); ok {
			const most = 300
			if len(samples) > most {
				keep := make([]history.Sample, 0, most)
				for i := 0; i < most; i++ {
					keep = append(keep, samples[i*(len(samples)-1)/(most-1)])
				}
				samples = keep
			}
			httpkit.WriteJSON(w, 200, map[string]any{"hours": hours, "snapshots": len(samples), "rates": history.Rates(samples)})
			return
		}
	}
	if payload, ok := trafficSt(core).get(hours, now); ok {
		httpkit.WriteJSON(w, 200, payload)
		return
	}
	pts, err := core.Store.ListHistory(r.Context(), core.OrgID, now.Add(-time.Duration(hours)*time.Hour), time.Time{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A long window holds many snapshots; a few hundred, evenly spread, give the same averages.
	const most = 300
	if len(pts) > most {
		keep := make([]store.HistoryPoint, 0, most)
		for i := 0; i < most; i++ {
			keep = append(keep, pts[i*(len(pts)-1)/(most-1)])
		}
		pts = keep
	}
	var samples []history.Sample
	for _, p := range pts {
		deps, ok := snapshotDeps(r.Context(), core, p)
		if !ok {
			continue
		}
		s := history.Sample{At: p.At, Bytes: make(map[string]uint64, len(deps))}
		for id, d := range deps {
			s.Bytes[id] = d.bytes
		}
		samples = append(samples, s)
	}
	payload := map[string]any{"hours": hours, "snapshots": len(samples), "rates": history.Rates(samples)}
	trafficSt(core).set(hours, now, payload)
	httpkit.WriteJSON(w, 200, payload)
}

// maxDependencySeriesPoints caps how many points historyDependencySeries ever decodes and returns -
// a sparkline only needs enough samples to show the shape of a trend, not every recorded snapshot,
// and this keeps a long `hours` window cheap the same way historyTraffic's own `most` cap does.
const maxDependencySeriesPoints = 120

// dependencySeriesPoint is one sample in a dependency's RTT/loss/throughput trend. RttMs follows
// Dependency.RttMs's own "0 means no sample" convention; LossPct is a pointer for the same reason
// Dependency.Stats.LossPct is one (nil means not measured, never a fabricated 0%); BytesPerSec is nil
// for the first point returned (there is no earlier sample to derive a rate from) and whenever the
// gap to the previous point is zero or negative.
type dependencySeriesPoint struct {
	At          string   `json:"at"`
	RttMs       float64  `json:"rttMs,omitempty"`
	LossPct     *float64 `json:"lossPct,omitempty"`
	BytesPerSec *float64 `json:"bytesPerSec,omitempty"`
}

// historyDependencySeries returns one dependency's RTT/loss/throughput trend across recorded
// history - the per-point analogue of historyTraffic's aggregate average/peak, for a sparkline
// rather than a summary number. Same downsampling idea as historyTraffic's slow path, and the same
// per-snapshot cache (snapshotDeps), so hovering an edge twice decodes nothing the second time. A snapshot the dependency didn't exist in yet (too
// young, or since removed) is skipped rather than turned into a fabricated zero point.
func (h *API) historyDependencySeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpkit.WriteErr(w, 400, "a dependency id is required")
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24*30 {
			httpkit.WriteErr(w, 400, "hours must be between 1 and 720")
			return
		}
		hours = n
	}
	core := h.For(r).C
	now := core.Now()
	pts, err := core.Store.ListHistory(r.Context(), core.OrgID, now.Add(-time.Duration(hours)*time.Hour), time.Time{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(pts) > maxDependencySeriesPoints {
		keep := make([]store.HistoryPoint, 0, maxDependencySeriesPoints)
		for i := 0; i < maxDependencySeriesPoints; i++ {
			keep = append(keep, pts[i*(len(pts)-1)/(maxDependencySeriesPoints-1)])
		}
		pts = keep
	}
	out := make([]dependencySeriesPoint, 0, len(pts))
	var prevBytes uint64
	var prevAt time.Time
	havePrev := false
	for _, p := range pts {
		deps, ok := snapshotDeps(r.Context(), core, p)
		if !ok {
			continue
		}
		dep, ok := deps[id]
		if !ok {
			continue
		}
		pt := dependencySeriesPoint{At: kernel.RFC(p.At), RttMs: dep.rttMs, LossPct: dep.lossPct}
		if havePrev {
			if dt := p.At.Sub(prevAt).Seconds(); dt > 0 {
				delta := dep.bytes - prevBytes // counter reset (dep.bytes < prevBytes) wraps to a huge
				if dep.bytes < prevBytes {     // delta instead, so treat it as a restart from zero.
					delta = dep.bytes
				}
				bps := float64(delta) / dt
				pt.BytesPerSec = &bps
			}
		}
		out = append(out, pt)
		prevBytes, prevAt, havePrev = dep.bytes, p.At, true
	}
	httpkit.WriteJSON(w, 200, map[string]any{"id": id, "hours": hours, "points": out})
}

func (h *API) recordNow(w http.ResponseWriter, r *http.Request) {
	h.For(r).ScanNow(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// trafficSt is historyTraffic's short-TTL cache for its SQLite slow path. It is one per organisation, so one organisation's cached
// traffic can never reach another's.
func trafficSt(c *Service) *trafficCache {
	return kernel.OrgState(c.Core, "estate.traffic", func() *trafficCache { return &trafficCache{} })
}

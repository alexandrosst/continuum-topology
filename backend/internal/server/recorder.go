package server

import (
	"context"
	"sync/atomic"
	"time"

	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/store"
)

func bg() context.Context { return context.Background() }

const (
	scanEvery       = 10 * time.Second // the soonest a change is looked at
	tickEvery       = 5 * time.Second
	heartbeatSnap   = time.Hour // an unchanged estate is still recorded this often
	pruneEvery      = 10 * time.Minute
	eventRetention  = 90 * 24 * time.Hour
	maxStoredEvents = 100000
)

// recorder keeps the history: it looks at the topology when something changed, and at least every
// SnapshotMinutes, describes what changed since the last look, and stores a snapshot when there is
// something new to remember.
type recorder struct {
	h       *Hub
	differ  *history.Differ
	changed atomic.Bool

	prev     *model.Topology
	lastFP   string
	lastSnap time.Time
	lastScan time.Time
	lastPrun time.Time
	loaded   bool
}

func newRecorder(h *Hub) *recorder { return &recorder{h: h, differ: history.NewDiffer()} }

// Run starts the background work of the hub: recording history and keeping agents' configuration
// current. It returns when ctx ends.
func (h *Hub) Run(ctx context.Context) {
	cfg := time.NewTicker(time.Minute)
	defer cfg.Stop()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cfg.C:
			h.pushConfigs()
			h.C.ExpirePending(ctx, h.C.OrgID)
		case <-t.C:
			h.rec.tick(ctx)
			h.twinTick(ctx)
		}
	}
}

func (r *recorder) tick(ctx context.Context) {
	now := r.h.C.Now()
	set := r.h.C.Settings()
	if !r.loaded {
		r.loaded = true
		// Continue from the last recorded picture so a restart does not look like everything changing.
		if _, data, err := r.h.C.Store.GetHistory(ctx, r.h.C.OrgID, now); err == nil {
			if t, err := history.Decode(data); err == nil {
				r.prev = &t
				r.lastFP = history.Fingerprint(t)
			}
		}
		if pts, err := r.h.C.Store.ListHistory(ctx, r.h.C.OrgID, now.Add(-2*time.Hour), time.Time{}); err == nil && len(pts) > 0 {
			r.lastSnap = pts[len(pts)-1].At
		}
	}
	periodic := now.Sub(r.lastSnap) >= time.Duration(set.SnapshotMinutes)*time.Minute
	dirty := r.changed.Load() && now.Sub(r.lastScan) >= scanEvery
	if dirty || periodic {
		r.changed.Store(false)
		r.scan(ctx, now, periodic)
	}
	if now.Sub(r.lastPrun) >= pruneEvery {
		r.lastPrun = now
		r.prune(ctx, now, set)
	}
}

// scan looks at the topology now. Events are stored whenever there are any; a snapshot is stored
// when something changed, when the periodic interval is due and the estate differs from the last
// stored one, or when an hour has passed regardless.
func (r *recorder) scan(ctx context.Context, now time.Time, periodic bool) {
	r.lastScan = now
	doc, err := r.h.State(ctx)
	if err != nil {
		r.h.Log.Error("history: could not build the topology", "err", err)
		return
	}
	if len(doc.Topology.Clusters) == 0 && r.prev == nil {
		return // nothing to remember yet
	}
	cur := history.Compact(doc.Topology)
	evs := r.differ.Diff(r.prev, cur, now)
	data, fp, err := history.Encode(cur)
	if err != nil {
		r.h.Log.Error("history: could not encode a snapshot", "err", err)
		return
	}
	if len(evs) > 0 {
		if err := r.h.C.Store.AddEvents(ctx, r.h.C.OrgID, evs); err != nil {
			r.h.Log.Error("history: could not store events", "err", err)
		}
	}
	save := r.prev == nil || len(evs) > 0 || fp != r.lastFP || now.Sub(r.lastSnap) >= heartbeatSnap
	if save {
		if err := r.h.C.Store.AddHistory(ctx, r.h.C.OrgID, now, data); err != nil {
			r.h.Log.Error("history: could not store a snapshot", "err", err)
			return
		}
		r.lastSnap, r.lastFP = now, fp
	}
	r.prev = &cur
}

func (r *recorder) prune(ctx context.Context, now time.Time, set Settings) {
	pts, err := r.h.C.Store.ListHistory(ctx, r.h.C.OrgID, time.Time{}, time.Time{})
	if err == nil {
		if del := history.Retention(pts, now, set.RetentionDays, int64(set.MaxHistoryMB)<<20); len(del) > 0 {
			if err := r.h.C.Store.DeleteHistory(ctx, r.h.C.OrgID, del); err != nil {
				r.h.Log.Error("history: pruning failed", "err", err)
			}
		}
	}
	_ = r.h.C.Store.PruneEvents(ctx, r.h.C.OrgID, now.Add(-eventRetention), maxStoredEvents)
	// The organisation's own event retention is opt-in (0 = keep forever, the existing behaviour above is
	// unaffected either way): when set, prune purely by age, with no count-based cap of its own.
	if set.EventRetentionDays > 0 {
		cutoff := now.Add(-time.Duration(set.EventRetentionDays) * 24 * time.Hour)
		if err := r.h.C.Store.PruneEvents(ctx, r.h.C.OrgID, cutoff, 0); err != nil {
			r.h.Log.Error("history: event retention pruning failed", "err", err)
		}
	}
}

// ScanNow records the topology immediately (used by tests and by the administrator's "record now").
func (h *Hub) ScanNow(ctx context.Context) {
	h.rec.loaded = true
	h.rec.scan(ctx, h.C.Now(), true)
}

// storeEvent is a convenience for the few places that add a single event.
func (h *Hub) storeEvent(ctx context.Context, e store.Event) {
	if e.At.IsZero() {
		e.At = h.C.Now()
	}
	_ = h.C.Store.AddEvents(ctx, h.C.OrgID, []store.Event{e})
}

package graph

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"continuum/internal/history"
	"continuum/internal/store"
)

// Store is the server's store when a graph database is configured. Control state (accounts, sessions,
// tokens, agents, the CA, tenants and memberships, and the live workspace) stays in the inner store,
// where a write is one atomic transaction and works when the graph does not. What the graph adds is
// memory: topology history, events, audit and workspace revisions are written here, and the inner
// store keeps only a buffer for the moments the graph cannot be reached (and, on first use, the
// history an earlier version kept there, which is moved across).
type Store struct {
	store.Store
	DB  *DB
	Log *slog.Logger
	Now func() time.Time

	ready atomic.Bool

	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	histDirty map[string]bool // history for this org is being buffered in the inner store
	evDirty   map[string]bool
	pending   map[string]bool // organisations deleted while the graph was away, still to purge
	auditLast int64
	auditInit bool
	lastRec   time.Time
}

func Wrap(inner store.Store, db *DB, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{Store: inner, DB: db, Log: log, Now: time.Now, locks: map[string]*sync.Mutex{}, histDirty: map[string]bool{}, evDirty: map[string]bool{}, pending: map[string]bool{}}
}

func (s *Store) lock(org string) func() {
	s.mu.Lock()
	m := s.locks[org]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[org] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (s *Store) dirty(m map[string]bool, org string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return m[org]
}

func (s *Store) setDirty(m map[string]bool, org string, v bool) {
	s.mu.Lock()
	if v {
		m[org] = true
	} else {
		delete(m, org)
	}
	s.mu.Unlock()
}

// Ready says whether the graph has been prepared and writes go to it directly.
func (s *Store) Ready() bool { return s.ready.Load() }

// Close closes the inner store.
func (s *Store) Close() error { return s.Store.Close() }

// ---- history ----

func (s *Store) AddHistory(ctx context.Context, org string, at time.Time, data []byte) error {
	defer s.lock(org)()
	if s.ready.Load() && !s.dirty(s.histDirty, org) {
		t, err := history.Decode(data)
		if err != nil {
			return err
		}
		err = s.DB.Record(ctx, org, at, t, history.Fingerprint(t), len(data))
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrOutOfOrder) {
			return err
		}
		s.Log.Warn("graph: could not record a snapshot, keeping it aside until the database is back", "org", org, "err", err)
	}
	s.setDirty(s.histDirty, org, true)
	return s.Store.AddHistory(ctx, org, at, data)
}

func (s *Store) ListHistory(ctx context.Context, org string, since, until time.Time) ([]store.HistoryPoint, error) {
	if !s.ready.Load() {
		return s.Store.ListHistory(ctx, org, since, until)
	}
	pts, err := s.DB.Points(ctx, org, since, until)
	if err != nil {
		return s.Store.ListHistory(ctx, org, since, until)
	}
	if s.dirty(s.histDirty, org) {
		if buf, err := s.Store.ListHistory(ctx, org, since, until); err == nil {
			seen := map[int64]bool{}
			for _, p := range pts {
				seen[p.At.Unix()] = true
			}
			for _, p := range buf {
				if !seen[p.At.Unix()] {
					pts = append(pts, p)
				}
			}
			sort.Slice(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
		}
	}
	return pts, nil
}

func (s *Store) GetHistory(ctx context.Context, org string, at time.Time) (store.HistoryPoint, []byte, error) {
	if !s.ready.Load() {
		return s.Store.GetHistory(ctx, org, at)
	}
	snap, err := s.DB.AsOf(ctx, org, at)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return s.Store.GetHistory(ctx, org, at) // the graph is away: what was buffered is all there is
	}
	if s.dirty(s.histDirty, org) {
		// Something newer may still be waiting in the buffer.
		if p, data, e := s.Store.GetHistory(ctx, org, at); e == nil && (err != nil || p.At.After(snap.At)) {
			return p, data, nil
		}
	}
	if err != nil {
		return store.HistoryPoint{}, nil, err
	}
	data, _, err := history.Encode(snap.Topology)
	if err != nil {
		return store.HistoryPoint{}, nil, err
	}
	return store.HistoryPoint{At: snap.At, Bytes: len(data)}, data, nil
}

func (s *Store) DeleteHistory(ctx context.Context, org string, ats []time.Time) error {
	if err := s.Store.DeleteHistory(ctx, org, ats); err != nil {
		return err
	}
	if !s.ready.Load() {
		return nil
	}
	if err := s.DB.DeleteSnapshots(ctx, org, ats); err != nil {
		s.Log.Warn("graph: could not thin old snapshots", "org", org, "err", err)
	}
	return nil
}

// ---- events ----

func (s *Store) AddEvents(ctx context.Context, org string, evs []store.Event) error {
	if len(evs) == 0 {
		return nil
	}
	defer s.lock("ev:" + org)()
	if s.ready.Load() && !s.dirty(s.evDirty, org) {
		err := s.DB.AddEvents(ctx, org, append([]store.Event(nil), evs...))
		if err == nil {
			return nil
		}
		s.Log.Warn("graph: could not store events, keeping them aside until the database is back", "org", org, "err", err)
	}
	s.setDirty(s.evDirty, org, true)
	return s.Store.AddEvents(ctx, org, evs)
}

func (s *Store) ListEvents(ctx context.Context, org string, q store.EventQuery) ([]store.Event, error) {
	if !s.ready.Load() {
		return s.Store.ListEvents(ctx, org, q)
	}
	evs, err := s.DB.Events(ctx, org, q)
	if err != nil {
		return s.Store.ListEvents(ctx, org, q)
	}
	if s.dirty(s.evDirty, org) {
		if buf, err := s.Store.ListEvents(ctx, org, q); err == nil {
			for _, e := range buf {
				e.ID = -e.ID // keep buffered ids from colliding with the graph's
				evs = append(evs, e)
			}
			sort.SliceStable(evs, func(i, j int) bool {
				if !evs[i].At.Equal(evs[j].At) {
					return evs[i].At.After(evs[j].At)
				}
				return evs[i].ID > evs[j].ID
			})
			if q.Limit > 0 && len(evs) > q.Limit {
				evs = evs[:q.Limit]
			}
		}
	}
	return evs, nil
}

func (s *Store) PruneEvents(ctx context.Context, org string, before time.Time, keepNewest int) error {
	err := s.Store.PruneEvents(ctx, org, before, keepNewest)
	if s.ready.Load() {
		if e := s.DB.PruneEvents(ctx, org, before, keepNewest); e != nil {
			s.Log.Warn("graph: could not prune old events", "org", org, "err", e)
		}
	}
	return err
}

// ---- workspace, organisations ----

// PutWorkspace saves in the inner store (atomic, compare-and-swap) and then keeps the revision in the
// graph so any past state of the human layer can be read back.
func (s *Store) PutWorkspace(ctx context.Context, org string, expectRev int64, data []byte, by string, now time.Time) (store.Workspace, error) {
	w, err := s.Store.PutWorkspace(ctx, org, expectRev, data, by, now)
	if err != nil || !s.ready.Load() {
		return w, err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := s.DB.AppendWorkspace(c, org, w); e != nil {
		s.Log.Warn("graph: could not keep a workspace revision", "org", org, "rev", w.Rev, "err", e)
	}
	return w, nil
}

func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	if err := s.Store.DeleteOrg(ctx, id); err != nil {
		return err
	}
	s.setDirty(s.histDirty, id, false)
	s.setDirty(s.evDirty, id, false)
	if !s.ready.Load() {
		s.mu.Lock()
		s.pending[id] = true
		s.mu.Unlock()
		return nil
	}
	if err := s.DB.PurgeTenant(ctx, id); err != nil {
		s.Log.Warn("graph: could not remove a deleted organisation yet, will retry", "org", id, "err", err)
		s.mu.Lock()
		s.pending[id] = true
		s.mu.Unlock()
	}
	return nil
}

// auditCursorKey is where the audit projection remembers its place (a settings row that belongs to no organisation).
const auditCursorKey = "_graph.audit-cursor"

// ---- background work ----

// Start prepares the graph and then keeps it in step until ctx ends: moves buffered history across,
// projects the audit trail, and reconciles tenants and memberships. Until the graph is prepared,
// everything is buffered in the inner store, so starting with the database down loses nothing.
func (s *Store) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			s.Sync(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Sync runs one round of the background work. Exported so tests and the CLI can drive it.
func (s *Store) Sync(ctx context.Context) {
	if !s.ready.Load() {
		if err := s.DB.C.Ensure(ctx); err != nil {
			s.Log.Warn("graph: not ready", "err", err)
			return
		}
		s.markBuffered(ctx)
		s.ready.Store(true)
		s.Log.Info("graph: ready")
	}
	if !s.DB.C.Healthy() {
		return
	}
	s.drain(ctx)
	s.projectAudit(ctx)
	if s.Now().Sub(s.lastRec) >= time.Minute {
		s.lastRec = s.Now()
		s.reconcile(ctx)
	}
	s.retryPurges(ctx)
}

// markBuffered notes which organisations already have history or events in the inner store (an earlier
// version kept them there, or the graph was away): they are drained before anything new is written.
func (s *Store) markBuffered(ctx context.Context) {
	orgs, err := s.Store.ListOrgs(ctx)
	if err != nil {
		return
	}
	for _, o := range orgs {
		if pts, err := s.Store.ListHistory(ctx, o.ID, time.Time{}, time.Time{}); err == nil && len(pts) > 0 {
			s.setDirty(s.histDirty, o.ID, true)
		}
		if evs, err := s.Store.ListEvents(ctx, o.ID, store.EventQuery{Limit: 1}); err == nil && len(evs) > 0 {
			s.setDirty(s.evDirty, o.ID, true)
		}
	}
}

func (s *Store) drain(ctx context.Context) {
	s.mu.Lock()
	var hs, es []string
	for o := range s.histDirty {
		hs = append(hs, o)
	}
	for o := range s.evDirty {
		es = append(es, o)
	}
	s.mu.Unlock()
	for _, o := range hs {
		s.drainHistory(ctx, o)
	}
	for _, o := range es {
		s.drainEvents(ctx, o)
	}
}

func (s *Store) drainHistory(ctx context.Context, org string) {
	for pass := 0; pass < 50; pass++ {
		pts, err := s.Store.ListHistory(ctx, org, time.Time{}, time.Time{})
		if err != nil {
			return
		}
		if len(pts) == 0 {
			unlock := s.lock(org)
			if left, _ := s.Store.ListHistory(ctx, org, time.Time{}, time.Time{}); len(left) == 0 {
				s.setDirty(s.histDirty, org, false)
			}
			unlock()
			return
		}
		if len(pts) > 200 {
			pts = pts[:200]
		}
		for _, p := range pts {
			unlock := s.lock(org)
			_, data, err := s.Store.GetHistory(ctx, org, p.At)
			if err == nil {
				topo, derr := history.Decode(data)
				if derr != nil {
					s.Log.Error("graph: dropping an unreadable buffered snapshot", "org", org, "at", p.At, "err", derr)
				} else {
					err = s.DB.Record(ctx, org, p.At, topo, history.Fingerprint(topo), len(data))
				}
			}
			if err != nil && !errors.Is(err, ErrOutOfOrder) {
				unlock()
				s.Log.Warn("graph: moving buffered history failed, will retry", "org", org, "err", err)
				return
			}
			if errors.Is(err, ErrOutOfOrder) {
				s.Log.Warn("graph: a buffered snapshot is older than the graph's newest and was dropped", "org", org, "at", p.At)
			}
			_ = s.Store.DeleteHistory(ctx, org, []time.Time{p.At})
			unlock()
		}
	}
}

func (s *Store) drainEvents(ctx context.Context, org string) {
	for pass := 0; pass < 50; pass++ {
		unlock := s.lock("ev:" + org)
		evs, err := s.Store.ListEvents(ctx, org, store.EventQuery{Limit: 2000})
		if err != nil {
			unlock()
			return
		}
		if len(evs) == 0 {
			s.setDirty(s.evDirty, org, false)
			unlock()
			return
		}
		ids := make([]int64, len(evs))
		for i, e := range evs {
			ids[i] = e.ID
		}
		if err := s.DB.AddEvents(ctx, org, evs); err != nil {
			unlock()
			s.Log.Warn("graph: moving buffered events failed, will retry", "org", org, "err", err)
			return
		}
		_ = s.Store.DeleteEvents(ctx, org, ids)
		unlock()
	}
}

func (s *Store) projectAudit(ctx context.Context) {
	if !s.auditInit {
		// The cursor lives beside the audit trail it reads (in the control store), so a control
		// database that is replaced starts again from its own beginning.
		var last int64
		if b, err := s.Store.GetSettings(ctx, auditCursorKey); err == nil && b != nil {
			last, _ = strconv.ParseInt(string(b), 10, 64)
		}
		s.auditLast, s.auditInit = last, true
	}
	for pass := 0; pass < 20; pass++ {
		rows, err := s.Store.AuditSince(ctx, s.auditLast, 500)
		if err != nil || len(rows) == 0 {
			return
		}
		if err := s.DB.AddAudit(ctx, rows); err != nil {
			s.Log.Warn("graph: could not project the audit trail", "err", err)
			return
		}
		s.auditLast = rows[len(rows)-1].ID
		if err := s.Store.PutSettings(ctx, auditCursorKey, []byte(strconv.FormatInt(s.auditLast, 10)), s.Now()); err != nil {
			return
		}
		if len(rows) < 500 {
			return
		}
	}
}

// Reconcile makes the graph's tenants and memberships match the authoritative ones. It never deletes
// a tenant on its own account (an empty or mismatched control database must not be able to wipe the
// memory); Orphans reports graph tenants the control database does not know.
func (s *Store) reconcile(ctx context.Context) {
	orgs, err := s.Store.ListOrgs(ctx)
	if err != nil {
		return
	}
	for _, o := range orgs {
		ms, err := s.Store.ListMembers(ctx, o.ID)
		if err != nil {
			continue
		}
		members := make([]MemberInfo, 0, len(ms))
		for _, m := range ms {
			members = append(members, MemberInfo{Name: m.Username, Role: m.Role, Since: m.JoinedAt})
		}
		if err := s.DB.SyncTenant(ctx, TenantInfo{ID: o.ID, Name: o.Name, CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt}, members, s.Now()); err != nil {
			s.Log.Warn("graph: could not project an organisation", "org", o.ID, "err", err)
			return
		}
		if w, err := s.Store.GetWorkspace(ctx, o.ID); err == nil && w.Rev > 0 {
			if revs, err := s.DB.WorkspaceRevs(ctx, o.ID, 1); err == nil && (len(revs) == 0 || revs[0].Rev < w.Rev) {
				_ = s.DB.AppendWorkspace(ctx, o.ID, w)
			}
		}
	}
}

func (s *Store) retryPurges(ctx context.Context) {
	s.mu.Lock()
	var ids []string
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.DB.PurgeTenant(ctx, id); err == nil {
			s.mu.Lock()
			delete(s.pending, id)
			s.mu.Unlock()
		}
	}
}

// Orphans lists tenants the graph holds that the control database does not know.
func (s *Store) Orphans(ctx context.Context) []string {
	if !s.ready.Load() {
		return nil
	}
	ids, err := s.DB.TenantIDs(ctx)
	if err != nil {
		return nil
	}
	orgs, err := s.Store.ListOrgs(ctx)
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, o := range orgs {
		known[o.ID] = true
	}
	var out []string
	for _, id := range ids {
		if !known[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// TrafficSamples serves the traffic endpoint straight from the snapshots' counters. ok is false when
// the graph cannot be the only source (it is away, or history is still being moved across), and the
// caller falls back to reading the snapshots one by one.
func (s *Store) TrafficSamples(ctx context.Context, org string, since, until time.Time) (samples []history.Sample, ok bool) {
	if !s.ready.Load() || s.dirty(s.histDirty, org) {
		return nil, false
	}
	out, err := s.DB.TrafficSamples(ctx, org, since, until)
	if err != nil {
		return nil, false
	}
	return out, true
}

// ---- what the admin API reads ----

// Status is the graph's health, for the settings page.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Ready     bool   `json:"ready"`
	Error     string `json:"error,omitempty"`
	Buffering bool   `json:"buffering"` // some history or events are waiting in the local buffer
	Stats     *Stats `json:"stats,omitempty"`
}

func (s *Store) Status(ctx context.Context, org string) Status {
	st := Status{Enabled: true, Ready: s.ready.Load()}
	if st.Ready {
		if stats, err := s.DB.Stats(ctx, org); err == nil {
			st.Connected, st.Stats = true, &stats
		}
	}
	st.Error = s.DB.C.LastError()
	if !st.Ready && st.Error == "" {
		st.Error = "starting"
	}
	s.mu.Lock()
	st.Buffering = s.histDirty[org] || s.evDirty[org]
	s.mu.Unlock()
	return st
}

func (s *Store) Timeline(ctx context.Context, org, kind, id string, limit int) (Timeline, error) {
	if labelOf(kind) == "" {
		return Timeline{}, store.ErrNotFound
	}
	return s.DB.Timeline(ctx, org, kind, id, limit)
}

func (s *Store) Audit(ctx context.Context, org string, q AuditQuery) ([]AuditRow, error) {
	return s.DB.Audit(ctx, org, q)
}

func (s *Store) WorkspaceRevs(ctx context.Context, org string, limit int) ([]WorkspaceRev, error) {
	return s.DB.WorkspaceRevs(ctx, org, limit)
}

func (s *Store) WorkspaceAt(ctx context.Context, org string, at time.Time) (WorkspaceRev, error) {
	return s.DB.WorkspaceAt(ctx, org, at)
}

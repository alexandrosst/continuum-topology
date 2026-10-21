package estate

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/graph"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

// graph is the organisation's graph store, or nil when the server runs without one.
func (h *API) graph(r *http.Request) GraphAPI {
	g, _ := h.For(r).C.Store.(GraphAPI)
	return g
}

// GraphAPI is what the graph store adds to the plain store: the questions only a memory of the past can
// answer. When the server runs without a graph database, the store does not implement it and the
// routes below say so or fall back to what SQLite keeps.
type GraphAPI interface {
	Status(ctx context.Context, org string) graph.Status
	Timeline(ctx context.Context, org, kind, id string, limit int) (graph.Timeline, error)
	Audit(ctx context.Context, org string, q graph.AuditQuery) ([]graph.AuditRow, error)
	WorkspaceRevs(ctx context.Context, org string, limit int) ([]graph.WorkspaceRev, error)
	WorkspaceAt(ctx context.Context, org string, at time.Time) (graph.WorkspaceRev, error)
	AsOfEntities(ctx context.Context, org string, at time.Time) (time.Time, []graph.EntitySnapshot, error)
	Dependents(ctx context.Context, org string, at time.Time, kind, id string, hops int) (time.Time, []graph.Reached, error)
	Dependencies(ctx context.Context, org string, at time.Time, kind, id string, hops int) (time.Time, []graph.Reached, error)
	DiffEntities(ctx context.Context, org string, from, to time.Time) (graph.StructuralDiff, error)
}

// GET /storage: where history is kept and whether that place is healthy. Anyone in the organisation may
// learn whether history is available; only administrators see why it is not (an error can name a host).
func (h *API) storage(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteJSON(w, 200, storageDoc{Backend: "sqlite"})
		return
	}
	st := g.Status(r.Context(), h.For(r).C.OrgID)
	out := storageDoc{Backend: "neo4j", Enabled: true, Connected: &st.Connected, Ready: &st.Ready, Buffering: &st.Buffering}
	if accounts.Allows(httpkit.Principal(r), accounts.ActAdminister) {
		out.Error, out.Stats = &st.Error, st.Stats
	}
	httpkit.WriteJSON(w, 200, out)
}

// GET /graph/snapshot?at=...: the estate's entities as of a moment, in the graph's own schema-agnostic
// shape -- every kind that has ever been versioned, known to this server's UI or not -- rather than the
// fixed, typed model.Topology historySnapshot projects. Meant for a consumer that walks the graph on its
// own terms: an external integration, or an LLM being fed the estate's memory directly.
func (h *API) graphSnapshot(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteErr(w, 404, "the graph's entities need the graph database, which this server was started without")
		return
	}
	at, err := httpkit.ParseTime(r, "at")
	if err != nil || at.IsZero() {
		httpkit.WriteErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	sat, entities, err := g.AsOfEntities(r.Context(), h.For(r).C.OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "nothing was recorded at or before that time")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, graphSnapshotDoc{At: kernel.RFC(sat), Entities: entities})
}

// parseWalkParams reads the kind/id/at/hops query parameters Dependents and Dependencies share. hops
// defaults to 3 when absent or not a positive number - enough to see past the entity's immediate
// neighbours without walking the whole estate by default; DB.walk clamps it further regardless.
func parseWalkParams(r *http.Request) (kind, id string, at time.Time, hops int, err error) {
	kind, id = r.URL.Query().Get("kind"), r.URL.Query().Get("id")
	if kind == "" || id == "" || len(id) > 300 {
		return "", "", time.Time{}, 0, errors.New("give kind and id")
	}
	at, err = httpkit.ParseTime(r, "at")
	if err != nil || at.IsZero() {
		return "", "", time.Time{}, 0, errors.New("at must be an RFC 3339 time")
	}
	hops, _ = strconv.Atoi(r.URL.Query().Get("hops"))
	if hops <= 0 {
		hops = 3
	}
	return kind, id, at, hops, nil
}

// GET /graph/dependents?kind=&id=&at=&hops=: everything that would be affected, directly or
// transitively, if this entity became unavailable at that moment - the graph walked backward along
// every relationship type it knows (see DB.Dependents). "What breaks if this goes down."
func (h *API) graphDependents(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteErr(w, 404, "graph traversal needs the graph database, which this server was started without")
		return
	}
	kind, id, at, hops, err := parseWalkParams(r)
	if err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	sat, reached, err := g.Dependents(r.Context(), h.For(r).C.OrgID, at, kind, id, hops)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "no such record")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, graphWalkDoc{At: kernel.RFC(sat), Kind: kind, ID: id, Hops: hops, Reached: reached})
}

// GET /graph/dependencies?kind=&id=&at=&hops=: everything this entity itself relies on at that moment -
// the same walk as dependents, outward instead of backward (see DB.Dependencies). "What this needs in
// order to keep working."
func (h *API) graphDependencies(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteErr(w, 404, "graph traversal needs the graph database, which this server was started without")
		return
	}
	kind, id, at, hops, err := parseWalkParams(r)
	if err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	sat, reached, err := g.Dependencies(r.Context(), h.For(r).C.OrgID, at, kind, id, hops)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "no such record")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, graphWalkDoc{At: kernel.RFC(sat), Kind: kind, ID: id, Hops: hops, Reached: reached})
}

// GET /graph/diff?from=&to=: what changed structurally across the whole estate between two moments -
// entities added, entities removed, and entities that looked different by the second moment, down to
// which fields moved (see DB.DiffEntities). Two point-in-time reads compared, not a search through
// events, so it still answers precisely even across a span the event log has since pruned.
func (h *API) graphDiff(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteErr(w, 404, "a structural diff needs the graph database, which this server was started without")
		return
	}
	from, err1 := httpkit.ParseTime(r, "from")
	to, err2 := httpkit.ParseTime(r, "to")
	if err := errors.Join(err1, err2); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	if from.IsZero() || to.IsZero() {
		httpkit.WriteErr(w, 400, "give both from and to as RFC 3339 times")
		return
	}
	diff, err := g.DiffEntities(r.Context(), h.For(r).C.OrgID, from, to)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, graphDiffDoc{From: kernel.RFC(diff.From), To: kernel.RFC(diff.To), Added: diff.Added, Removed: diff.Removed, Changed: diff.Changed})
}

// GET /timeline?kind=service&id=...: every version of one record, what changed between them, the events
// about it and what people did to it (the last only for administrators, as /audit is).
func (h *API) timeline(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteErr(w, 404, "record timelines need the graph database, which this server was started without")
		return
	}
	kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
	if kind == "" || id == "" || len(id) > 300 {
		httpkit.WriteErr(w, 400, "give kind and id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tl, err := g.Timeline(r.Context(), h.For(r).C.OrgID, kind, id, limit)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "no history for that record")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	evs := make([]eventDoc, len(tl.Events))
	for i, e := range tl.Events {
		evs[i] = eventDoc{ID: "ev-" + strconv.FormatInt(e.ID, 10), At: kernel.RFC(e.At), Kind: e.Kind, TargetKind: e.TargetKind, TargetID: e.TargetID, Name: e.Name, ClusterID: e.ClusterID,
			ClusterName: e.ClusterName, Detail: e.Detail, Cause: e.Cause, Severity: e.Severity}
	}
	audit := tl.Audit
	if !accounts.Allows(httpkit.Principal(r), accounts.ActAdminister) { // it names people, which /audit and the state document keep for administrators
		audit = []graph.AuditRow{}
	}
	httpkit.WriteJSON(w, 200, timelineDoc{Kind: tl.Kind, ID: tl.ID, Versions: tl.Versions, Events: evs, Audit: audit})
}

// GET /audit: who did what in this organisation. Administrators only. With the graph it can be searched
// by person, action, record and time; without it the latest actions are listed as SQLite keeps them.
func (h *API) audit(w http.ResponseWriter, r *http.Request) {
	since, err1 := httpkit.ParseTime(r, "since")
	until, err2 := httpkit.ParseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	q := graph.AuditQuery{Actor: r.URL.Query().Get("actor"), Action: r.URL.Query().Get("action"), TargetID: r.URL.Query().Get("target"), Since: since, Until: until}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	org := h.For(r).C.OrgID
	if g := h.graph(r); g != nil {
		if rows, err := g.Audit(r.Context(), org, q); err == nil {
			httpkit.WriteJSON(w, 200, auditSearchDoc{Source: "graph", Rows: rows})
			return
		}
	}
	// Without the graph (or while it is away): the latest rows from the control store, filtered here.
	evs, err := h.For(r).C.Store.ListAudit(r.Context(), org, 500)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rows := []graph.AuditRow{}
	for _, e := range evs {
		if q.Actor != "" && e.Actor != q.Actor || q.Action != "" && !strings.HasPrefix(e.Action, q.Action) || q.TargetID != "" && e.TargetID != q.TargetID ||
			!q.Since.IsZero() && e.At.Before(q.Since) || !q.Until.IsZero() && e.At.After(q.Until) {
			continue
		}
		rows = append(rows, graph.AuditRow{ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, TargetKind: e.TargetKind, TargetID: e.TargetID, Detail: e.Detail})
	}
	if q.Limit > 0 && len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	httpkit.WriteJSON(w, 200, auditSearchDoc{Source: "local", Rows: rows})
}

// GET /workspace/revisions and /workspace/at?at=: the human layer as it was saved over time.
func (h *API) workspaceRevisions(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	if g == nil {
		httpkit.WriteJSON(w, 200, workspaceRevisionsDoc{Revisions: []graph.WorkspaceRev{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	revs, err := g.WorkspaceRevs(r.Context(), h.For(r).C.OrgID, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, workspaceRevisionsDoc{Revisions: revs})
}

func (h *API) workspaceAt(w http.ResponseWriter, r *http.Request) {
	g := h.graph(r)
	at, err := httpkit.ParseTime(r, "at")
	if err != nil || at.IsZero() {
		httpkit.WriteErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	if g == nil {
		httpkit.WriteErr(w, 404, "past revisions of the workspace need the graph database")
		return
	}
	rev, err := g.WorkspaceAt(r.Context(), h.For(r).C.OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		httpkit.WriteErr(w, 404, "the workspace had not been saved by then")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := workspaceAtDoc{Rev: rev.Rev, UpdatedAt: kernel.RFC(rev.At), UpdatedBy: rev.By, Data: rev.Data}
	if workspace.Peek(rev.Data) < workspace.CurrentVersion {
		// A revision saved before observed facts were held apart from the workspace carries them; they are not shown.
		if d, rep, err := workspace.Declare(rev.Data); err == nil {
			out.Data = d
			if rep.Changed() {
				out.Note = rep.Note()
			}
		}
	}
	httpkit.WriteJSON(w, 200, out)
}

type eventDoc struct {
	ID          string `json:"id"`
	At          string `json:"at"`
	Kind        string `json:"kind"`
	TargetKind  string `json:"targetKind"`
	TargetID    string `json:"targetId"`
	Name        string `json:"name"`
	ClusterID   string `json:"clusterId,omitempty"`
	ClusterName string `json:"clusterName,omitempty"`
	Detail      string `json:"detail"`
	Cause       string `json:"cause,omitempty"`
	Severity    string `json:"severity"`
}

func (h *API) listEvents(w http.ResponseWriter, r *http.Request) {
	since, err1 := httpkit.ParseTime(r, "since")
	until, err2 := httpkit.ParseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	if since.IsZero() {
		since = h.For(r).C.Now().Add(-7 * 24 * time.Hour)
	}
	q := store.EventQuery{Since: since, Until: until, Kind: r.URL.Query().Get("kind"), ClusterID: r.URL.Query().Get("cluster"), TargetID: r.URL.Query().Get("target")}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	evs, err := h.For(r).C.Store.ListEvents(r.Context(), h.For(r).C.OrgID, q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]eventDoc, len(evs))
	for i, e := range evs {
		out[i] = eventDoc{ID: "ev-" + strconv.FormatInt(e.ID, 10), At: kernel.RFC(e.At), Kind: e.Kind, TargetKind: e.TargetKind, TargetID: e.TargetID, Name: e.Name, ClusterID: e.ClusterID,
			ClusterName: e.ClusterName, Detail: e.Detail, Cause: e.Cause, Severity: e.Severity}
	}
	httpkit.WriteJSON(w, 200, eventsDoc{Events: out})
}

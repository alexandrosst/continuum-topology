package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"continuum/internal/graph"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

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

func (a *Admin) graphAPI() GraphAPI {
	g, _ := a.C.Store.(GraphAPI)
	return g
}

// GET /storage: where history is kept and whether that place is healthy. Anyone in the organisation may
// learn whether history is available; only administrators see why it is not (an error can name a host).
func (a *Admin) storage(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeJSON(w, 200, map[string]any{"backend": "sqlite", "enabled": false})
		return
	}
	st := g.Status(r.Context(), a.core(r).OrgID)
	out := map[string]any{"backend": "neo4j", "enabled": true, "connected": st.Connected, "ready": st.Ready, "buffering": st.Buffering}
	if roleRank[principal(r).Role] >= roleRank[RoleAdmin] {
		out["error"] = st.Error
		if st.Stats != nil {
			out["stats"] = st.Stats
		}
	}
	writeJSON(w, 200, out)
}

// GET /graph/snapshot?at=...: the estate's entities as of a moment, in the graph's own schema-agnostic
// shape -- every kind that has ever been versioned, known to this server's UI or not -- rather than the
// fixed, typed model.Topology historySnapshot projects. Meant for a consumer that walks the graph on its
// own terms: an external integration, or an LLM being fed the estate's memory directly.
func (a *Admin) graphSnapshot(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeErr(w, 404, "the graph's entities need the graph database, which this server was started without")
		return
	}
	at, err := parseTime(r, "at")
	if err != nil || at.IsZero() {
		writeErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	sat, entities, err := g.AsOfEntities(r.Context(), a.core(r).OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "nothing was recorded at or before that time")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"at": rfc(sat), "entities": entities})
}

// parseWalkParams reads the kind/id/at/hops query parameters Dependents and Dependencies share. hops
// defaults to 3 when absent or not a positive number - enough to see past the entity's immediate
// neighbours without walking the whole estate by default; DB.walk clamps it further regardless.
func parseWalkParams(r *http.Request) (kind, id string, at time.Time, hops int, err error) {
	kind, id = r.URL.Query().Get("kind"), r.URL.Query().Get("id")
	if kind == "" || id == "" || len(id) > 300 {
		return "", "", time.Time{}, 0, errors.New("give kind and id")
	}
	at, err = parseTime(r, "at")
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
func (a *Admin) graphDependents(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeErr(w, 404, "graph traversal needs the graph database, which this server was started without")
		return
	}
	kind, id, at, hops, err := parseWalkParams(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sat, reached, err := g.Dependents(r.Context(), a.core(r).OrgID, at, kind, id, hops)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "no such record")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"at": rfc(sat), "kind": kind, "id": id, "hops": hops, "reached": reached})
}

// GET /graph/dependencies?kind=&id=&at=&hops=: everything this entity itself relies on at that moment -
// the same walk as dependents, outward instead of backward (see DB.Dependencies). "What this needs in
// order to keep working."
func (a *Admin) graphDependencies(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeErr(w, 404, "graph traversal needs the graph database, which this server was started without")
		return
	}
	kind, id, at, hops, err := parseWalkParams(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sat, reached, err := g.Dependencies(r.Context(), a.core(r).OrgID, at, kind, id, hops)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "no such record")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"at": rfc(sat), "kind": kind, "id": id, "hops": hops, "reached": reached})
}

// GET /graph/diff?from=&to=: what changed structurally across the whole estate between two moments -
// entities added, entities removed, and entities that looked different by the second moment, down to
// which fields moved (see DB.DiffEntities). Two point-in-time reads compared, not a search through
// events, so it still answers precisely even across a span the event log has since pruned.
func (a *Admin) graphDiff(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeErr(w, 404, "a structural diff needs the graph database, which this server was started without")
		return
	}
	from, err1 := parseTime(r, "from")
	to, err2 := parseTime(r, "to")
	if err := errors.Join(err1, err2); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if from.IsZero() || to.IsZero() {
		writeErr(w, 400, "give both from and to as RFC 3339 times")
		return
	}
	diff, err := g.DiffEntities(r.Context(), a.core(r).OrgID, from, to)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"from": rfc(diff.From), "to": rfc(diff.To), "added": diff.Added, "removed": diff.Removed, "changed": diff.Changed})
}

// GET /timeline?kind=service&id=...: every version of one record, what changed between them, the events
// about it and what people did to it (the last only for administrators, as /audit is).
func (a *Admin) timeline(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeErr(w, 404, "record timelines need the graph database, which this server was started without")
		return
	}
	kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
	if kind == "" || id == "" || len(id) > 300 {
		writeErr(w, 400, "give kind and id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tl, err := g.Timeline(r.Context(), a.core(r).OrgID, kind, id, limit)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "no history for that record")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	evs := make([]eventDoc, len(tl.Events))
	for i, e := range tl.Events {
		evs[i] = eventDoc{ID: "ev-" + itoa(e.ID), At: rfc(e.At), Kind: e.Kind, TargetKind: e.TargetKind, TargetID: e.TargetID, Name: e.Name, ClusterID: e.ClusterID,
			ClusterName: e.ClusterName, Detail: e.Detail, Cause: e.Cause, Severity: e.Severity}
	}
	audit := tl.Audit
	if roleRank[principal(r).Role] < roleRank[RoleAdmin] { // it names people, which /audit and the state document keep for administrators
		audit = []graph.AuditRow{}
	}
	writeJSON(w, 200, map[string]any{"kind": tl.Kind, "id": tl.ID, "versions": tl.Versions, "events": evs, "audit": audit})
}

// GET /audit: who did what in this organisation. Administrators only. With the graph it can be searched
// by person, action, record and time; without it the latest actions are listed as SQLite keeps them.
func (a *Admin) audit(w http.ResponseWriter, r *http.Request) {
	since, err1 := parseTime(r, "since")
	until, err2 := parseTime(r, "until")
	if err := errors.Join(err1, err2); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	q := graph.AuditQuery{Actor: r.URL.Query().Get("actor"), Action: r.URL.Query().Get("action"), TargetID: r.URL.Query().Get("target"), Since: since, Until: until}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	org := a.core(r).OrgID
	if g := a.graphAPI(); g != nil {
		if rows, err := g.Audit(r.Context(), org, q); err == nil {
			writeJSON(w, 200, map[string]any{"source": "graph", "rows": rows})
			return
		}
	}
	// Without the graph (or while it is away): the latest rows from the control store, filtered here.
	evs, err := a.C.Store.ListAudit(r.Context(), org, 500)
	if err != nil {
		a.fail(w, err)
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
	writeJSON(w, 200, map[string]any{"source": "local", "rows": rows})
}

// GET /workspace/revisions and /workspace/at?at=: the human layer as it was saved over time.
func (a *Admin) workspaceRevisions(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	if g == nil {
		writeJSON(w, 200, map[string]any{"revisions": []graph.WorkspaceRev{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	revs, err := g.WorkspaceRevs(r.Context(), a.core(r).OrgID, limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": revs})
}

func (a *Admin) workspaceAt(w http.ResponseWriter, r *http.Request) {
	g := a.graphAPI()
	at, err := parseTime(r, "at")
	if err != nil || at.IsZero() {
		writeErr(w, 400, "at must be an RFC 3339 time")
		return
	}
	if g == nil {
		writeErr(w, 404, "past revisions of the workspace need the graph database")
		return
	}
	rev, err := g.WorkspaceAt(r.Context(), a.core(r).OrgID, at)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "the workspace had not been saved by then")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := map[string]any{"rev": rev.Rev, "updatedAt": rfc(rev.At), "updatedBy": rev.By, "data": rev.Data}
	if workspace.Peek(rev.Data) < workspace.CurrentVersion {
		// A revision saved before observed facts were held apart from the workspace carries them; they are not shown.
		if d, rep, err := workspace.Declare(rev.Data); err == nil {
			out["data"] = json.RawMessage(d)
			if rep.Changed() {
				out["note"] = rep.Note()
			}
		}
	}
	writeJSON(w, 200, out)
}

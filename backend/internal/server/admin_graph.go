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

// GET /timeline?kind=service&id=...: every version of one record, what changed between them, the events
// about it and what people did to it.
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
	writeJSON(w, 200, map[string]any{"kind": tl.Kind, "id": tl.ID, "versions": tl.Versions, "events": evs, "audit": tl.Audit})
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

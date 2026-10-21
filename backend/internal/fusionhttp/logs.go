package fusionhttp

import (
	"net/http"

	"continuum/internal/fusionapi"
	"continuum/internal/httpkit"
)

import ()

func (g *Guard) Logs(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	q := r.URL.Query()
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 200, 2000)
	if err != nil {
		g.Err(w, err)
		return
	}
	var order string
	switch order = q.Get("order"); order {
	case "", "newest", "oldest":
	default:
		g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "order must be newest or oldest"})
		return
	}
	backward := order != "oldest"
	var entries []fusionapi.LogEntry
	var truncated bool
	if raw := q.Get("query"); raw != "" {
		entries, truncated, err = c.RawLogQuery(r.Context(), who.Scope, raw, backward, tr, limit)
	} else if scope, serr := g.Scope(r, who); serr != nil {
		err = serr
	} else {
		cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (Op.CheckQuery)
		entries, truncated, err = c.Logs(r.Context(), scope, fusionapi.LogFilter{
			Service: q.Get("service"), Namespace: q.Get("namespace"), Pod: q.Get("pod"), Cluster: q.Get("cluster"), Categories: cats,
			TraceID: q.Get("trace_id"), SpanID: q.Get("span_id"), Severity: q.Get("severity"), Contains: q.Get("contains"), Backward: backward,
		}, tr, limit)
	}
	if err != nil {
		g.Err(w, err)
		return
	}
	if entries == nil {
		entries = []fusionapi.LogEntry{}
	}
	httpkit.WriteJSON(w, 200, logsDoc{Entries: entries, Truncated: truncated, Limit: limit, From: tr.From, To: tr.To})
}

package fusionhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"ikhnos/internal/fusionapi"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
)

import ()

func (g *Guard) Traces(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	q := r.URL.Query()
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	opts, fused, err := fusionapi.ParseFuseParams(q)
	if err != nil {
		g.Err(w, err)
		return
	}
	opts.Extras = g.Extras()
	stream, err := wantsStream(r)
	if err != nil {
		g.Err(w, err)
		return
	}
	def, max := 20, 100
	if fused { // each hit is read in full, so a fused search is a smaller page
		def, max = 10, fusionapi.MaxBatch
	}
	limit, err := fusionapi.Limit(q.Get("limit"), def, max)
	if err != nil {
		g.Err(w, err)
		return
	}
	var traces []fusionapi.TraceSummary
	var truncated bool
	if raw := q.Get("q"); raw != "" {
		traces, truncated, err = c.RawTraceSearch(r.Context(), who.Scope, raw, tr, limit)
	} else {
		var minD, maxD time.Duration
		if minD, err = fusionapi.DurationParam(q.Get("min_duration")); err == nil {
			maxD, err = fusionapi.DurationParam(q.Get("max_duration"))
		}
		var scope fusionapi.Scope
		if err == nil {
			scope, err = g.Scope(r, who)
		}
		if err == nil {
			cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (Op.CheckQuery)
			traces, truncated, err = c.SearchTraces(r.Context(), scope, fusionapi.TraceFilter{
				Service: q.Get("service"), Namespace: q.Get("namespace"), Cluster: q.Get("cluster"), Name: q.Get("name"), Status: q.Get("status"), Categories: cats,
				MinDuration: minD, MaxDuration: maxD,
			}, tr, limit)
		}
	}
	if err != nil {
		g.Err(w, err)
		return
	}
	if traces == nil {
		traces = []fusionapi.TraceSummary{}
	}
	page := tracesDoc{Traces: traces, Truncated: truncated, Limit: limit, From: tr.From, To: tr.To}
	if !fused {
		httpkit.WriteJSON(w, 200, page)
		return
	}
	ids := make([]string, len(traces))
	for i, t := range traces {
		ids[i] = t.TraceID
	}
	if len(ids) > 0 && !g.bulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		page.Type = "traces"
		g.streamBulk(w, r, c, who, ids, opts, page)
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	summary := fusionapi.Summarise(items)
	page.Results, page.Summary = nonNilItems(items), &summary
	httpkit.WriteJSON(w, 200, page)
}

// Trace is one trace; with fused=true (or an include list) it is the fused object: each span carrying its log
// lines and each resource its metric series, and whatever else the options ask for.
func (g *Guard) Trace(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	id := r.PathValue("id")
	opts, fused, err := fusionapi.ParseFuseParams(r.URL.Query())
	if err != nil {
		g.Err(w, err)
		return
	}
	opts.Extras = g.Extras()
	if !fused {
		tr, err := c.Trace(r.Context(), who.Scope, id)
		if err != nil {
			g.Err(w, err)
			return
		}
		httpkit.WriteJSON(w, 200, tr)
		return
	}
	f, err := c.FuseTrace(r.Context(), who.Scope, id, opts)
	if err != nil {
		g.Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, f)
}

// TraceBatch reads many traces in one request. The body names the ids and any option the single read takes
// (include, pad, metric, omit, span_status ...), so a script states once what it wants from all of them.
func (g *Guard) TraceBatch(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBatchBody)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "the body must be a JSON object such as {\"ids\": [\"<trace id>\"], \"include\": [\"logs\", \"metrics\"]}"})
		return
	}
	q := r.URL.Query() // options given in the query string are defaults the body overrides
	for k, v := range body {
		if k != "ids" && k != "stream" && !slices.Contains(fusionapi.FuseParamNames, k) {
			g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("unknown field %q", kernel.Printable(k, 40))})
			return
		}
		if k == "fused" {
			g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
			return
		}
		if k == fusionapi.ParamPromQL { // queries hold commas, so they are not a comma-joined list
			qs, err := promqlValues(v)
			if err != nil {
				g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
				return
			}
			q[k] = qs
			continue
		}
		str, err := paramString(v)
		if err != nil {
			g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
			return
		}
		q.Set(k, str)
	}
	if q.Has("fused") {
		g.Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
		return
	}
	ids, err := fusionapi.NormalizeIDs(strings.Split(q.Get("ids"), ","))
	if err != nil {
		g.Err(w, err)
		return
	}
	opts, err := fusionapi.ParseFuseOptions(q)
	if err != nil {
		g.Err(w, err)
		return
	}
	opts.Extras = g.Extras()
	stream := false
	if v := q.Get("stream"); v != "" {
		if stream, err = fusionapi.ParseBool("stream", v, false); err != nil {
			g.Err(w, err)
			return
		}
	} else if stream, err = wantsStream(r); err != nil {
		g.Err(w, err)
		return
	}
	if !g.bulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		g.streamBulk(w, r, c, who, ids, opts, nil)
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	httpkit.WriteJSON(w, 200, traceBatchDoc{Results: nonNilItems(items), Summary: fusionapi.Summarise(items)})
}

// promqlValues turns the promql field of a batch body into the repeated name=expression values the shared parser reads:
// an object {"name": "expression"}, a list of "name=expression" strings, or one such string.
func promqlValues(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("a list holds only \"name=expression\" strings")
			}
			out[i] = s
		}
		return out, nil
	case map[string]any:
		names := make([]string, 0, len(x))
		for n := range x {
			names = append(names, n)
		}
		slices.Sort(names)
		out := make([]string, 0, len(x))
		for _, n := range names {
			e, ok := x[n].(string)
			if !ok {
				return nil, errors.New("an object maps each query name to its expression")
			}
			out = append(out, n+"="+e)
		}
		return out, nil
	case nil:
		return nil, nil
	}
	return nil, errors.New("must be an object of name to expression, or a list of \"name=expression\" strings")
}

// paramString turns a JSON value from a batch body into the string the shared parser reads: lists are comma-joined.
func paramString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		return x.String(), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return "", errors.New("a list holds only strings")
			}
			parts[i] = s
		}
		return strings.Join(parts, ","), nil
	case nil:
		return "", nil
	}
	return "", errors.New("must be a string, number, boolean or list of strings")
}

func nonNilItems(items []fusionapi.BulkItem) []fusionapi.BulkItem {
	if items == nil {
		return []fusionapi.BulkItem{}
	}
	return items
}

// bulkAllowed charges the caller's rate limit for the traces beyond the first: reading 25 is 25 reads, whatever
// the number of requests.
func (g *Guard) bulkAllowed(w http.ResponseWriter, who Caller, n int) bool {
	for i := 1; i < n; i++ {
		if !g.Allow(who) {
			w.Header().Set("Retry-After", "5")
			httpkit.WriteErr(w, http.StatusTooManyRequests, "too many requests, slow down; read fewer traces at a time")
			return false
		}
	}
	return true
}

// wantsStream says whether the caller asked for NDJSON: stream=true, or an Accept header that names it.
func wantsStream(r *http.Request) (bool, error) {
	if v := r.URL.Query().Get("stream"); v != "" {
		return fusionapi.ParseBool("stream", v, false)
	}
	return strings.Contains(r.Header.Get("Accept"), "application/x-ndjson"), nil
}

// streamBulk answers a bulk read as newline-delimited JSON: an optional first line (the search hits), then one line per
// trace as soon as it is read - in the order they finish, not the order asked - and a last line that counts them. A
// caller can start on the first trace while the rest are still being read, and a deadline costs it only the traces not
// yet done. The status is 200 once the first line is out; each line carries its own status.
func (g *Guard) streamBulk(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller, ids []string, opts fusionapi.FuseOptions, head any) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no") // a reverse proxy must pass lines on as they come
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	if head != nil {
		_ = enc.Encode(head)
		flush()
	}
	type line struct {
		Type string `json:"type"`
		fusionapi.BulkItem
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, func(it fusionapi.BulkItem) { // (called one at a time)
		_ = enc.Encode(line{Type: "result", BulkItem: it})
		flush()
	})
	_ = enc.Encode(struct {
		Type string `json:"type"`
		fusionapi.BulkSummary
	}{"summary", fusionapi.Summarise(items)})
	flush()
}

// MaxBatchBody bounds a batch request: 25 ids and some options are far smaller.
const MaxBatchBody = 64 << 10

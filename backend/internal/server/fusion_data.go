package server

import (
	"context"
	"continuum/internal/fusionhttp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"continuum/internal/fusion"
	"continuum/internal/fusionapi"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// The shared API over FUSION: one place another system reads the metrics, logs and traces FUSION saved, signal by
// signal or joined around a trace, with a credential that can read nothing else. Who may read, and how the routes are guarded,
// described and mounted, is internal/fusionhttp; what is left here are the handlers, which read with what Ikhnos knows.

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// fusionOrg is the organisation FUSION belongs to: the one the server was told, else the server's main organisation. (Not
// a.C.OrgID alone: the server's platform-wide core has none, which once made every request look like it came from another
// organisation.)
func (a *Admin) fusionOrg() string {
	if a.Fusion != nil && a.Fusion.Org != "" {
		return a.Fusion.Org
	}
	if a.C.OrgID != "" {
		return a.C.OrgID
	}
	return a.C.Accounts.MainOrg()
}

// ---- the data routes ----

func (a *Admin) fusionStatus(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	access := map[string]any{"kind": who.Kind, "name": who.Name, "signals": who.Scope.Signals,
		"namespaces": orEmpty(who.Scope.Namespaces), "clusters": orEmpty(who.Scope.Clusters), "rawQueries": who.Scope.Unrestricted()}
	if who.ExpiresAt != nil {
		access["expiresAt"] = kernel.RFC(*who.ExpiresAt)
	}
	// A token holder learns whether the stores are up, not how the cluster is set up: the full status carries workload
	// names, the namespace and Kubernetes API error text, which are for administrators. Nor does it read the cluster for
	// them: a token can poll as fast as the rate limit lets it, so it is answered from the last read (TokenStatus).
	var st fusion.FusionStatus
	if who.Kind == "token" {
		t := a.Fusion.TokenStatus(r.Context())
		st = fusion.FusionStatus{Available: t.Available, State: t.State}
	} else {
		st = a.Fusion.Status(r.Context())
	}
	httpkit.WriteJSON(w, 200, map[string]any{"fusion": st, "access": access})
}

// ---- parameters ----

func metricFilter(q url.Values) fusionapi.MetricFilter {
	cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (checkQuery)
	return fusionapi.MetricFilter{Name: q.Get("name"), NameRegex: q.Get("metric"), Service: q.Get("service"), Namespace: q.Get("namespace"),
		Pod: q.Get("pod"), Node: q.Get("node"), Cluster: q.Get("cluster"), Categories: cats}
}

// fusionApplications lists the Ikhnos applications, each with its services and which signals FUSION has for them in the range.
// An application here is what Ikhnos calls one (a group of services); the services themselves, as the telemetry names them, are
// listed by fusionServices.
func (a *Admin) fusionApplications(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	groups, err := a.fd().Groups(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	svcs, sources, warnings, err := c.Applications(r.Context(), who.Scope, tr)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"applications": fusionapi.DescribeGroups(groups, svcs), "sources": sources, "warnings": orEmpty(warnings), "from": tr.From, "to": tr.To})
}

// fusionServices lists the services that have telemetry (their service.name), each with its signals and the Ikhnos
// applications it is in. These are what the telemetry itself calls an application, which is not what Ikhnos does.
func (a *Admin) fusionServices(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	scope, err := a.fd().Scope(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	svcs, sources, warnings, err := c.Applications(r.Context(), scope, tr)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	type item struct {
		fusionapi.Application
		Applications []string `json:"applications"`
	}
	in := map[string][]string{}
	if groups, err := a.fd().Groups(r, who); err == nil { // best effort: the list is useful without it
		for _, g := range groups {
			for _, n := range g.ServiceNames() {
				in[n] = append(in[n], g.Name)
			}
		}
	}
	out := make([]item, 0, len(svcs))
	for _, sv := range svcs {
		names := in[sv.Name]
		if names == nil {
			names = []string{}
		}
		out = append(out, item{sv, names})
	}
	httpkit.WriteJSON(w, 200, map[string]any{"services": out, "sources": sources, "warnings": orEmpty(warnings), "from": tr.From, "to": tr.To})
}

// fusionApplication is one Ikhnos application at a glance, named by its id or its name.
func (a *Admin) fusionApplication(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	groups, err := a.fd().Groups(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	g, err := fusionapi.FindGroup(groups, r.PathValue("name"))
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	o, err := c.ApplicationOverview(r.Context(), who.Scope, g, tr)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, o)
}

// topologyAter is what the extras add when the server remembers the past (fusionExtras.TopologyAt).
type topologyAter interface {
	TopologyAt(ctx context.Context, appID string, at time.Time) (*fusionapi.TopologyView, []fusionapi.AppMember, error)
}

// fusionAppTopology is one application's services and the traffic between them and the rest of the estate, now or (at=) as of a
// past moment, with what changed around them in the window before it.
func (a *Admin) fusionAppTopology(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	q := r.URL.Query()
	window, err := fusionapi.DurationParam(q.Get("window"))
	if err == nil && (window < 0 || window > fusionapi.MaxTopologyWindow) {
		err = &fusionapi.Error{Status: 400, Msg: "window can be at most 7d"}
	}
	if window == 0 {
		window = fusionapi.DefaultTopologyWindow
	}
	var noise bool
	if err == nil {
		noise, err = fusionapi.ParseBool("noise", q.Get("noise"), false)
	}
	var at time.Time
	if err == nil && q.Has("at") {
		if at, err = time.Parse(time.RFC3339, q.Get("at")); err != nil {
			err = &fusionapi.Error{Status: 400, Msg: "at must be an RFC 3339 time"}
		}
	}
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	groups, err := a.fd().Groups(r, who)
	var g *fusionapi.AppGroup
	if err == nil {
		g, err = fusionapi.FindGroup(groups, r.PathValue("name"))
	}
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	ex, until := a.fusionExtras(), a.C.Now()
	var v *fusionapi.TopologyView
	if !at.IsZero() {
		h, ok := ex.(topologyAter)
		if !ok {
			a.fd().Err(w, &fusionapi.Error{Status: 404, Msg: "history is not enabled on this server: it needs the graph database"})
			return
		}
		var members []fusionapi.AppMember
		if v, members, err = h.TopologyAt(r.Context(), g.ID, at); err == nil {
			then := *g
			then.Members, then.Unresolved = members, 0
			if vis := fusionapi.VisibleGroups([]fusionapi.AppGroup{then}, who.Scope); len(vis) > 0 { // as the caller may see it, then
				g, until = &vis[0], at
			} else {
				err = &fusionapi.Error{Status: 404, Msg: "the application had no services you may see at that time"}
			}
		}
	} else {
		v, err = ex.Topology(r.Context())
	}
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	t := fusionapi.BuildAppTopology(g, v, who.Scope, noise)
	if !at.IsZero() {
		snap := v.At
		t.Source, t.AsOf, t.SnapshotAt = "history", at, &snap
	}
	evs, err := ex.Changes(r.Context(), until.Add(-window), until, nil, t.Targets(), fusionapi.MaxAppChanges+1)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	t.AddChanges(evs)
	httpkit.WriteJSON(w, 200, t)
}

// fusionService is one service at a glance, by the service.name its telemetry carries.
func (a *Admin) fusionService(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	o, err := c.ServiceOverview(r.Context(), who.Scope, r.PathValue("name"), tr)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, o)
}

func (a *Admin) fusionMetricNames(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 500, 5000)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	scope, err := a.fd().Scope(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	names, truncated, err := c.MetricNames(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	cat := make(map[string]string, len(names))
	for _, n := range names {
		cat[n] = fusionapi.MetricCategory(n)
	}
	httpkit.WriteJSON(w, 200, map[string]any{"names": orEmpty(names), "categories": cat, "truncated": truncated, "limit": limit, "from": tr.From, "to": tr.To})
}

func (a *Admin) fusionMetricSeries(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 200, 2000)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	scope, err := a.fd().Scope(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	series, truncated, err := c.Series(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	if series == nil {
		series = []map[string]string{}
	}
	httpkit.WriteJSON(w, 200, map[string]any{"series": series, "truncated": truncated, "limit": limit, "from": tr.From, "to": tr.To})
}

func (a *Admin) fusionMetricRange(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	q := r.URL.Query()
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	req, err := fusionapi.DurationParam(q.Get("step"))
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	step, err := fusionapi.ChooseStep(tr, req, 120)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	scope, err := a.fd().Scope(r, who)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	f := metricFilter(q)
	if err := f.NeedSelective(scope); err != nil {
		a.fd().Err(w, err)
		return
	}
	series, truncated, err := c.MetricRange(r.Context(), scope, f, tr, step, limit)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	if series == nil {
		series = []fusionapi.MetricSeries{}
	}
	httpkit.WriteJSON(w, 200, map[string]any{"series": series, "truncated": truncated, "limit": limit, "stepSeconds": step.Seconds(), "from": tr.From, "to": tr.To})
}

// fusionMetricRaw is PromQL as written, answered the way Prometheus itself answers.
func (a *Admin) fusionMetricRaw(endpoint string) fusionhttp.Handler {
	return func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
		data, err := c.RawMetricQuery(r.Context(), who.Scope, endpoint, r.URL.Query())
		if err != nil {
			a.fd().Err(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":`))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte(`}`))
	}
}

func (a *Admin) fusionLogs(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	q := r.URL.Query()
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 200, 2000)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	var order string
	switch order = q.Get("order"); order {
	case "", "newest", "oldest":
	default:
		a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "order must be newest or oldest"})
		return
	}
	backward := order != "oldest"
	var entries []fusionapi.LogEntry
	var truncated bool
	if raw := q.Get("query"); raw != "" {
		entries, truncated, err = c.RawLogQuery(r.Context(), who.Scope, raw, backward, tr, limit)
	} else if scope, serr := a.fd().Scope(r, who); serr != nil {
		err = serr
	} else {
		cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (checkQuery)
		entries, truncated, err = c.Logs(r.Context(), scope, fusionapi.LogFilter{
			Service: q.Get("service"), Namespace: q.Get("namespace"), Pod: q.Get("pod"), Cluster: q.Get("cluster"), Categories: cats,
			TraceID: q.Get("trace_id"), SpanID: q.Get("span_id"), Severity: q.Get("severity"), Contains: q.Get("contains"), Backward: backward,
		}, tr, limit)
	}
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	if entries == nil {
		entries = []fusionapi.LogEntry{}
	}
	httpkit.WriteJSON(w, 200, map[string]any{"entries": entries, "truncated": truncated, "limit": limit, "from": tr.From, "to": tr.To})
}

func (a *Admin) fusionTraces(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	q := r.URL.Query()
	tr, err := fusionhttp.Range(r, a.C.Now())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	opts, fused, err := fusionapi.ParseFuseParams(q)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	opts.Extras = a.fusionExtras()
	stream, err := wantsStream(r)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	def, max := 20, 100
	if fused { // each hit is read in full, so a fused search is a smaller page
		def, max = 10, fusionapi.MaxBatch
	}
	limit, err := fusionapi.Limit(q.Get("limit"), def, max)
	if err != nil {
		a.fd().Err(w, err)
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
			scope, err = a.fd().Scope(r, who)
		}
		if err == nil {
			cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (checkQuery)
			traces, truncated, err = c.SearchTraces(r.Context(), scope, fusionapi.TraceFilter{
				Service: q.Get("service"), Namespace: q.Get("namespace"), Cluster: q.Get("cluster"), Name: q.Get("name"), Status: q.Get("status"), Categories: cats,
				MinDuration: minD, MaxDuration: maxD,
			}, tr, limit)
		}
	}
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	if traces == nil {
		traces = []fusionapi.TraceSummary{}
	}
	page := map[string]any{"traces": traces, "truncated": truncated, "limit": limit, "from": tr.From, "to": tr.To}
	if !fused {
		httpkit.WriteJSON(w, 200, page)
		return
	}
	ids := make([]string, len(traces))
	for i, t := range traces {
		ids[i] = t.TraceID
	}
	if len(ids) > 0 && !a.fusionBulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		page["type"] = "traces"
		a.streamBulk(w, r, c, who, ids, opts, page)
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	page["results"], page["summary"] = nonNilItems(items), fusionapi.Summarise(items)
	httpkit.WriteJSON(w, 200, page)
}

// fusionTrace is one trace; with fused=true (or an include list) it is the fused object: each span carrying its log
// lines and each resource its metric series, and whatever else the options ask for.
func (a *Admin) fusionTrace(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	id := r.PathValue("id")
	opts, fused, err := fusionapi.ParseFuseParams(r.URL.Query())
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	opts.Extras = a.fusionExtras()
	if !fused {
		tr, err := c.Trace(r.Context(), who.Scope, id)
		if err != nil {
			a.fd().Err(w, err)
			return
		}
		httpkit.WriteJSON(w, 200, tr)
		return
	}
	f, err := c.FuseTrace(r.Context(), who.Scope, id, opts)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, f)
}

// maxBatchBody bounds a batch request: 25 ids and some options are far smaller.
const maxBatchBody = 64 << 10

// fusionTraceBatch reads many traces in one request. The body names the ids and any option the single read takes
// (include, pad, metric, omit, span_status ...), so a script states once what it wants from all of them.
func (a *Admin) fusionTraceBatch(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBody)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "the body must be a JSON object such as {\"ids\": [\"<trace id>\"], \"include\": [\"logs\", \"metrics\"]}"})
		return
	}
	q := r.URL.Query() // options given in the query string are defaults the body overrides
	for k, v := range body {
		if k != "ids" && k != "stream" && !slices.Contains(fusionapi.FuseParamNames, k) {
			a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("unknown field %q", kernel.Printable(k, 40))})
			return
		}
		if k == "fused" {
			a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
			return
		}
		if k == fusionapi.ParamPromQL { // queries hold commas, so they are not a comma-joined list
			qs, err := promqlValues(v)
			if err != nil {
				a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
				return
			}
			q[k] = qs
			continue
		}
		str, err := paramString(v)
		if err != nil {
			a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s: %v", k, err)})
			return
		}
		q.Set(k, str)
	}
	if q.Has("fused") {
		a.fd().Err(w, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "a batch is always fused; use include=none for the traces alone"})
		return
	}
	ids, err := fusionapi.NormalizeIDs(strings.Split(q.Get("ids"), ","))
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	opts, err := fusionapi.ParseFuseOptions(q)
	if err != nil {
		a.fd().Err(w, err)
		return
	}
	opts.Extras = a.fusionExtras()
	stream := false
	if v := q.Get("stream"); v != "" {
		if stream, err = fusionapi.ParseBool("stream", v, false); err != nil {
			a.fd().Err(w, err)
			return
		}
	} else if stream, err = wantsStream(r); err != nil {
		a.fd().Err(w, err)
		return
	}
	if !a.fusionBulkAllowed(w, who, len(ids)) {
		return
	}
	if stream {
		a.streamBulk(w, r, c, who, ids, opts, nil)
		return
	}
	items := c.FuseMany(r.Context(), who.Scope, ids, opts, nil)
	httpkit.WriteJSON(w, 200, map[string]any{"results": nonNilItems(items), "summary": fusionapi.Summarise(items)})
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

// fusionBulkAllowed charges the caller's rate limit for the traces beyond the first: reading 25 is 25 reads, whatever
// the number of requests.
func (a *Admin) fusionBulkAllowed(w http.ResponseWriter, who fusionhttp.Caller, n int) bool {
	for i := 1; i < n; i++ {
		if !a.fd().Allow(who) {
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
func (a *Admin) streamBulk(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionhttp.Caller, ids []string, opts fusionapi.FuseOptions, head any) {
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

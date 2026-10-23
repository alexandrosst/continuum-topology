// Package fusionhttp is what the routes of the shared data API over FUSION have in common, and none of them owns: the table that
// describes every route once (Op), the guard that authenticates, throttles and bounds each request (Guard), the OpenAPI
// description and page built from that table, and the helpers the handlers read a request with (the time range, the scope).
//
// It does not read metrics, logs or traces, and it does not know what Ikhnos is: the handlers, and the facts about applications and
// links they need, are supplied by the server, which owns them.
package fusionhttp

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"ikhnos/internal/fusionapi"
	"ikhnos/internal/kernel"
)

// The data API, described once. Every route, and every parameter it takes, is an entry here; the router is built from
// this table (Mount) and so is the OpenAPI description served beside the API (openapi.go), which
// the interactive page reads. A route that is not in the table does not exist, and one that is in it is documented.

const APIPath = "/api/v1/fusion"

// Op is one route.
type Op struct {
	Method, Path string // Path is relative to APIPath
	Tag          string
	Summary      string
	Description  string
	PathParams   []string          // names in PathParamDefs
	Params       []string          // query parameter names in ParamDefs, in the order they are shown
	Notes        map[string]string // what a parameter means on this route, where that is more than its shared description
	Body         string            // name of the request body schema, for POST
	BodyExample  string            // a JSON example of it
	Response     string            // name of the 200 response schema
	Stream       bool              // can also answer application/x-ndjson
	AlsoPost     bool              // a POST with a form is served too (the backend's own way of sending a long query)
	// Open routes forward whatever they are sent to the backend, so they cannot say which parameters they take; every other route
	// refuses a parameter it does not list, so a typo is told and not silently dropped.
	Open bool
	// Excludes says which parameters a given one overrides: sent together, the override would make the others do nothing, which
	// is refused. `query` is sent to Loki as written, so the filters that would have built a query are not used.
	Excludes map[string][]string
	Handler  Handler
}

// Conflicts is every parameter a route cannot take together with name, in either direction.
func (op Op) Conflicts(name string) []string {
	set := map[string]bool{}
	for _, e := range op.Excludes[name] {
		set[e] = true
	}
	for k, es := range op.Excludes {
		for _, e := range es {
			if e == name {
				set[k] = true
			}
		}
	}
	var out []string
	for _, p := range op.Params { // in the order the route lists them
		if set[p] {
			out = append(out, p)
		}
	}
	return out
}

// CheckQuery refuses a query string the route does not take: a parameter it does not list, an empty value where one is
// given on purpose, or two parameters of which one would make the other do nothing.
func (op Op) CheckQuery(q url.Values) error {
	if op.Open {
		return nil
	}
	allowed := map[string]bool{}
	for _, p := range op.Params {
		allowed[p] = true
	}
	if op.Body != "" { // a batch takes its options in the body, and as defaults in the query string
		for _, p := range fusionapi.FuseParamNames {
			allowed[p] = true
		}
	}
	for k := range q {
		if !allowed[k] {
			return &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("this route does not take %q; it takes %s", kernel.Printable(k, 40), takes(op))}
		}
	}
	if v := q.Get("category"); v != "" {
		if _, err := fusionapi.ParseCategories(v); err != nil {
			return err
		}
	}
	for k := range q {
		for _, e := range op.Conflicts(k) {
			if q.Has(e) {
				return &fusionapi.Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf("%s and %s cannot be used together: %s", k, e, op.whyExcluded(k, e))}
			}
		}
	}
	return nil
}

// fusedOptionNeedsFused is whether a parameter shapes a fused read on a route that reads one only when asked to.
func fusedOptionNeedsFused(op Op, name string) bool {
	if name == fusionapi.ParamFused || name == fusionapi.ParamInclude || name == fusionapi.ParamPromQL {
		return false
	}
	has := false
	for _, p := range op.Params {
		has = has || p == fusionapi.ParamFused
	}
	return has && slices.Contains(fusionapi.FuseParamNames, name)
}

func (op Op) whyExcluded(a, b string) string {
	for _, raw := range []string{"query", "q"} {
		if a == raw || b == raw {
			return "`" + raw + "` is sent to the store as written, so the filters would not be used"
		}
	}
	return "one would make the other do nothing"
}

func takes(op Op) string {
	if len(op.Params) == 0 {
		return "no parameters"
	}
	return strings.Join(op.Params, ", ")
}

// Param is one parameter, shared by every route that takes it.
type Param struct {
	Type    string   // string | integer | boolean
	Enum    []string // the values it takes
	List    bool     // a comma-separated list of Enum values (or of free values when Enum is empty)
	Default string
	Example string
	Desc    string
	// Multi is a parameter repeated to give several values (Prometheus' match[]); the page takes one per line.
	Multi bool
}

var PathParamDefs = map[string]Param{
	"id":   {Type: "string", Example: "0af7651916cd43dd8448eb211c80319c", Desc: "The trace id: 32 hex characters (shorter ones are zero-padded)."},
	"name": {Type: "string", Example: "checkout", Desc: "The application (its id or name) on `/applications/{name}` and `/applications/{name}/topology`; the service (its `service.name`) on `/services/{name}`."},

	"label": {Type: "string", Example: "service_name", Desc: "The label name."},
	"tag":   {Type: "string", Example: "resource.service.name", Desc: "The tag name, with its scope: `resource.service.name`, `span.http.method`, or an intrinsic such as `name`."},
}

// The time range, the filters of each signal, and the fused read's options. A parameter is described here once, wherever
// it is used.
var ParamDefs = map[string]Param{
	// time
	"from": {Type: "string", Default: "to − 1h", Example: "now-15m", Desc: "Start of the range: an RFC 3339 time, unix seconds, or `now-<duration>` such as `now-15m` or `now-7d` (durations take s, m, h, d and w)."},
	"to":   {Type: "string", Default: "now", Example: "now", Desc: "End of the range, in the same forms as `from`. The range can be at most 31 days, but a trace search reads at most 7 days and a log read at most 30 days at a time."},

	// the topology of an application
	"at":     {Type: "string", Example: "2026-10-05T11:30:00Z", Desc: "An RFC 3339 time: answer as the estate was then instead of now: the services, links and traffic of the newest recording at or before it (`snapshotAt`), and the services the application had at that moment. Needs the graph database."},
	"window": {Type: "string", Default: "24h", Example: "6h", Desc: "How far before now (or before `at`) to look for what changed, at most 7d."},
	"noise":  {Type: "boolean", Default: "false", Desc: "Include the machinery links (DNS, system) that are left out by default."},

	// shared filters
	"limit":       {Type: "integer", Example: "50", Desc: "The most results to return. Larger values are cut to the route's maximum, which each route states."},
	"service":     {Type: "string", Example: "checkout", Desc: "Only this service (the `service.name` of the telemetry)."},
	"namespace":   {Type: "string", Example: "shop", Desc: "Only this Kubernetes namespace."},
	"pod":         {Type: "string", Desc: "Only this pod."},
	"node":        {Type: "string", Desc: "Only this node."},
	"cluster":     {Type: "string", Example: "cl-1", Desc: "Only this cluster (its Ikhnos cluster id)."},
	"peer":        {Type: "string", Example: "cl-2", Desc: "Only the link to this cluster (its Ikhnos cluster id); needs `cluster`."},
	"category":    {Type: "string", Example: "application", Desc: "Only this kind of telemetry (or a comma-separated list of kinds): `system` (the machine and the telemetry pipeline: host CPU, memory and disk, energy, scrape health, the collectors' own metrics, the `ikhnos_link_*` measurements between clusters; and logs written outside any namespace), `kubernetes` (the cluster's own objects: the `k8s_*`, `container_*` and `kube_*` metrics, and the logs and spans of `kube-system`, `kube-public` and `kube-node-lease`) or `application` (everything else: what an application reports about itself, and the logs and spans of every other namespace). A metric is placed by its name, a log line or a span by its namespace; the category is what the signal is about, not who owns it, so a pod's CPU is `kubernetes` whichever application the pod is in (use `application` for whose). Traces have no `system` category, and a span with no namespace attribute is in neither of the others. Every metric series and log line also says its own `category`. All three kinds, or none, is no filter."},
	"application": {Type: "string", Example: "Shop", Desc: "Only this Ikhnos application: the id or the name (any case) of one listed by `/applications`. It is resolved when you ask, from the services Ikhnos groups into it, to their service names within their namespaces and clusters, so a change to the application shows at once and the stored telemetry carries no application label. It narrows alongside the other filters and your own access, and cannot be combined with a query you write yourself (`query`, `q`). When two members of the application share a service name in different namespaces, a same-named service of the application's other namespaces may show too: telemetry says which service it is from, not which application."},

	// metrics
	"name":         {Type: "string", Example: "http_server_duration_seconds_count", Desc: "The exact metric name."},
	"metric":       {Type: "string", Example: "http_.*|cpu_.*", Desc: "A regular expression for metric names, anchored at both ends, so write `http_.*` for a prefix. In a fused read it picks which metric names are carried (default all)."},
	"step":         {Type: "string", Default: "chosen so the range gives about 120 points", Example: "30s", Desc: "Seconds between points: a duration (`30s`, `2m`) or plain seconds."},
	"query":        {Type: "string", Example: "sum(rate(http_requests_total[5m]))", Desc: "The query as the store reads it (PromQL or LogQL). Only a caller whose access is not limited to certain namespaces or clusters may send one."},
	"time":         {Type: "string", Desc: "The instant to evaluate at (RFC 3339 or unix seconds). Default now."},
	"start":        {Type: "string", Desc: "Start of the range, RFC 3339 or unix seconds."},
	"end":          {Type: "string", Desc: "End of the range, RFC 3339 or unix seconds."},
	"trace_id":     {Type: "string", Desc: "Only lines written under this trace."},
	"span_id":      {Type: "string", Desc: "Only lines written under this span (16 hex characters)."},
	"severity":     {Type: "string", Example: "error,warn", Desc: "Only lines of these levels (`error`, `warn`, `info`, ... ignoring case); a comma-separated list is allowed."},
	"contains":     {Type: "string", Desc: "Only lines that contain this text."},
	"order":        {Type: "string", Enum: []string{"newest", "oldest"}, Default: "newest", Desc: "Which end of the range the `limit` keeps."},
	"q":            {Type: "string", Example: `{ resource.service.name = "checkout" && status = error }`, Desc: "A TraceQL query as written. Only a caller whose access is not limited may send one; the filters below are ignored when it is given."},
	"status":       {Type: "string", Enum: []string{"error", "ok", "unset"}, Desc: "Only traces with a span of this status."},
	"min_duration": {Type: "string", Example: "500ms", Desc: "Only traces at least this long: a duration (`500ms`, `2s`) or plain seconds."},
	"max_duration": {Type: "string", Example: "10s", Desc: "Only traces at most this long."},

	// the fused read
	fusionapi.ParamFused:            {Type: "boolean", Default: "false", Example: "true", Desc: "Return the fused object: each trace with the logs and metrics saved around it, joined to its spans. Without `include`, it carries `logs` and `metrics`."},
	fusionapi.ParamInclude:          {Type: "string", List: true, Enum: []string{"logs", "context_logs", "system_logs", "metrics", "topology", "changes", "all", "none"}, Example: "logs,metrics", Desc: "What to join to the trace. `logs`: the lines that carry the trace id, placed on their span (exact). `context_logs`: lines of the same service and pod in the same window that carry no trace id, on the resource. `system_logs`: lines of the system namespaces on the nodes the trace ran on. `metrics`: the series of the same service, pod (and node) over the trace's time, on each resource and each span. `topology`: what Ikhnos knows about each service: which services and external addresses it was seen calling and which were seen calling it, as of now. `changes`: the events Ikhnos recorded about the trace's services, nodes and clusters shortly before and after the trace (scalings, restarts, node and cluster events). `all`: every one of these. `none`: the trace alone, shaped by the other options. Giving `include` or `promql` implies `fused=true`; with `promql` alone only your queries are added."},
	fusionapi.ParamPad:              {Type: "string", Default: "2m", Example: "5m", Desc: "How far before the trace starts and after it ends logs and metrics are looked for (at most 1h)."},
	fusionapi.ParamSpanPad:          {Type: "string", Default: "30s", Example: "1m", Desc: "How far either side of a span its own metric points are taken from. Samples arrive every 30 to 60 seconds, so a span of a few milliseconds needs some room."},
	fusionapi.ParamMetricScope:      {Type: "string", List: true, Enum: []string{"app", "pod", "node"}, Default: "app,pod", Desc: "Whose series each resource carries: `app` what the service itself reported, `pod` what was reported about its pod, `node` what was reported about the node itself (series that name the node and no pod)."},
	fusionapi.ParamMaxLogs:          {Type: "integer", Default: "500", Desc: "The most trace-linked log lines (at most 2000)."},
	fusionapi.ParamMaxContextLogs:   {Type: "integer", Default: "50", Desc: "The most `context_logs` per resource (at most 500)."},
	fusionapi.ParamMaxSystemLogs:    {Type: "integer", Default: "100", Desc: "The most `system_logs` lines in all (at most 1000)."},
	fusionapi.ParamMaxSeries:        {Type: "integer", Default: "15", Desc: "The most metric series per resource (at most 100)."},
	fusionapi.ParamPoints:           {Type: "integer", Default: "60", Desc: "About how many points each metric series has over the trace (at most 500)."},
	fusionapi.ParamSystemNamespaces: {Type: "string", List: true, Default: "kube-system", Example: "kube-system,kube-public", Desc: "Which namespaces count as system for `system_logs`."},
	fusionapi.ParamLogSeverity:      {Type: "string", Example: "error,warn", Desc: "Narrow every log read of the fused object to these levels."},
	fusionapi.ParamLogContains:      {Type: "string", Desc: "Narrow every log read of the fused object to lines containing this text."},
	fusionapi.ParamPromQL:           {Type: "string", Multi: true, Example: `errors=sum(rate(http_server_errors_total{service_name="${service}",k8s_namespace_name="${namespace}"}[1m]))`, Desc: "Your own PromQL, as `<name>=<expression>`; repeat it (one per line here) for up to 5 queries. A query that names `${service}`, `${namespace}`, `${pod}`, `${node}` or `${cluster}` is evaluated for every resource of the trace with its values filled in, and its series sit on that resource (and, with `promql_spans`, on each span); one that names none is evaluated once, over the trace's time, and sits in `queries`. `${trace_id}` and `${range}` (the window's length, as a duration such as `154s`) are available to both, `${service:re}` gives the value escaped for a regular expression, and `$${` writes a literal `${`. A resource that lacks a value (no pod) is skipped and the answer says so. Only a caller whose access is not limited to certain namespaces or clusters may send queries. In a batch body: an object of name to expression."},
	fusionapi.ParamPromQLSpans:      {Type: "boolean", Default: "false", Desc: "Also cut the series of per-resource `promql` queries to each span's own time (plus `span_pad`), the way metrics are."},
	fusionapi.ParamPromQLStep:       {Type: "string", Default: "chosen from points", Example: "15s", Desc: "Seconds between the points of your `promql` queries: a duration or plain seconds, at least one second."},
	fusionapi.ParamPromQLPad:        {Type: "string", Default: "pad", Example: "10m", Desc: "How far around the trace your `promql` queries look, when that should differ from `pad` (at most 1h). A rate over 5m needs a window longer than a short trace."},
	fusionapi.ParamPromQLServices:   {Type: "string", List: true, Example: "checkout,payments", Desc: "Evaluate per-resource `promql` queries only for the resources of these services."},
	fusionapi.ParamChangesBefore:    {Type: "string", Default: "30m", Example: "2h", Desc: "How far before the trace `include=changes` looks for events (at most 24h). It looks `pad` after the trace."},
	fusionapi.ParamMaxChanges:       {Type: "integer", Default: "50", Desc: "The most `changes` events (at most 500); the newest are kept."},
	fusionapi.ParamOmit:             {Type: "string", List: true, Enum: []string{"attributes", "events"}, Example: "attributes,events", Desc: "Leave the span and resource attributes, or the span events, out. Large traces are mostly these."},
	fusionapi.ParamSpanService:      {Type: "string", Desc: "Return only the spans of this service. The trace's totals (`spanCount`, `errorCount`) stay those of the whole trace and `spansOmitted` says how many were left out."},
	fusionapi.ParamSpanStatus:       {Type: "string", Enum: []string{"error", "ok", "unset"}, Desc: "Return only the spans with this status."},
	fusionapi.ParamSpanMinDuration:  {Type: "string", Example: "100ms", Desc: "Return only the spans at least this long."},

	// the backends' own parameters, for the native mirror (what each means is in that backend's HTTP API documentation)
	"match[]":     {Type: "string", Multi: true, Example: `{service_name="checkout"}`, Desc: "A series selector; repeat it (one per line here) for several. Only series that match any of them are considered."},
	"timeout":     {Type: "string", Example: "20s", Desc: "Evaluation timeout, as the backend reads it."},
	"direction":   {Type: "string", Enum: []string{"forward", "backward"}, Default: "backward", Desc: "Which end of the range the `limit` keeps."},
	"interval":    {Type: "string", Desc: "Return entries at this interval at most, as Loki reads it (a duration or seconds)."},
	"since":       {Type: "string", Example: "1h", Desc: "How far back to look from now, as a duration: an alternative to `start`."},
	"tags":        {Type: "string", Example: "service.name=checkout http.status_code=500", Desc: "Tempo's logfmt tag search: `key=value` pairs, space-separated."},
	"minDuration": {Type: "string", Example: "100ms", Desc: "Only traces at least this long."},
	"maxDuration": {Type: "string", Example: "5s", Desc: "Only traces at most this long."},
	"spss":        {Type: "integer", Desc: "Spans to return per matching trace (Tempo)."},
	"scope":       {Type: "string", Enum: []string{"span", "resource", "intrinsic"}, Desc: "Only the tags of this scope."},
	"exemplars":   {Type: "integer", Desc: "How many exemplars a TraceQL metrics query returns."},

	// bulk
	"stream": {Type: "boolean", Default: "false", Desc: "Answer as newline-delimited JSON (`application/x-ndjson`), one line per trace as soon as it is read, instead of one JSON document at the end. Sending `Accept: application/x-ndjson` does the same."},
	"ids":    {Type: "string", List: true, Desc: "In the body: the trace ids, a list of at most 25."},
}

// fusionFilterParams are the filters every metric read takes.
var MetricFilter = []string{"name", "metric", "service", "namespace", "pod", "node", "cluster", "application", "category"}

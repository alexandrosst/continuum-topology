package server

import "continuum/internal/fusionapi"

// The data API, described once. Every route, and every parameter it takes, is an entry here; the router is built from
// this table (registerFusionData) and so is the OpenAPI description served beside the API (fusion_openapi.go), which
// the interactive page reads. A route that is not in the table does not exist, and one that is in it is documented.

const fusionAPIPath = "/api/v1/fusion"

// fusionOp is one route.
type fusionOp struct {
	Method, Path string // Path is relative to fusionAPIPath
	Tag          string
	Summary      string
	Description  string
	PathParams   []string          // names in fusionPathParams
	Params       []string          // query parameter names in fusionParams, in the order they are shown
	Notes        map[string]string // what a parameter means on this route, where that is more than its shared description
	Body         string            // name of the request body schema, for POST
	BodyExample  string            // a JSON example of it
	Response     string            // name of the 200 response schema
	Stream       bool              // can also answer application/x-ndjson
	Handler      fusionHandler
}

// fusionParam is one parameter, shared by every route that takes it.
type fusionParam struct {
	Type    string   // string | integer | boolean
	Enum    []string // the values it takes
	List    bool     // a comma-separated list of Enum values (or of free values when Enum is empty)
	Default string
	Example string
	Desc    string
}

var fusionPathParams = map[string]fusionParam{
	"id":   {Type: "string", Example: "0af7651916cd43dd8448eb211c80319c", Desc: "The trace id: 32 hex characters (shorter ones are zero-padded)."},
	"name": {Type: "string", Example: "checkout", Desc: "The application (service) name."},
}

// The time range, the filters of each signal, and the fused read's options. A parameter is described here once, wherever
// it is used.
var fusionParams = map[string]fusionParam{
	// time
	"from": {Type: "string", Default: "to − 1h", Example: "now-15m", Desc: "Start of the range: an RFC 3339 time, unix seconds, or `now-<duration>` such as `now-15m`."},
	"to":   {Type: "string", Default: "now", Example: "now", Desc: "End of the range, in the same forms as `from`. The range can be at most 31 days."},

	// shared filters
	"limit":     {Type: "integer", Example: "50", Desc: "The most results to return. Larger values are cut to the route's maximum, which each route states."},
	"service":   {Type: "string", Example: "checkout", Desc: "Only this service (the `service.name` of the telemetry)."},
	"namespace": {Type: "string", Example: "shop", Desc: "Only this Kubernetes namespace."},
	"pod":       {Type: "string", Desc: "Only this pod."},
	"node":      {Type: "string", Desc: "Only this node."},
	"cluster":   {Type: "string", Example: "cl-1", Desc: "Only this cluster (its Ikhnos cluster id)."},

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
	fusionapi.ParamInclude:          {Type: "string", List: true, Enum: []string{"logs", "context_logs", "system_logs", "metrics", "all", "none"}, Example: "logs,metrics", Desc: "What to join to the trace. `logs`: the lines that carry the trace id, placed on their span (exact). `context_logs`: lines of the same service and pod in the same window that carry no trace id, on the resource. `system_logs`: lines of the system namespaces on the nodes the trace ran on. `metrics`: the series of the same service, pod (and node) over the trace's time, on each resource and each span. `none`: the trace alone, shaped by the other options. Giving `include` implies `fused=true`."},
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
	fusionapi.ParamOmit:             {Type: "string", List: true, Enum: []string{"attributes", "events"}, Example: "attributes,events", Desc: "Leave the span and resource attributes, or the span events, out. Large traces are mostly these."},
	fusionapi.ParamSpanService:      {Type: "string", Desc: "Return only the spans of this service. The trace's totals (`spanCount`, `errorCount`) stay those of the whole trace and `spansOmitted` says how many were left out."},
	fusionapi.ParamSpanStatus:       {Type: "string", Enum: []string{"error", "ok", "unset"}, Desc: "Return only the spans with this status."},
	fusionapi.ParamSpanMinDuration:  {Type: "string", Example: "100ms", Desc: "Return only the spans at least this long."},

	// bulk
	"stream": {Type: "boolean", Default: "false", Desc: "Answer as newline-delimited JSON (`application/x-ndjson`), one line per trace as soon as it is read, instead of one JSON document at the end. Sending `Accept: application/x-ndjson` does the same."},
	"ids":    {Type: "string", List: true, Desc: "In the body: the trace ids, a list of at most 25."},
}

// fusionFilterParams are the filters every metric read takes.
var fusionMetricFilter = []string{"name", "metric", "service", "namespace", "pod", "node", "cluster"}

func fusionOps(a *Admin) []fusionOp {
	rng := []string{"from", "to"}
	join := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	fused := fusionapi.FuseParamNames
	return []fusionOp{
		{Method: "GET", Path: "/status", Tag: "Service", Summary: "Is FUSION up, and what may I read?",
			Description: "Whether the stores are running and what the credential you present may read: its signals, namespaces and clusters, and when it expires.",
			Response:    "Status", Handler: a.fusionStatus},
		{Method: "GET", Path: "/applications", Tag: "Applications", Summary: "List the applications that have telemetry",
			Description: "Every service FUSION has data for in the range, with which signals (metrics, logs, traces) it has.",
			Params:      rng, Response: "ApplicationList", Handler: a.fusionApplications},
		{Method: "GET", Path: "/applications/{name}", Tag: "Applications", Summary: "One application at a glance",
			Description: "The signals the application has, its most recent traces and failing traces, its latest error logs and the metric names it reports: the places to go on from.",
			PathParams:  []string{"name"}, Params: rng, Response: "Overview", Handler: a.fusionApplication},

		{Method: "GET", Path: "/metrics/names", Tag: "Metrics", Summary: "List metric names",
			Params: join(rng, fusionMetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most names (default 500, at most 5000)."},
			Response: "NameList", Handler: a.fusionMetricNames},
		{Method: "GET", Path: "/metrics/series", Tag: "Metrics", Summary: "List the series (label sets) that match",
			Params: join(rng, fusionMetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most series (default 200, at most 2000)."},
			Response: "SeriesList", Handler: a.fusionMetricSeries},
		{Method: "GET", Path: "/metrics/range", Tag: "Metrics", Summary: "Read metric series over a range",
			Description: "The samples of the series that match the filters, thinned to a sensible number of points, with each series' min, max, average and last value.",
			Params:      join(rng, fusionMetricFilter, []string{"step", "limit"}), Response: "MetricRange", Handler: a.fusionMetricRange},
		{Method: "GET", Path: "/metrics/query", Tag: "Metrics", Summary: "Run a PromQL instant query",
			Description: "PromQL as written, answered the way Prometheus answers. Only for a caller whose access is not limited to certain namespaces or clusters.",
			Params:      []string{"query", "time"}, Response: "PromResult", Handler: a.fusionMetricRaw("query")},
		{Method: "GET", Path: "/metrics/query_range", Tag: "Metrics", Summary: "Run a PromQL range query",
			Description: "PromQL over a range, answered the way Prometheus answers. Only for a caller whose access is not limited. `start`, `end` and `step` are all required.",
			Params:      []string{"query", "start", "end", "step"}, Response: "PromResult", Handler: a.fusionMetricRaw("query_range")},

		{Method: "GET", Path: "/logs", Tag: "Logs", Summary: "Search log lines",
			Description: "Lines matching every filter given, in the range. A line carries the trace and span id it was written under, so `trace_id` finds everything a request logged.",
			Params:      join(rng, []string{"service", "namespace", "pod", "cluster", "trace_id", "span_id", "severity", "contains", "order", "limit", "query"}),
			Response:    "LogResult", Handler: a.fusionLogs},

		{Method: "GET", Path: "/traces", Tag: "Traces", Summary: "Search traces — optionally fused",
			Description: "Traces matching the filters, newest first. With `fused=true` each hit is also read in full and joined to its logs and metrics (the options below apply to every hit); that returns up to 25 traces, read in parallel, and with `stream=true` they arrive one by one as they are ready.",
			Params:      join(rng, []string{"service", "namespace", "cluster", "name", "status", "min_duration", "max_duration", "limit", "q"}, fused, []string{"stream"}),
			Response:    "TraceList", Stream: true, Handler: a.fusionTraces},
		{Method: "GET", Path: "/traces/{id}", Tag: "Traces", Summary: "One trace — optionally fused",
			Description: "The trace as Tempo has it. With `fused=true` (or an `include` list) it is the fused object: every span carries the log lines written under its id and the metric points of its own time; every resource carries its metric series and optionally the lines it wrote without a trace id; and `system_logs` adds the system namespaces' lines on the trace's nodes. `sources` says what could be read, `joins` how each signal was tied to the trace, `warnings` what went wrong.",
			PathParams:  []string{"id"}, Params: fused, Response: "FusedTrace", Handler: a.fusionTrace},
		{Method: "POST", Path: "/traces/batch", Tag: "Traces", Summary: "Read many fused traces at once",
			Description: "Up to 25 traces in one request, read three at a time and without ever taking more than part of the capacity of the stores, so other reads are not held up. One trace failing does not fail the rest: each result has its own `status`. When the time limit (30 seconds) runs out, the traces not yet read come back with status 504. The body takes the trace `ids` and every fused option (`include`, `pad`, `metric`, `omit`, `span_status` ...); options in the query string are defaults the body overrides. With `stream=true` the answer is NDJSON: one `result` line per trace as it finishes (in finishing order), then a `summary` line.",
			Params:      []string{"stream"}, Body: "BatchRequest",
			BodyExample: `{"ids": ["0af7651916cd43dd8448eb211c80319c"], "include": ["logs", "metrics"], "omit": ["events"], "log_severity": "error,warn"}`,
			Response:    "BulkResult", Stream: true, Handler: a.fusionTraceBatch},
	}
}

package fusionhttp

import "ikhnos/internal/fusionapi"

// ReadOps are the routes that read what FUSION saved, signal by signal and joined around a trace: the metrics, the logs and the
// traces. Each is described here beside the handler that answers it.
func (g *Guard) ReadOps() []Op {
	rng := []string{"from", "to"}
	join := func(groups ...[]string) []string {
		var out []string
		for _, grp := range groups {
			out = append(out, grp...)
		}
		return out
	}
	fused := fusionapi.FuseParamNames
	return []Op{
		{Method: "GET", Path: "/metrics/names", Tag: "Metrics", Summary: "List metric names",
			Params: join(rng, MetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most names (default 500, at most 5000)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "NameList", Handler: g.MetricNames},
		{Method: "GET", Path: "/metrics/series", Tag: "Metrics", Summary: "List the series (label sets) that match",
			Params: join(rng, MetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most series (default 200, at most 2000)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "SeriesList", Handler: g.MetricSeries},
		{Method: "GET", Path: "/metrics/range", Tag: "Metrics", Summary: "Read metric series over a range",
			Description: "The samples of the series that match the filters, thinned to a sensible number of points, with each series' min, max, average and last value. At least one of `name`, `metric`, `service`, `namespace`, `pod`, `node` or `application` is required: a range read of every series is refused.",
			Params:      join(rng, MetricFilter, []string{"step", "limit"}), Notes: map[string]string{"limit": "The most series (default 20, at most 100)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "MetricRange", Handler: g.MetricRange},
		{Method: "GET", Path: "/metrics/query", Tag: "Metrics", Summary: "Run a PromQL instant query",
			Description: "PromQL as written, answered the way Prometheus answers. Only for a caller whose access is not limited to certain namespaces or clusters.",
			Params:      []string{"query", "time"}, Required: []string{"query"}, Response: "PromResult", Handler: g.MetricRaw("query")},
		{Method: "GET", Path: "/metrics/query_range", Tag: "Metrics", Summary: "Run a PromQL range query",
			Description: "PromQL over a range, answered the way Prometheus answers. Only for a caller whose access is not limited. `start`, `end` and `step` are all required.",
			Params:      []string{"query", "start", "end", "step"}, Required: []string{"query", "start", "end", "step"}, Response: "PromResult", Handler: g.MetricRaw("query_range")},

		{Method: "GET", Path: "/logs", Tag: "Logs", Summary: "Search log lines",
			Description: "Lines matching every filter given, in the range. A line carries the trace and span id it was written under, so `trace_id` finds everything a request logged.",
			Params:      join(rng, []string{"service", "namespace", "pod", "cluster", "application", "category", "trace_id", "span_id", "severity", "contains", "order", "limit", "query"}),
			Notes:       map[string]string{"limit": "The most lines (default 200, at most 2000)."},
			Excludes:    map[string][]string{"query": {"service", "namespace", "pod", "cluster", "application", "category", "trace_id", "span_id", "severity", "contains"}},
			Response:    "LogResult", Handler: g.Logs},

		{Method: "GET", Path: "/traces", Tag: "Traces", Summary: "Search traces — optionally fused",
			Description: "Traces matching the filters, newest first. With `fused=true` each hit is also read in full and joined to its logs and metrics (the options below apply to every hit); that returns up to 25 traces, read in parallel, and with `stream=true` they arrive one by one as they are ready.",
			Params:      join(rng, []string{"service", "namespace", "cluster", "application", "category", "name", "status", "min_duration", "max_duration", "limit", "q"}, fused, []string{"stream"}),
			Notes:       map[string]string{"limit": "The most traces (default 20, at most 100; a fused search defaults to 10 and takes at most 25)."},
			Excludes:    map[string][]string{"q": {"service", "namespace", "cluster", "application", "category", "name", "status", "min_duration", "max_duration"}},
			Response:    "TraceList", Stream: true, Handler: g.Traces},
		{Method: "GET", Path: "/traces/{id}", Tag: "Traces", Summary: "One trace — optionally fused",
			Description: "The trace as Tempo has it. With `fused=true` (or an `include` list) it is the fused object: every span carries the log lines written under its id and the metric points of its own time; every resource carries its metric series and optionally the lines it wrote without a trace id; and `system_logs` adds the system namespaces' lines on the trace's nodes. `sources` says what could be read, `joins` how each signal was tied to the trace, `warnings` what went wrong.",
			PathParams:  []string{"id"}, Params: fused, Response: "FusedTrace", Handler: g.Trace},
		{Method: "POST", Path: "/traces/batch", Tag: "Traces", Summary: "Read many fused traces at once",
			Description: "Up to 25 traces in one request, read three at a time and without ever taking more than part of the capacity of the stores, so other reads are not held up. One trace failing does not fail the rest: each result has its own `status`. When the time limit (30 seconds) runs out, the traces not yet started come back with status 504 and one being read comes back with what was read, the rest named in its `warnings`. The body takes the trace `ids` and every fused option (`include`, `pad`, `metric`, `omit`, `span_status` ...); options in the query string are defaults the body overrides. With `stream=true` the answer is NDJSON: one `result` line per trace as it finishes (in finishing order), then a `summary` line.",
			Params:      []string{"stream"}, Body: "BatchRequest",
			BodyExample: `{"ids": ["0af7651916cd43dd8448eb211c80319c"], "include": ["logs", "metrics"], "omit": ["events"], "log_severity": "error,warn"}`,
			Response:    "BulkResult", Stream: true, Handler: g.TraceBatch},
	}
}

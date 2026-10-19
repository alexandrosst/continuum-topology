package server

import (
	"continuum/internal/fusionhttp"

	"continuum/internal/fusionapi"
)

func fusionOps(a *Admin) []fusionhttp.Op {
	rng := []string{"from", "to"}
	join := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	fused := fusionapi.FuseParamNames
	ops := []fusionhttp.Op{
		{Method: "GET", Path: "/status", Tag: "Service", Summary: "Is FUSION up, and what may I read?",
			Description: "Whether the stores are running and what the credential you present may read: its signals, namespaces and clusters, and when it expires.",
			Response:    "Status", Handler: a.fusionStatus},
		{Method: "GET", Path: "/applications", Tag: "Applications", Summary: "List the Ikhnos applications",
			Description: "The applications Ikhnos groups services into (made by a person or found by discovery), each with its services and which signals (metrics, logs, traces) FUSION has for them in the range. Pass an application's `id` or `name` as `application` to the reads below to read only that application. A token limited to certain namespaces or clusters sees only the services of an application in them, and no application with none there. The services as telemetry names them are listed by `/services`.",
			Params:      rng, Response: "ApplicationList", Handler: a.fusionApplications},
		{Method: "GET", Path: "/applications/{name}", Tag: "Applications", Summary: "One Ikhnos application at a glance",
			Description: "The signals the application has across all of its services, its most recent traces and failing traces, its latest error logs and the metric names it reports: the places to go on from. `name` is the application's id or name.",
			PathParams:  []string{"name"}, Params: rng, Response: "Overview", Handler: a.fusionApplication},
		{Method: "GET", Path: "/applications/{name}/topology", Tag: "Applications", Summary: "The topology of one Ikhnos application",
			Description: "The application's services with their health, the traffic between them and with everything else (the other services and addresses outside the clusters), and what Ikhnos recorded about all of that in the window before. Each service carries the labels its telemetry is found by (`telemetry.filter`), so the telemetry itself is read with `application` on the other routes: this answer is pointers, not data. Without `at` it is the estate now; with it, as the graph database remembered it at that moment (and `history is not enabled` without one). A token limited to certain namespaces or clusters sees only the services, neighbours and links it may see, and no addresses outside the clusters. `name` is the application's id or name.",
			PathParams:  []string{"name"}, Params: []string{"at", "window", "noise"}, Response: "ApplicationTopology", Handler: a.fusionAppTopology},
		{Method: "GET", Path: "/services", Tag: "Services", Summary: "List the services that have telemetry",
			Description: "Every service FUSION has data for in the range (its `service.name`), with which signals it has and the Ikhnos applications it is in. This includes infrastructure that reports under a name of its own, such as an exporter or an energy meter; `applications` is empty for those.",
			Params:      join(rng, []string{"application"}), Response: "ServiceList", Handler: a.fusionServices},
		{Method: "GET", Path: "/services/{name}", Tag: "Services", Summary: "One service at a glance",
			Description: "The signals the service has, its most recent traces and failing traces, its latest error logs and the metric names it reports.",
			PathParams:  []string{"name"}, Params: rng, Response: "Overview", Handler: a.fusionService},

		{Method: "GET", Path: "/links", Tag: "Links", Summary: "The latest figures of the links between clusters",
			Description: "For each pair of clusters the organisation watches (the `links` of its settings), the latest loss in each direction, round-trip time, delay variation, path MTU and throughput, and when they were measured, read from the `continuum_link_*` metrics the clusters send. A figure FUSION does not hold is absent, never zero. Each pair is listed once, with `a` before `b`, `aToB` and `bToA` naming its directions, and `measuredBy` saying whose series the figures are: both clusters measure towards each other, so do not count a pair under each of its clusters. The figures are fixed queries read by the server and shared for 15 seconds, so asking more often only costs your rate limit. Needs the `metrics` signal. A token limited to clusters sees only the pairs whose two clusters it may read; one limited to namespaces is refused, since the figures belong to no namespace. For arbitrary series, use `/metrics/range` with `metric=continuum_link_.*` (category `system`).",
			Params:      []string{"cluster", "peer"}, Notes: map[string]string{"cluster": "Only the pairs this cluster is in, on either side.", "peer": "With `cluster`: only the pair of the two. Alone it is refused."},
			Response: "LinkList", Handler: a.fusionLinks},

		{Method: "GET", Path: "/metrics/names", Tag: "Metrics", Summary: "List metric names",
			Params: join(rng, fusionhttp.MetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most names (default 500, at most 5000)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "NameList", Handler: a.fusionMetricNames},
		{Method: "GET", Path: "/metrics/series", Tag: "Metrics", Summary: "List the series (label sets) that match",
			Params: join(rng, fusionhttp.MetricFilter, []string{"limit"}), Notes: map[string]string{"limit": "The most series (default 200, at most 2000)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "SeriesList", Handler: a.fusionMetricSeries},
		{Method: "GET", Path: "/metrics/range", Tag: "Metrics", Summary: "Read metric series over a range",
			Description: "The samples of the series that match the filters, thinned to a sensible number of points, with each series' min, max, average and last value. At least one of `name`, `metric`, `service`, `namespace`, `pod`, `node` or `application` is required: a range read of every series is refused.",
			Params:      join(rng, fusionhttp.MetricFilter, []string{"step", "limit"}), Notes: map[string]string{"limit": "The most series (default 20, at most 100)."},
			Excludes: map[string][]string{"name": {"metric"}}, Response: "MetricRange", Handler: a.fusionMetricRange},
		{Method: "GET", Path: "/metrics/query", Tag: "Metrics", Summary: "Run a PromQL instant query",
			Description: "PromQL as written, answered the way Prometheus answers. Only for a caller whose access is not limited to certain namespaces or clusters.",
			Params:      []string{"query", "time"}, Response: "PromResult", Handler: a.fusionMetricRaw("query")},
		{Method: "GET", Path: "/metrics/query_range", Tag: "Metrics", Summary: "Run a PromQL range query",
			Description: "PromQL over a range, answered the way Prometheus answers. Only for a caller whose access is not limited. `start`, `end` and `step` are all required.",
			Params:      []string{"query", "start", "end", "step"}, Response: "PromResult", Handler: a.fusionMetricRaw("query_range")},

		{Method: "GET", Path: "/logs", Tag: "Logs", Summary: "Search log lines",
			Description: "Lines matching every filter given, in the range. A line carries the trace and span id it was written under, so `trace_id` finds everything a request logged.",
			Params:      join(rng, []string{"service", "namespace", "pod", "cluster", "application", "category", "trace_id", "span_id", "severity", "contains", "order", "limit", "query"}),
			Notes:       map[string]string{"limit": "The most lines (default 200, at most 2000)."},
			Excludes:    map[string][]string{"query": {"service", "namespace", "pod", "cluster", "application", "category", "trace_id", "span_id", "severity", "contains"}},
			Response:    "LogResult", Handler: a.fusionLogs},

		{Method: "GET", Path: "/traces", Tag: "Traces", Summary: "Search traces — optionally fused",
			Description: "Traces matching the filters, newest first. With `fused=true` each hit is also read in full and joined to its logs and metrics (the options below apply to every hit); that returns up to 25 traces, read in parallel, and with `stream=true` they arrive one by one as they are ready.",
			Params:      join(rng, []string{"service", "namespace", "cluster", "application", "category", "name", "status", "min_duration", "max_duration", "limit", "q"}, fused, []string{"stream"}),
			Notes:       map[string]string{"limit": "The most traces (default 20, at most 100; a fused search defaults to 10 and takes at most 25)."},
			Excludes:    map[string][]string{"q": {"service", "namespace", "cluster", "application", "category", "name", "status", "min_duration", "max_duration"}},
			Response:    "TraceList", Stream: true, Handler: a.fusionTraces},
		{Method: "GET", Path: "/traces/{id}", Tag: "Traces", Summary: "One trace — optionally fused",
			Description: "The trace as Tempo has it. With `fused=true` (or an `include` list) it is the fused object: every span carries the log lines written under its id and the metric points of its own time; every resource carries its metric series and optionally the lines it wrote without a trace id; and `system_logs` adds the system namespaces' lines on the trace's nodes. `sources` says what could be read, `joins` how each signal was tied to the trace, `warnings` what went wrong.",
			PathParams:  []string{"id"}, Params: fused, Response: "FusedTrace", Handler: a.fusionTrace},
		{Method: "POST", Path: "/traces/batch", Tag: "Traces", Summary: "Read many fused traces at once",
			Description: "Up to 25 traces in one request, read three at a time and without ever taking more than part of the capacity of the stores, so other reads are not held up. One trace failing does not fail the rest: each result has its own `status`. When the time limit (30 seconds) runs out, the traces not yet started come back with status 504 and one being read comes back with what was read, the rest named in its `warnings`. The body takes the trace `ids` and every fused option (`include`, `pad`, `metric`, `omit`, `span_status` ...); options in the query string are defaults the body overrides. With `stream=true` the answer is NDJSON: one `result` line per trace as it finishes (in finishing order), then a `summary` line.",
			Params:      []string{"stream"}, Body: "BatchRequest",
			BodyExample: `{"ids": ["0af7651916cd43dd8448eb211c80319c"], "include": ["logs", "metrics"], "omit": ["events"], "log_severity": "error,warn"}`,
			Response:    "BulkResult", Stream: true, Handler: a.fusionTraceBatch},
	}
	return append(ops, nativeOps(a)...)
}

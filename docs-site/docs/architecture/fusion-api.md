---
id: fusion-api
title: Reading FUSION (the shared API)
description: One token-protected API over the metrics, logs and traces FUSION saved - each signal on its own, or joined around a trace into one object - and exactly what a token can and cannot see.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# Reading FUSION

[FUSION](./regional-operators.md#fusion-where-a-regional-operator-saves-what-it-receives) saves what the central operator receives: metrics in Prometheus, logs in Loki, traces in Tempo, three stores that authenticate nothing and are never exposed. The shared API is the one way another system reads them back: a decision engine that wants the spans and logs behind a slow request, a dashboard, a script. It lives in the Ikhnos server, under `/api/v1/fusion`, so there is nothing more to install and one credential model for everything.

It does two jobs. It lets a caller read each signal **on its own**, with filters that are the same across the three stores (service, namespace, pod, cluster, a time range). And it **fuses** them around a trace: one call returns the trace's spans, each carrying the log lines written under its span id, and each resource (a service in a pod in a namespace) carrying the metric series saved for it around the trace's time.

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/fusion-stack.svg')} alt="Regional operators in other clusters send over mutual TLS to the central operator in the server's cluster, which writes to Prometheus, Loki and Tempo. The server reads those three stores over plain HTTP inside the cluster, scales the four FUSION workloads up and down on Enable and Disable, and answers a token-holding reader on its shared API." />
  <figcaption className="diagram-caption">FUSION is part of the server's own release and switched off until enabled. Telemetry goes in through one mTLS door; the shared API reads it back from the three stores.</figcaption>
</figure>

## Who can read

There are two kinds of caller, and nothing else is accepted.

**A FUSION access token** (`cnf_…`) is for another system. An administrator of the server's main organisation makes one on the FUSION card (Regional operators → Data access → New access token), gives it a name, picks which signals it may read, optionally limits it to some namespaces and some cluster ids, and chooses how long it lasts (30 to 365 days, 90 by default). The secret is shown once; only its SHA-256 is stored, as with every other secret here. A token is read-only, it can be revoked at once from the same card, and it opens nothing but this API: presented to any other part of the server, it is refused.

**A signed-in administrator** of that organisation (a browser session or a personal access token) can read everything, which is what lets the UI explore the data without minting a token for itself. A viewer or an editor cannot: the stored telemetry is more than the topology they can already see.

Making and revoking a token are audited, with the name and the scope and never the secret. Reads are not audited one by one (a dashboard would write thousands of rows); each token records when it was last used, and each caller is rate limited (600 requests a minute, bursts of 60). A token belongs to the organisation, not to the administrator who made it: it keeps working if that person leaves, and any administrator of the organisation can list and revoke it. A token whose stored scope cannot be read back is refused rather than treated as unlimited.

```bash
curl -H "Authorization: Bearer $TOKEN" https://ikhnos.example/api/v1/fusion/status
```

A browser on another origin cannot call it: the `Authorization` header is not among the headers the server's CORS policy allows, the same as for personal access tokens. It is a server-to-server API.

## What a token can see

A token's scope has three parts: the **signals** it may read (metrics, logs, traces), the **namespaces** whose telemetry it may see, and the **clusters** whose telemetry it may see. Empty namespaces or clusters means no limit on that part.

The limit is enforced twice. Every query a limited token causes is built by the server from structured filters, never from text the caller wrote, with the scope's matchers added to it: a `k8s_namespace_name` matcher in the PromQL selector, in the LogQL stream selector and in the TraceQL condition, and a `continuum_cluster_id` one beside it. Then each answer is checked again against the scope before it leaves, so a store that ignored a matcher still cannot leak a line. Telemetry that carries no namespace (node metrics, say) is invisible to a token that limits namespaces: the limit fails closed.

Two consequences follow, and both are deliberate:

- **A limited token cannot send a raw query.** PromQL, LogQL and TraceQL written by hand cannot be restricted safely without parsing them, so `metrics/query`, `metrics/query_range`, `logs?query=` and `traces?q=` need a token with no namespace and no cluster limit (or an administrator). Everything else works for every token.
- **A limited token sees spans, not whole traces.** A trace that crosses namespaces comes back with only the spans of the namespaces the token may see; a span whose parent is hidden is shown as a root, and the hidden parent's id is not given. A trace with no visible span reads as not found, which is also what a trace that does not exist reads as, with the same message. In a search result the trace's root service and name are left blank for a limited token (the root may be in a namespace it cannot see), and its start time and duration are those of the spans that matched, not of the whole trace; it gets the services of the spans that matched.

What the server does with the data matters too: the stores are reached over plain HTTP inside the cluster (their ClusterIP Services), and the server chart's network policy, when you turn it on, lets the server pod reach exactly those three ports and nothing else of FUSION. The server never sees telemetry *in transit* (the central operator writes straight to the stores), but it now **reads stored telemetry and returns it to token holders**. That is what the feature is; it is why only administrators of the main organisation can make tokens, and why a token's scope is narrow by choice.

## The calls

All are `GET` (but the batch read, and the backends' own POST forms), all take `from` and `to` (an RFC 3339 time, unix seconds, or `now-15m`; default the last hour, never more than 31 days) and answer JSON.

| Call | What it returns |
| --- | --- |
| `/api/v1/fusion/status` | FUSION's own state, and what this caller may read (signals, namespaces, clusters, expiry, whether raw queries are allowed). |
| `/api/v1/fusion/applications` | The services with telemetry in the range, and which signal types each has. |
| `/api/v1/fusion/applications/{name}` | One service at a glance: recent traces, recent traces with an error span, recent error-level log lines, the metric names it reports. |
| `/api/v1/fusion/metrics/names`, `/series`, `/range` | Metric names, series label sets, and series values over the range, filtered by `name`, `metric` (a regular expression over the name), `service`, `namespace`, `pod`, `node`, `cluster`. `/range` takes `step` and `limit`. |
| `/api/v1/fusion/metrics/query`, `/query_range` | PromQL as written, answered in Prometheus' own shape. Unrestricted callers only. |
| `/api/v1/fusion/logs` | Log lines, with `service`, `namespace`, `pod`, `cluster`, `trace_id`, `span_id`, `severity`, `contains`, `order=newest\|oldest`, `limit`. With `query=` it takes LogQL as written (unrestricted callers only). |
| `/api/v1/fusion/traces` | A trace search: `service`, `namespace`, `cluster`, `name`, `status=error\|ok\|unset`, `min_duration`, `max_duration`, `limit`. With `q=` it takes TraceQL as written (unrestricted callers only). With `fused=true` every hit also comes back as a fused trace ([reading many](#reading-many-traces)). |
| `/api/v1/fusion/traces/{id}` | One trace. With `fused=true` (or an `include` list) it is the fused object below. |
| `POST /api/v1/fusion/traces/batch` | Up to 25 fused traces in one request ([reading many](#reading-many-traces)). |
| `/api/v1/fusion/prometheus/...`, `/loki/...`, `/tempo/...` | Each store's own read API, unchanged ([the backends' own APIs](#the-backends-own-apis)). |
| `/api/v1/fusion/openapi.json`, `/api/v1/fusion/docs` | The OpenAPI description of all of the above, and a page that renders it and lets you try each call ([the API page](#the-api-page-and-the-openapi-description)). No credential needed: they describe the API, they read nothing. |

## The backends' own APIs

Everything above is FUSION's own shape: one set of parameters (`from`, `to`, `service`, `namespace` ...), one error format, one access model, whichever store the data is in. For everything the three stores can do beyond that (aggregations, label and tag discovery, log volumes and patterns, TraceQL metrics, exemplars), FUSION also serves each store's **own read API, unchanged**, under its name:

| Prefix | Serves | Example |
| --- | --- | --- |
| `/api/v1/fusion/prometheus/` | Prometheus' HTTP API: `query`, `query_range`, `query_exemplars`, `series`, `labels`, `label/{name}/values`, `metadata`, `status/tsdb`, `status/buildinfo` | `/api/v1/fusion/prometheus/api/v1/query?query=sum(up)` |
| `/api/v1/fusion/loki/` | Loki's: `query`, `query_range`, `labels`, `label/{name}/values`, `series`, `index/stats`, `index/volume`, `index/volume_range`, `patterns`, `detected_labels`, `detected_fields` | `/api/v1/fusion/loki/loki/api/v1/query_range?query={service_name="web"}` |
| `/api/v1/fusion/tempo/` | Tempo's: `api/traces/{id}`, `api/v2/traces/{id}`, `api/search`, `api/search/tags`, `api/v2/search/tags`, `api/search/tag/{tag}/values` (and v2), `api/metrics/query`, `api/metrics/query_range` | `/api/v1/fusion/tempo/api/search?q={ status = error }` |

After the prefix the path, the parameters and the answer are the store's own, so a client written for the store works with only its address and a bearer token changed: a Grafana datasource of type Prometheus, Loki or Tempo with the URL `https://ikhnos.example/api/v1/fusion/prometheus` (or `/loki`, `/tempo`) and an `Authorization: Bearer <token>` header, `promtool`, `logcli`. A POST with a form (how Grafana sends queries) is accepted on the query endpoints. A bad query gets the store's own error, in its own format.

What is *not* mirrored is as deliberate as what is. Only read endpoints are served: nothing that writes (remote write, OTLP and Loki push, delete, snapshot), administers (reload, quit, flush, config, ring) or tails exists at these addresses - there is no refusal to bypass, the route is simply not there (404). The same authentication, rate limit (a request is a request), 30-second timeout, 16 MiB answer cap and failure rules as the rest of the API apply, and a store's own text for a failure of the store (as opposed to a mistake in your query) is not passed on.

**Who may use it.** The mirror forwards a query the caller wrote, and a query written by hand cannot be narrowed to a namespace or a cluster without understanding it, so it is for an administrator (a session or a personal access token) or a FUSION token with no namespace or cluster limit, for the signal in question (`prometheus` needs `metrics`, `loki` needs `logs`, `tempo` needs `traces`). A token that is limited keeps the structured calls above, where the limit is enforced; on the mirror it is refused with a 403 that says so. The token is what says that this caller may read what FUSION collected; the stores themselves are never exposed.

## The fused trace

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://ikhnos.example/api/v1/fusion/traces/0af7651916cd43dd8448eb211c80319c?fused=true"
```

The answer is the trace (`traceId`, `start`, `end`, `durationMs`, `services`, `spanCount`, `errorCount`, `roots`) with:

- `spans`, in start order, each with its `parentSpanId`, `depth`, `service`, `namespace`, `pod`, `status`, `attributes`, `events`, a `resource` key, `logs` (the log lines that carry this span's id) and `metrics`: the points of its resource's series that fall inside the span's own time, widened by `span_pad` either side, each series with its `points` and a `min`, `max`, `avg` and `last` computed over just those points. A series with nothing in a span's window is left off that span.
- `resources`, one per service-in-a-pod, each with its `metrics`: the series saved for the same service and namespace (what the application itself reported) and for the same pod and namespace (what was reported about the pod), over the trace's time plus a margin, each as `points` of `[unix seconds, value]` and a `min`, `max`, `avg`, `last`.
- `logs`: how many lines were found (`total`), how many landed on a span (`matched`), and `unmatched`, the lines that carry the trace id but no span id or the id of a span this read does not have.
- `joins`: in words, how each requested signal was tied to the trace. Logs are `exact` (the record carries the ids); metrics are `associated, not proven`.
- `sources`: for each of `traces`, `logs` and `metrics` (and `contextLogs` and `systemLogs` when asked for), one of `ok`, `not requested`, `not allowed` (the token's scope does not include it), `unavailable` (the store could not be reached) or `error`; `warnings` says why. **A store that is down does not fail the read**: the trace comes back and the missing part is named. Only a trace that cannot be read at all fails.

`pad` (the margin around the trace, default two minutes, at most an hour), `span_pad` (the margin around each span when its metrics are cut out, default 30 seconds, at most an hour: samples arrive every 30 to 60 seconds, so a span of a few milliseconds would often have none without it), `metric` (a regular expression over metric names, default all), `max_logs` (default 500, at most 2000), `max_series` (per resource, default 15, at most 100) and `points` (per series, default 60) shape it. A trace returns at most 5000 spans, and says when it had more. The spans of one fused read carry at most 20000 metric points between them (overlapping spans of one pod repeat the same samples); past that a span keeps each series' summary but not its points, and `warnings` says so. The per-span metrics are cut from what was already read for the resource, so they cost no extra store call.

### Choosing what it carries

`fused=true` alone is `include=logs,metrics`. Giving `include` (a comma-separated list) says exactly what to join, and implies `fused=true`; `fused=false` with an `include` is refused rather than guessed at.

| `include` | What is joined | How |
| --- | --- | --- |
| `logs` | The lines that carry the trace's id, each on the span whose id it carries. | Exact. |
| `context_logs` | For each resource, the lines it wrote around the trace that carry **no** trace id (the connection pool warning that came just before the failure). On `resources[].logs`, oldest first, `max_context_logs` per resource (default 50). | Associated: same service, namespace and pod, in the trace's window. |
| `system_logs` | The lines of the system namespaces (`system_namespaces`, default `kube-system`) on the nodes the trace's pods ran on, in `systemLogs`, `max_system_logs` of them (default 100). One read per node, at most five nodes. | Associated: same node, in the trace's window. A token whose namespaces exclude the system ones gets a warning, not a read. |
| `metrics` | The series of each resource, and of each span's own time. | Associated. `metric_scope` says whose: `app` (what the service reported), `pod` (what was reported about its pod) and `node` (series that name the node and no pod). Default `app,pod`. |
| `all`, `none` | Everything above; or nothing, which is the trace alone, shaped by the options below. | |

The same options narrow or shape the answer:

- `log_severity` (`error` or `error,warn`) and `log_contains` apply to every log read of the fused object, so a read for failures can leave the `info` lines out.
- `span_service`, `span_status` and `span_min_duration` return only the spans that match, for a long trace of which you want the failing part. The trace's own totals (`spanCount`, `errorCount`) stay those of the whole trace, `spansOmitted` says how many were left out, and `logs.onFilteredSpans` how many of the matched lines sat on them.
- `omit=attributes,events` leaves the span and resource attributes, or the span events, out; most of a large trace's bytes are these.
- `pad`, `span_pad`, `metric`, `max_logs`, `max_series` and `points` are as below.

### What the join rests on

The join is only as good as the instrumentation behind it, and it is worth saying where it can come apart:

- **Logs to spans** use the OTLP log record's own `trace_id` and `span_id`, which Loki keeps as structured metadata. An application that logs without its trace context (most do until the logging bridge of its OpenTelemetry SDK is turned on) has logs, and traces, and nothing that joins them.
- **Metrics to spans** are placed by resource and by time, never by request. They use resource attributes that the FUSION chart promotes to Prometheus labels: `service.name`, `k8s.namespace.name`, `k8s.pod.name` and a few more (`prometheus.promoteResourceAttributes` in the FUSION chart). The join is "the same service, namespace or pod around the same time", not a causal link from one request to one sample: metrics do not carry trace ids.
- **An application is a `service.name`**, as the telemetry reports it. It is not (yet) the same thing as an application in the topology graph, which is built from discovery; matching the two is a separate piece of work.
- **Clocks.** Spans, log lines and samples are stamped by the nodes that produced them. The margin around the trace absorbs ordinary skew, not a node whose clock is minutes out.

## Reading many traces

```bash
# the ten newest failing traces of one service, each in full
curl -H "Authorization: Bearer $TOKEN" \
  "https://ikhnos.example/api/v1/fusion/traces?service=checkout&status=error&fused=true&limit=10&include=logs&omit=events"

# these specific traces, with the options stated once for all of them
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"ids": ["0af7651916cd43dd8448eb211c80319c", "..."], "include": ["logs", "context_logs"], "log_severity": "error,warn"}' \
  "https://ikhnos.example/api/v1/fusion/traces/batch"
```

`GET /traces?fused=true` searches and then reads every hit as a fused trace (a smaller page: 10 by default, at most 25). `POST /traces/batch` takes up to 25 `ids` and every fused option in the JSON body (lists as arrays); options given in the query string are defaults the body overrides. Both answer `{"results": [...], "summary": {requested, ok, failed}}` with the results in the order asked (the search also returns its `traces`). Each result has its own `status`: one trace that is missing, malformed or hidden from the token does not fail the others.

**Streaming.** With `stream=true` (or `Accept: application/x-ndjson`) the answer is newline-delimited JSON, flushed as it goes: for a search first a line with the hits, then one `{"type": "result", ...}` line per trace *as soon as it is read*, in the order they finish, then `{"type": "summary", ...}`. A caller can start on the first trace while the rest are still being read, and if the 30 seconds run out it has lost only the traces not yet done.

**Why it does not block anyone.** Three limits work together. One request reads at most three traces at once. Every store call a bulk read makes waits in a bulk lane of five of the server's eight store-call slots before it takes one, so any number of bulk requests together leave three slots free and a single read never queues behind them. And a bulk read of *n* traces costs *n* reads against the caller's rate limit (600 a minute), so it cannot be used to go round it. When the 30 seconds end, the traces not yet started come back with status `504` and a message, instead of the request failing as a whole.

## The API page and the OpenAPI description

`/api/v1/fusion/openapi.json` is an OpenAPI 3.0 description of every route, parameter and answer, and `/api/v1/fusion/docs` is a page built from it: the routes grouped, each with a form for its parameters (checkboxes for `include`, `omit` and `metric_scope`), a field for your token (kept for the browser session only; leave it empty to use your signed-in session), a Send button that calls the real API and shows the status, time and size, a *Copy as curl*, and, for `stream=true`, each line as it arrives with the time it arrived at. It also shows the shape of each object (the fused trace, a span, a resource, a log entry). The page is served with a policy that lets it run only its own script and call this server; it loads nothing from anywhere else.

The description is generated from the same table the router is built from, so a route cannot exist without being described, and a test checks that every query parameter the handlers read is described and that every route is served. Point a client generator at `openapi.json` to get a typed client.

## Limits and errors

A store answer larger than 16 MiB is refused (narrow the range or the filters). A request takes at most 30 seconds. The server keeps at most 8 calls to the stores in flight at once, across all callers, and bulk reads no more than 5 of them. A fused read looks metrics up for at most 20 resources of a trace, and clamps the window it searches to 31 days; either is reported in the read's `warnings`. When a store itself fails, the caller is told which store and the status, not the store's own error text. Failures use the usual statuses:

| Status | Meaning |
| --- | --- |
| 400 | A parameter or a filter was not valid (the message says which), or a store refused the query. |
| 401 | No credential, or one that is unknown, revoked or expired. |
| 403 | The caller's scope does not include this (a signal, or a raw query). |
| 404 | No such trace or application, or none that this caller may see. |
| 422 | The answer was larger than this API returns. |
| 429 | Too many requests from this caller. |
| 503 | The store could not be reached: FUSION is off or still starting. Turn it on from the FUSION card. |
| 504 | A store took too long. |

## Not in this release

There is no push: a caller polls (a bulk read can stream its results, but each request is still a request). There is no write path of any kind through this API (FUSION is filled by the central operator and nothing else). Token scopes name namespaces and clusters, not individual services or label sets. A limited token's application list is a sample, not a census: for logs it comes from the most recent log lines (Loki cannot filter a label-value call by structured metadata), and for traces from the services of its 200 most recent traces (Tempo's tag-values call does not promise to honour a scope), so a quiet service can be missing from it.

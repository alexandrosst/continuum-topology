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

All are `GET`, all take `from` and `to` (an RFC 3339 time, unix seconds, or `now-15m`; default the last hour, never more than 31 days) and answer JSON.

| Call | What it returns |
| --- | --- |
| `/api/v1/fusion/status` | FUSION's own state, and what this caller may read (signals, namespaces, clusters, expiry, whether raw queries are allowed). |
| `/api/v1/fusion/applications` | The services with telemetry in the range, and which signal types each has. |
| `/api/v1/fusion/applications/{name}` | One service at a glance: recent traces, recent traces with an error span, recent error-level log lines, the metric names it reports. |
| `/api/v1/fusion/metrics/names`, `/series`, `/range` | Metric names, series label sets, and series values over the range, filtered by `name`, `metric` (a regular expression over the name), `service`, `namespace`, `pod`, `node`, `cluster`. `/range` takes `step` and `limit`. |
| `/api/v1/fusion/metrics/query`, `/query_range` | PromQL as written, answered in Prometheus' own shape. Unrestricted callers only. |
| `/api/v1/fusion/logs` | Log lines, with `service`, `namespace`, `pod`, `cluster`, `trace_id`, `span_id`, `severity`, `contains`, `order=newest\|oldest`, `limit`. With `query=` it takes LogQL as written (unrestricted callers only). |
| `/api/v1/fusion/traces` | A trace search: `service`, `namespace`, `cluster`, `name`, `status=error\|ok\|unset`, `min_duration`, `max_duration`, `limit`. With `q=` it takes TraceQL as written (unrestricted callers only). |
| `/api/v1/fusion/traces/{id}` | One trace. With `include=logs,metrics` it is the fused object below. |

## The fused trace

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://ikhnos.example/api/v1/fusion/traces/0af7651916cd43dd8448eb211c80319c?include=logs,metrics"
```

The answer is the trace (`traceId`, `start`, `end`, `durationMs`, `services`, `spanCount`, `errorCount`, `roots`) with:

- `spans`, in start order, each with its `parentSpanId`, `depth`, `service`, `namespace`, `pod`, `status`, `attributes`, `events`, a `resource` key, `logs` (the log lines that carry this span's id) and `metrics`: the points of its resource's series that fall inside the span's own time, widened by `span_pad` either side, each series with its `points` and a `min`, `max`, `avg` and `last` computed over just those points. A series with nothing in a span's window is left off that span.
- `resources`, one per service-in-a-pod, each with its `metrics`: the series saved for the same service and namespace (what the application itself reported) and for the same pod and namespace (what was reported about the pod), over the trace's time plus a margin, each as `points` of `[unix seconds, value]` and a `min`, `max`, `avg`, `last`.
- `logs`: how many lines were found (`total`), how many landed on a span (`matched`), and `unmatched`, the lines that carry the trace id but no span id or the id of a span this read does not have.
- `joins`: in words, how each requested signal was tied to the trace. Logs are `exact` (the record carries the ids); metrics are `associated, not proven`.
- `sources`: for each of `traces`, `logs` and `metrics`, one of `ok`, `not requested`, `not allowed` (the token's scope does not include it) or `unavailable` (the store could not be reached); `warnings` says why. **A store that is down does not fail the read**: the trace comes back and the missing part is named. Only a trace that cannot be read at all fails.

`pad` (the margin around the trace, default two minutes, at most an hour), `span_pad` (the margin around each span when its metrics are cut out, default 30 seconds, at most an hour: samples arrive every 30 to 60 seconds, so a span of a few milliseconds would often have none without it), `metric` (a regular expression over metric names, default all), `max_logs` (default 500, at most 2000), `max_series` (per resource, default 15, at most 100) and `points` (per series, default 60) shape it. A trace returns at most 5000 spans, and says when it had more. The spans of one fused read carry at most 20000 metric points between them (overlapping spans of one pod repeat the same samples); past that a span keeps each series' summary but not its points, and `warnings` says so. The per-span metrics are cut from what was already read for the resource, so they cost no extra store call.

### What the join rests on

The join is only as good as the instrumentation behind it, and it is worth saying where it can come apart:

- **Logs to spans** use the OTLP log record's own `trace_id` and `span_id`, which Loki keeps as structured metadata. An application that logs without its trace context (most do until the logging bridge of its OpenTelemetry SDK is turned on) has logs, and traces, and nothing that joins them.
- **Metrics to spans** are placed by resource and by time, never by request. They use resource attributes that the FUSION chart promotes to Prometheus labels: `service.name`, `k8s.namespace.name`, `k8s.pod.name` and a few more (`prometheus.promoteResourceAttributes` in the FUSION chart). The join is "the same service, namespace or pod around the same time", not a causal link from one request to one sample: metrics do not carry trace ids.
- **An application is a `service.name`**, as the telemetry reports it. It is not (yet) the same thing as an application in the topology graph, which is built from discovery; matching the two is a separate piece of work.
- **Clocks.** Spans, log lines and samples are stamped by the nodes that produced them. The margin around the trace absorbs ordinary skew, not a node whose clock is minutes out.

## Limits and errors

A store answer larger than 16 MiB is refused (narrow the range or the filters). A request takes at most 30 seconds. The server keeps at most 8 calls to the stores in flight at once, across all callers. A fused read looks metrics up for at most 20 resources of a trace, and clamps the window it searches to 31 days; either is reported in the read's `warnings`. When a store itself fails, the caller is told which store and the status, not the store's own error text. Failures use the usual statuses:

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

There is no streaming or push: a caller polls. There is no write path of any kind through this API (FUSION is filled by the central operator and nothing else). Token scopes name namespaces and clusters, not individual services or label sets. A limited token's application list is a sample, not a census: for logs it comes from the most recent log lines (Loki cannot filter a label-value call by structured metadata), and for traces from the services of its 200 most recent traces (Tempo's tag-values call does not promise to honour a scope), so a quiet service can be missing from it.

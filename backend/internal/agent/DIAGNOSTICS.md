# Agent self-diagnosis, consent and what can go wrong

This is the reference for what an agent tells the server about itself, what the server may ask of an agent, and the problem codes the Agents page shows. The short version is in the README ("What an agent may see, and consent").

## Three tiers, one direction

| Name | Where it is set | Who can change it | What it means |
|---|---|---|---|
| **Installed** (the ceiling) | The chart: `access.tier`, the RBAC it creates, `scope.*` | The cluster's owner, with `helm upgrade` | The most this agent can read. Kubernetes enforces it: with `access.tier=1` the agent's role cannot list pods. |
| **Approved** | The server: the tier typed at approval, later `POST /agents/{id}/tier` | An editor or administrator | What the organisation has agreed to receive. Never above the installed ceiling. |
| **Effective** | The agent | Nobody: it is the lower of the two | What the agent is collecting right now. Reported back, so the page shows what is true and not only what was asked. |

The server can only **narrow**: lower the approved tier, pause an optional collector, leave more namespaces out. It can raise the approved tier again, but never past the installed ceiling. To go further the cluster's owner runs the command the UI prints:

```
helm upgrade continuum-agent <chart> --namespace continuum-system --reuse-values --set access.tier=2
```

The agent does not take the server's word for it. `resolveOverrides` (`stream.go`) cuts every Config to what the install allows: an approved tier above the ceiling is ignored (the agent stays at its ceiling), an unknown collector name is ignored, an invalid or system namespace is ignored. Each thing ignored is reported as `override_ignored`, so a buggy or hostile server gains nothing and the operator can see that it tried.

## What the server can push (Config)

| Field | Effect | How it is enforced |
|---|---|---|
| `approved_access_tier` | The tier to collect at, capped at the ceiling | A change of the effective tier makes the agent reconnect (a new collector with the smaller set of watches); narrowing also makes the server drop what it holds above the new tier at once. |
| `paused_collectors` (`probes`, `flow`, `measure`) | Stop an optional collector | `probes`: the receiver forgets every node's report and discards new ones, so nothing of it reaches the next picture. `flow`: the aggregator forgets what it held and takes nothing in; nothing is flushed. `measure`: the timing rounds stop and the server issues no targets. The receivers keep answering the node DaemonSets as usual (they are not told to stop), and resume when the override is lifted. |
| `excluded_namespaces` | Leave more namespaces out, on top of the install's own scope | **Emit-time filtering.** The watches are cluster-wide lists and cannot be narrowed per namespace, so the agent keeps reading but drops the namespace (and its workloads, services, pods, volumes and the traffic from or to them) when it builds the picture, before anything is sent. It is add-only: the install's own scope is never loosened. |

Overrides are stored on the server per agent (survive restarts and reconnects) and sent in every Config. Every change is in the audit trail, old to new.

## What an agent reports (Diagnostics)

Sent in the Hello, and in heartbeats **when it changed** and at least every 5 minutes (a change is looked for every 5 seconds; numbers that always move, like uptime, do not count as a change). It contains: version, architecture, uptime; installed, approved and effective tier; the scope as counts (never names); the optional collectors (configured, enabled, producing, paused by the server, nodes reporting of expected, last data); each informer (synced, last error, object count, module); the overrides in force; how many observed connections were dropped while disconnected; and the problems below.

The server keeps the latest one per agent in memory (it is refreshed at every connection), bounds its size, and puts it in the state document only for editors and administrators.

## Problem codes

A problem has a stable `code`, a `severity` (`info`, `warn`, `error`), a plain message with the fix, and `since`. The Agents page counts warnings and errors in its "N problems" line; notices do not count.

| Code | Severity | Meaning | What to do |
|---|---|---|---|
| `rbac_forbidden` | error (a watch), warn (an optional module) | The cluster answered 403: the agent may not `list`/`watch` a named resource (the message names the resource and the verb). It reports none of it and marks the module in error; the rest of the picture is still sent ("half blind, and says so"). | Compare the agent's ClusterRole with the chart's (`kubectl get clusterrole -l app.kubernetes.io/instance=continuum-agent -o yaml`), then restore the chart's permissions with `helm upgrade --reuse-values` on the same chart. An admission policy or a hand edit is the usual cause. |
| `rbac_wider_than_tier` | warn | The mirror image of `rbac_forbidden`: a periodic `SelfSubjectAccessReview` (on by default, `--rbac-self-check=false` to turn it off) finds the cluster still grants a higher tier than `access.tier` declares. `access.tier` is only ever the number the container was launched with; it is never re-verified on its own, so this is what catches a narrowing `helm upgrade` that was never run, or one that failed partway through, leaving a `ClusterRole`/`ClusterRoleBinding` a compromised agent could still use even though the server and this agent both believe access is narrower. | Run (or re-run) `helm upgrade --reuse-values --set access.tier=N` (N = this install's declared tier) against this cluster. |
| `informer_not_synced` | error (first read never finished, after 20 s), warn (a watch is failing or a module is off because the API does not serve it) | The agent has not finished reading a kind of object, or keeps failing on it. Until every watch has read its kind or been refused, the agent **holds back** its picture rather than send half of one (a full picture replaces what the server holds). | Check that the pod can reach the Kubernetes API (network policy, proxy `NO_PROXY`) and the API server's health. The message has the last error. |
| `sync_too_large` | error | The server refused the picture because it is over its limits (nodes, namespaces, workloads or total bytes) or a single message was too big. Nothing new arrives until it fits. | Narrow what the agent reports with `scope.namespaces` / `scope.exclude` (`helm upgrade --reuse-values`), or ask whoever runs the server to raise its limits. |
| `server_limits_refused` | error | The server refused another message from this agent (malformed, out of order, sent too fast). | The message says what. If it keeps happening, look at the server's log. |
| `clock_skew` | warn | The agent's clock differs from the server's by more than 2 minutes. Certificates are checked against the clock, so it can look like a certificate error. | Fix time synchronisation (NTP) on the node the agent runs on. |
| `collector_silent` | warn | An optional collector is switched on but has said nothing for more than 3 of its own intervals (`nodeProbe.interval`, `flowObserver.interval`; measurements: no round completed although there are addresses to time). | Check the DaemonSet is running (`kubectl -n continuum-system get ds`) and can reach the agent's Service on its port; a NetworkPolicy is the usual cause. On a node without eBPF the flow collector falls back to conntrack or reports nothing. |
| `scope_empty` | warn | The install's scope matches none of the cluster's namespaces, so no workloads are reported. | Change `scope.namespaces`, `scope.exclude` or `scope.selector` with `helm upgrade --reuse-values`, or clear the server-side exclusions on the agent. |
| `identity_secret_unwritable` | error | The agent cannot write its identity (the Secret holding its key and certificate), so the certificate it renews every day cannot be saved and it will lose its identity when the current one expires. It works now. | Check the Role that grants update on that one Secret (`kubectl -n continuum-system get role,rolebinding`) and restore it with `helm upgrade --reuse-values`. |
| `override_ignored` | info | The server pushed something the agent did not apply: a tier above the install's ceiling, an unknown collector, an invalid namespace. | Nothing is wrong on the agent. Only `helm upgrade --reuse-values` can widen what an agent may do. Look at why the server asked. |
| `flow_dropped` | warn | While the agent could not reach the server it held more observed connections than it may (20 000 edges) and dropped the oldest. Traffic figures for that time are incomplete. Shown for an hour after the last drop. | Usually a long outage. If it recurs while connected, report it. |
| `internal_error` | error | A task of the agent panicked. The stack is in the agent's log; the agent restarted the task and carries on. Shown for an hour. | `kubectl -n continuum-system logs deploy/continuum-agent`, and please report it. |
| `rbac_namespaced_mode` | info | This install uses `rbac.mode=namespaced` (a `Role`+`RoleBinding` per namespace instead of one `ClusterRole`), always shown while `access.tier >= 2` so it isn't mistaken for a fault. Namespace labels/creation time and persistent volumes are never read in this mode (no `Role` can grant them, only a `ClusterRole` can) and a namespace added to the cluster later stays invisible until it's added to `scope.namespaces` and the release is upgraded. | Nothing to fix; this is describing the install's own chosen mode. See "The agent's namespace and RBAC" in `deploy/README.md` for the trade-offs and the upgrade command to add a namespace. |

## Resilience behaviours worth knowing

- **Chunked full sync.** A picture larger than about 1 MiB is sent as `chunk_total` messages sharing a `sync_id`, in order, cluster facts in the first. The server counts every piece against its limits **before buffering it**, checks the assembled result, and applies it **atomically** when the last one arrives: readers see the old picture or the new one, never half of one. A connection that drops mid-picture discards the partial (the assembly belongs to the stream). Later pieces are not rate limited. Messages with `chunk_total` 0 or 1 are ordinary syncs (older agents keep working).
- **Bounded while disconnected.** The flow aggregator holds at most 20 000 distinct edges; past that it drops the oldest tenth, counts them, and reports `flow_dropped`.
- **Shutdown.** On SIGTERM the agent sends a final heartbeat, closes its stream cleanly, lets a certificate renewal that already has its certificate finish saving it, and exits within a few seconds even if a collector is still starting against an unreachable API. (The previous behaviour could wait for the collector's first sync, up to two minutes.) The server stops listening, waits five seconds for agent streams, then closes them.
- **Panics.** Every agent goroutine that can panic recovers, logs the stack, and raises `internal_error` - but what happens next is chosen per task, deliberately, rather than "log and let the goroutine quietly end" (an earlier version of this agent did exactly that for the stream's receive loop, the certificate-renewal loop, and the service-mesh policy refresh, and each one is a real, different way to end up silently degraded: the receive loop stops seeing `Config`/`Revoked` from the server for the rest of the connection while still heartbeating as if nothing were wrong; the renewal loop stops trying to renew a certificate that will eventually expire; the mesh-policy ticker stops refreshing forever after a single bad tick). Now: a panic in the receive loop or the certificate-renewal loop ends this whole connection (the same path a real network error takes) so Run's outer loop reconnects with backoff and every background task gets a clean restart; a panic during one measurement round or one service-mesh policy refresh is recovered without tearing down the connection, since those are periodic and safe to just skip once and try again next tick. The server recovers panics in its gRPC handlers and streams (the caller gets `Internal`, the process keeps serving), counts them in `continuum_stream_panics_total`.

## Server endpoints

- `GET /healthz`: the process is up. `GET /readyz`: the database answers a cheap read (cached for 2 s) and the agents' listener accepts connections; Neo4j is optional, so when it is configured but unreachable the answer is `200 ready, degraded: ...`. 503 otherwise. Both are on the admin listener, unauthenticated (they say nothing about tenants).
- Metrics: `--metrics-listen 127.0.0.1:9090` (default off, env `CONTINUUM_METRICS_LISTEN`) serves `GET /metrics` in the Prometheus text format on a separate listener. `--metrics-token-file` requires a bearer token and is required when the address is not loopback. Series: `continuum_agents{status}`, `continuum_agents_connected`, `continuum_syncs_applied_total`, `continuum_syncs_chunked_total`, `continuum_syncs_refused_total`, `continuum_auth_failures_total`, `continuum_rate_limited_total`, `continuum_stream_panics_total`, `continuum_store_errors_total`, `continuum_agent_tier_changes_total`, `continuum_agent_consent_changes_total`, `continuum_uptime_seconds`, `continuum_build_info`. Only counts: no names, addresses or cluster data.

---
id: fusion-grafana
title: Grafana and the web pages in FUSION
description: Grafana ships inside FUSION already connected to Prometheus, Loki and Tempo; Prometheus and Grafana open through the Ikhnos server, so nothing is exposed and nobody looks up an address. What the server proxies, who may open it, and what that does to the trust boundary.
---

# Grafana and the web pages in FUSION

[FUSION](./regional-operators.md#fusion-where-a-regional-operator-saves-what-it-receives) saves metrics, logs and traces. To look at them you do not need to find an address or port-forward anything: the Operators page has **Open Grafana** and **Open Prometheus**, each opening in a new tab once that part is up.

## What is in it

**Grafana** is the fifth workload of the bundled FUSION chart (`grafana.enabled`, on by default; a StatefulSet with its own small volume for the dashboards a person saves). It starts with:

- Prometheus, Loki and Tempo already provisioned as **read-only data sources** (`fusion-metrics`, `fusion-logs`, `fusion-traces`), pointing at the in-cluster Services. Nobody types a URL, and nobody can edit the connection from the UI.
- The links between them already made: a log line that carries a trace id opens that trace, and a trace opens the logs written under it (Loki keeps `trace_id` as structured metadata, which is what makes the join exact).
- Six dashboards in a folder called **Ikhnos**, linked from a menu on each of them, with **Clusters and nodes** as the home page: **Clusters and nodes** (readiness, CPU, memory, disk, network, pod health, namespaces, deployments below their replicas), **Namespaces and workloads** (pods, CPU, memory, network, restarts and replicas for a chosen cluster, namespace and workload, with the logs of the same selection), **Delivery health** (how long ago each cluster last sent, through which operator, series per cluster, scrape targets, the collectors' own memory and restarts, log lines and spans arriving) **Applications** (one Ikhnos application at a time: its services, pods, CPU, memory, spans, failing spans, traces and logs; its variables read the `ikhnos_application_info` series the server writes while FUSION runs, so choosing an application filters by its services, namespaces and clusters as the API's `application=` does) **Telemetry by category** (the same data split into three rows, **System** for the host and the collectors, **Kubernetes** for the cluster's own objects and **Application** for what the workloads report, using exactly the rule of the API's `category=` (data that arrives now also carries it as the label `ikhnos_category`, which you can use in Explore and in your own panels), [see](fusion-api.md#categories-system-kubernetes-application)) and the starter **What is arriving**. **Namespaces and workloads** and **Telemetry by category** also have an optional **Application** picker (first in the row, default *All*): it narrows the dashboard to the services of one Ikhnos application, each as one service in one namespace of one cluster, exactly like the Applications dashboard (a service called `web` in two namespaces is two members and picking one never selects the other). *All* leaves every panel exactly as it was. On Namespaces and workloads it narrows the pods, their CPU, memory, network, restarts and replicas, and the logs; on Telemetry by category it narrows the pod panels of the Kubernetes row and everything in the Application row, while the System row and the node and `kube-system` panels ignore it. Cluster, Namespace and Workload still apply on top of it, and their lists are not cut down by the application. Traces are matched by service name only (a span says nothing reliable about its cluster), and a service the application names without a namespace or cluster is kept only by telemetry that also lacks that label. The picker lists the applications of the `ikhnos_application_info` series the server writes every minute while FUSION runs, so a new application appears within about a minute. All of them refresh every 30 seconds by default, with a picker from 10 seconds to 1 hour (turn it off from the same picker). They are built only from series the collectors actually send and each query was run against real data; there is deliberately no energy or GPU dashboard until those series have been verified on real hardware. Their cluster variable reads the `continuum_cluster_id` label the install command stamps; "All" also matches data from an install that never set one. A panel whose store is off is left out.

How the **Applications** dashboard selects, so its numbers can be trusted: the application is escaped into every query, so a name with a quote works. **Service** lists the application's members, each as service/namespace/cluster, so a selection is exact: a service that runs in two namespaces is two entries, and choosing one never also picks the other. *Namespace* and *Cluster* only narrow that list, and **All** on *Service* means exactly the entries listed, never every service there is. Metrics, pods and logs are cut by the chosen tuples (a join on the info series for metrics and pods, an exact label filter for logs), so nothing from a namespace the service does not run in is counted. The dashboard is grouped in three rows: the application, its pods (Kubernetes) and what its services report (Application, with a table of the metric names). Pods are those whose Deployment, StatefulSet or DaemonSet has the service's name, in the namespace and cluster of the entry. A member Ikhnos knows no namespace or cluster for matches only telemetry that has none either. **Traces are the exception:** they follow the service name only, because a span says nothing reliable about its cluster and TraceQL cannot filter on tuples taken from a list, so two entries with the same service name in different namespaces share their traces. An empty selection (a namespace with no members, say) shows empty panels rather than an error. "Per minute" panels are rates scaled to a minute, so they do not change with the zoom, and **Delivery health** measures freshness from the last sample of the last day, so a cluster that went silent reads as old rather than as 0 seconds.
- **Charts of traces over time** (Drilldown > Traces: rate, errors, duration per service) are TraceQL metrics queries, which Tempo answers only with its metrics generator's `local-blocks` processor on. The chart turns it on (`tempo.traceqlMetrics`, default true); without it every such chart fails with `error finding generators in Querier.queryRangeRecent: empty ring`. It costs some memory and disk on the volume Tempo already has.

Grafana is **optional and secondary**. FUSION's state ("Starting - 3 of 4 parts are up") counts the central operator and the three stores only, so a Grafana that is still pulling its image never holds FUSION back, and a release with `grafana.enabled=false` (or a server Role that predates Grafana) simply has no Grafana button.

## How a person gets in

Grafana has no login page and no anonymous access. It trusts one request header (`auth.proxy`), and the only thing that ever sets it is the Ikhnos server:

1. The person opens **Open Grafana**. The UI asks the server for the page's address (`POST /fusion/pages`, an ordinary signed-in call) and opens a new tab at it. The address carries a ticket that works once, for 30 seconds: a link into a new tab does not bring the `SameSite=Strict` session cookie along, so the ticket is how the tab gets in. The server swaps it for a cookie that is good for `/fusion/` alone (HttpOnly, `SameSite=Lax`, 8 hours) and redirects to the page without the ticket.
2. On every request the server checks the session, or that page cookie, and that the person is still an **administrator** of the organisation (taking the role away takes effect at the next request); anyone else gets a 403 or a 401. It then forwards the request to Grafana's Service, **removing every `X-WEBAUTH-*` header the browser sent** and adding its own with the signed-in person's name, so a caller cannot pose as someone else.
3. Grafana gives that person the role in `grafana.role` (default **Editor**: look, build dashboards, use Explore). Data sources stay read-only whatever the role.

**Prometheus** works the same way at `/fusion/prometheus/`. It has no sign-in of its own, so the server's check is the whole gate. Prometheus is started with `--web.external-url` and `--web.route-prefix=/` (`prometheus.webPrefix`), so the links on its own pages carry the prefix the browser sees while the server strips it before forwarding.

Loki and Tempo have no web page; Grafana is how you query them.

Neither page is exposed anywhere else. The Services stay `ClusterIP`; the server is the only door, and it needs your sign-in.

## The trust boundary, stated plainly

The proxied pages are served from **the same origin as the Ikhnos UI**. A script running inside Grafana or Prometheus (a malicious dashboard panel, a plugin, a crafted data-source response) runs with the same origin as the UI, so in principle it could call the Ikhnos API as the administrator who has the tab open. What limits that:

- Only administrators can open either page, and only an administrator can save a dashboard on the Editor role. The people who can put something into Grafana are the people who could already do the same things through the API.
- The session cookie is `SameSite=Strict`, and the proxy never forwards it (or any `Authorization` header) to Grafana or Prometheus. Cookies they try to set are dropped, since every request is signed in by the header anyway. The page cookie reaches only `/fusion/`, the API does not accept it, and it is not forwarded either. It is `Lax`, not `Strict`, because it has to come with a navigation that starts in another tab; for that reason a link to a page from another site would load it (reading only: what the pages may write is limited by the rules in this section, and a write from another origin is refused).
- Grafana is served with a Content-Security-Policy; Prometheus pages get a restrictive one added by the server (same-origin scripts and styles, no framing, no outside connections).
- Grafana's login form and anonymous access are off, and so are public dashboards, external snapshots, plugin installation, embedding, update checks and telemetry. The one account that appears is the person the server signed in.

If that residual risk is more than you want, set `grafana.enabled=false` in the FUSION values (or `fusion.grafana.enabled=false` in the server chart): the stores and the shared API are unchanged. Prometheus's page cannot be turned off separately from Prometheus.

## What may reach the stores: NetworkPolicy, on by default

The three stores authenticate nothing, so the network is the only thing between them and every other pod. `networkPolicy.enabled` is now **true by default** (it used to be off) and installs two policies:

- **The stores** accept connections only from the other pods of the same release (the central operator, Grafana, and the Ikhnos server when it is installed together with FUSION) and from whatever `networkPolicy.allowedIngress` names, which is **nothing by default**. A regional operator sends to the central operator, which is in the release, so it needs no entry; no pod of another release or namespace reaches a store, whatever labels it carries (an earlier default admitted any pod labelled `app.kubernetes.io/name: continuum-regional-operator` in any namespace, and a label is something anyone who can create a pod can set).
- **Grafana** accepts connections only from the same release: that is, from the server's proxy. Nothing else in the cluster can reach it, so nothing else can present that trusted header.

The server chart's own egress policy already lets the server pod reach the stores' query ports (9090, 3100, 3200) and Grafana (3000).

**Upgrading.** If something wrote to a store directly (a regional operator with an export route pointing at Prometheus, Loki or Tempo, a hand-made collector, a test pod), it is now refused. Name it in `networkPolicy.allowedIngress` (the same shape as a NetworkPolicy `from` entry), with its namespace and its pod label, for example `[{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: operators}}, podSelector: {matchLabels: {app.kubernetes.io/name: continuum-regional-operator}}}]`, or set `networkPolicy.enabled=false` and accept the exposure. A cluster whose network plugin does not enforce NetworkPolicy ignores all of this.

## Permissions the server needs

Switching FUSION on and off scales Grafana along with the other workloads, so the server's namespaced Role (`fusionControl`) grants `get` and `patch` on the `scale` subresource of Grafana's StatefulSet too. Grafana is treated as optional: if it does not exist, or the Role does not cover it, the server skips it and FUSION still switches. No other permission changed.

## Checking it

- `kubectl -n <ns> get pods -l app.kubernetes.io/instance=<release>` shows the five pods.
- On the Operators page the FUSION card shows each part with its own state, and Open Grafana is disabled (with "Available when it has started") until Grafana is ready.
- In Grafana, **Connections > Data sources** lists the three stores; they carry a "provisioned" label and cannot be edited.

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
- One starter dashboard, **What is arriving**: clusters reporting and series by cluster, log lines by namespace with the latest logs, and recent traces, so the first thing you see after connecting a cluster is proof that it is working.

Grafana is **optional and secondary**. FUSION's state ("Starting - 3 of 4 parts are up") counts the central operator and the three stores only, so a Grafana that is still pulling its image never holds FUSION back, and a release with `grafana.enabled=false` (or a server Role that predates Grafana) simply has no Grafana button.

## How a person gets in

Grafana has no login page and no anonymous access. It trusts one request header (`auth.proxy`), and the only thing that ever sets it is the Ikhnos server:

1. The person opens **Open Grafana**. The browser calls `/fusion/grafana/` on the server, with the session cookie it already has.
2. The server checks the session and that the person is an **administrator**; anyone else gets a 403. It then forwards the request to Grafana's Service, **removing every `X-WEBAUTH-*` header the browser sent** and adding its own with the signed-in person's name, so a caller cannot pose as someone else.
3. Grafana gives that person the role in `grafana.role` (default **Editor**: look, build dashboards, use Explore). Data sources stay read-only whatever the role.

**Prometheus** works the same way at `/fusion/prometheus/`. It has no sign-in of its own, so the server's check is the whole gate. Prometheus is started with `--web.external-url` and `--web.route-prefix=/` (`prometheus.webPrefix`), so the links on its own pages carry the prefix the browser sees while the server strips it before forwarding.

Loki and Tempo have no web page; Grafana is how you query them.

Neither page is exposed anywhere else. The Services stay `ClusterIP`; the server is the only door, and it needs your sign-in.

## The trust boundary, stated plainly

The proxied pages are served from **the same origin as the Ikhnos UI**. A script running inside Grafana or Prometheus (a malicious dashboard panel, a plugin, a crafted data-source response) runs with the same origin as the UI, so in principle it could call the Ikhnos API as the administrator who has the tab open. What limits that:

- Only administrators can open either page, and only an administrator can save a dashboard on the Editor role. The people who can put something into Grafana are the people who could already do the same things through the API.
- The session cookie is `SameSite=Strict`, and the proxy never forwards it (or any `Authorization` header) to Grafana or Prometheus. Cookies they try to set are dropped, since every request is signed in by the header anyway.
- Grafana is served with a Content-Security-Policy; Prometheus pages get a restrictive one added by the server (same-origin scripts and styles, no framing, no outside connections).
- Grafana's login form and anonymous access are off, and so are public dashboards, external snapshots, plugin installation, embedding, update checks and telemetry. The one account that appears is the person the server signed in.

If that residual risk is more than you want, set `grafana.enabled=false` in the FUSION values (or `fusion.grafana.enabled=false` in the server chart): the stores and the shared API are unchanged. Prometheus's page cannot be turned off separately from Prometheus.

## What may reach the stores: NetworkPolicy, on by default

The three stores authenticate nothing, so the network is the only thing between them and every other pod. `networkPolicy.enabled` is now **true by default** (it used to be off) and installs two policies:

- **The stores** accept connections only from the other pods of the same release (the central operator, Grafana, and the Ikhnos server when it is installed together with FUSION) and from whatever `networkPolicy.allowedIngress` names. The default entry admits pods labelled `app.kubernetes.io/name: continuum-regional-operator` in any namespace, because a regional operator can be routed straight to a store.
- **Grafana** accepts connections only from the same release: that is, from the server's proxy. Nothing else in the cluster can reach it, so nothing else can present that trusted header.

The server chart's own egress policy already lets the server pod reach the stores' query ports (9090, 3100, 3200) and Grafana (3000).

**Upgrading.** If something other than a regional operator wrote to a store directly (a hand-made collector, a test pod), it is now refused. Add it to `networkPolicy.allowedIngress` (the same shape as a NetworkPolicy `from` entry), or set `networkPolicy.enabled=false` and accept the old exposure. A cluster whose network plugin does not enforce NetworkPolicy ignores all of this, exactly as before.

## Permissions the server needs

Switching FUSION on and off scales Grafana along with the other workloads, so the server's namespaced Role (`fusionControl`) grants `get` and `patch` on the `scale` subresource of Grafana's StatefulSet too. Grafana is treated as optional: if it does not exist, or the Role does not cover it, the server skips it and FUSION still switches. No other permission changed.

## Checking it

- `kubectl -n <ns> get pods -l app.kubernetes.io/instance=<release>` shows the five pods.
- On the Operators page the FUSION card shows each part with its own state, and Open Grafana is disabled (with "Available when it has started") until Grafana is ready.
- In Grafana, **Connections > Data sources** lists the three stores; they carry a "provisioned" label and cannot be edited.

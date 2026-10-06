# continuum-regional-operator

A standalone OTel Collector that aggregates telemetry a set of already-approved clusters' own
`continuum-agent` releases already export, and re-exports it as one stream. One Deployment, one image,
no RBAC and no channel to the Ikhnos server at all — see
[Regional operators](https://alexandrosst.github.io/continuum-topology/architecture/regional-operators)
for the full architecture and why those boundaries are deliberate.

This file is the chart's own reference material: everything that doesn't change between installs. What
*this particular release* has turned on is printed by `helm install`/`helm upgrade` itself (and again any
time with `helm get notes <release> -n <namespace>`); this file explains what each of those lines means and
the commands you'd only need occasionally, not at every install.

## What it is, and isn't

- **By default it never dials the Ikhnos server.** There is no `server.address` or enrollment anywhere
  in this chart. The server only ever learns that the operator exists and what it's configured to do, never
  the data passing through it. The one exception is the opt-in [heartbeat](#heartbeat-opt-in) below, off
  unless you set `heartbeat.enabled`.
- **It needs no Kubernetes API access.** `serviceaccount.yaml` renders a `ServiceAccount` with
  `automountServiceAccountToken: false` and nothing else — no `ClusterRole`, no `ClusterRoleBinding`. It
  only relays and re-processes telemetry that already arrived over OTLP; it never watches this cluster's
  own object graph the way `continuum-agent`'s `k8sattributes` processor does, so there is no
  `telemetry.scope` or namespace filter here either.
- **What it checks is inbound only, and for an operator Ikhnos creates today it is the client
  certificate alone.** The install command sets `receiver.tls.enabled` + `receiver.tls.mtls`, so only a
  source cluster presenting a certificate signed by this operator's own private CA (in `ca.crt` of the receiver TLS
  Secret, the same CA that signed the receiver's own certificate) can send anything - a certificate from another
  operator's CA, or from the org CA, is rejected (an mTLS operator created before per-operator CAs has the
  org CA there instead, and accepts any certificate that CA signed: weaker, fixed by recreating it); `receiver.auth.enabled` is `false` and no receiver bearer token exists. It also
  sets `receiver.requireAuth=true`, which makes the chart refuse to render if neither gate is on. Operators
  created before that (and one whose certificates could not be minted) instead check a receiver bearer
  token (`receiver.auth.enabled=true`, Secret named by `receiver.auth.secretName`), shown once when created,
  the server keeping only a hash of it. The defaults in `values.yaml` (both gates off) mean an open receiver
  and only matter if you hand-edit these values. If you opt in to the heartbeat there is also a second,
  separate heartbeat secret.

## Heartbeat (opt-in)

Off by default. With `heartbeat.enabled: false` this chart renders exactly what it did before the heartbeat
existed and never contacts the Ikhnos server. Turn it on and the Ikhnos UI can show this operator as
online, offline or last-seen.

**What is sent.** Every `heartbeat.intervalSeconds` (default 60; 10 to 60 allowed) the collector probes its own
health endpoint on `127.0.0.1` (the one the liveness probe uses) and a separate metrics pipeline - fed by
nothing else, sharing no receiver, processor or exporter with the pipelines that relay your telemetry - POSTs
that single result (`httpcheck.status`) to `heartbeat.url`. **It carries no telemetry**: nothing you relay, no
logs or traces, no cluster or workload data, nothing from `export.*` or `receiver.*`. Ikhnos discards the
body and records only that an authenticated request arrived, and when.

**Its credential.** A heartbeat secret, separate from the receiver bearer token (which must never be reused for
a call in the opposite direction). Ikhnos mints it once - when the operator is created with the heartbeat
on, or from the operator's heartbeat action - and prints the exact commands:

```
kubectl create secret generic <release>-heartbeat-auth --namespace <ns> --from-literal=token=<heartbeat secret>
helm upgrade <release> <chart> --namespace <ns> --reuse-values \
  --set heartbeat.enabled=true --set heartbeat.url=https://<server>/api/v1/operator-heartbeat \
  --set heartbeat.auth.secretName=<release>-heartbeat-auth
```

Rotating it in Ikhnos stops the old secret working at once; replace the Secret and restart the Deployment
(`kubectl rollout restart`) and the heartbeat resumes. Until then the collector logs `401` for each attempt.

**TLS.** `heartbeat.url` must be `https://`. The server's certificate is verified against the image's normal
trust roots, or - for a private CA - against `ca.crt` (key set by `heartbeat.tls.caSecretKey`) of the Secret named
in `heartbeat.tls.caSecretName`. Verification is never turned off; `heartbeat.allowPlainHTTP` exists only to
permit an `http://` URL for a throwaway test and sends the secret unencrypted.

**Egress.** If `networkPolicy.egress.enabled` is on, add the heartbeat URL's address and port to
`networkPolicy.egress.allowedEgress`; nothing here does it for you.

## Checking on it

```
kubectl -n <namespace> get pods -l app.kubernetes.io/name=continuum-regional-operator
kubectl -n <namespace> logs deploy/continuum-regional-operator
```

There's no approval wait or enrollment state to watch for — once the pod is `Running`, it's already
receiving on `:4317`/`:4318` (grpc/http) and re-exporting to `export.otlp.endpoint`.

## Connecting a source cluster

This chart's own install does not reach into any other cluster. Each source cluster's own
`continuum-agent` release needs a separate `helm upgrade --reuse-values --set telemetry.export.otlp.endpoint=...`
pointed at this operator's receiver (`<release>.<namespace>.svc:4317`) — Ikhnos prints the exact command
for every source cluster named when the operator was created. Nothing here applies that for you, and adding
a cluster later is the same `helm upgrade` against that cluster's own release, not against this chart.

## Values reference

`values.yaml` is commented in full; the shape worth knowing before you read it:

- **`export.otlp`** — where this operator sends what it aggregates: another regional operator's receiver,
  or an observability backend directly. `export.otlp.endpoint` is the only required value in this chart.
- **`receiver.auth` / `receiver.tls` / `receiver.requireAuth`** — the inbound side: the bearer token and/or
  required mTLS client certificate a source cluster's collector must present. Either can stand alone (new
  operators use the certificate only); `requireAuth` refuses to render a receiver that has neither.
- **`receiver.networkPolicy`** / **`networkPolicy.egress`** — ingress and egress lockdown, both off by
  default for the same reason `continuum-agent`'s equivalents are: the right policy depends on your CNI and
  network, and a wrong one silently cuts a pipeline off rather than failing loudly.
- **`heartbeat`** — the opt-in liveness report described above: `enabled`, `url`, `intervalSeconds`, `auth`
  (the Secret holding the heartbeat secret), `tls` (an optional private CA) and `allowPlainHTTP`.
- **`processors`** — `memory_limiter`, `resourceDetection`, `redaction`, trace sampling and the
  `extraProcessors`/`extraProcessorNames` escape hatch, deliberately the same shape as
  `continuum-agent`'s `telemetry.processors` so the same processor-editing UI drives both charts unmodified.

## Removing it

```
helm uninstall <release> -n <namespace>
```

There is no identity Secret to preserve here (unlike `continuum-agent`'s `continuum-agent-identity`) —
this chart holds no long-lived identity of its own, only the receiver credential (client-CA certificate or bearer token) you gave it. Ikhnos
keeps its own record of the operator until you remove it in the UI; uninstalling the chart does not do
that for you, and any source cluster still pointed at this receiver will simply fail to export until its
own `telemetry.export.otlp.endpoint` is changed.

## No chaining, yet

`export.otlp.endpoint` can point at another regional operator's receiver address, but the Ikhnos server
refuses to create or update an operator whose `Destination` names another operator (`DestinationKind:
"operator"`) — see [Regional operators § How one is created](https://alexandrosst.github.io/continuum-topology/architecture/regional-operators#how-one-is-created).
This is a deliberate, named gap, not an oversight: the mechanism exists, the decision layer that should
pick a fleet's topology does not, yet.

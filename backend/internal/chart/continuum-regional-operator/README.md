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

## Renewed certificates

Run the install command again with the renewed Secrets and the new certificate is picked up in one of two ways:

- **At once, when Helm can see the cluster.** The pod carries a `checksum/mtls` annotation over what the TLS Secrets
  it mounts hold (the receiver certificate, each destination's client certificate, the heartbeat CA), computed with
  Helm's `lookup`; a changed Secret changes the annotation and the pod restarts. Under `helm template` - which is
  what Argo CD and Flux run - `lookup` sees nothing, so no annotation is rendered and nothing restarts. An account
  that may not `get` Secrets in the namespace makes Helm fail the render: set `rolloutOnSecretChange=false`.
- **Without a restart, on a later connection.** Every receiver and every client-certificate exporter has
  `reload_interval: 1h`: the certificate and key are re-read from the mounted Secret (Kubernetes refreshes the mount
  within about a minute) at the first new handshake after the hour has passed, so a connection that stays open keeps the
  certificate it started with ([configtls](https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/configtls/README.md)).
  The receiver's mTLS client CA is re-read when the file changes (`client_ca_file_reload`). A destination's CA bundle
  (`ca.crt` of an exporter) is read at start only: after replacing it, run `kubectl rollout restart`.

## Where it sends, through a proxy, and what the receiver accepts

- **Endpoint shape.** `export.otlp.protocol=grpc`: `host:port`, no path (`[fd00::5]:4317`; a leading `https://`, `http://`
  or `dns:///` is accepted). `protocol=http`: `host[:port]` or a base URL; the collector appends `/v1/metrics`, `/v1/logs` or
  `/v1/traces`. A route whose address is not `<base>/v1/<signal>` sets `fullUrl: true` (posted as written). An endpoint
  that cannot work is refused when the chart is rendered, with the value to change in the message; the other protocol's
  conventional port (`:4318` with grpc, `:4317` with http) can be allowed with `export.checkPorts=false`. Ports and paths
  of common backends are listed in continuum-agent's README, "Where the data goes".
- **Proxy (`export.proxy`).** Same semantics as the agent's: the exporters read `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`;
  OTLP/gRPC uses `HTTPS_PROXY` only (a CONNECT tunnel); `NO_PROXY` always holds `localhost`, `127.0.0.1`, `::1`, `.svc`,
  `.cluster.local` plus `export.proxy.noProxy`. The heartbeat to the Ikhnos server goes through the proxy too. Credentials
  in the proxy URL belong in a Secret (`export.proxy.secretName`, keys `HTTPS_PROXY`, optionally `HTTP_PROXY`). With
  `networkPolicy.egress` on, allow the proxy's address and port. `extraEnv` and `dnsConfig` (ndots) are available as well.
- **What the receiver accepts.** gRPC pings from a sender's `telemetry.export.keepalive` (one every 30s, also while idle; a
  stock receiver would answer faster pings with `too_many_pings`), and, with `receiver.tls.mtls`, a client CA that is
  re-read when its Secret changes (`client_ca_file_reload`), so adding a source cluster's CA needs no restart. Its server
  certificate is re-read at the next handshake after an hour; a destination's CA bundle (`export...tls.caSecretName`) is
  read at start only.

### Troubleshooting export

Read the pod's log: `kubectl -n <namespace> logs deploy/<release>-regional-operator | grep -E "Exporting failed|createTransport"`.
Lines measured with the pinned collector (0.160.0):

| Log line (abridged) | Cause | Fix |
|---|---|---|
| `connect: connection refused` (`Exporting failed. Will retry`) | Nothing listens at that address and port, or a firewall / NetworkPolicy rejects it | Check the endpoint and port; `networkPolicy.egress.allowedEgress` |
| `tls: failed to verify certificate: x509: certificate signed by unknown authority` | The destination's certificate is signed by a CA the image does not trust | `tls.caSecretName` (Secret with `ca.crt`); a TLS-terminating proxy: its CA |
| `x509: cannot validate certificate for <ip> because it doesn't contain any IP SANs` | Endpoint is an IP, the certificate names hosts only | Use the name, or `tls.serverName` |
| `x509: certificate is valid for other.test, not recv.test` | Name in the endpoint differs from the certificate's | `tls.serverName` |
| `x509: certificate has expired or is not yet valid` | Expired certificate on the destination, or a wrong clock on the node | Renew; check node time |
| `tls: first record does not look like a TLS handshake` | TLS client, plaintext port (`insecure: false` against a plaintext receiver) | `tls.insecure=true` for that destination, or enable TLS there |
| `error reading server preface: EOF` | Plaintext client, TLS port | `tls.insecure=false` |
| `error reading server preface: remote error: tls: certificate required` | The receiver requires a client certificate | `tls.mtls.enabled` with a Secret holding `tls.crt`, `tls.key`, `ca.crt` |
| receiver log: `TLS handshake error ... remote error: tls: bad certificate` | The client certificate is not signed by the CA the receiver trusts (receiver side) | Reissue; on the operator, `client_ca_file_reload` picks up a new CA without a restart |
| `code = Unauthenticated desc = missing or empty authorization header: Authorization` | No credential sent | `auth.secretName` (key `auth.secretKey`) |
| `code = Unauthenticated desc = provided authorization does not match expected scheme or token` | Wrong token, or a token without the `Bearer ` prefix the receiver expects | Fix the Secret value; the install command again restarts the pods |
| `error reading server preface: http2: failed reading the frame payload: http2: frame too large, note that the frame header looked like an HTTP/1.1 header` | gRPC client on an HTTP port (4318) | `protocol=http`, or port 4317 |
| `net/http: HTTP/1.x transport connection broken: malformed HTTP response "\x00\x00..."` | HTTP client on a gRPC port (4317) | `protocol=grpc`, or port 4318 |
| `HTTP Status Code 404` / `code = Unimplemented` (Dropping data) | The path is wrong (a `/v1/<signal>` appended to an address that has it, or a backend with another path), or the receiver has that signal off | Drop a doubled `/v1/...`; `fullUrl`; check which signals the receiver accepts |
| `HTTP Status Code 400` (Dropping data) | The destination did not accept the body, e.g. gRPC-framed bytes to an HTTP port | Check protocol against port |
| `grpc: received message larger than max (N vs. 4194304)` (Dropping data) | A request above the receiver's 4 MiB | Keep `export.queue.maxRequestBytes` at or below it |
| `write ...: no space left on device` | Persistent queue's `emptyDir` is full | `queue.persistent.sizeLimit`, `queue.size`; see above |
| `Client received GoAway ... too_many_pings` | `export.keepalive.time` faster than the destination allows | Raise it or set the destination's policy |
| `proxyconnect tcp: ...` / `Proxy Authentication Required` | The proxy refuses or needs credentials (message text from the Go standard library: unverified with this collector) | `proxy.secretName`; allow the destination at the proxy |


## Exposing the receiver

`service.type` is `ClusterIP` unless the install command asks for more. `LoadBalancer` and `NodePort` publish only the
OTLP/gRPC port. A `NodePort` gets a random port from Kubernetes each time its Service is created; set
`service.nodePort` to keep it (it must be inside your cluster's node-port range, 30000-32767 by default). Remember that a node address is one node, and often a private one. An
Ingress in front must pass TLS through to the pod (the client certificate is checked here, not at the Ingress).

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

## Platform notes (admission, registries, mesh)

The pod passes Pod Security `restricted` (non-root, no host access, read-only root filesystem, all capabilities dropped, `RuntimeDefault`
seccomp). **Images**: `global.imageRegistry` replaces
the registry host of `image.repository` and keeps the rest of its path, for an air-gapped mirror. **Service mesh**: the pod is labelled
`sidecar.istio.io/inject: "false"` and annotated `linkerd.io/inject: disabled` (`mesh.injection=inherit` turns that off), because the receiver
terminates TLS/mTLS itself and an injected namespace enforcing `restricted` would refuse Istio's `NET_ADMIN` init container. **Network
policy**: the egress policy always lets DNS through to kube-dns/CoreDNS and NodeLocal DNSCache (169.254.20.10).

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
- **`export.proxy` / `export.timeout` / `export.keepalive` / `export.checkPorts`, `extraEnv`, `dnsConfig`** — reaching the destination
  through a proxy, the per-attempt timeout, idle-connection pings, the port check, extra environment and pod DNS options,
  described above.
- **`export.queue`** — retry window, queue size, the largest request (`maxRequestBytes`) and the opt-in persistent queue, described above.
- **`selfMetrics`** — the collector's own metrics, on by default, described above.
- **`podDisruptionBudget`** — with more than one replica, a budget of `maxUnavailable` (1), and upgrades replace one pod at a time.
- **`processors`** — `memory_limiter`, `batch` sizes, `resourceDetection`, `redaction`, trace sampling and the
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

# continuum-agent

Read-only discovery agent for Ikhnos. One Deployment, one image, three optional roles (`agent` always
runs; `probe` and `flow` are opt-in DaemonSets). Dials out to the server named in `server.address` — nothing
in the cluster it watches needs to be reachable from outside.

This file is the chart's own reference material: everything that doesn't change between installs. What
*this particular release* has turned on is printed by `helm install`/`helm upgrade` itself (and again any
time with `helm get notes <release> -n <namespace>`); this file explains what each of those lines means and
the commands you'd only need occasionally, not at every install.

## Supported clusters

| Cluster | Status |
|---|---|
| k3s | Tested end to end on a live cluster (delivery of every signal, scope, egress policy, Pod Security pre-flight, upgrade). |
| kubeadm, kind, minikube | Expected to work; not yet run live. Their kubelets serve a certificate of their own by default, so kubelet metrics need `telemetry.kubelet.ca`, `telemetry.kubelet.viaAPIServer` or `telemetry.kubelet.insecureSkipVerify` (see "Kubelet metrics verify the kubelet"). |
| EKS, GKE, AKS with ordinary (Standard) nodes | Expected to work; not tested. |
| OpenShift/OKD, GKE Autopilot, EKS Fargate, Cilium network-policy presets, IPv6-only and dual-stack clusters, SELinux-enforcing nodes | Not supported by this chart. The telemetry pods do not carry the adaptations those platforms need (security context constraints, SELinux types, Cilium policies, IPv6 addressing). |

## What it can see, by access tier

- **Tier 0, "registered only"**: the UID of the `kube-system` namespace, which is how Ikhnos tells
  clusters apart. Nothing else.
- **Tier 1, "infrastructure"**: the cluster's identity, plus nodes, storage classes and ingress classes. No
  namespaces, workloads or pods.
- **Tier 2, "services"** (the default, and the widest): the cluster's identity, nodes, storage and ingress
  classes, plus namespaces, workloads, pods, services and ingresses (and the names of persistent volume
  claims, autoscaling and disruption rules). Kubernetes cannot grant read access to part of an object, so the
  agent does receive full pod specs; it drops environment variables, commands, arguments and volume sources
  on arrival and never stores or sends them.

It is read-only in every tier: it never creates, changes or deletes anything in the cluster except its own
identity Secret (`continuum-agent-identity`), and it cannot read any other Secret or any ConfigMap.

Scoping (`scope.namespaces` / `scope.selector` / `scope.exclude`) narrows what is *reported*, not what the
agent's RBAC *allows* — the ClusterRole/ClusterRoleBinding for the installed tier is granted regardless, and
stays granted until you change the tier (see "Changing what it may see" below).

### RBAC mode: namespaced

`rbac.mode=namespaced` grants tier 2 with a `Role`/`RoleBinding` per namespace in `scope.namespaces`,
instead of one cluster-wide `ClusterRole` — for platforms that disallow `ClusterRole` for tenants. It is a
real trade-off, not a strict downgrade:

- `scope.selector` cannot be used in this mode (evaluating a label selector needs cluster-wide namespace
  listing, which namespaced mode exists to avoid).
- A namespace added to the cluster later is invisible until it is added to `scope.namespaces` and the
  release is upgraded (see the `helm upgrade --set "scope.namespaces=..."` example below).
- Namespace metadata (labels, creation time — only the name is shown) and persistent volumes (only claims
  are shown) are never read at all in this mode: no `Role`, in any namespace, can grant either (both are
  cluster-scoped types).

Tier 1 (nodes, storage classes, ingress classes) is unaffected by this setting: those are cluster-scoped
types with no namespaced form, so they still need the tier-1 `ClusterRole` regardless.

## Checking on it

```
kubectl -n <namespace> get pods -l app.kubernetes.io/name=continuum-agent
kubectl -n <namespace> logs deploy/continuum-agent
```

`READY 1/1` means the agent has reached the server (or is waiting for approval, which is normal and not an
error — the pod is not restarted for it); `0/1` means it has not, or has been revoked. The logs say which.

## Changing what it may see

Always upgrade with `--reset-then-reuse-values` (Helm 3.14 or newer), or every setting you gave at install falls back
to the chart's default (the tier defaults to 2, "services", the widest):

```
helm upgrade <release> <chart> -n <namespace> --reset-then-reuse-values --set access.tier=1
```

Not `--reuse-values`: that swaps the new chart's defaults for the old release's whole set of values, so a setting the
newer chart added (every telemetry option that came after your install, for one) is missing and the render fails.
`--reset-then-reuse-values` starts from the new chart's defaults and puts what you set on top. This is also what the
commands printed by the Ikhnos UI use.

The cluster owner decides this; the server can approve up to whatever tier is installed and never beyond
it. **Narrowing the approved tier in the Ikhnos UI does not shrink what is granted in the cluster** — that
is a reporting control on the agent, not an RBAC control. The agent's detail page in the UI prints the exact
`helm upgrade ... --set access.tier=N` that actually removes the wider `ClusterRole`/`ClusterRoleBinding`
once you're ready to do that.

`enrollment.key`'s token half is only used on first start; it does nothing on later upgrades. Unlike
`enrollment.token`, there is no need (and no safe way) to blank it afterwards — it also carries the CA pin,
which the agent needs on every restart, not just the first. Leaving it set is fine.

## Removing it

```
helm uninstall <release> -n <namespace>
```

This removes the agent, its RBAC and the probe/flow parts. It deliberately **keeps** the Secret
`continuum-agent-identity` (the agent's private key and certificate), so a reinstall under the same release
name and namespace finds its identity again. To remove that too:

```
kubectl -n <namespace> delete secret continuum-agent-identity
```

Ikhnos's server keeps its record of this cluster until you remove it on the Discovery page — uninstalling
the chart does not do that for you.

## Node probe (`nodeProbe.enabled`)

One small read-only pod per node reports the machine type to the agent (in-cluster, signed with a generated
secret). It mounts the host's `/sys` read-only when `nodeProbe.hostSys` is set.

## Traffic observer (`flowObserver.enabled`)

One collector per node counts connections between workloads (`flowObserver.method`: `auto`, eBPF where the
kernel supports it and conntrack otherwise). It runs as root with `CAP_BPF`/`CAP_PERFMON` and an unconfined
seccomp profile because loading a tracing program needs them, and in the host PID namespace when
`flowObserver.liveBytes` is set so it can count the bytes of open connections. It reads no packet payloads.

## Telemetry (`telemetry.*`)

Independent of the access tier above, on its own ServiceAccount, and never reported to the Ikhnos server
itself — it goes straight to whatever OTLP endpoint `telemetry.export.otlp.endpoint` names. The host
collector (for signals that can only be observed per-node) mounts several host paths read-only (kubelet
stats, container/journal logs).

- **Where applications push.** Applications send OTLP to the Service `continuum-telemetry-cluster` in this release's
  namespace: `continuum-telemetry-cluster.<namespace>.svc:4317` (gRPC) or `:4318` (HTTP, `/v1/metrics`, `/v1/logs`,
  `/v1/traces`), for the signals that are on only (any other is refused: 404 over HTTP, `Unimplemented` over gRPC). With
  `telemetry.receiver.auth` on, send `Authorization: Bearer <token>`. The collector attributes a record to a pod by the
  source address of the connection, which is what `telemetry.scope` then reads. That cannot work for a pod on the host
  network (every such pod shares the node's address) or for traffic that is address-translated on the way in: with
  `scope.namespaces` set that data is dropped, and with only `scope.exclude` / `scope.workloads` it passes without a
  namespace. Give such an application its own identity instead: `OTEL_RESOURCE_ATTRIBUTES=k8s.pod.uid=$(POD_UID)` with
  `POD_UID` from the downward API (`metadata.uid`) is looked up first. An identity an application sets itself
  (`k8s.namespace.name`, ...) is never overwritten by the collector, so the scope trusts it. The Service is
  reachable from every namespace unless `telemetry.receiver.networkPolicy` (or a policy of your own) says otherwise.
- **Behind a service mesh.** The cluster collector pod asks Istio (`sidecar.istio.io/inject: "false"`) and Linkerd
  (`linkerd.io/inject: disabled`) not to inject a proxy into it (`mesh.telemetryInjection=inherit` leaves that
  to the namespace; a key of your own in `podLabels`/`podAnnotations` wins). Behind a proxy every connection reaches the
  collector from the proxy, so the source-address attribution above finds no pod. An application pod that does have a
  sidecar is fine: set `k8s.pod.uid` (see above) or `k8s.pod.name` and `k8s.namespace.name` as resource attributes, which
  are matched before the address. Mesh mTLS and authorization do not cover the OTLP Service while the collector is outside
  the mesh; use `telemetry.receiver.auth` and `telemetry.receiver.tls` for that. The Service names its protocols
  (`appProtocol: grpc` / `http`, left out when the receiver terminates TLS itself) because a port called `otlp-grpc` is not
  one a mesh recognizes (https://istio.io/latest/docs/ops/configuration/traffic-management/protocol-selection/). Istio ambient
  mode is untested.
- **TLS on the receiver.** `telemetry.receiver.tls.secretName` names a `kubernetes.io/tls` Secret (`tls.crt`, `tls.key`) in
  this release's namespace, mounted into the cluster collector and served on both OTLP ports; it must be valid for
  `continuum-telemetry-cluster.<namespace>.svc`. It is re-read every hour. `telemetry.receiver.tls.clientCAKey` (a key of the
  same Secret holding the CA bundle) requires client certificates as well. Plain-text clients are refused once it is on.
- **Size of the cluster collector.** It holds a copy of every node, pod and workload of the cluster, so the node count
  decides its memory. While installing against a live cluster the chart counts the nodes and raises (never lowers)
  `telemetry.clusterCollector.resources.limits.memory`: 2Gi above 300 nodes, 3Gi above 600, 5Gi above 1000, 8Gi above 2000.
  `helm template`, Argo CD and Flux cannot count; set `telemetry.clusterCollector.autoSize.nodes` there. The collector stops accepting data when its memory reaches the limiter's refusal point and logs
  `Memory usage is above soft limit` / `Refusing data`: that line means the limit is too small for the cluster.
- **Scrape targets.** `telemetry.applicationMetrics.metrics.scrapeTargets` lists the pods the cluster collector scrapes: each
  entry has `jobName`, `namespace`, `podLabelSelector` and `port`, optionally `path` (default `/metrics`), `scheme: https` and
  `tlsInsecureSkipVerify` (off by default: it skips the check of the pod's certificate, which is rarely valid for a pod IP).
  The pod's IP is scraped on `port` (one series set per pod however many ports it declares), and pods that
  have completed are skipped. Discovering the pods needs no permission beyond the `pods` read access every collector of this
  chart already has.
- **A scope limits what is reported, not what the collectors may read.** `telemetry.scope` decides which namespaces' data
  leaves the cluster: the collectors drop everything else before export. Their Kubernetes read access (pods, events,
  workloads, nodes, namespaces) stays cluster-wide, because the components that attach pod metadata and watch cluster state
  need it, and an exclude-style scope cannot be written as per-namespace permissions at all. If a narrower grant matters,
  that is a limit of this chart today, not something the scope setting provides.
- **Container logs follow the scope.** `systemLogs` reads every container's output from the kubelet's log
  directory, so it honours `telemetry.scope` (namespaces to keep, namespaces to drop; `telemetry.scope.infra` first)
  and never reads this release's own pods. While `systemLogs` is on the host collector runs as root with every
  capability dropped: the container runtime writes each log file as `root` with mode 0640 (containerd) or 0600
  (CRI-O), so no other user can read them. It reads them from `/var/log/pods` (`systemLogs.logs.podLogsDir`, for a
  kubelet started with another `podLogsDir`), in the CRI format of containerd and CRI-O and in Docker's json-file format
  (the `container` operator detects which). Where the files under `/var/log/pods` are only symlinks - Docker through
  cri-dockerd links them into `/var/lib/docker/containers` - mount the target as well:
  `systemLogs.logs.extraHostPaths={/var/lib/docker/containers}`. k3s, RKE2, kubeadm, kind, minikube, EKS, GKE and AKS
  all use containerd or CRI-O and need nothing here. The receiver polls
  (`poll_interval: 1s`; it uses no inotify, so `fs.inotify.max_user_watches` and `max_user_instances` do not apply to
  it). It keeps its read position per file on an `emptyDir`, so a restarted container carries on where it stopped
  instead of skipping what was written meanwhile; a replaced pod (an upgrade) starts from the end of each file. When
  the destination is down or the collector is short of memory it pauses reading (`retry_on_failure`) instead of
  discarding: the lines wait in the files the kubelet keeps, up to its rotation limit (`containerLogMaxSize` 10Mi x
  `containerLogMaxFiles` 5 per container by default). Multi-line records (stack traces) arrive one line at a time; the
  `container` operator only joins lines the runtime itself split (partial lines).
- **Node journal.** `systemLogs.logs.journaling` is `none` by default. `host` runs the NODE's own `journalctl` chrooted into
  the read-only mount of the node's root filesystem (the receiver's documented "use the host's journalctl" route,
  github.com/open-telemetry/opentelemetry-collector-contrib, `receiver/journaldreceiver`): no custom image, and the journal
  is read by the systemd that wrote it. It adds the `SYS_CHROOT` capability (verified: without it the receiver fails
  to start with "operation not permitted"), needs systemd and `journalctl` at `journalctlPath` on every node it runs on
  (a node without it - Talos has no systemd - makes the collector fail to start, metrics included: scope it with
  `hostCollector.nodeSelector`). `journald` runs a `journalctl` inside the collector image, which the pinned image lacks
  (`telemetry.collectorImage` must ship one), and reads `/var/log/journal` only; a node that keeps its journal in memory
  has no such directory and the pod stays in `ContainerCreating` (`hostPath type check failed`).
- **Kubelet metrics verify the kubelet.** `kubelet_stats` dials the node's IP (`status.hostIP`, port 10250, the secure port: the
  read-only port 10255 is off by default in the kubelet, `readOnlyPort: 0`, kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/, and GKE documents moving workloads to 10250) with the pod's service account token (RBAC:
  `nodes/stats` `get`) and checks the kubelet's certificate against the cluster CA. What a kubelet serves differs by
  distribution:
  - Signed by the cluster CA, verifies out of the box: k3s (checked on a live cluster by the chart's authors), clusters whose
    kubelets use `serverTLSBootstrap` with approved CSRs (kubernetes.io/docs/reference/access-authn-authz/kubelet-tls-bootstrapping/),
    AKS (kubelet serving certificate rotation is on by default, learn.microsoft.com/azure/aks/certificate-rotation; the
    signer is not stated there - unverified). EKS, GKE, RKE2, microk8s: not verified; if the collector logs the error below, use
    one of the options.
  - A certificate of the kubelet's own, which fails verification (`x509: certificate signed by unknown authority`, in the
    collector's log with no kubelet metric arriving): kubeadm and kind by default, Talos by default (its guide enables
    `rotate-server-certificates` plus the kubelet-serving-cert-approver for that reason, docs.siderolabs.com).
  Options, in the order to prefer them: (1) make the kubelets serve CA-signed certificates (`serverTLSBootstrap: true`
  and approve the CSRs); (2) `telemetry.kubelet.ca.configMap` (or `.secret`, `.key`) naming the CA that signed them; (3) `telemetry.kubelet.viaAPIServer=true`: the API
  server reaches the kubelet for the collector (verified against the real receiver: `auth_type: kubeConfig` without a
  kubeconfig uses the pod's in-cluster credentials and calls `/api/v1/nodes/<node>/proxy/stats/summary`), so no kubelet
  certificate matters, but the token then needs `nodes/proxy` `get`, which Kubernetes documents as NOT read-only (it
  authorizes running commands in any container on the node, kubernetes.io/docs/reference/access-authn-authz/kubelet-authn-authz/),
  and every node's stats pass through the API server; (4) `telemetry.kubelet.insecureSkipVerify=true`, which sends
  the pod's token to whatever answers on that address. `telemetry.kubelet.address=nodeName` addresses the node by name. A
  corporate HTTPS proxy configured through `HTTPS_PROXY` on this pod would also catch this connection (Go sends every
  non-loopback address through it): the node's address must be in `NO_PROXY`.
- **Renewed certificates.** A client certificate (`telemetry.export...tls.mtls`) is re-read from its Secret by the
  exporter, but only when a new connection is made and at least `reload_interval` (1h) has passed since the last read:
  a connection that stays open keeps using the certificate it started with (so a renewal can take effect later than an
  hour, up to the connection's next reconnect). Running the install command again after renewing it restarts the
  collectors at once where Helm can read the cluster (not under `helm template`, Argo CD or Flux;
  `telemetry.rolloutOnSecretChange=false` for an account that may not read Secrets). A replaced destination `ca.crt` is
  read at start only and needs `kubectl rollout restart`
  ([`reload_interval`](https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/configtls/README.md) and
  [`client_ca_file_reload`](https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/configtls/README.md),
  which only exists for a server's client CA; this chart's regional operator turns it on).
- **When the destination is down.** Each exporter retries for `telemetry.export.queue.retryMaxElapsedTime` (30m)
  holding up to `telemetry.export.queue.size` requests in memory; `telemetry.export.queue.persistent.enabled` also keeps
  them on an `emptyDir` across a container restart. When the queue is full the receivers wait instead of the collector
  discarding the batch (`block_on_overflow`). `telemetry.export.queue.maxRequestBytes` (3 MiB) cuts every request to what
  the next hop accepts, which `telemetry.processors.batch` cannot do: it counts items, and 4096 log records of 2 KiB are
  8 MiB. A destination that refuses for good (a rejected token, the wrong protocol for the port) is dropped at once and
  logged at error level; retries of anything else end after the window. The cluster collector's default memory (`telemetry.clusterCollector.resources`) and a sizing guide are in
  `values.yaml`.

### Where the data goes: endpoints, proxy, DNS

- **Endpoint shape.** `telemetry.export.otlp.protocol=grpc`: `host:port`, no path (`api.example.com:443`,
  `[fd00::5]:4317`; a leading `https://`, `http://` or `dns:///` is accepted; with `http://` or `tls.insecure=true` it is
  plaintext). `protocol=http`: `host[:port]` or a base URL (`https://otlp.example.com:4318`,
  `https://gw.example.net/otlp`); the collector appends `/v1/metrics`, `/v1/logs` or `/v1/traces`, so do not write them.
  A signal route (`telemetry.export.routes.<signal>`) whose address is not `<base>/v1/<signal>` sets `fullUrl: true` and
  is posted to as written, e.g. Splunk Observability Cloud traces
  `https://ingest.<realm>.observability.splunkcloud.com/v2/trace/otlp` and metrics `.../v2/datapoint/otlp`, header
  `X-SF-Token` (the older `ingest.<realm>.signalfx.com` names were documented to work until 2026-03-24:
  [Splunk](https://help.splunk.com/en/splunk-observability-cloud/manage-data/splunk-distribution-of-the-opentelemetry-collector/get-started-with-the-splunk-distribution-of-the-opentelemetry-collector/collector-components/exporters/otlphttp-exporter)). An endpoint a collector cannot use is refused when the chart is
  rendered, with the value to change in the message; only the conventional-port check (`:4318` with grpc, `:4317` with
  http) can be switched off with `telemetry.export.checkPorts=false`.
- **Which port, which protocol (primary sources).** OTLP/gRPC is 4317 and OTLP/HTTP is 4318 by convention
  ([OTLP specification](https://opentelemetry.io/docs/specs/otlp/#otlphttp-default-port)); a hosted backend usually
  answers on 443 and says which protocol it speaks:

  | Destination | What its documentation says | Source |
  |---|---|---|
  | Grafana Cloud | OTLP over HTTP at `https://otlp-gateway-prod-<zone>.grafana.net/otlp`, Basic auth (instance ID and token); gRPC not stated, use `protocol=http` | [Grafana blog](https://grafana.com/blog/exploring-opentelemetry-collector-configurations-in-grafana-cloud-a-tasting-menu-approach/), [docs](https://grafana.com/docs/grafana-cloud/send-data/otlp/send-data-otlp/) |
  | Honeycomb | `api.honeycomb.io:443` gRPC, `https://api.honeycomb.io:443` HTTP (EU: `api.eu1.honeycomb.io`), header `x-honeycomb-team`; metrics also `x-honeycomb-dataset` | [docs](https://docs.honeycomb.io/send-data/opentelemetry/collector) |
  | Datadog | OTLP intake is HTTP only, one URL per signal (metrics `https://otlp.datadoghq.com/v1/metrics`), headers `dd-api-key`, metrics must be delta; use a route per signal; logs and traces URLs: unverified | [docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/) |
  | Elastic Cloud managed OTLP | header `Authorization: ApiKey <key>`, requests of at most 4 MB; endpoint form and port: unverified here | [docs](https://www.elastic.co/docs/reference/opentelemetry/managed-inputs/managed-otlp-endpoint) |
  | Grafana Tempo | OTLP/gRPC 4317 by default, OTLP/HTTP 4318 when configured | [docs](https://grafana.com/docs/tempo/latest/set-up-for-tracing/instrument-send/set-up-collector/otel-collector/) |
  | Grafana Loki | OTLP over HTTP only, exporter endpoint `http://<loki>:3100/otlp` | [docs](https://grafana.com/docs/loki/latest/send-data/otel/) |
  | Prometheus | `--web.enable-otlp-receiver`, HTTP only, `/api/v1/otlp/v1/metrics` (route endpoint `http://<prometheus>:9090/api/v1/otlp`, `protocol=http`) | [docs](https://prometheus.io/docs/guides/opentelemetry/) |
  | Jaeger | 4317 OTLP/gRPC, 4318 OTLP/HTTP | [docs](https://www.jaegertracing.io/docs/1.76/getting-started/) |

- **Behind a corporate proxy (`telemetry.export.proxy`).** The collectors read `HTTPS_PROXY`, `HTTP_PROXY` and
  `NO_PROXY` from their environment. OTLP/HTTP uses `HTTPS_PROXY` for an `https://` URL and `HTTP_PROXY` for an `http://`
  one; OTLP/gRPC asks the proxy for a CONNECT tunnel and uses `HTTPS_PROXY` for every destination, TLS or not, never
  `HTTP_PROXY` (measured with the pinned collector against a CONNECT proxy; grpc-go documents the variables at
  [grpc.io](https://github.com/grpc/grpc-go/blob/master/Documentation/proxy.md)). Set `httpsProxy` whenever a gRPC
  destination is behind the proxy. `NO_PROXY` always lists `localhost`, `127.0.0.1`, `::1`, `.svc`, `.cluster.local`,
  the API server (`$(KUBERNETES_SERVICE_HOST)`), the pod's own address and, on the host collector, the node's address
  (the kubelet): without those, `k8sattributes`, `k8s_cluster`, `k8sobjects` and `kubelet_stats` (client-go and the Go
  HTTP client both honour the variables) would be sent to the proxy, which cannot reach them. `noProxy` adds yours (an
  internal gateway, a domain other than `cluster.local`). Go reads `NO_PROXY` as: a name matches itself and, with a
  leading dot, its subdomains; an address or CIDR matches addresses; `*` disables the proxy. The Prometheus receiver does **not** use the
  variables for scrapes (all scraped targets here are in-cluster); its Kubernetes service discovery does, and is covered
  by `NO_PROXY`. A proxy that terminates TLS makes the destination's certificate check fail: trust its CA with
  `tls.caSecretName`. Credentials in the proxy URL belong in a Secret (`proxy.secretName`, keys `HTTPS_PROXY` and
  optionally `HTTP_PROXY`), not in the values. With `networkPolicy.telemetryEgress` on, the collectors connect only to
  the proxy, so allow its address and port.
- **Other environment.** `telemetry.extraEnv` is appended to both collectors after everything the chart sets (a variable
  named like one of its own replaces it): `SSL_CERT_FILE`, `GODEBUG`, an internal CA bundle path.
- **DNS.** A destination name with fewer dots than `ndots` (5 in a pod by default) is tried against every search domain
  first, which costs lookups per connection and, with a wildcard search domain, can resolve to the wrong host. Write the
  name with a trailing dot (`otlp.example.com.:4317`, not for a certificate that names the host without the dot) or set
  `telemetry.dnsConfig` (`options: [{name: ndots, value: "2"}]`)
  ([Kubernetes](https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/#pod-dns-config)). With NodeLocal
  DNSCache or a DNS server outside the cluster, `networkPolicy.telemetryEgress` must allow it.
- **Several replicas behind one name.** gRPC resolves a name once per connection and keeps one connection, so a ClusterIP
  Service sends everything to one pod. A headless Service with `dns:///name:4317` and a load-balancing policy of
  `round_robin` on the exporter spreads it ([gRPC](https://github.com/grpc/grpc/blob/master/doc/load-balancing.md)); this
  chart does not set the policy, so it stays on `pick_first`: unverified whether the `dns:///` form is enough on its own.
- **Idle connections.** A NAT gateway or load balancer that drops an idle connection silently leaves the exporter
  discovering it on the next send. `telemetry.export.keepalive.time` (at least `10s`) pings an idle gRPC connection; the
  destination must allow it: a stock OTLP/gRPC receiver (grpc-go) accepts a ping every 5 minutes and answers a faster one
  with `GOAWAY ... too_many_pings` (logged as `[transport] Client received GoAway with error code ENHANCE_YOUR_CALM and
  debug data equal to ASCII "too_many_pings"`; data is still delivered). This chart's regional operator accepts one every
  30s. `telemetry.export.timeout` raises the per-attempt timeout (5s gRPC, 30s HTTP) for a slow link.
- **Request size.** Every request is cut to `telemetry.export.queue.maxRequestBytes` (3 MiB): below the 4 MiB a collector's
  gRPC receiver accepts by default (`grpc: received message larger than max (6178132 vs. 4194304)` is a permanent error:
  dropped at once) and far below an OTLP/HTTP receiver's 20 MiB. A destination with a smaller limit needs a smaller value.
- **Persistent queue.** `telemetry.export.queue.persistent` writes to an `emptyDir` with `sizeLimit`; the kubelet evicts a
  pod that passes it, which also deletes the queue. A full disk shows as `write ...: no space left on device` with
  `rejected_items`: the batch is refused at the queue, not stored.

### Troubleshooting export

Where to look: `kubectl -n <namespace> logs ds/continuum-telemetry-host` and `deploy/continuum-telemetry-cluster`, filtered
with `grep -E "Exporting failed|failed to connect|createTransport"`. The lines below were produced with the pinned
collector (0.160.0) against a receiver set up to fail that way. "Will retry" lines repeat every interval while the cause
lasts; "Dropping data" means the data is gone.

| Log line (abridged) | Cause | Fix |
|---|---|---|
| `connect: connection refused` (`Exporting failed. Will retry`) | Nothing listens at that address and port, or a firewall / NetworkPolicy rejects it | Check the endpoint and port; `networkPolicy.telemetryEgress.allowedEgress` |
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
| `grpc: received message larger than max (N vs. 4194304)` (Dropping data) | A request above the receiver's 4 MiB | Keep `queue.maxRequestBytes` at or below it |
| `write ...: no space left on device` | Persistent queue's `emptyDir` is full | `queue.persistent.sizeLimit`, `queue.size`; see above |
| `Client received GoAway ... too_many_pings` | `keepalive.time` faster than the destination allows | Raise it or set the destination's policy |
| `proxyconnect tcp: ...` / `Proxy Authentication Required` | The proxy refuses or needs credentials (message text from the Go standard library: unverified with this collector) | `proxy.secretName`; allow the destination at the proxy |

A Ready pod proves only that the collector started. The agent shows each destination's state in its Diagnostics
(`telemetry.health`).

## Pod Security / platform troubleshooting

### What each pod needs, and which clusters admit it

Only the pods the intent needs exist. What each one asks of the cluster (the rows are pods; "restricted" and "baseline" are the Pod
Security profiles):

| Pod (when it exists) | Needs | restricted | baseline | Admitted by |
|---|---|---|---|---|
| cluster collector (any cluster-scoped signal) | nothing | yes | yes | every supported cluster |
| host collector, `nodeRuntime` only | nothing from the host | yes | yes | every supported cluster |
| host collector, `resourceUsage` | hostPath `/proc`, read-only (`/` too with `resourceUsage.metrics.hostFilesystem`) | no | no | namespace labelled `privileged` |
| host collector, `systemLogs` | hostPath `/var/log/pods`, root | no | no | same |
| Kepler (`energy`, bundled) | privileged, hostPID, hostPath `/proc` `/sys` | no | no | `privileged` namespace |
| dcgm-exporter (`accelerators`, bundled) | root + `SYS_ADMIN` (+ the kubelet pod-resources hostPath when a scope is set) | no | no | `privileged` namespace |
| node probe / traffic observer (opt-in) | hostPath / hostNetwork / eBPF capabilities | no | no | as above (see their own sections) |

`kubectl -n <namespace> get ds` shows `0` pods for a refused DaemonSet; `kubectl -n <namespace> describe ds <name>` shows the reason
as a `FailedCreate` event. Pod Security enforces on the pods a DaemonSet creates, not on the DaemonSet, so the install itself
always succeeds (kubernetes.io/docs/concepts/security/pod-security-admission/, "Workload resources and Pod templates").

Per platform (each statement is from the platform's documentation, cited; "unverified" is said where it is not):

- **Pod Security `baseline` / `restricted` on the namespace** (any distribution): refuses every row marked "no" above. `baseline`
  also refuses `SYS_ADMIN` (its capability allow-list does not include it), so dcgm-exporter is refused even without a scope
  (kubernetes.io/docs/concepts/security/pod-security-standards/). Label the namespace before installing, ideally a namespace of its
  own for these pods: `kubectl label namespace <namespace> pod-security.kubernetes.io/enforce=privileged --overwrite`.
- **Talos** applies `baseline` to every namespace except `kube-system` by default, and **RKE2** with a `cis` profile applies
  `restricted` to every namespace except a few system ones (talos.dev "Pod Security"; docs.rke2.io/security/hardening_guide). Both
  are cluster-wide defaults, not namespace labels, so the pre-flight below cannot see them: label the namespace `privileged`
  first (a namespace label overrides the default). RKE2's CIS profile also sets `automountServiceAccountToken: false` on each
  namespace's `default` ServiceAccount; each telemetry pod runs under a ServiceAccount of its own, so that is not in its way.
- **AKS** with Deployment Safeguards (always on in AKS Automatic) requires every container to have CPU and memory requests and **both**
  a liveness and a readiness probe, forbids the `latest` tag, and with the baseline Pod Security Standards (also on in Automatic)
  refuses host access like any other cluster (learn.microsoft.com/azure/aks/deployment-safeguards). Every container this chart
  creates for telemetry has requests, both probes and a pinned tag. In `Enforce` mode the safeguards also raise requests below
  100m CPU / 100Mi memory to those minimums, so the small collector requests are not what runs. Exclude the namespace from the
  safeguard (and label it `privileged`) for Kepler and dcgm-exporter.
- **Kyverno / Gatekeeper** policies of the usual kinds (no privileged, drop ALL, no privilege escalation, run as non-root, require
  requests/limits, disallow `latest`, read-only root filesystem): the collectors, the agent and the regional operator satisfy
  all of them; Kepler (privileged), dcgm-exporter (root, `SYS_ADMIN`, writable root filesystem) and the host collector with
  `systemLogs` (root, because the runtime creates container log files as root) are the documented exceptions, so exempt those
  namespaces or pods. The chart's tests check these properties on every container.

### Checked at install time

`preflight.podSecurity` (default `fail`) reads the release namespace's `pod-security.kubernetes.io/enforce` label with Helm's `lookup`
when you `helm install`/`upgrade`. If it says `baseline` or `restricted` and the release needs host access, the install stops
before anything is created and prints the exact `kubectl label` command; `warn` installs and puts the same text in NOTES; `off` is silent.
Limits, all safe: `helm template`, Argo CD and Flux cannot look at the cluster (declare the level with `preflight.assumeEnforce=restricted`
to get the same check offline), a namespace that does not exist yet (`--create-namespace`) has no label, and cluster-wide defaults (Talos,
RKE2 CIS; any admission configuration set on the API server) are not labels. An account that may not `get` Namespaces makes Helm itself fail with "namespaces ... is forbidden":
set `preflight.namespaceLookup=false` then.

### Network policies and the CNI

`networkPolicy.telemetryEgress` (off by default) is a plain `NetworkPolicy`; what it can express is the same on every CNI, what the CNI
*matches* is not (kubernetes.io/docs/concepts/services-networking/network-policies/: for an `ipBlock`, "it is not defined whether
[Service IP rewriting] happens before or after NetworkPolicy processing"; "node specific policies ... you cannot target nodes by
[name]"):

- **DNS** is always allowed to kube-dns/CoreDNS in `kube-system`, and to `169.254.20.10`, the address NodeLocal DNSCache uses in
  its upstream manifest and on GKE. NodeLocal DNSCache runs as host-network pods that no pod selector matches
  (kubernetes.io/docs/tasks/administer-cluster/nodelocaldns/); with it on a different address, or with kube-proxy in iptables mode (where the
  cache also answers for the kube-dns ClusterIP on the node), list that address in `networkPolicy.telemetryEgress.dnsCIDRs`.
- **The API server**: whether a policy engine sees the `kubernetes` Service's ClusterIP or the control-plane address it is rewritten to
  differs by CNI and Service implementation, and Kubernetes leaves it undefined (the page quoted above), so list the control-plane node
  addresses (`kubectl get endpoints kubernetes`) in `apiServerCIDRs` and the ClusterIP as well. 
- **The kubelet** (`NODE_IP:10250`): a NetworkPolicy cannot say "the node this pod runs on", so the rule allows port 10250 at
  `kubeletCIDRs` (default: any address). An export destination or regional operator that is a pod in the cluster goes in `extraEgress` as a
  `namespaceSelector`/`podSelector` peer, not a CIDR.
- **k3s's embedded kube-router controller** (exercised live on k3s with the lists above) and **Calico** (unverified here) have no node
  or API-server entity to name: the address lists above are the whole configuration, so there is no preset for them.
- The agent reads each collector's counters on `telemetry.health.port` through headless Services (pod addresses, no ClusterIP), and the
  cluster collector scrapes Kepler and dcgm-exporter by pod address, so neither depends on how a CNI treats Service addresses. The kubelet's
  own readiness probes are never blocked by a NetworkPolicy ("Pods cannot ... block access from their resident node").

### Service meshes

The four telemetry pods are labelled `sidecar.istio.io/inject: "false"` and annotated `linkerd.io/inject: disabled` (`mesh.telemetryInjection`,
default `disabled`; `inherit` changes nothing). In a namespace that injects every pod, Istio's init container needs `NET_ADMIN`, which a
namespace enforcing Pod Security baseline/restricted refuses, the collectors would export before the proxy is ready, and a mesh with
`REGISTRY_ONLY` outbound policy would block an OTLP destination without a ServiceEntry (istio.io/latest/docs/setup/additional-setup/sidecar-injection/;
linkerd.io/2/features/proxy-injection/). Applications in the mesh push OTLP to the cluster collector's Service as to any unmeshed server.

### Images, registries, quotas

`global.imageRegistry` replaces the registry host of all four images (the agent image, the OpenTelemetry collector, Kepler, dcgm-exporter)
and keeps the rest of each path, e.g. `registry.corp/sustainable_computing_io/kepler`; mirror the images there and add `imagePullSecrets`.
No pod has an init container or a sidecar image. Every container has CPU and memory requests and a memory limit (no CPU limit, which only
throttles a collector), so a ResourceQuota on requests is satisfied; one that also quotas `limits.cpu`, or a LimitRange with a maximum under
a pod's memory limit (collector 1Gi, Kepler 1Gi), needs `telemetry.*.resources` set to fit. A PodDisruptionBudget is not rendered: the DaemonSets do
not need one and the single cluster collector would block every node drain with one.

### What the host collector can see of the node

Only what the signals asked for. `nodeRuntime` mounts nothing from the node. `systemLogs` mounts the pod log directory
(read-only, as root: the runtime writes those files as root). `resourceUsage` mounts the node's `/proc` read-only, which is all
node CPU and memory need (checked with the real collector: same metrics, no errors); per-pod and node filesystem usage comes
from the kubelet. `telemetry.resourceUsage.metrics.hostFilesystem=true` also mounts the node's whole root read-only for the
usage of every mounted filesystem; `systemLogs.logs.journaling=host` mounts it for the node's own `journalctl`. Kepler
(privileged, host PID, `/proc` and `/sys`) and dcgm-exporter (root with `SYS_ADMIN`) keep what their upstream manifests ask for:
a way to drop either was not found in upstream documentation (dcgm-exporter's profiling fields `DCGM_FI_PROF_*` need
`SYS_ADMIN`; whether they are in its default counter set was not confirmed without a GPU), so those two are the signals to
leave off, or to point at an exporter you already run (`source=existing`), where the privilege is not wanted.

### Sizing the host collector

`telemetry.hostCollector.resources` defaults to a 128Mi request and a 512Mi memory limit. Measured with the real collector
(0.160.0) on 200 pods' log files and a fake kubelet of 110 pods: ~50 MiB of memory idle, ~95 MiB at about 19,000 log lines/s,
~195 MiB with the destination down and the same load arriving (memory_limiter and the queue hold the backlog) - more than
fits in the shared 256Mi default. There is no CPU limit (a limit throttles a log reader exactly when the node is busy).
`memory_limiter` percentages are of the container's cgroup limit (v1 and v2 are both read); with no memory limit it falls back
to the node's total memory and protects nothing, so keep a limit. Other mechanics: the DaemonSet tolerates every taint
(control-plane nodes included), selects Linux nodes (`kubernetes.io/os`; Windows nodes are skipped), updates 10% of the
nodes at a time, and uses no host network or host port (its only port, the readiness probe, is the pod's own). For a
`priorityClassName` the cluster must have the PriorityClass: the built-in `system-node-critical` is restricted to
`kube-system` by some platforms (unverified here), so create one of your own if nodes run full.
The node's root is mounted read-only at `/hostfs` without mount propagation, so a filesystem the node mounts after the pod started
is not seen until the pod is replaced; `telemetry.hostCollector.rootMountPropagation=HostToContainer` follows later mounts, but the
container runtime refuses to create the container where the node's root mount is not shared or slave (`path / is mounted on / but
it is not a shared or slave mount`; seen on a micro-VM node), so it is opt-in.

Additionally, Kepler (energy) runs privileged with `hostPID`, one pod per Linux node, under its own ServiceAccount
`continuum-agent-kepler` whose only permission is to get/list/watch pods cluster-wide (Kepler reads pod metadata from the API
to attribute energy to pods; without it every container is reported as `system_processes`). dcgm-exporter (accelerators)
runs as root with `SYS_ADMIN`, one pod per node labelled `nvidia.com/gpu.present=true` (set by the NVIDIA GPU Operator or GPU
feature discovery; label GPU nodes yourself otherwise) and needs no API access. It sees the GPUs only if the NVIDIA
container runtime starts it: where that is not the node's default runtime (k3s, for one) set
`telemetry.accelerators.metrics.runtimeClassName=nvidia`.

### Energy (Kepler): which engine, and where it can run

Kepler needs hardware or kernel features a node may not offer, so the chart does not pretend every node can run it.
`telemetry.energy.metrics.engine` selects one of the two upstream lines
([project](https://github.com/sustainable-computing-io/kepler)):

| | `ebpf` (default) | `powercap` |
|---|---|---|
| Kepler | release-0.7.12 (legacy, frozen upstream: "no bug fixes or feature requests" since the 0.10 rewrite) | v0.12.0 (current, maintained) |
| Reads | eBPF programs + perf counters; RAPL where present, otherwise an estimate from CPU data and pre-trained weights in the image | RAPL (`/sys/class/powercap`) or hwmon power sensors; nothing else, no estimate |
| Cloud VMs (EKS, GKE, AKS) | produces (estimated) data if the node kernel accepts eBPF tracing programs | nothing to read: Kepler exits ("failed to create CPU power meter") |
| Bare metal | RAPL, plus eBPF attribution | RAPL |
| Architectures | amd64, arm64 | amd64 only (upstream publishes one platform for v0.10 - v0.12); the chart adds `kubernetes.io/arch: amd64` |
| Metrics | `kepler_container_joules_total{pod_name,container_namespace,container_name,mode}`, `kepler_node_*_joules_total` | `kepler_node_cpu_watts`, `kepler_pod_cpu_joules_total{pod_name,pod_namespace,zone}` (per-container series carry only a `pod_id`) |

Switching engine changes the metric names. 

**A node Kepler cannot work on is not a failure.** With `telemetry.energy.metrics.nodeChecks` (default true) every Kepler pod
starts with a `preflight` init container: it asks the node (`ebpf`: can the kernel load eBPF tracing programs; `powercap`: is
there a RAPL or hwmon power source) and, on a definite no, prints why and waits. The pod then shows `Init:0/1` with no
restarts, and the cluster collector does not scrape it. Read the reason:

```
kubectl -n <namespace> get pods -l app.kubernetes.io/component=telemetry-kepler -o wide
kubectl -n <namespace> logs <pod> -c preflight
```

Held pods are not Ready, so the DaemonSet reports fewer ready pods than desired and `helm install --wait` (or a GitOps
health check) waits for all of them: restrict Kepler to the node pools that can run it (`nodeSelector`) if you use either. Its
rolling update allows every pod to be replaced at once (`maxUnavailable: 100%`), because the DaemonSet controller counts each
unavailable new pod against that budget and held pods would otherwise stall the upgrade on the healthy nodes.

A Kepler pod that is `Running` but restarting is a different thing - a Kepler that is broken - and its own log
(`logs <pod> -c kepler`) says why. Without the preflight, release-0.7 on a kernel that refuses eBPF tracing programs dies with
`panic: runtime error: invalid memory address or nil pointer dereference` (the real error is lost: it calls `Detach` on a
half-built exporter), whatever the flags; no environment variable or flag of release-0.7 skips the eBPF load. Missing perf
counters alone do not cause that panic: by its source, release-0.7 carries on without them and disables its hardware-counter metrics. A
startup probe (also `nodeChecks`) keeps a pod from becoming Ready until `/metrics` really serves Kepler's node series.

Pods are placed by `telemetry.energy.metrics.nodeSelector` and `tolerations`; to run the `powercap` engine only on
bare-metal pools, label them yourself and select the label there. With a scope set (`telemetry.scope.namespaces`, `exclude` or
`workloads`) the energy data follows it (`telemetry.energy.metrics.applyScope: auto`, the default): the filter reads each series'
namespace label, node-level series always pass, and the energy of pods in namespaces outside the scope is not reported. With no
scope nothing is filtered. `applyScope: false` reports every namespace of a node whatever the scope.

On Talos the default Pod Security profile is `baseline` for every namespace but
`kube-system`: label the namespace `privileged` as above, and a RAPL machine needs the `intel_rapl_common`/`intel_rapl_msr`
modules loaded (`machine.kernel.modules`) for the `powercap` engine.

### GPU metrics (`telemetry.accelerators`)

GPU utilisation, memory, temperature, power and (when the data follows a scope, see `applyScope` below) the pod using each GPU come from NVIDIA's
[dcgm-exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html). The chart either
deploys it (`source: bundle-dcgm`, the default once `accelerators` is on) or scrapes one that already runs
(`source: existing`). With no GPU anywhere the signal is a quiet no-op: the DaemonSet has no pod to start and the collector
has nothing to scrape. Pick the source by what the cluster already has:

| The cluster has | Use |
| --- | --- |
| NVIDIA GPU Operator (its `nvidia-dcgm-exporter` DaemonSet, `app=nvidia-dcgm-exporter`, namespace `gpu-operator`, port 9400) | `source=existing`, `existing.pods.labelSelector=app=nvidia-dcgm-exporter` |
| GPU nodes with the NVIDIA driver and container toolkit, no exporter | `source=bundle-dcgm` (default) |
| GKE with managed DCGM metrics, AKS managed GPU node pools, or any exporter of your own | `source=existing` and the endpoint, or `existing.pods` |
| No GPUs | leave `accelerators` off |

```
# GPU Operator: no second exporter, every node's exporter scraped on its own
--set telemetry.accelerators.metrics.enabled=true --set telemetry.accelerators.metrics.source=existing \
--set telemetry.accelerators.metrics.existing.pods.labelSelector=app=nvidia-dcgm-exporter
```

Do not point `existing.prometheusEndpoint` at the operator's Service (`nvidia-dcgm-exporter.<ns>.svc:9400`): it is one ClusterIP
in front of one pod per node, so a scrape sees whichever node's pod the proxy picked and the other nodes' GPUs are missing
from it. `existing.pods` gives each pod its own scrape target. The cluster collector can list pods cluster-wide already (its
k8sattributes processor needs that), so no extra permission is involved; with `networkPolicy.telemetryEgress` on, the scrape is
allowed to the namespace you name (or any namespace when you name none).

What the bundled exporter needs, and what you see when it is missing (`kubectl -n <namespace> get ds continuum-telemetry-dcgm`,
then `logs ds/continuum-telemetry-dcgm`):

| Symptom | Cause | Fix |
| --- | --- | --- |
| `DESIRED 0`, no pod | no node carries a GPU label the DaemonSet looks for (below) | `kubectl label node <node> nvidia.com/gpu.present=true`, or set `nodeSelector` / `affinity` |
| pods `Pending`, `untolerated taint` | a node taint the pod does not tolerate | it tolerates every taint by default; if you narrowed `tolerations`, add the GPU pool's taint |
| `CrashLoopBackOff`, log `Failed to initialize embedded DCGM` (with `libdcgm.so.4 not found` or a DCGM status hint) | the container got no NVIDIA driver libraries: no driver on the node, or the NVIDIA runtime did not start the pod | install driver and [container toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html); where it is not the default runtime (k3s, RKE2) set `runtimeClassName=nvidia` |
| DaemonSet `FailedCreate`, `RuntimeClass "nvidia" not found` | `runtimeClassName` set but the RuntimeClass does not exist | `kubectl get runtimeclass`; create it (handler `nvidia`) or use the name your cluster has |
| DaemonSet `FailedCreate`, `violates PodSecurity` | host access refused (Pod Security `baseline`/`restricted`) | see "Pod Security" above |
| pod Running, log shows no GPU or a permission error | the container's label cannot read the driver devices or `/var/lib/kubelet/pod-resources` | `telemetry.accelerators.metrics.privileged=true` (what the GPU Operator's own exporter pod does) |
| pod Ready, no `namespace`/`pod` labels, or all data gone under a namespace scope | the data follows a scope (`applyScope`) but the kubelet socket cannot be read (a kubelet root other than `/var/lib/kubelet`, or a runtime that confines the container) | `privileged=true`, or see below |

`dcgm-exporter` exits at startup when DCGM cannot initialise; its `/health` only says the HTTP server is up. That is why a node
without a usable GPU stack shows `CrashLoopBackOff` and not a Ready pod with no data. The image
(`nvcr.io/nvidia/k8s/dcgm-exporter`, linux/amd64 and linux/arm64, no pull secret) runs as root with the `SYS_ADMIN` capability,
which NVIDIA's own chart also uses because DCGM profiling metrics (`DCGM_FI_PROF_*`) need it; it needs no network at all (with
`networkPolicy.telemetryEgress` on it gets a policy that allows no egress), no token and no RBAC.

Where it runs: any node announcing an NVIDIA GPU by `nvidia.com/gpu.present=true` (GPU Operator), `nvidia.com/gpu.count` (GPU
Feature Discovery), the Node Feature Discovery NVIDIA PCI labels `feature.node.kubernetes.io/pci-10de.present`,
`pci-0302_10de.present`, `pci-0300_10de.present`, or `cloud.google.com/gke-accelerator` (GKE). A `nodeSelector` or `affinity` of
yours replaces that list (`nodeSelector: {kubernetes.io/os: linux}` runs it on every Linux node, GPU or not; then non-GPU
nodes crash-loop). GPU Feature Discovery alone does not set `nvidia.com/gpu.present`.

Per platform (every claim cites its source; "unverified" means it could not be checked without a GPU cluster):

- **GPU Operator** (any distribution): `source=existing` as above. If you do run the bundled exporter next to it, the operator
  keeps its own running; two exporters read the same GPUs, and DCGM documents its profiling counters failing with "resource is in use" while another profiler holds them
  ([DCGM profiling](https://docs.nvidia.com/datacenter/dcgm/latest/learn/modules/profiling.html); whether two exporters collide
  is unverified), so prefer one.
- **k3s** with the toolkit installed by hand: k3s adds the `nvidia` runtime to containerd when it finds the binaries but leaves
  `runc` the default, so set `runtimeClassName=nvidia` ([docs.k3s.io/advanced](https://docs.k3s.io/advanced), "NVIDIA Container
  Runtime"; the RuntimeClass object must exist) and label the nodes (`nvidia.com/gpu.present=true`) unless GPU Feature Discovery
  or the GPU Operator does.
- **RKE2**: with the GPU Operator, [RKE2's own page](https://docs.rke2.io/add-ons/gpu_operators) configures the toolkit with
  `ACCEPT_NVIDIA_VISIBLE_DEVICES_ENVVAR_WHEN_UNPRIVILEGED=false` and notes `runtimeClassName: nvidia` is needed only for operator
  v25.3.x. A toolkit set not to honour `NVIDIA_VISIBLE_DEVICES` from unprivileged containers (what that option's name says; not
  verified on a GPU) gives this image, which asks for its GPUs that way, none, so use the operator's exporter
  (`source=existing`), or `privileged=true` here.
- **kubeadm, kind, minikube, microk8s, Talos**: the same prerequisites (driver, toolkit, default runtime or `runtimeClassName`)
  and, unless the GPU Operator or GFD labels the nodes, a node label. kind/minikube need GPU passthrough into the node and Talos
  its NVIDIA system extensions; neither was checked here.
- **EKS**: the EKS-optimized AL2023 and Bottlerocket NVIDIA AMIs include the driver and the container toolkit (Bottlerocket the
  device plugin too; AL2023 needs one installed) ([docs.aws.amazon.com/eks/latest/userguide/ml-eks-optimized-ami.html](https://docs.aws.amazon.com/eks/latest/userguide/ml-eks-optimized-ami.html)).
  The page names no node label, so label the GPU nodes (or run GFD) for the bundled exporter; whether the toolkit is containerd's
  default runtime on those AMIs is unverified, so if the pod crash-loops with `Failed to initialize embedded DCGM` set
  `runtimeClassName=nvidia` (if a RuntimeClass of that name exists).
- **GKE Standard**: GPU nodes are labelled `cloud.google.com/gke-accelerator` and, when the cluster also has non-GPU pools,
  tainted `nvidia.com/gpu=present:NoSchedule` (both tolerated by default)
  ([cloud.google.com/kubernetes-engine/docs/how-to/gpus](https://cloud.google.com/kubernetes-engine/docs/how-to/gpus)). GKE keeps
  the driver in `/home/kubernetes/bin/nvidia`, with no NVIDIA container runtime: Google's own self-managed exporter manifest
  mounts that directory at `/usr/local/nvidia`, sets `LD_LIBRARY_PATH=/usr/local/nvidia/lib64` and
  `DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE=device-name`, and runs privileged
  ([cloud.google.com/stackdriver/docs/managed-prometheus/exporters/nvidia-dcgm](https://cloud.google.com/stackdriver/docs/managed-prometheus/exporters/nvidia-dcgm)).
  The equivalent here (unverified on a cluster; Google's runs a separate `nv-hostengine`, this one uses the exporter's embedded
  engine) is `privileged=true`, `hostMounts=[{hostPath: /home/kubernetes/bin/nvidia, mountPath: /usr/local/nvidia}]` and
  `extraEnv` with those two variables. Better, when GKE's managed DCGM is enabled (`--monitoring=SYSTEM,DCGM`), scrape that
  exporter with `source=existing`; do not run both ("duplicate or incorrect metrics" per the same docs).
- **AKS**: self-managed GPU pools use the taint `sku=gpu:NoSchedule` by the docs' example (tolerated by default) and are labelled
  `accelerator=nvidia` in the docs' sample; whether AKS sets that label itself is unverified, so rely on GFD or label them.
  AKS-managed GPU node pools (preview) install their own DCGM exporter, on port 19400 and node label
  `kubernetes.azure.com/dcgm-exporter=enabled`
  ([learn.microsoft.com/azure/aks/aks-managed-gpu-nodes](https://learn.microsoft.com/en-us/azure/aks/aks-managed-gpu-nodes)): not wired
  here (how it is reached per node is unverified); do not bundle a second one.
- **MIG, time-slicing, MPS, vGPU**: MIG instances are reported by default (`-d f`). For time-sliced or MPS GPUs set
  `extraEnv=[{name: KUBERNETES_VIRTUAL_GPUS, value: "true"}]` to name the pods sharing a GPU
  ([dcgm-exporter command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html)).

`applyScope` (`auto` by default: on exactly when the intent defines a scope; `true`/`false` force it) is what lets `telemetry.scope.namespaces/exclude` reach GPU data, so a scoped install does not report other namespaces' GPU use. dcgm-exporter names the pod using
a GPU in the labels `namespace`, `pod` and `container` of each data point (`--kubernetes`; `pod_name`, `pod_namespace`,
`container_name` only with its legacy `--use-old-namespace`, which this chart never sets). The collector keeps those labels on
the data point, takes the exporter's own pod identity (and its DaemonSet's name) off the resource, and filters on `namespace`. A
GPU that no pod holds has no namespace, so a namespaces allow-list drops it. With `source=existing` the exporter you point at
must already map pods (the GPU Operator's does: `DCGM_EXPORTER_KUBERNETES=true`).

### Readiness and what to check when a signal is silent

Every collector pod answers the kubelet on port 13133 (`health_check`): it is Ready once its pipelines are running, and
restarted if it stops answering. That says the collector *started*, not that data is arriving - for that read the agent's
export health (`telemetry.health`) or the collector's own log:

```
kubectl -n <namespace> get pods -l app.kubernetes.io/part-of=continuum
kubectl -n <namespace> logs ds/continuum-telemetry-host | grep -iE "error|denied|refused"
kubectl -n <namespace> logs deploy/continuum-telemetry-cluster | grep -iE "error|forbidden|refused"
```

# Deferred work: local-operator and operator deep review

Findings from the multi-agent review of the local operator (telemetry extractors), the regional operator and the
printed commands that were deliberately not changed. Merge into `deferred-work.md` when convenient.

## Needs a design decision

- **Declared-vs-effective drift detection for the local operator.** The server records what an intent declares; the
  agent only reports signal names and export counters. Nothing compares the two, so a hand-edited release that
  diverges from the intent is invisible.
- **Refuse versus warn on a bad "Reachable at".** The address check now warns (ClusterIP, NodePort, 4318, private or
  CGNAT address); it never refuses. Whether an unreachable-looking address should block saving is a product call.
- **Root host collector when system logs are on.** Container log files are root-only (0640/0600), so the host collector
  runs as root with all capabilities dropped when `systemLogs` is enabled. A narrower path (a log-group, a sidecar
  reader) was not found for stock kubelets.
- **Docker (cri-dockerd) log nodes.** Container logs are symlinks into `/var/lib/docker/containers`, which is not
  mounted; nodes using cri-dockerd produce no container logs.
- **Scope spoofing.** Namespace identity comes from the pod that sent the data (k8sattributes by source address); an
  application can still claim another namespace in its own payload. Records k8sattributes cannot attribute (hostNetwork
  pods) pass an exclude-only scope.
- **`networkLatency` has no emitter** in the Go code; enabling it re-emits nothing.
- **Redaction pattern `.*token.*`** is broader than intended and also redacts attributes such as `token_count`.
- **Replicas > 1 on the regional operator** need a rollout strategy decision (RollingUpdate plus PDB are rendered;
  per-replica queues are not shared).

## Not changed, could be better

- Heartbeats only prove the operator is alive; the agent's `exporthealth` reads send_failed/sent counters only
  (`queue_size` would show a stalled queue earlier).
- gRPC `max_recv_msg_size` is not set on the regional operator receiver.
- Collector `health_check` cannot show exporter failure (verified); the chart turns `selfMetrics` on for the regional
  operator instead.
- The resource stamps `continuum.org.id` / `continuum.cluster.id` can be empty until the install command sets them.
- Server: quoting in `installCommand`/`upgradeCommand` for unusual names, CGNAT range handling, duplicate
  `OperatorUsages` listing.

## Found while running the charts on a real cluster (k3s)

- **Collector component aliases.** `hostmetrics`, `filelog`, `k8sattributes`, `k8sobjects` and the `otlp` exporter are
  deprecated aliases in collector 0.160 (a warning at start-up). They work, and the chart pins 0.160.0, but a future
  collector release will remove them; moving to `host_metrics`, `file_log`, `k8s_attributes`, `k8s_objects`, `otlp_grpc`
  needs a decision about older `collectorImage` overrides.
- **Pod Security** is now checked at install (`preflight.podSecurity`, see round two below); the telemetry wizard in the
  UI still does not mention it before the command is generated.
- **Kepler needs perf/eBPF.** Handled in round two: a node pre-flight holds the pod with a reason instead of crash-looping.
- **One agent release per cluster.** Cluster-scoped objects are named `continuum-agent*`, so a second release in another
  namespace is refused by Helm.
- **NetworkPolicy and ClusterIP destinations.** Enforcement of `ipBlock` against a Service's ClusterIP differs between
  CNIs (some match after DNAT, so the pod IP is what is compared); `allowedEgress` for an in-cluster operator may need
  the pod CIDR rather than the Service address.

## Supported clusters, and what was left out

Supported: k3s (tested live) and, expected to work but not yet run live, kubeadm, kind, minikube and the managed EKS, GKE
and AKS services with ordinary nodes. Not supported by the chart: OpenShift/OKD (security context constraints, no fixed
uid, an SELinux type for host-mounting pods, ClusterResourceQuota metrics, OpenShift DNS), GKE Autopilot and EKS Fargate
(the host collector is kept off Fargate nodes by an affinity), the Cilium network-policy preset, IPv6-only and dual-stack
clusters (a bracketed kubelet address, dual-stack Service, `::/0` kubelet rule, IPv6 pod-IP scrape rules), and
SELinux-enforcing nodes. That code was written and tested in the render, but never against such a cluster; it is kept as a
separate patch outside the repository and can be reapplied when a platform is taken on and tested for real.

## Round two: robustness across distributions (decisions and what stays unverified)

Defaults chosen, each reversible by a value:

- **Kepler engine** stays the legacy `ebpf` line (release-0.7.12); `engine: powercap` (v0.12.0, RAPL only, amd64 only)
  is opt-in. `nodeChecks` (default true) puts a privileged `preflight` init container on every Kepler pod: on a node
  whose kernel refuses eBPF tracing programs the pod waits in `Init:0/1` with the reason in its log. Held pods are not
  Ready, so `helm install --wait` waits for them; the DaemonSet therefore uses `maxUnavailable: 100%`.
- **Mesh sidecar opt-out** is on for all four telemetry pods (`mesh.telemetryInjection: disabled`); `inherit` changes nothing.
- **`preflight.podSecurity` defaults to `fail`**: the install stops, with the exact `kubectl label` command, when the
  namespace's Pod Security label would refuse the host collector or Kepler. Cluster-wide defaults (Talos, RKE2 CIS) are not
  labels and cannot be seen; `preflight.assumeEnforce` declares them for `helm template`.
- **Export checks** (`telemetry.export.checkPorts`, default true) turn protocol/port mismatches (4317 on http, 4318 on
  grpc, and similar) into render failures. The regional operator chart now fails the same way where it only warned; its
  config checksum changes, so one restart happens on upgrade.
- **HTTP(S) proxy** (`telemetry.export.proxy`) always keeps the API server, kubelet and in-cluster names in `NO_PROXY`.
- **Node root mount propagation** is off by default (`telemetry.hostCollector.rootMountPropagation`): `HostToContainer`
  makes the runtime refuse to create the container on a node whose root mount is private (found on the test cluster).
- **`resourceUsage` mounts only the node's `/proc`**; the whole root (`resourceUsage.metrics.hostFilesystem`) is opt-in, and
  `system.filesystem.*` series are absent by default (the kubelet's node and pod filesystem series remain).
  Kepler (privileged) and dcgm-exporter (`SYS_ADMIN`) were not narrowed: no documented way found; dcgm's default counters
  were not checked on GPU hardware.
- **Energy and GPU data follow the scope** (`applyScope: auto`): with a namespaces/exclude/workloads scope set, series of pods
  in other namespaces are dropped; node-level series pass. With no scope nothing changes. For GPU this mounts the kubelet
  pod-resources socket (read-only) into dcgm-exporter, and an `existing` exporter without pod mapping then loses all its GPU
  data under a namespaces allow-list. Checked live with a stand-in Kepler (three namespaces, scope one); dcgm not checked on a GPU.
- **Memory of the cluster collector** scales with node count (`clusterCollector.autoSize`); resource requests of the host
  collector, Kepler and dcgm-exporter are their own defaults.

Not verified on a live cluster (documented as such in the chart README, with the primary source cited):

- kubeadm, kind and minikube (expected to work; their kubelet certificates need `telemetry.kubelet.ca`, `viaAPIServer` or
  `insecureSkipVerify`), managed EKS/GKE/AKS with ordinary nodes, `journaling: host`, a proxy in the path, the GPU
  Operator's existing exporter pods, real Kepler on bare metal (either engine), dcgm-exporter on a GPU node, multi-node
  clusters.
- Verified on the single-node k3s: delivery of every non-GPU, non-Kepler extractor to the destination with the merged
  chart, upgrade from the previous revision with `--reset-then-reuse-values`, scrape/egress NetworkPolicies,
  Pod Security pre-flight refusing a `baseline` namespace, Kepler pre-flight holding a pod on a kernel without eBPF
  tracing, container logs with `systemLogs`.

Left alone on purpose:

- One agent release per cluster (cluster-scoped names), the deprecated collector component aliases, the `.*token.*`
  redaction pattern, and the items in the earlier sections.

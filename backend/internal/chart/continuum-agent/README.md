# continuum-agent

Read-only discovery agent for Continuum. One Deployment, one image, three optional roles (`agent` always
runs; `probe` and `flow` are opt-in DaemonSets). Dials out to the server named in `server.address` — nothing
in the cluster it watches needs to be reachable from outside.

This file is the chart's own reference material: everything that doesn't change between installs. What
*this particular release* has turned on is printed by `helm install`/`helm upgrade` itself (and again any
time with `helm get notes <release> -n <namespace>`); this file explains what each of those lines means and
the commands you'd only need occasionally, not at every install.

## What it can see, by access tier

- **Tier 0, "registered only"**: the UID of the `kube-system` namespace, which is how Continuum tells
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

Always upgrade with `--reuse-values`, or every setting you gave at install falls back to the chart's default
(the tier defaults to 2, "services", the widest):

```
helm upgrade <release> <chart> -n <namespace> --reuse-values --set access.tier=1
```

The cluster owner decides this; the server can approve up to whatever tier is installed and never beyond
it. **Narrowing the approved tier in the Continuum UI does not shrink what is granted in the cluster** — that
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

Continuum's server keeps its record of this cluster until you remove it on the Discovery page — uninstalling
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

Independent of the access tier above, on its own ServiceAccount, and never reported to the Continuum server
itself — it goes straight to whatever OTLP endpoint `telemetry.export.otlp.endpoint` names. The host
collector (for signals that can only be observed per-node) mounts several host paths read-only (kubelet
stats, container/journal logs).

## Pod Security / platform troubleshooting

The node probe, traffic observer and host telemetry collector are each refused outright by Pod Security
`baseline`/`restricted` and by GKE Autopilot/EKS Fargate (neither of those platforms runs DaemonSets or
hostPath volumes at all). If a pod from any of them is refused with a `hostPath` or `PodSecurity` error,
label the namespace privileged:

```
kubectl label namespace <namespace> pod-security.kubernetes.io/enforce=privileged --overwrite
```

On OpenShift, grant the telemetry ServiceAccount the privileged SCC instead (SCCs, not Pod Security labels,
gate this there):

```
oc adm policy add-scc-to-user privileged -z <release>-telemetry -n <namespace>
```

On GKE Autopilot or EKS Fargate, none of the above will ever schedule. Point the affected signal(s) at a
Kepler/dcgm-exporter/collector you already run elsewhere instead (`telemetry.energy.metrics.source=existing`,
`telemetry.accelerators.metrics.source=existing`), or collect only cluster-scoped signals
(`kubernetesState`, `applicationMetrics`, `applicationLogs`, `traces`), which need none of this.

Additionally, Kepler (energy) runs privileged, one pod per Linux/amd64 node (the upstream image has no arm64
build; arm64 nodes are skipped by `nodeSelector` rather than crash-looping), and dcgm-exporter (accelerators)
runs as root with `SYS_ADMIN`, one pod per node labelled `nvidia.com/gpu.present=true`.

---
id: permissions-reference
title: Permissions reference
description: Exactly what Kubernetes RBAC, Linux capabilities and host access each Continuum workload asks for, scope by scope — the agent's access tiers, its optional telemetry extractors, and the regional operator (which needs none of this).
---

# Permissions reference

This page answers one question precisely, with citations into the chart templates themselves so it can't
quietly drift from what actually gets applied: *what does each thing Continuum deploys actually ask the
cluster for, and when.* [Trust model](./trust-model.md) is the narrative version of "should I trust this
with my infrastructure"; this page is the itemized receipt. Re-render with `helm template` after changing
any `values.yaml` flag named below to see the exact permission set a given configuration holds — the same
check this repository's own `agent_rbac_grants_test.go` runs in CI against the frontend's own "what this
grants" disclosure, so the UI text and this page should never say something the chart doesn't actually do.

## The discovery agent

One `ServiceAccount` per install, bound through several separate, additive grants rather than one broad
role. Nothing below is ever combined into a single ClusterRole — each piece is scoped to exactly the
resource it needs, in the namespace it needs it in.

**Its own identity.** A `Role` + `RoleBinding`, in the agent's own namespace, naming exactly one Secret by
`resourceNames` — `get`, `update`, `patch`. The agent cannot read any other Secret in the cluster and
cannot create a new one; this is also the *only* write verb anywhere in the entire agent RBAC surface.

**Resolving the real API address** (on by default — `access.resolveApiEndpoint`). A second, separate
`Role` + `RoleBinding`, in the fixed `default` namespace (every cluster has one), naming exactly the
`kubernetes` Endpoints object — `get` only. This exists purely so the agent can learn the control plane's
actual address instead of the pod-local ClusterIP; turning it off just means the agent uses the ClusterIP
instead, nothing else changes.

**Discovery, by access tier.** One `ClusterRole` per implemented tier (`t0`, `t1`, `t2` — tiers 3 and 4 are
reserved, with no RBAC behind them at all), each strictly additive to the one below it, bound via
`ClusterRoleBinding`:

| Tier | Adds (all `get`/`list`/`watch`, nothing else) |
|---|---|
| 0 — Registered | The `kube-system` namespace object, by name. Proves cluster identity; nothing else. |
| 1 — Infrastructure | `nodes`; `storageclasses` (storage.k8s.io); `ingressclasses` (networking.k8s.io). |
| 2 — Services | `namespaces`, `pods`, `services`, `persistentvolumeclaims`, `persistentvolumes`; `deployments`/`statefulsets`/`daemonsets`/`replicasets` (apps); `ingresses`; `horizontalpodautoscalers`; `poddisruptionbudgets`; and, only if `mesh.readPolicy` is on (default), Istio `peerauthentications` — read-only, no authorization policies, no secrets. |

Every verb at every tier is read-only. There is no `create`/`update`/`patch`/`delete` on any cluster
resource anywhere in this table — confirmed by a dedicated test (`TestAccessTierGrantsAreReadOnly`), not
just by inspection.

**`rbac.mode=namespaced`.** If you install this way instead of the cluster-wide default, tier 2's grant
stops being a `ClusterRole` and becomes a `Role` + `RoleBinding` per namespace you listed in
`scope.namespaces` — the same resources, minus `Namespace` and `PersistentVolume` (both cluster-scoped
types; no `Role`, in any namespace, can name them, so the agent simply doesn't see namespace metadata or
raw PV objects in this mode). Tiers 0 and 1 stay cluster-scoped `ClusterRole`s regardless — nodes, storage
classes and ingress classes aren't namespaced types, so there's no namespaced form of that grant to fall
back to.

## The agent's optional telemetry collectors ("beyond the OTel collector")

Turning on `telemetry.*` signals adds up to four more workloads alongside the agent, each scoped to only
what it does, each off unless you turn it on. None of this widens `access.tier` — a telemetry signal and
the discovery ClusterRoles above are deliberately on two separate ServiceAccounts, for a specific reason:
the cluster-telemetry collector below is the one workload in this chart with a receiver reachable from
elsewhere in the cluster, so if it's ever compromised, the token an attacker gets should only reach what
telemetry needs — never the union of that and the discovery agent's own access tier.

**A second, separate `ServiceAccount` and `ClusterRole`**, additive and independent of everything above,
built rule by rule per enabled signal — turn a signal off and its rule disappears, not just its use of the
data:

| Signal (`values.yaml` flag) | Grants (all read-only) |
|---|---|
| Tagging every signal with pod/namespace/node (always on once any telemetry is enabled) | `get`/`list`/`watch` on `pods`, `namespaces`, `nodes` |
| `resourceUsage.metrics` or `nodeRuntime.metrics` | `get` on `nodes/stats` — read from each node's own kubelet directly, never proxied through the API server, so this is the one rule that needs no `list`/`watch` at all |
| `kubernetesState.metrics` | `get`/`list`/`watch` on `nodes`, `namespaces`, `pods`, `replicationcontrollers`, `resourcequotas`, `services`, `deployments`/`replicasets`/`statefulsets`/`daemonsets`, `jobs`/`cronjobs`, `horizontalpodautoscalers` — the same object kinds `kube-state-metrics` itself watches |
| `kubernetesEvents.logs` | `get`/`list`/`watch` on `events`, and nothing else this receiver could be pointed at |

**Two collector pods, no elevated Kubernetes permissions, but real host access, gated by signal:**

- `continuum-telemetry-host` (a DaemonSet): runs as `runAsUser: 65532`, not root, not privileged,
  `capabilities: {drop: ["ALL"]}`. It mounts the host filesystem read-only (`hostPath: /`) to read it, plus
  `/var/log/pods` and `/var/log/journal` — each mount only actually used when the matching signal
  (`resourceUsage.metrics`, `systemLogs.logs`, with `journald` specifically for the journal mount) is on.
  A Pod Security "restricted" or "baseline" namespace label will refuse this hostPath volume outright; this
  is a deliberate, documented tradeoff of the feature, not an oversight.
- `continuum-telemetry-cluster` (a Deployment): same non-root, no-capabilities posture, no hostPath at
  all — it only talks to the Kubernetes API, via the ClusterRole above, and receives OTLP on two ports.

**Two more, each off by default and each gated twice** (the signal must be on *and* explicitly set to the
bundled source, not an externally-run instance) — because what they read genuinely cannot be reached with
an RBAC-only, capability-dropped container:

- **Kepler** (`telemetry.energy.metrics.enabled` + `source: bundle-kepler`, energy/power attribution): the
  upstream image's own documented requirement is `privileged: true` and `hostPID: true`, with no
  capability-based alternative — the chart's own comment is explicit that this is a third-party
  requirement, not a Continuum design choice. It reads `/proc` and `/sys` read-only to attribute RAPL
  energy readings to the processes/pods actually using it.
- **dcgm-exporter** (`telemetry.accelerators.metrics.enabled` + `source: bundle-dcgm`, GPU metrics):
  NVIDIA's own documented requirement is `runAsUser: 0` plus the single `SYS_ADMIN` capability (everything
  else dropped) — narrower than Kepler (no `hostPID`, no arbitrary device access), but still root inside
  the container. Reads GPU state via DCGM; no Kubernetes RBAC beyond what every other telemetry container
  already has.

Both are silent by default and require a deliberate, double opt-in before either one is ever scheduled.

## Regional operators: none of the above

A regional operator's chart has no RBAC manifest at all — no `ServiceAccount` doing anything beyond
existing (`automountServiceAccountToken` isn't even relevant; nothing in the pod calls the Kubernetes API),
no `ClusterRole`, no host mount, no elevated capability. Its only receiver is `otlp`; the only thing it
reads is telemetry already pushed to it over the network, authenticated by the receiver's own bearer
token — a completely different shape of grant from everything above ("here is a token to present to this
endpoint," not "here is what this ServiceAccount may read from the Kubernetes API"). What you actually
configure is the receiver token and the export destination — that actually is the complete list.

## What this page doesn't cover

NetworkPolicies (on the agent's egress, and optionally in front of the telemetry receiver) are a different
mechanism in the opposite direction — they restrict what a pod may reach on the network, they don't grant
API access, and they're off by default on every chart here like the rest of this project's networking
posture. See [Trust model](./trust-model.md) for how all of this fits into the bigger picture, including
the one open item (the quick-start gateway token) that isn't a Kubernetes permission at all.

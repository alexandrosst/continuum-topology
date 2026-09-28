---
id: telemetry-intent
title: Telemetry intent
description: The four commitments telemetry collection is built on, why its trust model has one party instead of two, and how a single Helm release already fans out across every node in a cluster.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# Telemetry intent

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/telemetry-intent.svg')} alt="Left: one Helm release fanning out to every node via DaemonSets for the host collector, Kepler and dcgm-exporter, plus one Deployment for cluster-wide aggregation. Right: three clusters each running their own independent release, with no fleet-wide telemetry policy today." />
  <figcaption className="diagram-caption">One release per cluster already reaches every node — Kubernetes does that fan-out, not a per-node chart. What it doesn't do yet is coordinate across clusters.</figcaption>
</figure>

Where [Observability intent](./observability-intent.md) is about how far an *agent* is allowed to see into a cluster, telemetry intent is a related but separate question: given a signal an agent is already allowed to see, should raw OTel data about it leave the cluster at all, and to where. The two systems share a chart, a philosophy of narrowing rather than widening, and a UI (telemetry sits in the same wizard as access tier and namespace scope), but they don't share a trust model — and that difference, not just a list of toggles, is what this page is actually about.

## Four commitments

Telemetry's philosophy was never written down as a single statement before this page — it existed only as comments scattered across `values.yaml` and `_helpers.tpl`. Reconstructed, it reads as four commitments, and everything else in this chart follows from them:

1. **Named consent, not raw configuration.** Every signal is a labeled, human-readable choice — "Energy", "Accelerators (GPU)" — with a stated permission cost, not a bare OTel receiver name typed into a form. Same instinct as the discovery agent's own collector checkboxes.
2. **Safe by default, not safe by assumption.** `memory_limiter` runs first in every pipeline unconditionally — not a toggle, baseline collector hygiene — and `redaction` defaults on. The two controls that matter most are not opt-in.
3. **Declarative only.** Nothing about telemetry is pushed live. Every change, at install time or afterward, is a generated `helm install` / `helm upgrade --reuse-values` command the cluster owner runs themselves. There is no hidden control-plane channel for telemetry configuration to leak through, even in principle.
4. **Additive permissions.** The telemetry `ClusterRole` never depends on or widens `access.tier`; each signal grants only the one read-only rule it specifically needs, and nothing is granted speculatively.

## Four axes: scope, layer, modality, kind

Every signal in the UI — "Energy", "Accelerators (GPU)", "Application logs" — is really a point along four axes, and only one of them is a free choice:

- **Kind** is the only axis a person actually picks: which of the eleven named signals to turn on. Everything below is metadata about a kind, not a further choice within it.
- **Modality** (`metrics` \| `logs` \| `traces`) is a physical fact about a kind's receiver, not a toggle. A `hostmetrics` receiver produces metrics and nothing else; there's no configuration that would make it emit logs. The UI and the catalog (`TELEMETRY_SIGNALS`) surface it as read-only metadata precisely so it's never implied to be independently configurable.
- **Layer** (`infrastructure` \| `application`) is the RBAC/deployment-shape classification each signal has always had — the field this page used to call `domain`. It answers "is this about the cluster's own machinery, or about what's running on it," and it's what the permissions section above and the chart's `ClusterRole` gating are actually keyed on. One signal, accelerators, needs an explicit escape hatch from this: see below.
- **Scope** (`cluster` \| `node` \| `application`) is where the real per-kind mechanism lives:
  - **cluster**-scoped signals (`kubernetesState`, `kubernetesEvents`, `networkLatency`) come from a single, cluster-wide receiver — there's no "which cluster" to narrow, so the UI says so rather than pretending a control exists.
  - **node**-scoped signals (`resourceUsage`, `nodeRuntime`, `systemLogs`, `energy`, `accelerators`) run one pod per node — narrowing *which* nodes is the DaemonSet's own `nodeSelector`/`tolerations` fields (see [What one release actually deploys](#what-one-release-actually-deploys)), not a new form control, since a structured Kubernetes scheduling object doesn't fit this form's `--set`-command model any more cleanly than `scope.selector` does for namespace scope.
  - **application**-scoped signals (`applicationMetrics`, `applicationLogs`, `traces`) carry real namespace identity and can be narrowed per kind, falling back field-by-field to the install's own global `telemetry.scope` when a kind's own override is left empty — the "Application scope overrides" panel in the wizard.

Accelerators is the one honest exception to "layer settles everything": it's `layer: infrastructure` for deployment shape (a DaemonSet, no RBAC, reads hardware directly) and `scope: node` for the same reason, but its GPU metrics can carry the namespace/pod using the GPU via the kubelet's pod-resources mapping — `namespaceScopable: true` names that explicitly rather than overloading `layer` with a second meaning. It's opt-in and off by default (`telemetry.accelerators.metrics.applyScope`): turning it on asks dcgm-exporter for its own Kubernetes pod-label enrichment and lets the install's namespace scope reach GPU metrics the same way it reaches application data, closing the gap the old version of this page's "Where this doesn't reach yet" section named.

## One party, not two

`observability-intent.md` works because there are two independent parties in the loop: the agent applies scope to itself, and the server checks the result a second time, from outside, before storing anything. Telemetry only ever has one party. By design, the Continuum server never sees telemetry payloads at all — they go straight from the collector to whatever backend the operator configured in `telemetry.export.otlp`. There is no second, independently-trusted actor positioned to re-check what the collector did.

`redaction`-on-by-default is the closest thing telemetry has to that server-side backstop, but it's the same actor checking its own work, not a second one. That's a legitimate design choice — the point of telemetry is to avoid Continuum slowly becoming an observability backend itself — but it means the bar for "safe by default" is higher here than it is for discovery, precisely because nobody is double-checking behind it. It's also why the [permissions](#permissions) section below matters more for telemetry than the numbers alone would suggest: the cluster collector's receiver is the one workload in this chart genuinely reachable from anywhere else in the cluster, so its identity has to be minimal on its own, not just bounded by a tier nobody is re-checking.

## What one release actually deploys

Kubernetes already has a primitive for "run this on every node," and telemetry uses it rather than reinventing it. The host collector, Kepler and dcgm-exporter are each a `DaemonSet` — the kubelet schedules one pod per matching node automatically, the moment a node joins or a taint/toleration/nodeSelector changes, with no per-node install step of any kind. The cluster collector is a `Deployment` with one replica: cluster-wide aggregation — k8s object watching, Prometheus-shaped scraping of Kepler/dcgm, and the OTLP receiver applications push to directly — only ever needs to exist once.

So a single `helm install` already reaches every node in that cluster. There is no separate chart per node, and nothing to make "node-specific": scope, in the sense of *which nodes*, is Kubernetes' job, expressed through the DaemonSet's own `tolerations`/`nodeSelector` fields (already used, for instance, to keep Kepler off nodes without an arm64 image), not something this chart has to re-solve. This is the mechanism behind the **node** value of the scope axis described in [Four axes](#four-axes-scope-layer-modality-kind) below — it already existed, independently, on every DaemonSet-backed signal; that section just gives it one name.

Multi-cluster starts the same way it always has: **one release per cluster, independently.** The discovery agent already works this way — one enrolled identity per cluster — and telemetry, sharing that chart, inherits the same shape. Each cluster's `telemetry.*` values are its own, and turning on a signal in one cluster still has no effect on any other's configuration.

What's changed is that those independent exports no longer have to stay siloed once they leave a cluster. [Regional operators](./regional-operators.md) are a second, explicit fan-in tier above the per-cluster one this page describes: a standalone collector that receives what a set of already-approved clusters already export and re-exports it as one stream. Be precise about what that does and doesn't solve — it's fan-in **aggregation of exports that are already flowing**, not a fleet-wide telemetry *policy*. Turning a signal on across many clusters at once, in a single action, is still genuinely absent: each cluster's `telemetry.*` values still have to be set by that cluster's own `helm upgrade`, one at a time. A regional operator changes where already-collected data converges, not how many clusters' collection settings a single action can change. Whether the Continuum server ever becomes a place that reasons about telemetry *configuration* across a fleet — as opposed to relaying its already-collected output — is still the same open question [Recommendation 7 of the design review](#where-this-doesnt-reach-yet) names for the one-way data boundary.

## The pipeline itself

Every pipeline — host metrics, host logs, cluster metrics/infra, metrics/app, logs/infra, logs/app, traces — runs processors in the same fixed order: `memory_limiter` first, always; `k8sattributes` for identity, where the receiver needs it; `resourcedetection` if enabled; `redaction` if enabled (on by default); whatever the pipeline itself specifically needs (a node-identity transform for infra metrics, the namespace scope filter for application-domain pipelines, `probabilistic_sampler` for traces below 100%); the `extraProcessorNames` escape hatch; `batch`, always last. `memory_limiter` has to be first to protect the collector before anything else touches the data; `batch` has to be last so it never batches data a later processor might still drop.

`memory_limiter` is paired with a computed `GOMEMLIMIT` environment variable on each collector container — set from that container's own `resources.limits.memory`, so Go's garbage collector backs off in step with the processor instead of the two fighting each other under memory pressure — rather than left to coincidence, which is where most charts that add `memory_limiter` stop.

## Permissions

Telemetry's collectors run under their own `ServiceAccount` — `{{ include "agent.name" . }}-telemetry` — separate from the discovery agent's, and by extension from whatever `access.tier` `ClusterRole` the agent's own identity holds. `automountServiceAccountToken: false` at the ServiceAccount level; only the two collector containers that actually call the Kubernetes API (for `k8sattributes` pod/namespace/node identity) mount it, explicitly, in their own pod spec. If the cluster collector's receiver is ever compromised — the one component in this chart reachable from anywhere else in the cluster — the token an attacker gets reaches exactly what telemetry itself needs, structurally, not the union of that and whatever tier the install happens to be running at.

Each RBAC rule in `telemetry-rbac.yaml` is gated behind the one signal that actually needs it: `k8sattributes` needs pods/namespaces/nodes; `kubeletstatsreceiver` (resource usage, node runtime) needs `nodes/stats` only, read directly from each node's own kubelet — never proxied through the API server, so it needs no `nodes/proxy` permission; `k8sclusterreceiver` (Kubernetes state) needs the same read-only object kinds `kube-state-metrics` itself watches; `k8sobjectsreceiver` is scoped to `events` only, nothing else it could technically be pointed at. Re-render with `helm template` after changing `telemetry.*` to see exactly which permission set a given configuration holds — the values file comments cross-reference which RBAC rule each signal requires directly.

## Interoperability

Every destination preset — Honeycomb, New Relic, SigNoz, Splunk, Chronosphere, Jaeger, Grafana Cloud, Datadog, Elastic Cloud, self-hosted — resolves to the exact same `telemetry.export.otlp.*` shape. There is no per-vendor export mode to maintain or drift out of sync; only UI sugar over one generic OTLP exporter. AWS/Azure/GCP are deliberately not offered as one-click presets — those backends need IAM-style auth a generic OTLP exporter can't do — and the UI says so explicitly rather than silently doing nothing.

With Continuum itself, telemetry is a one-way configuration channel by design: the server tells the cluster what to collect and where to send it, and never receives, stores, or reasons about the telemetry data itself. One place that boundary is meant to be crossed cleanly rather than blurred: the `networkLatency` signal is meant to re-emit the discovery agent's own already-existing path-measurement capability as OTel metrics, real reuse across the discovery/telemetry line rather than a parallel implementation of the same idea. Today that's a contract, not yet a fact: the collector side is real and enforced (a reserved `service.name` of `continuum-network-latency`, and every application-scope filter — global or per-kind — is built to leave records carrying it untouched, structurally exempt rather than merely un-scoped by omission), but nothing in this codebase's Go agent emits OTLP yet, so no cluster today actually produces that data. See [Where this doesn't reach yet](#where-this-doesnt-reach-yet).

## Cross-distribution deployment

The paths this chart relies on — `/var/log/pods`, `/var/lib/kubelet/pod-resources`, each node's own kubelet `:10250` stats endpoint — are standardized by the kubelet itself, not by any distribution, so they hold on k3s, kubeadm, RKE2, Talos, EKS, GKE, AKS and OpenShift alike. What genuinely varies by platform is security posture and hard platform limits: Pod Security Admission levels, OpenShift's SecurityContextConstraints, GKE Autopilot's default refusal of privileged/hostPath/DaemonSet workloads, EKS Fargate's lack of DaemonSet support at all, and Kepler's absence of an arm64 image. [Deploy → Telemetry on every distribution](https://github.com/alexandrosst/continuum-topology/blob/main/deploy/README.md#telemetry-on-every-distribution) has the concrete commands for each of these; this page is the philosophy behind why they're needed, not a duplicate of them.

## Reconfiguration

"Alter it anytime" is a real architectural property, not just a slogan: every telemetry change, at install time or later, is the same kind of `helm upgrade --reuse-values` command, generated by the UI, run by the cluster owner. `withTelemetry()` deliberately states every signal and every processor flag explicitly on every call, specifically so `--reuse-values` can never silently keep a stale value the panel meant to change — which only holds if the form generating that command starts from what's actually installed. The post-install "Change telemetry" panel now seeds its draft from the agent's own self-report (`installedTelemetry`) rather than a blank form, so a small edit — bump traces sampling from 100% to 20%, say — can no longer silently reset every other currently-enabled signal to off in the same command.

## Where this doesn't reach yet

Three things named here on purpose, because a page like this is exactly where they'd otherwise get lost in Helm comments again:

- The agent's self-report today only ever says *which* signals are on (`CONTINUUM_TELEMETRY_SIGNALS`), never *how* — not the effective export destination, not the effective processor settings, not which accelerators source is in use, not any per-kind application scope override. Extending it to report effective configuration the way `installedTier` already reports the effective tier is the structural fix behind a safe "alter anytime" panel, not just the seeding fix above. Still open.
- There is no Go-side OTLP emitter behind the `networkLatency` signal yet — the collector-side contract described in [Interoperability](#interoperability) (the reserved `service.name`, the scope exemption) is real and enforced, but the discovery agent doesn't produce that data today. Newly named and scoped here on purpose, now that the collector side is a real, tested contract rather than an implicit assumption: closing it is a Go-agent change, not a chart change.
- Fleet-wide telemetry *policy* is still absent: there is no way to turn a signal on across many clusters in one action. [Regional operators](./regional-operators.md) close part of the multi-cluster picture — fan-in of what's already being collected — but deliberately not this part; that page's own "Where this doesn't reach yet" section says the same thing from the other direction.

The accelerators namespace-identity gap the previous version of this page named here is closed: `namespaceScopable` (see [Four axes](#four-axes-scope-layer-modality-kind)) names the dual classification honestly instead of overloading `layer`, and the opt-in, off-by-default `applyScope` flag lets an operator who wants GPU metrics narrowed by the install's namespace scope turn that on explicitly, rather than either silently ignoring scope forever or applying it by surprise.

All three remaining items are open, not urgent: worth deciding on purpose, the same way whether telemetry ever closes the loop back into Continuum (and, with it, any future fleet-wide policy) is worth deciding on purpose rather than discovering the answer by accretion.

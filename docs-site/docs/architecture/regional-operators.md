---
id: regional-operators
title: Regional operators
description: The second fan-in tier above per-cluster telemetry — a standalone aggregation point with no RBAC, no server channel, and no chaining, and what that deliberately leaves out.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# Regional operators

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/regional-operators.svg')} alt="Two clusters, each already exporting its own aggregated telemetry, arrowing into one regional operator box labeled with its processor pipeline, which arrows onward to a destination. A second, dashed arrow points at a ghost 'another regional operator' box labeled not supported in this release." />
  <figcaption className="diagram-caption">A regional operator fans in what clusters already export as a whole — it doesn't reach into any single node, and it can't (yet) feed another regional operator.</figcaption>
</figure>

[Telemetry intent](./telemetry-intent.md) covers the first fan-in: one Helm release already reaches every node in a cluster, because a DaemonSet is Kubernetes' own fan-out primitive, not something this project reinvents. That page's own "Multi-cluster" section names the fan-in that stops there — each cluster's release is independent, and nothing aggregates what several clusters produce. A regional operator is the second, explicit fan-in tier that closes that gap: a standalone collector that receives what a set of already-approved clusters already export, and re-exports it as one stream.

## What a regional operator is

Architecturally, a regional operator is the same idea as a local operator's own collectors — an `otel/opentelemetry-collector-contrib` deployment with a config the server generates — just pointed the other way. A cluster's telemetry collectors *export* OTLP outward; a regional operator *receives* it. There is no gRPC/mTLS channel to the Continuum server at all, ever — the same one-way boundary [Telemetry intent's "One party, not two"](./telemetry-intent.md#one-party-not-two) describes for a single cluster's telemetry holds here too, just one layer up. The server never sees the data a regional operator relays, only the record of the operator's own existence and scope.

It needs no Kubernetes API access of its own, and gets none: the chart's `serviceaccount.yaml` renders a `ServiceAccount` with `automountServiceAccountToken: false` and nothing else — no `ClusterRole`, no `ClusterRoleBinding`, confirmed by the chart's own render tests. That follows directly from what it does: it only relays and re-processes telemetry that already arrived over OTLP, so it never needs to watch this cluster's own object graph the way `k8sattributes` does for a cluster's own collectors.

Its only credential is a receiver bearer token — minted once, at creation, and shown exactly once, the same convention an enrollment token already follows. The server keeps only a hash of it; anyone who needs the value again has to create a new operator.

## The two fan-ins, as one picture

Read the two tiers together and there's really just one shape, applied twice:

- **Nodes into a cluster.** Existing, automatic, and covered in full by [Telemetry intent](./telemetry-intent.md#what-one-release-actually-deploys). Kubernetes' own DaemonSet scheduling puts one collector pod on every matching node the moment it joins; the cluster's single `Deployment` collector is where that per-node output, plus the cluster's own application/cluster-scoped signals, already converges into one export point. This has always been automatic and requires no separate decision.
- **Clusters into a regional operator.** New, and explicit rather than automatic: a person names a set of `SourceClusterIDs` by hand when creating the operator, and the server checks that every one of them is currently the cluster of an approved agent in the organisation — the same "already-approved clusters only" rule that governs everything else a regional operator touches, re-checked server-side even though the UI's own picker only ever lists approved clusters to begin with.

The important distinction: a regional operator aggregates what a cluster *already* exports as a whole — the union of that cluster's own DaemonSet and Deployment collector output, whatever signals its own telemetry intent has turned on — not individual nodes directly. It operates one level up from where local operators already converge, never below it. There is no path from a regional operator back down into a specific node's data; the cluster's own collector is where that shape is lost, by design, on the way out.

## How one is created

A regional operator is a real, immediately-persisted record (`store.Operator`), not a pending request. That's a deliberate difference from agent enrollment: an agent starts in a pending state and needs a person to approve it before anything is trusted; a regional operator has no equivalent approval step, because its own creation already required naming a set of already-approved clusters — the trust decision was made when those clusters were approved, not again here. The receiver credential is minted at the same moment the record is created, not held back for a later step.

`Destination` also models chaining one regional operator into another (`DestinationKind: "operator"`), and the server rejects it outright in this release — creating or updating an operator with that destination kind fails validation. This is named as a real, deliberately deferred capability, not an oversight: this release builds the mechanism for a two-tier fleet (clusters feeding one regional operator that exports onward), and a fleet of regional operators feeding each other, or reassigned dynamically, is exactly the kind of decision a future decision-making layer should make on purpose — not something to bolt on ahead of that layer existing.

## What's rendered

The `continuum-regional-operator` chart's processor pipeline follows the exact ordering discipline `continuum-agent`'s telemetry collectors already use — `memory_limiter` always first, `resourcedetection`/`redaction` next if enabled, signal-specific processors (`probabilistic_sampler` for traces below 100%), the `extraProcessorNames` escape hatch, `batch` always last — so the same processor-editing UI the agent's telemetry panel already has works for this chart completely unmodified. The one deliberate omission is `k8sattributes` and any namespace scope filter: a regional operator never watches a Kubernetes object graph of its own, so there is nothing for either of those to attach identity from.

## What's not automatic, and why

Creating a regional operator hands back a `helm install` command and a companion `kubectl create secret` line for the receiver token — nothing is applied on anyone's behalf, following the same declarative-only commitment [Telemetry intent's four commitments](./telemetry-intent.md#four-commitments) already states for a single cluster. Alongside those, the server prints one more informational line per source cluster: the exact `helm upgrade --reuse-values` a person would run against that cluster's own `continuum-agent` release to actually point its `telemetry.export.otlp.endpoint` at this operator. That line is a reminder, never executed — there is no live reparenting, no auto-discovery of new children, and nothing pushed to any cluster the moment a regional operator's scope changes. Each of those `helm upgrade` commands is a separate, deliberate step a person runs themselves, on their own schedule.

## Where this doesn't reach yet

Two things worth naming here on purpose, the same way [Telemetry intent's own gap section](./telemetry-intent.md#where-this-doesnt-reach-yet) does, rather than letting them quietly disappear into a values file comment:

- **No chaining between regional operators.** Modeled in the data shape, rejected by validation — see [How one is created](#how-one-is-created) above.
- **No dynamic or automatic assignment.** Nothing decides which regional operator a cluster's traffic should feed, and nothing moves that assignment once it's made — a person names the source clusters by hand, once, and changes them by hand later. This is exactly where a future decision-making or reinforcement-learning layer would plug in, once one exists: reassigning a cluster's export target based on load, latency or cost is a real capability this mechanism makes possible, not one it provides today.

Neither gap is urgent, and neither is an oversight — both are the same kind of "decide this on purpose later" the rest of this project's telemetry documentation is honest about elsewhere.

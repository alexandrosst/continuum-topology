---
id: overview
title: System overview
description: The server, the agent, and how they fit together across cloud, edge and far-edge clusters.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# System overview

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/architecture-overview.svg')} alt="Continuum Server with two listeners, agents dialing out from cloud, edge and far-edge clusters, GHCR supplying images and charts, your browser talking over HTTPS" />
  <figcaption className="diagram-caption">Everything an agent sends is pulled by the server, never pushed in from outside it — the arrows into "Continuum Server" are the only traffic initiated from a cluster.</figcaption>
</figure>

## The server

One replica, one PersistentVolumeClaim. It's deliberately not built to scale horizontally: it holds a SQLite database (accounts, sessions, the enrollment CA and its private key) that needs a single writer, plus the in-memory state of every connected agent's live stream. There are two listeners, and they have almost nothing in common:

- **`:8443`, the agent port.** gRPC over mutual TLS. Agents connect *out* to this — nothing ever connects in the other direction. The handshake must reach the pod byte-for-byte untouched, which is why this port can never sit behind something that terminates TLS. See [Exposing the agent port](./exposure-options.md).
- **`:8080`, the admin port.** The web UI and the JSON API it's built on. Plain HTTP inside the pod, meant to be put behind a Gateway that terminates TLS (or given its own certificate directly with `admin.tls`).

Optionally, a Neo4j Community instance (bundled by the chart, or your own external one) holds topology history, change events and the audit trail as a temporal graph — every entity gets versions with a `validFrom`/`validTo`, so "what did this look like an hour ago" is one query away. Without it, the same things live in SQLite; the server keeps working exactly the same either way, and if Neo4j is briefly unreachable, events simply buffer in SQLite and drain in once it's back.

The server **decides nothing on its own**. The Placement page's recommendations are advice with evidence attached; nothing is ever deployed or moved without a person approving it.

## The agent

One small pod per cluster, with cluster-wide **read-only** RBAC — it can `list`/`watch`, never write, and it never reads Secrets or ConfigMaps at all (that's enforced both by the chart's RBAC and by the agent discarding those fields on arrival if it ever saw them). It authenticates to the server with a short-lived client certificate (mTLS, TLS 1.3), renews itself automatically at half-life, and rejoins on its own if it's been offline for less than 7 days.

Three tiers govern what actually gets reported, always checked in this order: the chart's own `access.tier` is the ceiling (only a `helm upgrade` on the cluster's side can raise it), an administrator's **approved** tier in the UI can only sit at or below that ceiling, and the agent's own **effective** reporting can only sit at or below what's approved. The server can narrow this at any time; it can never widen past the ceiling, and the agent checks that for itself rather than trusting the server to ask nicely.

## Clusters and tiers

Every connected cluster is labeled <span className="tier-chip tier-chip--cloud">cloud</span>, <span className="tier-chip tier-chip--edge">edge</span> or <span className="tier-chip tier-chip--far">far-edge</span> — this is what the placement engine's cost function and the topology views group by. There's nothing structurally different about how an agent behaves in each tier; the distinction is about the estate you're modeling, not the software.

## Images and charts

The chart's defaults already point at this project's own published images, so none of the install commands on this site — nor the one the server itself prints on the **Connect a cluster** screen — ever need `--set image.repository=...`.

## Where to go next

[Agent trust model](./agent-trust-model.md) walks through enrollment step by step — how a cluster goes from nothing to a certificate the server trusts. [Exposing the agent port](./exposure-options.md) covers the three ways to get real traffic to `:8443` and the one mistake (terminating TLS at a proxy) that breaks all of them.

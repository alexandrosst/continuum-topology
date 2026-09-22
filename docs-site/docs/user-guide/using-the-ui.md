---
id: using-the-ui
title: Using the UI
description: A tour of the topology views, Discovery, Agents, Placement and Team pages once your server is running.
---

# Using the UI

This assumes you have a server running and at least one cluster connected (see [Connecting a cluster](../installation/connecting-a-cluster.md) if not). It's a tour, not an exhaustive reference — each section below links to a deeper page where one exists.

## Two views of one model

Everything you see is a projection of a single shared model, kept live by your connected agents:

- **Application view** — your services, grouped by the cluster they run in, with dependencies drawn as edges between them. IoT devices sit in a row per site below the clusters they connect to; anything a service calls that isn't part of your own estate shows up as an external endpoint.
- **Infrastructure view** — the nodes underneath (VMs, bare metal, edge devices), grouped by cluster, with an optional overlay of which services are scheduled on which node.

Edit something in one place — rename a service, correct a cluster's tier — and it updates everywhere, because there's only one underlying document.

## Discovery

**Discovery → Connect a cluster** is where every new cluster starts (see [Connecting a cluster](../installation/connecting-a-cluster.md)). The same page's **inbox** is where the server surfaces things it noticed but didn't decide on its own: a newly discovered application grouping suggested from the traffic it's seen, or a *suspicion* — traffic from an onboarded cluster toward an address nothing has claimed, on a port only Kubernetes itself tends to use — which usually means there's another cluster out there worth connecting. Accept, correct, or dismiss each one; nothing is applied automatically.

## Agents

**Agents** lists every connected cluster's agent: whether it's currently connected, its last heartbeat, certificate expiry, and — expand a row — exactly what it's allowed to see versus what it's actually sending right now (the installed / approved / effective tiers from [System overview](../architecture/overview.md)), plus any self-reported problems and their fixes. This is also where an administrator narrows what an agent shares (pausing an optional collector, excluding more namespaces) or requests it be widened — widening always needs a `helm upgrade` on the cluster's own side; the page shows you the exact command.

## Placement

The **Placement** page answers "where would this service be better off, and what would that change?" — it's read-only advice, and every recommendation shows its evidence: what the round-trip estimate is based on (measured, declared, or a distance-based guess), how confident the traffic-weight figure is, and what would have to be true for the recommendation to flip. Nothing is ever moved from here; a person applies a change deliberately, elsewhere. See [Mobility and placement](./mobility-and-placement.md) for how the cost function and confidence levels work.

## Namespaces and services

Every discovered namespace and the services inside it are browsable in their own right — which cluster they're in, how exposed they are, whether a service mesh covers them, and whether an agent was actually told to look at them (some namespaces are deliberately left out of an agent's scope; see [Namespaces and services](./namespaces-and-services.md)).

## Approvals

Two different things use the word "approve" and it's worth keeping them straight: approving a **newly enrolling agent** (typing the approval code from its log, confirming it's the cluster you expect — see [Agent trust model](../architecture/agent-trust-model.md)) is a one-time identity check. Approving a **placement recommendation**, by contrast, isn't a single button — it's whatever your own change-management process is; the Placement page's job ends at giving you a well-evidenced recommendation, not at applying it. See [Approvals in depth](./approvals-in-depth.md).

## Team and roles

Every topology belongs to one organization, and nothing crosses between organizations. Four roles, most to least: `owner`, `admin` (connect and approve clusters, manage tokens, invitations and members), `editor` (change the topology, apply decisions), `viewer` (read-only). Invite colleagues from **Members & access** — invitations are one-time links that expire after 7 days.

## History

If Neo4j is enabled (see [System overview](../architecture/overview.md)), the **History** page adds a time scrubber and an exact-moment picker: "view the estate as of an hour ago" is one click, with a clear banner reminding you that you're looking at the past and editing is disabled until you return to now. Without Neo4j the same events are tracked in SQLite; you get the audit trail either way, just not the point-in-time views.

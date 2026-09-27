---
id: system-memory
title: System memory
description: How every kind of change — a service scaling, an application renamed, an agent's tier narrowed — ends up in the same temporal graph, with a reason attached and a bounded way to ask what depends on what.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# System memory

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/entity-version-model.svg')} alt="One entity's timeline: three Version nodes in sequence, each with a validFrom and validTo, the current one open-ended, and an Event connected to the version it produced by an EXPLAINS edge" />
  <figcaption className="diagram-caption">Nothing is ever edited or deleted here. A change closes the open version's validTo and opens a new one — "what did this look like an hour ago" is a filter on a timestamp, not a separate audit system bolted on afterward.</figcaption>
</figure>

Every entity this server has ever seen — a cluster, a service, a namespace, an agent, an application — is remembered the same way, regardless of which of the nine kinds it is or how it came to be recorded. That uniformity is deliberate: the topology views, the Timeline panel and the [Dependents/Dependencies graph](#what-depends-on-what) all work against one shape, not nine special cases.

## Entities and versions

An **Entity** is an identity — `(org, kind, id)` — that persists for as long as anything has ever been true about it. A **Version** is one period during which it looked a certain way: a JSON document, a name, a status, a `validFrom`, and a `validTo` that is `null` exactly when that version is the current one. Recording a new state closes whatever version was open (setting its `validTo` to the instant of the change) and opens a fresh one — an entity's full history is just its versions in order, and "what did the estate look like as of last Tuesday" is a `WHERE validFrom <= t AND (validTo IS NULL OR validTo > t)` filter, not a reconstruction from a change log.

Recording is idempotent by design: every version's document is hashed, and handing the same document to the same entity again — the ordinary case, since most of what a poll rediscovers hasn't changed — is a no-op rather than a new, identical version. Two things distinguish this from a naive "diff and skip": a version that closes and reopens at the exact same instant (a snapshot mid-transition) collapses rather than leaving a zero-length version behind, and moving an entity between clusters is tracked as its own fact (the `cluster` field is part of what has to match for a state to count as "unchanged") even when nothing else about it did.

Two families of caller produce versions, and both end up in exactly the same place:

- **`Record`** takes a whole topology snapshot — everything a periodic scan found — and diffs it entity by entity against what the graph already holds, closing what has disappeared and opening what changed. This is how clusters, services, namespaces, workloads, dependencies and network paths get recorded: the seven kinds nothing outside a poll ever changes.
- **`RecordEntity`** versions one entity at a time, for state whose owner already knows the exact moment and reason something changed rather than noticing it by comparing two snapshots — an agent's tier, its consent overrides, its approval status; an application's declared shape. Same `Version`/`HAS_VERSION` shape, same idempotence, so Timeline treats an agent's own history no differently from a service's.

## Why, not just what

A version alone answers "what changed." Two of this server's nine kinds used to have no way to answer "why" at all — an application's document changing shape in a save, or an agent's tier moving — even though every *other* kind (a service scaling, a namespace's labels changing) already came with a plain-language reason via the same polled-topology diff that produces `service-added`, `service-scaled`, and so on.

The fix is the same mechanism extended to cover them: an **Event** — a timestamped, kind-named, human-readable record (`agent-tier-changed`, `application-renamed`, `application-membership`, …) — written at the same instant as the version it explains, then linked to it with an **`EXPLAINS`** edge. `LinkEventChanges` does the linking: it matches an event to whichever version of the same target opened in the same one-second window, so a caller only has to write the event and call it — it does not have to already know which version its own change produced. Timeline shows both sides of this: `Explains` on a version is *why this one looks the way it does*, and the plain `Events` list on an entity is *everything ever noticed about it*, including removals that have no surviving version left to attach to.

One save can explain itself more than once. Renaming an application and clearing its membership in the same workspace save produces two events — `application-renamed` and `application-membership` — both linked to the one version that save produced, rather than a single flattened "something changed" event losing the distinction. The same discipline the polled diff already followed (one event per changed fact, not one per changed entity) now applies uniformly.

## The relationships between entities

Six relationship types connect entities to each other — `IN_CLUSTER`, `RUNS_ON`, `CALLS`, `PATH_FROM`, `PATH_TO`, `CONTAINS` — and every one of them is temporal the same way a version is: closed (`validTo` set) when it stops being true, never deleted. A service that moves clusters does not lose its history of having been in the old one; a dependency that stops being called is closed, not erased. This is also why a relationship's own validity has to be checked separately from its endpoints' — an edge from a year-old, now-closed version of one entity to a current version of another is exactly the kind of thing "as of last Tuesday" has to get right, and getting it wrong silently is worse than a missing edge.

## What depends on what

Given one entity and a moment, **`Dependents`** answers "what would be affected if this changed" and **`Dependencies`** answers "what does this itself rely on" — the same walk over those six relationship types, in opposite directions, out to a caller-supplied number of hops (`/graph/dependents` and `/graph/dependencies`, both `?kind=&id=&at=&hops=`).

The walk moves level by level — breadth-first, one query per hop, over only the entities the previous level reached and had not already visited — rather than asking the database to enumerate every path through a `*1..hops` variable-length pattern in one query. That distinction matters specifically because relationships are never deleted: a node with a long history behind it, or genuine fan-out (a handful of shared services with hundreds of callers each), can carry far more edges than it has distinct neighbors, and a variable-length pattern walks every one of those edges as a separate path before it can collapse them back down to "reached, at its shortest distance." The cost of that is exponential in the branching a query happens to meet; a level-by-level walk with a visited set costs work proportional to the frontier's actual size instead, at the price of up to eight round trips to the database instead of one — cheap next to a query that might never return.

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/bounded-graph-walk.svg')} alt="A breadth-first walk fanning out from a hub entity: six reached callers plus a placeholder for thousands more at hop 1, a maxWalkResults barrier once the cap fires, a grayed-out ghost region for hop 2 and beyond that is never queried, and a panel showing malformed hops input all clamped to 200 OK" />
  <figcaption className="diagram-caption">Three independent bounds, not one. The result cap can fire while hops would still allow more levels — hop 2 is never "disallowed," it's simply never reached, because the walk already stopped for an unrelated reason.</figcaption>
</figure>

Three independent limits keep any single call bounded regardless of what the estate looks like:

- **Hops** are clamped to 8 regardless of what a caller asks for — including nonsense input (empty, negative, non-numeric, absurdly large) at the HTTP layer, which falls back to the documented default rather than erroring.
- **Results** stop growing past 4,000 entities: a walk that keeps finding more at every level stops opening further hops and returns what it already has, because answering "what's connected" was never meant to enumerate an entire estate.
- **Wall-clock time** is capped across the whole call, not just each individual round trip to the database — a walk this deep makes up to ten round trips in total (the hop levels, plus the existence check and the final batch fetch), and being merely unlucky on every one of them could otherwise take a single request's own timeout multiplied by that count. Once the overall budget fires, the in-flight call fails immediately with a clear deadline error rather than quietly running past what a caller would consider reasonable.

None of the three changes what a walk finds within them — only what happens once an answer would otherwise have gotten implausibly large or slow, which is precisely the situation a real, long-lived, historical graph eventually reaches and a small demo one never does.

## Without Neo4j

Everything above needs the temporal graph specifically — it is the one place `validFrom`/`validTo` windows, `EXPLAINS` edges and multi-hop walks exist. The server does not require it: Neo4j is optional (bundled by the chart, or an external instance you already run), and without it, topology snapshots, events and the audit trail still live in SQLite exactly as they always have — the server works the same either way, just without Timeline's "why" or a Dependents/Dependencies answer. If Neo4j is briefly unreachable, nothing is lost: events buffer in SQLite and drain into the graph once it is back, and every graph write in this server is written to be best-effort and silent on its own failure, on purpose — the durable record of *that* something changed is never the graph's to guarantee, only the richer *why* and *what's connected* views built on top of it.

## Where this ends up

[Observability intent](./observability-intent.md) is the other half of this: every tier change, pause and exclusion an administrator makes is itself recorded through `RecordEntity`, with an event and an `EXPLAINS` edge, the same as anything else on this page. An agent's own Timeline entry looks exactly like a service's for that reason — not because agents and services are alike, but because "record what changed, and say why" was built once and used everywhere.

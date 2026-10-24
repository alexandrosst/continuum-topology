---
id: data-flow
title: From facts to what you see
description: How what agents observe and what people declare are kept apart, combined into the effective model, and kept as history.
---

# From facts to what you see

Ikhnos's model of an estate has two authors. **Agents** report what is true now: clusters, nodes, namespaces, workloads, flows. **People** say what should be true: sites, devices, links they know about, policies, and corrections to something observed. The twin is the rule that keeps those two apart, combines them when something asks, and never lets one pass for the other.

<Figure name="FactsToViews" caption="Two inputs, one combination step, four readers. Everything below the combination is read-only." />

## The path of one fact

A node reports a fact. It travels a short, fixed route.

1. The agent's sync arrives at the **hub**, which keeps one view per agent behind one lock.
2. `interpret` turns the raw facts into clusters, nodes and applications. This is the **observed topology**. Each record carries its value and when it was last seen. Its *state* (live, disconnected, stale, revoked, gone, or declared for something nobody observes) is worked out from the clock whenever it is asked for, never stored, so it cannot be out of date.
3. Separately, a person's edits go into the **workspace**, which holds declared intent only. A discovered cluster or node is *not* in the workspace. If a person corrected something about one, the workspace keeps a **ref**, keyed by the record's stable id, and applies it again whenever the record returns.
4. `twin.Build` combines the two **on request**. The result is the **effective model**.

## The effective model

Every value in the effective model says where it came from (declared, observed or computed), how old it is, and how sure the model is. A person's value wins over an observed one, and is flagged when what is observed has since moved, so a correction never hides a change in the world.

Four things read the effective model, all through the same path: the model API, placement advice, your decider, and the web UI. Because they share the path, the UI cannot show a cluster as a target that advice would refuse.

## Why observed and declared are kept apart

It is a design decision with a history, and the history is worth knowing.

An earlier version saved everything the browser had merged into one document. Discovered facts were then saved as if a person had written them. They ended up in exports and in revisions, where they aged without anyone noticing: a file could say a cluster had twelve nodes months after it had three. A stale cluster could be offered as a place to run a workload. A renamed node became a new node and lost its history.

The twin fixes those by rule:

- **The workspace has no observed records.** It cannot go stale, because it holds nothing that can.
- **Freshness is computed, not stored.** A record is stale when its agent has missed four heartbeats (the window comes from Settings). A stale or revoked cluster is not a valid placement target, and the placement page says why it was left out.
- **Identity is stable and not a name.** A cluster is identified by the UID of its `kube-system` namespace, which the agent pins at enrollment. A workload is `cluster/namespace/kind/name`. An identity registry remembers nodes, so the same machine under a new name keeps its record and its history.
- **Records that vanish become tombstones** on the server, so "gone" is a fact with a time and not an absence. A cluster is never "gone" on its own account: it becomes stale, or revoked.

The long form, with what was deliberately not done, is `backend/docs/twin-design.md` in the repository, and the wire contract of the model is `backend/docs/model-api.md`.

## History

Everything above describes now. History is the same model across time.

<Figure name="EntityVersions" caption="An entity keeps its identity. A change closes a version and opens the next one. Nothing is edited in place." />

Each entity has a stable identity. A version is one period during which it looked a certain way. When something changes, the open version is closed and a new one is opened, and an event records *why*, so "what did this look like an hour ago" is a filter on a timestamp.

History is kept in SQLite (snapshots and events). If you run Neo4j, a temporal graph is kept as well, and questions like "what depends on this" become walks over it.

<Figure name="BoundedWalk" caption="A walk over dependents is bounded three ways, so one request cannot take down the service." />

Such a walk is bounded on purpose. The hop count is clamped to 8, results stop growing at 4,000 entities, and the whole call has one deadline. An estate cannot make a request run away. [System memory](../architecture/system-memory.md) has the details, including how it works without Neo4j.

## Where to look

| To understand | Read |
|---|---|
| How raw facts become records | `backend/internal/interpret` |
| How declared and observed combine | `backend/internal/twin` and `twin-design.md` |
| What the hub holds | `backend/internal/estate` |
| The wire shape of the effective model | `model-api.md` |

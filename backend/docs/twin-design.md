# The twin: declared and observed, kept apart

Continuum's model of an estate has two authors. **People** say what should be true: sites, devices, links they
know about, policies, the applications they accepted, values they corrected. **Agents** report what is true
right now: clusters, nodes, namespaces, workloads. The twin is the rule that keeps these apart, combines them
on request, and never lets one pass for the other.

This document says what the design is, why, what changed from what existed before, and what was deliberately not
done. The wire contract of the combined model is in [model-api.md](model-api.md); how placement advice turns that
model into a three-valued verdict with its confidence is in [advice.md](advice.md); backups are in
[../../deploy/BACKUP.md](../../deploy/BACKUP.md).

## What existed before

Agent facts reach the server as `facts.State`, are turned into a `model.Topology` by `interpret.Interpret`, and are
served in the state document. The browser merged that topology into its own model with `mergeDiscovered`, and the
**whole browser model was the workspace document**: what it saved to the server, what Export wrote, what
workspace revisions (SQLite and Neo4j) recorded. The consequences, all confirmed by reading the code:

* Discovered facts were saved as if a person had written them. Every observed value ended up in exports and in
  revisions, where it aged silently: a file could say a cluster had 12 nodes months after it had 3.
* A record's freshness was a boolean (`stale`) that only the Discovery page looked at.
* **Stale clusters were valid placement targets.** `moveTargets` (Mobility panel and the placement engine) and
  `buildWorld` filtered clusters only by `deletedAt`. A cluster whose agent had been silent for a day, or had
  been revoked, was offered as a place to run workloads, and the decider request listed it as a candidate.
* Node identity was the node's name (hashed with the cluster). A renamed machine became a new node and lost its
  history; a replacement that reused a name inherited the old machine's id.
* A record that vanished from an agent's report was flagged `deletedAt` in the browser, and nowhere else.

The audit note "identity keys weak" was checked. It is true for nodes and not for the rest: a cluster's identity
is the UID of its `kube-system` namespace (which agents pin at enrolment; sound), and a workload's is
`cluster/namespace/kind/name` (sound; the wire carries no workload UID, which only an agent change could add).

## The design

```
 agents ──facts──▶ hub ──interpret──▶ observed topology ─┐
                    │                                     ├──▶ twin.Build ──▶ effective model ──▶ /model, decider, UI
 people ──edits──▶ workspace (declared only, refs) ──────┘
                    │
                    └── tombstones, identities, model version (SQLite)
```

1. **The workspace is declared intent only.** Format version 4 holds sites, devices, declared links,
   applications, decisions (accepted and dismissed suggestions), policies, annotations, adoptions and overrides.
   A discovered cluster, node, namespace or service is **not** in it. What a person said about one (values they
   overrode, the application or site they assigned) is kept as a **ref** keyed by the record's stable id:
   `"refs": { "sv-21a802a8b1c5": { "kind": "service", "applicationId": "app-shop" } }`. The ref survives while the
   record itself comes and goes, and is applied again when it returns.
2. **Observation lives on the server**, per record, as a state and a timestamp; never in the workspace.
3. **The effective model is computed, not stored.** `twin.Build` takes the observed topology, the declared
   workspace, the tombstones and the identity registry, and produces entities with per-attribute provenance.
   The same input gives the same output.
4. **Only live things are targets.** Placement, the deciders and mobility ask the model (server side) or the same
   pure function (browser side, `src/lib/provenance.ts`) and treat non-live clusters as excluded, with the reason.

### Observation states

A state is a function of the clock, never stored: `live`, `disconnected`, `stale`, `revoked`, `gone`, plus
`declared` for records nobody observes. Precedence: revoked, then stale, then disconnected, then live. The
staleness window is `staleAfterBeats × heartbeat` from Settings (default 4 × 30 s). The definitions are in
[model-api.md](model-api.md#states). The browser gets the state on every record (`state`, `stateReason`) and the
old `stale` boolean stays (`stale` is true for stale and revoked) so nothing that reads it breaks.

**Revoked agents.** Revoking removes the agent's ability to connect; it does not erase what it reported. Its last
picture stays in the state document, marked `revoked`, for the retention window, and is excluded from flows and
from placement. If an approved agent now reports the same cluster, the revoked one is not shown (the newer agent
replaced it). Across a restart the hub reloads the pictures of revoked agents within retention.

**Tombstones.** When a record disappears from an agent's picture (a full sync without it, or an explicit delete),
the server writes a tombstone: kind, id, name, cluster, agent, when, why, and the record as last known. Tombstones
are kept 7 days (`Hub.TombstoneRetention`; `0` means the default) and swept by the recorder tick; they are capped
at 5000 per organisation, oldest first. They are shown as `gone`, used to explain changes in history and events,
and never as a target. If the record comes back under the same id it is revived and the tombstone removed.
Three cases are deliberately not "deleted": a **Job** (finishing is what jobs do), system machinery, and records an
agent stops reporting because its access tier was **lowered** (consent narrowing; the reason says so).

### Stable identity

| Entity | Identity | Note |
|---|---|---|
| organisation | its id | every other id is derived from it, so two organisations can never share one |
| cluster | UID of `kube-system` | a cluster re-created under the same name is a different cluster (the model warns when two clusters share a name and merges nothing) |
| node | provider id, else system UUID, else machine id, else name | provider ids that merely repeat the node's name (`k3s://name`, `kind://…/name`) are ignored; a value that two nodes of one cluster share (cloned images) is distrusted for both; only a digest is stored |
| workload | `cluster/namespace/kind/name` | no workload UID on the wire |

Node record ids stay what they always were the first time an identity is seen, so existing workspaces keep
working. After that the registry (`identities` table) remembers them: the same machine under a new name keeps its id
and gains an alias (a rename is not a new node); a *different* machine that takes a name another machine used
gets a **new** id (a replaced node is not the old one). Tests prove there is no collision between two clusters
with the same node and workload names, or between two organisations (`internal/twin`, `internal/server`).

### Precedence

Declared beats observed for the same attribute; the observed value stays visible as `shadowed`. Observed
attributes each have one source. Unknown is a value: a cluster's allocatable CPU is unknown unless every node
reported its own, and an unknown value is `null` with the reason in `evidence`, never zero.

### Placement and the decider

A cluster is an eligible target when it is `live` (or declared) and, if observed, its allocatable capacity is
known. Everything else is excluded **with the reason**: "stale for 2 h", "agent revoked 3 h ago", "agent
disconnected; last heard 5 s ago", "capacity unknown: …".

* Browser: `moveTargets` adds a blocker with the reason and marks the target `excluded`; `recommend` skips services
  whose own cluster is not live (what runs in a cluster nobody is watching is not known); the Placement page lists
  the excluded clusters with their reasons; the What-if target list disables them.
* Server: `POST /decide` builds the effective model, adds `modelVersion`, `excluded[]` and per-cluster state to the
  request, and **removes excluded clusters from every service's `candidates`** whatever the client sent. If the
  model cannot be built the request fails closed. Only fields are added (see model-api.md), so an existing
  decider keeps working.

## Formats and migrations

Two things are versioned, separately, and both refuse to go backwards.

| What | Where | Version now | Older | Newer |
|---|---|---|---|---|
| Database schema | `PRAGMA user_version` | 4 | migrated forward in place, once, in a transaction | **refused at start-up** with a message, database untouched |
| Workspace document | `schemaVersion` in the document | 4 | `workspace.Declare` rewrites it (1–3 carried observed records) | **refused** on save and on import, with a message; nothing is changed |
| Backup archive | `manifest.json` `format` | 1 | – | refused on restore |

Schema 4 adds `tombstones`, `identities` and `model_state` tables and a `note` column on the workspace, then
rewrites every stored workspace into the declared-only form. A workspace that cannot be read is left exactly
as it was. The person who next opens a migrated workspace sees a note saying what was removed ("Removed 14
discovered records (2 clusters, 3 nodes, …) from the workspace: … Your 2 overrides and assignments on discovered
records were kept."). Old workspace revisions in Neo4j are not rewritten; they are stripped, with the same note,
when they are read (`/workspace/at`). Revisions written from now on are declared-only. Migrations are
forward-only and numbered (v4 onwards); a later change adds `migrateV5` and raises `SchemaVersion`.

The browser applies the same rules (`src/lib/declared.ts`, the mirror of `internal/workspace`): Export writes the
declared workspace, saves send it, and Import strips an old file with the same note and refuses a newer one.
Discovered records the browser holds (fetched from the server, cached in local storage for the sake of a fast
first paint) are never sent anywhere.

The model version (`modelVersion`) is persisted in `model_state` whenever the content changes, so it does not
restart from 1 after a restart, and clients that compare versions never see it go back.

## What was deliberately not done

* **No cluster tombstones.** A cluster is never "gone" on its own account: it becomes stale, or revoked. Deleting
  a cluster is a person's decision made in the workspace.
* **No workload UID.** The agent protocol does not carry one and the agent is outside this change; a workload
  deleted and recreated under one name is the same service.
* **Revoked pictures are not purged.** After the retention window they stop being shown; the rows are not
  deleted, and the audit log is untouched.
* **Neo4j history is unchanged.** Snapshots of the observed estate over time are observation, and history is
  their purpose. Only *workspace revisions* had to become declared-only.
* **The browser's local cache still holds discovered records** (so the first paint after a reload is not empty).
  It is per browser, never exported, never saved to the server, and replaced by the next poll.
* **The model is served, not streamed.** Consumers poll with `If-None-Match`.
* **No proto changes.** Everything here derives from what agents already send.

## Where things are

| | |
|---|---|
| `internal/workspace` | the declared document, `Declare`, format versions |
| `internal/twin` | `Assess` (states), identity registry, tombstones, `Build` (the effective model), `Exclusions` |
| `internal/store/twin.go`, `backup.go` | schema 4, its tables, the schema check, consistent database copy |
| `internal/server/twin.go` | the runtime in the hub, `Model`, the `/model` handler, decision enrichment |
| `cmd/server/backup.go` | `server backup`, `server restore` |
| `src/lib/declared.ts`, `provenance.ts` | declared/observed split and state/evidence rules in the browser |
| `src/components/EvidenceSection.tsx`, `Observations.tsx`, `placement/Excluded.tsx` | Evidence, gone records, exclusions |

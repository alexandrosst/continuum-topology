# The effective model API

`GET /api/v1/orgs/{org}/model` returns what Continuum currently believes about an organisation's estate: what
agents observe and what people declared, combined by explicit rules, with the provenance of every value. It is
the one place a consumer (an external decider, a script, a dashboard) can read "what is true, and how sure are
we" without re-implementing discovery.

* **Who may call it:** any member of the organisation (viewer or above), with a session cookie or, for
  tooling, the same credentials as the rest of the admin API. Organisations are isolated: the model of one is
  never visible through another's path.
* **Contract:** `continuum.model/v1`. The string changes only for an incompatible change. Adding fields does not
  change it, so **readers must ignore fields they do not know**.
* **It reads; it never changes anything.** Requests do not make agents do anything.

## Versioning and caching

| Field / header | Meaning |
|---|---|
| `modelVersion` | An integer that goes up by one every time the model's *content* changes (a record appears, disappears or changes value; a state changes; a person saves the workspace). It **never goes down**, including across a server restart (the last version is stored). Ages that only advance with the clock ("stale for 2 h" turning into "stale for 3 h") do not bump it; the change of state itself does. |
| `generatedAt` | When this document was computed (UTC, RFC 3339). Not a change marker: use `modelVersion` or the ETag for that. |
| `ETag` | `"m<version>-<12 hex>"`, for example `"m5-568fbcca9f17"`. Derived from the version and a fingerprint of the content. |
| `If-None-Match` | Send the last ETag; the server answers `304 Not Modified` with no body while nothing changed. A list of ETags and `*` are understood. |
| `Cache-Control` | `no-cache`: caches must revalidate, and revalidation is cheap. |

The server builds a model at most every two seconds per organisation and reuses it in between; a sync from an
agent, a revocation or a workspace save invalidates it at once. Polling once a second with `If-None-Match` is
fine.

## The document

```json
{
  "contract": "continuum.model/v1",
  "modelVersion": 5,
  "generatedAt": "2026-09-21T03:17:16Z",
  "observation": { "staleAfterSeconds": 120, "tombstoneRetentionDays": 7 },
  "entities": [ ...Entity ],
  "warnings": [ "2 clusters are called \"edge\" (cl-1, cl-2): they are different clusters (different identities) and are kept apart" ]
}
```

* `observation` states the rules the states were computed with, so a reader can explain them.
* `warnings` are things the server noticed that a person may want to look at (two clusters with one name; two
  records claiming one id). They never block anything. Empty is `[]`, never `null`.
* `entities` are ordered by kind (site, cluster, node, namespace, application, service, device, external, path,
  dependency) and then by id, so the same estate always produces the same document.

### Entity

| Field | Meaning |
|---|---|
| `kind` | `site`, `cluster`, `node`, `namespace`, `application`, `service`, `device`, `external`, `path`, `dependency` |
| `id` | The stable id used everywhere else in the API. |
| `name` | A display name. |
| `clusterId` | For nodes, namespaces and services: the cluster they belong to. |
| `origin` | `observed` (an agent reports it), `declared` (a person authored it), or `observed+declared` (an observed record a person overrode or assigned). |
| `state` | See below. `declared` for hand-authored records, which nobody observes and which therefore have nothing to be stale about. |
| `stateReason` | A sentence for a person when the state is not `live` ("stale for 2 h", "agent revoked 3 h ago", "deleted in the cluster"). It contains ages, so it changes with the clock. |
| `stateSince` | When the entity entered this state, when known (for `gone`, when it disappeared). |
| `lastObservedAt` | The last time the owning agent vouched for it (a sync or a heartbeat). |
| `goneAt` | Only for `gone`: when it disappeared from its agent's picture. |
| `agentId` | The agent that observes (or observed) it. |
| `identity` | What makes it the same entity over time. See below. |
| `attributes` | A map from attribute name to **Attribute**. |

### States

| `state` | Meaning | May anything be placed on it or moved to it? |
|---|---|---|
| `live` | Its agent is connected and was heard from within the staleness window. | yes |
| `disconnected` | The agent's stream is closed but it was heard from within the window. The picture is probably still right; nothing new is arriving. | no |
| `stale` | Silent for longer than the window (connected or not). The record is the last known state, not the current one. | no |
| `revoked` | An administrator revoked the agent. Its last picture is kept for the retention window, marked revoked, and is never a target of anything. | no |
| `gone` | It disappeared from what its agent reports. A **tombstone**, kept for the retention window (default 7 days) so history and events can refer to it. Its `attributes` are empty. If it comes back under the same id it is `live` again. | no |
| `declared` | Authored by a person. | yes, if its capacity is stated |

The staleness window is `staleAfterBeats × heartbeat` (defaults 4 × 30 s = 120 s, configurable in Settings). The
precedence when several apply is: `revoked`, then `stale`, then `disconnected`, then `live`.

When an agent's access is narrowed by consent (the approved tier is lowered), the records it stops reporting
become `gone` too, but the `stateReason` says why ("access tier lowered: nodes are no longer reported"), so nobody
reads a change of permission as something being deleted in the cluster.

### Identity

```json
"identity": { "basis": "kube-system-uid", "key": "d4e914e4-bc6c-4fcd-bcaf-ef86beeb8080" }
```

`basis` is what the identity is made of:

| `basis` | Entity | Notes |
|---|---|---|
| `kube-system-uid` | cluster | The UID of the `kube-system` namespace. A cluster re-created under the same name is a **different** cluster. |
| `provider-id` | node | The cloud instance id. Ignored when it only repeats the node's name (`k3s://name`). |
| `system-uuid` | node | The SMBIOS product UUID. |
| `machine-id` | node | `/etc/machine-id`. Two nodes of one cluster that report the same value are both distrusted (cloned images). |
| `name` | node | Nothing better was reported. The node's name is its identity. |
| `workload` | service | `cluster/namespace/kind/name` in `key`. The wire carries no workload UID, so a workload deleted and created again under the same name is the same service. |
| `declared` | declared records | The id the person's document gave it. |

Node identifiers other than the name are never shown or stored, only a digest of them. `aliases` lists earlier
names of a node that was renamed (same machine, new name: same entity). `note` explains anything odd, such as
another cluster carrying the same name. Every id is derived from the organisation as well, so two
organisations can never share an id.

### Attribute

```json
"cpuAllocatable": {
  "value": 4,
  "unit": "cores",
  "source": "agent",
  "agentId": "ag-ecd401f35875",
  "confidence": "reported",
  "observedAt": "2026-09-21T03:05:40Z",
  "state": "live"
}
```

| Field | Meaning |
|---|---|
| `value` | The value, or **`null` when `confidence` is `unknown`**. Unknown is never zero, empty or false. |
| `unit` | Explicit for anything with one: `cores`, `bytes`, `count`, `ms`, `bytes/s`. Quantities are converted to these (millicores to cores, MiB to bytes), never left for the reader to guess. Absent for values without a unit (names, strings, flags). |
| `source` | `agent` (read from the Kubernetes API), `probe` (read from the machine by the node probe), `measured` (timed or counted by the agent), `inferred` (derived by the server from other facts), `declared` (stated by a person). |
| `agentId` | Which agent, for `agent`, `probe` and `measured`. |
| `confidence` | `measured`, `reported`, `inferred`, `guess` or `unknown`; see below. |
| `observedAt` | The last time the source vouched for the value. Empty for declared values. |
| `state` | The entity's state, repeated so one attribute can be judged on its own. |
| `evidence` | The signal behind an inferred value, or **why** a value is unknown. |
| `shadowed` | When a person declared a value over an observed one: the observed attribute, so nothing is lost. |

| `confidence` | Meaning |
|---|---|
| `measured` | Read from the thing itself by an instrument: a node probe, a timed connection, counted traffic. |
| `reported` | An agent read it from the Kubernetes API, or a person stated it. Taken at its word. |
| `inferred` | Worked out by a rule from other facts. `evidence` names the signal. |
| `guess` | Inferred from weak signals. Likely to be wrong sometimes. |
| `unknown` | Not known. `value` is `null`. |

### Precedence

1. What a person **declared** about an entity beats what was observed for the same attribute. The observed value
   stays visible under it as `shadowed`. A declaration on something that is not (or is no longer) observed
   changes nothing (it waits for the entity to exist).
2. Observed attributes are not blended: each has exactly one source. Where the node probe supplied the evidence
   (for instance the machine kind), the attribute is `source: probe` and is rated `measured` if the probe was
   sure of it; a value the server worked out from the Kubernetes API is `inferred`, or a `guess` when its signal
   is weak (a "VM" that only means "no hardware signal seen").
3. **Unknown** is a value: a cluster's `cpuAllocatable` is unknown unless every node reported its own. The
   model never sums a partial set and calls it a total.

## Example

Abbreviated from a real response (a single-cluster k3s estate whose agent was later revoked, plus one declared
cluster). Attribute lists are cut to a few entries.

```json
{
  "contract": "continuum.model/v1",
  "modelVersion": 5,
  "generatedAt": "2026-09-21T03:17:16Z",
  "observation": { "staleAfterSeconds": 120, "tombstoneRetentionDays": 7 },
  "entities": [
    {
      "kind": "cluster",
      "id": "cl-a287110206",
      "name": "twin",
      "origin": "observed",
      "state": "revoked",
      "stateReason": "agent revoked 9 min ago",
      "lastObservedAt": "2026-09-21T03:05:40Z",
      "agentId": "ag-ecd401f35875",
      "identity": { "basis": "kube-system-uid", "key": "d4e914e4-bc6c-4fcd-bcaf-ef86beeb8080" },
      "attributes": {
        "version":  { "value": "v1.30.5+k3s1", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "reported", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked" },
        "nodeCount": { "value": 2, "unit": "count", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "reported", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked" },
        "tier": {
          "value": "edge", "source": "inferred", "confidence": "guess", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked",
          "evidence": "not a public cloud (on-prem and edge share this default; change it if it is a data center)"
        },
        "region": {
          "value": null, "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "unknown", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked",
          "evidence": "no node carries a region label"
        }
      }
    },
    {
      "kind": "node",
      "id": "nd-6722c8b13bec",
      "name": "sandbox-1",
      "clusterId": "cl-a287110206",
      "origin": "observed",
      "state": "revoked",
      "stateReason": "agent revoked 9 min ago",
      "identity": { "basis": "name" },
      "attributes": {
        "cpuAllocatable": { "value": 2, "unit": "cores", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "reported", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked" },
        "memoryAllocatable": { "value": 8422264832, "unit": "bytes", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "reported", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked" },
        "kind": {
          "value": "vm", "source": "inferred", "confidence": "guess", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked",
          "evidence": "no hypervisor or hardware signal in the Kubernetes API (could be bare metal; the node probe can decide)"
        }
      }
    },
    {
      "kind": "service",
      "id": "sv-21a802a8b1c5",
      "name": "db",
      "clusterId": "cl-a287110206",
      "origin": "observed",
      "state": "revoked",
      "identity": { "basis": "workload", "key": "cl-a287110206/shop/StatefulSet/db" },
      "attributes": {
        "replicas": { "value": 1, "unit": "count", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "reported", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked" },
        "cpuRequest": {
          "value": null, "unit": "cores", "source": "agent", "agentId": "ag-ecd401f35875", "confidence": "unknown", "observedAt": "2026-09-21T03:05:40Z", "state": "revoked",
          "evidence": "no CPU request is set, so nothing is reserved and the need is not known"
        }
      }
    },
    {
      "kind": "service",
      "id": "sv-dd3318693c26",
      "name": "twin-gone",
      "clusterId": "cl-a287110206",
      "origin": "observed",
      "state": "gone",
      "stateReason": "deleted in the cluster",
      "stateSince": "2026-09-21T03:05:54Z",
      "lastObservedAt": "2026-09-21T03:05:51Z",
      "goneAt": "2026-09-21T03:05:54Z",
      "agentId": "ag-ecd401f35875",
      "attributes": {}
    },
    {
      "kind": "cluster",
      "id": "cl-declared",
      "name": "declared-b",
      "origin": "declared",
      "state": "declared",
      "identity": { "basis": "declared" },
      "attributes": {
        "tier": { "value": "cloud", "source": "declared", "confidence": "reported", "state": "declared" }
      }
    }
  ],
  "warnings": []
}
```

A `304` looks like this:

```console
$ curl -si -H "If-None-Match: \"m5-568fbcca9f17\"" https://continuum.example/api/v1/orgs/org-aa04dadc3067/model
HTTP/1.1 304 Not Modified
ETag: "m5-568fbcca9f17"
Cache-Control: no-cache
```

## Errors

| Status | When |
|---|---|
| `401` | no or expired session |
| `403` | not a member of the organisation |
| `404` | no such organisation, or one the caller is not a member of (the API does not say which) |
| `500` | the model could not be built; the body says so. Nothing is served from a half-built model. |

## The placement decider request

The external decider (`POST /api/v1/orgs/{org}/decide`, editor or above) is fed from this model. The request the
browser builds is forwarded as it is, and the server **adds fields only**: nothing is renamed or removed, so a
decider written against the earlier contract keeps working, and one that reads the additions can do better.

The formal version of everything below - the exact request and response shapes, and the optional HMAC request
signing scheme - is [`decider-webhook.openapi.yaml`](decider-webhook.openapi.yaml) (OpenAPI 3.1, described as a
`webhooks` operation since the server is the one making the call). This section stays as the prose walkthrough;
the schema is kept by hand against the same TypeScript (`src/lib/placement/deciders.ts`) and Go types described
here, not generated or cross-checked by a test the way [advice.md](advice.md)'s vectors are - if the two ever
disagree, this prose and the code it describes are the source of truth.

| Addition | Where | Meaning |
|---|---|---|
| `modelVersion` | request | The version of the effective model the check was made against. |
| `excluded` | request | `[{ "cluster", "name", "state", "reason", "kind" }]`: the clusters that are not eligible targets, with why (`"stale for 2 h"`, `"agent revoked 3 h ago"`, `"capacity unknown: ..."`) and `kind` (`"not-live"` or `"capacity-unknown"`, additive). Always present, `[]` when none. |
| `excluded[].fix` | request | For `kind: "capacity-unknown"`: what would make it known (`{ "action", "text", "link" }`). |
| `clusters[].state` | request | The cluster's state in the model. |
| `clusters[].eligible` | request | Whether workloads may be placed on it. |
| `clusters[].stateReason` | request | Why not, when not. |
| `clusters[].free` | request | `{ "cpu", "memory" }`, each a fact ([`FactDoc`](advice.md), server-computed) about the cluster's own free capacity, with `value` (`null` if not known), `low`/`high` (the pessimistic/optimistic bounds), `confidence`, `observedAt` and `aged`. See [advice.md](advice.md). |
| `services[].clusterState` | request | The state of the cluster the service runs in. |
| `services[].capacity` | request | For a movable service: `[{ "cluster", "name", "verdict", "confidence", "dimensions", "facts", "wouldChange", "fixes", "reasons" }]`, the **server's own** three-valued verdict for every live or declared cluster the service does not already run in (see [advice.md](advice.md)). Capped at 4000 entries across the whole request; `capacityTruncated: true` is added when the cap was hit. |
| `services[].undecided` | request | Clusters where the server's own verdict is `cantTell` (unioned with whatever the client already believed was undecided): not candidates, but not ruled out either. |
| `services[].candidates` | request | **Filtered twice**: clusters that are not eligible, and clusters the server's own capacity check does not certify as `fits`, are both removed, whatever the client believed. A decider is only ever offered targets both sides agree are safe. |

Eligible means: `live` (or declared). Capacity is judged separately (`clusters[].free`, `services[].capacity`):
an unknown is not the same as ineligible, so a cluster with unknown room stays a target, just not a candidate until
its verdict is `fits`. If the model cannot be built the request fails closed with a `500` rather than being
forwarded unfiltered. The browser computes the same rules from its own copy of the model before it ever builds the
request (`src/lib/advice.ts`; `services[].candidates` and `services[].undecided` in the request the browser sends
already reflect its own verdicts), so the two agree; where they must differ because the server does not see a
workload's node selectors, the server may only ever narrow `candidates` further, never widen them.

All of this is additive: an existing decider that reads only `candidates`, `excluded[].reason` and `clusters[].freeCpu`/`freeMemGb` keeps working exactly as before. `freeCpu`/`freeMemGb` are now sent only when every node of the cluster reports enough to compute them exactly (`exact`, not merely `known`); read `clusters[].free` for the fact with its interval and confidence instead.

A decider's answer is not changed: `{ "schema": 1, "recommendations": [{ "serviceId", "to", "reason" }] }`.
Unknown fields in the answer are ignored, and a recommendation for a cluster that was excluded, or that the
server's own capacity check does not certify, is dropped and reported by the browser rather than shown.

See [advice.md](advice.md) for the uncertainty rules behind `verdict`, `confidence`, `facts` and `wouldChange`.

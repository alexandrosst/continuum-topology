# The decision loop for agent access

An **advisor** (any program: a rule set, a model, a script) looks at what Ikhnos knows, for example through the
[effective model API](model-api.md), and **proposes** a change to one agent's access. A person decides. If the
proposal is accepted, the change is made by the same code a manual change uses, and later the server records an
**Outcome**: what the advisor predicted beside what was observed.

* **An advisor never changes anything.** Submitting a proposal stores it as pending and touches nothing else.
* **Policy decides, not the advisor.** Whether a proposal widens what an agent shares is worked out by the
  server, never taken from the advisor. Accepting a widening takes an administrator; narrowing takes an editor.
* **Same path, same audit.** An accepted change is made by the code behind `POST .../agents/{id}/tier` and
  `.../consent`, so validation, delivery to the agent and the audit trail are identical to a manual change.

Every route needs an editor and is under `/api/v1/orgs/{org}`. An advisor is a client holding an editor's
personal API token (`Authorization: Bearer ...`). Bodies refuse fields they do not know.

## A proposal

```
POST /proposals
{
  "advisor": "rules-v1",            // who is proposing; shown to the person deciding
  "modelVersion": 42,               // the model version it reasoned over (recorded, not checked)
  "change": { "agentId": "ag-...", "pause": ["flow"] },
  "evidence": [ { "name": "flow bytes", "value": "12 MB/s", "class": "measured",
                  "observedAt": "2026-10-16T09:00:00Z", "source": "heartbeat" } ],
  "predicted": { "link_bytes_per_sec": -0.5 },
  "ttlSeconds": 14400               // how long it may wait for a decision; default 4 h, at most 72 h
}
```

**The change is typed, never free text, and is exactly one of two kinds.** A new approved tier
(`"tier": 1`), or changes to the agent's overrides as additions and removals: `pause` and `resume` (collectors:
`probes`, `flow`, `measure`), `exclude` and `include` (namespaces). Additions and removals apply to what the
agent has *when the proposal is accepted*, not when it was made, so a proposal cannot undo a change someone made
in between. A change that leaves the agent as it is, that the install does not allow (above `access.tier`), or
that names a system namespace, is refused when it is submitted.

**Evidence** is up to 20 facts, each with a class: `measured`, `derived`, `declared` or `unknown`. The
proposal's `confidence` is the weakest class among them.

**Predicted** names the effect the advisor expects, as a relative change per metric: `-0.5` halves it,
`0.2` raises it by a fifth. The metrics are `link_bytes_per_sec` (the agent's traffic to the server),
`agent_cpu_cores` and `agent_rss_bytes` (the agent's own load).

The server adds `widens`, `blast` (what the change touches), `mode` (always `advise` in this release),
`status`, `createdAt` and `expiresAt`. At most 20 proposals per agent and 200 per organisation may wait at once.

## Life of a proposal

| Status | Meaning |
|---|---|
| `pending` | Waiting for a person. Becomes `expired` when `expiresAt` passes; an expired proposal can never be accepted. |
| `rejected` | A person said no. Nothing changed. |
| `accepted` | Claimed by one person; the change is being made. |
| `applied` | The change was made. |
| `failed` | The change could not be made; `note` says why. |

`GET /proposals?status=&agent=&limit=` lists them, newest first. `GET /proposals/{id}` returns one, with its
Outcome once it has one. `POST /proposals/{id}/accept` and `/reject` take `{"note": "..."}` (at most 200
characters). Accepting:

1. reads the agent again and checks the change against it as it is now. A proposal made against an old state may
   no longer be valid (refused), or may now widen what it narrowed (then it needs an administrator);
2. claims the proposal with a compare-and-set, so of several people accepting at once exactly one makes the
   change and the others get `409`;
3. makes the change, then marks the proposal `applied` or `failed`.

Each step writes its audit row in the same transaction as the step: `proposal-submitted`, `proposal-accepted`,
`proposal-rejected`, `proposal-expired`, `proposal-applied`, `proposal-failed`, `proposal-outcome`. If an audit
row cannot be written, the step is not made.

## The Outcome

Once an applied change is **in force**, meaning the agent has reported (in its heartbeat) that it applied the
Config the server sent after the decision, the server compares the agent's own load before and after, from the
self-telemetry its heartbeats carry. Each window is five minutes (the one before the decision, the one after the
change came into force). The Outcome has one of three results:

| `result` | Meaning |
|---|---|
| `measured` | `observed` holds `before`, `after` and the relative `change` of each metric that could be computed. |
| `unconfirmed` | The agent did not report the change in force within 30 minutes of the decision. |
| `no_data` | The change is in force but there were too few samples to compare (the samples are kept in memory, so a server restart in between leaves none). |

An Outcome is written once. Comparing `predicted` with `observed.change` is how a decision method is evaluated
afterwards. The load can also move for reasons unrelated to the change; the Outcome records what was observed,
not a causal claim.

## An example advisor

`docs/examples/advisor.py` is a complete advisor in one file (Python 3, standard library only). Set
`IKHNOS_URL`, `IKHNOS_ORG` and `IKHNOS_TOKEN` (an editor's API token), then:

* `advisor.py run --dry-run` prints what it would propose and sends nothing;
* `advisor.py run` proposes pausing the flow collector on an agent whose CPU has stayed above a threshold, once
  (it skips agents that already have a pending proposal);
* `advisor.py list` and `advisor.py show ID` read proposals back.

It only proposes. A person still accepts or rejects. A test (`advisor_example_test.go`) runs it against the real
handlers.

## The Proposals screen

**Manage → Proposals** lists proposals for editors and administrators (the same roles as the routes). It opens on the ones
waiting for a decision, newest first, and can show the applied, rejected, failed and expired ones. A row reads as a sentence
about a named agent ("Pause the traffic observer on edge-athens") with the advisor, how strong its evidence is, and what it
expects. Opening one shows the evidence fact by fact, the expected effect, and, for an applied one, the Outcome: the expected
change beside the observed one, with the reminder that load can move for other reasons.

* **Accept and apply** and **Reject** take an optional note (at most 200 characters), kept with the decision.
* A proposal that **widens** (the server's word, never the advisor's) can only be accepted by an administrator. An editor sees
  why and can still reject it. The server enforces the same rule; the buttons only say so first.
* If the server refuses a decision (already decided, expired, no longer valid against the agent as it is now), the reason is
  shown and the proposal is read again as it stands.
* The list is read again every 30 seconds while the page is open.

The screen never submits a proposal. An advisor does, with an editor's API token.

The menu shows how many proposals wait for a decision beside **Proposals**, for editors and administrators only (the list carries the
evidence an advisor used, so anyone else is not asked). It is read every minute while the window is visible, and the Proposals page
updates it when it learns something newer, such as after you decide one. It counts at most 100.

## Not in this release

Auto mode (applying a narrowing without a person), other kinds of change (telemetry intents, placement), and any notification
outside the app: today the only signal is the number beside Proposals in the menu.

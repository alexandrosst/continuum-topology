---
id: placement-engine
title: Placement advice and the decision loop
description: How a recommendation is made without pretending to know more than it does, and how a proposal to change an agent's access becomes a change only a person can approve.
---

# Placement advice and the decision loop

Two parts of Ikhnos help people decide: **advice** judges whether a service would fit somewhere else, and the **decision loop** carries a proposed change to an agent's access from an advisor to a person who accepts or refuses it. They share one philosophy: *say what you know, say how sure you are, and let a person decide.*

## How a recommendation is made

<Figure name="PlacementPipeline" caption="Read-only. Every step keeps the source, the confidence and the age of what it used." />

Advice starts from the [effective model](data-flow.md), so it already knows which clusters are live and which are stale or revoked. A stale cluster is not a candidate. It is listed separately with the reason.

For each service and each possible cluster, advice asks three questions: *does it fit, how sure is that, and what would change the answer.* The code is `backend/internal/advice`, and its mirror in TypeScript, `src/lib/advice.ts`, serves the browser. The two are tested against one shared table of cases, generated independently, so they cannot silently drift apart.

### The philosophy

An unknown is not a zero. A guess is not a fact. Advice refuses to certify what it does not know, and every recommendation names the facts it used, with source, confidence and age.

### The rules

**Fit has three values.** `fits`, `doesNotFit`, or `cantTell`. Capacity is worked out as an interval, low to high, for each dimension (CPU cores, memory bytes), from the free capacity of the nodes that could host the workload. It *fits* only when the workload fits at the pessimistic end. It *does not fit* only when it fails even at the optimistic end. Anything between is *can't tell*, with the reason.

**Unknown is not a value.** A node that never reported its allocatable resources contributes nothing certain and pushes the optimistic end open. A workload with no CPU or memory request has an unknown need, and that dimension is "can't tell" outright. The page says so: *the workload sets no CPU request, so what it needs is not known.*

**Five confidence classes.** Each fact carries one, and a class widens the interval by a factor.

| Class | Meaning | Interval widened by |
|---|---|---|
| `measured` | Read from the thing itself by an instrument. | 5 % |
| `reported` | An agent read it from the Kubernetes API, or a person stated it. | 15 % |
| `inferred` | Worked out by a rule from other facts. | 30 % |
| `guess` | Inferred from weak signals. | 50 % |
| `unknown` | Not known at all. | Open bound |

**Staleness demotes a fact by one class.** A fact confirmed longer ago than the staleness window counts as one class worse than it claims. The window is the same one that turns a live record stale.

**Combining takes the weakest.** Several checks combine by severity: `doesNotFit` beats `cantTell` beats `fits`. The confidence of the combined verdict is the weakest class among the parts that produced *that* verdict, never an average, so one guessed input among solid ones is never hidden.

**What would change it.** For every dimension, advice gives the smallest figure of free capacity that would flip the verdict: "if free CPU at cluster X is below 1.18 cores it can no longer be said to fit". Each threshold is checked by applying it and recomputing the verdict from scratch, in the test suite, so the promise is a tested one.

### What is not modelled, honestly

Nothing is `measured` for capacity today. Kubernetes reports allocatable and requested resources through its API, so free capacity is at best `reported`, and `unknown` when the agent's access tier is too low to read pods or nodes. A cluster pool is the sum of its nodes: fragmentation and storage capacity are not judged. A cluster whose capacity is unknown is *not* excluded; it is listed as "can't tell", with the missing fact and the fix. The full rules are in `backend/docs/advice.md`.

## The decision loop

<Figure name="DecisionLoop" caption="An advisor proposes. The server's policy decides what approval is needed. A person decides." />

An **advisor** is any program: a rule set, a model, a script. It reads what Ikhnos knows and **proposes** a change to one agent's access. A person decides. If the proposal is accepted, the change is made by the same code a manual change uses, and later the server records an **outcome**: what the advisor predicted, beside what was observed.

Three rules hold the loop together.

**An advisor never changes anything.** Submitting a proposal stores it as pending and touches nothing else. An advisor is a client holding an editor's personal API token.

**Policy decides, not the advisor.** Whether a proposal *widens* what an agent shares is worked out by the server and never taken from the advisor's word. Accepting a widening takes an administrator. Narrowing takes an editor.

**Same path, same audit.** An accepted change is made by the code behind the manual tier and consent routes, so validation, delivery to the agent and the audit trail are identical.

### What a proposal is

A proposal has an advisor name, the model version it reasoned over, one typed change, up to twenty facts of evidence (each with a class), and the effect it predicts. The change is **typed, never free text**, and is one of two kinds: a new approved tier, or additions and removals to the agent's overrides (collectors to pause or resume, namespaces to exclude or include). Additions and removals apply to what the agent has *when the proposal is accepted*, so a proposal cannot undo a change someone made in between.

The server adds whether the change widens, what it touches, the mode (always `advise` in this release), a status, and an expiry. By default a proposal waits four hours and at most 72.

### Its life

A proposal is `pending` until a person accepts or rejects it, or it expires. Accepting re-reads the agent and checks the change against it *as it is now*, claims the proposal with a compare-and-set so that of several people accepting at once exactly one makes the change, makes the change, and marks it `applied` or `failed`. Every step writes its audit row in the same transaction as the step; if the row cannot be written, the step is not made.

### Outcomes, and why they exist

Later, once the [Config is known to be in force](config-channel.md), the server records what was predicted beside what was observed, keyed to the advisor. The Proposals page then shows, for each advisor, how often it held and how often it missed. The point is to let a person choose which advisor to trust by its record.

The routes, statuses and the body of a proposal are in `backend/docs/decision-loop.md`, and the external decider contract is the OpenAPI file next to it.

## Where to look

| To change | Look in |
|---|---|
| The fit rules or confidence classes | `backend/internal/advice`, and `src/lib/advice.ts` in the same change |
| How the model becomes facts and pools | `backend/internal/server/decide_advice.go` |
| Proposals, statuses and outcomes | `backend/internal/decisionloop` |
| The browser's own movability rules | `src/lib/movability.ts`, `src/lib/capacity.ts` |

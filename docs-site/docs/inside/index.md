---
id: index
title: Inside Ikhnos
description: How Ikhnos is built, for someone who wants to contribute, review it, or remember why a part works the way it does. A tour of the repository and one page for each part.
---

# Inside Ikhnos

This section is for people who work on Ikhnos, review it, or need to remember why a part is the way it is. If you only want to run it, the [journey](../journey/index.md) is the better place to start. If you want the idea in ten minutes first, read [How it works](../learn/how-it-works.md); this section assumes you have.

The pages here follow one rule. Every figure draws the system **as it is in the code today**. Where a figure names a package, a message or a test, it is the one in the repository. Plans and the reasons behind older choices live on [Design history](design-history.md), with dates, so you never mistake a plan for a fact.

<Figure name="SystemAtAGlance" caption="The whole system on one page. Every page in this section opens one box of it." />

## Pick the part you need

<Cards>
  <Card to="/docs/inside/server" kicker="Server" title="The server, in layers">Packages, what may use what, and the tests that hold the lines.</Card>
  <Card to="/docs/inside/agent" kicker="Agent" title="Inside the agent">What runs in the pod, the stream message by message, and what happens when the link breaks.</Card>
  <Card to="/docs/inside/data-flow" kicker="Model" title="From facts to what you see">How observed and declared stay apart, and how history is kept.</Card>
  <Card to="/docs/inside/config-channel" kicker="Control" title="How the server knows what is in force">Config, digest, echo and the three conditions.</Card>
  <Card to="/docs/inside/placement-engine" kicker="Advice" title="Placement and the decision loop">Three-valued fit, confidence, and how a proposal becomes a change.</Card>
  <Card to="/docs/inside/frontend" kicker="Frontend" title="The web UI">Feature layers, wire types and how the UI is tested.</Card>
</Cards>

## A tour of the repository

| Where | What lives there |
|---|---|
| `backend/cmd/` | The programs: `server`, `agent`, `controller`, `probe`, `flow`, and `ikhnos`, the command-line tool. |
| `backend/internal/` | Everything the programs are made of: about sixty Go packages, cut into layers (see [the server](server.md)). |
| `backend/proto/` | The agent protocol, `agent.proto`. Generated code is in `backend/gen/`. |
| `backend/docs/` | Design notes that sit next to the code and change with it: the model API, the twin, advice, the decision loop, the module map. |
| `src/` | The web UI: `app/` is the shell, `features/` one folder per area, `components/`, `lib/` and `store/` are shared. |
| `tests/`, `tests-ui/`, `e2e/` | UI logic tests, component tests, and browser tests. |
| `deploy/helm/` | The charts. The server chart carries the others as sub-charts. |
| `docs-site/` | This site. |

## Rules every part follows

These are not preferences. Most are held by a test that fails with the rule it enforces, and you are asked not to edit those tests to make a change pass.

**Read-only by construction.** The agent holds list and watch permission and nothing else. It never reads Secrets or ConfigMaps. The server never writes to a cluster on its own; the optional controller is the one thing that does, and only for what an administrator asked for.

**The agent dials out.** No cluster needs an inbound port for Ikhnos. Every connection between a cluster and the server is opened by the agent.

**Authority only narrows on the way down.** A ceiling set at install time, an approval, and the agent's own check form a chain. The server may narrow it. It may never widen it, and the agent checks again.

**Observed is not declared.** What an agent saw and what a person said are stored apart and combined only on request. See [From facts to what you see](data-flow.md).

**An unknown is not a value.** Advice says "can't tell" and names the missing fact instead of guessing. See [Placement](placement-engine.md).

**A person decides.** Nothing moves, widens or narrows without a human accepting it. An advisor can only propose.

**Layers are enforced.** Go packages and the UI's feature folders have import rules, and tests fail when one is broken.

**Wire shapes come from Go.** The TypeScript types the UI uses are generated from the Go types, and a test fails if the committed file is stale.

## More on the design

These pages go deeper on one subject each.

**Trust and security.** [Trust model](../architecture/trust-model.md), [Agent trust model](../architecture/agent-trust-model.md) and [Permissions reference](../architecture/permissions-reference.md).

**Telemetry and FUSION.** [Observability intent](../architecture/observability-intent.md), [Telemetry intent](../architecture/telemetry-intent.md), [Regional operators](../architecture/regional-operators.md), [FUSION API](../architecture/fusion-api.md) and [FUSION and Grafana](../architecture/fusion-grafana.md).

**Links and memory.** [Cluster links](../architecture/cluster-links.md) and [System memory](../architecture/system-memory.md).

**Operating.** [Exposing the agent port](../architecture/exposure-options.md), [Installing and removing what Ikhnos installs](../architecture/lifecycle.md) and [Regional operators: certificates and removal](../architecture/regional-operators-lifecycle.md).

Ready to change something? Go to the [developer guide](../contributing/developer-guide.md).

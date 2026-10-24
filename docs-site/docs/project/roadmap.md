---
id: roadmap
title: Roadmap
description: Where Ikhnos could go next, with the pluggable placement decider as the research testbed, in the order the pieces unlock each other.
---

# Roadmap

Nothing here is a promise or a date. It is the order in which the pieces unlock each other, and none of it changes the posture the project has held throughout: **read-only, and nothing moves without a person's approval.** Each item is additive to the existing `Decider` contract, the history store or the advice engine, not a redesign of them.

## The pluggable decider is the deliberate gap

The `Decider` interface is a real, working plug point today. Any HTTP service that speaks the schema in [`backend/docs/decider-webhook.openapi.yaml`](https://github.com/alexandrosst/ikhnos/blob/main/backend/docs/decider-webhook.openapi.yaml) (OpenAPI 3.1, optionally HMAC-signed request for request) is compared fairly against the built-in *Baseline* and *Follow the heaviest talker* on the **Deciders** tab. But nothing dynamic or learned has been plugged into it yet: it is infrastructure for AI-driven placement, not an example of one.

## What would make it a better testbed

1. **A reference learned decider**, shipped as its own small service speaking the same schema. It should learn from the history already being recorded, not only from the instantaneous request: a contextual bandit over move outcomes, or an online model re-scored as new measurements arrive, fits the existing confidence vocabulary naturally.
2. **A feedback loop that closes the read-only gap.** Nothing today records whether a person followed a recommendation or what happened after, so there is no way to tell a good decider from a lucky one. Even before acting on advice is unpaused, a lightweight "I did this" log would let a decider be scored against its own past predictions.
3. **Offline replay for evaluation.** History keeps thinned snapshots. Replaying a sequence of them through several deciders and reporting the counterfactual cost each would have produced turns every recorded estate into a benchmark, with no cluster required.
4. **Acting on advice, narrowly first.** Applying one already-computed recommendation as a Kubernetes-native hint (a `nodeAffinity` or `topologySpreadConstraint` patch, or cordoning a node before a planned move), behind an explicit, audited, one-recommendation-at-a-time approval.
5. **A pluggable cost function, not only its weights.** A decider could supply its own scoring function, still checked against the same hard constraints. That opens objectives research increasingly cares about for edge placement: energy or carbon per site, tail latency instead of mean, fairness across tenants.
6. **A synthetic topology generator**, so an experiment is reproducible and shareable without hardware, and a natural export and import point to and from established continuum simulators (iFogSim, EdgeCloudSim, PureEdgeSim).
7. **Failure and churn injection** against a synthetic or sandbox topology, turning the project's existing honesty about degraded states into a resilience test suite.

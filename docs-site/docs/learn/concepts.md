---
id: concepts
title: Concepts in plain words
description: Every term Ikhnos uses, in a sentence or two, grouped by what it is about, with a link to where it is explained in full.
---

# Concepts in plain words

Ikhnos has a small vocabulary and uses it consistently. This page is the one place to look a word up. Each entry is the short version; the last column leads to the full one.

## The parts

| Term | In plain words | More |
|---|---|---|
| **Server** | The one program that holds the model, the history, the accounts and the certificate authority. One replica, one database. | [Inside: the server](../inside/server.md) |
| **Agent** | A small read-only pod, one per cluster. It dials out to the server and reports what it sees. | [Inside: the agent](../inside/agent.md) |
| **Agent port** | Port 8443 on the server. Agents connect here, over mutual TLS. It can never sit behind something that ends TLS. | [Exposing the agent port](../architecture/exposure-options.md) |
| **Admin port** | Port 8080 on the server. The web interface and the JSON API. Put TLS in front of it. | [Production cluster](../installation/production-cluster.md) |
| **Regional operator** | An optional collector that receives the telemetry several clusters already export and sends it on as one stream. | [Regional operators](../architecture/regional-operators.md) |
| **FUSION** | The optional bundled stack that stores telemetry (Prometheus, Loki, Tempo) and shows it (Grafana), released together with the server. | [The FUSION API](../architecture/fusion-api.md) |
| **Decider** | A program of yours, reached by webhook, that answers placement questions so it can be compared with the built-in ones. | [Mobility and placement](../user-guide/mobility-and-placement.md#pluggable-deciders) |

## The estate

| Term | In plain words | More |
|---|---|---|
| **Cluster** | One Kubernetes cluster, seen through its agent. It has a tier. | [Using the UI](../user-guide/using-the-ui.md) |
| **Tier** (cloud, edge, far edge) | A label you give a cluster for where it sits on the continuum. It groups the views and shapes the cost of a move. It changes nothing about how the agent behaves. | [System overview](../architecture/overview.md) |
| **Site** | A physical or logical place: a region, a factory, a shop. Clusters and devices belong to sites. | [Using the UI](../user-guide/using-the-ui.md) |
| **Device** | A thing that is not a cluster, such as a sensor, camera or controller, that services talk to. | [Using the UI](../user-guide/using-the-ui.md) |
| **Application** | A group of services that belong together. Ikhnos suggests groupings from traffic; you accept, correct or dismiss them. | [Namespaces and services](../user-guide/namespaces-and-services.md) |
| **Suggestion** | Something Ikhnos noticed and did not decide on its own, such as a likely application grouping. It waits on the Agents page. | [Using the UI](../user-guide/using-the-ui.md) |

## Trust

| Term | In plain words | More |
|---|---|---|
| **Enrollment token** | A one-time secret, valid an hour, in the install command. It proves you were allowed to try to connect a cluster. | [Agent trust model](../architecture/agent-trust-model.md) |
| **Approval code** | A short code the agent prints only in its own pod log. Typing it in proves the agent that showed up is the one you meant. | [Connecting a cluster](../installation/connecting-a-cluster.md) |
| **CA pin** | A fingerprint of the server's certificate authority, put in the install command, so the agent knows it is talking to the right server. | [Trust model](../architecture/trust-model.md) |
| **Mutual TLS** | Both sides show a certificate. The agent's lasts 24 hours and renews itself at half-life. | [Trust model](../architecture/trust-model.md) |
| **Ceiling, approved, effective** | What the agent's permissions allow, what an administrator agreed to receive, and what is being sent now. Each is at or below the one before. | [Observability intent](../architecture/observability-intent.md) |
| **Access tier** | How much of a cluster an agent may report. 0 is registration only, 1 adds nodes and storage, 2 adds namespaces and workloads. 3 and 4 are reserved. | [Permissions reference](../architecture/permissions-reference.md) |
| **Collector** | An optional part of an agent that watches one thing: the node probe, the traffic observer, path measurements. An administrator can pause one. | [Observability intent](../architecture/observability-intent.md) |
| **Role** | What a person may do in an organisation: viewer reads, editor edits what people declare, admin manages agents and settings, owner owns the organisation. | [Approvals in depth](../user-guide/approvals-in-depth.md) |

## The model

| Term | In plain words | More |
|---|---|---|
| **Fact** | A raw observation from an agent, with no interpretation. | [Inside: from facts to views](../inside/data-flow.md) |
| **Observed** and **declared** | What an agent saw, and what a person said. Kept apart, combined only when asked. | [Inside: from facts to views](../inside/data-flow.md) |
| **Twin** | The rule that keeps observed and declared apart and combines them. | [Inside: from facts to views](../inside/data-flow.md) |
| **Effective model** | The combined answer: every value with where it came from, how old it is, and how sure. | [Inside: from facts to views](../inside/data-flow.md) |
| **Version** | One period during which something looked a certain way. A change closes one version and opens the next. | [System memory](../architecture/system-memory.md) |
| **Event** | A short record of why something changed, linked to the version it produced. | [System memory](../architecture/system-memory.md) |
| **Config** | What the server tells an agent to do: its tier, what to pause, what to leave out. The agent only applies what narrows. | [Inside: the config channel](../inside/config-channel.md) |
| **Condition** | A statement about an agent that is true, false or unknown, such as Connected or ConfigInForce. Unknown is an answer: it means the server cannot tell. | [Inside: the config channel](../inside/config-channel.md) |

## Advice

| Term | In plain words | More |
|---|---|---|
| **Placement** | Where a service runs. Ikhnos advises on it and never moves anything. | [Mobility and placement](../user-guide/mobility-and-placement.md) |
| **Fits, does not fit, can't tell** | The three answers to whether a service would work on a cluster. Free capacity is an interval, so an unknown is never taken as zero. | [Inside: the placement engine](../inside/placement-engine.md) |
| **Confidence** | How much to trust an answer: measured, reported, inferred, guess or unknown. It is the weakest fact that decided it, never an average. | [Inside: the placement engine](../inside/placement-engine.md) |
| **Cost, in points** | How much a move costs: round trips weighted by traffic, new cross-site traffic, a penalty near full, data to copy. One point is one millisecond of round trip. | [Mobility and placement](../user-guide/mobility-and-placement.md) |
| **What-if** | Try a move on paper before deciding anything. | [Mobility and placement](../user-guide/mobility-and-placement.md) |
| **Advisor and proposal** | A program that suggests a change to an agent's access. A proposal waits for a person; an advisor never changes anything. | [Inside: the placement engine](../inside/placement-engine.md) |
| **Outcome** | What an accepted proposal was predicted to do, recorded beside what was observed. | [Inside: the placement engine](../inside/placement-engine.md) |

## Telemetry

| Term | In plain words | More |
|---|---|---|
| **Telemetry intent** | Your named consent for what an agent's collectors do and where they send it. | [Telemetry intent](../architecture/telemetry-intent.md) |
| **OTLP** | The OpenTelemetry protocol the collectors use to send metrics, logs and traces. | [Telemetry intent](../architecture/telemetry-intent.md) |
| **Pipeline** | The path telemetry takes: agents, local operators, regional operators, the central operator, FUSION. The Pipeline page shows each hop and whether it works. | [Using the UI](../user-guide/using-the-ui.md) |
| **Cluster link** | A measured connection between two clusters you pair: loss, delay and jitter. | [Cluster links](../architecture/cluster-links.md) |

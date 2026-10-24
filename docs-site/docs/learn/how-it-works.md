---
id: how-it-works
title: How it works in ten minutes
description: The parts of Ikhnos, how they come to trust each other, how a cluster becomes a model, and how advice is made. One figure for each idea.
---

# How it works in ten minutes

This page walks one cluster through Ikhnos from the day it is connected to the day you read advice about it. Each section has a figure, and each ends with a pointer to the page that goes deeper. You do not need to know Kubernetes internals to follow it; you do need to know that a cluster has nodes, and that workloads run on them.

## The parts

<Figure name="SystemAtAGlance" caption="Solid arrows start where they begin. Every connection between a cluster and the server is opened by the agent. Dashed parts are optional." />

There are only three things you must have.

The **server** is one program with one database. It has two doors that have almost nothing in common. Port 8443 is for agents, over mutual TLS. Port 8080 is for you: the web interface and the JSON API behind it. The server holds the model of your estate, its history, the accounts of the people who use it, and the certificate authority that issues agents their identity.

The **agent** is one small pod in each cluster. It can read the cluster and nothing else. It starts a connection to the server and keeps it open.

**You**, in a browser.

Everything else on the figure is optional: a **regional operator** and **FUSION** (a bundled Prometheus, Loki, Tempo and Grafana) for telemetry, and a **decider** of your own that answers placement questions. They come later, and each is covered in its own place.

Deeper: [Inside Ikhnos](../inside/index.md) and the [system overview](../architecture/overview.md).

## How a cluster earns trust

An agent cannot just announce itself. If it could, anyone who found the server's address could appear in your model. Ikhnos uses two secrets that never travel together.

<Figure name="EnrollmentSequence" caption="The token is in the command you run. The approval code is only ever printed in the agent's own pod log, so approving it proves the agent that showed up is the one you meant." />

When you connect a cluster, the server mints a **one-time token**, valid for an hour and one use, and prints a ready-made `helm install` command that contains it, together with a fingerprint of the server's certificate authority. You run that command in the cluster. The agent starts, dials the server, and checks the fingerprint before it trusts anything the server says. The server checks the token and the agent shows an **approval code** in its own log, and only there. You read the code from the cluster and type it into the interface. Only then does the server issue the agent a certificate.

From that moment every connection is mutual TLS: both sides prove who they are. The certificate lasts 24 hours and renews itself at half-life. Revoking an agent is immediate.

Deeper: [Agent trust model](../architecture/agent-trust-model.md) and [Trust model](../architecture/trust-model.md).

## What an agent may see

Being trusted does not mean seeing everything. What an agent reports is a chain of three settings, and the chain only ever narrows.

<Figure name="TrustChain" caption="Each setting is at or below the one before. The server can narrow what an agent reports but never widen it; widening needs a helm upgrade by the cluster's owner. The agent checks this itself, so it does not have to trust the server." />

The **ceiling** is what the agent's Kubernetes permissions allow, set by whoever owns the cluster when they install it. The **approved** level is what an administrator in Ikhnos agreed to receive, and can never be above the ceiling. The **effective** level is what the agent is sending right now. Tier 0 sends only enough to tell clusters apart. Tier 1 adds nodes and storage classes. Tier 2, the default, adds namespaces, workloads, pods and services. At every tier the agent is read-only and never reads Secrets or ConfigMaps.

Deeper: [Observability intent](../architecture/observability-intent.md) and the [Permissions reference](../architecture/permissions-reference.md).

## From facts to a model

Agents send **facts**: raw observations, with no interpretation. The server interprets them: it groups workloads into applications, tells virtual machines from bare metal, and works out which cluster is which. Interpreting on the server means the rules can improve without redeploying a single agent.

People add the other half. You say what you know that no agent can: which site a cluster is in, which services belong to one application, a latency you measured yourself. Ikhnos keeps what was **observed** and what was **declared** apart, and combines them only when something asks. Every value says which one it is, how old it is, and how sure the system is. A value you declared wins over an observed one, and is flagged if what is observed has since moved.

The result is **one model**, shown two ways in the interface, and remembered: every change is a new version with a reason, so history is a question, not an export.

Deeper: [From facts to what you see](../inside/data-flow.md) and [System memory](../architecture/system-memory.md).

## How advice is made

<Figure name="PlacementPipeline" caption="Read-only, and honest about what it does not know. A missing fact is named, never silently replaced by a guess." />

For a service that could move, Ikhnos first removes every cluster that cannot work: a local volume, a pinned node, data residency, a trust zone, or not enough CPU and memory. For each cluster left, it answers one of three things: **fits**, **does not fit**, or **can't tell**, because free capacity is an interval, and an unknown is not a zero. It then scores the move in points, where one point is one millisecond of round trip, weighted by how much traffic crosses.

Every answer carries a **confidence**, taken from the weakest fact that decided it. It is never an average, because a recommendation that rests on one guess is a guess. Each input is shown with its source and age, together with the smallest change that would flip the answer. When there is not enough evidence, the page says so and names the missing fact.

A person decides. Nothing is moved by Ikhnos.

Deeper: [Mobility and placement](../user-guide/mobility-and-placement.md) and [How a recommendation is made](../inside/placement-engine.md).

## Where telemetry goes

If you want more than a picture of the estate, Ikhnos can also carry the metrics, logs and traces of your clusters.

<Figure name="TelemetryFlow" caption="The server grants permission and reads results back. The data itself never passes through it." />

Collectors in each cluster send over OTLP, with mutual TLS, to a regional operator or straight to a backend you run. The central operator of FUSION writes to Prometheus, Loki and Tempo, and nine Grafana dashboards read them. Ikhnos decides what may leave a cluster and where it may go, opens Grafana for you behind your sign-in, and reads the stores back through one token-protected API. All of it is opt-in.

Deeper: [Telemetry intent](../architecture/telemetry-intent.md), [Regional operators](../architecture/regional-operators.md) and [Grafana and the web pages](../architecture/fusion-grafana.md).

## What to read next

<Cards>
  <Card to="/docs/learn/concepts" kicker="Reference" title="Concepts in plain words">Every term above, in a sentence each.</Card>
  <Card to="/docs/journey/" kicker="Hands on" title="Your first hour">Do all of this with your own cluster.</Card>
  <Card to="/docs/inside/" kicker="Deeper" title="Inside Ikhnos">The same story, drawn at the level of the code.</Card>
</Cards>

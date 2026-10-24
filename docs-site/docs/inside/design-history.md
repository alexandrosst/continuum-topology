---
id: design-history
title: Design history
description: The plan Ikhnos was restructured by, drawn as it was agreed, with what became of each part. These figures are plans, not descriptions of the system today.
---

# Design history

Every other page in this section draws the system as it is. This page is different on purpose. It keeps the **plan** the system was restructured by, drawn as it was agreed, so a reader can see where an odd-looking choice came from and what was decided against.

:::caution Plans, not facts
The figures here were drawn in October 2026, when the restructuring was under way. They show the *target* and the *order of work* at that moment. Some of what they show is built, some changed on contact with the code, and some is deliberately not done. Each figure is followed by what became of it. For the system as it is, use the other pages in [Inside Ikhnos](index.md).
:::

## Why there was a plan

Ikhnos grew by adding features. By the time it was first prepared for others to run, the server was a single package of about thirty thousand lines in which any file could use any other, the same rules were written in several places, and the browser kept the one copy of the model that mattered. Adding the next feature meant touching all of it.

The plan asked for one change of shape and no new feature until it was done: *every feature as one loop*.

<Figure name="TargetArchitecture" caption="The target: intent, desired, delivered, observed, status. One loop that every feature runs, so that what a person wants, what was sent, what happened and what the system believes are four different things that can be compared." />

**What became of it.** The loop is the reason for the [config channel](config-channel.md): the digest names what was delivered, the heartbeat echo says what is observed, and the three conditions are the status. It is built for agent configuration. It is not yet a general engine that every feature runs: delivery receipts and a shared kit for a new kind of resource (the telemetry intents, the operators and the proposals are the examples it would be taken from) wait for the first feature that needs them.

## One change, end to end

The plan followed a single change through seven stops, to find the seams where a change could be lost or mistaken for another.

<Figure name="ChangeSequence" caption="A change as a sequence between the API, intents, delivery, the compiler, the agent, observation and status. Each arrow was a place where the plan asked for a test." />

**What became of it.** Audit rows are now written in the same transaction as the change (33 of 34 write sites; link-credential rotation stays on the older path on purpose). The compiler, a single function that reads nothing and builds an agent's Config from five named sections, exists and is pinned by a test that fails when a Config field has no owner.

## The order of work

<Figure name="MigrationRoadmap" caption="The phases, with a gate between each. A phase might delete code, move it, or add a contract. It could not add a user feature." />

<Figure name="WorkingPlan" caption="The first four stages, as drawn on the day the stop line was set. Stages 0 and 2 were then still open; both have since closed." />

**What became of it.** Stages 0 to 3 are done: behaviour pinned by tests, audit written with the change, the config echo and conditions, and a minimal decision loop in advise mode. The "stop line", which asked for the decision loop to run in production before the rest, was waived on 2026-10-17 because there was no advisor yet to run it. After it came the policy kit, the config channel, install rendering on the server, the status model, the split of the server into packages, generated wire types, the frontend feature folders, and organisation scope on the store. The [server](server.md) and [frontend](frontend.md) pages describe where that ended.

## Modules around one engine

<Figure name="PlanePlan" caption="Eight modules plug into one engine. Each channel was to have its own receipt, so 'sent' and 'in force' are never the same word." />

**What became of it.** The eight modules are real packages today (accounts, agents, estate, telemetry, operators, connectivity, fusion, advice). The *engine* beneath them was the most ambitious part of the plan and is the part not built as drawn. What is built is the kernel, the router and one set of policy functions that every module shares. A common engine with channels and receipts is deferred until a feature needs it.

## Applying a change through Kubernetes

<Figure name="CrdFlow" caption="The server renders; people apply; an optional controller reconciles. The server never needs write access to your cluster." />

**What became of it.** This is built as an option. The server renders the install, and an optional controller with one custom resource, `IkhnosAgent`, keeps a Helm release of the agent chart to match it. The Connect page offers the same install as a resource in a second tab. Without the controller, the same output is a command a person runs. See [Installing and removing what Ikhnos installs](../architecture/lifecycle.md).

## Where the live status is kept

The repository's `REDESIGN.md` is the running record of the restructuring, with a row for each stage, its state, and what was found when the code was audited against the plan on 2026-10-23. Read it when you need the detailed history. The [roadmap](../project/roadmap.md) says what is planned from here.

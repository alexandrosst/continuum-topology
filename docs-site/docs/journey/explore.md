---
id: explore
title: "6. Explore"
description: "A tour of what your first connected cluster gives you: Topology, Clusters, Agents and History, and where to look when something looks wrong."
---

<StepBar n={6} of={7} title="Explore" />

# Explore

Your cluster is in. This page is a short, guided look at what you now have. The screenshots here use the built-in sample estate (made-up clusters), so that the pictures show a populated interface; yours will show your own cluster.

## Topology: one model, shown four ways

**Topology** has four tabs. They are all views of the same model, so a change in one shows in the others.

<Shot src="readme/ui-topology" alt="The Application view: a cloud cluster, a regional edge cluster and two far-edge clusters with their services and the dependencies between them" caption="Application: services, grouped by the cluster they run in, with dependencies between them." />

**Application** shows your services grouped by cluster, with a line for each dependency. A solid line was measured, a dashed one is inferred from connection tracking, and a dotted one was declared by a person but not seen in traffic. Hover a line for its protocol, throughput and latency. Click a service to focus it: everything not connected to it fades away.

<Shot src="readme/ui-infrastructure" alt="The Infrastructure view: nodes grouped by cluster, with the services scheduled on them" caption="Infrastructure: the nodes underneath, grouped the same way." />

**Infrastructure** shows the nodes underneath: virtual machines, bare metal, edge devices, with an optional overlay of which services run on which node. **Map** places clusters and sites on a map, and **Telemetry** shows the path telemetry takes, once you have set any up.

With only one cluster connected the picture will be small. That is correct: Ikhnos shows what its agents report and nothing it has not seen. The two things that make the dependency lines appear are the traffic observer (an extra from step 4) and time.

## Clusters, and the tables

**Clusters** lists every cluster with its tier, status and how long ago it last reported. Nodes, Namespaces, Services and Devices are the same model as tables, and every table can be searched, sorted and filtered. Press <kbd>Ctrl</kbd> + <kbd>K</kbd> from anywhere to search the whole estate.

A cluster has a **tier**: cloud, edge or far edge. Ikhnos guesses a sensible one; you can correct it. The tier groups the views and shapes the cost of moving a service, so it is worth a quick check.

## Agents: what each one is allowed to see

**Agents** lists every connected agent: whether it is connected, its last heartbeat, its certificate expiry, and its status. Expand a row to open its insight panel.

It shows the three settings from step 4 side by side: **ceiling**, **approved** and **effective**. This is where an administrator narrows what an agent shares: lowering the approved level, pausing an optional collector, or leaving more namespaces out. Widening past the ceiling always needs a `helm upgrade` on the cluster itself, and the page shows you the exact command.

Each row has a **Diagnose** button. It explains what is wrong with that agent, worst first, and gives the `kubectl` command for each finding. When something looks off, start there.

The **Suggestions** tab is where Ikhnos puts things it noticed and did not decide for you: a likely grouping of services into an application, or traffic that points at a cluster you have not connected yet. Accept, correct or dismiss each one. Nothing is applied automatically.

## History: ask what a cluster looked like

**History** is recorded by the server, not by your browser. Every scaling, migration, node joining or leaving, and image change is a new version with a reason, and you can scrub back through snapshots. It answers "what did this look like an hour ago", and "what would be affected if this changed".

:::note[What a past view shows]
A past view shows what agents discovered back then, with today's names, sites and policy, because only discovery is recorded.
:::

## Things worth trying

- Click a cluster on the Topology and read the **Inspector** that opens: what it is made of, its health, and where each fact came from.
- Open the **Agents** page and press **Diagnose** on your agent, even if nothing looks wrong.
- Rename a service in a table, then look at the Topology.
- In **Settings**, add a second sign-in step to your account.

[Next: ask for advice →](./advice.md)

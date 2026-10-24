---
id: next-steps
title: What next
description: "What to do after your first hour: more clusters, a second sign-in step, telemetry, a proper address for the server, and where to look when something breaks."
---

# What next

You have a server, one connected cluster, and a first look at advice. Here is what most people do next, roughly in the order they do it.

<Cards>
  <Card to="/docs/installation/connecting-a-cluster" kicker="Grow" title="Connect more clusters">Same wizard, one per cluster. The more of your estate it sees, the better the placement advice.</Card>
  <Card to="/docs/installation/production-cluster" kicker="Harden" title="Give the server a real address">A DNS name, a load balancer or a Gateway, and real TLS, so agents can dial from other networks.</Card>
  <Card to="/docs/user-guide/using-the-ui#team-and-roles" kicker="People" title="Invite your team">Roles, two-factor sign-in and the audit trail.</Card>
  <Card to="/docs/architecture/telemetry-intent" kicker="See more" title="Turn on telemetry">Metrics, logs and traces, and nine Grafana dashboards, with your consent for each.</Card>
  <Card to="/docs/architecture/cluster-links" kicker="Measure" title="Pair clusters">Loss, delay and jitter between the clusters you choose.</Card>
  <Card to="/docs/troubleshooting/common-errors" kicker="When stuck" title="Common errors">The handful of mistakes that are easy to make once, with the exact fix.</Card>
</Cards>

## Before you rely on it

Read [Status and limits](../project/status-and-limits.md). Ikhnos is pre-1.0, and that page says plainly what has been tested, what has never been run for real, and what is not built.

Keep a copy of the sealing key from step 2 somewhere other than the cluster. The server is one replica with one database on a volume, so a backup of that volume, together with the key, is what you restore from. The repository's [backup guide](https://github.com/alexandrosst/ikhnos/blob/main/deploy/BACKUP.md) covers what to keep, how to take a consistent copy, and what an upgrade does to your data.

## To see how it works inside

If you came to understand it rather than only to run it, [Inside Ikhnos](../inside/index.md) is next. It draws the server's layers, the agent's stream, how facts become the views, and how advice is made, at the level of the code.

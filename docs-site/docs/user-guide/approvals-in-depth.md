---
id: approvals-in-depth
title: Approvals in depth
description: The two different things "approve" means in this app, and where each one is handled today.
---

# Approvals in depth

:::info[This page is still growing]
The short version lives in [Using the UI](./using-the-ui.md#approvals) and [Agent trust model](../architecture/agent-trust-model.md) — this page will expand with the full walkthrough (roles, the audit trail entries each approval produces, and the API endpoints behind them) in a future update.
:::

Two distinct things share the word "approve" in this project, and it's worth not conflating them:

**Approving an agent's enrollment** is a one-time identity check: an administrator reads a short code from the newly-installed agent's own pod log and types it into the UI, confirming that the agent asking to join really is the one running where it's expected. Five wrong attempts reject the request permanently. The full sequence is in [Agent trust model](../architecture/agent-trust-model.md).

**Approving a placement recommendation** isn't a single button in this app at all — the Placement page's job stops at giving you a well-evidenced, honestly-hedged recommendation (see [Mobility and placement](./mobility-and-placement.md)); acting on it is deliberately left to whatever change-management process you already run. Nothing about a service's actual deployment is touched by this project.

Everything either kind of approval does is written to the audit trail — who did it and when — visible to administrators and, if Neo4j is enabled, queryable against a specific point in time from the History page.

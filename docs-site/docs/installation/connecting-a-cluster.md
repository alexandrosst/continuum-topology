---
id: connecting-a-cluster
title: Connecting a cluster
description: Installing the agent and approving it, from the UI's Connect a cluster wizard through to a live cluster.
---

# Connecting a cluster

Once the server is running and you've signed in, every cluster is added the same way: **Discovery → Connect a cluster**. The wizard does the work that would otherwise mean copying a chart reference, an image name and a token around by hand.

## What the wizard gives you

The printed command already contains everything it needs:

- The chart reference (this server's own GHCR namespace — nothing to fill in).
- This server's **CA pin**, so the agent can verify it's really talking to your server on first contact.
- A **one-time enrollment token**, valid for one hour and one use.

Optionally, before you run it: narrow which namespaces the agent reports (**Only look at some namespaces**), or turn on the node probe and traffic observer checkboxes if you want that extra detail from the start (both are described in the [main project README](https://github.com/alexandrosst/continuum-topology#readme) — they're additive, and off by default). None of this is a Kubernetes *permission* boundary — the agent's RBAC stays cluster-wide read-only either way — it's a privacy boundary applied before anything leaves the cluster.

Run the printed command against the target cluster. See [Exposing the agent port](../architecture/exposure-options.md) if you're curious what's actually happening on the wire, or [Agent trust model](../architecture/agent-trust-model.md) for the enrollment sequence step by step.

## The approval code

A token proves someone was *allowed* to install an agent — it doesn't prove the agent that showed up is the one you meant. That's what the approval code is for.

When the agent first contacts the server, it generates a short code and prints it **only in its own log**:

```bash
kubectl -n continuum-system logs deploy/continuum-agent
```

Look for a line like `enrollment pending: approval code K7QM-4TXD`. Type that code into the approve dialog in the UI (case, dashes and spaces don't matter — pasting the whole log line works). Because only someone with access to the cluster can read that log, approving it is confirmation that the agent asking to join really is the one running where you expect.

Five wrong codes reject the request permanently — install again with a fresh token if that happens. The dialog shows how many attempts are left.

## What happens after approval

Nodes, namespaces and workloads start flowing into the topology within moments. Application groupings show up as suggestions in the **Discovery** inbox rather than being decided for you — accept the ones that look right, correct or dismiss the rest.

From here, the [User guide](../user-guide/using-the-ui.md) covers the rest of the UI: what the Agents page shows you about each connected cluster, how to widen or narrow what an agent shares later, and how the Placement page's recommendations work.

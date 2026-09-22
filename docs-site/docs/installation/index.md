---
id: index
title: Installation overview
description: Which installation path fits your situation.
---

# Installation overview

There's one Helm chart for the server ([`continuum-server`](../reference/server-helm-values.md)) and one for the agent ([`continuum-agent`](../reference/agent-helm-values.md)), both published to `ghcr.io/<your-org>` by this repo's own GitHub Actions workflow — nothing to build, nothing to publish yourself, unless you want to.

**Already did the [Quickstart](../getting-started/quickstart.md)?** You have a working server. This section is for the two things that come after: making it reachable for real (not just from your own machine), and connecting your first cluster.

| I want to... | Go to |
|---|---|
| Get a server running as fast as possible, anywhere | [Quickstart](../getting-started/quickstart.md) |
| Expose it properly on a cloud cluster, with a real DNS name | [Production cluster](./production-cluster.md) |
| Install the agent in a cluster and see it appear in the UI | [Connecting a cluster](./connecting-a-cluster.md) |
| Understand *why* the agent port needs special treatment | [Exposing the agent port](../architecture/exposure-options.md) |

The one thing worth understanding before you touch any of these: the server has **two separate listeners**, and they're exposed completely differently. The agent port (`:8443`) speaks mutual TLS gRPC and that handshake has to reach the pod **untouched** — it can never be terminated at an HTTP proxy or ingress. The admin port (`:8080`, the UI and JSON API) is the opposite: plain HTTP inside the pod, meant to sit behind a normal TLS-terminating Ingress. Mixing these up is the single most common way to get a confusing, half-working install — see [Architecture](../architecture/overview.md) for the full picture.

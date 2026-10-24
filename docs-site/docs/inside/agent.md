---
id: agent
title: Inside the agent
description: What runs in the agent pod, the stream message by message, and how the agent behaves when the link is slow, broken or too large to send in one piece.
---

# Inside the agent

The agent is a small program, `backend/cmd/agent`, built from `backend/internal/agent` and a few libraries it shares with the server. It runs as one Deployment in each cluster. It reads the cluster, cuts what it reads to what it is allowed to share, and sends it to the server. It never writes to the cluster.

<Figure name="AgentInside" caption="One pod, with optional node DaemonSets beside it. The identity is a Secret that the agent renews on its own." />

## The pieces

**Informers** are Kubernetes list-and-watch loops, one per kind of object. Their permissions are the cluster-wide read-only role the chart creates, and that role is the first limit on what the agent can see. It does not include Secrets or ConfigMaps.

**Receivers** take reports from the optional node DaemonSets. The *node probe* reports facts about the host that the Kubernetes API does not carry. The *flow observer* reports which workloads talked to which. Receivers keep answering even when the server pauses a collector; they simply drop what they hear.

**Scope and tier** cut everything to what the install allows, what the organisation approved and what has been excluded. This stage is why a lowered tier takes effect even if the server is wrong: the agent does not trust the server to be right about its own limits. See [Config channel](config-channel.md).

**The picture builder** sends the full picture first and then only changes.

**The stream** is one gRPC stream, dialled out to the server on port 8443 over mutual TLS.

**The identity** is a Secret holding the agent's key and certificate. The agent renews it at half of its life, on its own.

## The stream, message by message

<Figure name="AgentStream" caption="Solid arrows go to the server, dashed ones come back. Short calls on the same port, such as Enroll and Renew, sit beside the stream." />

The conversation has a fixed shape, which is the reason a reviewer can reason about it.

1. The agent says **Hello**: its version, its installed ceiling, its diagnostics, the protocol it speaks and what it is able to do.
2. The server answers with **Config**: the approved tier, any overrides, link peers, a digest that names this Config, and the server's time.
3. The agent sends a **full Sync**. If the picture is larger than about 1 MiB it goes in pieces. The server answers each with an **Ack** that names the sequence number applied.
4. From then on the agent sends **changes** (what changed and what was deleted) and **heartbeats** (module health, clock skew, its own resource use and the digest of the Config it applied). Observed connections and timing rounds follow when those collectors are on.
5. The server may send a new **Config** at any time. It may send **Revoked**, which ends the agent's trust.

A change of effective tier ends the stream on purpose, so the next stream starts with the right watches. That is not a failure, and the Agents page does not show it as one.

## Resilience

The agent is built to degrade in ways that are visible, never silent. The behaviours worth knowing:

**Large pictures arrive whole or not at all.** A full picture over about 1 MiB is sent in numbered pieces. The server counts each piece against its limits before it buffers it, and applies the assembled result in one step when the last arrives. A reader sees the old picture or the new one, never half of one. A connection that drops partway discards the partial.

**Memory is bounded while disconnected.** The flow aggregator keeps at most 20,000 distinct edges. Past that it drops the oldest tenth, counts them and reports the number.

**Shutdown is quick and polite.** On SIGTERM the agent sends a last heartbeat, closes the stream cleanly, lets a certificate renewal that already has its certificate finish saving it, and exits within seconds.

**Panics are handled one by one.** Each goroutine that can panic recovers, logs the stack and raises an `internal_error` problem. What happens next is chosen for each task, because "log and let it end" would leave, for example, a stream that heartbeats while it no longer hears the server.

**The agent re-checks the server.** Every Config is cut to what the install allows before it is applied: a tier above the ceiling is ignored and reported, as is an unknown collector or an invalid namespace.

## Identity: three ways back in

An agent proves itself with a certificate from the server's own CA. That certificate has a life, and there are three ways it is replaced:

- **Enroll** (once): the agent shows a token and the server answers with the first certificate, after a person approves the agent. See [Agent trust model](../architecture/agent-trust-model.md).
- **Renew**: at half of the certificate's life, over the existing identity.
- **Rejoin**: if the agent was away for less than seven days, it can come back without a new approval.

Past that, the agent is asked to enroll again and a person approves it again.

## Where the details are

The agent's own reference, with the problem codes the Agents page shows and every server endpoint it uses, is `backend/internal/agent/DIAGNOSTICS.md` in the repository. The protocol is `backend/proto/agent.proto`. For a failing agent, [Common errors](../troubleshooting/common-errors.md) is written in the same words the UI uses.

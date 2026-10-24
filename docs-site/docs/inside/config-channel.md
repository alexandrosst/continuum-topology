---
id: config-channel
title: How the server knows what is in force
description: The Config an agent is told, the digest that names it, the echo that proves it was applied, and the three conditions the server states about every agent.
---

# How the server knows what is in force

There is a gap between *sending* an instruction and *knowing it took effect*. A server that assumes the first means the second will, sooner or later, show a green tick next to an agent that is still collecting what you told it to stop. Ikhnos closes the gap with a digest and an echo.

<Figure name="ConfigChannel" caption="Six steps, and then three conditions stated about every agent. The comparison is of two names, never a guess." />

## What a Config is

A Config is what the server tells an agent about how to behave: the approved tier, the collectors paused, the namespaces excluded, the other clusters to measure against and the targets to probe. It travels on the open stream, first after Hello and again whenever it changes.

A Config can only **narrow** what the install allows. The agent re-checks every Config against its ceiling in `resolveOverrides`, so a bug or a compromise on the server cannot widen an agent's access. A tier above the ceiling, an unknown collector or an invalid namespace is ignored, and the agent says so.

## The loop

1. **Compose.** The server builds the Config for an agent from the approval, overrides and link settings.
2. **Name and send.** The digest is the server's own name for exactly that Config. It is recorded with the time it was sent, and goes on the stream.
3. **The agent narrows.** It cuts the Config to what its install allows.
4. **Apply.** Collectors change. If the *effective* tier changed, the stream reconnects on purpose so the right watches are started.
5. **Echo.** The agent's next heartbeat carries `applied_config_digest`, the digest of what is now in force.
6. **Compare.** The server compares the digest it sent with the one echoed, and how long ago it sent. No inference is involved.

## Three conditions

From this the server states three conditions about every agent. They are the words you see on the Agents page.

| Condition | True | False | Unknown |
|---|---|---|---|
| **Connected** | A stream is open. | None is. | |
| **ConfigInForce** | The agent echoed the digest the server sent. | The agent names an earlier one. | Not connected, or the agent is old and does not echo. |
| **Compatible** | The protocol is one the server serves. | Too new or too old. | The agent has not been seen since the server started. |

`Unknown` is a real value and is shown as such. An older agent that never echoes a digest is *not* reported as out of date; the server says it cannot tell.

Because the condition is derived from the digest, it is also what makes outcomes honest in the [decision loop](placement-engine.md): the server only judges whether a change worked once it knows the change was in force.

## Where this is in the code

| What | Where |
|---|---|
| The Config message, Hello, Heartbeat | `backend/proto/agent.proto` |
| Composing the Config and its digest | `backend/internal/connectivity` |
| The stream and `resolveOverrides` | `backend/internal/agent` (`stream.go`) and `backend/internal/agents` |
| Conditions | `backend/internal/agents` |

If you add a field to Config, three things must change together: the proto, the composition (so the field is part of the digest), and `resolveOverrides` (so the agent can refuse it).

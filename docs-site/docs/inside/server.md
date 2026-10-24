---
id: server
title: The server, in layers
description: How the Ikhnos server is cut into packages, what may use what, and which tests hold the lines.
---

# The server, in layers

The server is one Go program with one database. Inside it is about sixty packages, and the way they are cut is the most useful thing to know before you change one.

<Figure name="ServerLayers" caption="Each row may use the rows below it and never the rows above. The compiler holds that line, and so does a test." />

## Why it is cut this way

The server began as one large package. Its parts were in different files, but nothing stopped a file from reaching into any other, and the compiler could not tell a fair use from a reach into someone's internals. The cut was made by measuring first: every identifier was resolved to the file that declares it, each file was given to a module, and every use across a module boundary was counted. Then the packages were split one module at a time, and the lines were held with tests.

The result is a set of layers. Reading the figure from the bottom:

**Libraries** know nothing of the server. `model` defines the shapes, `facts` the raw report of an agent, `interpret` turns facts into a topology, `twin` combines observed and declared, `advice` judges fit, `pki` is the certificate code, and `store` is SQLite. Several are shared with the agent, which is why they must not know about the server.

**The kernel** (`internal/kernel`) holds the `Core`: the store, the certificate authority, the organisation, the audit trail, the clock and the rate limits, plus settings and secrets. It stands on no module, and every module may use it.

**Who is calling** is `accounts` (people, organisations, roles, sessions, tokens, passkeys) and `httpkit`, the router that records what each route asks of its caller. A route says what role it needs when it is mounted, so the requirement can be listed and checked rather than discovered by reading handlers.

**Domain modules** are where the product is. The *hub cluster* is `estate` (the hub, the twin, history and the applications graph), `agents` (enrolled agents, the stream, consent and tiers, conditions), `connectivity` (paths, links and the config an agent is told) and `observed` (pure: what flows say about edges). *Telemetry* is `telemetry`, `operators` and `fusion`. *Advice* is `decisionloop`.

**API packages** are the routes of a module, mounted through the router. They are given what they need as functions and small interfaces, not by importing a neighbour's internals.

**The shell** (`internal/server`) wires it together. It is about seventeen files, and it is the only place allowed to use everything.

## Ports instead of reaching up

When a module needs something from a neighbour that sits above or beside it, the module declares a small interface and the neighbour satisfies it. A few examples show the pattern:

- `fusion` calls the central operator through `operators.Central`, so `operators` does not need to know that `fusion` exists.
- The decision loop owns two ports, `Access` (answered by the agents' side) and `Load` (answered by the estate), and imports neither.
- `agents` renews an agent's telemetry certificate through its own `TelemetryCerts` interface.

The point is that dependency arrows go one way. When you find yourself wanting to import a package above yours, the answer is almost always to add a port.

## Two listeners, one process

The server serves two ports and they share almost nothing.

**:8443 is the agent port.** gRPC over mutual TLS. A certificate issued by the server's own CA is the only credential. It is the only port a cluster needs to reach.

**:8080 is the admin port.** The web interface and the JSON API. People sign in with a password, a passkey or a token, and every route checks the caller's role.

Because they are separate listeners, the agent port can be published to the internet without exposing the API, and the API can sit behind your normal ingress and SSO. See [Exposing the agent port](../architecture/exposure-options.md).

## One writer

State is in SQLite, and there is one writer. The hub holds one view per connected agent behind one lock and the `store` serialises writes. That choice is deliberate: it keeps the model easy to reason about and the install to a single pod with a single volume. It also sets a limit, which [Status and limits](../project/status-and-limits.md) states plainly.

## What holds the lines

You will meet these when a change breaks a rule. Read the message; it says which rule and why.

| Held by | Rule |
|---|---|
| The Go compiler | A package cannot import its caller. |
| A test in each package | It refuses an import outside that package's list. |
| `TestPackagesImportEachOtherOnlyWhereTheListSays` | One list of all allowed imports, `testdata/packages.txt`, checked against the code. |
| The route list, `testdata/routes.txt` | Every route and what it asks of its caller, so a new route cannot be added silently. |
| The wire test | Generated TypeScript types match the Go types. See [the web UI](frontend.md). |

If a change needs a new import between packages, add it to the list in the same change, so the review sees it. If it needs a route, add it to the route list for the same reason.

## Where to start reading

To follow one request, start at the router in `httpkit`, find the route in its API package, and follow the call into the module. To follow one agent message, start at `internal/agents` and follow the sync into `estate`. The next pages show both paths: the [agent](agent.md) and [from facts to what you see](data-flow.md).

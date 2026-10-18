# The server, cut into modules: the map

`internal/server` is one Go package of about 31,000 lines of source (and as many of tests). Its parts are written in separate files,
but nothing stops any file from using anything in any other, and the compiler cannot tell a fair use from a reach into
somebody's internals. This page draws the lines the package is to be cut along, from the code and not from its file names, and
says what has to be settled before each cut. **Nothing was moved to make it.** The lines are held by a test (see the end).

How it was made: the package was type-checked, and every identifier was resolved to the file that declares it (a function, a type,
a field of a struct, a method, a constant). Each file was assigned to a module (`testdata/modules.txt`), and a use is a place where
a file names something declared in a file of another module. There are 549 such uses, once each, in
`testdata/module_edges.txt`. The store was read the same way: which module calls which store method.

## The groups

Eight modules, and two things around them that are not modules. The **kernel** is what every module stands on and must depend on
none of them. The **shell** wires the modules together and is the only thing that may use all of them.

| Group | What it is | Files | Lines | Tables | Routes |
|---|---|---|---|---|---|
| kernel | The `Core` (store, CA, organisation, audit, clock, rate limits), settings, secrets, the outbound address policy (`decider.go`), the Kubernetes interface (`kube.go`), install image settings | 8 | 2,461 | `audit`, `settings` | 0 |
| accounts | People, organisations, roles, sessions, API tokens, passkeys, two-factor, mail; the words for who is calling (`Actor`, `Principal`, `Role`) and the one decision of what a caller may do (`Decide`) | 8 | 2,609 | `users`, `orgs`, `memberships`, `invites`, `sessions`, `api_tokens`, `mail_config` | 40 |
| agents | An enrolled agent: tokens and enrolment, the stream (gRPC), consent and access tier, the conditions, diagnostics and self-telemetry, the agent's connecting address and place | 14 | 3,542 | `tokens`, `agents` | 12 |
| estate | What the server knows about the world: the hub, the per-agent views, observed flows, the twin, history, the applications graph | 8 | 4,683 | `workspace`, `history`, `events`, `snapshots`, `identities`, `model_state`, `tombstones` | 22 |
| telemetry | Telemetry intents (what an agent's local collectors do and where they send) and the quick-start backends | 4 | 985 | `telemetry_intents`, `gateway_tokens` | 10 |
| operators | Regional operators: certificates, senders, addresses, heartbeat, and the node probe of the server's own cluster | 12 | 4,687 | `operators`, `operator_certs` | 15 |
| connectivity | Paths, links, tunnels, the addresses a cluster dials, and the config an agent is told (its digest and the compile of it) | 12 | 3,899 | `link_iperf_credentials` | 3 |
| fusion | The FUSION stack: control, data API and its OpenAPI, retention, UI access | 12 | 4,600 | `fusion_tokens` | 69 |
| advice | The decision loop: advice, proposals, outcomes, decisions, the advisor records | 7 | 1,487 | `decisions`, `proposals`, `proposal_outcomes` | 8 |
| shell | The admin mux and its guards, the platform of organisations, routes, readiness, metrics | 5 | 2,145 | none | 4 |

The routes add up to the 183 in `testdata/routes.txt`. The routes in `admin.go` were given to the module whose handler they
call; most of them are accounts' (sign-in, organisations, members, invites, API tokens), and `state`, `workspace`, the agent tokens
and approve/reject/revoke are estate's and agents'.

## What the code says

**1. Accounts is nearly a leaf.** It uses the kernel and the shell, and one thing from estate (`Core.recordApplicationsGraph`,
called when an organisation is made). Nothing else is needed to move it, apart from the shell's HTTP helpers (below). It is the
first module to move.

**2. The hub is where three modules meet.** `Hub` and the per-agent `view` it keeps hold the state of agents, estate and
connectivity in one struct behind one lock. Agents use estate's code 52 times and estate uses agents' 52 times; connectivity uses
estate's 35 times and estate uses connectivity's 17. The `view` has 34 fields, and who touches them is clear:

- Agents' (the `viewExt` behind `view.ext`, and `view.self`): consent and when it was loaded, diagnostics, the namespaces known, the link capabilities, the release name, and the agent's own self-stats ring.
- Connectivity's: `addrTests`, `reach`, `paths`, `targets`, `pushMu`, and the four config-echo fields (`cfgHash`, `cfgApplied`, `cfgSentAt`, `cfgInForceAt`; advice reads three of them to judge outcomes).
- Estate's: `state`, `link`, `flows`, `obs`, the consistency and implausibility counters, the clock-skew and refusal fields, `lastBeat`, `lastSync`, `connected`, `savedAt`.

So the three cannot be cut by moving files. They can be cut by giving the `view` three parts, one owned by each, behind the
lock, and that is one change in its own right. It is the last of the cuts, not the first.

**3. The kernel depends on six modules.** 29 uses, all small, all fixable: the link settings (`LinkSetting`, `LinkThroughput`,
`normalizeLinks`: 12) belong to connectivity's section of `Settings`, not to a shared struct that names them; the operator
caches (`heartbeatSeen`, `operatorCAs`: 4), the authentication state and the actor type (`authState`, `mailHolder`,
`WebAuthnProvider`, `Actor`: 7), the traffic cache (2), `recordAgentGraph`, `GrantableTier`, `nsNameRe` and
`RevokeTelemetryIntent` are each a thing that `Core` holds because it was convenient. Each belongs to its module and `Core`
should hold it by an interface or not at all. (The kernel also uses 6 things of the shell, the image settings on `Admin` and the
metrics counters; those go with the HTTP kit below.) `Settings` should
become the sum of the modules' sections, the way the agent's config already is.

**4. The shell is used by every module: 152 uses.** 82 of them are plain HTTP plumbing that every handler needs, and which sits
in `admin.go` because handlers are methods on `Admin`: `writeJSON`, `writeErr`, `decode`, `principal`, the role levels
(`memberRole` to `ownerRole`), `ctxPrincipal`, and the receiver `Admin` with its `core()` and `fail`. Another 28 are `Admin`'s
own settings and limiters (`Admin.C`, `Admin.Fusion`, the rate limiters, the probes), 20 are the metrics counters, 12 are the
lookup of an organisation's hub (`Tenant`, `Platform`) and 10 are the chart references. A small HTTP kit that depends on no
module takes the 82; the counters are a registry the kernel can own; each module's handlers get a struct of their own with
what they need, and mount themselves.

**5. Operators and FUSION share a knot.** The central operator is the one FUSION runs. Operators uses 18 things of fusion's
(`CentralOperatorID`, `guardCentral`, `FusionControl`'s endpoint and status) and fusion uses 11 of operators' (the TLS bundle, the
issuer, the address recording). Which of the two owns the central operator is a decision, and the cut depends on it.

**6. Advice is read-only and sits above the rest.** It reads agents (19), estate (13) and accounts (8: `Decide`), and nothing
depends on it except estate (15: the hub builds the capacity view and records outcomes by calling into advice). Inverting that one
direction, so that the hub offers a read and advice asks for it, lets advice move on its own.

**7. Telemetry depends on operators.** Telemetry uses 17 things of operators' (destinations, client certificates, the route
values), 5 of agents' and 1 of estate's; operators uses one of telemetry's (`signalModality`). It moves with or after operators.

**8. The store is cleaner than the code.** Of the 133 store methods the server calls, 22 are called by more than one module
(52 module-and-method pairs in `testdata/module_store.txt`). They fall in four groups. Reading another module's table, which
should become a call to that module: `GetUser` by fusion and the shell, `GetOperator` and `ListOperatorCerts` by fusion,
`ListAgents` by operators, connectivity and the shell, `ListTelemetryIntents` by operators and connectivity, `GetWorkspace` by
accounts, fusion and the shell, `GetMembership` by fusion. The shared snapshot and event tables, which agents and estate both
write (`LoadSnapshot`, `SaveSnapshot`, `AddEvents`). Writing another module's row: `CreateOperator` by fusion (the central
operator). And the audit, revoke and reject paths that the kernel runs for agents (`RejectAgent`, `ClearPollSecret`, `GetAgent`,
`ListTelemetryIntentsByAgent`). Organisation scope on the methods that take a bare id (the open item from the policy kit) is
settled in the same moves, module by module.

## The uses between groups

Rows use, columns are used. Each cell counts distinct things (a function, a type, a field), not calls. The kernel is left out of
the columns because every module may use it; the shell is a column because no module should use it.

| uses → | accounts | agents | estate | telemetry | operators | connectivity | fusion | advice | kernel | shell |
|---|---|---|---|---|---|---|---|---|---|---|
| accounts | . | . | 1 | . | . | . | . | . | . | 15 |
| agents | 12 | . | 52 | . | 1 | 5 | . | . | . | 20 |
| estate | 4 | 52 | . | . | . | 17 | 1 | 15 | . | 20 |
| telemetry | . | 5 | 1 | . | 17 | . | . | . | . | 13 |
| operators | 2 | 17 | 9 | 1 | . | 6 | 18 | . | . | 26 |
| connectivity | . | 8 | 35 | . | 5 | . | 5 | . | . | 17 |
| fusion | 12 | . | 8 | . | 11 | 2 | . | . | . | 32 |
| advice | 8 | 19 | 13 | . | . | . | . | . | . | 9 |
| kernel | 7 | 3 | 2 | 1 | 4 | 12 | . | . | . | 6 |

Agents using accounts (12) and everyone using `Decide` is the right direction: accounts is the vocabulary of who is calling, and
sits under the others. The cells that are not are the ones above: agents and estate and connectivity with each other, operators
with fusion, estate calling advice, and the kernel and the shell.

## The order I would move in, and what each needs first

This is a proposal from the numbers above; each move starts with its own short plan.

1. **The HTTP kit** (not a module): take the 82 plumbing uses out of `admin.go`, and the 20 metrics counters into a registry the kernel owns. Mechanical, and it makes every later move smaller.
2. **Accounts.** One edge to estate; a callback instead.
3. **Advice.** Invert the hub's 15 calls into it; define what advice reads as an interface of its own.
4. **The central operator question**, then **operators and telemetry** together, then **fusion**.
5. **The hub cluster**: the `view` in three parts, then agents, estate and connectivity.
6. **The kernel last**, or as it goes: each thing the kernel holds that belongs to a module leaves with that module.

Whether a module becomes its own Go package (so the compiler enforces the line) or stays a group of files in this one with the
test enforcing it is the end-state decision. Packages are the stronger line, and the test's lists are the way there: a module can
move into its own package when the lines about it have gone from both lists.

## The assignments I was least sure of

These are the files whose place could be argued; moving one changes the lists, and the test shows by how much.
`decider.go` and `kube.go` are in the kernel because three modules each use them (the address policy; reading Services).
`selfstats.go` and `admin_telemetry.go` are agents', as the agent's own telemetry, and not telemetry's, which is intents.
`self_probe.go` is operators', because what it finds is the server's own cluster's addresses. `config_digest.go` is connectivity's,
with the compile of the config. `tier.go` is agents'; `actor.go` and `policy.go` are accounts'. `tenants.go` is the shell: it
holds an organisation's hub, and so it names modules rather than being one. `admin_history.go` is estate's, though it also holds
the settings endpoints, which belong with the kernel's settings.

## What keeps the lines

`modules_test.go` holds three things. Every source file is in exactly one module (`testdata/modules.txt`), so a new file cannot be
added without choosing. The 549 uses across modules are in `testdata/module_edges.txt`; a use that is not in it fails the test and
names the file, and a line for a use that is gone also fails, so the list can only shrink without a decision. The store methods that
more than one module calls are in `testdata/module_store.txt`, held the same way. Accept a change on purpose with
`go test ./internal/server -run TestModule -update`. The test type-checks the package with `go list -export` and the standard
library, so it adds no dependency; it is skipped with `-short`.

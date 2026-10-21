# The server, cut into modules: the map

`internal/server` is one Go package of about 31,000 lines of source (and as many of tests). Its parts are written in separate files,
but nothing stops any file from using anything in any other, and the compiler cannot tell a fair use from a reach into
somebody's internals. This page draws the lines the package is to be cut along, from the code and not from its file names, and
says what has to be settled before each cut. **Nothing was moved to make it.** The lines are held by a test (see the end).

That is how the page began. The cuts have been made since, so the next section is the picture as it stands, and everything after it is
the reasoning and the log of how it got there, with the numbers of the first reading (549 uses, about 31,000 lines in one package).

How it was made: the package was type-checked, and every identifier was resolved to the file that declares it (a function, a type,
a field of a struct, a method, a constant). Each file was assigned to a module (`testdata/modules.txt`), and a use is a place where
a file names something declared in a file of another module. At the first reading there were 549 such uses, once each, in
`testdata/module_edges.txt`. The store was read the same way: which module calls which store method.

## The picture now

Every module is a package, the handlers have left the shell module by module, and `internal/server` is what the compiler could not
take from it: 17 source files and about 1,950 lines, the wiring. The lines between the packages are held by the compiler (a package
cannot import its caller), by a test in each package that refuses an import outside its list, and by one list of all of them,
`testdata/packages.txt` in the server's tests (`TestPackagesImportEachOtherOnlyWhereTheListSays`): 188 imports among 58 packages.
`module_edges.txt` is empty, because there is no longer a use across a module boundary inside the shell to list.

Read from the bottom, what the packages stand on:

- **The libraries** know nothing of the server: `model`, `facts`, `netid`, `probe`, `link`, `measure`, `pki`, `store` (with `workspace`),
  `interpret`, `twin`, `history`, `graph`, `chart`, `install`, `geoip`, `approval`, `advice`. Several of them are also the agent's.
- **The kernel** (`internal/kernel`) stands on the store, the CA and the two settings libraries and on no module. Everything above may use it.
- **The vocabulary of who is calling** is `internal/accounts` (with `internal/passkey` beside it), on the kernel and the store alone.
  The HTTP kit `internal/httpkit` is on top of it: the router that records what a route asks of its caller, the roles, the error
  and JSON helpers. Module logic does not need it; the packages that mount routes do, and several modules carry their routes in the same package.
- **The domain modules** are `internal/operators`, `internal/telemetry`, `internal/fusion` (on `operators`: it calls the central
  operator, and `operators` does not know it, through the `operators.Central` port), `internal/decisionloop` and the hub cluster:
  `internal/observed` (pure, below), `internal/agents`, `internal/connectivity` and `internal/estate` on top, which owns the hub and the
  twin built from it. The decision loop is inverted: it owns the two ports it asks the rest through (`Access`, which the agents'
  side answers, and `Load`, which the estate answers) and imports neither.
- **The API packages** are the routes of a module: `accountsapi`, `settingsapi`, `operatorsapi`, `fusionhttp` (FUSION's whole data API
  over `fusionapi`, the client library for it), `linksapi` (the Sites figures), `fusionpages` (the rules of the gateway to FUSION's
  pages). Each mounts through the kit's router and is handed what it needs as functions and ports.
- **The wire** (`internal/wire`) writes the shapes the API puts on the wire as TypeScript, from the Go types, with reflection and nothing of
  this repository. Each API package lists the shapes it writes in a `WireTypes()` of its own; the shell's test generates
  `src/lib/generated/wire.ts` from them and fails when the committed file is not what the types say, so a renamed or dropped field shows
  as a changed line in the review of the change that made it. What it says follows `encoding/json`: omitempty makes a field optional, and a
  nil slice, map or pointer without it is `| null`. The inventory behind it, at the start: of 135 places a handler writes or reads JSON, 49 pass a named
  type, 83 write an inline map and 46 read an anonymous struct, so those shapes are not in the file until they are named (the progress table has the count as it goes down).
- **The shell** (`internal/server`) builds the `Core`, the hub and the services, hands each API what it asks for, and holds what
  is cookies, origins, limiters, the reverse proxy and the topology and application knowledge FUSION is given. `internal/testkit` is the
  fixtures the tests share.

| Package | Source lines | Files |
|---|---|---|
| `internal/kernel` | 2,464 | 11 |
| `internal/accounts`, `internal/accountsapi`, `internal/settingsapi`, `internal/httpkit` | 2,547 + 856 + 115 + 168 | 9 + 2 + 1 + 1 |
| `internal/agents` | 2,982 | 17 |
| `internal/observed`, `internal/connectivity` | 1,013 + 1,907 | 2 + 10 |
| `internal/estate` | 6,671 | 25 |
| `internal/decisionloop` | 1,826 | 10 |
| `internal/operators`, `internal/operatorsapi` | 2,025 + 2,500 | 9 + 10 |
| `internal/telemetry` | 788 | 4 |
| `internal/fusion`, `internal/fusionhttp`, `internal/fusionpages`, `internal/linksapi`, `internal/fusionapi` | 2,196 + 1,622 + 381 + 696 + 5,795 | 8 + 12 + 2 + 3 + 19 |
| `internal/installcfg`, `internal/destination` | 269 + 30 | 1 + 1 |
| `internal/server` (the shell) | 1,951 | 17 |

**The small read interfaces are in.** Where a module needs something of a neighbour that is not below it, it owns the interface and
the neighbour satisfies it: `fusion` calls the central operator through `operators.Central`; `telemetry` asks the operators two things
through its own `Operators` (the operator a destination names, and whether a destination is acceptable, address written as it is
stored); `agents` renews an agent's telemetry certificate through its own `TelemetryCerts`; the decision loop asks the agents' side
through `Access` and the estate through `Load`. The shared shape of a destination, which both the operators' routes and the telemetry
intents carry, is `internal/destination`, a document over the store's type and nothing more. `agents` has its own `AccessChange`, the
estate converts the loop's record of a proposal into it, and a test fails for a field dropped on the way.

What stays concrete on purpose: `estate` is built with the decision loop (`Loop *decisionloop.Service`) because the hub is what answers
both of the loop's ports and drives the outcome recorder, and it is the top of the hub cluster, which owns that wiring; `operatorsapi`
and `linksapi` and the other API packages import the modules whose routes they mount, which is what an API package is.

## The groups (as of the first reading)

Nine modules, and two things around them that are not modules. The **kernel** is what every module stands on and must depend on
none of them. The **shell** wires the modules together and is the only thing that may use all of them.

| Group | What it is | Files | Lines | Tables | Routes |
|---|---|---|---|---|---|
| kernel (now the package `internal/kernel`) | The `Core` (store, CA, organisation, audit, clock, rate limits), settings, secrets, the outbound address policy (`decider.go`), the Kubernetes interface (`kube.go`), install image settings | 11 | 2,442 | `audit`, `settings` | 0 |
| accounts (now the package `internal/accounts`) | People, organisations, roles, sessions, API tokens, passkeys, two-factor, mail; the words for who is calling (`Actor`, `Principal`, `Role`) and the one decision of what a caller may do (`Decide`) | 9 | 2,974 | `users`, `orgs`, `memberships`, `invites`, `sessions`, `api_tokens`, `mail_config` | 40 |
| agents (now the package `internal/agents`) | An enrolled agent: tokens and enrolment, the stream (gRPC), consent and access tier, the conditions, diagnostics and self-telemetry, the agent's connecting address and place | 14 | 3,542 | `tokens`, `agents` | 12 |
| estate (now the package `internal/estate`) | What the server knows about the world: the hub, the per-agent views, the twin built from them, history, the applications graph, the saved workspace | 16 | 4,100 | `workspace`, `history`, `events`, `snapshots`, `identities`, `model_state`, `tombstones` | 22 |
| telemetry (now the package `internal/telemetry`) | Telemetry intents (what an agent's local collectors do and where they send) and the quick-start backends | 4 | 985 | `telemetry_intents`, `gateway_tokens` | 10 |
| operators (now the package `internal/operators`) | Regional operators: certificates, senders, addresses, heartbeat, and the node probe of the server's own cluster | 12 | 4,687 | `operators`, `operator_certs` | 15 |
| observed (now the package `internal/observed`) | What the flows say: the table of observed edges, the sanitising of a flow batch, the topology read off them, the index of addresses a cluster is known by. Pure; it sits under agents and connectivity so neither has to reach the hub for it | 1 | 1,006 | none | 0 |
| connectivity (now the package `internal/connectivity`) | Paths, links, tunnels, the addresses a cluster dials, and the config an agent is told (its digest and the compile of it) | 12 | 3,899 | `link_iperf_credentials` | 3 |
| fusion (now the package `internal/fusion`) | The FUSION stack: control, data API and its OpenAPI, retention, UI access | 12 | 4,600 | `fusion_tokens` | 69 |
| advice (now the package `internal/decisionloop`) | The decision loop: advice, proposals, outcomes, decisions, the advisor records | 7 | 1,487 | `decisions`, `proposals`, `proposal_outcomes` | 8 |
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

**3. The kernel depended on six modules; it now depends on none.** It was 29 uses, all small. The link settings (12) were the shape
of part of the `Settings` document and the checks on it, and use nothing of connectivity's behaviour, so they moved into the
kernel with the document: the kernel owns the shape of `Settings` and the rules that are pure checks of it, and the modules own what
is done with the values. The life of an agent's identity (tokens, reject, revoke, renew, rejoin: 6) had been written in `core.go`
and moved to agents. What the `Core` held for modules (the sign-in state, the mail holder, the passkey implementation, the opened
CAs of operators, the heartbeat coalescer, the traffic cache: 10) is now a slot each module fills itself (`state.go`), and the
Core knows nothing of what is in the slots. The namespace-name check moved to the kernel, where three modules use it. The
last 6 reaches, into the shell (the image settings on `Admin` and the metrics counters), were cut by splitting each: the shape and
the checks stay in the kernel, and what the flags say, and the HTTP exposition of the counters, stay in the shell. One edge was made visible by the moves and is to be inverted when telemetry is cut: agents ending an
agent's telemetry intents when it is revoked.

**4. The shell is used by every module: 152 uses.** 82 of them are plain HTTP plumbing that every handler needs, and which sits
in `admin.go` because handlers are methods on `Admin`: `writeJSON`, `writeErr`, `decode`, `principal`, the role levels
(`memberRole` to `ownerRole`), `ctxPrincipal`, and the receiver `Admin` with its `core()` and `fail`. Another 28 are `Admin`'s
own settings and limiters (`Admin.C`, `Admin.Fusion`, the rate limiters, the probes), 20 are the metrics counters, 12 are the
lookup of an organisation's hub (`Tenant`, `Platform`) and 10 are the chart references. A small HTTP kit that depends on no
module takes the 82; the counters are a registry the kernel can own; each module's handlers get a struct of their own with
what they need, and mount themselves.

**5. Operators and FUSION share a knot.** The central operator is the one FUSION runs. Operators uses 18 things of fusion's
(`CentralOperatorID`, `guardCentral`, `FusionControl`'s endpoint and status) and fusion uses 11 of operators' (the TLS bundle, the
issuer, the address recording). The central operator is an Operator that FUSION manages (see Decided), which settles the direction: fusion calls operators.

**6. Advice is read-only and sits above the rest.** It reads agents (19), estate (13) and accounts (8: `Decide`), and nothing
depends on it except estate (15: the hub builds the capacity view and records outcomes by calling into advice). Inverting that one
direction, so that the hub offers a read and advice asks for it, lets advice move on its own.

**7. Telemetry depends on operators.** Telemetry uses 17 things of operators' (destinations, client certificates, the route
values), 5 of agents' and 1 of estate's; operators uses one of telemetry's (`signalModality`). It moves with or after operators.

**8. The store is cleaner than the code.** Of the 133 store methods the server calls, 22 were called by more than one module at the first reading
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
| agents | 12 | . | 52 | 1 | 1 | 5 | . | . | . | 20 |
| estate | 4 | 53 | . | . | . | 17 | 1 | 15 | . | 20 |
| telemetry | . | 4 | 1 | . | 17 | . | . | . | . | 13 |
| operators | 2 | 17 | 9 | 1 | . | 6 | 18 | . | . | 26 |
| connectivity | . | 8 | 35 | . | 5 | . | 5 | . | . | 17 |
| fusion | 12 | . | 8 | . | 11 | 2 | . | . | . | 32 |
| advice | 8 | 19 | 13 | . | . | . | . | . | . | 9 |
| kernel | . | . | . | . | . | . | . | . | . | 6 |

Agents using accounts (12) and everyone using `Decide` is the right direction: accounts is the vocabulary of who is calling, and
sits under the others. The cells that are not are the ones above: agents and estate and connectivity with each other, operators
with fusion, estate calling advice, and the uses of the shell. (The matrix is as of the last cut; the number of lines in `module_edges.txt` is the count.)

## Decided

**Each module becomes its own Go package.** The compiler then enforces the line, which the test lists only approximate. Two
consequences. A package cannot import its caller, so the kernel (the `Core`, the store, the CA, the audit trail, the clock) has to
be a package of its own before any module can leave, and the 29 places where the kernel reaches into modules have to be cut first.
And a method cannot be defined on a type from another package, while `Core` has methods in every module's files, so each module
turns its `Core` methods into a service of its own that holds the kernel. That rewrite is the work of each move. The hub cluster
(agents, estate, connectivity) stayed together until the per-agent `view` was in three parts; it is now four packages (observed, agents,
connectivity, estate) and the test lists guard what is left inside `internal/server`.

**The central operator is an Operator that FUSION manages.** Operators owns the type and all its mechanics (certificates,
address, senders, heartbeat). FUSION owns the lifecycle of the one instance: it creates and renews it by calling operators.
Operators stops importing FUSION: it defines a small interface for an operator that someone else manages (the refusal to change
it, the exposure hints, the address), FUSION implements it, and the operator row records who manages it instead of operators
recognising an id. That is one small schema addition, to be brought as its own step.

## The order I would move in, and what each needs first

This is a proposal from the numbers above; each move starts with its own short plan.

1. **The kernel's reaches into modules** (29 uses): done, in three cuts (see Progress).
2. **The kernel as a package**: done (`internal/kernel`; see Progress). The HTTP kit (not a module) beside it is done too (`internal/httpkit`, see Progress). Until the handlers move, a module package holds domain logic only, and its HTTP handlers stay in the shell as thin adapters.
3. **Accounts**: done (`internal/accounts`; see Progress). The one edge to estate was not a callback in the end: `SaveWorkspace` had been written in `auth.go` and is estate's. Its 15 uses of the shell were HTTP handlers and the route guard's reading of a level, which went to the shell, and the key a context carries its caller under, which stayed with the caller's type.
4. **Advice**: done (`internal/decisionloop`; see Progress). The hub's 15 calls into it became one function for the placement request and one for the recorder; what the loop asks of agents and of the estate is two ports it owns (`Access`, `Load`), which those modules implement and the Hub wires in when it is built. The package is named for what it is; `internal/advice` was already the capacity calculation it uses.
5. **Operators and telemetry**: done (`internal/operators` and `internal/telemetry`; see Progress). Their HTTP and Hub-bound files are the shell's, as the proposal handlers were; what is left is Core methods, and it reached outside in five places, all cut. The central operator's health is read from a `ManagedStatus` that operators define and `FusionStatus.Managed()` fills, so FUSION will call operators and operators do not know FUSION. Two changes from the plan above. The agents-to-telemetry hook was not needed: telemetry never imports agents, so ending an agent's intents is a direct call (`Core.Telemetry.EndAgentIntents`). The schema column that says who manages an operator was not added: nothing reads it until fusion becomes a package, and a migration that changes no behaviour belongs with the change that uses it. Then **fusion**.
6. **Fusion**: done (`internal/fusion`; see Progress). Nine of its twelve files are HTTP and are the shell's; the control, the retention and the release naming (about 1,660 lines) are the package. It reached outside in one place, the doubts about the gateway's address, which the shell now composes from the control's `GatewayService`. It stands on operators (the central operator is one of them), which is the direction the plan asked for. The "managed by" column on the operator row is still not added: the central operator is known by its fixed id, and a second manager does not exist yet.
7. **The hub cluster**: in progress. The `view` is in three parts (done) and the cluster no longer reaches the shell. The Hub-receiver methods that sat in agents and connectivity files are estate's now (they are operations on the live hub). What the flows say became a module of its own, `observed`, under agents and connectivity. The layers are observed, then agents, then connectivity, then estate on top, and the edge list shows no use upward. `observed`, `agents`, `connectivity` and the estate are packages now, and the hub cluster is done: what is left in `internal/server` is the shell, and the edge list between modules is empty. The estate took the sixteen files of the Hub and 94 of its tests; `Core.Estate` holds the Core for the Hub, which `Platform` makes for each organisation. The 66 fixture declarations those tests need (the environment, the gRPC rig, the scripted agent) are copied into `internal/estate/fixtures_test.go`, because the shell's tests need the originals; one fixture package both import is what removes the copy.


## The handlers that remain: operators and FUSION

Everything else has left the shell's route list (see Progress, Handlers steps 1 to 11). What is left is two groups of files, about 2,400
lines for the operators and about 3,600 for FUSION, plus the two small things that stay on purpose (`info`, which is the server describing
itself, and the chart download). They are one knot and not two, so this is the plan for the knot before any of it moves.

**What they share.** Read from what each file takes from `Admin`:

- *The central operator.* The operators' handlers treat one operator (`operators.CentralOperatorID`, the gateway FUSION runs) differently
  in about a dozen places: its address is FUSION's `CentralEndpoint()` while FUSION is exposed, setting its address sets FUSION's
  public address and asks the gateway's Service for its port, its candidates are looked up in the gateway's namespace, its health is
  FUSION's. Each of these is a call on `*fusion.FusionControl` (`Exposed`, `CentralEndpoint`, `WithGatewayPort`, `SetPublicAddress`,
  `GatewayService`, `ServiceName`, `Status`) or a read of two of its fields (`Namespace`, `ClusterUID`), which become methods on the port. The direction the code already has is FUSION calls operators and
  operators do not know FUSION (`ManagedStatus`), so the handlers cannot import `fusion` either. They need a port that operators own.
- *The install settings* (`installcfg.Settings`): solved in Handlers step 5. Operators use the chart, version and image methods; FUSION
  uses none.
- *The hub.* The operators' address candidates and sender health read what agents reported (`estate.Hub`). Those two files are hub-bound
  routes, like the agents' were, and belong with `estate.API`, not with the operators' own.
- *FUSION serves one organisation* (`FusionControl.Org`), and its data routes check the request's organisation against it (`fusionOrg`).
  The link routes and the history snapshot FUSION reads (`topologyAt`, `GraphAPI`, now estate's) sit on the same side.
- *What FUSION adds that is the shell's own*: the limiters (`fusionRL`, `fusionPageRL`), the client address, the secure-cookie setting,
  the ticket and cookie that open FUSION's own pages (`fusion_ui_access.go`, `admin_fusion_proxy.go`).

**The steps, in this order, each its own patch with the tests and ratchets as before.**

1. *The central operator is a port* (small, no routes move). `operators.Central` is an interface of the calls above; `FusionControl`
   satisfies it (a compile-time assertion in `fusion`); the shell hands the operators' handlers `a.central()`, which returns a nil
   interface, not a typed nil, when the server has no FUSION. The operators' docs take a `ManagedStatus`, not a `fusion.FusionStatus`.
   After this the operators' files name no `fusion` type.
2. *The operators' routes* (about 1,500 lines: create, list, get, scope, install and uninstall documents, certificates, address and its
   check, revoke, delete, the destinations list, and the heartbeat: the administrators' route and the operators' own secret-authenticated
   one) become `operatorsapi`, a package of its own like `accountsapi`. It imports operators, installcfg, install, telemetry, httpkit,
   kernel, store and accounts, and is given the `Central` port, the install settings and the CA pin.
3. *The hub-bound operator routes* (address candidates, the server cluster, sender health: about 1,300 lines) join `estate.API`, with
   the operators service and the `Central` port as further things the shell hands it.
4. *The telemetry-intent command route* (it composes an operator's endpoint with the install command for an agent) joins
   `operatorsapi`, because the operators' addressing is what it needs; it is mounted from the shell like the rest.
5. *FUSION's control routes* (status, enable, disable, retention, tokens: about 130 lines) become `fusion.API`; the package already owns
   the control, and the kit and accounts are the only imports it gains.
6. *FUSION's data routes* (the proxied data, the operations table, the native operations, the OpenAPI document, application info, the
   extras it is given, the links and the credential rotation: about 3,000 lines) become `fusionhttp`, standing on fusion, fusionapi,
   estate, httpkit and accounts. This is the largest and goes in three patches by file group, the shared plumbing (`fusionCaller`,
   `fusionScope`, `fusionErr`) first.
7. *The gateway to FUSION's own pages* (the ticket, the cookie, the proxy: about 700 lines) is last, and may stay in the shell. It is
   cookies, origins and the limiters, which are the shell's, and a package would be handed everything it needs and own only the
   control flow.

**What I would watch.** A typed nil in step 1 (an interface holding a nil `*FusionControl` is not nil). The body-size limits that the
guard keys by path (the workspace save, `/decisions`; none for these routes today, but FUSION's bulk routes stream and must keep doing
so). The limiters, which are per server and not per request, so they are handed in as functions and not copied. The `module_store.txt`
ratchet will list `ListOperatorCerts` and a few other store methods as shared by operators and FUSION until step 6; that is true, and
settles when FUSION's handlers are a package.

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
added without choosing. The uses across modules are in `testdata/module_edges.txt`; a use that is not in it fails the test and
names the file, and a line for a use that is gone also fails, so the list can only shrink without a decision. The store methods that
more than one module calls are in `testdata/module_store.txt`, held the same way. Accept a change on purpose with
`go test ./internal/server -run TestModule -update`. The test type-checks the package with `go list -export` and the standard
library, so it adds no dependency; it is skipped with `-short`. The kernel needs no such test for its own direction: it is a package,
so the compiler refuses an import of the server, and `internal/kernel/imports_test.go` refuses an import of any other package of
this repository that a module could be (it may use the store, the CA and the two libraries its settings use).

## Progress

| Step | Commit | What it did to the lists |
|---|---|---|
| The map | step 1 | 549 uses across modules, 22 shared store methods |
| Kernel cut 1: the link settings | the link types and checks move into the kernel with `Settings` | 12 lines leave |
| Kernel cut 2: the life of an agent's identity | `core.go` to `agent_lifecycle.go` | 6 leave, 2 appear that were hidden (agents to telemetry, estate to agents) |
| Kernel cut 3: what the Core held for modules | `state.go`: a slot per module; the namespace check to the kernel | 11 leave. The kernel depends on no module. Now 521 lines, and 21 shared store methods |
| Kernel cut 4: the last reaches into the shell | `admin_images.go` and `counters.go`; the image settings and the metrics counters split in two | 28 lines leave; the kernel depends on nothing outside itself |
| Kernel step 2a: export | 69 unexported names that other files use become exported, by object identity (933 sites) | none |
| Kernel step 2b: the kernel as a package | `internal/kernel` takes the 11 files and their unit tests; `server.Core` embeds `*kernel.Core` (`kernel_bridge.go`), and every use of a kernel name is `kernel.X` (1,780 sites, in this package and in the five that import it) | the store list loses one line (the kernel no longer counts as a caller of `GetAgent`); the list of edges between modules is unchanged |
| Accounts cut 1: nothing in accounts reaches outside it | `SaveWorkspace` to estate; the mail handlers and the guard's reading of a level to the shell; `WithCaller` | the accounts lines leave the edge list |
| Accounts step 1: export | 13 unexported names other files use become exported (66 sites) | none |
| Accounts step 2: the package | `internal/accounts` takes the 8 files and 12 of their tests; the server's `Core` holds an `*accounts.Service` (`Core.Accounts`) and the 66 methods the `Core` had for accounts are now `Core.Accounts.X` | the 39 lines with accounts in them leave the edge list: the compiler holds that line now |
| Advice cuts: what the loop asks of agents and the estate is two seams | `planAccessChange` and the hub's views behind `accessPlan` and `agentLoad`; the HTTP halves of proposals and decisions to the shell | the advice lines to agents and estate shrink to those two |
| Advice step 1: export | `enrichDecision` (with its document type) moves from `twin.go` next to the capacity view, so estate calls one function; the names others use are exported | none |
| Advice step 2: the package | `internal/decisionloop` takes six files and the signing and enrichment tests; `Core.Loop` is a `*decisionloop.Service` whose ports `NewHub` sets; the Hub methods the loop had are `Hub.C.Loop.X` | the advice lines to estate and shell leave; what remains is the two adapters the Hub wires in, and `AccessChange`, which agents read |
| Operators cut 1: nothing in the operators domain files reaches outside them | the admin_ files, `operator_candidates`, `operator_senders` and `self_probe` are the shell's; `MaxExcluded` and `Plural` to the kernel; `ServiceTypeFor`, `CentralOperatorID` and the guard into operators; `ManagedStatus`; ending an agent's intents moves to telemetry | the operators lines to fusion, agents, estate, connectivity and telemetry leave |
| Operators step 1: export | the names other files use of the domain files are exported (197 sites) | none |
| Operators step 2: the packages | `internal/operators` takes six files and its unit tests (the CA cache, the address and audit-list checks), `internal/telemetry` takes two; `Core.Operators` and `Core.Telemetry`, telemetry standing on operators | the 23 lines with operators or telemetry as the user leave the edge list: the compiler holds that line now |
| Fusion cut 1: the fusion domain files reach nowhere | nine HTTP files to the shell; the gateway-address warnings move to the shell | the fusion lines to connectivity and estate leave |
| Fusion step 1: export | the names tests and handlers use are exported; two accessors replace field access in tests; the clock method is `Clock` | none |
| Fusion step 2: the package | `internal/fusion` takes three files and four unit tests; `Core.Fusion` holds the Core and the operators; three methods the shell had hung on the control became functions | the fusion lines leave the edge list |
| Hub cluster step 1: the view in three parts | `view` is `estView`, `agtView` (`agent_view.go`) and `netView` (`net_view.go`), embedded, so the reads stay the same | none; the fields now have an owner |
| Hub cluster cut 1: the cluster's domain files reach nowhere outside it | the admin_ files of agents and connectivity to the shell; `admin_twin.go` for the model handler; `teardown.go` for what a teardown removes; `hintsOf` to estate | the cluster lines to the shell leave |
| Hub cluster cut 2: layers inside the cluster | 49 Hub-receiver methods of agents and connectivity files move to `hub_*.go` (estate); `rfc` to the kernel (`RFC`, `RFCP`); `observed.go` is a module of its own with `clusterOf` and `refusalEvery`; the note-functions are methods of the part they change (`agtView`, `netView`); `link_caps.go` and `ScopeDoc`, `eventLinker` to agents; `condInputsOf` and the access adapter to estate | agents and connectivity use nothing of estate; agents use nothing of connectivity |
| Observed step 1: export | the names the rest of the cluster uses of the flows are exported and named as a package names them (`observed.Cluster`, `FlowTable`, `Topology`, `AddrIndex`...), 363 sites | none |
| Observed step 2: the package | `internal/observed` takes `observed.go` and 39 of its tests (the ones that stand on nothing of the hub); six small test fixtures are copied into the server's tests for the tests there that feed flows; three variables called `observed` are renamed so they do not hide the package | the observed lines leave the edge list |
| Agents step 1: export | the names estate, connectivity and the shell use of the agents' files are exported (523 sites): the agent's part of the view is `agents.View`, with its fields and those of its extension; the consent, tier and conditions functions the hub calls; `Core.pendingTTL` is `EffectivePendingTTL` (the kernel Core has a field of that name). The two geo databases the tests build are `geoip/geoiptest` | none |
| Agents step 2: the package | `internal/agents` takes sixteen files and 41 of their tests (the ones that stand on nothing of the hub); `Core.Agents` is an `*agents.Service` that holds the Core, the operators and the telemetry (an agent's revocation ends its intents; its stream renews its telemetry certificate); the variables called `agents` in the shell are `agentList`. The store-sharing list now reads the packages too, so the tables they share through the store are still listed | the agents lines leave the edge list |
| Connectivity step 1: export | the names the hub and the shell use of connectivity's files are exported (725 sites): the network part of the view is `connectivity.NetView`, the target and path trackers, the link planning types, the correlation functions; the function that lists the steps of a link is `LinkSetupSteps` | none |
| Connectivity step 2: the package | `internal/connectivity` takes nine files and 52 of their tests (the config compile, its digest and sections, the config hash gate, the tunnels, the path summary); `Core.Connectivity` holds the Core for the sealed link credentials. The edge list between modules is empty from here: the only modules left in the package are the estate and the shell | the connectivity lines leave the edge list |
| Estate step 1: the shell asks the Hub | what the shell read of the Hub's lock, its maps and its views is now `ReadStates` (a copy-out, under the lock, of what each agent reported), `Connected`, `ExportHealthOf`, `Flush` (was `twinTick`) and `DropAll` (moved from `tenants.go`); `PushConfigs`, `ReachOf` and `HintsOf` are exported. The hand-rolled `itoa`, which printed nothing for a negative number, is `strconv` | none |
| Estate step 2: export | the names the tests that stay in the shell use of the Hub are exported (the lock, the views and the sessions, `View` and its fields, `ApplySync`, `ConfigFor`, `PushOne` and a few more), with a note on the Hub that they are for the tests of what stands on it. `LoadFlowTable` is exported for the doctor's stage 1 | none |
| Estate step 3: the package | `internal/estate` takes sixteen files and 94 of their tests; `Core.Estate` is the Core for the Hub (the agents, connectivity and the decision loop it stands on); the Hub finders the operator candidates used are `hubHosts` in the shell, over `ReadStates`. The tests that drive the shell (admin API, platform) stay and reach the Hub through the exported names; the fixtures the moved tests need are copied (66 declarations) | the module list holds only the kernel bridge and the shell |
| Testkit step 1: the pure fixtures | `internal/testkit` holds what a test builds without a server: CSRs and approval codes, graph and flow builders, the stubs (a store that fails, a recording linker, a locked buffer). Written once; the estate's copy of them is gone and about 960 call sites use `testkit.X` | the shell and the estate share them; neither keeps its own |
| Testkit step 2: the world, enrolment and the wire | `testkit.World` is the SQLite file, the CA, the clock and the kernel Core every test server stands on; `Enroll`, `ApprovedAgent`, `EnrollCoded`, `TryEnroll` enrol through an `agents.Service`; `Serve`, `Dial`, `DialProxied` are the gRPC listener and its clients; `FakeAgent` is the scripted agent (connect, next Config, beat, wait). The shell's and the estate's `env`, `rig` and `fakeAgent` keep only what stands on their own Hub or Core: `fakeAgent` embeds the kit's and adds `drop`, `restart`, `inForce` and `changeTier` | what stays duplicated is exactly what cannot be shared: the estate's tests cannot import a package that imports the estate (`hubRig`, `credRig`, `liveView`, `linkHub`) |
| HTTP kit step 1: the plumbing is a package | `internal/httpkit` holds what every handler needs and none owns: `WriteJSON`, `WriteErr`, `Fail` (the status of each error kind; an error from outside the kernel is logged and answered as an internal error that says nothing), `Decode`, `ParseTime`, `Principal`, `Actor`, and the role levels `Need` (`AnySession` to `OwnerRole`, with their names in the route table and the act each asks of `accounts`). It imports only the kernel and `accounts`. About 480 uses in 24 shell files are rewritten by name; behaviour is unchanged | what stays in the shell is what needs the server itself: `Admin.fail` (the kit's `Fail` with the server's log), `Admin.core` and `tn` (the organisation a request is for), `guard`, the limiters and the metrics. A module's handlers will be given a lookup for their own service instead of reaching for the Tenant |
| Handlers step 1: the decision log | the two decision-log routes (`POST` and `GET .../decisions`) are `decisionloop.API`, mounted from the shell with `API{For: ...}.Mount(routes)`. `For` is the one thing a module's handlers ask the shell for: the module's service for the organisation a request is for. `httpkit.Router` is where they mount (the shell's guard, role and route table apply to what is mounted), and `httpkit.OrgPath` the prefix. The pattern for the other modules | a module that accounts' own handlers cannot follow as is: `httpkit` imports `accounts`, so accounts' handlers need a package of their own |
| Handlers step 2: the proposals | the six proposal routes (submit, list, advisors, get, accept, reject) join the decision log in `decisionloop.API`; `admin_proposals.go` is gone from the shell. Same handlers, now reading the loop through `For` | |
| Handlers step 3: telemetry | the seven telemetry-intent routes and the two quick-start gateway-token routes are `telemetry.API`; `admin_quickstart.go` is gone from the shell and `admin_telemetry_intents.go` keeps only the install command (`POST .../command`), which is built from the install settings and the operators' addresses. The destination JSON shape both operators and intents use is `operators.DestinationDoc`; `httpkit.Reasoned` is the "optional reason" answer agents' revoke and the intents' revoke share | the command route and `GET .../telemetry/self` stay in the shell: they read the hub and the install settings |
| Handlers step 4: accounts | the 37 routes of sign-in and sign-out, two-factor, passkeys, personal API tokens, organisations, members and invitations are `accountsapi.API` (`admin.go` goes from 1,702 lines to 964). It is a package of its own because `httpkit` imports `accounts`. The shell hands it an `Edge`: the client address, the session cookie (set and read), the relying party, the sign-in throttle, and what ends the FUSION page sessions. `httpkit.Router` gains `Open` for the routes that ask nothing of the caller. Existing tests of the cookie, the pages and the roles caught each mutation of the moved code | the guard (it reads the cookie and finds the organisation), the mail settings and `GET .../info` stay in the shell |
| Handlers step 5: the install settings | `internal/installcfg.Settings` is how the server was told to write the commands it prints: the chart reference and its versions (agent and regional operator), the image defaults and the organisation's override (`Images`), the address agents dial, the public address, the private CA for the heartbeat. `Admin` embeds it, so the flags and the tests still say `a.ChartRef`; `AgentInstallCommand` and `AgentUpgradeCommand` write the two commands from it (the shell's `installCommand` and `upgradeCommand` only add the CA pin and the agent's reported release). It imports the kernel, the charts and the command writers | this is what the agents', operators' and telemetry routes were leaning on; they can move now, each given the `Settings` |
| Handlers step 6: the agents' routes | the eleven routes for the agents of an organisation (the enrolment tokens: list, create, delete; approve, reject, revoke; the address tests, start and list; tier; consent) and the command that upgrades an installed agent are `estate.API`, mounted with the hub as its lookup (`For`), the install settings and the CA pin the commands carry. They live in estate, not agents, because tier, consent and address tests are the hub's (`SetAgentTier`, `SetConsent`, `StartAddressTests`) and the hub stands on agents. `estate.UpgradeCommand` is the one place the upgrade command is written, for these routes and for the state route still in the shell; `installcfg.ConfigError` is how a command the server cannot write is logged and told. `admin_commands.go` is gone from the shell; `admin_agents.go` keeps two small helpers the state route still uses | the state route is next |
| Handlers step 7: state, model, workspace | the four routes the page polls and saves (`GET .../state`, `GET .../model`, `GET` and `PUT .../workspace`) join `estate.API`. The state route's upgrade commands come from `estate.UpgradeCommand`, so the shell no longer has its own copy of the logic; `withoutDiagnostics`, the etag match and the workspace's time formatting move with their handlers. `admin_twin.go` is gone from the shell; `admin_agents.go` keeps only the `upgradeCommand` wrapper its tests call | history and settings are next |
| Handlers step 8: the estate's memory | the ten routes that read what the estate remembers (`storage`, `timeline`, the four `graph/...` reads, `audit`, `workspace/revisions`, `workspace/at`, and `events`) join `estate.API`. `GraphAPI`, the questions only a graph database can answer, is declared in estate; the routes find it by asking the organisation's store whether it implements it, as the shell did, and answer as before when it does not. `admin_graph.go` is gone; `graph_api.go` keeps the one-line lookup the shell's history routes and FUSION still use | `module_store.txt`: `ListAudit` is estate's alone now, and `ListEvents` is shared by estate and the shell (FUSION's history reads it) until FUSION moves |
| Handlers step 9: history | the five history routes (the index, a snapshot, traffic, one dependency's series, and recording a snapshot now) join `estate.API` with the traffic cache and the per-snapshot decode cache they share, which stay one per organisation (`kernel.OrgState`). `ListHistory` leaves `module_store.txt`: only the estate reads it now | |
| Handlers step 10: settings and the external decider | `POST .../decide` is `decisionloop.API.decide`: the decider is the decision loop's, and the model a request is checked against reaches it as a function the shell hands in (`Model`, which the estate builds), as the server's version does (`Version`). The two settings routes are `settingsapi.API`, a package of its own like `accountsapi`: the settings are the kernel's, and the kernel cannot hold HTTP handlers (the HTTP kit stands on it). It imports only the kernel, accounts and the kit. `admin_history.go` is gone from the shell | |
| Handlers step 11: mail and self-telemetry | the server's two mail-settings routes join `accountsapi.API` (they are not an organisation's: the guard asks only that the person has settled in one, and the handler asks for an owner of the default organisation); `GET .../telemetry/self` joins `estate.API` with the code that turns the agents' and the server's self-reports into samples, and that code's unit tests. What stays in the shell, and why: `info` and the chart download are the server describing and serving itself; the links routes and the telemetry-intent command are tangled with FUSION and the operators and move with them | operators and FUSION: a design step first (their shared state), then the moves |
| Operators and FUSION step 1: the central operator is a port | `operators.Central` is what the operators' handlers know of the gateway FUSION runs: its organisation, its state (`Managed`, which now carries `LastDataAt`), whether it is exposed and where it is dialled, completing and recording its public address, its Service, and where it runs. `FusionControl.Central()` is the adapter; it returns a nil interface, not a typed nil, when the server has no control. The six operator files no longer name `a.Fusion` or import `fusion`, and a test says so; `fusionState` is `managedState`, and the address warnings for the gateway are `centralAddressWarnings` on the port. No route moved. Found on the way: "FUSION is managed from one organisation, so another organisation is not told its state" had no test, and now has one | the operators' routes (step 2) are a package now: nothing of FUSION goes with them |
| Operators and FUSION step 2: the operators' routes are a package | `operatorsapi` holds twelve routes (list, create, get, scope, install again, certificates, address and its check, heartbeat enabling, revoke, delete, destinations) with the documents they answer with and every command they print, in four files: `docs.go`, `commands.go`, `endpoints.go` (where an operator is dialled from elsewhere) and `operators.go`. Its edges are the operators of the request's organisation, the central operator (`operators.Central`), the install settings, `Secure` (the scheme of the request) and `AgentRelease` (which release an agent runs as, so the package never meets the hub). The shell builds it once (`Admin.operatorsAPI`) and asks it where it writes a command for an operator: the telemetry intents' command, the senders, the install check. The heartbeat receiver stays in the shell (`operator_heartbeat_receiver.go`: open, throttled and authenticated by its own secret); the address-candidates, server-cluster and senders routes stay too, for they need the hub. The address-warning and probe tests moved with the code, and `testkit.FakeDNS` is shared | the hub-bound operator routes (step 3) and the telemetry intents' command (step 4) can now ask a package, not the shell, for every command |
| Operators and FUSION step 3a: the senders route is in `operatorsapi` | `GET .../operators/{id}/senders` and its readings (the addresses each sender dials, how delivery goes, and which end a fault is on) moved with the diagnosis, which is pure and has its tests beside it. The hub is reached through a two-method port, `SenderHub` (`ReachOf`, `ExportHealthOf`; `*estate.Hub` is one), and the telemetry intents through the service the shell hands in, so the package still does not import the estate | the address-candidates and server-cluster routes (step 3b) are the last of the operator routes that need the hub's facts |
| Operators and FUSION step 3b: the address candidates and the server's cluster are in `estate` | `GET .../operators/{id}/address-candidates` and `PUT .../operators/{id}/server-cluster` are mounted by `estate.API`, with the reading of the hub's facts they stand on (`operator_hosts.go`: the cluster an operator runs in, its nodes and the ranking of the addresses they offer, hints included) and the pure tests of it. The API is handed the operators of the request's organisation, the central operator (`operators.Central`) and, for an install with no agent in the server's own cluster, the nodes the chart's node probes reported (`ServerNodes`; the shell reads `probe.Receiver` and converts, `withSelfProbe`, which merges them, moved). `ExposeCommand` is exported for the golden test | the telemetry intents' command (step 4) is the last operator route in the shell |
| Operators and FUSION step 4: the telemetry intent's command is in `operatorsapi` | `POST .../telemetry-intents/{id}/command` (an administrator's: it can mint an operator's client certificate) moved with the page's whole-block answer and the checks that come before anything is issued. It is the last operator route that was in the shell, so `admin.go` no longer names an operator handler, and the test that held those files to the `operators.Central` port is gone: the package's import test says the same thing for all of them. `IntentProvenanceValues` is exported for the golden test | FUSION control (step 5) and the FUSION data routes (step 6) are what the shell still serves |
| Operators and FUSION step 5: the FUSION switch is `fusion.API` | `GET .../fusion`, enable, disable and the two retention routes are mounted by the package that owns the control (`api.go`), with the switch's document (`Doc`, `CentralDoc`, `PageLinks`). They are all an administrator's, reads included, and a test says so. What the package is handed beyond the organisation's view and the control (read for every request, as tests and the install change it): the two paths the server serves FUSION's own pages at, and the function that says what is doubtful about the gateway's address (that is the operators' to say, so the package does not import them). Nothing about a server with no control changed: the nil control still answers with its reason. The page links moved with it (`FusionControl.PageLinks`); the shell's `uiLinksOf` is a one-line call of it | the FUSION data routes (step 6) and the pages (step 7) are what the shell still serves of FUSION |
| Operators and FUSION step 6a: the FUSION access tokens are in `fusion` | The rules of a token (what it may be named and scoped to, the cap of fifty, how long it lasts, what authenticating one does) are methods of `fusion.Service`, in `tokens.go`; the three token routes are `fusion.API`'s, behind one check that FUSION exists and that the request is for the organisation it belongs to (the API is handed that organisation, `Org`). `fusion_data.go` keeps only the data routes, whose caller is a token or an administrator and which authenticate through `Fusion.AuthenticateFusionToken` | the data routes (6b onwards) no longer carry token minting along |
| Operators and FUSION step 6b: the shared plumbing of the data routes is `fusionhttp` | `fusionhttp` holds what every data route has in common: the route table type `Op` with the parameter tables and `CheckQuery`, the OpenAPI description and docs page built from it (the embedded `docs/`), the `Guard` (who is calling: a FUSION token, or an administrator by session or personal token; the failed-authentication throttle, the per-caller rate limit, the 30 s budget and the write grace; the 503 when FUSION is off), and the helpers a handler reads a request with (`Range`, `Scope`, `Groups`, `Err`). It is handed the credentials a request carries, the client address, the two limiters, the organisation and what Ikhnos knows (`Extras`) as edges, and mounts through its own small `Router` (`Open` and `Credential`, which the shell records as `public` and `fusion-credential`, so the route table is unchanged). The handlers and the table of routes are still in the shell, and call the guard | the handler moves of 6c and 6d need only the guard and the edges for the application and link figures |
| Operators and FUSION step 6c: the metric, log and trace routes are `fusionhttp` | The metric names, series and range reads, the raw PromQL passthrough, the log search, and the trace search, single read and batch (fused, bulk and streamed, charged per trace against the same rate limit) are methods of `fusionhttp.Guard`, and their rows of the route table (`Guard.ReadOps`) sit beside them, spliced into the shell's table between the link row and the native operations so the order of the description is unchanged. The guard is handed the clock. A nil guard still describes the routes, since the table is read to build the OpenAPI document before there is a server. The pure helpers (the batch body's promql and option forms, the stream switch) have unit tests in the package; the routes keep their end-to-end tests in the shell, where the stores are faked | `fusion_data.go` is down to the status, application, service and topology handlers, the ones that need what Ikhnos knows (6d) |
| Operators and FUSION step 6d: the whole data API is `fusionhttp` but for the link figures | The status, application, service and application-topology handlers, the native Prometheus, Loki and Tempo mirror, and the table of routes itself (`Guard.Ops`) are in `fusionhttp`; `fusion_data.go`, `fusion_native.go` and `fusion_ops.go` leave the shell. What Ikhnos knows reaches them through the one edge that already carried it (`Extras`: applications, topology, events), and the `/links` route is described in the table but answered by a handler the server hands in (`Guard.Links`), because it reads the links the organisation watches. The check that every query parameter a handler reads is described is made in `fusionhttp` for its handlers and in the shell for the links handler | what is left of FUSION in the shell is the Ikhnos side: extras, application info pushed to FUSION, the link figures and credential rotation, and the gateway to FUSION's own pages (step 7) |
| Operators and FUSION step 6e: the links are `linksapi` | The Sites screen's latest figures (`GET links/latest`), the same figures in FUSION's data API (`/fusion/links`) and the replacing of the throughput tests' credentials (`POST links/rotate-credentials`) are `linksapi.API`, with the pure work of turning what the clusters reported into a pair's figures, probe state and diagnosis, and the cache that shares one read of FUSION between everyone who has the screen open. It is handed the organisation's core and connectivity, the hub (two methods: the notes and setup it works out, and the push of new configuration), FUSION's data client and organisation, the settings of the organisation FUSION belongs to, and the data API's way of writing a failure. It imports connectivity, fusionapi, fusionhttp, httpkit and the kernel, and the hub is a port it is given. `links_latest.go` and `admin_link_credentials.go` leave the shell; `links_wire.go` builds the API once, since the cache is its state | what is left of FUSION in the shell is extras, the application info pushed to FUSION, and the gateway to FUSION's own pages (step 7) |
| Operators and FUSION step 7: the rules of the gateway to FUSION's pages are `fusionpages` | The ticket and page-session table (one-time tickets that live thirty seconds, page sessions of eight hours, a cap per person and in all, the person's oldest dropped first, a full table that answers busy without spending the ticket) and the proxy's rules (which methods and paths a page may use on Prometheus and Grafana, what is refused outright, redirects only to this server, what is stripped from a response, the namespaced Grafana login) are in `fusionpages`, which imports only the kernel and has its own tests. The cookie, the origin check, the two limiters, the reverse proxy and the three handlers stay in the shell, because they are the server's cookies and limiters and a package would be handed all of them to own only the control flow | the FUSION handler moves are done: what stays in the shell for FUSION is wiring (guard, links and operators API construction), extras, and the application info pushed to FUSION |
| Whole-graph list: the picture now | step 8 | `testdata/packages.txt` and `TestPackagesImportEachOtherOnlyWhereTheListSays`: every import between the packages of `internal/` in one list; this page's first section brought up to the present |
| Small read interfaces: telemetry, agents | step 9 | `telemetry` owns `Operators` (the operator a destination names; `CheckDestination`, which is the validation and the normalising of the address in one call, added to the operators) and no longer imports them; the destination document (`operators.DestinationDoc`, now `destination.Doc`) is the package `internal/destination` that both the operators' routes and the intents carry; `agents` owns `TelemetryCerts` for the certificate renewed on the stream and its own `AccessChange`, and imports neither the operators nor the decision loop. In the whole-graph list three edges go (`agents` to `operators`, `agents` to `decisionloop`, `telemetry` to `operators`) and three come (`destination` to `store`, `telemetry` and `operatorsapi` to `destination`) | the three concrete reaches left by step 8 are gone; `estate` building the loop stays, on purpose |
| The wire shapes are generated | step 10 | `internal/wire` (the generator, tests for each kind it writes) and a `WireTypes()` in the nine packages that mount routes; `TestWireFileIsWhatTheServerEncodes` holds `src/lib/generated/wire.ts` (166 interfaces) to the Go types. Nothing reads the file yet; the page adopts it area by area | 188 edges (nine `> wire`); the unnamed shapes (83 maps, 46 request structs) are named next |
| The accounts shapes are named | step 11 | The 24 anonymous request bodies of `accountsapi` are `requests.go` (one type to a route) and its 8 answers that were maps (the session, the two-factor secret and recovery codes, a new API token, a new invitation, an invitation preview, the server's public info, `ok`) are `responses.go`, with fields in the order of their JSON names so the bytes are what a map wrote (pinned by a test). `wire.RequireDocsListed` fails a package's test for a `Doc`, `Request` or `Response` struct that its `WireTypes` does not list. Places a handler writes or reads JSON: named 98 (was 51), inline maps 61 (was 83), anonymous request structs 22 (was 46) | estate, operators, FUSION, decision loop and the rest follow |
| The estate shapes are named | step 12 | The 27 answers of `estate` that were maps at the handler (storage, the graph snapshot, walks and diff, the timeline, the audit search, the workspace in its four forms, the events, the three history answers, a new token, an agent's tier, the address tests, the server-cluster choice) are `answers.go`, and its 6 anonymous request bodies are `requests.go`; the fields are in the order of their JSON names so the bytes are what a map wrote, and a test holds each answer to the map it replaced. The answers whose keys come and go (storage, the workspace, the tier) are optional fields, not a second type. The traffic cache holds a `trafficDoc`, not a map. Places a handler writes or reads JSON: named 131 (was 98), inline maps 34 (was 61), anonymous request structs 16 (was 22) | operators, FUSION, the decision loop and the rest follow |
| The decision loop, telemetry, settings and links shapes are named | step 13 | The decision loop's 5 map answers and 3 request bodies, telemetry's 2 gateway-token answers and 4 request bodies, the settings save body and its 409 answer (the settings document with an `error` beside it), and the links package's rotation body, its answer and the FUSION links document: each is a type in the package's `requests.go`, `responses.go` or `bodies.go`, and each map answer is held by a test to the map it replaced. `wire.RequireDocsListed` now guards all four packages. Places a handler writes or reads JSON: named 151 (was 131), inline maps 25 (was 34), anonymous request structs 7 (was 16) | operators, FUSION and the rest follow |
| The operators' shapes are named | step 14 | The seven answers of `operatorsapi` that were maps (an operator just created or installed again, one whose scope changed, its certificates, the uninstall command, the heartbeat just switched on, and the command for a telemetry intent) are `responses.go`, and its four request bodies `requests.go`. Their keys came and went with the operator's kind, so each is one type with optional fields (`omitempty`; `omitzero` for `problems`, where an empty list is different from none), and a test holds every way each could come out to the map it replaced. `addOperatorTargetExport`, which filled a map it was handed, now returns the export target it found. Places a handler writes or reads JSON: named 164 (was 151), inline maps 16 (was 25), anonymous request structs 3 (was 7) | FUSION and the shell follow |

# The server, cut into modules: the map

`internal/server` is one Go package of about 31,000 lines of source (and as many of tests). Its parts are written in separate files,
but nothing stops any file from using anything in any other, and the compiler cannot tell a fair use from a reach into
somebody's internals. This page draws the lines the package is to be cut along, from the code and not from its file names, and
says what has to be settled before each cut. **Nothing was moved to make it.** The lines are held by a test (see the end).

How it was made: the package was type-checked, and every identifier was resolved to the file that declares it (a function, a type,
a field of a struct, a method, a constant). Each file was assigned to a module (`testdata/modules.txt`), and a use is a place where
a file names something declared in a file of another module. At the first reading there were 549 such uses, once each, in
`testdata/module_edges.txt`. The store was read the same way: which module calls which store method.

## The groups

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

# The redesign: where the plan is and where we are

This file exists so the plan is not lost between sessions. It points at the plan and keeps a short status. Update the status at each gate, in the same commit as the work.

## The plan to follow

**Ikhnos: the plan to follow** (https://claude.ai/artifact/8vfoZWCoq5acknQixSX9UK). One page that merges the two documents below: the target, what is being fixed, the order of stages with their gates, where we are, and the decisions taken and open. Read it first. It is the plan.

It is built from two documents, which stay as the reasoning and the evidence:

1. **Ikhnos architecture: control plane, data plane and the seams between** (https://claude.ai/artifact/UwySnmbgattPpNxM1mTmaV). The first document: the target shape, ten seams, six contracts, a strangler migration.
2. **Ikhnos design: entities, interfaces and channels, from the code** (https://claude.ai/artifact/W1FoXitwyaUmXsZS9CwxaM). A second pass over the first, made against the code and reviewed. It holds the defects D1 to D14, the smaller plan with its stop line, and a progress log.

Where the two differ, the second pass wins.

## Rules that hold throughout

- A phase may delete code, move code or add a contract. It does not add a user feature. Features resume after the stop line.
- Each change is its own commit, with a test that fails without it, a rollback plan and a compatibility plan.
- The stop line asked that the minimal decision loop run in production before anything below it starts. That gate was waived on 2026-10-17, because there is no advisor yet to run it; work that does not depend on what the loop would show goes ahead, one small commit at a time.
- The first three stages change no wire format except one additive protocol change.

## Status

| Stage (first document) | Phase (second document) | State |
|---|---|---|
| 0. Pin behaviour, small defect fixes | 0 | Store, graph history, binaries, server binary: done. Config hash gate, route-guard pin, the full `configFor` output on a test matrix, and the cross-organisation test over every route that takes an id: done. The TypeScript install commands are pinned (74 of them, each rendered through `helm template`). The first fake-agent harness (a scripted agent on a real stream, with seven scenarios on the config echo) is in. Still open: the status words are only partly pinned. |
| 1. Audit written with the change | part of 2 | Done (33 of 34 write sites; link-credential rotation stays on `audited()` on purpose). |
| 2. Config echo, capabilities, server-side Conditions | 1 and 2 (in part) | Done. Config digest echo; a protocol version and named capabilities in the hello (`internal/agentcaps`, recorded in memory, bounded); three Conditions (`Connected`, `ConfigInForce`, `Compatible`) decided in one function and carried on the agent document; the stated ceiling is told by the `diagnostics` capability. No screen reads the new fields yet. Documented in the agent trust model page. |
| 3. Minimal decision loop (Advise mode) | 7 (in part) | Done. Auto mode is designed and shelved. |
| Stop line | | Waived on 2026-10-17 (no advisor yet). The decision loop is still to be run in use with a real advisor, and the order below the line revisited then. |
| Policy kit (after the line, before the server split) | 1 | Done, in four commits. D15: the words the audit trail uses for the server are reserved as usernames. A typed `Actor` (`actor.go`). One tier check (`tier.go`: `CheckTier`, `GrantableTier`, `ReportedTier`) in place of six separate bounds. One `Decide` (`policy.go`) for what a caller may do, including whether a change to an agent's access widens it, and every route (182) mounted through one function and listed in `testdata/routes.txt`. Each has a guard test that fails if the decision is made by hand elsewhere. Not done: organisation scope on the store methods that take a bare id. |
| Config channel (after the policy kit) | 1 and 2 (in part) | Done, in three commits. What an agent is told is compiled from gathered facts by one function that reads nothing (`compileConfig` in `paths.go`), as the sum of five named sections (policy, timing, cluster, links, probes). A test fails if a field of the Config has no owner. The digest is computed from the sections (`config_digest.go`), not written out by hand; it lives in server memory only, so no database change and no extra push. The existing hash gate and config matrix passed unchanged. |

Open defects: D2 (the model version is written on read), D7 (the agent drops held flow edges at reconnect), the frontend half of D6 (the Measured paths screen still shows 0 ms) and the dead quick-start gateway generator. D15 is closed. D14 (a README line) has a patch waiting.

## Next

1. Stages 0 to 3, the policy kit and the config channel are done, apart from the older status words (the Conditions are pinned; the older words are not).
2. The config channel is done. Install rendering is next: a typed install spec and one renderer on the server, in place of about 40 command builders in two languages, with the 74 pinned commands as the check. Then the status model, the server split into eight modules, and generated types with frontend feature folders.
3. When there is an advisor, run the decision loop in use and revisit this order.

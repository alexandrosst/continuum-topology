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
- Nothing below the stop line starts until the minimal decision loop has run in production.
- The first three stages change no wire format except one additive protocol change.

## Status

| Stage (first document) | Phase (second document) | State |
|---|---|---|
| 0. Pin behaviour, small defect fixes | 0 | Store, graph history, binaries, server binary: done. Config hash gate, route-guard pin, the full `configFor` output on a test matrix, and the cross-organisation test over every route that takes an id: done. The TypeScript install commands are pinned (74 of them, each rendered through `helm template`). The first fake-agent harness (a scripted agent on a real stream, with seven scenarios on the config echo) is in. Still open: the status words are only partly pinned. |
| 1. Audit written with the change | part of 2 | Done (33 of 34 write sites; link-credential rotation stays on `audited()` on purpose). |
| 2. Config echo, capabilities, server-side Conditions | 1 and 2 (in part) | Done. Config digest echo; a protocol version and named capabilities in the hello (`internal/agentcaps`, recorded in memory, bounded); three Conditions (`Connected`, `ConfigInForce`, `Compatible`) decided in one function and carried on the agent document; the stated ceiling is told by the `diagnostics` capability. No screen reads the new fields yet. Documented in the agent trust model page. |
| 3. Minimal decision loop (Advise mode) | 7 (in part) | Done. Auto mode is designed and shelved. |
| Stop line | | Everything below waits until stage 3 has run in production. |

Open defects: D2 (the model version is written on read) and D7. D14 (a README line) has a patch waiting.

## Next

1. Stages 0 to 3 are done, apart from the status words (still only partly pinned; Conditions are the first of them and are pinned, the older words are not).
2. Stop here. Review what the decision loop does in use before anything below the line.
3. Decide where the policy kit goes (recommended: after the stop line, before the server split).

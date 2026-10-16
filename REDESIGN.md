# The redesign: where the plan is and where we are

This file exists so the plan is not lost between sessions. It points at the two documents that hold the plan and keeps a short status. Update the status at each gate, in the same commit as the work.

## The two documents

1. **Ikhnos architecture: control plane, data plane and the seams between** (https://claude.ai/artifact/UwySnmbgattPpNxM1mTmaV). The first document, and the base. It describes the target shape: ten seams, six contracts, the decisions, and a strangler migration in phases 0 to 7, each ending at a gate.
2. **Ikhnos design: entities, interfaces and channels, from the code** (https://claude.ai/artifact/W1FoXitwyaUmXsZS9CwxaM). A second pass over the first, made against the code. It lists what the code does today, the defects found (D1 to D14), the prototypes, a reviewer's verdict, and a smaller working plan with a **stop line**. Its last sections are a progress log: Stage 0 results, then one section per slice. Where its last section disagrees with an earlier one, the last wins.

Where the two differ, the second pass wins: it was checked against the code and reviewed. Read both before starting a stage.

## Rules that hold throughout

- A phase may delete code, move code or add a contract. It does not add a user feature. Features resume after the stop line.
- Each change is its own commit, with a test that fails without it, a rollback plan and a compatibility plan.
- Nothing below the stop line starts until the minimal decision loop has run in production.
- The first three stages change no wire format except one additive protocol change.

## Status

| Stage (first document) | Phase (second document) | State |
|---|---|---|
| 0. Pin behaviour, small defect fixes | 0 | Store, graph history, binaries, server binary: done. Config hash gate and route-guard pin: done. Still open: TypeScript install commands through `helm template`, the full `configFor` output on a test matrix, a cross-organisation test over every id-keyed store method, the fake-agent harness. |
| 1. Audit written with the change | part of 2 | Done (33 of 34 write sites; link-credential rotation stays on `audited()` on purpose). |
| 2. Config echo, capabilities, server-side Conditions | 1 and 2 (in part) | Config digest echo done. Capabilities and Conditions: open. |
| 3. Minimal decision loop (Advise mode) | 7 (in part) | Done. Auto mode is designed and shelved. |
| Stop line | | Everything below waits until stage 3 has run in production. |

Open defects: D2 (the model version is written on read) and D7. D14 (a README line) has a patch waiting.

## Next

1. Close stage 0: the four open pins above.
2. Stage 2: capabilities and Conditions, one additive protocol change.
3. Then stop, and review what the decision loop did in use before anything below the line.

---
id: frontend
title: The web UI
description: How the single-page application is organised into feature layers, where its types come from, and how it is tested.
---

# The web UI

The interface is a single-page application in `src/`: React, TypeScript and Tailwind, built with Vite. It reads the server's JSON API and, with no server at all, a built-in sample estate, which is how the screenshots and most component tests run.

<Figure name="FrontendLayers" caption="A feature uses shared code and the layers below it, never one beside it or above. A test holds that." />

## Folders

`src/app/` is the **shell**: routes, layout, and which page loads when. Nothing imports it.

`src/features/<name>/` is one area of the product. Each has its own page, `components/`, `lib/` and `store/`. The features are `topology`, `agents`, `pipeline`, `placement`, `health`, `discovery`, `accounts`, `activity`, `fusion`, `history`, `links`, `proposals` and `settings`.

`src/components/`, `src/lib/` and `src/store/` are **shared**: primitives, the API client, the model, and whatever more than one feature needs. Shared code never imports a feature.

## The layers

Features are in five layers. A feature may use shared code and any feature in a layer *below* its own. It may not use a feature beside it or above it.

| Layer | Features |
|---|---|
| 5 (highest) | `topology` |
| 4 | `agents` |
| 3 | `pipeline` |
| 2 | `discovery`, `health`, `placement` |
| 1 (lowest) | `accounts`, `activity`, `fusion`, `history`, `links`, `proposals`, `settings` |

The map is highest because it may draw on every other feature. The effect to rely on is the other direction: nothing in the lowest layer knows the map exists, so a change in `settings` cannot break the map's code.

`tests-ui/architecture.test.tsx` holds these rules. When it fails, the message names the import and the rule. Move the shared piece down into `src/components` or `src/lib`; do not edit the test.

## Where the types come from

The UI does not hand-write the shapes the server sends. They are generated from Go.

1. A Go type is in an API package, and the package lists the shapes it writes in a `WireTypes()` function.
2. `internal/wire` writes those as TypeScript.
3. The result is `src/lib/generated/wire.ts`, committed, and imported by the UI.
4. A test fails if the committed file differs from what the server would generate.

A renamed field therefore shows up as a changed line in the review of the change that made it. The generator follows what the server really writes: a field with `omitempty` is optional, a pointer without it is `| null`, and a list or map is never null.

If you change a Go type that the API writes, run the wire test, commit the regenerated file, and read the diff in `wire.ts`. That diff is the UI's view of your change.

## The model and the pages

The UI works from an in-browser model of the estate (`src/lib`). Placement and movability rules live there too and mirror the server's, so the UI can give advice with no server and the two agree. They are tested against the same table of cases. The rest of the state (what is selected, which tab is open) lives in `zustand` stores in `src/store` and in each feature's `store/` folder.

The map itself is built on React Flow (`@xyflow/react`). Its nodes and edges are computed in `src/features/topology/lib/graph.ts`, which is plain TypeScript and tested without a browser.

## Testing

| Layer of test | Where | Run with |
|---|---|---|
| Logic: model, advice, placement, history, workspace, consent | `tests/*.test.ts` | `npm run test:unit` |
| Components and pages | `tests-ui/*.test.tsx` | `npm run test:ui` |
| In the browser | `e2e/*.spec.ts` | `npm run test:e2e` |
| The type check and lint | | `npm run build` and `npm run lint` |

The logic tests are plain Node scripts bundled with esbuild, so they run in a second. The component tests use Vitest. A few install-command tests compare against golden files (`tests/install_commands.golden.json`) that were rendered through `helm template`, so a typo in a command the UI prints fails a test.

## Style

The UI has one visual language and changes go through it: spacing, type and colour come from the primitives in `src/components/ui`. If a screen needs a new variant, add it there so that every screen gets it, rather than styling one page differently. Dark and light are both first-class; a change is not done until it looks right in both.

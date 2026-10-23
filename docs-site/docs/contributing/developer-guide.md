---
id: developer-guide
title: Developer guide
description: Building and running the frontend and backend locally, and where things live in the repository.
---

# Developer guide

This is for working on the code itself. If you just want to run the server, see [Getting started](../getting-started/quickstart.md) instead — none of this is required for that.

## Frontend

```bash
npm install
npm run dev      # http://localhost:5173
npm run build    # type-check + production build
```

Without a server connected, state lives in `localStorage` and the app is fully usable on its own — useful for UI work that doesn't need the Go backend at all. Stack: Vite, React 19, TypeScript, Tailwind 4, React Flow (`@xyflow/react`) for the topology canvas, Zustand for state, React Router. The visual style deliberately follows the NetBird dashboard: dark neutrals, one orange accent, the same treatment this documentation site borrows for its own diagrams and theme.

## Backend

```bash
# 1. Build the UI the server will serve
npm run build

# 2. Build and run the server
cd backend
go build -o server ./cmd/server
go build -o agent ./cmd/agent
./server --data-dir ./data --agent-address my-host.example.com:8443 --agent-listen 0.0.0.0:8443 --ui-dir ../dist
```

The first start prints a one-time password for the user `admin` to stderr. `--agent-address` has to be reachable **from wherever your agent runs**, not just from your own machine — for local development, an agent can run entirely outside a cluster:

```bash
IKHNOS_SERVER=host:8443 IKHNOS_CA_PIN=<pin> IKHNOS_TOKEN=<token> \
  ./agent --kubeconfig ~/.kube/config --state-dir ./agent-id
```

## Tests

```bash
cd backend && go vet ./... && go test -race ./...
```

Neo4j-backed tests skip themselves unless `IKHNOS_TEST_NEO4J` is set — you don't need a Neo4j instance running to get a meaningful pass. The frontend has three test suites: pure logic (`npm run test:unit`, plain `node:test`, no DOM), rendered components (`npm run test:ui`, Vitest + React Testing Library + jsdom, in `tests-ui/`), and end-to-end (`npm run test:e2e`, Playwright against a real Chromium browser and the real production build, in `e2e/`) - plus lint (`npm run lint`). All four run in CI on every push and pull request.

The e2e suite exists for one specific class of bug the other two structurally can't see: a real infinite render loop born from the interaction between our state and a UI library's own internals (React Flow's selection/store-sync effects, React's own render-loop guard). jsdom doesn't run React Flow's actual drag/selection/store behavior, so a unit or component test never exercises the code path where that kind of bug lives - only a real browser against the real, built app does. `npm run test:e2e` builds the app and runs Playwright against `vite preview` (`playwright.config.ts`'s `webServer`), so `npx playwright install chromium` is a one-time local prerequisite; CI installs it fresh on every run.

## Where things live

```
src/
  lib/types.ts        Domain model (Cluster, MachineNode, Service, Device, Dependency, Site...)
  lib/migrate.ts       Schema upgrades and normalization
  lib/effective.ts     Two-layer values: detected base + human overrides
  lib/suggestions.ts   What accepting a Discovery suggestion changes
  lib/advice.ts        The same placement cost function the server runs, kept in sync deliberately
  components/          UI, grouped by feature area
  pages/               One file per route

backend/               Go module "ikhnos" (Go 1.25)
  proto/               gRPC contract (Enrollment, AgentService)
  cmd/server           Control plane: CA, enrollment, sync stream, admin JSON API, serves the built UI
  cmd/ikhnos        The one binary the cluster image runs: ikhnos agent | probe | flow
  cmd/agent            In-cluster agent (a thin wrapper over the same code)
  cmd/probe            Optional node probe
  internal/            pki, store (SQLite), server, agent, probe, facts, interpret, model
internal/chart         The agent's Helm chart, embedded in the server, served as a .tgz to the wizard

deploy/helm/ikhnos-server/   The server's own Helm chart
docs-site/                       This documentation site (Docusaurus)
```

## Telemetry doctor

`go test -count=1 -run TestDoctor ./internal/server` checks, without a cluster, that every label keeps its meaning from the agent to a Grafana query. The stages are: the label table against the chart and the dashboards; the agent's flows against the server; the commands the server prints against the rendered agent and operator charts; the gateway, Prometheus and Loki as rendered; the queries the API builds and the dashboards' joins; and the links between Grafana and the stores. The rendered-chart stages need `helm` and skip without it.

Every label lives in one table, `fusionapi.Vocabulary` (`internal/fusionapi/vocab.go`): its OTLP attribute, whether Prometheus promotes it, whether Loki indexes it (a stream label) or keeps it as structured metadata (filterable only after a pipe), and who stamps it. To add a label, add a row. Then run the doctor, and the failing stage names the chart value to change: `prometheus.promoteResourceAttributes` in the FUSION chart's `values.yaml` for a promoted label, the `otlp_config` index list in `loki-config.yaml` for an indexed one, or the stamping processor in the agent, operator or gateway chart. If it is a label the dashboards select on, a LogQL stream selector may name it only if it is indexed.

Its limits: stage 2 proves that the id printed in the command equals `ClusterIDFor(org, fingerprint)` and that the chart stamps it, but does not follow that id through the live hub into the application info series (stage 4 uses fixtures for that part). The Loki default index list in stage 3 is a constant in the test, so it has to be updated when the pinned Loki version changes.

## The graph and the fused changes read

The fused `changes` read is scoped by id. `Extras.Changes` takes `targetIDs`, and `EventQuery` has `TargetIDs`: when it is set the graph store adds `AND e.targetId IN $ids` and the SQLite store `target_id IN (...)`, with the order and limit unchanged; an empty list means no id filter. `Core` has an `AppHints` callback (set in `hub.go`) that supplies the accepted discovery hints; the recorder links application membership from it, together with the workspace's applications, each time it saves a snapshot.

## Docs site

This site lives in `docs-site/` and is a standalone Docusaurus project:

```bash
cd docs-site
npm install
npm start    # http://localhost:3000, live reload
npm run build
```

It publishes to GitHub Pages automatically on a push to `main` that touches `docs-site/**` — see [Release process](./release-process.md).

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
CONTINUUM_SERVER=host:8443 CONTINUUM_CA_PIN=<pin> CONTINUUM_TOKEN=<token> \
  ./agent --kubeconfig ~/.kube/config --state-dir ./agent-id
```

## Tests

```bash
cd backend && go vet ./... && go test -race ./...
```

Neo4j-backed tests skip themselves unless `CONTINUUM_TEST_NEO4J` is set — you don't need a Neo4j instance running to get a meaningful pass. The frontend has its own unit test suite (`npm run test:unit`) and lint (`npm run lint`); both run in CI on every push and pull request.

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

backend/               Go module "continuum" (Go 1.25)
  proto/               gRPC contract (Enrollment, AgentService)
  cmd/server           Control plane: CA, enrollment, sync stream, admin JSON API, serves the built UI
  cmd/continuum        The one binary the cluster image runs: continuum agent | probe | flow
  cmd/agent            In-cluster agent (a thin wrapper over the same code)
  cmd/probe            Optional node probe
  internal/            pki, store (SQLite), server, agent, probe, facts, interpret, model
internal/chart         The agent's Helm chart, embedded in the server, served as a .tgz to the wizard

deploy/helm/continuum-server/   The server's own Helm chart
docs-site/                       This documentation site (Docusaurus)
```

## Docs site

This site lives in `docs-site/` and is a standalone Docusaurus project:

```bash
cd docs-site
npm install
npm start    # http://localhost:3000, live reload
npm run build
```

It publishes to GitHub Pages automatically on a push to `main` that touches `docs-site/**` — see [Release process](./release-process.md).

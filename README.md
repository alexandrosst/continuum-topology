# Continuum Topology Studio

[![CI](https://github.com/alexandrosst/continuum-topology/actions/workflows/ci.yml/badge.svg)](https://github.com/alexandrosst/continuum-topology/actions/workflows/ci.yml)
[![Docs](https://github.com/alexandrosst/continuum-topology/actions/workflows/docs.yml/badge.svg)](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site)
[![Go](https://img.shields.io/badge/backend-Go%201.25-00ADD8?logo=go&logoColor=white)](backend/go.mod)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Model Kubernetes clusters across the cloud → edge → far-edge continuum and see them through two planes — an **application view** (services and their dependencies, IoT devices, external endpoints) and an **infrastructure view** (nodes, capacity, cross-cluster links) — both projections of one shared model, so editing a service in a table updates every view at once.

A Go server and an in-cluster Go agent discover the estate for you: nothing ever calls out from a cluster, the agent dials the server, and the server never holds a kubeconfig. On top of discovery sits history (what changed, when, and why), and a read-only **placement advisor** that scores every recommendation for confidence and shows the evidence behind it — through a pluggable `Decider` interface built for plugging in your own AI-driven placement policy, not just the two built-in ones (see *Where this could go next* below).

Stack: Vite · React 19 · TypeScript · Tailwind 4 · React Flow (`@xyflow/react`) · Zustand · React Router on the frontend, Go 1.25 on the backend. Styling follows the NetBird dashboard (dark neutrals, orange accent, sidebar + tables + modals).

## Documentation

**This README is a quick tour for building and hacking on the code.** Installing the server, connecting a cluster, the architecture, day-to-day use of the UI, and the full API/Helm/CLI reference all live on **the documentation site** — a Docusaurus project in [`docs-site/`](docs-site), published via GitHub Pages on every push to `main` (linked from the app's own header once deployed; run it locally with `cd docs-site && npm install && npm start`). Since a fresh clone may not have Pages live yet, the links below go straight to the Markdown sources, which read fine on GitHub too:

| Looking for... | Start here |
|---|---|
| Installing the server on Kubernetes, end to end | [Getting started → Quickstart](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/getting-started/quickstart.md) |
| Connecting a cluster, the approval code | [Installation → Connecting a cluster](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/installation/connecting-a-cluster.md) |
| How the server and agent fit together, the trust model | [Architecture → Overview](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/architecture/overview.md), [Agent trust model](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/architecture/agent-trust-model.md) |
| Using the UI day to day (discovery, placement, history, teams) | [User guide](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/user-guide/using-the-ui.md) |
| Building and running this repo locally, tests, where things live | [Contributing → Developer guide](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/contributing/developer-guide.md) |
| Helm values, CLI flags, and the external decider webhook contract | [Reference](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/reference/index.md) |

The short version, to see it running right now:

```bash
npm install && npm run dev   # frontend only, http://localhost:5173 — state stays in the browser, no server needed
```

```bash
npm run build && cd backend && go build -o server ./cmd/server && \
  ./server --data-dir ./data --agent-address my-host.example.com:8443 --agent-listen 0.0.0.0:8443 --ui-dir ../dist
# first start prints a one-time password for "admin" on stderr; the Developer guide above has the rest,
# including running an agent locally without a cluster
```

Publishing your own images and chart, deploying with Helm, and the full detail on roles, consent, mesh detection, GeoIP, the node probe and the traffic observer all live on the pages above rather than being repeated here.

## Repository layout

```
src/                             React + TypeScript frontend (Vite) — see the Developer guide for the full breakdown
backend/                         Go module "continuum": the server, the in-cluster agent, the optional node probe
                                  and traffic observer, and the CLI. backend/docs/ has the deep, code-adjacent
                                  references: the effective-model API, the placement cost function's exact
                                  formulas, the decider webhook's OpenAPI contract, and the twin's design.
deploy/helm/continuum-server/    The server's own Helm chart
docs-site/                       This documentation site (Docusaurus)
```

## Status and roadmap

Done: the trust foundation (sign-in, organizations and roles, consent that lives with the cluster's owner, approval codes, an audit trail); discovery and the digital twin (agent, node probe, traffic observer, history, path measurements); and uncertainty-aware placement advice — a three-valued fit (fits / does not fit / can't tell), confidence from the weakest deciding fact, and the evidence and "what would change it" behind every answer (see [`backend/docs/advice.md`](backend/docs/advice.md)). In progress: a UX pass. Deliberately paused: acting on advice — deploying to clusters or moving anything — until asked for.

### Where this could go next

The **pluggable `Decider` interface is the one deliberate gap worth calling out**: it's a real, working plug point today — any HTTP service speaking the schema formalised in [`backend/docs/decider-webhook.openapi.yaml`](backend/docs/decider-webhook.openapi.yaml) (OpenAPI 3.1, optionally HMAC-signed request for request) is compared fairly against the built-in "Baseline" and "Follow the heaviest talker" on the *Deciders* tab — but nothing dynamic or learned has been plugged into it yet. It's infrastructure for AI-driven placement, not an example of it. A few things would make it a better testbed for that specifically, roughly in the order they unlock each other:

1. **A reference learned decider**, shipped as its own small service speaking the same schema: something that learns from the history already being recorded rather than only the instantaneous request — a contextual bandit over move outcomes, or an online model re-scored as new measurements arrive, fits the existing confidence-class vocabulary naturally.
2. **A feedback loop closing the read-only gap.** Nothing today records whether a person followed a recommendation or what happened after, so there's no way to tell a good decider from a lucky one. Even before "acting on advice" is unpaused, a lightweight "I did this" log would let a decider be scored against its own past predictions.
3. **Offline replay for evaluation.** History already keeps thinned snapshots; replaying a sequence of them through several deciders and reporting the counterfactual cost each would have produced turns every recorded estate into a benchmark, no cluster required.
4. **Acting on advice, narrowly first.** Applying a single already-computed recommendation as a Kubernetes-native hint (a `nodeAffinity`/`topologySpreadConstraint` patch, or cordoning a node before a planned move) behind an explicit, audited, one-recommendation-at-a-time approval — keeping "nothing moves without a person's say-so" intact.
5. **A pluggable cost function, not just its weights.** Letting a decider supply its own scoring function (still checked against the same hard constraints) opens the door to objectives research increasingly cares about for edge/continuum placement — energy or carbon per site, tail latency instead of mean, fairness across tenants.
6. **A synthetic topology generator**, so an experiment is reproducible and shareable without hardware access — and a natural export/import point to and from established continuum simulators (iFogSim, EdgeCloudSim, PureEdgeSim).
7. **Failure and churn injection** against a synthetic or sandbox topology, turning the project's existing honesty about degraded states into a resilience test suite.

None of the above changes the read-only, nothing-moves-without-approval posture the project has held throughout; each is additive to the existing `Decider` contract, history store or advice engine, not a redesign of them.

## What's verified, and what isn't

Discovery and advice only — nothing is deployed to clusters and nothing is ever moved (tiers 3–4 are reserved and refused). Testing has run against real code paths wherever possible (Go tests under `-race`, a sandboxed single-node k3s API for server/agent integration, and — for the traffic observer specifically — real eBPF programs against real loopback traffic), but **pods cannot run in this project's own test sandbox**, so a few things are honestly out of reach here:

- **Never run against a real production cluster or a real DaemonSet.** The node probe and traffic observer were verified as standalone binaries and by tests, not as pods on real nodes; a second real cluster (cross-cluster resolution is covered by tests that feed two clusters' facts, not two live clusters); real Istio/Linkerd/Cilium proxies (mesh *detection* is tested against the Kubernetes objects a mesh creates, never against a running proxy); and Pod Security enforcement.
- **Platform gaps:** arm64 is built but never executed; IPv6 code paths exist but the sandbox has no IPv6; other kernels/distributions for the eBPF collector (needs Linux 5.9+ with BTF; older kernels fall back to conntrack, which is exercised); kube-proxy setups.
- **Scale and network realism:** measurements are TCP connect times over the sandbox's local addresses only — real WAN round trips, ICMP-only networks and hosts that filter TCP are untested; a picture of 60,000 workloads was exercised in tests, not on a cluster of that size.
- **Not built:** OIDC/SSO (local accounts only), ambient-mesh traffic attribution (HBONE hides the workload-to-workload hop from the observer), Cilium mesh detection, Consul/Kuma control-plane recognition (their sidecars are recognised, not their control planes), authorization-policy and `DestinationRule`/`Server` TLS settings (only `PeerAuthentication` is read), a Neo4j *import* mode, and a hard multi-tenant boundary in Neo4j Community (tenants are separated by query, not by database — run one Neo4j per tenant, or Enterprise composite databases, for that).
- **Read with appropriate skepticism:** the placement engine's weights are a starting point, not a calibrated model — with little measured traffic, advice says so ("mostly guessed") rather than pretending otherwise. A past view (History) shows what agents discovered back then with today's names, sites and policy, since only discovery is recorded.

```bash
cd backend && go vet ./... && go test -race ./...   # frontend: npm run test:unit && npm run lint
```

## Data and attribution

The footer text in the app (`OWNER` in `src/components/ui/brand.tsx`) reads "© \<year\> Continuum Topology Studio. All rights reserved." — replace it with the name of the actual rights holder. That string is just UI copy; the code itself is licensed under Apache 2.0 regardless of what it says.

- City names and coordinates: [GeoNames](https://www.geonames.org/) `cities15000`, CC BY 4.0. Rebuild with `scripts/build-geodata.py`.
- Country outlines: Natural Earth via `world-atlas` (public domain).
- IP geolocation (optional, supplied by you): [DB-IP.com](https://db-ip.com) (CC BY 4.0) or MaxMind GeoLite2, under its own license.

## Continuous integration and releases

Three workflows: [`ci.yml`](.github/workflows/ci.yml) (build, vet, test and lint on every push and PR, plus two dependency scans), [`release.yml`](.github/workflows/release.yml) (publishes images and Helm charts to this repo's own GHCR namespace on a push to `main` or a version tag), and [`docs.yml`](.github/workflows/docs.yml) (publishes the docs site to GitHub Pages). The one-time setup each needs, and exactly what gets published where, is in the docs site's [Contributing → Release process](https://github.com/alexandrosst/continuum-topology/blob/main/docs-site/docs/contributing/release-process.md) page.

## License

Apache License 2.0 - see [LICENSE](LICENSE). Backend (Go) and frontend (npm) dependencies were checked for license compatibility; nothing under a copyleft license that would affect this project's own licensing terms was found, with one deliberate exception documented in [`deploy/README.md`](deploy/README.md#neo4j): the *optional*, separately-run bundled Neo4j Community Edition is GPLv3, which is why the server talks to it only over HTTP as a separate process rather than linking against it, and why `neo4j.mode: external` exists for anyone who would rather not run GPL software in their deployment at all.

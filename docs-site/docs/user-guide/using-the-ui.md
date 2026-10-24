---
id: using-the-ui
title: Using the interface
description: "A tour of every page of the interface once your server is running: the topology views, Agents, Proposals, Pipeline, FUSION, Placement, History and Team."
---

# Using the interface

This assumes you have a server running and at least one cluster connected (see [Connecting a cluster](../installation/connecting-a-cluster.md) if not). It's a tour, not an exhaustive reference — each section below links to a deeper page where one exists.

## Two views of one model

Everything you see is a projection of a single shared model, kept live by your connected agents:

- **Application view** — your services, grouped by the cluster they run in, with dependencies drawn as edges between them. IoT devices sit in a row per site below the clusters they connect to; anything a service calls that isn't part of your own estate shows up as an external endpoint.
- **Infrastructure view** — the nodes underneath (VMs, bare metal, edge devices), grouped by cluster, with an optional overlay of which services are scheduled on which node.

Edit something in one place — rename a service, correct a cluster's tier — and it updates everywhere, because there's only one underlying document.

**Platform layer.** **Options → Show → Platform** (off by default; `platform=1` in the URL) adds the telemetry path to the picture: each cluster's agent and local operator, the Regional operators, the Central operator and FUSION. They are joined by dashed telemetry edges, each labelled with the age of the last data and a state glyph. Clicking a platform node opens the Inspector with its state, what it sends to, and an **Open in Pipeline** link. While the layer is on, the **Filter** menu has **Problems only**, which narrows the picture to what has a problem.

**Reading the canvas.** Lines are drawn at right angles, routed around cards rather than through them; lines that would run together are spaced into separate lanes, and a line leaves and arrives square to the box it joins. The arrowhead is an open chevron pointing at what is called. A dotted line is a dependency declared but not seen in traffic, a dashed one is inferred from connection tracking, and a solid one was measured; the busier a measured line, the faster its dashes run.

- **Click** a service, node or cluster to focus it: everything else fades outward from the click, leaving the thing itself, its lines, the entities they reach and the boxes those sit in. Click empty canvas to clear it. When you open something from search, the view goes to it and its card glows briefly.
- **Hover** a line to see its protocol, throughput and latency; the line thickens and the two things it joins are ringed.
- **Zoom** runs from 20% to 150%, and panning stops a little way past the graph, so the structure is never lost off screen. Zoomed far out, boxes keep only their names and any warning; the detail returns as you zoom in.
- **Cards** show a strip of small cells for a service's replicas (green ready, amber not ready) and, with per-pod facts collected, the pod rail. Unhealthy cards carry a faint glow and an offline status dot pulses slowly.
- The **legend** is a small chip at the bottom left (your choice is remembered), and the **minimap** only appears when part of the graph is off screen. **⋯** holds Reset layout and Export PNG.

## Agents

**Agents** (under *Manage*) is where every new cluster starts and where you look after the ones you have. It has three tabs: **Agents**, **Suggestions** and **Seen and gone**. The **Connect a cluster** button opens the wizard (see [Connecting a cluster](../installation/connecting-a-cluster.md)); the first step of the page's checklist does the same.

The **Agents** tab lists every connected cluster's agent: whether it's currently connected, its last heartbeat, certificate expiry, and — expand a row to open its **Agent insight panel** — exactly what it's allowed to see versus what it's actually sending right now (the ceiling / approved / effective tiers from [System overview](../architecture/overview.md); together these three settings are what the architecture docs call an agent's *observability intent*), plus any self-reported problems and their fixes. This is also where an administrator narrows what an agent shares (pausing an optional collector, excluding more namespaces) or requests it be widened — widening always needs a `helm upgrade` on the cluster's own side; the page shows you the exact command.

The **Suggestions** tab is where the server surfaces things it noticed but did not decide on its own: a newly discovered application grouping suggested from the traffic it has seen, or a *suspicion* (traffic from an onboarded cluster toward an address nothing has claimed, on a port only Kubernetes itself tends to use), which usually means there is another cluster worth connecting. Accept, correct or dismiss each one; nothing is applied automatically. The older `/discovery` address leads here. **Seen and gone** lists agents that have left, so a cluster that disappeared is a record and not a mystery.

**Set up telemetry** (here and on the Pipeline page) starts the [telemetry wizard](#setting-up-telemetry). For an agent that is already set up, the **Change telemetry** disclosure in its details is the same wizard, starting from what is installed now.

## Pipeline

**Telemetry → Pipeline** (`/pipeline`) is one page for the whole path telemetry takes: agents, local operators, regional operators, the central operator and [FUSION](#fusion). (The older `/operators` address redirects here.)

- **What needs attention** comes first and appears only when something is wrong. Each item has a **What to do** button that opens numbered steps, with the commands to run.
- Five tiles, one per hop, show a count and when that hop last sent data. Selecting a tile filters the table below it.
- One table lists every component, with its state (**Healthy**, **Needs attention**, **Not working** or **Unknown**), its certificate (**Renewing automatically** unless renewal fails), **Sends to**, **Last data**, and the same row menu on every row.

**Set up telemetry** and **New operator** are the two primary buttons. How operators are created, reached and renewed is in [Regional operators](../architecture/regional-operators.md) and [Running regional operators](../architecture/regional-operators-lifecycle.md).

## Proposals

**Proposals** (under *Manage*) lists changes an *advisor* has suggested to what an agent shares. Each shows who proposed it, the evidence, what it predicts, and whether accepting it would widen what the agent shares. Nothing changes until a person accepts: editors can accept a narrowing, and widening takes an administrator. An accepted change is made the same way as one you make by hand and appears in **Who did what**. Later, the page shows how each advisor's predictions held up. How this works is in [Placement advice and the decision loop](../inside/placement-engine.md).

## System health

**System health** shows the server and each agent as they are right now. For history and comparison it points to the **Ikhnos self** dashboard in Grafana, which draws the server and every cluster's agent over time and marks when agents joined or left. That dashboard comes with FUSION.

## FUSION

**Telemetry → FUSION** (`/fusion`) is the built-in place to save what the central operator receives (see [Regional operators](../architecture/regional-operators.md#fusion-where-a-regional-operator-saves-what-it-receives)). It has four tabs, each its own address (`/fusion`, `/fusion/data`, `/fusion/access`, `/fusion/settings`):

- **Overview** shows what needs attention first, then the stores and the central operator.
- **Data** lists your applications and their services, and which signals arrived in the last 24 hours.
- **Access** is where API tokens are made, each with a scope and an expiry; the secret is shown once. It also has the API reference and a note on signing in to Grafana and Prometheus. See [The FUSION API](../architecture/fusion-api.md) and [Grafana and the web pages](../architecture/fusion-grafana.md).
- **Settings** sets retention for each store and shows whether it fits the volume at today's growth; shortening it asks **Delete older data?** first. It is also where FUSION is turned off. See [Running regional operators](../architecture/regional-operators-lifecycle.md#fusion-retention-and-volume-size-from-settings).

## Setting up telemetry

**Set up telemetry** is a guided wizard, the only way in. It asks **Where from** (the cluster), **What to collect**, and **Where to send**, one merged list of FUSION, the operators in this system, backends it detected, presets, or another OTLP endpoint; an entry that cannot take a signal you chose says why. **Review and install** then says what changes, what does not change and what happens if you stop, and shows the command, which you run yourself. **Check that data arrives** confirms it worked. You can start it from the Pipeline and Agents pages, from **Define scope from selection** in the topology, and from **Change telemetry** in an agent's details. The design is in [Telemetry intent](../architecture/telemetry-intent.md).

## Placement

The **Placement** page answers "where would this service be better off, and what would that change?" — it's read-only advice, and every recommendation shows its evidence: what the round-trip estimate is based on (measured, declared, or a distance-based guess), how confident the traffic-weight figure is, and what would have to be true for the recommendation to flip. Nothing is ever moved from here; a person applies a change deliberately, elsewhere. See [Mobility and placement](./mobility-and-placement.md) for how the cost function and confidence levels work.

## Namespaces and services

Every discovered namespace and the services inside it are browsable in their own right — which cluster they're in, how exposed they are, whether a service mesh covers them, and whether an agent was actually told to look at them (some namespaces are deliberately left out of an agent's scope; see [Namespaces and services](./namespaces-and-services.md)).

## Approvals

Two different things use the word "approve" and it's worth keeping them straight: approving a **newly enrolling agent** (typing the approval code from its log, confirming it's the cluster you expect — see [Agent trust model](../architecture/agent-trust-model.md)) is a one-time identity check. Approving a **placement recommendation**, by contrast, isn't a single button — it's whatever your own change-management process is; the Placement page's job ends at giving you a well-evidenced recommendation, not at applying it. See [Approvals in depth](./approvals-in-depth.md).

## Team and roles

Every topology belongs to one organization, and nothing crosses between organizations. Four roles, most to least: `owner`, `admin` (connect and approve clusters, manage tokens, invitations and members), `editor` (change the topology, apply decisions), `viewer` (read-only). Invite colleagues from **Members & access** — invitations are one-time links that expire after 7 days. Editors can narrow what an agent shares (lower its approved tier, pause a collector, exclude a namespace) but not widen it: raising a tier toward the install ceiling, lifting a pause or removing an exclusion needs an admin. Personal access tokens are created from a browser session; a bearer token cannot create another. Changing a password keeps the account's tokens; an offline password recovery (`reset-password`) revokes them.

Each person can also turn on two-factor authentication for their own account, from the account menu at the bottom of the sidebar: it adds a standard 6-digit code from an authenticator app (Google Authenticator, 1Password, Authy, or similar) to the password check at sign-in. Setup shows a secret key to enter into the app — no camera or QR code needed — and, once a code confirms it, a set of one-time recovery codes to keep somewhere safe in case the device with the app is ever lost. Turning it off again asks for the current password.

## History

If Neo4j is enabled (see [System overview](../architecture/overview.md)), the **History** page adds a time scrubber and an exact-moment picker: "view the estate as of an hour ago" is one click, with a clear banner reminding you that you're looking at the past and editing is disabled until you return to now. It also adds a per-entity **Timeline** — open any cluster, node or service to see what changed about it and why, event by event (see [System memory](../architecture/system-memory.md)). The audit entries in a Timeline are listed for administrators only; viewers and editors get the events without them. Historic views show the recorded topology; live readings such as a node's requested resources, pod counts, pressure, power and traffic counters appear in the live view only. Without Neo4j the same events are tracked in SQLite; you get the audit trail either way, just not the point-in-time views or the Timeline.

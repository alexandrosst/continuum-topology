# Continuum Topology Studio

Model Kubernetes clusters across the cloud → edge continuum and see them through different planes:

- **Application view** – microservices (services) grouped by the cluster they run in, with dependencies as edges. IoT **devices** (sensors, cameras, PLCs, actuators) sit in a row per site below the clusters and connect to services with the same kind of edge. External endpoints appear when something calls them.
- **Infrastructure view** – nodes (VMs / bare-metal / edge devices) grouped by cluster, optional overlay of the services scheduled on each node, and aggregated cross-cluster links.

Both planes are projections of one model, so editing a service in the table updates every view.

Stack: Vite · React 19 · TypeScript · Tailwind 4 · React Flow (`@xyflow/react`) · Zustand · React Router. Styling follows the NetBird dashboard (dark neutrals, orange accent, sidebar + tables + modals).

**This README is for building and hacking on the code.** For installing the server, connecting a cluster, the architecture, and day-to-day use of the UI, see **the documentation site** — published from [`docs-site/`](docs-site) via GitHub Pages, linked from the app's own header once deployed. Building it locally: `cd docs-site && npm install && npm start`.

## Run the frontend

```bash
npm install
npm run dev      # http://localhost:5173
npm run build    # type-check + production build
```

Without a server, state is kept in `localStorage` (key `continuum-topology/v1`). With a Continuum server it is saved to the server (see below). Use **Settings → Import / Export** for JSON backup, sample data, or a clean slate.

## Run the backend, and deploy on Kubernetes

Building and running the Go server and agent locally, and the full Helm chart reference, live in the docs site: **Getting started**, **Installation**, and **Architecture**. The short version: the server has two ports that need very different treatment (agents on `:8443`, mutual TLS, never behind a TLS-terminating proxy; the UI on `:8080`, plain HTTP meant to sit behind one), it's one replica with one PersistentVolumeClaim, and if this repository (or your fork) is on GitHub, [`.github/workflows/release.yml`](.github/workflows/release.yml) publishes zero-config images and charts to your own `ghcr.io/<owner>` namespace on every push to `main` or version tag — nothing to build or publish by hand. `scripts/publish.sh REGISTRY` remains for publishing to a registry other than GHCR, or a fork not wired up to Actions.

The chart passes `helm lint`, `helm template` and a server-side dry run against a k3s API; no pod has run from it in a real cluster, so read the first install as a trial (the status note at the top of [`deploy/README.md`](deploy/README.md) says the same — that file is now the deep operational reference; start with the docs site instead).

## Discovery backend (Phase 1)

*The rest of this file, from here down, is deep implementation reference: exactly how enrollment, consent, the optional collectors, history and placement work under the hood. If you want to install and use the product rather than build on it, the docs site covers all of this at a more approachable level — this section stays for contributors who need the full detail.*

A Go server and an in-cluster Go agent discover clusters for you. Nothing calls out from the cluster: the agent dials the server, and the server never holds a kubeconfig.

```
backend/          Go module "continuum" (Go 1.25)
  proto/          gRPC contract (Enrollment, AgentService); generated code in gen/
  cmd/server      control plane: CA, enrollment, sync stream, admin JSON API, serves the built UI
  cmd/continuum   the one binary the cluster image runs: `continuum agent | probe | flow`
  cmd/agent       in-cluster agent (thin wrapper over the same code)
  cmd/probe       optional node probe (one small read-only pod per node)
  internal/       pki, store (SQLite), server, agent (+ collect), probe, facts, interpret, model
internal/chart      the agent's Helm chart (tiered read-only RBAC), embedded in the server, which hands it out as a .tgz
```

### Run it

```bash
# 1. UI
npm run build

# 2. Server. --agent-address is what agents will dial; the admin UI/API stays on loopback by default.
cd backend && go build -o server ./cmd/server && go build -o agent ./cmd/agent
./server --data-dir ./data --agent-address my-host.example.com:8443 --agent-listen 0.0.0.0:8443 --ui-dir ../dist
# first start prints a one-time password for the user "admin" on stderr
```

Open http://127.0.0.1:8080. The UI served by the server asks you to sign in: use `admin` and the one-time password from the first start, then choose a new password (12 to 128 characters). On a loopback address (the default) anyone who can reach the page can create an account; on any other listener registration is by invitation only unless you choose otherwise (see *Organizations, roles and registration*); invite colleagues under **Members & access**. Then go to **Discovery → Connect a cluster**. The wizard prints a `helm install` command containing the chart, the server's CA pin and a one-time token (valid for one hour, one use). Run it against the cluster, after two preparations the wizard lists:

- **The chart.** The server carries the chart and serves it as `/charts/continuum-agent-<version>.tgz` (no sign-in needed; it holds no secrets). Download it from the wizard, or `curl -fLO http://<server>/charts/continuum-agent-<version>.tgz`, and put it in the folder you run the command from: the command says `./continuum-agent-<version>.tgz`. To install from somewhere else (an OCI registry, a chart repository, an https URL), start the server with `--chart-ref <reference>` and the command uses that instead.
- **The image and the chart: publish once, then the printed command is the whole install.** `scripts/publish.sh` builds ONE image, `continuum` (the agent, the node probe and the flow collector are roles of the same binary, chosen by the pod's `args`), for x86 and ARM (`docker buildx`, cross-compiled, no emulation) and pushes it with the chart to one registry namespace, then prints the image's `sha256:` digest and the `--set image.digest=…` that pins an install to it: `docker login`, then `scripts/publish.sh myname` (a Docker Hub namespace, or `ghcr.io/me`; there is no default registry; tag = the chart's `appVersion`, `scripts/publish.sh myname 0.2.0` for another; `DRY_RUN=1` to see the commands; `ONLY=image|chart` for one piece). Keep the repositories public so a cluster and helm can pull without credentials (private: `--set 'imagePullSecrets[0].name=<secret>'` for images, `helm registry login` for the chart). There is **no built-in registry**: you say where you published, either per organisation in the UI (**Settings → Installation**: registry, tag and digest, saved and audited, administrators only) or server-wide with `--image-registry`, `--image-tag` and `--image-digest` (env `CONTINUUM_IMAGE_REGISTRY`, `CONTINUUM_IMAGE_TAG`, `CONTINUUM_IMAGE_DIGEST`; the digest is `sha256:` and 64 hex digits). An organisation's own setting wins over the flags, as a whole (a registry saved in the UI is never combined with a flag's tag); with neither, the command uses the chart's own image name and the chart file this server serves, and the wizard says so. Once a registry is set the server prints `helm install continuum-agent oci://registry-1.docker.io/<namespace>/continuum-agent --version <chart version> … --set image.repository=<namespace>/continuum` (plus `--set image.tag=…` and `--set image.digest=…` when set), with nothing to download or build on the cluster's side. A tag is mutable, so pin a digest for reproducible installs; with a digest the chart pulls `repository@digest` and ignores the tag. The chart lives next to the images (a bare name is a Docker Hub namespace; `ghcr.io/me/x` or `reg.example.com:8443/team` work as they are). `--image-tag` and `--image-digest` (above) set the image version; `--chart-ref` names a chart elsewhere; `--chart-ref local` prints the file this server serves (`./continuum-agent-<version>.tgz`, with a download link in the wizard) for a cluster machine that cannot reach your registry. The optional node probe and flow collector run that same image, so there is nothing else to publish. Verified: the script's chart push and the printed command's pull and render against a local OCI registry; not verified: a real Docker Hub push, image pulls by a real cluster (pods cannot run in the sandbox).

The `--agent-address` you start the server with must be reachable *from the cluster*, not just from your laptop. When the agent appears, type the approval code from its log (next section), choose an access level and approve. Nodes, namespaces and workloads then flow into the topology, and application groupings arrive as suggestions in the inbox.

For local development an agent can also run outside a cluster: `CONTINUUM_SERVER=host:8443 CONTINUUM_CA_PIN=<pin> CONTINUUM_TOKEN=<token> ./agent --kubeconfig ~/.kube/config --state-dir ./agent-id`.

Forgotten the administrator password? Stop the server and run `./server reset-password --data-dir ./data admin`; it prints a new one-time password and ends that user's sessions. `CONTINUUM_ADMIN_PASSWORD` can seed the first administrator instead of a random one.

### Enrolling an agent and approval codes

A token proves that someone was allowed to install an agent. It does not prove that the agent that arrived is the one you meant: with two clusters waiting, or a token that leaked, the wrong request could be approved. The approval code closes that gap.

- **The code.** On first contact the agent makes a random 8-character code (Crockford base32, shown as `K7QM-4TXD`, 40 bits) and prints it in its own log, leading the line so it's easy to spot: `APPROVAL CODE: K7QM-4TXD (enter it in Continuum to approve this cluster)`. It sends the server only a hash (Argon2id, bound to the agent's public key), so the server never learns the code and cannot show it. Read it where only a person with access to the cluster can: `kubectl -n continuum-system logs deploy/continuum-agent`. The administrator types it into the approve dialog (case, dashes and spaces do not matter; pasting the whole log line works).
- **Wrong codes.** The server compares in constant time and counts every attempt before comparing. The fifth wrong code rejects the request for good and the agent stops; install again with a new token. A malformed code (wrong length or characters) is refused without costing an attempt. Successes and failures are in the audit trail (the code itself never is), and the dialog shows the attempts left.
- **Agents without a code (legacy).** An agent from before this change sends no hash. The server still lets you approve it, the old way (type the start of the cluster's fingerprint, `kubectl get ns kube-system -o jsonpath='{.metadata.uid}'`), and marks it `legacyEnrollment` in the UI and the audit trail, because that check is weaker: the fingerprint is not secret. Start the server with `--refuse-legacy-approval` to refuse such agents outright once yours are updated.
- **Bind a token to a cluster (optional).** `POST /api/v1/orgs/{org}/tokens` accepts `expectedCluster`, the UID of the cluster's `kube-system` namespace. An agent that reports another UID with that token is refused at enrollment. The Agents page has no token form, so this is API-only for now.
- **Restarts are safe.** The agent saves its key and code before it calls the server. If the pod restarts while waiting, it enrolls again with the same token and the same key, the server continues the same pending request, and the log prints the same code. A different key with a token that was already used is refused.
- **Waiting has a limit.** A pending request lives 24 hours (`--pending-enrollment-ttl`). After that it is marked `expired`, an event is recorded, and the still-running agent enrolls again on its own with a new code, if the token is still valid. Expired rows are purged after 7 days. The agent polls at the interval the server asks for (clamped to 2-60 seconds, with jitter) and backs off exponentially, up to 2 minutes, on errors.
- **Revocation is permanent.** A revoked agent does not retry with its old identity, since it can never succeed. `agent.Run` returns `ErrRevoked` and the process exits with code 3, so the pod's restart count and `kubectl get pod` show it. A restarted pod holds 5 to 10 minutes (random) before exiting, so the restart loop backs off to a few contacts an hour, and logs the reason at most once per 10 minutes: "permanent; run `helm upgrade` with a new token". A new token clears the marker and enrolls fresh.
- **Clock skew.** The server sends its time in its enrollment, poll and config replies. The agent works out the difference, warns in its log (at most hourly) when it exceeds 2 minutes, and reports it in each heartbeat. The Agents page then shows a "clock 3 min off" chip: certificates are checked against the clock, so a wrong clock looks like a certificate error.

Not verified: a real pod restart or a real cluster's log (the e2e tests run the agent in-process against a fake API and read the code from the agent's log and identity store).

### What an agent may see, and consent

Consent lives with the owner of the cluster. Three tiers, always in this order: **installed** (the ceiling: the chart's `access.tier`, its RBAC and its scope, which only `helm upgrade` can move) >= **approved** (what an administrator agreed to receive here) >= **effective** (what the agent collects now, reported by the agent itself). This server can never exceed the ceiling, and the agent checks that for itself.

- **What the server can push** (editors and administrators, audited old to new): a lower approved tier (or back up, to at most the ceiling), pausing an optional collector (`probes`, `flow`, `measure`: it really stops and forgets what it held), and leaving more namespaces out (add-only, dropped in the agent before anything is sent; the watches themselves stay cluster-wide, so this is filtering at emit time). All of it is kept per agent and survives restarts. It can only reduce what is shared: an override that would widen anything is ignored by the agent and shown as `override_ignored`.
- **How to widen.** Ask the cluster's owner to run `helm upgrade continuum-agent <chart> --namespace continuum-system --reuse-values --set access.tier=2` in that cluster (the Agents page shows the exact command, with your chart reference), then select the tier here. A request above the ceiling is refused by the API (`POST /api/v1/orgs/{org}/agents/{id}/tier`, HTTP 400) with that command in the `helm` field.
- **The Agents page** shows for each approved agent (to editors and above) a health line ("Healthy" or "N problems"), *What this agent can see* (installed, approved and effective tier, namespaces in view as counts, the optional collectors and whether they deliver, how many watches were read), the problems with their fix, and the *Consent* panel (tier, pause toggles, namespaces to leave out, with a note that this can only reduce what is shared).
- **Self-diagnosis.** The agent reports its version, tiers, scope as counts, collectors, per-watch status and typed problems in its Hello and (when changed, and at least every 5 minutes) in heartbeats. It is honest about limits: an agent that cannot read something says so and still sends the rest, and it holds its picture back until every watch has read its kind or been refused. Problem codes, what each means and the fix: [`backend/internal/agent/DIAGNOSTICS.md`](backend/internal/agent/DIAGNOSTICS.md).

| Code | Severity | Meaning | Fix |
|---|---|---|---|
| `rbac_forbidden` | error / warn | The cluster refused the agent a `list` or `watch` (named in the message); it reports none of that | Restore the chart's RBAC with `helm upgrade --reuse-values` |
| `informer_not_synced` | error / warn | A watch never finished, or keeps failing; the picture is held back | Check the pod can reach the Kubernetes API |
| `sync_too_large` | error | The server refused a picture over its limits | Narrow `scope.*`, or raise the server's limits |
| `server_limits_refused` | error | The server refused another message | Read the message; check the server log |
| `clock_skew` | warn | The clock is more than 2 minutes off the server's | Fix NTP on the node |
| `collector_silent` | warn | An optional collector is on but silent for more than 3 of its intervals | Check the DaemonSet and NetworkPolicy |
| `scope_empty` | warn | The scope matches no namespace | Change `scope.*` |
| `identity_secret_unwritable` | error | The agent cannot save its renewed certificate | Restore the Role on its Secret |
| `override_ignored` | info | The server pushed something that would widen access or is invalid; ignored | Nothing on the agent; only `helm upgrade` widens |
| `flow_dropped` | warn | Observed traffic was dropped while disconnected (bounded at 20 000 edges) | Usually a long outage |
| `internal_error` | error | An agent task panicked and was restarted | Read the agent's log and report it |

- **Large clusters.** A picture over about 1 MiB is sent in chunks (`chunk_index`, `chunk_total`, `sync_id`); the server bounds what it buffers by its limits, checks the assembled whole, and applies it atomically on the last chunk. A connection that drops mid-picture leaves the previous state untouched.
- **Operations.** `/healthz` and `/readyz` (database and agent listener; a configured but unreachable Neo4j is "ready, degraded"), Prometheus metrics with `--metrics-listen` (default off; agents by status, syncs applied and refused, auth failures, rate limits, recovered panics, store errors), recovery of panics in gRPC handlers, and a shutdown that waits five seconds for agent streams instead of forever.

Not verified: the consent panel and health block were checked in a browser against real agents on a k3s API (Playwright), but not against a real DaemonSet (so the node probe and flow collectors were "on and silent", never reporting), nor a cluster where the RBAC was actually reduced by an administrator (403s were produced with a fake API in the Go tests). A picture of 60,000 workloads was sent through a real agent and a real server in a test, not against a cluster of that size.

### Sign-in and the shared workspace

- Local accounts with argon2id password hashes. Sessions live on the server in an HttpOnly, SameSite=Strict cookie and end after 8 hours idle or 7 days in total, and immediately when the password changes or the account is disabled. Logins are rate limited, and state-changing requests need a custom header plus an allowed origin (CSRF).
- The topology is one **workspace document per organization on the server**. Your decisions, manual overrides, applications and sites are saved there automatically and shared by everyone who signs in. Saves carry the revision you last saw: if someone else saved first, you are told and choose *Load theirs* or *Keep mine*. Agent status is refreshed from the server and is not part of the saved document.
- Without a server the app still works on its own and keeps its data in the browser (Settings → Import / Export for backup).

### Organizations, roles and registration

Every topology belongs to an **organization** and nothing crosses between organizations: not the workspace, agents, tokens, history, events, measurements, the audit trail or the graph. A person has one account and can be a member of several organizations; the account menu switches between them. A user who is not a member of an organization gets *404*, exactly as if it did not exist.

- **Roles per organization:** `owner` (everything, including renaming, deleting and changing owners), `admin` (connect and approve clusters, tokens, invitations, members, settings, the audit trail), `editor` (change the topology and apply decisions) and `viewer` (read only).
- **Registration** (`--registration open|invite|closed`, env `CONTINUUM_REGISTRATION`): `open` lets anyone who can reach the admin address create an account and an organization of their own. That is right for a private network and wrong on an exposed one; the server warns at start when it is open on a non-loopback address. Default: `open` while `--admin-listen` is a loopback address (local use), `invite` for any other listener; an explicit flag or environment value always wins, and the start-up log says which default was applied and why. `invite` accepts only a person holding an invitation link, and `closed` accepts nobody: provision accounts with `./server create-org --data-dir ./data --owner USERNAME "Org name"` (and `reset-password`).
- **Invitations** are one-time links with a role that expire after 7 days; the secret is shown once, when it is made, and only its hash is kept.
- **Agents belong to one organization.** The install token that enrolls an agent names the organization that made it, and the certificate the server issues carries that identity, so an agent can only ever report into its own organization.

### Where the memory is kept (Neo4j, optional)

By default history, events and the audit trail live in SQLite next to the server. Point the server at a Neo4j 5 database and they move into a **temporal graph** instead:

```bash
CONTINUUM_NEO4J_PASSWORD=... ./server --neo4j-url http://neo4j-host:7474 --neo4j-user neo4j [--neo4j-database neo4j] ...
# or --neo4j-password-file / CONTINUUM_NEO4J_PASSWORD_FILE; the password is never taken from a command-line flag
```

- **What is stored.** Every cluster, node, namespace, service, external endpoint, dependency and measured path is an entity with *versions* (`validFrom`/`validTo`) and time-stamped relationships (`IN_CLUSTER`, `RUNS_ON`, `CALLS`, `PATH_FROM/TO`). A snapshot node records which versions were current, so the estate **at any recorded second** is one query away (History → *Or type an exact moment*), and the inspector shows a thing's **own history** (what changed, when, and the people or events around it). Volatile counters (bytes, connection counts, round-trip times) live on the snapshot, so a busy link does not create a new version every minute.
- **Accountability.** Every action (sign-ins included; failed sign-ins belong to no organization and go to the server's own trail, which no tenant can read) is written to the audit trail with who did it, and projected into the graph as `Audit -[:BY]-> Actor`. **Who did what** (admins) searches it; each save of the shared workspace is a numbered revision (the last 500 are kept), and memberships are versioned so *who could see what, when* is answerable.
- **What stays in SQLite, on purpose.** Password hashes, sessions, invitation and enrollment token hashes, the CA and its keys, and the authoritative list of organizations and memberships. These need atomic single-writer semantics and must keep working when the graph is down. Tenants and memberships are *projected* into Neo4j (every minute) so the graph can answer questions about them, but SQLite decides.
- **If Neo4j is down**, the server keeps working: history and events are written to SQLite and drained into the graph, in order, when it comes back (every 5 seconds). The History page shows the state ("buffering"). Existing history in SQLite is migrated into the graph the same way and then removed from SQLite.
- **Isolation.** Neo4j Community has one database, so tenants are separated by the queries, not by the database: every tenant statement goes through one function that refuses to run without the tenant id and takes it from the authenticated request, never from the payload. A test counts the few statements allowed to run without it and fails when a new one appears. If you need a hard boundary, run one Neo4j per tenant or use Enterprise composite databases.
- **Retention** follows the same policy as before (snapshots thinned, then versions closed before the oldest remaining snapshot are deleted).

### GeoIP (optional)

The server can suggest where an agent is, using the public address it connects from (`connectingIp`). It is a suggestion for a person to confirm, computed **offline** from a database file you supply; no address is ever sent to a third party.

- Get a free MaxMind-format `.mmdb`: **DB-IP Lite** (country or city, CC BY 4.0, attribution required) from https://db-ip.com/db/lite.php, or **MaxMind GeoLite2** (free account, accept their license terms).
- Start the server with `--geoip-db /path/to/dbip-city-lite.mmdb` (or `CONTINUUM_GEOIP_DB`). If the file cannot be read the server refuses to start. `/api/v1/info` then reports `geoip` (with the attribution line to show), and each agent in `/api/v1/state` gets `connectingGeo`.
- A cluster in a cloud resolves to its provider's egress point, not to the datacenter it runs in, and private or carrier-grade NAT addresses have no location at all. Keep the region tables and human confirmation in the loop.
- A country-level database gives `level: "country"` and no coordinates, so there is no dot on the map; a city database adds city, region and lat/lng.

### Node probe (optional)

The Kubernetes API cannot say whether a node is a VM or a physical server, so without help Continuum guesses and shows the guess as low confidence. `--set nodeProbe.enabled=true` (or the checkbox in *Connect a cluster*) adds a DaemonSet of tiny read-only pods that read, from the machine itself: the CPU's hypervisor flag (`/proc/cpuinfo`), the firmware vendor and product name (`/sys/class/dmi/id`), the hypervisor type, the device-tree model of ARM boards (Raspberry Pi, Jetson, …), which kinds of physical network interface are up (Ethernet, Wi-Fi, cellular), and whether a battery exists. Run `probe --print` to see exactly that. It never reads serial numbers, MAC addresses, machine or product UUIDs, disks or processes.

- Interpretation happens on the server, so the rules can change without redeploying anything: a set hypervisor flag means VM (the firmware string names the platform: "Amazon EC2", "KVM/QEMU", "VMware"…); no flag and no hypervisor named in firmware means bare metal; AWS `.metal` instances are recognised; ARM has no hypervisor flag, so it relies on the firmware or device tree; a device-tree board with at most 32 GB of memory, or a laptop or mini-PC chassis or a machine with a battery, is an edge device. Every conclusion carries its evidence ("node probe: CPU reports a hypervisor", high), and a person's override always wins. If the probe cannot decide (an ARM machine with neither firmware tables nor a device tree), the API-based heuristics still apply.
- Security: the probe pod runs as non-root with all capabilities dropped, a read-only root filesystem, no host network or PID, no Kubernetes API access (no service account token) and no privileged mode. It reports only to the agent, over plain HTTP inside the cluster, signed with HMAC-SHA256 (node name, timestamp, body) using a random secret the chart generates and keeps across upgrades; reports older than five minutes, over 16 KB, with an invalid node name or with a wrong signature are refused. The secret is shared by all probes, so whoever can read it (or takes over one node's probe) can report false hardware facts for any node; that is why they are shown as evidence with a confidence, never as certainty, and why a person's override wins. The agent's API permissions do not change: the secret is mounted as a file, not read through the API. Everything received is length-bounded, stripped of control characters and restricted to known values, and facts about a node the cluster does not have are dropped. An optional NetworkPolicy (on by default) lets only the probe pods reach the agent's port.
- Cost and trade-offs: about 12 MiB and a few milliseconds of CPU per node; the only pieces on every node. The probe mounts the host's `/sys` read-only (a `hostPath`), which namespaces enforcing the Pod Security *baseline* profile refuse: label the namespace `privileged`, or set `nodeProbe.hostSys=false` to run without it and still tell VMs from bare metal (from DMI and CPU flags) but not see device trees or uplinks. Build the image with `docker build -f backend/Dockerfile --target probe`.
- The probe reports every three minutes; the agent keeps the latest report per node in memory only, so after an agent restart nodes fall back to guesses for up to three minutes.

### Traffic observer (optional)

Kubernetes does not record who calls whom, so without help every dependency is declared by a person or guessed. `--set flowObserver.enabled=true` (or the checkbox in *Connect a cluster*, available from access tier 2) adds a DaemonSet that counts connections between workloads and shows them as **observed** links: solid edges whose width follows the traffic, against dotted edges that are declared but never seen. It never reads packets or contents; it keeps only who connected to whom, on which port and protocol, how many connections and how many bytes.

- **Two collection methods, chosen per node.** eBPF (`tp_btf/inet_sock_set_state`, CO-RE, kernel 5.9 or newer with BTF) is the primary: it knows the client and the server of every TCP connection, counts a connection when it is established, and the bytes acknowledged and received. With `flowObserver.liveBytes=true` (the default) it also snapshots open connections every window, so a connection that stays open for days keeps reporting bytes instead of only at close (this needs `hostPID`, because the kernel iterators walk the caller's PID namespace). Where eBPF is not available the collector falls back to the kernel's connection-tracking table (`/proc/net/nf_conntrack`; TCP and UDP; bytes only where `nf_conntrack_acct` is on). UDP is observed through the same table next to eBPF when it is readable (the collector's `--udp auto|on|off`, default `auto`; the chart does not expose it), because a UDP "connection" only exists in conntrack.
- **Attribution in the agent, resolution on the server.** A collector sends what it saw, signed with HMAC-SHA256 with a secret of its own (`continuum-agent-flows`, separate from the node probe's), to the agent's receiver. The agent maps pod addresses, Service cluster IPs, node ports and node addresses to workloads using what it already reads at tier 2 and forwards only aggregated edges (a window of 60 s). The server matches the addresses that stay unknown to the ones every other onboarded cluster reports, which is what turns a call to another cluster's node port or load balancer into a cross-cluster link. Observed links are computed on every state request and overlaid on the workspace; they are never stored in it, so removing the observer removes them after 24 hours without a trace.
- **What is shown.** Solid edges for what was seen (width by the logarithm of the traffic, dimmed when stale), dotted for declared and not seen, animated dashes across clusters. Traffic to things the model does not own becomes an *external endpoint* suggestion in the inbox; naming one stores a manual record and later observations attach to it. DNS (port 53) and system traffic (NTP, mDNS, SSDP, anything in `kube-system`) is flagged as noise and hidden unless you turn on *DNS & system traffic* under *Options*. The Discovery page shows, per agent, which method runs on how many nodes, whether bytes are counted, how many observations were dropped and when the last report arrived.
- **Security.** The collector needs privileges: root with `CAP_BPF`, `CAP_PERFMON` and `CAP_SYS_RESOURCE`, an unconfined seccomp profile (the `bpf` syscall), the host network (for conntrack), and `hostPID` with live bytes. It has no Kubernetes API access and no service account token. A namespace enforcing the Pod Security *baseline* profile refuses it: label the namespace `privileged`, or leave the observer off. The optional NetworkPolicy lets only the collector's node addresses reach the agent's port (`ipBlock`, because host-network pods have no pod labels). Reports older than five minutes, over the size limit, with a bad signature or naming a node the cluster does not have are refused. The shared secret means a compromised node's collector can fabricate traffic for any workload; observed links carry their method and are one input among several, and a person's decision wins.
- **Try it without a cluster.** `backend/cmd/flow` runs anywhere Linux does: `sudo ./flow --print 30s` observes for 30 seconds and prints exactly what a report would contain, sending nothing. Build the image with `docker build -f backend/Dockerfile --target continuum`; the role is its first argument (`… IMAGE flow --print 30s`, as root: the image's default user is unprivileged, so add `--user 0`, and the eBPF method needs the capabilities the chart grants; unverified here, no Docker daemon).
- **Regenerating the eBPF objects.** The compiled objects (amd64 and arm64) are committed. To change `backend/internal/flow/ebpf/flow.c` run `go generate ./internal/flow/ebpf` (needs clang, libbpf headers are vendored under `headers/`).

### Which namespaces the agent reports (scope, optional)

By default the agent reports every namespace, and that is what lets Continuum discover your applications by itself; nothing changes unless you narrow it. Some namespaces should never leave the cluster (HR data, a tenant's workloads), so the scope is a rule the agent applies **before anything is sent**: `--set scope.namespaces='{shop,payments}'` (only these), `--set scope.exclude='{hr-data}'` (never these, wins over everything), `--set-string scope.selector='continuum.io/scope=yes'` (namespaces carrying this label, added to the names), or the checkbox *Only look at some namespaces* in *Connect a cluster*. A namespace labelled `continuum.io/observe=false` is always left out, so a team can opt itself out without touching the agent's install.

- **What it is, and what it is not.** A privacy boundary, not a permission. The agent's RBAC is unchanged (a cluster-wide read-only role), so it still *can* read every namespace; it chooses not to send. If you need Kubernetes itself to refuse the read, per-namespace Roles are the next step and are not built.
- **Left out means gone.** The namespace, its workloads, services, pods and volumes are dropped in the agent, and traffic from or to their pods and Service addresses is discarded (it does not become a mystery "external endpoint"). A test serialises everything the agent sends and asserts that the excluded names appear nowhere. What the server is told about the rule is a count (`8 of 9 namespaces`, `1 namespace left out by name`), never the names that were left out; the Agents page shows it.
- **System namespaces** (`kube-system`, `kube-public`, `kube-node-lease`) are always read, to recognise the cluster and its mesh, but never drawn as applications.
- **Selectors use labels the agent keeps.** The agent forwards only allow-listed labels, so a selector on any other key (`team=a`) would silently match nothing; the agent refuses to start with one, and the wizard says so. Use a key starting `continuum.io/`, `app.kubernetes.io/` or `topology.kubernetes.io/`, or `app`, `k8s-app`, `istio-injection`.

### Service mesh (Istio, Linkerd; a toggle in *Options*)

The agent looks for a mesh from configuration alone (no proxy is queried, nothing is intercepted): namespace markers (`istio-injection`, `istio.io/rev`, `istio.io/dataplane-mode=ambient`, `linkerd.io/inject`), proxy containers in pods (`istio-proxy`, `linkerd-proxy`, and the Consul, Kuma and Envoy sidecar names), ambient pods (`ambient.istio.io/redirection`), the port lists a workload keeps out of its proxy (`traffic.sidecar.istio.io/exclude*Ports`, `config.linkerd.io/skip-*-ports`), the control plane (istiod, ztunnel, the CNI node agent, gateways, the Linkerd namespaces; the version is read from its image tag), and for Istio the `PeerAuthentication` objects, read every two minutes with a `get,list` rule that `mesh.readPolicy=false` removes (the agent then says the policy was not read and shows mutual TLS as "not known" rather than guessing).

- **Off by default.** With *Service mesh* off the topology looks as before, except that the mesh's own workloads (istiod, ztunnel, gateways) are no longer proposed or drawn as your applications. The switch is greyed out, with the reason, when no connected cluster has a mesh. It is saved in the URL and in saved views (`mesh=1`).
- **On:** the cluster header shows the mesh and mode, each service shows *Istio · sidecar*, *· ambient*, *· not injected* or *control plane*, the control plane appears in its cluster, and each seen connection between two services of the same cluster is coloured: green (both ends in the mesh and mutual TLS is strict, automatic or ambient), amber (permissive, or only one end in the mesh), red (a port kept out of the proxy, or mutual TLS switched off). Parallel lines between the same two boxes (two ports) are drawn side by side so neither hides the other. The inspector gives the sentence behind each colour, and the cluster and service panels list the mesh, mode, mutual-TLS mode per namespace, kept-out ports and the control plane.
- **Everything shown is inferred from configuration**, and says so wherever it appears. "Encrypted" means: proxies were seen, the policy requires it, and no port bypasses it. It does not mean anyone looked at the wire.

### History, change events and the consistency check

The server records the estate as it changes. Agents already stream changes as they happen; on top of that the server keeps:

- **Snapshots.** A compact, gzipped copy of what agents discovered, taken every *recording interval* (default 5 minutes, only when something differs, and at least once an hour) and right after a significant change. Retention is thinned, not cut: everything for 24 hours, then one per hour up to 7 days, then one per day up to the retention limit (default 30 days), plus a size cap (default 512 MB) that removes the oldest first. The newest is never deleted. Snapshot times are whole seconds.
- **Change events**, each with a likely cause where one can be told: a cluster or node appearing, going away, becoming unreachable or being upgraded; a service appearing, being removed, scaled ("no autoscaler is attached" or "the autoscaler changed it"), moved between clusters or nodes, restarted repeatedly, or given a new image; traffic seen for the first time or gone quiet. A burst of changes is summarised as one *many changes* event. The first recording of a server is the baseline and has no events.
- **A periodic consistency check.** Every *consistency interval* (default 15 minutes) each agent re-sends its complete picture. The server compares it with its own; any difference is corrected and recorded as a `drift` event ("a change was missed"), never fixed silently. The Discovery page shows per agent whether the last check matched.
- **Settings**, all editable by an administrator on the History page and pushed live to connected agents: recording interval, retention, size cap, consistency interval, how many missed heartbeats make an agent stale, when an observed link counts as quiet.

The UI's **History** page has a time scrubber, an exact-moment picker, the event list (filter by kind and cluster), busiest links (rates worked out from cumulative bytes between recordings) and the settings. "View the estate then" makes every page show that recording: a banner says so, editing is refused while it is shown, and "Return to now" leaves it. What agents discovered comes from the recording; what people own (names, sites, applications, devices, hand-drawn dependencies, policy) is shown as it is now, because it was never recorded, and the banner says that.

New nodes in an onboarded cluster are found by its agent. A new cluster or machine outside cannot be, so Continuum raises a **suspicion** in the Discovery inbox instead: traffic from an onboarded cluster to an address no onboarded cluster owns, on ports that only Kubernetes uses (6443, 10250, 2379/2380, 8472/udp), grouped by /24 and scored, with the evidence in the text. *Connect it* opens the Connect wizard; a dismissal is remembered. The suspicion goes away when the address is onboarded or the traffic stops.

### Reading the topology: metrics, moving lines and the filter

- **Cluster load** (CPU and memory requested by pods over what is allocatable, pods over capacity, nodes ready, services not fully up) sits under each cluster's name; a figure nobody reported is left out, never drawn as zero. The map's site card shows the same per cluster, and a dot gets an amber or red badge when a cluster there is 70% or 90% full.
- **Cross-cluster edges** show the measured round-trip time and loss when a path was measured (and turn amber at 1% loss, red at 5%). A service that the placement engine would run better elsewhere carries a small hint.
- **Map connections** animate like the other views: the dashes travel towards the side that receives more traffic, faster and thicker the busier the link (log scale), and slowly when nothing was measured. Hover one for dependencies, traffic each way, and the round-trip time and loss with whether they were measured or declared.
- **Filter** (Topology → *Filter*, also on the map): tick clusters and/or applications ("No application" included) to see only those. The choice lives in the URL and in saved views. It is strict: a service must be in a ticked cluster *and* a ticked application, and a dependency is drawn only if both of its ends are shown. Sites, devices and external endpoints follow what is kept.

- **Edge details** are not written on the lines any more (a line carries only its protocol and port, so nothing overlaps). Click a line to open the right bar: the connection, its observed rate, connections and bytes (with how fresh they are), and the network path between the two sites with measured round-trip time and loss.
- **Zoom levels**: zoomed out, cluster and service boxes keep only their name (drawn larger) and a status dot, plus a load badge on a cluster that is 70% or more full; zoomed in they show the full detail.
- **Agents page** (sidebar → *Agents*): every agent, where it runs (cluster, site, tier), whether it is connected, its last heartbeat, certificate expiry, the bytes and messages it has sent (syncs, flows, measurements, heartbeats) and what each discovery module reported, as a list or as a map grouped by tier. Counters are kept by the server in memory and start again when the server restarts; bytes are the size of the messages as encoded, before gRPC framing and TLS. Agents that only exist in the sample data are marked "Not live".
- **Live views and Neo4j**: the live topology is built from the server's in-memory model; Neo4j serves history, events, audit and past moments. The filter works on the model, so it behaves the same on both.

### Measurements between places (optional)

Without a mesh, Prometheus or an agent on every host, Continuum still learns how far apart places are: `--set measurements.enabled=true` (the *path measurements* checkbox, tier 2) lets the agent time TCP connections. The server tells the agent which addresses to time: the busiest observed outbound TCP destinations of that cluster (at most 16) plus addresses an administrator names on the **Sites** page. Every round takes a handful of connect timings per address (nothing is sent over the connection), keeps a rolling summary (median, best, 95th percentile, share failed) and reports it. Results are used by the placement advice in preference to estimates and are listed on the Sites page.

Security: measuring is off until enabled per agent; the agent refuses any address the server did not issue, and loopback, link-local (including the cloud metadata address), unspecified and multicast addresses are always refused, also after a host name resolves. The server accepts results only for targets it issued, with sane values. The minimum interval is 30 seconds.

### Placement advice, what-if and pluggable deciders

The **Placement** page answers "where would each service be better off, and what would that change?" It is read-only advice: nothing is ever moved, and every recommendation shows the evidence it stands on.

- **The cost function** counts, per service, the round trip to what it talks to weighted by how busy each connection is, the traffic that crosses between sites, a penalty for filling a cluster, and (once, subtracted from the gain) the cost of copying persistent data. The unit is *points*, where 1 point is 1 ms of round trip on a connection in constant use. Weights are yours to change in the *Policy* panel (kept in your browser): round trip 1 per ms, cross-site traffic 8 per MB/s, data copying 0.5 per GB, headroom 15, a move must gain at least 15 % and 2 points, and 80 ms is assumed where nothing is known.
- **Round-trip evidence**, best first: same cluster (0.3 ms), a measured path, a measured or declared link between sites, same site (1 ms), an estimate from the distance between coordinates (2 ms + 0.016 ms/km), otherwise unknown. Every figure says which of these it is, and each recommendation says how well evidenced it is ("well evidenced", "partly estimated", "mostly guessed", or "no evidence" when something it rests on is not known at all). Traffic weight comes from measured bytes per second (50 KB/s counts as fully busy); a declared-only dependency counts as moderately busy and says so.
- **Hard constraints** are never traded against cost: everything the *Can it move?* analysis knows (local volumes, node pinning, data residency, trust zones and tier policy) plus whether the target has room for the service's CPU and memory requests.
- **Uncertainty is never hidden.** Whether it fits is three-valued — *fits*, *does not fit*, or *can't tell* — never a yes/no with the unknown folded in: a cluster whose room is not known is a "can't tell" target with the missing fact and a fix, not a hidden zero and not skipped as unlimited. Every fact behind an answer (free CPU or memory, a round trip, how busy a link is) carries a confidence class — measured, reported, inferred or guessed, each with a documented margin (±5 % to ±50 %), one class worse once it is older than the staleness window — and a fit is only certified at the pessimistic end of that margin. A recommendation's overall confidence is the weakest deciding fact, never an average, so low or no confidence is shown hedged ("Not enough evidence to recommend — 2 of 3 inputs are guesses") rather than presented as the best option; an expandable *Why* lists every fact with its source and age, what would change the verdict, and, in What-if, how far the reported free capacity could be off before the answer flips. The rules and the factor table are in [`backend/docs/advice.md`](backend/docs/advice.md); the same logic runs identically in the server (`internal/advice`, added to the `/decide` request) and the browser (`src/lib/advice.ts`), so the two never disagree.
- **Recommendations are sequential**: the single best move is chosen, pretended to have happened, and the estate is looked at again, so two services are never told to swap places and a cluster that takes one service has less room for the next. At most 40 moves.
- **What-if.** Build a scenario of moves, or evacuate a whole cluster, and see the network cost, cross-site traffic and cluster load before and after, with warnings for pinned services, missing capacity and unchecked constraints.
- **Deciders.** A decider proposes moves; whatever it is, every proposal is checked against the same hard constraints and every survivor is scored by the same function, so deciders can be compared fairly on the *Deciders* tab. Built in: *Baseline (weighted cost)* (the rules above) and *Follow the heaviest talker* (a deliberately naive foil). An **external decider** is any HTTP service an administrator configures (name, URL, timeout of 1-25 s). The browser sends its input to the server, which forwards it to that URL only (never one given in the request), with no redirects, no proxy, no link-local addresses, size limits on both directions, and the URL hidden from non-administrators.

  Request (`POST`, `Content-Type: application/json`), schema 1; the *Preview* on the Deciders tab shows exactly this for your estate:

  ```json
  {
    "schema": 1, "question": "placement", "generatedAt": "2026-09-20T07:00:00Z",
    "policy": { "latency": 1, "traffic": 8, "migration": 0.5, "headroom": 15, "minBenefit": 0.15, "minAbsolute": 2, "fallbackMs": 80 },
    "clusters": [{ "id": "cl-1", "name": "athens", "tier": "edge", "site": "Athens", "country": "GR", "residency": "EU", "trustZone": "", "freeCpu": 6.5, "freeMemGb": 20 }],
    "links": [{ "from": "cl-1", "to": "cl-2", "rttMs": 38, "basis": "measured" }],
    "services": [{
      "id": "sv-1", "name": "inference", "namespace": "ml", "cluster": "cl-1", "kind": "Deployment", "replicas": 2,
      "cpuRequestM": 500, "memRequestMi": 1024, "volumesGb": 0, "sensitivity": "internal",
      "mobility": "free", "candidates": ["cl-2"], "constraints": []
    }],
    "flows": [{ "from": "sv-1", "to": "sv-9", "toKind": "service", "bytesPerSec": 180000, "connectionsPerMin": 30, "observed": true }]
  }
  ```

  Response: `{ "recommendations": [{ "serviceId": "sv-1", "to": "cl-2", "reason": "optional, shown to the reader" }] }` (`toCluster` is accepted for `to`; unknown fields are ignored; at most 5000 entries). A proposal is refused, with the reason shown under *Proposals that were refused*, when the service or cluster does not exist, the service already runs there, is proposed twice, is a DaemonSet or job, is pinned, or would not fit the target. Each proposal is checked on its own against today's estate; the combined effect of all kept proposals (including a cluster that would end up over-committed) is shown as warnings under the comparison. `candidates` already lists only the clusters where every hard constraint holds, so a decider that stays inside it is never refused.

### Security properties

- Agents authenticate with mTLS (TLS 1.3, ECDSA P-256). Their private key never leaves the agent. Certificates last 24 hours and renew at half-life; an agent offline for longer rejoins on its own within 7 days without a new token.
- First contact is protected by the CA pin from the install command. A wrong pin, wrong host or foreign CA is refused.
- Enrollment needs a one-time token **and** a human typing the approval code that only the agent's own log shows (see *Enrolling an agent and approval codes*). The server picks the certificate identity; the agent cannot choose it.
- Revocation is checked on every call and drops a live stream at once. Stealing an agent's key lets a thief keep renewing until you revoke it, so revoke promptly.
- The agent is read-only and never reads Secrets or ConfigMaps (RBAC in the chart, plus fields are discarded on arrival). Access tiers: 0 identity, 1 nodes and classes, 2 namespaces, pods and workloads. Tier 2 also reads persistent volume claims, volumes, autoscalers and disruption budgets; these are optional extras that the agent probes first and switches off (with a reason shown on the Discovery page) if the cluster does not allow or serve them. From pods it keeps only the names of claimed volumes; from volumes only the node they are tied to, never the volume source (NFS server, CSI attributes, host path).
- Users sign in with a password; there is no shared admin token any more. The server refuses to serve the admin UI and API in clear text on a non-loopback address unless you give `--admin-tls-cert/--admin-tls-key`, or `--admin-behind-tls-proxy` when a TLS proxy protects the port (this also makes it trust `X-Forwarded-For` and `X-Forwarded-Proto` from that proxy).

### Limitations today

Discovery and advice only: nothing is deployed to clusters and nothing is ever moved (tiers 3-4 are reserved and refused). The traffic observer was verified on one Linux 6.18 kernel (amd64, root, BTF): unit and race tests, the eBPF programs against real loopback traffic (roles, connections, bytes, resets, dropped-event accounting, live snapshots), the conntrack parser against the real table, and a full run of real server, real agent and real collector against a k3s API with real traffic between two addresses that pods were given (TCP through eBPF, UDP through conntrack, attributed to the right services, byte totals matching what was sent). Not verified: other kernels and distributions (eBPF needs 5.9+ with BTF, older kernels use conntrack), arm64 at run time (the object is built but was never executed), IPv6 (the code path exists; the sandbox has no IPv6), a real DaemonSet (pods cannot run in the test sandbox), a second real cluster (cross-cluster resolution is covered by tests that feed two clusters' facts, not by two live clusters), kube-proxy setups, and Pod Security enforcement. **Service mesh and namespace scope** were verified with Go tests (detection for Istio sidecar and ambient, Linkerd, no mesh; policy read allowed and forbidden; scope rules; a leak test over everything the agent serialises), agent-to-server tests, browser runs, and a live run of real server and agent against the sandbox k3s in which the *objects* of a mesh existed (an `istio-injection` namespace, an istiod and a gateway Deployment, pods with an `istio-proxy` container, a `PeerAuthentication` CRD with a strict mesh-wide and a permissive per-namespace object, a kept-out port annotation) and real traffic between the pods' addresses. **No mesh was ever running**: pods cannot start in the sandbox, so no real Envoy, ztunnel or Linkerd proxy was observed and nothing was checked against a real Istio or Linkerd release. Not built: ambient attribution (with ztunnel, a workload's traffic between ambient pods is carried over HBONE between node proxies, and the traffic observer can then see node-to-node connections instead of workload-to-workload ones; the mesh view marks ambient services but does not repair those edges), mesh detection for Cilium, and control-plane recognition for Consul and Kuma (their proxies are recognised, their control planes are not). Mesh policy other than `PeerAuthentication` (authorization policies, `DestinationRule` TLS settings, Linkerd's `Server` policies) is not read, so a connection this file calls encrypted might be refused or downgraded by a policy it cannot see; and traffic across two clusters is not judged. The eBPF method sees TCP only (UDP comes from conntrack, which needs an active conntrack rule on the node, as any Kubernetes node has), an idle connection that stays open without traffic for 24 hours turns stale, and the collector needs the privileges listed above. Without the optional node probe, VM versus bare-metal is only inferred from labels and provider IDs and a plain on-prem x86 node is marked as a low-confidence guess. The probe was verified on a real host, with real agent and server processes against k3s, but not inside a real DaemonSet (pods cannot run in the test sandbox), on a bare-metal server, on ARM boards or under a Pod Security-enforcing namespace; ARM and bare-metal results are unit-tested against fake sysfs trees only. GeoIP needs a database file you supply, and cluster placement is only ever a suggestion; the city table covers cities of 15,000+ people, so a factory in a smaller town is placed by picking a nearby city or typing coordinates. Movability is analysis over discovered facts (no volume-topology data). History, the consistency check, path measurements, suspicions and the placement engine were verified with Go tests under `-race`, an agent-to-server test with a fake network, unit tests of the engine and the past-view merge, browser runs, and one live run of real server, agent and collector against the sandbox k3s (a scaling is recorded as an event and a snapshot; the periodic check ran and matched; a real TCP connect time to an address was measured; real traffic to the API-server and kubelet ports of an unowned address raised a suspicion). Not verified live: a second real cluster (so the placement advice was verified on the sample estate and by tests, not on two live clusters), drift caused by a genuinely lost message, an external decider other than the test doubles, and measurements over real WAN paths (the sandbox only reaches local addresses; round-trip times in the milliseconds range over the internet, ICMP-only networks and hosts that filter TCP are untested). Measurements are TCP connect times, which include the far side's accept queue and say nothing about bandwidth; a path to a host that never answers is reported as failed, not slow. The placement weights are a starting point, not a calibrated model: with little measured traffic the advice says "mostly guessed", and it should be read as a hint until paths are measured. Snapshots hold what agents discovered, so a past view uses today's names, sites and policy. Storage, autoscaling, disruption budgets and pod counts were verified against k3s only (not yet EKS/GKE/AKS); autoscalers on custom or external metrics are shown by metric name, without their target values. Local accounts only (no OIDC or SSO yet). Control data (accounts, sessions, tokens, the CA, organizations and the workspace) is in SQLite; history, events and the audit trail can be in Neo4j (verified against Neo4j Community 5.26 over its HTTP API, not against a cluster or Aura, and the Go driver was not used), and Neo4j Community separates tenants by query only. Workspace revisions in the graph are best-effort (a save is never refused because the graph is down). The workspace is one document per organization, so two people editing at the same moment resolve by choosing a side rather than by merging. The Dockerfile and chart have not been published or tested against a registry. Agent memory was measured on amd64 only (about 60 MB at 400 workloads).

```bash
cd backend && go vet ./... && go test -race ./...
```

## Project layout

```
src/
  lib/types.ts        Domain model (Cluster, MachineNode, Service, Device, Dependency, Site…) + control records (Agent, Suggestion, AuditEvent)
  lib/migrate.ts      upgrade() v1/v2 → v3 and normalize() (drops dangling references)
  lib/effective.ts    two-layer values: detected base + human overrides; tombstones hidden
  lib/suggestions.ts  what accepting a discovery suggestion changes (pure, tested)
  lib/seed.ts         Sample cloud → edge → far-edge topology
  lib/graph.ts        buildGraph(): model + view options -> React Flow nodes/edges (layout lives here)
  lib/history.ts      settings, snapshots, change events and atSnapshot() (the past view)
  lib/placement/      world (evidence for round trips), engine (evaluate/recommend/what-if/evacuate), deciders (built-in, external, vet, compare)
  store/topology.ts   Zustand store: CRUD with cascades, import validation/repair, persistence
  components/
    Layout.tsx        NetBird-style sidebar shell
    ui/primitives.tsx Button, Input, Modal, Table, badges…
    forms.tsx         Cluster / Node / Service forms (service form edits dependencies too)
    topology/         Custom React Flow nodes + right-hand inspector
  pages/              Topology canvas, Clusters, Nodes, Services, Devices, Applications, Sites, Discovery, Import/Export
```

## What discovery reports

Per cluster: distribution, version, provider, region label, age. Per node: capacity and allocatable, resources promised to pods, **pods placed versus the kubelet's limit** (the real cap on small boards), age, accelerators, taints and conditions. Per service: image, replicas, requests and limits, exposure, placement constraints, **persistent volume claims (size, class, and the node a local volume is pinned to), the autoscaler that targets it (min-max replicas and metric) and the disruption budget that selects it**. Pod-derived node figures are *unknown* (not zero) when the agent does not read pods.

IP addresses are labelled **private / public / shared (CGNAT) / loopback / link-local**, for IPv4 and IPv6; a hostname is left unlabelled because names are not resolved.

### Where a cluster is (location from tables, not from labels)

A region label is free text ("eu-central-1", "Patras HQ", "rack 7"), so it is never trusted on its own. For a cluster that is not on a site yet, the app works out *candidate places* from three sources, best first, and shows the best one as a suggestion with its reason and confidence:

1. **A cloud region table** (`src/data/cloud-regions.ts`: AWS, GCP, Azure, Hetzner, OVH, DigitalOcean, Scaleway). `eu-central-1`, `europe-west3`, `westeurope` and zone labels such as `eu-central-1a` resolve to a metro area exactly. When the cluster's provider is known it has to agree with the table, so an AWS code on a "GCP" cluster is not trusted. Unknown codes give no suggestion rather than a wrong one.
2. **A city named in the label**, matched against ~34,000 cities of 15,000+ people (GeoNames, CC BY 4.0, `src/data/cities.tsv`; accents are folded, common English names such as "Patras" or "Cologne" are mapped through `src/data/exonyms.ts`, and words that name a kind of place, not a place ("lab", "hq", "edge") are ignored). Medium confidence: it is still free text.
3. **GeoIP of the address the agent connects from** (see *GeoIP* above), low or medium confidence: behind a cloud provider's network, a VPN or a mobile network the address says little about the cluster. A database that only knows the country gives a starting point (its largest city) marked low.

Two sources that agree within about 30 km raise each other's confidence one step. Suggestions are **worked out on the fly and are never stored until a person accepts or dismisses them** (so every browser sees the same ones, and nothing churns in a shared workspace); an accepted one creates a site (deterministic id, reusing a site that is already within ~30 km) and puts the cluster there as a human override that survives rediscovery. They appear in the Discovery inbox, on the Clusters page, in the cluster inspector and on the map's "not on the map yet" list.

The other direction is checked too: in the site form, a **city search** fills in city, country and coordinates from the table, and coordinates are checked against country outlines (with a tolerance for coasts and borders), so a typo or a wrong country is flagged, with a one-click fix, both in the form and in the Sites table.

The city table is about 640 KB gzipped and is downloaded only when something needs it (a cluster without a place, or the site form).

## How the views work

`src/lib/graph.ts` is the single place that turns the model into a picture:

- Each **tier** (`cloud`, `edge`, `far-edge`) is a horizontal row; clusters sit side by side within it. *Group by → Tier* collapses clusters into one box per tier.
- Cluster boxes are React Flow parent nodes (custom type `boundary`); services/machines are children.
- Edge endpoints are picked by relative position (top/bottom/left/right handles) so lines leave from the sensible side.
- Selecting a node opens the inspector, highlights its edges and dims the rest. Toolbar state lives in the URL (`?view=infrastructure&group=tier&services=1`) so a view is linkable.

**Map view** (`?view=map`): an SVG world map drawn by the app itself (Natural Earth boundaries via `world-atlas`, projected with `d3-geo`; no tiles and no external service). Sites appear as dots whose **fill is the tier** (cloud, edge, far edge; the most common tier of the clusters there) and whose **ring is the worst status** of what is there, so both can be read at a glance. Hovering a dot shows its clusters and the **public exit IP** they reach the server from; the same appears in the site inspector. Dots that are close on screen merge into one numbered dot, so zoomed out you see countries and zooming in separates the cities; click a merged dot to zoom into it, a single dot to open the site. Dashed arcs show sites that are linked or whose services call each other. Clusters without a site are listed on the map so nothing silently disappears. The detailed coastlines are downloaded only when you zoom in.

**Can it move?** Selecting a service shows a verdict (*Free to move*, *Move with care*, *Pinned*), the reasons behind it and which other clusters could host it (`src/lib/movability.ts`). Blockers are a local volume, a machine-bound device, a hostname selector, a DaemonSet; cautions are cloud volumes to copy, architecture, accelerator or label selectors, a disruption budget that allows nothing, a single replica, ingress hosts that must follow, data residency and restricted trust zones. A candidate cluster is ruled out for a data-residency or trust-zone mismatch, a selector no node satisfies, or too little free CPU or memory, and each exclusion says why. The same verdict is a chip in the Services table. It is analysis only: nothing is moved.

**Saved views** (the *Views* menu in the Topology toolbar) store a name and the URL options (view, grouping, what is shown) in the workspace, so a team shares them; they hold no selection and no data, so they cannot go stale. **Search** (`Ctrl/Cmd + K`, or the button in the sidebar) jumps to any cluster, node, service, device, application, site, page or saved view; results open in the Topology inspector through `?sel=service:<id>`.

To add a new plane (network, data-flow, cost…): add a value to `ViewKind`, a branch in `buildGraph`, and a toggle in `TopologyPage`.

## Schema, migrations and the override layer

- Exported files and the shared workspace carry `schemaVersion` (currently 4). Older data is upgraded on load: `workloads` become `services`, org ids and dependency `sources`/`confidence`/endpoint kinds get defaults, and new collections start empty. **From v4 the workspace holds declared intent only.** Records an agent discovered are never stored in it (the server re-sends them); what a person said about one of them, an override or an assignment to an application or site, is kept under `refs`, keyed by the record's stable id. A file from a *newer* schema is rejected with a clear message rather than mangled.
- Every record has an `orgId` (the organization it belongs to).
- Discovered records keep the agent-reported values as their base fields; anything a person edits is stored separately as `overrides` (in `refs`, in the saved workspace). The UI shows `{...base, ...overrides}`, so rediscovery never erases a human edit, and *Reset to detected values* drops the overrides.
- Dependencies record `sources` (`declared` / `observed` / `manual`), `confidence`, first/last seen and optional rolling `stats`, ready for the agent's observers. Each end is a `service`, a `device` or an `external` endpoint.

### Schema v3 in short

- **Devices** are their own entity (not microservices): `kind`, `count` (one record = a fleet of identical units), protocol, connectivity, application, site, optional attached node. They send data to services and services can command them.
- **Discovery bookkeeping** on every record: stable `key` (kube-system UID, machine-id…), `agentId`, `revision`, `detectedAt`, `stale`, `deletedAt` tombstone, and per-field `evidence` (signal + confidence) so a guess like "k3s" or "VM" can explain itself. None of it can be overridden by a person.
- **Attributes** discovery fills in: node architecture, kernel, runtime, hardware model, accelerators, allocatable vs requested, taints, conditions, uplink; cluster CNI, ingress, CIDRs (overlaps are flagged), storage classes, egress IP; service ready replicas, digest, requests/limits, exposure, managed-by, node selector, tolerations; dependency stats.
- **Policy inputs**: trust zone and data residency on sites and clusters, sensitivity on services.
- **Control records** (Postgres later, not the graph): `Agent` (access tier, per-module status and why one was skipped), `Suggestion` (discovery inbox; accepting one with an `apply` action changes the model, edits to discovered records land as overrides) and `AuditEvent`.
- `SiteLink` (RTT, loss, bandwidth) and `Namespace` complete the graph.

## Tests

```bash
npm run test:unit   # model, workspace, history, placement, consent, first-run checklist and namespaces suites. Model logic: overrides, migration, validation, completeness, IP scope, map grouping and tiers, place resolution (cloud regions, city table, coordinates to country, suggestions), movability, saved views, search, observed traffic overlay and its naming, edge weights and noise filtering
```

## Graph-shaped by design

Entities have stable ids and typed relations, and every entity carries `source: 'manual' | 'discovered' | 'imported'` plus the discovery bookkeeping described under *Schema v3 in short*. The model is ten collections: `clusters`, `nodes`, `namespaces`, `services` (a workload: Deployment, StatefulSet, DaemonSet or Job), `devices`, `dependencies`, `applications`, `sites`, `siteLinks` and `externalEndpoints`, plus what agents reported about themselves. As a property graph:

```
(:Node)-[:IN_CLUSTER]->(:Cluster)
(:Namespace)-[:IN_CLUSTER]->(:Cluster)
(:Service)-[:IN_CLUSTER]->(:Cluster)
(:Service)-[:RUNS_ON]->(:Node)
(:Service|Device|External)-[:CALLS {port, protocol, sources, confidence}]->(:Service|Device|External)
(:Path)-[:PATH_FROM|PATH_TO]->(:Cluster)              measured round trip and loss between two clusters
```

The five relations above (`IN_CLUSTER`, `RUNS_ON`, `CALLS`, `PATH_FROM`, `PATH_TO`) are what the optional Neo4j memory stores, each with `validFrom`/`validTo`. The rest is references between records, kept in the model and not stored as edges: a service or namespace belongs to an **application** (`applicationId`), a cluster or device sits at a **site** (`siteId`), a device may be attached to a node, and sites are joined by `siteLinks`. A `CALLS` end is a service, a device or an external endpoint (SaaS, database, or unknown), and `sources` says whether the call was `declared`, `observed` or `manual`.

## Roadmap

Where this stands, in the order it was built:

1. **Trust foundation: done.** Sign-in, organizations and roles, consent that lives with the cluster's owner (what an agent may see, and which namespaces), approval codes, an audit trail, and per-value confidence so a guess is shown as a guess.
2. **Discovery and the digital twin: done.** The agent, node probe and traffic observer; history and change events; the consistency check; path measurements; the placement engine with what-if and pluggable deciders (read-only advice). Verified against a single-node k3s API and by tests; **not verified against real clusters or running pods** (pods cannot run in the sandbox): see *Limitations today* for exactly what was and was not exercised.
3. **UX pass: in progress.** A first-run checklist that tracks real server state, a Namespaces page, one flow for connecting and approving clusters (Discovery to find, Agents to manage), honest empty states, per-row freshness and evidence chips, and a keyboard and screen-reader pass with an automated accessibility smoke test.
4. **Uncertainty-aware placement advice: done.** A three-valued fit (fits / does not fit / can't tell), confidence from the weakest deciding fact, the facts behind every answer and what would change it, and a sensitivity readout in What-if. See *Placement advice, what-if and pluggable deciders* and [`backend/docs/advice.md`](backend/docs/advice.md).
5. **Acting on advice: paused.** Deploying to clusters or moving anything, first through a built-in operator and then a Karmada adapter, is deliberately not started until asked for; tiers 3-4 are reserved and refused.

Later, if graphs outgrow hand placement: ELK (compound and layered) instead of the tier-row layout, and edge bundling for dense cross-cluster traffic. A Neo4j *import* (read an existing graph and mark entities `imported`) and OIDC sign-in are not built.

### Where this could go next

The **`Decider` interface is the one deliberate gap worth calling out**: it is a real, working plug point today (an external decider is *any* HTTP service speaking the schema in *Placement advice, what-if and pluggable deciders* above, already exercised by the built-in "Baseline" and "Follow the heaviest talker" and compared fairly on the *Deciders* tab), but nothing dynamic or learned has been plugged into it yet - it is infrastructure for AI-driven placement, not an example of it. That is the natural next milestone for orchestration research built on top of this project, and a few things would make it a better testbed for that specifically, roughly in the order they unlock each other:

1. **A reference learned decider**, shipped as its own small service speaking the same schema (so it costs nothing to swap for a baseline in a comparison): something that learns from the history already being recorded (snapshots, change events, measured round trips and traffic) rather than only the instantaneous request - a contextual bandit over move outcomes, or an online model re-scored as new measurements arrive, would both fit the existing "confidence class" vocabulary naturally (a learned estimate is just another evidence class, no schema change needed).
2. **A feedback loop closing the read-only gap.** Right now nothing records whether a person actually followed a recommendation or what happened afterward, so there is no way to tell a good decider from a lucky one. Even before "acting on advice" is unpaused: a lightweight "I did this" log (which recommendation, when, and the estate a recording interval later) would let a decider be scored against its own past predictions - the minimum a research pipeline needs to claim a learned policy is actually better, not just different.
3. **Offline replay for evaluation.** History already keeps thinned snapshots; a batch mode that re-plays a sequence of them through several deciders and reports the counterfactual cost each would have produced turns every already-recorded estate into a benchmark, with no cluster and no risk required to compare policies. This is the difference between "looks reasonable in a demo" and "measurably better on real traces" for a paper's evaluation section.
4. **Acting on advice, narrowly first.** Rather than jumping straight to a general operator, the smallest useful step is applying a single already-computed recommendation as a Kubernetes-native hint (a `nodeAffinity`/`topologySpreadConstraint` patch, or cordoning a node before a planned move) behind an explicit, audited, one-recommendation-at-a-time approval - keeping the project's existing "nothing moves without a person's say-so" posture intact while finally closing the loop from advice to effect for whoever wants to evaluate that, too.
5. **A cost function that is pluggable, not just its weights.** Today the five terms (round trip, cross-site traffic, migration cost, headroom, the 80 ms fallback) are fixed and only their weights move in the *Policy* panel; letting a decider supply its own scoring function (still checked against the same hard constraints) would open the door to alternative objectives research increasingly cares about for edge/continuum placement - energy or carbon intensity per site, tail latency instead of mean, or a fairness term across tenants sharing a cluster - without forking the engine.
6. **A synthetic topology generator.** Discovery only ever sees real clusters, which is right for production but makes it hard to reproduce an experiment or share a scenario with a collaborator. A generator that produces a plausible cloud/edge/far-edge estate (configurable size, churn rate, traffic pattern, link quality) and feeds it through the same model the UI already renders would give the placement engine and any learned decider a repeatable benchmark independent of hardware access - and a natural export/import point to and from established continuum simulators (iFogSim, EdgeCloudSim, PureEdgeSim) for anyone who wants to compare against simulation-only baselines.
7. **Failure and churn injection.** The project's honesty about degraded states (a stale agent, a missed heartbeat, an unreachable Neo4j) is already a real strength; deliberately triggering those conditions against a synthetic or sandbox topology - and watching whether placement advice, the consistency check and the UI's own "unknown, not zero" posture hold up - would turn that philosophy into a resilience test suite, which is exactly the kind of evidence a paper about dynamic orchestration under uncertainty needs.

None of the above changes the read-only, nothing-moves-without-approval posture the project has held throughout; each is additive to the existing `Decider` contract, history store, or advice engine, not a redesign of them.

## Data and attribution

The footer text in the app (`OWNER` in `src/components/ui/brand.tsx`) reads "© <year> Continuum Topology Studio. All rights reserved." — replace it with the name of the actual rights holder. That string is just UI copy; the code itself is licensed under Apache 2.0 regardless of what it says (see *License* below).

- City names and coordinates: [GeoNames](https://www.geonames.org/) `cities15000`, CC BY 4.0. Rebuild with `scripts/build-geodata.py`.
- Country outlines: Natural Earth via `world-atlas` (public domain).
- IP geolocation (optional, supplied by you): IP geolocation by [DB-IP.com](https://db-ip.com) (CC BY 4.0) or MaxMind GeoLite2 under its own license.

## Continuous integration and releases

Three workflows: [`ci.yml`](.github/workflows/ci.yml) (build, vet, test and lint on every push and PR, plus two dependency scans), [`release.yml`](.github/workflows/release.yml) (publishes images and Helm charts to this repo's own GHCR namespace on a push to `main` or a version tag), and [`docs.yml`](.github/workflows/docs.yml) (publishes the docs site to GitHub Pages). The one-time setup each needs, and exactly what gets published where, is in the docs site's **Contributing → Release process** page.

## License

Apache License 2.0 - see [LICENSE](LICENSE). Backend (Go) and frontend (npm) dependencies were checked for license compatibility; nothing under a copyleft license that would affect this project's own licensing terms was found, with one deliberate exception documented in [`deploy/README.md`](deploy/README.md#neo4j): the *optional*, separately-run bundled Neo4j Community Edition is GPLv3, which is why the server talks to it only over HTTP as a separate process rather than linking against it, and why `neo4j.mode: external` exists for anyone who would rather not run GPL software in their deployment at all.

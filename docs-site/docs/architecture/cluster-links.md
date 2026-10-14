---
id: cluster-links
title: Cluster links
description: How the quality of the connection between two clusters is measured and exported — which pairs are watched, what each source can and cannot tell you, and what has been built so far.
---

# Cluster links

A link is the connection between two onboarded clusters. Knowing it matters to placement and to troubleshooting: an application that calls across a link inherits its loss, delay and jitter, and nothing else in the system sees those directly. The measurements are exported as Prometheus-style metrics through the same collector pipeline as every other [telemetry signal](./telemetry-intent.md), so they follow the same destination, scope and provenance rules and arrive in FUSION like the rest.

No single tool covers a link. Real traffic shows what applications actually experience but says nothing about an idle link. A constant probe shows an idle link but travels a path of its own, and a ping is blocked by many networks. A throughput test shows capacity but loads the link. So the design combines sources, and gives each one a job that the others cannot do.

## Which pairs are measured

Only pairs an administrator turns on. The setting is `links` in the organisation settings, a list of cluster pairs; a pair is measured in both directions, and a pair that is not listed is not measured at all. Each cluster of a pair exports its own passive series, but the active measurements of a pair are made from one side only (see [Who measures what](#who-measures-what)). Nothing is probed between every pair automatically, because the cost grows with the square of the number of clusters, and the throughput test spends real traffic.

The server is the only place that knows which address belongs to which cluster, so it tells each agent about its peers in the `Config` it already pushes: the peer's id, its name for display, and the addresses its traffic comes from or goes to (its nodes' addresses, including overlay and tunnel addresses, and the address its agent connects from). A cluster only ever learns the addresses of the clusters it was paired with. An address the two clusters share, such as two clusters on the same private range, is left out rather than mislabelled, and an address claimed by two different peers is dropped by the agent. Peers are sent only at access tier 2, because the measurements are derived from observed traffic, and are withdrawn while the flow collector is paused for that agent. The list also says, for each peer, whether the other cluster of the pair makes the one-way probe and the connect timing (`skip_probe`, below). The list is re-sent when settings change and at least once a minute, so a node added to a peer cluster reaches its partners without anyone doing anything.

## Who measures what

The one-way probe measures both directions from one end, and the connect timing is the same round trip whichever end takes it. If both clusters of a pair ran them, each direction would be measured twice, the link would carry twice the probe packets, the pair would export about twice the active series, and every consumer would have to avoid counting the pair twice. So the work is split, with the same rule the throughput tests already used: the cluster whose id sorts first is the pair's runner.

| Measurement | Runs on | Series carry |
| --- | --- | --- |
| Passive link metrics (traffic, retransmits, round-trip histogram, failures) | both clusters, each its own observation | `cluster` = the observing cluster |
| One-way probe (OWAMP), both directions | the first cluster only | `cluster` = the first id, `direction` = `forward` from it to the peer, `reverse` from the peer to it |
| Connect timing | the first cluster only | `cluster` = the first id |
| Throughput tests, both directions | the first cluster only | `cluster` = the first id |
| Route and MTU trace | both clusters: the two directions can take different routes, so it is not a duplicate | `cluster` = the tracing cluster |

The other cluster only serves: its responder (and iperf3 server) answers, and its prober traces the route. A consumer therefore finds a pair's active series under `cluster=<first id>`; a query that reads the one-way probe, the connect timing or the throughput of a pair by either cluster's label sees each measurement once. The server tells the second cluster by setting `skip_probe` on the peer in its `Config` (a field of `LinkPeer`; unset means "run everything", so an older server, or an older agent that does not know the field, behaves as before and measures from both sides).

**A pair is never left with nobody measuring.** The server sets `skip_probe` for the second cluster only while the first can measure: its agent is approved, at access tier 2, has not paused the flow collector, and has told the server enough for the second cluster to be in its peer list (the same conditions under which any peer list is sent). When the first cluster stops meeting them, the second cluster's next `Config` drops `skip_probe` and it probes again (the Config is re-sent when settings change and at least once a minute, so this takes up to a minute); when the first is back, the second stops again. Whether the chart on the first cluster has the probe switched on is something the agents now report (see [What a pair needs](#what-a-pair-needs)) and the Sites screen says so, but the server does not yet change who measures because of it: see [the chart switch](#active-measurement-loss-and-delay-in-each-direction) below.

When a cluster is not told about its peer, the Sites screen says why under the pair, whether or not FUSION can be read (`notes` in `GET /links/latest`). The reasons are the ones the server uses to decide: the cluster has no approved agent; its agent is below access tier 2; its flow collector is paused; the peer's agent has reported no address yet; or every address of the peer is also one of this cluster's own, or the public address its agent connects from (two clusters behind the same NAT, say), so traffic to the peer cannot be told from its own and the pair is left out there. A pair that is [pinned to one kind of address](#which-address-is-measured) has one more reason: the peer has no address of that kind (`no-address-of-kind`). A pair that is measured has no note.

## Choices per pair

Each entry of `links` can carry choices of its own, the same for both clusters of the pair:

```json
{"a": "cl-eu", "b": "cl-us", "noProbe": false, "via": "public", "throughput": {"intervalMinutes": 720, "budgetMB": 4096}}
```

- `noProbe: true` turns the one-way probe, the connect timing and the route trace off for the pair, on both clusters. The passive metrics stay, and the pair has no active series at all (not counters that read zero). Without it, the first cluster runs the probe and the connect timing, and both trace the route.
- `via` pins the active measurements of the pair (the one-way probe, the connect timing, the route trace and the throughput tests) to one kind of the peer's addresses: `mesh`, `private` or `public`. Without it (the default, and the recommended choice) they follow the address the real traffic is seen using. See [Which address is measured](#which-address-is-measured). Any other value is refused when the settings are saved.
- `throughput` asks for the [throughput tests](#throughput-tests-how-much-a-link-carries) on the pair. Without it the pair gets none, even where the cluster has them enabled in the chart: the chart says what a cluster *can* do, the pair says what it *should*, because those tests load the link. Only one cluster of the pair runs them, in both directions: the one whose id sorts first, so the link is not loaded twice. The other cluster only serves them, so it needs the iperf3 server (the same chart setting) but runs no tests, and has no throughput series for the pair; the results are in the first cluster's metrics, labelled with the pair.
- `throughput.intervalMinutes` (10 minutes to 7 days) replaces the cluster's interval between two sets of tests for this pair, and `throughput.budgetMB` (up to 1 TiB) is the most the tests may carry on this link in a day, in addition to the cluster's own budget: a test has to fit both. The pair's use and limit are exported as `continuum_link_throughput_pair_budget_used_bytes` and `..._pair_budget_bytes`.

The agent holds each value to the same bounds as the server, whatever a Config says. The options are part of the settings and are saved with them.

### Turning a cluster on

In the telemetry wizard, **Cluster links** (infrastructure, metrics) makes a cluster able to take part. Ticking it adds `telemetry.clusterLinks.metrics.enabled=true` and also `flowObserver.enabled=true`, because the link metrics are labelled from what the flow collectors see (access level 2). Two choices appear under it:

- **Probing** (on by default) starts the prober and the responder on every node. Switch it on in both clusters of a pair: the one that sorts first measures and the other answers, and the answering needs the responder that comes with this switch.
- **Throughput tests** (off by default, and only with probing) adds the iperf3 server and client. Nothing else to fill in: Ikhnos issues the iperf3 credentials itself (see [Who may use the server](#throughput-tests-how-much-a-link-carries)). Under "Advanced: use my own iperf3 credentials" you can instead name a Secret of your own that exists in every cluster of the mesh.

Ticking the signal does not measure anything by itself: no pair exists until an administrator adds one under Sites → Cluster links. On a cluster that already has the signal, the wizard leaves probing and throughput as they are until you change them there, since the wizard does not read what the install has (the Sites screen does: see [What a pair needs](#what-a-pair-needs)). Unticking the signal also states the probe and throughput switches off, because the chart refuses a prober without the metrics switch.

### In the app

An administrator manages the pairs on the Sites page, in the **Cluster links** section under Measured paths; everyone else sees the list. **Add pair** takes two clusters that each have an approved agent and starts with probing on and throughput off, so nothing heavy runs by surprise. A pair's edit panel switches probing and throughput on or off, has a **Measure through** choice (the address your traffic uses, which is the default, or mesh, private or public addresses; the same choice is offered when the pair is added), and offers three named amounts for the tests: Light (every day, 50 MB a day), Standard (every 6 hours, 500 MB) and Heavy (every hour, 5 GB), or Custom numbers within the bounds above. It also lists what each cluster needs (a connected agent, access level 2, the flow collector running) and says which cluster runs the probing and the tests; a missing requirement is a warning, not a block, because the settings apply when the cluster catches up.

**Latest result.** Each row shows the newest figures of its pair: loss in each direction, the round trip, jitter, the smallest MTU, and the rate of the latest throughput tests, with how long ago the probe last finished. An arrow points from the first cluster named in the row to the second, and the back arrow the other way. The loss turns amber from 1 % and red from 5 % in either direction. The figures are not stored by the server: `GET /api/v1/orgs/{org}/links/latest` (any member of the organisation) reads them from the link series in FUSION with fixed queries, once every 15 seconds however many people have the page open, and the page asks again every minute while it is visible. A figure FUSION does not hold is left out, never shown as zero. When both clusters of a pair report, the first one's figures are used, and where the first has none, the second's; the MTU is the smaller of the two. Jitter is the median delay variation of the one-way probe and the round trip is the connect timing, so a pair with probing off shows no figures and says to look at its traffic in FUSION. The row says that probing and the tests run from the first cluster. Where an older agent still probes from both sides, the two clusters' figures can differ a little, which is why the first one's win. The answer says `available: false` with `reason` `no-fusion` (the server has no FUSION) or `unavailable` (it could not be read), and never carries what the store replied.

The pairs are one part of the organisation settings, and a save sends the whole settings document. To keep two administrators (or two tabs) from silently undoing each other, the settings carry a revision: `GET /api/v1/settings` returns it as `rev`, and `PUT /api/v1/settings` names the revision the edit started from in the `If-Match` header. If someone else saved in between, the server stores nothing and answers `409 Conflict` with the current document; the page then reloads it and says "Someone else changed the settings; reloaded - try again", and the dialog stays open. A `PUT` without `If-Match` is still accepted and replaces the document, for scripts.

## What a pair needs

A pair gives figures only when several things are true at once, in two different clusters, and until they are the Latest result stays empty without saying why. The server therefore works out a checklist for every listed pair and the Sites screen shows it under the pair. For each pair, per cluster where it applies:

1. **Both clusters have an agent the server can use.** An approved agent, at access tier 2, with the flow collector not paused, and told about the other cluster (the reasons in [Who measures what](#who-measures-what) are the same ones).
2. **Cluster links is switched on in each install.** The agent serves the link metrics (`telemetry.clusterLinks.metrics.enabled`). When the pair asks for active measurement (probing on, or throughput tests), probing is on in both (`telemetry.clusterLinks.probe.enabled`), because the cluster that runs the probes needs the prober and the other needs the responder.
3. **The cluster that runs the probes sends telemetry to FUSION.** The pair's runner (the cluster whose id sorts first, or the second while the first cannot measure) makes the one-way probe, the connect timing and the throughput tests. What it measures reaches FUSION, and so Grafana and the Latest result, only through that cluster's own telemetry: the cluster collector with an export destination, that is, a local operator or a backend with Cluster links on. A pair with probing off has no runner; each cluster's passive series then leave through its own telemetry, and one of them sending is enough.
4. **The runner's prober has been seen recently.** The prober asks its agent for the clusters to measure about once a minute; the step is met when it last asked within 10 minutes. It only applies where probing is on.
5. **Throughput tests have iperf3 in both clusters**, when the pair asks for them (`telemetry.clusterLinks.probe.capacity.enabled`): the first cluster runs the tests and the other answers.
6. **Optional: the other cluster sends telemetry too.** It adds its traffic series and its half of the path MTU; the pair works without it, so this is never a failure.

Each step has a state: *done*, *to do*, *not known* or *optional*, and a sentence that names the cluster and says what to do, for example "sunrise-cluster runs the probes for this pair but has no telemetry sent to FUSION, so what it measures stays in the cluster. Turn on telemetry with Cluster links there and point it at a regional operator or backend." A step that cannot be told is *not known*, never *to do*: an agent too old to report what its install can do, or one that has not reported for 15 minutes, gives one "not known" line for that cluster in place of steps 2 to 5, and nothing about it is called missing. A server without FUSION cannot say where a cluster's figures would go, so it says that once, as *not known*, instead of calling the telemetry missing.

**What the agents report.** The agent sends `LinkCapabilities` in every heartbeat (field 5 of `Heartbeat`): `link_metrics` (it serves the link metrics), `probe_enabled` (the prober and responder are in the install), `capacity_enabled` (iperf3), `telemetry_export` (this install runs a cluster collector that exports the link metrics to a destination) and `prober_seen_unix` (when the link prober last made a valid signed request to `/v1/link/peers`, 0 if never). The chart tells the agent the three it cannot know itself with `CONTINUUM_LINK_CAPACITY` and `CONTINUUM_LINK_TELEMETRY_EXPORT`, computed from the same conditions that render the cluster collector's configuration and an export destination for the metrics (their own route, or the default one). `link_metrics` is whether the link meter exists, and `probe_enabled` is `CONTINUUM_LINK_SERVE_PEERS`. An agent without Cluster links sends the message with everything false, so "off here" and "too old to say" are different things. The server keeps the latest report per agent in memory (a restart brings it back with the next heartbeat), reads `prober_seen_unix` in its own clock using the clock difference the agent measured, and never accepts a time in the future. The report carries no cluster data.

**Where it is shown.** `GET /api/v1/orgs/{org}/links/latest` carries it as `setup`, a list with an entry `{a, b, steps: [{code, cluster?, state, text}]}` per listed pair, whether or not FUSION can be read, next to `notes` (which keep saying why a cluster is not told about its peer). The codes are `agent`, `addresses`, `capabilities`, `links-on`, `export`, `prober`, `capacity` and `peer-export`; the states are `ok`, `todo`, `unknown` and `optional`. On the Sites screen each pair shows its unmet steps under its name, at most two and then "and N more", and a **Setup** button that opens the whole list with a mark and a word for each state. When a note and an unmet step say the same of a cluster, only the step is shown. The same sentence about what a pair needs is at the top of the Cluster links section, in the Add pair dialog (the button is disabled, with the reason, until two clusters have an approved agent) and beside the Cluster links switch in the telemetry wizard.

## Which address is measured

A cluster is known by many addresses: its nodes' internal and external addresses, the addresses of any tunnel or mesh interface (a Tailscale `100.64.x.x`, say), and the public address its agent connects from. The server sends the agent all of them for each peer, and they are used differently.

- **The passive metrics use the whole list.** A connection the flow collector sees is matched against every address of every peer, so traffic is counted whichever address it uses.
- **The active measurements use one path at a time.** The one-way probe, the connect timing, the route trace and the throughput tests each need a destination, and the addresses of a cluster can lead to it by different paths: over the public internet, over a private link, over a mesh. A figure is only about the path it was measured on.

By default the active measurements follow the traffic. The agent notes, per peer, which of its addresses the connections it observes really use (the busiest and most recent first, at most 16 addresses per peer, forgotten after an hour without traffic, and held in memory only) and tells the prober. The prober then tries those addresses first, in that order, then the one that answered last, then the others in random order, within the same limits on how many are tried as before. So the measurement is made through the address your clusters really talk over, and if that address does not answer (the responder only runs on some nodes, a firewall closes the port) it falls back to the others rather than reporting the link as down. The route trace goes towards the first of that order, and keeps to it from one trace to the next so a route change is a real one. A peer whose traffic has not been seen yet, or an agent too old to say, behaves as it did before: the address that answered last, then the others at random.

An administrator can **pin a pair** to one kind of address with `via`, in the pair's edit panel under **Measure through**:

| `via` | Addresses used |
| --- | --- |
| (empty) | The address the traffic uses, then the others. The default. |
| `mesh` | `100.64.0.0/10` and Tailscale's IPv6 range `fd7a:115c:a1e0::/48`. |
| `private` | Private ranges that are not mesh addresses: `10/8`, `172.16/12`, `192.168/16` and IPv6 unique local addresses. |
| `public` | Any other global unicast address. |

A pin is strict. Only addresses of that kind are tried, whatever the traffic uses and whatever answered last, and if the peer has none the measurement fails like an unreachable peer would: the failure counters carry `reason="no_such_kind"` (probe, connect timing and throughput tests; the route trace counts one in `continuum_link_path_probe_failures_total`) and the pair says so under its name on the Sites screen (`no-address-of-kind`, below), instead of quietly measuring another path. The choice belongs to the pair, so both clusters apply it, including the route trace of the one that does not run the probe.

Use a pin when the path the traffic takes is not the one you want to know about: to watch a mesh while the applications talk over a private link or to keep a measurement from drifting between paths when the traffic uses several.

**No address ever appears in a series.** The table of addresses in use is not exported, no label carries an address or the kind a pair is pinned to, and the number of series per peer does not depend on any of it (a test counts them, see [Series per peer](#series-per-peer)). The pinned kind is part of the setting, not of the data.

**What the figures describe.** Delay, loss, jitter, the path's largest packet and the throughput describe the path that was measured, which is the one the traffic uses by default or the kind you pinned, and not necessarily every path between the two clusters. After a fall back they describe the address that answered, and a change of the busiest address changes the path being measured, which can show as a step in the figures or as a route change. If several paths matter, pin the pair to the one you care about.

## What exists today: passive link metrics

The first source is the node flow collector that was already there. Its eBPF programs read the kernel's own per-connection counters: round-trip time, retransmits, connection time and failed attempts. The agent labels each report row by the peer cluster its far end belongs to, and keeps running totals per cluster pair. No address, port or workload is ever a label of a series or kept on disk; the only trace of an address is a small table in the agent's memory of which addresses of a peer the traffic is seen using, for the active measurements (see [Which address is measured](#which-address-is-measured)). The number of series is fixed per peer, so it grows with the number of peers and with nothing that traffic or the network does: not with connections, addresses or nodes, ports, or the rates the UDP ramp steps through. The only settings that change what one peer costs are the ones that add a measurement: `dscp` classes and the stream counts of the throughput tests (about 26 series each, see [Series per peer](#series-per-peer)), and whether probing and the throughput tests are on at all.

The agent serves them at `/metrics` on port 9104 and the cluster collector scrapes that, on the infrastructure metrics pipeline that `telemetry.scope` never filters. They are switched on with `telemetry.clusterLinks.metrics.enabled`, which needs `flowObserver.enabled`; the chart refuses to render otherwise. Every series carries `cluster` (this cluster's id) and `peer` (the other cluster's id).

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_peer_info` | gauge | One per peer, with `peer_name` for display. |
| `continuum_link_connections_total` | counter | Connections observed with the peer. |
| `continuum_link_sent_bytes_total`, `continuum_link_received_bytes_total` | counter | Bytes this cluster sent to, and received from, the peer. |
| `continuum_link_segments_sent_total` | counter | TCP segments sent towards the peer, the denominator for the loss ratio. |
| `continuum_link_retransmits_total`, `continuum_link_rto_retransmits_total` | counter | Segments resent. The best passive sign of packet loss; the second counts those where nothing came back at all. |
| `continuum_link_receive_drops_total` | counter | Socket-level drops on this side: the application not draining, not the network. |
| `continuum_link_connection_failures_total` | counter | Failed connection attempts, by `reason` (`refused`, `timeout`, `reset`, `unreachable`, `other`). |
| `continuum_link_rtt_seconds` | histogram | Round-trip time. One sample per connection group per report window. There is no passive jitter series: the kernel's mean deviation of the round-trip time says less than the one-way probe's delay variation (`continuum_link_probe_delay_variation_seconds`, below) and cost about 15 series per peer. The time to establish a connection is not exported here: `continuum_link_tcp_connect_seconds` (below) measures the same thing, SYN to established, on a fixed schedule. |
| `continuum_link_last_observation_timestamp_seconds` | gauge | When traffic with the peer was last seen. |

The loss ratio to alert on is `rate(retransmits_total) / rate(segments_sent_total)`, which is the loss on the path this cluster sends over, not the reverse. A configured peer with no traffic reports zeros, and `last_observation_timestamp_seconds` says how long it has been quiet.

The limits are the limits of passive measurement. There is nothing to see on a link nobody is using, retransmits are a proxy for loss that also counts reordering and congestion, and the numbers describe TCP only.

## Active measurement: loss and delay in each direction

Traffic that is not there cannot be measured, and TCP retransmits only say that something was lost, not where. The second source sends a short stream of small test packets to each paired cluster and counts what arrives. It uses OWAMP (RFC 4656) from [perfSONAR](https://github.com/perfsonar/owamp), which measures one direction at a time: packets are numbered at the sender and timestamped at both ends, so loss and delay belong to the direction that caused them. It is switched on with `telemetry.clusterLinks.probe.enabled`, on top of the passive metrics.

For a pair, the first cluster (the one whose id sorts first) runs it, and the other only answers: see [Who measures what](#who-measures-what). **Both clusters need the probe installed**: the responder is deployed together with the prober by the same chart switch (`telemetry.clusterLinks.probe.enabled`), so a probe towards a cluster that has it switched off finds no responder and fails (`refused` or `timeout`), whichever cluster of the pair it is. If the first cluster has the switch off, nobody measures the pair, because the server does not know about the chart and keeps the second cluster waiting. Install it on both.

It runs in two workloads of its own, from the `link` image, which is the `continuum` image plus the two OWAMP programs built from pinned commits:

- **The prober**, one pod. It asks the agent which clusters this one is paired with (a signed request to the agent's link address, with a secret of its own, `continuum-agent-link-peers`, that the chart generates and mounts into the agent and the prober only; the node collectors' flow secret is never mounted into the prober, so a flaw in a measurement tool that parses a remote responder's answers cannot be used to forge flow reports. The signature is bound to the peers request, so a flow report's signature is not valid for it, nor the other way round. `telemetry.clusterLinks.probe.peersSecret` sets the value instead of generating one; it must differ from `flowObserver.secret`) and measures, one at a time, both directions of each pair it runs (pairs where it is the first cluster, or where the first cannot measure), every `interval` (a minute by default): `packets` test packets (100) about `spacing` apart (100 ms). It traces the route to every peer. It has no access to the Kubernetes API and serves its own `/metrics` on port 9105, which the cluster collector scrapes as a second job.
- **The responder**, a pod on every node in the host network, because the other clusters are given this cluster's node addresses and try them. It answers test requests and nothing else. It runs unprivileged on a port above 1024, with no capabilities and no API access.

Both are kept out of the agent's image and pod on purpose: the agent has no measurement tools, and a probe that misbehaves cannot take the agent down.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_probe_packets_sent_total`, `..._lost_total`, `..._duplicated_total` | counter | Test packets by `direction` (`forward` is from this cluster to the peer). `rate(lost) / rate(sent)` is the loss on that direction alone. |
| `continuum_link_probe_loss_ratio` | gauge | Loss in the latest run. |
| `continuum_link_probe_delay_variation_seconds` | gauge | How far the delay rose above its minimum (`stat` is `median` or `max`). |
| `continuum_link_probe_one_way_delay_seconds` | gauge | Absolute delay (`stat` is `min`, `median` or `max`). **Present only when both clocks were synchronised.** |
| `continuum_link_probe_clock_synchronized`, `..._clock_error_seconds` | gauge | Whether the delay above can be trusted, and the error bound the tool derived (present only when the clocks were synchronised). |
| `continuum_link_probe_path_hops` | gauge | Routers crossed by the test packets, from the TTL they arrived with, per `direction`. The only hop count exported; the path probe below does not export its own. |
| `continuum_link_probe_failures_total` | counter | Runs that could not complete, by `reason` (`unreachable`, `refused`, `timeout`, `auth`, `other`, and `no_such_kind` for a pair pinned to a kind of address the peer has none of). |
| `continuum_link_probe_up` | gauge | 1 if the latest run in that direction completed, 0 if it failed. After a failed run the per-run gauges above are absent rather than showing the last good run. |
| `continuum_link_probe_last_success_timestamp_seconds` | gauge | When a run in that direction last finished. |

Every series also carries `cluster`, `peer` and `method="owamp"`.

**Absolute one-way delay needs agreeing clocks.** The two ends timestamp with their own clocks, so a difference between them is added to the delay in one direction and taken off in the other. The tool reports whether both clocks were synchronised (`SYNC`) and the error it derived. Absolute delay is exported only when they were. The delay variation is always exported, because a constant clock offset cancels out of them, and so does the difference between the two directions' variation. Run chrony or ntpd on the nodes, with the kernel clock synchronised, if you want absolute delay.

**What has to be open between paired clusters.** TCP `responder.port` (8861) and UDP `responder.testPorts` (18900–18919: one port is used per running test, and a cluster only ever runs one test at a time, so twenty serve a mesh of ten clusters) to every node address of the peer cluster, in both directions. The prober tries a peer's addresses until one answers (those the traffic uses first, then the one that answered last; a [pinned pair](#which-address-is-measured) only those of its kind), at most five per run, and a peer that does not answer is skipped for a growing number of rounds (up to ten minutes) so it cannot hold up the others. Where the clusters are not on a network you control, set `responder.allowFrom` to the CIDRs the other clusters' nodes appear from; the responder then refuses everyone else at the control port. Without it the responder accepts anyone who reaches the port, within a bandwidth, session and disk limit, and it reports nothing about this cluster.

**The load.** With the defaults a link carries about ten packets a second for ten seconds, once a minute, in each direction, per pair: tens of kilobytes a minute, from one side. Before the work was split between the two clusters this was doubled (see the [cost per pair](#cost-per-pair)). Only one test runs at a time anywhere, so the load does not grow with the number of peers; it only takes longer to go round them. The prober refuses to start with settings that cannot fit in the interval (a run in each direction must complete inside it).

## Path MTU and route changes

A path whose MTU is smaller than the sender believes, with a router that does not say so, produces failures that look like loss: small packets pass and large ones vanish. The prober therefore also traces the route to each paired cluster every `pathInterval` (10 minutes by default; `"0"` turns it off), in the same place as the OWAMP tests and without any privilege: it sends UDP packets with the don't-fragment bit and a rising TTL, and reads the ICMP answers from the socket error queue, as `tracepath` does. It starts from the egress interface's MTU, not from the kernel's cached value, so a change or a black hole is not hidden by an earlier discovery. Both clusters trace towards the first address of [the order the measurements follow](#which-address-is-measured): the address the traffic with the peer is seen using, else the one that answered the OWAMP test, else the first address of the peer's list; with a pin, only addresses of the pinned kind. The first cluster's route is the one its test packets took as long as they were answered at that address; the other cluster of a pair makes no such test, so its trace is guaranteed to be the very node the first cluster measures only when the traffic uses one node. Both trace, because the two directions can take different routes. Each sends from a fixed source port per peer so that routers balancing by flow keep sending the probes the same way.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_path_mtu_bytes` | gauge | The largest packet that fits the whole way, as the routers reported it. Absent until learned. |
| `continuum_link_path_reached` | gauge | 1 if the last step was answered by the peer's address. A network that drops the answer shows 0 without being broken. |
| `continuum_link_path_mtu_blackhole` | gauge | 1 if small packets passed a router that silently dropped full-size ones. |
| `continuum_link_path_changes_total` | counter | Route changes. A new route counts only after two probes in a row saw it, so one odd probe is not a change. The route itself, and the routers' addresses, are not exported. |
| `continuum_link_path_probe_failures_total`, `..._last_success_timestamp_seconds` | counter, gauge | Probes that could not run, and when one last did. |

Limits: routers that do not send ICMP answers appear as silent hops and are left out of the route; load balancing per packet (rare) makes the route look unstable; a path whose ICMP is filtered shows an MTU only as far as it answers. IPv4 only for now: IPv6 peers are skipped without counting as failures, until it has been tested.

## Baseline: connect timing

One cheap measurement runs all the time towards every paired cluster that this cluster probes (for a pair, the first cluster; see [Who measures what](#who-measures-what)), so there is always a round-trip figure to compare the rest with, and one that works where ICMP is blocked.

Every `probe.tcpConnect.interval` (15s) the prober opens a TCP connection to the peer's responder port, times the handshake and closes it with a reset: one SYN and its answer, no data, nothing left in TIME_WAIT. The time is a round trip as the TCP stack sees it. A peer has many node addresses: the address the traffic uses is tried first, then the one that answered last and one more after it (see [Which address is measured](#which-address-is-measured)), so a single node being down does not count against the link. `probe.tcpConnect.interval: "0"` turns it off.

It needs the responder to be reachable, so on its own it cannot tell a dead responder from a dead link. The failure reason tells most of it: `refused` means the host is up and the responder is not, `timeout` means the host or the path is down. The one-way probe says the rest.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_tcp_connect_seconds` | histogram | Time to open the connection. |
| `continuum_link_tcp_connect_failures_total` | counter | Rounds in which no address answered, by `reason` (`refused`, `timeout`, `reset`, `unreachable`, `other`, `no_such_kind`). |
| `continuum_link_tcp_connect_last_success_timestamp_seconds` | gauge | When a connection last opened. Absent until the first time. |

There is no ICMP ping baseline. It was built and dropped: the connect timing gives the same round trip without a second program, a privilege-related sysctl or a second image build, and the one-way probe gives loss in each direction.

## Throughput tests: how much a link carries

The probes above send a trickle of packets, so they say whether a link is healthy but not how much it can carry or where it starts to drop. For that, the prober also runs [iperf3](https://github.com/esnet/iperf) against the other clusters, rarely and within limits. It is off by default: `telemetry.clusterLinks.probe.capacity.enabled`. Each responder pod then also runs an iperf3 server, on `capacity.port` (8862), and the prober, which already has the peer list, runs these tests for the pairs that [ask for them](#choices-per-pair), in this order:

- **TCP, one stream**, in each direction (`reverse: true`): what one connection reaches, the retransmits the test itself provoked, and the round-trip time under load. Compare the last with the idle round-trip time from the passive series: the difference is queueing delay (bufferbloat). The round-trip time is only available for the direction this cluster sends in.
- **TCP, several streams** (`parallel`, 4): what the link carries when one connection is not enough. A single stream far below it points at the window or at loss on a long path; the same figure means the link is the limit.
- **A UDP ramp**, in each direction: UDP at `udp.startRate`, doubled every `udp.step` until more than `udp.lossPercent` of the datagrams are lost or `udp.maxRate` is reached. The highest rate that stayed clean is the rate at which the link starts to drop, and the loss and jitter at that rung and at the first rung over the threshold are exported. If the ramp reached its top without loss, the link carries at least that.
- **Optionally** a TCP test that loads both directions at once (`bidir`), and the single-stream and UDP tests repeated for other DSCP classes (`dscp: [0, 46]`), to see whether the network treats traffic classes differently.

Figures are read where the data arrived, and the first seconds of each TCP test (`omit`) are left out, because they measure slow start.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_throughput_bits_per_second` | gauge | Latest result, by `direction`, `protocol` (`tcp`, or `tcp_bidir`), `streams` and `dscp`. |
| `continuum_link_throughput_retransmits` | gauge | TCP segments resent during that test. |
| `continuum_link_throughput_rtt_seconds` | gauge | Mean TCP round-trip time during that test. |
| `continuum_link_throughput_udp_loss_ratio`, `..._udp_jitter_seconds` | gauge | Loss and jitter at two points of the latest ramp, by `direction`, `dscp` and `point`: `clean` is the highest rung within the loss threshold (absent if even the first rung lost more), `first_loss` the first rung over it (absent if the ramp never exceeded it). Only these two rungs are exported, so the series do not depend on the configured rates. |
| `continuum_link_throughput_udp_point_bits_per_second` | gauge | The rate of each `point` above. |
| `continuum_link_throughput_udp_clean_bits_per_second` | gauge | The highest rate of the ramp within the loss threshold. Absent if even the lowest rate lost more. |
| `continuum_link_throughput_udp_saturated` | gauge | 1 if a higher rate lost more, so the clean rate is near what the link carries; 0 if the ramp ended at its top without loss. |
| `continuum_link_throughput_test_bytes_total` | counter | Bytes the tests put on the link, per direction: what they cost. |
| `continuum_link_throughput_test_failures_total` | counter | Tests that could not be run or finished, by `reason` (`unreachable`, `refused`, `timeout`, `auth`, `other`, `no_such_kind`). |
| `continuum_link_throughput_tests_skipped_total` | counter | Tests held back, by `reason` (`link_busy`, `budget`, `server_busy`). A skipped test is tried again later. |
| `continuum_link_throughput_budget_used_bytes`, `..._budget_bytes` | gauge | The daily byte budget and how much of it is used (no `peer` label: all peers share it). |
| `continuum_link_throughput_last_success_timestamp_seconds` | gauge | When a test with that peer last finished. |

**What keeps it safe to leave running.**

- *One test at a time, anywhere.* The prober holds a single turn for the link, shared by these tests and the one-way probe, so a throughput test never runs beside a probe (it would show up as loss and jitter). A pause between tests lets the probe take its turn. A peer's iperf3 server also serves one client at a time; if every node of the peer is busy, the test waits one to three minutes and is counted as skipped, not as a failure.
- *A byte budget.* `budget` (10Gi) is the most the tests may carry in 24 hours, all peers together, counted over a sliding window. Before a test, what it carried last time (or, the first time, a guess from `tcpMaxRate`, or 1 Gbit/s) must fit what is left; if not it waits. A test larger than the whole budget never runs, and the prober's log says so once; set `tcpMaxRate` or raise the budget. The count is in the prober's memory, so a restart starts the window again.
- *Waiting for real traffic.* The agent already counts the bytes it observes with each peer, and the prober reads those totals once a minute. A test is held back for ten minutes while the traffic in the direction it would load is above `busyFraction` (30%) of what the last TCP test reached there, or above `skipAbove` before there is a first measurement. This sees the traffic the flow observer sees, which is the platform's own cluster-to-cluster traffic, not everything on a shared line.
- *Time of day.* `window: "02:00-06:00"` (UTC) restricts the tests to those hours; a window may cross midnight.
- *Slow.* Each peer goes through its whole set once per `interval` (12 hours), and the first run is a random time within the first minutes after a peer appears.

**Cost.** TCP tests are as fast as the link allows, which is the point. On a 1 Gbit/s link a 10-second test carries about 1.5 GB, and the default set (single and four streams, both directions, the UDP ramp) about 6.5 GB per peer per run. That is more than the default budget for two peers twice a day: the budget then spreads the tests out over time and the skipped counter shows it. Raise `interval`, cap each stream with `tcpMaxRate`, or raise the budget to suit the link and what you pay for it.

**Who may use the server.** iperf3 has no allow-list, so `responder.allowFrom` does not apply to it. Anyone who can reach `capacity.port` could otherwise run a test against your nodes, one at a time, which is a free way to use your bandwidth and slow the node down. The server therefore runs only in iperf3's authenticated mode, and by default you do nothing for that:

- **Automatic (default, `capacity.auth.automatic: true`).** The Ikhnos server makes the credentials when a pair turns throughput tests on, and delivers them in the configuration the agents already receive over their mutually authenticated connection. For each cluster that serves tests it holds one RSA-2048 key pair; for each pair it holds a user name and a random password (32 random bytes, hex). The cluster that runs the tests gets the serving cluster's *public* key, the user name and the password. The serving cluster gets its own *private* key and the user lines (`USER,SHA256`, the hash of `{USER}PASSWORD`) of the pairs it serves. A third cluster gets nothing. Nothing is created or named in any cluster: the chart generates one more Secret, `continuum-agent-link-iperf`, which is only a request key (below), not a credential. The server keeps its copies sealed in its database (AES-256-GCM under a key derived from the server's CA key), and removes them when the pair is removed, when the pair's throughput tests are switched off, or when no pair needs them; switching the tests on again makes new ones.
- **Your own (`capacity.auth.existingSecret`).** If you name a Secret it wins, and everything works as it always did: the same Secret in every cluster, mounted as before, nothing fetched from the agent, the server issues and sends nothing for it. Create it like this:

```sh
openssl genrsa -out private.pem 2048
openssl rsa -in private.pem -pubout -out public.pem
echo -n 'a-long-random-password' > password
echo -n 'link' > username
echo "link,$(echo -n "{link}$(cat password)" | sha256sum | cut -d' ' -f1)" > users.csv
kubectl -n continuum-system create secret generic iperf3-auth \
  --from-file=private.pem --from-file=public.pem --from-file=users.csv --from-file=username --from-file=password
```

  The servers mount only `private.pem` and `users.csv`, and the prober only `public.pem` and `password` (the user name is an environment variable), so the prober never holds the private key. To rotate, change the Secret in every cluster and restart the responder and the prober.
- **Open (`capacity.auth.automatic: false` and no Secret).** The server accepts anyone who can reach the port, `allowFrom` combined with the tests is refused, and the chart's notes warn. Use it only where a firewall already restricts the port.

A client without the credentials is refused at the start of the test, and shows as an `auth` failure on the other side. `capacity.server.bitrateLimit` makes a server end any test arriving faster than the limit, which also ends your own tests if you set it below what they reach.

*How the automatic credentials travel inside a cluster.* The agent holds them in memory only: never in a file, a Secret or a log. Two pods ask it, each with a signed request (the same scheme as the peer list: a timestamp window, a MAC bound to a context, and a replay cache), and the answer is sealed (AES-256-GCM, bound to the request's signature) because the agent's link address is plain HTTP inside the cluster.

- The prober asks for its peers as before and, with the peers, gets the client credentials of the pairs it tests. It holds the peers secret only. It never receives a private key.
- The responder's iperf3 pod runs `continuum link-iperf-server`, which asks `POST /v1/link/iperf-server` with the request secret and a MAC context of its own (mounted into the agent and the responder pods only; not the prober's secret, not the flow secret; the chart refuses a `requestSecret` equal to either). It writes the key and the users to a private in-memory volume, runs `iperf3 -s` with them, asks again every minute (backing off after a failure) and restarts iperf3 only when they changed. It fails closed: with no credentials, or ones it cannot write, no server runs at all, and the other cluster's prober reports `auth` or `unreachable` like for any server that is down. If the agent is briefly unreachable it keeps serving with what it has.

*Rotating.* `POST /api/v1/orgs/{org}/links/rotate-credentials` (admin) with `{"cluster": "<id>"}` makes a serving cluster's key pair again, or with `{"a": "<id>", "b": "<id>"}` makes a pair's user name and password again. The new credentials reach the clusters in their next configuration (within a minute), the responders restart iperf3 on their own, and nothing is done in any cluster. For a short while the pair's tests can fail with `auth`, until both ends have the new ones. The audit trail records which cluster or pair, never a credential. Turning a pair's throughput tests off and on again also makes new credentials. Changing the server's CA key makes the stored credentials unreadable, so they are made again.

*Who can read what.*

| Where | What it holds | Notes |
| --- | --- | --- |
| The Ikhnos server's database | every key and password, sealed | readable only with the server's CA key, which is on the server's disk (encrypted with a passphrase if one is configured). A copy of the database alone is not enough |
| An agent's memory | its own cluster's credentials only | not written anywhere |
| The prober | the peers' public keys, and the user names and passwords of the pairs it tests | no private key. Passwords go to iperf3 in its environment, public keys to a memory volume |
| The responder's iperf3 pod | its cluster's private key and the user lines (password hashes) of its pairs, in memory | and the request secret, so a compromised responder can ask again for these, and only these |
| Another cluster | nothing | |
| Logs, metrics, `/links/latest`, other API answers, audit rows, diagnostic reports | no credential | they are redacted in formatting and tests search for them |

*What this does not protect against.* Whoever controls a cluster's prober can run tests as that cluster against the peers it tests (that is the cluster's job anyway). Whoever controls a responder pod gets its cluster's private key and the password hashes (`SHA256` of `{USER}PASSWORD`; with 32 random bytes they cannot be reversed) of the pairs it serves. The key lets them pose as that cluster's iperf3 server, and iperf3 hands the server the user name and password of every client that authenticates, so they learn the passwords of those pairs from then on, as any compromised server would; they get nothing of any other cluster's pairs, and no other cluster's private key. The credentials sit in agent memory, so anyone who can read the agent's memory (a debugger, a core dump) has them. The server's CA key and database together expose everything; with no CA passphrase the key is a plain 0600 file. The request secrets live in Kubernetes Secrets in the agent's namespace, like the peers secret. An older Ikhnos server does not issue credentials, so with the automatic mode the tests simply wait (and fail closed) until the server is updated; use `existingSecret` in the meantime.

**What has to be open.** TCP and UDP `capacity.port` to every node address of the peer cluster, in both directions, besides the ports of the one-way probe. The tests try the node address of the peer the traffic uses, then the one that answered last time, and then the others, five at most, or only addresses of the kind the pair is pinned to.

**What it cannot tell you.** A UDP ramp measures what gets through at a given rate, not why: loss at a rate can be the link, a policer, or the node's own receive path. The multi-stream figure is not "the" capacity if the other end's CPU is the limit. And a link shared with traffic this cluster does not observe can be busy without the wait noticing it.

## Series per peer

What one peer costs in the metrics store, counted by rendering a meter for one fully active peer with the default settings (traffic seen, both one-way directions, the route trace, connect timing, and the throughput tests with TCP at one and four streams in both directions, the UDP ramp and a budget of its own): **about 140 series** on the pair's first cluster, of which about 30 are the passive ones the agent serves and about 110 the active ones the prober serves. A peer that is only observed passively costs about 30; a peer that is probed but not tested, about 97 including those; the throughput tests add about 40 (two of them are the cluster-wide budget gauges, which are not per peer but are rendered with the peer by the test). On the pair's other cluster a peer costs about 35: the passive series and the route trace. More than half of the passive count is the round-trip histogram's buckets (15 series); the connect timing's histogram adds 15 more on the first cluster.

Every further `dscp` class in the throughput tests adds about 26 series (the TCP readings at both stream counts and the UDP ramp, in both directions); nothing else a user can set changes the count: not the pair's `via`, and not the addresses the traffic uses (the failure reason `no_such_kind` costs four series, one for each probe direction, the connect timing and the throughput tests, whether or not a pair is pinned). The numbers are bounded by a test (`TestOnePeerCostsABoundedNumberOfSeriesWhateverTheRatesAndAddresses` in `backend/internal/link`), which fails if a label starts to follow the data or a series is added without these figures being revisited.

### Cost per pair

What a whole pair costs, as it is measured now (the first cluster probes and times connections, the other serves) against what it cost when both clusters probed. Defaults: 100 packets per direction, one run a minute, one connection timed every 15 seconds, one route trace every 10 minutes.

| | Both clusters probed (before) | First cluster only (now) |
| --- | --- | --- |
| One-way probe packets per minute, on the link | about 400 (each cluster sent 100 in each direction) | about 200 (100 in each direction) |
| Connect timing, handshakes per minute | 8 (4 from each cluster) | 4 |
| Route trace | both clusters, every 10 minutes | unchanged: both clusters |
| Active series of the pair, probed and not tested | about 135 (68 on each cluster) | about 75 (68 on the first cluster, 6 route-trace series on the other) |
| Active series of the pair, with throughput tests | about 180 (the tests ran from one side) | about 110 |
| All series of the pair, probed and not tested | about 225 (113 on each cluster) | about 130 (97 on the first cluster, 35 on the other) |
| All series of the pair, with throughput tests | about 265 (153 + 113) | about 175 (139 + 35) |
| Series a consumer must de-duplicate | the one-way probe and the connect timing of every pair, by cluster | none: read `cluster=<first id>` |

Both changes are in the "now" column: the split between the two clusters, and the passive jitter histogram that was dropped (about 15 series per cluster, included in the "before" figures). The packet counts are test packets only; the control connection of each OWAMP run and the route trace are small beside them.

## In FUSION

### Through the data API

The link series reach FUSION like any other metrics, and the figures the Sites screen shows are also available to other systems through FUSION's data API: `GET /api/v1/fusion/links` returns, for each watched pair, the latest loss in each direction, round trip, delay variation, MTU, throughput and times, once per pair (`a` before `b`, with `measuredBy` saying which cluster's series were used), with the same 15-second shared read as the screen. A token needs the `metrics` signal, sees only the pairs whose two clusters it may read, and is refused if it is limited to namespaces; see [the FUSION API](./fusion-api.md#cluster-links) for the shape and an example. The `continuum_link_*` series are in the `system` category (infrastructure between clusters, not something an application reports), so `category=application` reads and the application dashboards leave them out and `category=system` includes them.

### In Grafana

The bundled FUSION chart provisions a **Cluster links** dashboard in Grafana (folder Ikhnos, reachable from the menu on every dashboard of the folder), built from the series above. It has a **Cluster** picker (the cluster that measured) and a **Peer** picker that follows it, both listing clusters by name, and every panel's legend reads "cluster → peer" by name. The link series carry only cluster ids, which do not change, so the names come from a series the server writes into FUSION's Prometheus once a minute, `ikhnos_cluster_info{cluster, cluster_name}`, one per cluster Ikhnos knows (the same way as the application series; a renamed cluster shows its new name within a minute). The dashboard joins it by the id. A cluster the server has no name for is shown by its id rather than left out, and so is a pair when either of its clusters has none. A table at the top gives each pair's names and ids side by side. Your own queries can do the same: `continuum_link_probe_loss_ratio * on (cluster) group_left (cluster_name) ikhnos_cluster_info`. Its rows are Overview (pairs probed, worst probe loss now, probes failing now, tests skipped, byte budget used), Loss and delay (probe loss and delay variation in each direction, absolute one-way delay where the clocks agree and a timeline of which runs they did, whether each probe is up and how long since it last completed), Round trip (connect time mean and percentiles, the round trip of real connections, connect failures), Traffic (bytes, connections, retransmit ratio, failed connections), Path (MTU, black hole, route changes, hops) and Capacity (throughput by direction and stream count, the UDP clean rate and whether it saturated, the round trip under load next to the idle one, the byte budget, tests skipped and failed by reason). Loss turns amber from 1 % and red from 5 %, as on the Sites screen.

The series of one pair come from different clusters, and the dashboard draws each once: the one-way probe, the connect timing and the throughput tests run from one cluster of the pair and carry that cluster as `cluster`, while the passive series (traffic, round trip of real connections) and the route trace come from both, so those panels show one line per measuring cluster, labelled `cluster → peer`. A figure FUSION does not hold is shown as no data, never as zero: a probe run that failed has no loss figure, and "probes failing" is empty rather than zero when no probe is reporting. Absolute one-way delay is empty until the clocks are synchronised. How Grafana is set up in FUSION is in [FUSION's Grafana](./fusion-grafana.md).

## What is planned

- **Choosing who measures from what the clusters report.** The server now learns whether the first cluster of a pair has probing and telemetry switched on, but still gives the probe to the second cluster only on the conditions in [Who measures what](#who-measures-what). Using the report there would let the second cluster probe while the first has probing off, instead of only saying so.

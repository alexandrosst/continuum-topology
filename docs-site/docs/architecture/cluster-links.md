---
id: cluster-links
title: Cluster links
description: How the quality of the connection between two clusters is measured and exported — which pairs are watched, what each source can and cannot tell you, and what has been built so far.
---

# Cluster links

A link is the connection between two onboarded clusters. Knowing it matters to placement and to troubleshooting: an application that calls across a link inherits its loss, delay and jitter, and nothing else in the system sees those directly. The measurements are exported as Prometheus-style metrics through the same collector pipeline as every other [telemetry signal](./telemetry-intent.md), so they follow the same destination, scope and provenance rules and arrive in FUSION like the rest.

No single tool covers a link. Real traffic shows what applications actually experience but says nothing about an idle link. A constant probe shows an idle link but travels a path of its own, and a ping is blocked by many networks. A throughput test shows capacity but loads the link. So the design combines sources, and gives each one a job that the others cannot do.

## Which pairs are measured

Only pairs an administrator turns on. The setting is `links` in the organisation settings, a list of cluster pairs; a pair is measured in both directions and by both clusters, and a pair that is not listed is not measured at all. Nothing is probed between every pair automatically, because the cost grows with the square of the number of clusters, and the throughput test spends real traffic.

The server is the only place that knows which address belongs to which cluster, so it tells each agent about its peers in the `Config` it already pushes: the peer's id, its name for display, and the addresses its traffic comes from or goes to (its nodes' addresses, including overlay and tunnel addresses, and the address its agent connects from). A cluster only ever learns the addresses of the clusters it was paired with. An address the two clusters share, such as two clusters on the same private range, is left out rather than mislabelled, and an address claimed by two different peers is dropped by the agent. Peers are sent only at access tier 2, because the measurements are derived from observed traffic, and are withdrawn while the flow collector is paused for that agent. The list is re-sent when settings change and at least once a minute, so a node added to a peer cluster reaches its partners without anyone doing anything.

## Choices per pair

Each entry of `links` can carry choices of its own, the same for both clusters of the pair:

```json
{"a": "cl-eu", "b": "cl-us", "noProbe": false, "throughput": {"intervalMinutes": 720, "budgetMB": 4096}}
```

- `noProbe: true` turns the one-way probe and the route trace off for the pair. The passive metrics stay, and the pair has no active series at all (not counters that read zero).
- `throughput` asks for the [throughput tests](#throughput-tests-how-much-a-link-carries) on the pair. Without it the pair gets none, even where the cluster has them enabled in the chart: the chart says what a cluster *can* do, the pair says what it *should*, because those tests load the link. Only one cluster of the pair runs them, in both directions: the one whose id sorts first, so the link is not loaded twice. The other cluster only serves them, so it needs the iperf3 server (the same chart setting) but runs no tests, and has no throughput series for the pair; the results are in the first cluster's metrics, labelled with the pair.
- `throughput.intervalMinutes` (10 minutes to 7 days) replaces the cluster's interval between two sets of tests for this pair, and `throughput.budgetMB` (up to 1 TiB) is the most the tests may carry on this link in a day, in addition to the cluster's own budget: a test has to fit both. The pair's use and limit are exported as `continuum_link_throughput_pair_budget_used_bytes` and `..._pair_budget_bytes`.

The agent holds each value to the same bounds as the server, whatever a Config says. The options are part of the settings and are saved with them.

### Turning a cluster on

In the telemetry wizard, **Cluster links** (infrastructure, metrics) makes a cluster able to take part. Ticking it adds `telemetry.clusterLinks.metrics.enabled=true` and also `flowObserver.enabled=true`, because the link metrics are labelled from what the flow collectors see (access level 2). Two choices appear under it:

- **Probing** (on by default) starts the prober and the responder on every node.
- **Throughput tests** (off by default, and only with probing) adds the iperf3 server and client. The wizard asks for the name of the Secret that holds iperf3's credentials and refuses to continue without one, because iperf3 accepts anyone who can reach its port otherwise. The Secret must be the same in every cluster of the mesh.

Ticking the signal does not measure anything by itself: no pair exists until an administrator adds one under Sites → Cluster links. On a cluster that already has the signal, the wizard leaves probing and throughput as they are until you change them there, since agents do not report them. Unticking the signal also states the probe and throughput switches off, because the chart refuses a prober without the metrics switch.

### In the app

An administrator manages the pairs on the Sites page, in the **Cluster links** section under Measured paths; everyone else sees the list. **Add pair** takes two clusters that each have an approved agent and starts with probing on and throughput off, so nothing heavy runs by surprise. A pair's edit panel switches probing and throughput on or off and offers three named amounts for the tests: Light (every day, 50 MB a day), Standard (every 6 hours, 500 MB) and Heavy (every hour, 5 GB), or Custom numbers within the bounds above. It also lists what each cluster needs (a connected agent, access level 2, the flow collector running) and says which cluster runs the tests; a missing requirement is a warning, not a block, because the settings apply when the cluster catches up.

## What exists today: passive link metrics

The first source is the node flow collector that was already there. Its eBPF programs read the kernel's own per-connection counters: round-trip time, jitter, retransmits, connection time and failed attempts. The agent labels each report row by the peer cluster its far end belongs to, and keeps running totals per cluster pair. Nothing about addresses, ports or workloads is kept, so the number of series depends only on how many peers are configured.

The agent serves them at `/metrics` on port 9104 and the cluster collector scrapes that, on the infrastructure metrics pipeline that `telemetry.scope` never filters. They are switched on with `telemetry.clusterLinks.metrics.enabled`, which needs `flowObserver.enabled`; the chart refuses to render otherwise. Every series carries `cluster` (this cluster's id) and `peer` (the other cluster's id).

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_peer_info` | gauge | One per peer, with `peer_name` for display. |
| `continuum_link_connections_total`, `continuum_link_flows_total` | counter | Connections observed with the peer. |
| `continuum_link_sent_bytes_total`, `continuum_link_received_bytes_total` | counter | Bytes this cluster sent to, and received from, the peer. |
| `continuum_link_segments_sent_total` | counter | TCP segments sent towards the peer, the denominator for the loss ratio. |
| `continuum_link_retransmits_total`, `continuum_link_rto_retransmits_total` | counter | Segments resent. The best passive sign of packet loss; the second counts those where nothing came back at all. |
| `continuum_link_receive_drops_total` | counter | Socket-level drops on this side: the application not draining, not the network. |
| `continuum_link_connection_failures_total` | counter | Failed connection attempts, by `reason` (`refused`, `timeout`, `reset`, `unreachable`, `other`). |
| `continuum_link_rtt_seconds`, `continuum_link_jitter_seconds`, `continuum_link_handshake_seconds` | histogram | Round-trip time, its mean deviation, and time to establish a connection. One sample per connection group per report window. |
| `continuum_link_last_observation_timestamp_seconds` | gauge | When traffic with the peer was last seen. |

The loss ratio to alert on is `rate(retransmits_total) / rate(segments_sent_total)`, which is the loss on the path this cluster sends over, not the reverse. A configured peer with no traffic reports zeros, and `last_observation_timestamp_seconds` says how long it has been quiet.

The limits are the limits of passive measurement. There is nothing to see on a link nobody is using, retransmits are a proxy for loss that also counts reordering and congestion, and the numbers describe TCP only.

## Active measurement: loss and delay in each direction

Traffic that is not there cannot be measured, and TCP retransmits only say that something was lost, not where. The second source sends a short stream of small test packets to each paired cluster and counts what arrives. It uses OWAMP (RFC 4656) from [perfSONAR](https://github.com/perfsonar/owamp), which measures one direction at a time: packets are numbered at the sender and timestamped at both ends, so loss and delay belong to the direction that caused them. It is switched on with `telemetry.clusterLinks.probe.enabled`, on top of the passive metrics.

It runs in two workloads of its own, from the `link` image, which is the `continuum` image plus the two OWAMP programs built from pinned commits:

- **The prober**, one pod. It asks the agent which clusters this one is paired with (a signed request to the agent's link address, with a secret of its own, `continuum-agent-link-peers`, that the chart generates and mounts into the agent and the prober only; the node collectors' flow secret is never mounted into the prober, so a flaw in a measurement tool that parses a remote responder's answers cannot be used to forge flow reports. The signature is bound to the peers request, so a flow report's signature is not valid for it, nor the other way round. `telemetry.clusterLinks.probe.peersSecret` sets the value instead of generating one; it must differ from `flowObserver.secret`) and measures them one at a time, both directions, every `interval` (a minute by default): `packets` test packets (100) about `spacing` apart (100 ms). It has no access to the Kubernetes API and serves its own `/metrics` on port 9105, which the cluster collector scrapes as a second job.
- **The responder**, a pod on every node in the host network, because the other clusters are given this cluster's node addresses and try them. It answers test requests and nothing else. It runs unprivileged on a port above 1024, with no capabilities and no API access.

Both are kept out of the agent's image and pod on purpose: the agent has no measurement tools, and a probe that misbehaves cannot take the agent down.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_probe_packets_sent_total`, `..._lost_total`, `..._duplicated_total` | counter | Test packets by `direction` (`forward` is from this cluster to the peer). `rate(lost) / rate(sent)` is the loss on that direction alone. |
| `continuum_link_probe_loss_ratio` | gauge | Loss in the latest run. |
| `continuum_link_probe_delay_variation_seconds` | gauge | How far the delay rose above its minimum (`stat` is `median` or `max`). |
| `continuum_link_probe_one_way_delay_seconds` | gauge | Absolute delay (`stat` is `min`, `median` or `max`). **Present only when both clocks were synchronised.** |
| `continuum_link_probe_clock_synchronized`, `..._clock_error_seconds` | gauge | Whether the delay above can be trusted, and the error bound the tool derived (present only when the clocks were synchronised). |
| `continuum_link_probe_path_hops` | gauge | Routers crossed, from the TTL the packets arrived with. |
| `continuum_link_probe_failures_total` | counter | Runs that could not complete, by `reason` (`unreachable`, `refused`, `timeout`, `auth`, `other`). |
| `continuum_link_probe_up` | gauge | 1 if the latest run in that direction completed, 0 if it failed. After a failed run the per-run gauges above are absent rather than showing the last good run. |
| `continuum_link_probe_last_success_timestamp_seconds` | gauge | When a run in that direction last finished. |

Every series also carries `cluster`, `peer` and `method="owamp"`.

**Absolute one-way delay needs agreeing clocks.** The two ends timestamp with their own clocks, so a difference between them is added to the delay in one direction and taken off in the other. The tool reports whether both clocks were synchronised (`SYNC`) and the error it derived. Absolute delay is exported only when they were. The delay variation is always exported, because a constant clock offset cancels out of them, and so does the difference between the two directions' variation. Run chrony or ntpd on the nodes, with the kernel clock synchronised, if you want absolute delay.

**What has to be open between paired clusters.** TCP `responder.port` (8861) and UDP `responder.testPorts` (18900–18999) to every node address of the peer cluster, in both directions. The prober tries a peer's addresses until one answers (the one that answered last first), at most five per run, and a peer that does not answer is skipped for a growing number of rounds (up to ten minutes) so it cannot hold up the others. Where the clusters are not on a network you control, set `responder.allowFrom` to the CIDRs the other clusters' nodes appear from; the responder then refuses everyone else at the control port. Without it the responder accepts anyone who reaches the port, within a bandwidth, session and disk limit, and it reports nothing about this cluster.

**The load.** With the defaults a link carries about ten packets a second for ten seconds, once a minute, in each direction, per paired cluster: tens of kilobytes a minute. Only one test runs at a time anywhere, so the load does not grow with the number of peers; it only takes longer to go round them. The prober refuses to start with settings that cannot fit in the interval (a run in each direction must complete inside it).

## Path MTU, hops and route changes

A path whose MTU is smaller than the sender believes, with a router that does not say so, produces failures that look like loss: small packets pass and large ones vanish. The prober therefore also traces the route to each paired cluster every `pathInterval` (10 minutes by default; `"0"` turns it off), in the same place as the OWAMP tests and without any privilege: it sends UDP packets with the don't-fragment bit and a rising TTL, and reads the ICMP answers from the socket error queue, as `tracepath` does. It starts from the egress interface's MTU, not from the kernel's cached value, so a change or a black hole is not hidden by an earlier discovery. It traces towards the address that answered the OWAMP test, so the route described is the one the test packets took, and it sends from a fixed source port per peer so that routers balancing by flow keep sending the probes the same way.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_path_mtu_bytes` | gauge | The largest packet that fits the whole way, as the routers reported it. Absent until learned. |
| `continuum_link_path_hops` | gauge | Routers crossed, when the probe reached the peer. |
| `continuum_link_path_reached` | gauge | 1 if the last step was answered by the peer's address. A network that drops the answer shows 0 without being broken. |
| `continuum_link_path_mtu_blackhole` | gauge | 1 if small packets passed a router that silently dropped full-size ones. |
| `continuum_link_path_changes_total` | counter | Route changes. A new route counts only after two probes in a row saw it, so one odd probe is not a change. The route itself, and the routers' addresses, are not exported. |
| `continuum_link_path_probe_failures_total`, `..._last_success_timestamp_seconds` | counter, gauge | Probes that could not run, and when one last did. |

Limits: routers that do not send ICMP answers appear as silent hops and are left out of the route; load balancing per packet (rare) makes the route look unstable; a path whose ICMP is filtered shows a hop count and MTU only as far as it answers. IPv4 only for now: IPv6 peers are skipped without counting as failures, until it has been tested.

## Baseline: connect timing

One cheap measurement runs all the time towards every paired cluster that is probed, so there is always a round-trip figure to compare the rest with, and one that works where ICMP is blocked.

Every `probe.tcpConnect.interval` (15s) the prober opens a TCP connection to the peer's responder port, times the handshake and closes it with a reset: one SYN and its answer, no data, nothing left in TIME_WAIT. The time is a round trip as the TCP stack sees it. A peer has many node addresses: the one that answered last is tried first and one more after it, so a single node being down does not count against the link. `probe.tcpConnect.interval: "0"` turns it off.

It needs the responder to be reachable, so on its own it cannot tell a dead responder from a dead link. The failure reason tells most of it: `refused` means the host is up and the responder is not, `timeout` means the host or the path is down. The one-way probe says the rest.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_tcp_connect_seconds` | histogram | Time to open the connection. |
| `continuum_link_tcp_connect_failures_total` | counter | Rounds in which no address answered, by `reason` (`refused`, `timeout`, `reset`, `unreachable`, `other`). |
| `continuum_link_tcp_connect_last_success_timestamp_seconds` | gauge | When a connection last opened. Absent until the first time. |

There is no ICMP ping baseline. It was built and dropped: the connect timing gives the same round trip without a second program, a privilege-related sysctl or a second image build, and the one-way probe gives loss in each direction.

## Throughput tests: how much a link carries

The probes above send a trickle of packets, so they say whether a link is healthy but not how much it can carry or where it starts to drop. For that, the prober also runs [iperf3](https://github.com/esnet/iperf) against the other clusters, rarely and within limits. It is off by default: `telemetry.clusterLinks.probe.capacity.enabled`. Each responder pod then also runs an iperf3 server, on `capacity.port` (8862), and the prober, which already has the peer list, runs these tests for the pairs that [ask for them](#choices-per-pair), in this order:

- **TCP, one stream**, in each direction (`reverse: true`): what one connection reaches, the retransmits the test itself provoked, and the round-trip time under load. Compare the last with the idle round-trip time from the passive series: the difference is queueing delay (bufferbloat). The round-trip time is only available for the direction this cluster sends in.
- **TCP, several streams** (`parallel`, 4): what the link carries when one connection is not enough. A single stream far below it points at the window or at loss on a long path; the same figure means the link is the limit.
- **A UDP ramp**, in each direction: UDP at `udp.startRate`, doubled every `udp.step` until more than `udp.lossPercent` of the datagrams are lost or `udp.maxRate` is reached. The highest rate that stayed clean is the rate at which the link starts to drop, and each rung's loss and jitter are exported. If the ramp reached its top without loss, the link carries at least that.
- **Optionally** a TCP test that loads both directions at once (`bidir`), and the single-stream and UDP tests repeated for other DSCP classes (`dscp: [0, 46]`), to see whether the network treats traffic classes differently.

Figures are read where the data arrived, and the first seconds of each TCP test (`omit`) are left out, because they measure slow start.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_throughput_bits_per_second` | gauge | Latest result, by `direction`, `protocol` (`tcp`, or `tcp_bidir`), `streams` and `dscp`. |
| `continuum_link_throughput_retransmits` | gauge | TCP segments resent during that test. |
| `continuum_link_throughput_rtt_seconds` | gauge | Mean TCP round-trip time during that test. |
| `continuum_link_throughput_udp_loss_ratio`, `..._udp_jitter_seconds` | gauge | Loss and jitter at each `rate_bits_per_second` of the latest ramp, by `direction` and `dscp`. |
| `continuum_link_throughput_udp_clean_bits_per_second` | gauge | The highest rate of the ramp within the loss threshold. Absent if even the lowest rate lost more. |
| `continuum_link_throughput_udp_saturated` | gauge | 1 if a higher rate lost more, so the clean rate is near what the link carries; 0 if the ramp ended at its top without loss. |
| `continuum_link_throughput_test_bytes_total` | counter | Bytes the tests put on the link, per direction: what they cost. |
| `continuum_link_throughput_test_failures_total` | counter | Tests that could not be run or finished, by `reason` (`unreachable`, `refused`, `timeout`, `auth`, `other`). |
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

**Who may use the server.** iperf3 has no allow-list, so `responder.allowFrom` does not apply to it (the chart refuses `allowFrom` with the tests on unless authentication is set). Anyone who can reach `capacity.port` can run a test against your nodes, one at a time, which is a free way to use your bandwidth and slow the node down. Close the port to everything but the peer clusters, or use iperf3's authenticated mode: create one Secret, the same in every cluster of the mesh, and name it in `capacity.auth.existingSecret`:

```sh
openssl genrsa -out private.pem 2048
openssl rsa -in private.pem -pubout -out public.pem
echo -n 'a-long-random-password' > password
echo -n 'link' > username
echo "link,$(echo -n "{link}$(cat password)" | sha256sum | cut -d' ' -f1)" > users.csv
kubectl -n continuum-system create secret generic iperf3-auth \
  --from-file=private.pem --from-file=public.pem --from-file=users.csv --from-file=username --from-file=password
```

The servers mount only `private.pem` and `users.csv`, and the prober only `public.pem` and `password` (the user name is an environment variable), so the prober never holds the private key. A client without the credentials is refused at the start of the test, and shows as an `auth` failure on the other side. `capacity.server.bitrateLimit` makes a server end any test arriving faster than the limit, which also ends your own tests if you set it below what they reach.

**What has to be open.** TCP and UDP `capacity.port` to every node address of the peer cluster, in both directions, besides the ports of the one-way probe. The tests try the node address of the peer that answered last time, and then the others, five at most.

**What it cannot tell you.** A UDP ramp measures what gets through at a given rate, not why: loss at a rate can be the link, a policer, or the node's own receive path. The multi-stream figure is not "the" capacity if the other end's CPU is the limit. And a link shared with traffic this cluster does not observe can be busy without the wait noticing it.

## What is planned

- **Latest results on the screen.** The Sites screen sets the per-pair choices; showing the latest figures next to them needs a small endpoint that reads them from the telemetry backend.
- **What each cluster can do.** Agents reporting whether the responder and the iperf3 server are in place, so the screen can say "this cluster has no throughput server" rather than leave the user to find out.

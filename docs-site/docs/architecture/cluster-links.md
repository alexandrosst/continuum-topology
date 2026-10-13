---
id: cluster-links
title: Cluster links
description: How the quality of the connection between two clusters is measured and exported — which pairs are watched, what each source can and cannot tell you, and what has been built so far.
---

# Cluster links

A link is the connection between two onboarded clusters. Knowing it matters to placement and to troubleshooting: an application that calls across a link inherits its loss, delay and jitter, and nothing else in the system sees those directly. The measurements are exported as Prometheus-style metrics through the same collector pipeline as every other [telemetry signal](./telemetry-intent.md), so they follow the same destination, scope and provenance rules and arrive in FUSION like the rest.

No single tool covers a link. Real traffic shows what applications actually experience but says nothing about an idle link. A constant ping shows an idle link but travels a path of its own and is blocked by many networks. A throughput test shows capacity but loads the link. So the design combines sources, and gives each one a job that the others cannot do.

## Which pairs are measured

Only pairs an administrator turns on. The setting is `links` in the organisation settings, a list of cluster pairs; a pair is measured in both directions and by both clusters, and a pair that is not listed is not measured at all. Nothing is probed between every pair automatically, because the cost grows with the square of the number of clusters, and the throughput test spends real traffic.

The server is the only place that knows which address belongs to which cluster, so it tells each agent about its peers in the `Config` it already pushes: the peer's id, its name for display, and the addresses its traffic comes from or goes to (its nodes' addresses, including overlay and tunnel addresses, and the address its agent connects from). A cluster only ever learns the addresses of the clusters it was paired with. An address the two clusters share, such as two clusters on the same private range, is left out rather than mislabelled, and an address claimed by two different peers is dropped by the agent. Peers are sent only at access tier 2, because the measurements are derived from observed traffic, and are withdrawn while the flow collector is paused for that agent. The list is re-sent when settings change and at least once a minute, so a node added to a peer cluster reaches its partners without anyone doing anything.

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

- **The prober**, one pod. It asks the agent which clusters this one is paired with (a signed request to the agent's link address, with the same secret the node collectors use) and measures them one at a time, both directions, every `interval` (a minute by default): `packets` test packets (100) about `spacing` apart (100 ms). It has no access to the Kubernetes API and serves its own `/metrics` on port 9105, which the cluster collector scrapes as a second job.
- **The responder**, a pod on every node in the host network, because the other clusters are given this cluster's node addresses and try them. It answers test requests and nothing else. It runs unprivileged on a port above 1024, with no capabilities and no API access.

Both are kept out of the agent's image and pod on purpose: the agent has no measurement tools, and a probe that misbehaves cannot take the agent down.

| Metric | Type | Meaning |
| --- | --- | --- |
| `continuum_link_probe_packets_sent_total`, `..._lost_total`, `..._duplicated_total` | counter | Test packets by `direction` (`forward` is from this cluster to the peer). `rate(lost) / rate(sent)` is the loss on that direction alone. |
| `continuum_link_probe_loss_ratio` | gauge | Loss in the latest run. |
| `continuum_link_probe_jitter_seconds` | gauge | Delay variation in the latest run. |
| `continuum_link_probe_delay_variation_seconds` | gauge | How far the delay rose above its minimum (`stat` is `median` or `max`). |
| `continuum_link_probe_one_way_delay_seconds` | gauge | Absolute delay (`stat` is `min`, `median` or `max`). **Present only when both clocks were synchronised.** |
| `continuum_link_probe_clock_synchronized`, `..._clock_error_seconds` | gauge | Whether the delay above can be trusted, and the error bound the tool derived. |
| `continuum_link_probe_path_hops` | gauge | Routers crossed, from the TTL the packets arrived with. |
| `continuum_link_probe_failures_total` | counter | Runs that could not complete, by `reason` (`unreachable`, `refused`, `timeout`, `auth`, `other`). |
| `continuum_link_probe_last_success_timestamp_seconds` | gauge | When a run in that direction last finished. |

Every series also carries `cluster`, `peer` and `method="owamp"`.

**Absolute one-way delay needs agreeing clocks.** The two ends timestamp with their own clocks, so a difference between them is added to the delay in one direction and taken off in the other. The tool reports whether both clocks were synchronised (`SYNC`) and the error it derived. Absolute delay is exported only when they were. The delay variation and jitter are always exported, because a constant clock offset cancels out of them, and so does the difference between the two directions' variation. Run chrony or ntpd on the nodes, with the kernel clock synchronised, if you want absolute delay.

**What has to be open between paired clusters.** TCP `responder.port` (8861) and UDP `responder.testPorts` (18900–18999) to every node address of the peer cluster, in both directions. The prober tries a peer's addresses until one answers (the one that answered last first), at most five per run. Where the clusters are not on a network you control, set `responder.allowFrom` to the CIDRs the other clusters' nodes appear from; the responder then refuses everyone else at the control port. Without it the responder accepts anyone who reaches the port, within a bandwidth, session and disk limit, and it reports nothing about this cluster.

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

## What is planned

The other sources fill what these cannot see. None of them exists yet.

- **A constant baseline** to each peer: ICMP round-trip time and loss with [smokeping_prober](https://github.com/SuperQ/smokeping_prober), and TCP connect time with the agent's existing measurement code, which works where ICMP is blocked.
- **Throughput tests with iperf3**, run on a schedule that respects a byte budget, never overlaps on one target, and waits while the link is busy. They reveal the rate at which loss begins, per direction, and capacity over time.

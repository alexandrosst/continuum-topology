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

## What is planned

The other sources fill what passive measurement cannot see. They are listed here so the metric names above are read in context; none of them exists yet.

- **A constant baseline** to each peer: ICMP round-trip time and loss with [smokeping_prober](https://github.com/SuperQ/smokeping_prober), and TCP connect time with the agent's existing measurement code, which works where ICMP is blocked.
- **Directional loss, jitter and one-way delay** from a STAMP (RFC 8762) sender and reflector. Loss is counted per direction from sequence numbers, and absolute one-way delay is reported only when both clocks are known to be synchronised; the variation and the forward-versus-reverse difference are reported either way, because the clock error cancels out of them.
- **Path MTU and route changes**, since an MTU mismatch produces failures that look like loss.
- **Throughput tests with iperf3**, run on a schedule that respects a byte budget, never overlaps on one target, and waits while the link is busy. They reveal the rate at which loss begins, per direction, and capacity over time.

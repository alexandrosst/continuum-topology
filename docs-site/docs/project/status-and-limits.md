---
id: status-and-limits
title: Status and limits
description: What Ikhnos does today, how it was tested, what has never been run for real, and what is not built.
---

# Status and limits

Ikhnos is **pre-1.0**. It discovers and advises; it does not deploy anything and it never moves anything (access tiers 3 and 4 are reserved and refused). This page says plainly how far the testing goes and where it stops, so you can judge it before you put it near a cluster that matters.

## Where it stands

Done: the trust foundation (sign-in with optional two-factor, organizations and roles, consent that lives with the cluster's owner, approval codes, an audit trail); discovery and the digital twin (agent, node probe, traffic observer, history, path measurements); telemetry with FUSION (metrics, logs and traces saved and read back through Grafana and a token-protected API); and uncertainty-aware placement advice, a three-valued fit (fits, does not fit, can't tell) with confidence taken from the weakest deciding fact and the evidence and "what would change it" behind every answer (see [Mobility and placement](../user-guide/mobility-and-placement.md)).

Deliberately paused: acting on advice, meaning deploying to clusters or moving anything, until it is asked for. See the [roadmap](./roadmap.md) for what is next.

## How it was tested

Testing runs against real code paths wherever possible: Go tests under `-race`, a sandboxed single-node k3s API for server and agent integration, and, for the traffic observer, real eBPF programs against real loopback traffic. Pods cannot run in the project's own test sandbox, which is why several things below are out of reach there.

## What has not been run for real

- **A real production cluster, or a real DaemonSet.** The node probe and the traffic observer were verified as standalone binaries and by tests, not as pods on real nodes.
- **A second live cluster.** Cross-cluster resolution is covered by tests that feed two clusters' facts, not by two live clusters.
- **A running service mesh.** Mesh *detection* is tested against the Kubernetes objects a mesh creates (Istio, Linkerd, Cilium), never against a running proxy. Pod Security enforcement is also untested.
- **Other platforms.** arm64 is built but never executed. IPv6 code paths exist but the sandbox has no IPv6. Other kernels and distributions for the eBPF collector are untested (it needs Linux 5.9 or newer with BTF; older kernels fall back to conntrack, which is exercised). kube-proxy setups are untested.
- **Scale and network realism.** Measurements are TCP connect times over the sandbox's local addresses only: real WAN round trips, ICMP-only networks and hosts that filter TCP are untested. A picture of 60,000 workloads was exercised in tests, not on a cluster of that size.
- **The Grafana dashboards** are checked by tests for valid JSON, consistent identifiers and links between them, and a bounded size. They have not been rendered against a live Grafana in every deployment shape.

## What is not built

- OIDC and SSO: local accounts only (with TOTP, email codes and passkeys as second factors).
- Ambient-mesh traffic attribution: HBONE hides the workload-to-workload hop from the observer.
- Cilium mesh detection, and Consul or Kuma control-plane recognition (their sidecars are recognised, their control planes are not).
- Authorization policies and `DestinationRule` or `Server` TLS settings: only `PeerAuthentication` is read.
- A Neo4j *import* mode.
- A hard multi-tenant boundary in Neo4j Community: tenants are separated by query, not by database. Run one Neo4j per tenant, or Enterprise composite databases, if you need that.

## Read with appropriate skepticism

The placement engine's weights are a starting point, not a calibrated model. With little measured traffic, advice says so ("mostly guessed") rather than pretending otherwise. A past view in History shows what agents discovered back then, with today's names, sites and policy, because only discovery is recorded.

## Supported clusters

The agent chart's own table is the source of truth: k3s is tested end to end on a live cluster; kubeadm, kind and minikube are expected to work but were not yet run live; EKS, GKE and AKS with Standard nodes are expected to work and untested; OpenShift, GKE Autopilot, EKS Fargate, IPv6-only and SELinux-enforcing setups are not supported by the telemetry pods. See the [agent Helm values](../reference/agent-helm-values.md) and the chart's README.

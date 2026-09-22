---
id: namespaces-and-services
title: Namespaces and services
description: What agent scope means for which namespaces are visible, and how service mesh detection shows up in the UI.
---

# Namespaces and services

:::info[This page is still growing]
This page will expand into a full walkthrough of the Namespaces and Services pages, service mesh detection (Istio and Linkerd), and how to read the mesh coloring on connections. For now, here's the part that trips people up first.
:::

By default an agent reports every namespace it finds — that's what lets the topology discover your applications on its own. Scope narrows that, and it's applied **inside the agent, before anything is sent** — not a Kubernetes RBAC restriction (the agent's own permissions stay cluster-wide read-only either way), but a privacy boundary: a namespace left out of scope, along with its workloads, services and traffic, simply never leaves the cluster. The server is told a count of what was excluded, never the names.

Three system namespaces (`kube-system`, `kube-public`, `kube-node-lease`) are always read — needed to recognize the cluster and any service mesh running in it — but never drawn as if they were your own applications.

If a service mesh (Istio or Linkerd) is detected, connections between meshed services are colored by what was actually observed in configuration: green where mutual TLS is enforced end to end, amber where it's only partial or permissive, red where a port is explicitly excluded from the proxy or mTLS is off. This is inferred entirely from configuration the agent can already see — no traffic is intercepted or decrypted to produce it.

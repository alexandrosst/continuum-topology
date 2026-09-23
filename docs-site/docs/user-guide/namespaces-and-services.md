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

Three system namespaces (`kube-system`, `kube-public`, `kube-node-lease`) are always read — needed to recognize the cluster and any service mesh running in it — but never drawn as if they were your own applications. A namespace holding nothing but Continuum's own agent (however it was installed, whatever it's named) is left out the same way, once it has anything else running in it to tell the two apart.

A namespace can also opt itself out at any time, independent of whatever scope was set at install: label it `continuum.io/observe=false` and the agent stops reporting it from the moment that label appears — no reinstall or `helm upgrade` needed, since the agent checks a namespace's own labels live, not something baked into the chart. This wins over every other scope setting, including a namespace explicitly named in "Only these namespaces."

If a service mesh (Istio or Linkerd) is detected, connections between meshed services are colored by what was actually observed in configuration: green where mutual TLS is enforced end to end, amber where it's only partial or permissive, red where a port is explicitly excluded from the proxy or mTLS is off. This is inferred entirely from configuration the agent can already see — no traffic is intercepted or decrypted to produce it.

## How workloads are grouped into an application

Nothing has to be labelled for grouping to work at all — every workload always ends up in *some* application, even if it's just its own namespace on its own. But a few labels, in order, let you say more precisely: a workload carrying `continuum.io/application=<name>` is grouped under that name outright, no matter what else is set on it. Failing that, an [Argo CD](https://argo-cd.readthedocs.io/) tracking annotation or instance label groups by the Argo Application name; failing that, Helm's own release metadata (set automatically by every `helm install`/`upgrade`, nothing to add yourself) groups by release name; failing that, the community-standard `app.kubernetes.io/part-of` label groups by whatever value it carries. With none of these, everything in a namespace is grouped together under the namespace's own name — a reasonable default for a namespace that is one application, less so for one that holds several.

An application named this way is the same application everywhere it appears — the same name in a cloud cluster and an edge cluster becomes one node in the topology spanning both, which is what lets a service and the edge instance of it that talks to it show up as one thing rather than two unrelated ones. Only the namespace-name fallback stays local to each cluster, since a name like `default` means nothing shared across clusters.

Got a grouping wrong? The Discovery inbox is where to fix it, not a relabel: accept, correct, or dismiss what it suggests, same as any other discovered grouping.

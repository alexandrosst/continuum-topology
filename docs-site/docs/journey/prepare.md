---
id: prepare
title: "1. Prepare"
description: What to have ready before installing Ikhnos, how to make a local cluster for a first try, and how to find the address agents will use.
---

<StepBar n={1} of={7} title="Prepare" />

# Prepare

You will need a cluster to install the server in, two command-line tools, and one address. This step gets all three, and checks them before anything is installed.

## A cluster

Any Kubernetes cluster, version 1.25 or newer, works: a managed one on a cloud, one you run yourself, or a small local one. If you have one and `kubectl` already talks to it, skip ahead to the check below.

If you do not, make a local one. Pick whichever you already have a tool for.

```bash title="k3s (Linux; installs a single-node cluster)"
curl -sfL https://get.k3s.io | sh -
mkdir -p ~/.kube && sudo cat /etc/rancher/k3s/k3s.yaml > ~/.kube/config
```

```bash title="kind (inside Docker; works on any OS with Docker)"
kind create cluster --name ikhnos
```

```bash title="minikube"
minikube start
```

:::tip[Your first cluster can be the one you connect]
Ikhnos is meant to watch many clusters, but for a first try it is perfectly good to connect the same cluster the server runs in. You will see the whole flow, and you can add a second one later.
:::

## The check

Run these two commands.

```bash
kubectl get nodes
helm version --short
```

<See>
A list with at least one node whose status is `Ready`, and a Helm version of `v3.8` or newer.
</See>

If `kubectl` cannot connect, the problem is between your machine and the cluster, and nothing in Ikhnos can fix it yet. Fix that first.

## An address your agents can reach

An agent finds the server by an address that you give the server at install time. It has to be reachable **from wherever the agent runs**, not just from your own machine. The server bakes this address into its own certificate, so it must be right the first time.

For a first try on a single cluster, any node address will do. Ask Kubernetes for them:

```bash
kubectl get nodes -o wide
```

Copy one `EXTERNAL-IP` from the output. If the column says `<none>` (normal for a local cluster), copy an `INTERNAL-IP` instead. Call it `NODE_IP`. You will use it in the next step.

:::note[Why this is not a hostname]
A node address plus a fixed port is the simplest thing that works on every cluster, which is why this journey uses it. For a real deployment, with agents dialling in from other networks, you will want a stable DNS name behind a load balancer or a Gateway. [Production cluster](../installation/production-cluster.md) covers that when you get there. Nothing you do now has to be undone for it.
:::

## Ready?

You should now have a working `kubectl`, a working `helm`, and a `NODE_IP` written down.

[Next: install the server →](./install-server.md)

---
id: index
title: Your first hour
description: A guided path from an empty cluster to your first placement advice, in seven steps, with what you should see after each one.
slug: /journey/
---

# Your first hour

This is a guided path for someone who has never used Ikhnos. By the end you will have a server running, one cluster connected and approved, and a look at what Ikhnos can tell you about it, including its first placement advice. Allow about an hour, most of it waiting for pods to start.

Each step says what to do, what you should see when it worked, and what to try if it did not. If something is unclear, the step before it is where to look.

<Figure name="OnboardingJourney" caption="Seven steps. Steps 4 and 5 are where Ikhnos differs from most tools: the cluster asks to join, and you confirm it with a code only its own log shows." />

## What you need

| You need | Why | If you do not have it |
|---|---|---|
| A Kubernetes cluster, version 1.25 or newer | The server runs in one. For a first try, the same cluster can also be the one you connect. | [Step 1](./prepare.md) shows how to make a local one with k3s, kind or minikube in a few minutes. |
| `kubectl`, pointed at it | To check on pods and read two logs. | [Install kubectl](https://kubernetes.io/docs/tasks/tools/). |
| `helm`, version 3.8 or newer | Ikhnos is installed with two Helm charts. | [Install Helm](https://helm.sh/docs/intro/install/). |
| `openssl` | To make one key. | It is on almost every machine. |
| A web browser | Ikhnos is a web interface. | |

You do **not** need to build anything, run a database, hand over a kubeconfig, or open an inbound port on any cluster.

## The seven steps

<Cards>
  <Card to="/docs/journey/prepare" kicker="Step 1" title="Prepare">A cluster, two tools, and an address.</Card>
  <Card to="/docs/journey/install-server" kicker="Step 2" title="Install the server">One secret and one Helm command.</Card>
  <Card to="/docs/journey/sign-in" kicker="Step 3" title="Sign in">The first password, and choosing your own.</Card>
  <Card to="/docs/journey/connect-a-cluster" kicker="Step 4" title="Connect a cluster">The wizard prints one command. You run it.</Card>
  <Card to="/docs/journey/approve" kicker="Step 5" title="Approve it">Confirm it is the agent you meant.</Card>
  <Card to="/docs/journey/explore" kicker="Step 6" title="Explore">Topology, Clusters, Agents and History.</Card>
  <Card to="/docs/journey/advice" kicker="Step 7" title="Ask for advice">Read a recommendation, and what it rests on.</Card>
</Cards>

## In a hurry

If you already know Kubernetes and Helm and just want the commands, the [Quickstart](../getting-started/quickstart.md) is the same path without the explanations. Come back to this page when you want to know what a step meant.

## One idea before you start

Ikhnos is built so that the agent in a cluster always dials **out** to the server. You never open a port on the cluster, and the server never holds a key to it. That shapes the whole journey: the server needs one address your agents can reach, and each cluster needs one command run by someone who can already use that cluster. Everything else follows from those two facts. [How it works](../learn/how-it-works.md) draws it, if you want the picture first.

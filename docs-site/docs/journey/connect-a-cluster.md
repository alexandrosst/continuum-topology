---
id: connect-a-cluster
title: "4. Connect a cluster"
description: Use the Connect a cluster wizard to get the one install command for an agent, understand what it contains, and run it.
---

<StepBar n={4} of={7} title="Connect a cluster" />

# Connect a cluster

A cluster joins Ikhnos by running a small agent. You do not create the agent anywhere by hand: a wizard in the interface prints the exact command, already filled in with this server's address, the fingerprint of its certificate authority, and a one-time token.

## Open the wizard

On the empty Topology page, press **Connect a cluster**. It is also on the **Agents** page, and it is the first step of the checklist.

<Shot src="journey/connect-name" alt="The Connect a cluster dialog with a Cluster name field and a More options section" caption="Give the cluster a name as you want to see it. It can be renamed later." />

Type a name, such as `edge-lab`. The page explains the default in one sentence: the agent installs read-only, at the recommended access level, and can never read Secrets or ConfigMaps or change anything in the cluster.

## Choose what the agent may read (optional)

You can press **Create install command** now and come back to this later. If you want to see the choices, open **More options**.

<Shot src="journey/connect-options" alt="The More options section showing two access levels, Infrastructure and Services, and extras that can be switched on" caption="Two access levels, and extras that are separate things to install." />

**What may the agent read?** There are two choices.

- **Infrastructure** is the nodes, storage classes and ingress classes: what the cluster is made of.
- **Services** (the default) is everything in Infrastructure plus namespaces, workloads, pods, services and ingresses: what runs on it.

This is the **ceiling**. It is enforced by Kubernetes itself, through the permissions the chart creates, and only someone with `helm upgrade` rights on the cluster can raise it later.

<Figure name="TrustChain" caption="The ceiling you choose here is the top of the chain. Later, an administrator can approve less, and the agent can send less, but never more." />

**Extras** are independent of the access level. Each is a separate pod you are choosing to also install, and each is off until you switch it on. One tells virtual machines from bare metal on every node. One counts connections and bytes between services, using eBPF or the kernel's connection table, which is what lets Ikhnos draw real dependencies. One times TCP connections to addresses the server names, to measure the paths between clusters. You can add any of them after the install, so it is fine to start with none.

## Run the command

Press **Create install command**.

<Shot src="journey/connect-command" alt="The wizard showing the helm install command for the agent, a note that the token works once, and the message Waiting for the agent to start and connect" caption="The command, ready to copy. The enrollment token is shown only here and cannot be retrieved later. (Here the token is replaced by a placeholder.)" />

The command looks like this. Yours will have real values.

```bash
helm install ikhnos-agent oci://ghcr.io/alexandrosst/ikhnos-agent \
  --namespace ikhnos-system --create-namespace \
  --set server.address=NODE_IP:30443 \
  --set enrollment.key=sha256/…=.ikh_…
```

It contains the address the agent will dial, and one value, `enrollment.key`, that holds two things joined by a dot: the **CA pin** (the fingerprint of the server's certificate authority, so the agent can tell it reached the right server before it trusts anything) and the **one-time token** (valid for an hour, one use).

Copy it with **Copy command** and run it where `kubectl` and `helm` point at the cluster you are connecting. For your first try, that is the same cluster as before.

<See>
Helm reports `STATUS: deployed`. Then `kubectl -n ikhnos-system get pods` shows an `ikhnos-agent` pod that becomes `Running`. In the wizard, the first of four stages, **Agent connects**, completes, and the spinner reads that it is waiting for the agent to start and connect.
</See>

The wizard has four stages: **Agent connects**, **You approve it**, **Discovering**, and **Connected**. It tracks them for you, and **Continue in background** lets you close it and come back; the checklist and the Agents page show the same progress.

## If it did not work

| You see | Usually means | What to do |
|---|---|---|
| The pod is `ImagePullBackOff` | The node cannot pull from `ghcr.io`. | Same checks as in step 2. |
| The pod runs but the wizard never moves | The agent cannot reach the server's address, or the pin does not match. | `kubectl -n ikhnos-system logs deploy/ikhnos-agent` names the failure. See "The agent never appears" in [Common errors](../troubleshooting/common-errors.md). |
| `helm install` says the token is invalid or used | The token works once and for an hour. | Press **Connect a cluster** again for a fresh command. |

## What just happened

The agent started, dialled the server, checked the fingerprint, and sent the token with a request for a certificate. It is now waiting, and the server has not trusted it yet. That is deliberate. The next step is what makes it trusted.

[Next: approve it →](./approve.md)

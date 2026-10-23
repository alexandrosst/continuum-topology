---
id: lifecycle
title: Installing and removing what Ikhnos installs
description: Who owns the desired state of the agents, regional operators and FUSION, why Ikhnos does not integrate with GitOps, how removal works today, and the one optional step planned after it (a controller with a single CRD).
---

# Installing and removing what Ikhnos installs

Ikhnos puts four things into other people's clusters: the agent, the regional operator, the FUSION stack and the server itself. This page says who decides what is installed, how removal works now, and what is planned.

## One owner: the server, through its own pages

The server is the only place that holds the desired state of the agents, the regional operators and FUSION. It is changed in Ikhnos's own pages (connect a cluster, change an access level, revoke, remove). Ikhnos does **not** integrate with GitOps, on purpose: a second place that says what should be installed (a Git repository reconciled by Argo CD or Flux) would have to be kept in agreement with the first, and the two disagree in ways that are hard to see, for example a repository that still lists an agent the server has revoked. A cluster owner who runs their own GitOps can still install the charts with it; that is their cluster and their tooling. Ikhnos just does not read from it, write to it or reconcile with it.

The commands the pages print are the interface to the cluster: the server writes them, a person with rights in that cluster runs them. The server holds no Kubernetes credential for any agent cluster.

## Removing an agent today

Revoking an agent in the Agents page closes its connection and stops the server trusting it. It does not touch the cluster. The page then shows the commands the cluster's owner runs there: `helm uninstall` for the release, and, by default, a second command that deletes the identity Secret.

The identity Secret (the agent's private key and the certificate the server issued) is kept by an uninstall on purpose: the chart marks it `helm.sh/resource-policy: keep`, so a mistaken uninstall cannot orphan the agent or force a new enrollment.

An install can ask for the opposite. In the connect-cluster form, under More options, "Delete the agent's identity when it is uninstalled" sets `identity.purgeOnUninstall=true`. The chart then adds a post-delete hook: a short Job, with a ServiceAccount and Role of its own that can only `get` and `delete` that one Secret by name, runs `ikhnos purge-identity`, which deletes the Secret only if it carries the label `app.kubernetes.io/part-of=ikhnos` and only the exact object it read (it deletes by UID). A Secret that is already gone counts as success; one that is not Ikhnos's is left alone and reported.

It has to be chosen when the agent is installed or upgraded, because `helm uninstall` takes no values. An agent installed without it is cleaned up with the second command, as before. The option is not available as a switch at uninstall time, and `helm uninstall --no-hooks` skips it.

Removing the Secret does not make the server forget the agent. Revoke it in the page as well.

## Planned: a controller with one CRD (optional)

If it is built, it is an optional extra for people who want `kubectl` to be the way they ask for an agent. It stays inside the rule above: the server remains the owner.

- **Pull, not push.** The controller runs in the agent's own cluster and uses the connection the agent already makes to the server (outbound, mutual TLS). The server never gets a credential for the cluster.
- **One CRD that wraps the existing charts.** A resource says "this cluster runs an Ikhnos agent with these options"; the controller installs the existing chart through the Helm SDK, so there is one install path and not two.
- **A finalizer** runs the cleanup (including the identity) before the resource goes.
- **Costs, said now:** a controller cannot uninstall itself; it needs RBAC wide enough to install the chart, and Kubernetes only lets it grant what it holds; Helm does not upgrade CRDs, so the CRD has its own upgrade path; and it must stay optional, so a cluster without it works exactly as above.

None of this exists yet. Nothing in the agent or the server depends on it.

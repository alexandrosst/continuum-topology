---
id: lifecycle
title: Installing and removing what Ikhnos installs
description: Who owns the desired state of the agents, regional operators and FUSION, why Ikhnos does not integrate with GitOps, how removal works today, and the optional controller with a single CRD that lets `kubectl apply` be the way an agent is asked for.
---

# Installing and removing what Ikhnos installs

Ikhnos puts four things into other people's clusters: the agent, the regional operator, the FUSION stack and the server itself. This page says who decides what is installed, how removal works, and the optional controller.

## One owner: the server, through its own pages

The server is the only place that holds the desired state of the agents, the regional operators and FUSION. It is changed in Ikhnos's own pages (connect a cluster, change an access level, revoke, remove). Ikhnos does **not** integrate with GitOps, on purpose: a second place that says what should be installed (a Git repository reconciled by Argo CD or Flux) would have to be kept in agreement with the first, and the two disagree in ways that are hard to see, for example a repository that still lists an agent the server has revoked. A cluster owner who runs their own GitOps can still install the charts with it; that is their cluster and their tooling. Ikhnos just does not read from it, write to it or reconcile with it.

The commands the pages print are the interface to the cluster: the server writes them, a person with rights in that cluster runs them. The server holds no Kubernetes credential for any agent cluster.

## Removing an agent today

Revoking an agent in the Agents page closes its connection and stops the server trusting it. It does not touch the cluster. The page then shows the commands the cluster's owner runs there: `helm uninstall` for the release, and, by default, a second command that deletes the identity Secret.

The identity Secret (the agent's private key and the certificate the server issued) is kept by an uninstall on purpose: the chart marks it `helm.sh/resource-policy: keep`, so a mistaken uninstall cannot orphan the agent or force a new enrollment.

An install can ask for the opposite. In the connect-cluster form, under More options, "Delete the agent's identity when it is uninstalled" sets `identity.purgeOnUninstall=true`. The chart then adds a post-delete hook: a short Job, with a ServiceAccount and Role of its own that can only `get` and `delete` that one Secret by name, runs `ikhnos purge-identity`, which deletes the Secret only if it carries the label `app.kubernetes.io/part-of=ikhnos` and only the exact object it read (it deletes by UID). A Secret that is already gone counts as success; one that is not Ikhnos's is left alone and reported.

It has to be chosen when the agent is installed or upgraded, because `helm uninstall` takes no values. An agent installed without it is cleaned up with the second command, as before. The option is not available as a switch at uninstall time, and `helm uninstall --no-hooks` skips it.

Removing the Secret does not make the server forget the agent. Revoke it in the page as well.

## The controller with one CRD (optional)

For people who want a resource in the cluster to be the way an agent is asked for, there is an optional controller (chart `ikhnos-controller`, image `controller`). It stays inside the rule above: the server remains the owner, and the controller is only another way to run the same `helm install` the Connect page prints. The Connect page shows it in a second tab beside the helm command, written from the same choices: the controller's own `helm install` (skip it if the controller is already running), a command that creates the Secret holding the one-time token, and the `IkhnosAgent` itself, which names that Secret and holds no token. It needs the controller's chart in your image registry; without one the tab says so and the helm command stands.

- **A resource, not a connection.** An `IkhnosAgent` in the controller's namespace says which server to enrol with (its address and CA pin), where the one-time token is (a Secret, never the resource's own text) and the choices the Connect page offers: the access tier, the collectors, the namespaces to report and whether the identity goes with the agent. The controller never talks to the server and holds no credential for it. The agent that gets installed enrols and is approved on the server's page like any other.
- **The same install.** The controller carries the agent chart in its own image and installs it with the Helm library as a release called `ikhnos-agent` (so `helm list` shows it, and `helm get values` tells what it was given). The choices become Helm values through the same code the Connect page's command uses, and a test holds that code to Helm's own parser, so the two cannot disagree about what a choice means.
- **It only changes what differs.** It installs when there is no release and the token Secret is there; it upgrades when the resource, the controller's chart or the state of the release differs; otherwise it does nothing, and writes nothing. Once the agent has enrolled the token Secret may be removed: the release is upgraded without the token and the agent keeps its identity.
- **One agent per namespace.** The agent chart's objects have fixed names, so a second `IkhnosAgent` in the namespace is reported `Blocked` and does nothing; the oldest owns the release, and deleting a blocked one never uninstalls what is not its.
- **Deleting uninstalls, then lets go.** A finalizer holds the resource until the release is uninstalled, including the post-delete hook that deletes the identity when `purgeIdentityOnDelete` is set (the controller first applies that choice to the release if it differs, since the hook belongs to the release as installed). If an uninstall cannot finish, the annotation `ikhnos.io/skip-uninstall: "true"` lets the resource go without touching the release.
- **Permissions, said plainly.** Kubernetes lets nobody create a ClusterRole that grants what they do not hold, so a controller that installs a tier 2 agent holds, cluster-wide, everything tier 2 reads. The chart's `maxAccessTier` is the way to hold less: at 1 the controller holds only what tier 1 reads, at 0 only the name of one namespace, and a resource that asks for more is refused with a message, not trimmed. A test renders the agent chart for every choice a resource can make and holds the controller's permissions to it in both directions: nothing the chart creates is out of reach, and nothing is held that no rendered object asks for.
- **What it does not do.** It cannot uninstall itself; Helm installs a chart's CRD once and never upgrades it, so a new version of the definition is applied by hand (`kubectl apply -f` the file the chart's notes name); it runs as one replica, because two would fight over the release; and a resource cannot yet choose telemetry, the agent's other options being the chart's defaults. The controller has not yet been run against a real cluster: its logic is tested against a fake release store and Helm's in-memory one, its permissions by rendering, and the Helm library's own behaviour (creating, waiting, watching a hook Job) is the part that only a cluster shows.

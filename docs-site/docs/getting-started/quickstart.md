---
id: quickstart
title: Quickstart
description: Get the Continuum server running on any Kubernetes cluster in about five minutes.
---

# Quickstart

This gets the **server** (the control plane and web UI) running on any Kubernetes cluster — a local trial (k3s, kind, minikube) or a real one — in the fewest steps possible. It uses this project's own zero-config images and charts, published to GitHub Container Registry (GHCR), so there is nothing to build and no image name to fill in by hand.

You'll need `kubectl` pointed at your cluster and `helm` installed. Every command below is meant to be copied exactly, with only the two placeholders (`YOUR-GITHUB-USERNAME` and `NODE_IP`) replaced.

## 1. Cut a release

The publishing workflow marks anything pushed straight to `main` as a **pre-release** build, which Helm won't install by default (it always looks for the newest *stable* version unless you ask for a specific one). So before the very first install, tag a real release from your own clone of the repo:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Wait about a minute for the "Release" workflow to finish — check the **Actions** tab on GitHub. Every later deploy just needs a new tag (`v0.1.1`, `v0.2.0`, ...).

## 2. Make the GHCR packages public (one time only)

The very first time that workflow runs, GitHub creates its four packages as **private** by default — even though your repository is public. On your repo's page, open the **Packages** section in the right sidebar, and for each of `continuum`, `server`, `continuum-agent`, `continuum-server`: **Package settings** (bottom of the page) → **Change visibility** → **Public**.

You only do this once, ever. Skip it and the install in the next step fails with an "unauthorized" pull error.

## 3. Find an address for your nodes

```bash
kubectl get nodes -o wide
```

Copy one `EXTERNAL-IP` (or `INTERNAL-IP` if there isn't one — fine for a trial). This is `NODE_IP` below.

## 4. Install the server

Replace `YOUR-GITHUB-USERNAME` with the GitHub username or org that owns the repository (lowercase), and `NODE_IP` with the address from the previous step:

```bash
helm install continuum oci://ghcr.io/YOUR-GITHUB-USERNAME/continuum-server --version 0.1.0 \
  --namespace continuum --create-namespace \
  --set agent.service.type=NodePort \
  --set agent.service.nodePort=30443 \
  --set agent.publicAddress=NODE_IP:30443
```

This is the fastest path to a working server on any cluster. `agent.publicAddress` is the one value the chart cannot guess for you — it's the address your future agents will dial, and it's baked into the server's own TLS certificate, so it has to be exactly right. [Installation](../installation/index.md) covers the other exposure options (a cloud load balancer, an Ingress) once you're ready to move past a trial.

## 5. Wait for it to come up

```bash
kubectl get pods -n continuum -w
```

Ctrl+C once the pod shows `Running` and `1/1`.

## 6. Get the first admin password

Generated once and printed to the log the first time the server starts on an empty database:

```bash
kubectl -n continuum logs deployment/continuum | grep -A2 'First start'
```

## 7. Open the UI

Not exposed outside the cluster by default, so forward a port to it:

```bash
kubectl -n continuum port-forward service/continuum 8080:8080
```

Open `http://localhost:8080`, sign in as `admin` with the password from step 6, and choose a real password.

## You're running

From here, the UI's **Connect a cluster** screen prints the exact `helm install` for the agent — pre-filled with your GHCR registry, a one-time enrollment token and this server's CA pin. You never type an image name or a chart reference for the agent by hand. [Connecting a cluster](../installation/connecting-a-cluster.md) walks through that screen and the approval step that follows it.

:::tip
This NodePort setup is meant to get you to a working server fast, on any cluster. If agents will dial in from outside a network you control, move to a real DNS name and either a load balancer or a TLS-passthrough Ingress — see [Production cluster](../installation/production-cluster.md) and [Exposing the agent port](../architecture/exposure-options.md).
:::

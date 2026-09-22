---
id: quickstart
title: Quickstart
description: Get the Continuum server running on any Kubernetes cluster in about five minutes.
---

# Quickstart

This gets the **server** (the control plane and web UI) running on any Kubernetes cluster — a local trial (k3s, kind, minikube) or a real one — in the fewest steps possible. It uses this project's own ready-made images and Helm chart, published to GitHub Container Registry, so there's nothing to build and nothing to fill in yourself.

You'll need `kubectl` pointed at your cluster and `helm` installed. The only value you'll need to fill in below is `NODE_IP`.

## 1. Find an address for your nodes

```bash
kubectl get nodes -o wide
```

Copy one `EXTERNAL-IP` (or `INTERNAL-IP` if there isn't one — fine for a trial). This is `NODE_IP` below.

## 2. Install the server

Replace `NODE_IP` with the address from the previous step:

```bash
helm install continuum oci://ghcr.io/alexandrosst/continuum-server \
  --namespace continuum --create-namespace \
  --set agent.service.type=NodePort \
  --set agent.service.nodePort=30443 \
  --set agent.publicAddress=NODE_IP:30443 \
  --set admin.behindTlsProxy=true
```

This installs the latest release. `agent.publicAddress` is the one value the chart cannot guess for you — it's the address your future agents will dial, and it's baked into the server's own TLS certificate, so it has to be exactly right. `admin.behindTlsProxy=true` is what lets step 5 below (`kubectl port-forward`) reach the UI at all for this trial: the admin port carries your sign-in password and session cookie, so the server otherwise refuses to serve it in clear text — `kubectl port-forward` tunnels over an already-encrypted connection to the API server, so this is safe for exactly that case. [Installation](../installation/index.md) covers the other exposure options (a cloud load balancer, an Ingress) once you're ready to move past a trial.

:::tip[Pinning a version]
Leaving out `--version` always installs the newest release, which is right for a first trial. For a reproducible deploy later, add `--version X.Y.Z` — see the [Releases page](https://github.com/alexandrosst/continuum-topology/releases) for available versions.
:::

## 3. Wait for it to come up

```bash
kubectl get pods -n continuum -w
```

Ctrl+C once the pod shows `Running` and `1/1`.

## 4. Get the first admin password

Generated once and printed to the log the first time the server starts on an empty database:

```bash
kubectl -n continuum logs deployment/continuum | grep -A2 'First start'
```

## 5. Open the UI

Not exposed outside the cluster by default, so forward a port to it:

```bash
kubectl -n continuum port-forward service/continuum 8080:8080
```

Open `http://localhost:8080`, sign in as `admin` with the password from step 4, and choose a real password.

## You're running

From here, the UI's **Connect a cluster** screen prints the exact `helm install` for the agent — pre-filled with your GHCR registry, a one-time enrollment token and this server's CA pin. You never type an image name or a chart reference for the agent by hand. [Connecting a cluster](../installation/connecting-a-cluster.md) walks through that screen and the approval step that follows it.

:::tip
This NodePort setup is meant to get you to a working server fast, on any cluster. If agents will dial in from outside a network you control, move to a real DNS name and either a load balancer or a TLS-passthrough Ingress — see [Production cluster](../installation/production-cluster.md) and [Exposing the agent port](../architecture/exposure-options.md).
:::

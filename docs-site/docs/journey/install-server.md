---
id: install-server
title: "2. Install the server"
description: One secret and one Helm command start the Ikhnos server on your cluster. What each line does, and how to know it worked.
---

<StepBar n={2} of={7} title="Install the server" />

# Install the server

The server is one pod with a small database. It is installed with one Helm chart, published to this project's own container registry, so there is nothing to build and no chart repository to add.

## The commands

Replace `NODE_IP` with the address from the previous step.

```bash
kubectl create namespace ikhnos
kubectl -n ikhnos create secret generic ikhnos-sealing \
  --from-literal=sealing.key="$(openssl rand -base64 48)"

helm install ikhnos-server oci://ghcr.io/alexandrosst/ikhnos-server \
  --namespace ikhnos \
  --set sealing.keySecret.name=ikhnos-sealing \
  --set agent.service.type=NodePort \
  --set agent.publicAddress=NODE_IP:30443
```

## What each part does

**The namespace** is where everything of the server lives, so removing Ikhnos later is one deletion.

**The sealing secret** is a key that encrypts the few things the server must be able to read back: people's second-factor secrets, the mail password if you configure mail, and the secret of a decider. The chart refuses to install without one. Keep a copy of that key somewhere other than the cluster: if you lose it, those few secrets cannot be recovered. (For a throwaway trial where that does not matter, leave out the first two lines and add `--set sealing.disabled=true` to the helm command instead.)

**`agent.service.type=NodePort`** opens the agent port, 8443 inside the pod, as port 30443 on every node. NodePort works on any cluster, which is why it is used here. On a managed cloud you would use a load balancer instead.

**`agent.publicAddress`** is the one value the chart cannot guess. It is the address agents will dial, and it goes into the server's own certificate. If it is wrong, agents will fail their first handshake with an error that looks like a networking problem.

You did not set an image, a chart version, a storage class or a certificate. The defaults are the project's own published images, the newest release, the cluster's default storage and a self-signed certificate for the web interface.

## Wait for it

```bash
kubectl get pods -n ikhnos -w
```

Press Ctrl+C once the pod is ready.

<See>
One pod named like `ikhnos-server-…`, with `1/1` under READY and `Running` under STATUS. The first start can take a minute while the image is pulled.
</See>

## If it did not work

| You see | Usually means | What to do |
|---|---|---|
| `ImagePullBackOff` | The node cannot reach `ghcr.io`, or a proxy is in the way. | Check the pod with `kubectl -n ikhnos describe pod`, and see [Common errors](../troubleshooting/common-errors.md). |
| `Pending` for a long time | The cluster has no default storage class, so the database volume cannot be made. | `kubectl get storageclass`. On k3s and kind one exists by default. |
| `agent.publicAddress is required` | The helm command ran without that value. | Run it again with `--set agent.publicAddress=NODE_IP:30443`. Use `helm upgrade` if the release exists. |

[Common errors](../troubleshooting/common-errors.md) lists the rest, each with its exact fix.

[Next: sign in →](./sign-in.md)

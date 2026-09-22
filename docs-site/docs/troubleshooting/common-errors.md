---
id: common-errors
title: Common errors
description: The handful of mistakes that are easy to make once, and the exact fix for each.
---

# Common errors

These are errors you can hit while deploying and running the server or an agent. If you're setting up this repository's own releases or GitHub Pages rather than deploying the product, see [Release process](../contributing/release-process.md) instead — a couple of one-time repository settings live there.

## `the admin listener (UI and API) ... refuses to serve it in clear text without TLS`

The admin port carries your sign-in password and session cookie, so the chart refuses to install or upgrade until something protects it. Pick one:

- **Trial, reachable only via `kubectl port-forward`:** `--set admin.behindTlsProxy=true` (this is what the [Quickstart](../getting-started/quickstart.md) uses — `port-forward` already tunnels over an encrypted connection to the API server, so plain HTTP inside the pod is fine).
- **Real Gateway (HTTPRoute) in front, terminating TLS:** enable `httproute` — it sets this automatically, no separate flag needed. See [Production cluster](../installation/production-cluster.md).
- **The server should serve HTTPS itself:** set `admin.tls.secretName` to a `kubernetes.io/tls` Secret.

## `agent.publicAddress is required: ...`

The chart refuses to render without this value — it's the address your agents will dial, and it becomes a name in the server's own TLS certificate, so it has to be set explicitly rather than defaulted to something that would silently be wrong. Set it to `host:port`, for example `continuum.example.com:8443` or, for a NodePort setup, `NODE_IP:30443`. See the [Quickstart](../getting-started/quickstart.md) for a working example end to end.

A related one: `agent.publicAddress must look like host:port (no scheme, no path), got "..."` means exactly what it says — no `https://`, no trailing path, just `host:port`.

## The agent never appears / TLS handshake errors from the agent's side

Almost always one specific mistake: something between the agent and the server is terminating TLS instead of passing it through untouched. The most common cause is a `agent.tlsRoute` Gateway listener that isn't actually in `Passthrough` mode — the `TLSRoute` alone doesn't guarantee that; the *Gateway's listener* it's attached to also has to be configured for it:

```bash
kubectl get gateway shared-gateway -n gateways -o jsonpath='{.spec.listeners[?(@.name=="agents-tls")].tls.mode}'
```

That should print `Passthrough`. If it prints `Terminate` (or nothing), the Gateway is terminating TLS for that listener — fix the listener's `tls.mode`, not anything in this chart. Without that, the handshake fails silently — there's no obvious error naming the real cause, just a connection that doesn't complete. See [Exposing the agent port](../architecture/exposure-options.md) for why this port needs different treatment from the UI's.

## `EXTERNAL-IP` stuck on `<pending>`

`agent.service.type=LoadBalancer` (the default) asked the cluster for an external address, and nothing is answering that request. This means there's no load-balancer controller available — common on bare-metal or fully self-managed clusters. Either switch to `agent.service.type=NodePort` (works everywhere, see the [Quickstart](../getting-started/quickstart.md)), or install something like MetalLB to get real LoadBalancer support.

## Forgot the admin password

Stop the server and run, against its data directory:

```bash
./server reset-password --data-dir ./data admin
```

This prints a new one-time password and ends every existing session for that user. If you're running the chart rather than the binary directly, this means `kubectl exec` into the pod, or scaling it down, mounting the same PVC from a one-off pod, and running the same command.

---
id: common-errors
title: Common errors
description: The handful of mistakes that are easy to make once, and the exact fix for each.
---

# Common errors

## `Error: INSTALLATION FAILED: Could not locate a version matching provided version string`

You ran `helm install` without `--version`, and there is no **stable** chart version published yet — only pre-release ones. Every push to `main` (without a tag) publishes the chart as `0.0.0-edge.<commit-sha>`; the `-edge` part makes it a semver pre-release, and Helm's rule for "no version given" is "the newest **stable** version," which skips pre-releases entirely.

**Fix:** cut a real release tag and use it explicitly:

```bash
git tag v0.1.0
git push origin v0.1.0
# wait for the Release workflow to finish, then:
helm install continuum oci://ghcr.io/YOUR-GITHUB-USERNAME/continuum-server --version 0.1.0 ...
```

Every later deploy just needs a new tag and a matching `--version`.

## `Error: ... 403 Forbidden` / `unauthorized` pulling an image or chart

GitHub creates GHCR packages as **private** the very first time your release workflow runs — even in a public repository. Open your repo's page, find **Packages** in the right sidebar, and for each of `continuum`, `server`, `continuum-agent` and `continuum-server`: **Package settings** → **Change visibility** → **Public**. This is a one-time step; every later push publishes into the same, already-public packages.

## `agent.publicAddress is required: ...`

The chart refuses to render without this value — it's the address your agents will dial, and it becomes a name in the server's own TLS certificate, so it has to be set explicitly rather than defaulted to something that would silently be wrong. Set it to `host:port`, for example `continuum.example.com:8443` or, for a NodePort setup, `NODE_IP:30443`. See the [Quickstart](../getting-started/quickstart.md) for a working example end to end.

A related one: `agent.publicAddress must look like host:port (no scheme, no path), got "..."` means exactly what it says — no `https://`, no trailing path, just `host:port`.

## The agent never appears / TLS handshake errors from the agent's side

Almost always one specific mistake: something between the agent and the server is terminating TLS instead of passing it through untouched. The most common cause is an Ingress-based exposure (`agent.ingress`) whose controller was never told to skip TLS termination for that route:

```bash
helm upgrade ingress-nginx ingress-nginx/ingress-nginx --reuse-values \
  --set controller.extraArgs.enable-ssl-passthrough=true
```

Without that flag, the controller terminates TLS silently — there's no obvious error naming the real cause, just a handshake that doesn't complete. See [Exposing the agent port](../architecture/exposure-options.md) for why this port needs different treatment from the UI's.

## `EXTERNAL-IP` stuck on `<pending>`

`agent.service.type=LoadBalancer` (the default) asked the cluster for an external address, and nothing is answering that request. This means there's no load-balancer controller available — common on bare-metal or fully self-managed clusters. Either switch to `agent.service.type=NodePort` (works everywhere, see the [Quickstart](../getting-started/quickstart.md)), or install something like MetalLB to get real LoadBalancer support.

## Forgot the admin password

Stop the server and run, against its data directory:

```bash
./server reset-password --data-dir ./data admin
```

This prints a new one-time password and ends every existing session for that user. If you're running the chart rather than the binary directly, this means `kubectl exec` into the pod, or scaling it down, mounting the same PVC from a one-off pod, and running the same command.

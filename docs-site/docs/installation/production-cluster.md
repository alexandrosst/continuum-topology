---
id: production-cluster
title: Production cluster
description: Exposing the server properly on a managed cloud cluster, with a real DNS name and TLS.
---

# Production cluster

This builds on the [Quickstart](../getting-started/quickstart.md) — same chart, but exposed the way you'd actually want it running for more than a trial: a real DNS name for agents to dial, a load balancer instead of a bare node IP, and the UI behind a Gateway that terminates TLS instead of `kubectl port-forward`.

:::tip[Pinning a version]
The commands below install the newest release, same as the Quickstart. For a production rollout you'll usually want a reproducible, pinned version instead — add `--version X.Y.Z` to each command below (see the [Releases page](https://github.com/alexandrosst/continuum-topology/releases)), and use the same pinned version for the install and every upgrade that follows.
:::

## The two-step address problem

`agent.publicAddress` has to be set at install time — it's baked into the server's TLS certificate — but if you're using a cloud load balancer, you don't know its external IP or hostname *until after* the Service exists. So this is genuinely a two-step process:

**Install once**, with a placeholder address (the chart just needs something shaped like `host:port` — it doesn't have to resolve yet). `admin.behindTlsProxy=true` is required at this point because nothing is exposing the UI yet — see the note below:

```bash
helm install continuum oci://ghcr.io/alexandrosst/continuum-server \
  --namespace continuum --create-namespace \
  --set agent.publicAddress=pending.example.com:8443 \
  --set admin.behindTlsProxy=true
```

:::info[Why `admin.behindTlsProxy=true` here]
The admin port carries your sign-in password and session cookie, so the chart refuses to serve it in clear text unless something is already protecting it. At this point in the two-step process there's no Gateway (HTTPRoute) in front of it yet, so this flag is what lets the install succeed at all — use `kubectl port-forward` to reach the UI until [Exposing the UI properly](#exposing-the-ui-properly), below, is done. Leaving the flag set after that is harmless; it's also what the chart sets automatically once `httproute` is enabled.
:::

`agent.service.type` defaults to `LoadBalancer`, so this already asked your cloud for one. **Watch for the address:**

```bash
kubectl -n continuum get service continuum-agent -w
```

Once `EXTERNAL-IP` is no longer `<pending>`, point your real DNS name at it (an `A`/`AAAA` record, or a `CNAME` if your cloud gave you a hostname instead of an IP).

**Upgrade with the real address:**

```bash
helm upgrade continuum oci://ghcr.io/alexandrosst/continuum-server \
  --namespace continuum --reuse-values \
  --set agent.publicAddress=continuum.example.com:8443
```

Changing this issues the server a new certificate, but it doesn't break anything already enrolled — agents trust the CA, not the specific certificate, so they keep working straight through the change. If `EXTERNAL-IP` stays `<pending>` for more than a couple of minutes, your cluster has no load-balancer controller; use `agent.service.type=NodePort` instead (see the [Quickstart](../getting-started/quickstart.md)), or install one such as MetalLB.

## Exposing the UI properly

Instead of `port-forward`, put the admin port behind a Gateway that terminates TLS: an `HTTPRoute` attached to a `Gateway` that already has an HTTPS listener set up (its certificate is cert-manager's or however else you manage it — that's configured on the Gateway itself, not by this chart).

```bash
helm upgrade continuum oci://ghcr.io/alexandrosst/continuum-server \
  --namespace continuum --reuse-values \
  --set httproute.enabled=true \
  --set-json httproute.parentRefs='[{"name":"shared-gateway","namespace":"gateways","sectionName":"https"}]' \
  --set-json httproute.hostnames='["continuum.example.com"]'
```

Once this is set, `admin.behindTlsProxy` switches on automatically (the server trusts `X-Forwarded-For`/`X-Forwarded-Proto` from whatever sits in front of it) — you don't need to set it yourself. This needs the Gateway API CRDs and a `Gateway` already running in the cluster; the agent port has the same requirement for [Gateway API TLSRoute](../architecture/exposure-options.md#gateway-api-tlsroute-agenttlsroute).

## Should you turn on Neo4j?

The chart always deploys some form of history store — there's no "off" — but it defaults to `bundled`, which is fine for getting started. Bundled Neo4j Community is a single instance with its own volume; it's what gives you the History page's time scrubber, the audit trail, and "what did this look like an hour ago." If you already run Neo4j elsewhere, point at it instead with `neo4j.mode=external` rather than running two. Either way this is a day-two decision, not something the initial install needs to get right — everything works with the default.

## Before you go further

A short checklist worth running through once the server is reachable the way you want:

- **Registration.** The default (`invite`) is right for anything not on a fully private network. Don't switch it to `open` unless you mean it.
- **Backups.** Everything — including the private key of the CA every agent trusts — lives on one PersistentVolumeClaim. Losing it means re-enrolling every agent. `backup.volumeSnapshot.enabled=true` gives you scheduled CSI snapshots if your cluster's storage supports them; either way, back up that volume on purpose.
- **The CA key passphrase.** `pki.encryptAtRest` defaults to on, and the chart manages the passphrase Secret itself. Back that Secret up separately from the data volume — without it, a backup of the CA key alone is useless.

None of this needs to happen before you connect your first cluster. When you're ready for that, go to [Connecting a cluster](./connecting-a-cluster.md).

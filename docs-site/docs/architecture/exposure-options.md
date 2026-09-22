---
id: exposure-options
title: Exposing the agent port
description: LoadBalancer, NodePort, a TLS-passthrough Ingress, or Gateway API TLSRoute — ways to get real traffic to the agent port.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# Exposing the agent port

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/exposure-options.svg')} alt="Three columns comparing LoadBalancer, NodePort and Ingress TLS-passthrough exposure of the agent port, all preserving an unbroken TLS handshake to the server pod" />
  <figcaption className="diagram-caption">All three preserve one thing: the TLS handshake reaches the pod unbroken. The admin port, below, is the opposite case — it wants TLS terminated in front of it.</figcaption>
</figure>

The agent port speaks mutual TLS gRPC, and the handshake — client certificate, server certificate, the works — has to arrive at the server pod exactly as the agent sent it. Anything that terminates TLS in front of it (an HTTP-aware ingress controller in its default mode, an ALB doing HTTPS-to-HTTP, a service mesh sidecar rewriting the connection) breaks the handshake, usually with an error that doesn't obviously point back at "TLS was terminated somewhere." That one rule is why this port is exposed differently from the UI.

## LoadBalancer (`agent.service.type=LoadBalancer`, the default)

The chart asks your cloud for an external IP or hostname automatically. This is the right default for a managed cluster — EKS, GKE, AKS and similar all provision one without extra setup. If `EXTERNAL-IP` stays `<pending>` after a couple of minutes, there's no load-balancer controller available (common on a bare-metal or fully self-managed cluster); switch to NodePort, or install something like MetalLB to get LoadBalancer support anyway.

## NodePort (`agent.service.type=NodePort`)

Reachable on every node, at a fixed port you choose. This works on literally any cluster — including a local trial with k3s, kind or minikube — which is why it's what the [Quickstart](../installation/index.md) uses. The trade-off is that you're pointing agents at a specific node's address rather than a stable name a load balancer would give you, so it's a better fit for a trial or a small, self-contained deployment than for something agents will dial from far outside a network you control.

## Ingress with TLS passthrough (`agent.ingress`)

Lets the agent port share the same `:443` an HTTP ingress already uses, routed by SNI hostname rather than terminated. This only works if the ingress controller is explicitly told not to touch TLS for this route. With ingress-nginx, for example:

```bash
helm upgrade ingress-nginx ingress-nginx/ingress-nginx --reuse-values \
  --set controller.extraArgs.enable-ssl-passthrough=true
```

Skip that flag and nothing errors loudly — the controller just quietly terminates TLS anyway, and the agent's handshake fails in a way that looks like a networking problem rather than a one-line missing flag. This is the most capable option (no extra load balancer, shares infrastructure you already run) and also the easiest to get subtly wrong, which is why it's marked advanced rather than default.

:::info[ingress-nginx specifically is no longer maintained]
The command above is the ingress-nginx project's own flag, shown because it's the most widely deployed controller. That project reached end-of-life in March 2026 — no further releases, bug fixes or security patches. If you're choosing a controller today, either pick one still being maintained (Traefik, HAProxy Ingress and others all support TLS passthrough with their own annotation or CRD) or use [Gateway API TLSRoute](#gateway-api-tlsroute-agenttlsroute), below, which isn't tied to any one project.
:::

## Gateway API TLSRoute (`agent.tlsRoute`)

The Gateway API equivalent of the option above: a `TLSRoute` attached to a `Gateway` listener configured with `protocol: TLS` and `tls.mode: Passthrough`, routed by SNI exactly like the Ingress option. It needs the Gateway API CRDs and a Gateway already running in the cluster — if you don't have either yet, LoadBalancer or NodePort are the simpler starting points.

```bash
helm upgrade continuum oci://ghcr.io/alexandrosst/continuum-server \
  --namespace continuum --reuse-values \
  --set agent.tlsRoute.enabled=true \
  --set-json agent.tlsRoute.parentRefs='[{"name":"shared-gateway","namespace":"gateways","sectionName":"agents-tls"}]' \
  --set-json agent.tlsRoute.hostnames='["continuum.example.com"]'
```

TLSRoute graduated to Gateway API's stable Standard channel in v1.5 (April 2026); check which `apiVersion` your specific Gateway implementation actually serves it at (`agent.tlsRoute.apiVersion` defaults to `gateway.networking.k8s.io/v1alpha2` for the widest compatibility) before relying on a newer one. Unlike ingress-nginx's flag above, this isn't any single project's feature to deprecate out from under you — it's part of the Gateway API spec itself, implemented by whatever Gateway controller you're already running (Envoy Gateway, Istio, Cilium, NGINX Gateway Fabric, and most cloud-managed Gateways all support it).

The admin/UI port has the same choice available: `httproute` is the Gateway API `HTTPRoute` alternative to `ui.ingress`, covered in [Production cluster](../installation/production-cluster.md#exposing-the-ui-properly).

## Meanwhile, the admin port

None of the above touches `:8080` at all — that one is a completely separate decision, and it wants the *opposite* treatment: plain HTTP inside the pod, sitting behind a normal Ingress that **does** terminate TLS (or serving TLS itself via `admin.tls`, if you'd rather skip a proxy entirely). See [Production cluster](../installation/production-cluster.md) for the actual commands.

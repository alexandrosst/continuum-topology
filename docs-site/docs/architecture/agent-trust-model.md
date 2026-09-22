---
id: agent-trust-model
title: Agent trust model
description: How an agent goes from nothing to a certificate the server trusts, step by step.
---

import useBaseUrl from '@docusaurus/useBaseUrl';

# Agent trust model

<figure className="diagram-figure">
  <img src={useBaseUrl('/img/diagrams/agent-trust-model.svg')} alt="Six-step enrollment flow from clicking Connect a cluster in the UI through to an established mutual-TLS channel" />
  <figcaption className="diagram-caption">Nothing here needs an inbound firewall rule on the cluster's side — every network connection is opened by the agent, outward.</figcaption>
</figure>

## Why two secrets instead of one

A token alone would prove that *someone* was allowed to install an agent — it says nothing about whether the agent that actually showed up is the one you meant. If two clusters are enrolling around the same time, or a token leaked, the wrong request could get approved by mistake. That's why enrollment needs two things that never travel together:

1. **The enrollment token**, in the `helm install` command the UI prints. It proves authorization to attempt enrollment at all.
2. **The approval code**, which never appears anywhere the token does. The agent generates it locally on first contact and prints it *only* to its own pod log — never to the server, never to the UI. The server only ever receives a hash of it (Argon2id, bound to the agent's own public key), so it can check a submitted code without ever having seen the real one.

An administrator has to go read that code from the cluster's own logs and type it into the approve dialog. That step is what turns "someone had a valid token" into "I confirmed this specific agent is the one running where I expect."

## The six steps

1. **You click "Connect a cluster"** in the UI. Nothing exists on the cluster side yet.
2. **The server mints a one-time token** — valid one hour, one use — and prints a ready-to-run `helm install` command with its CA pin already embedded.
3. **You run that command** against the target cluster. Nothing to fill in by hand: the chart reference, image and CA pin are all already correct.
4. **The agent starts, dials the server's `:8443`, and checks the pin.** Before it trusts anything the server sends back, it verifies the server's certificate against the CA pin from the install command — first contact is protected against a wrong pin, wrong host, or a foreign CA entirely.
5. **The server verifies the token and signs the agent a certificate** from its own private CA. The token is consumed here — reusing it fails.
6. **Every connection after this is mutual TLS.** Both sides present certificates; there's no shared secret sitting on disk anywhere, and no inbound rule needed on the cluster's firewall, because the agent is the one dialing out, every time, for the life of the connection.

Certificates last 24 hours and renew automatically at the halfway point. An agent that's been offline for less than 7 days re-establishes on its own with no new token needed; longer than that, or if the certificate is explicitly revoked, it needs to enroll again from step 1.

## Revocation

Revoking an agent is immediate and checked on every call — a live stream drops at once. It's permanent by design: a revoked agent's pod will keep restarting and trying (Kubernetes doesn't know it's been told "no"), but it exits promptly every time with a clear reason in its log, backing off to a few attempts an hour rather than crash-looping. The fix is always the same: `helm upgrade` with a fresh token, which clears the revoked marker and enrolls fresh from step 1.

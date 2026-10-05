---
id: trust-model
title: Trust model
description: One assembled answer to "should I trust this with my infrastructure" — agent enrollment, regional operators, the quick-start gateway token, and what the model deliberately does not cover.
---

# Trust model

This page exists for one question: *should I trust this software with my infrastructure?* The honest
answer is split across several places that were each written for a narrower purpose — [Agent trust
model](./agent-trust-model.md) for the enrollment mechanics, [Regional operators](./regional-operators.md)
for the second fan-in tier, and the repository's own `SECURITY_REVIEW.md` for an independent adversarial
pass over the whole codebase. This page pulls the parts of all three that matter for that one question
into a single story, in the order a cluster's trust actually gets established, narrowed, and (sometimes)
deliberately left alone. Each section below links back to the deeper page it was drawn from; nothing here
replaces those pages, it just assembles them.

## The agent lifecycle

An agent goes from nothing to a server-trusted certificate in six steps, and the two secrets involved are
deliberately kept apart so that "someone had a valid token" and "I confirmed this specific agent is the one
running where I expect" are two different, separately-checked facts:

1. **Enroll.** The UI mints a one-time, one-hour enrollment token and prints a ready-to-run `helm install`
   command with the server's CA pin already embedded.
2. **Approve.** The agent generates its own approval code on first contact and prints it *only* to its own
   pod log — never to the server, never to the UI. The server only ever receives an Argon2id hash of it,
   bound to the agent's own public key. An administrator has to go read that code from the cluster's logs
   and type it into the approve dialog; that's what turns "someone had a token" into an actual human
   decision about this specific cluster.
3. **Certificate.** The agent dials the server, checks the CA pin from its own install command before
   trusting anything back, and the server signs it a certificate from its private CA once the token and
   approval are both verified. The token is consumed at this point — reusing it fails.
4. **Tier enforcement.** Every connection after this is mutual TLS, and what an agent is actually allowed
   to report is governed by three tiers, always checked in this order: the chart's own `access.tier` is a
   ceiling only a `helm upgrade` on the cluster's own side can raise; an administrator's **approved** tier
   can sit at or below that ceiling; the agent's own **effective** reporting sits at or below what's
   approved. The server can narrow this at any time and can never widen past the installed ceiling — the
   agent checks that for itself rather than trusting the server to ask nicely, and the server independently
   drops anything above the approved tier from what it's sent, so a buggy or hostile server gains nothing by
   trying to ask for more. See [Observability intent](./observability-intent.md) for the two further
   narrowings (paused collectors, excluded namespaces) layered on top of tier.
5. **Revocation.** Immediate and checked on every call — a live stream drops at once, and it's permanent by
   design. A revoked agent's pod keeps restarting (Kubernetes doesn't know it's been told no) but exits
   promptly every time with a clear reason logged, backing off to a few attempts an hour. The only way back
   in is `helm upgrade` with a fresh token, starting over from step 1.
6. **Rejoin.** Certificates last 24 hours and renew automatically at the halfway point. An agent that's been
   offline for less than 7 days re-establishes on its own with no new token needed; longer than that, or if
   it was explicitly revoked, it has to enroll again from scratch.

Full detail, including why two secrets rather than one and exactly what each RPC re-checks, is in [Agent
trust model](./agent-trust-model.md).

## Regional operators: absence of a credential, not absence of a network path

A regional operator is a second, explicit fan-in tier above per-cluster telemetry — a standalone
`otel-contrib` collector that receives what a set of already-approved clusters already export, and
re-exports it as one stream. The important property, worth stating precisely rather than loosely: **it
never dials the Continuum server, because it has nothing that could — not because a firewall stops it.**
The chart ships zero Continuum client code; nothing in it knows the control-plane API exists, holds a
client certificate, or has any credential the server would accept over the agent port. That's a barrier
made of an absent capability, not a network boundary.

That holds by default. There is one opt-in exception, off unless `heartbeat.enabled` is set: the operator's
collector can POST the result of probing its own health endpoint to a single server URL, so the UI can show
online/offline. It is not a client of the control plane: it uses its own secret, separate from the receiver
token, that the server accepts at that one endpoint and nowhere else (not the agent port, not the admin
API), the request carries no telemetry and none of what the operator relays, and the server discards its
body. See [Regional operators § Optional heartbeat](./regional-operators.md#optional-heartbeat-online-offline-last-seen).

It follows directly that **egress isn't network-isolated by default**. Like its sibling chart's own
`telemetry.receiver.networkPolicy`, this chart's egress `NetworkPolicy` is off unless an operator turns it
on (`networkPolicy.egress.enabled`), the same "the right answer depends on your CNI, and a wrong policy
silently cuts export off" reasoning the chart's own comments give. So a compromised regional-operator pod
is free to *attempt* outbound TCP to anywhere, including the server's agent port — it just has nothing the
server's mTLS handshake would accept. The server rejecting a connection with no valid client certificate is
doing real work here; a NetworkPolicy, if you turn one on, is defense in depth on top of that, not the thing
actually preventing it today.

What it checks is on its own OTLP input only: for an operator created today, the mTLS client certificate a
source cluster presents - signed by THAT operator's own private CA (minted with the operator, signing the
receiver's certificate and every client certificate, its key sealed like the CA key above and erased on
revoke), required by the receiver, and the only gate (no bearer token exists). A certificate from another
operator's CA, or the server-wide CA, is rejected. For an older operator, or one whose certificates could
not be minted, a receiver bearer token minted once at creation, of which the server keeps only a hash. An
mTLS operator created before per-operator CAs (`clientCaScope: "org"`) is the exception and is weaker: its
receiver trusts the server-wide CA, which every organisation shares and which is checked by chain, not by
which operator a certificate was issued for, so any certificate that CA signed is accepted until the
operator is recreated. (Plus, only if the
heartbeat above is turned on, a second secret that opens that one endpoint and nothing else, handled the
same way as a token.) It also gets no Kubernetes API access of its own
(`automountServiceAccountToken: false`, no `ClusterRole`), because relaying already-exported telemetry never
needs to watch this cluster's object graph the way a cluster's own collectors do. Full detail, including how
scope is assigned and why chaining operators is rejected in this release, is in [Regional
operators](./regional-operators.md).

## The quick-start gateway token: an accepted tradeoff, not an oversight

The quick-start flow lets an administrator stand up a backend (Jaeger, Zipkin, Prometheus, or Loki) in-cluster and
front it with a minimal `nginx:alpine` gateway so it isn't open to anything that can reach the Service
directly. That gateway is Part C of quick-start (`src/lib/quickStartGateway.ts`); the token it checks is
minted by the server (`backend/internal/server/quickstart.go`, `admin_quickstart.go`) and handed to the
admin exactly once, the same convention every other secret in this app follows (enrollment tokens, operator
receiver tokens).

**What it actually is:** a random 32-byte secret (`cnq_...`), stored by the server only as a hash
(`tokens.go`) and minted with a TTL that defaults to 24 hours and is clamped into `[5m, 7d]`
(`DefaultGatewayTokenTTL`, `MinGatewayTokenTTL`, `MaxGatewayTokenTTL` in `quickstart.go`). There is no path
in `store.Store` to revoke one — the interface has exactly `CreateGatewayToken` and `LatestGatewayToken`
(`store/store.go:372-377`) and nothing else.

**What checks it:** nothing on the Continuum server. The plaintext token is templated directly into a
static nginx `ConfigMap` at generation time, as a literal string an `if ($http_authorization = "Bearer
<token>")` block compares against. Continuum's own role ends the moment that manifest is generated; from
then on, the admin's own gateway pod is the entire enforcement mechanism, and the server never sees that
traffic or dials the fronted backend itself.

**What its blast radius actually is, precisely:** whatever single backend Service the gateway was generated
for — never more than one, and never anything Continuum itself holds. For Jaeger (port `16686`, the
http-query service) and Prometheus (port `80`→`9090`, the query/web API), that's read/query access to that
one tool. For Loki, it's worth being exact rather than assuming "read-only" across the board: Loki serves
both its query API *and* OTLP log ingestion (`/otlp`) on the same port `3100`, and the gateway fronts that
whole Service — so a leaked Loki quick-start token can write (ingest) as well as read, not just read. In
every case the ceiling is still just that one backend, bounded by the token's TTL, with a hard cap of 7
days.

**The accepted tradeoff:** a leaked quick-start gateway token is valid until it expires, and there is
currently nothing Continuum itself can do to shorten that — the only way to invalidate it early is for the
admin to manually redeploy the gateway (re-mint a token in the UI, re-apply the generated manifest, which
overwrites the `ConfigMap`'s literal comparison string). This is named here as a conscious decision for this
project's current stage — a single operator fronting their own deployment, not a multi-tenant SaaS handing
out credentials across organizations — rather than a bug to quietly patch. If a future need calls for real
revocation, the shape of it is: a store-level revoke call (marking a `GatewayToken` invalid before its
`ExpiresAt`), plus some way for the gateway's running nginx config to actually learn about that without a
person redeploying it — a short poll against a status endpoint, or a redeploy webhook triggered by the
revoke call. Neither is designed here; this just names what the gap would need filled.

## The CA private key: bare binary vs. the chart you actually install

`internal/pki`'s own warning, logged whenever `--ca-key-passphrase-file` isn't set, says the CA private key
is stored **UNENCRYPTED on disk** (mode `0600`) — true, and worth taking seriously for a bare `continuum-server`
binary run without that flag. It is not, however, what most people actually get: the Helm chart installs
with `pki.encryptAtRest: true` by default, managing an auto-generated passphrase `Secret` itself and
encrypting the key (Argon2id + AES-256-GCM) the first time the server starts against it. That default is
already called out in [the production-cluster install guide](../installation/production-cluster.md) — the
point here is just to say plainly, in one place, that the scary-sounding warning in the logs is describing
the bare-binary path, not the chart-installed one most readers of this page will actually run.

## What this model deliberately does not do

None of this is a defensive disclaimer bolted on after the fact — each item below is a real, specific edge
this model stops at on purpose, the same way the rest of this project's docs name what they don't cover
rather than letting it go unsaid:

- **It doesn't protect a compromised node's root user from its own agent's secrets.** Once something has
  root on a node, it can read whatever credential the agent has already decrypted into its own process
  memory — its mTLS private key, its poll secret. This model protects what crosses the network between an
  agent and the server; it makes no claim about a fully compromised node.
- **It doesn't substitute for the approving human's judgment.** The approval code proves *this specific
  agent is the one actually running where I expect* — it says nothing about whether that cluster should be
  trusted at all. A careless or compromised approver can wave a cluster through that shouldn't be connected,
  and nothing in the mechanics catches that.
- **A regional operator's isolation is a missing credential, not a network wall**, as above: with egress
  `NetworkPolicy` off by default, a compromised operator pod can still attempt outbound calls to the server;
  it just has nothing the handshake would accept.
- **The quick-start gateway token can't be revoked before it expires**, as above — up to 7 days of exposure
  from a leaked token, closeable today only by a manual gateway redeploy.
- **The CA key is unencrypted by default outside the chart**, as above — real protection depends on either
  the chart's default or deliberately setting `--ca-key-passphrase-file` yourself.
- **The audit trail proves internal consistency, not tamper-proofing against raw file access.** The hash
  chain in `internal/store/auditchain.go` catches accidental corruption and a live tampering attempt made
  through the API, but by its own design it cannot detect a rewrite performed by someone with direct access
  to the SQLite database file itself — that needs an external anchor (a signed export, a value recorded
  somewhere the server can't reach), which nothing here automates yet.

None of these are urgent fixes hiding in plain sight — they're the boundary of what this specific trust
model was built to cover, stated so a reader doesn't have to discover it the hard way.

For the exact RBAC, Linux capabilities and host access each workload asks for — scope by scope, with
citations into the chart templates — see [Permissions reference](./permissions-reference.md).

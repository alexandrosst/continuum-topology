---
id: deferred-work
title: Deferred work
description: What was found or designed but deliberately not done yet, with the reason, so it can be picked up later.
---

# Deferred work

Things that were found, designed or discussed and left for later on purpose. Each entry says why it was left, so it can be judged again rather than rediscovered. Newest sections last.

## Certificates and operators

- **Per-sender certificate expiry is not watched.** The expiry check and "Renew certificates" look at the operator's own dates, not at the certificates issued per source cluster through telemetry intents (`/command`) or for upstream operators that export into this one. The ledger (`operator_certs`) already has every `not_after`; the client date should be the earliest of the newest certificate per (operator, sender), and the check and `certState` should use it. Best done together with automatic renewal.
- **Automatic certificate renewal.** Recommended next. Today renewal is "run the install command again".
- **Gateway stamping and enforcement of the certificate name.** Each cluster's certificate carries `<operator>-export-<cluster>`, but the receiver does not check it, so a certificate proves "some sender of this operator", not which cluster. Needs a custom collector.
- **Full CA rotation.** Per-operator CA keys are sealed under the passphrase in force at creation and are not re-sealed when the passphrase changes; plaintext rows made before a passphrase was set stay plaintext. Re-seal on startup or at reissue.
- **No revocation list.** Revoke and renew only stop new issuance; a running receiver keeps trusting its CA until the pod is replaced. The UI text should say so.
- **Regional-operator address reporting**, and **IPv6 kubelet brackets**.
- **Reissue ordering.** Per-sender ledger and audit rows are written before the final audited update; a failure part-way leaves certificates recorded that nobody holds. Write the audit first and the ledger last.
- **Operator CA creation** does two 64 MiB Argon2id derivations with no concurrency cap; seed the CA cache with the issuer returned at creation and run sealing through the same slot semaphore.
- **`/command` and scope edits mint a new certificate every call**, and the ledger list is unbounded. Mint only when the destination changed; paginate; mark ledger rows of revoked operators.
- Smaller: name length is counted in bytes not characters and control characters are accepted; numeric shorthand hosts such as `127.1` pass address validation; the alert-level write is not compare-and-set.

## FUSION

- **Grafana shares an origin with the Ikhnos UI** and renders data that comes from monitored clusters. Serve the pages from a separate origin (the ticket flow already supports a cross-origin hop), or at least default Grafana off.
- **Grafana's datasource proxy** is allowed with write methods; confirm Loki's push and delete endpoints are not reachable through it, or deny `/api/datasources/proxy/` for non-GET.
- **FUSION access tokens outlive their creator's admin role** (up to 365 days); revoke a user's tokens when they lose the role, or check the creator at use.
- **No per-caller fairness** on the shared data client (8 in-flight calls): one token can starve the rest.
- Upstream and Kubernetes error text is returned to API clients; return a generic message and log the detail.
- **Components enabled after first install stay at 0 replicas** under `switch.managed` until FUSION is toggled.
- **Storage cannot be resized with `helm upgrade`** (`volumeClaimTemplates` are immutable) although the values comments say to adjust `storage`.
- The batch size limit counts items, not bytes; large items can exceed the 4 MiB receive limit and be dropped.
- Agent telemetry collectors have no liveness or readiness probe, and the host `filelog` uses `start_at: end` without a storage extension, so every restart leaves a gap in container logs.
- The central TLS private key is copied into Helm release history by the `lookup` idiom.

## Commands and charts

- **The operator chart file is never offered for download** in the default setup (no registry, no `--chart-ref`): the printed command names `./continuum-regional-operator-<v>.tgz` but nothing tells the user to fetch it. Expose it in `info` and add a step.
- Several chart value blocks are dereferenced without a `default` (heartbeat, health, export.queue, export.routes, tls, telemetry.\*). The printed commands now use `--reset-then-reuse-values`, which hides this for upgrades through the product, but Argo CD or Flux users with older stored values can still hit nil-pointer errors. Guard with `dig` and add a render test with each block removed.
- Chart-reference and version logic exists four times on the server and twice in the browser; fold into one helper.
- The server chart's `agentInstall.imageRegistry: "none"` is documented but the binary exits on it; the chart has no `imageDigest` value.
- `kubectl create secret`-style commands in the browser still put a credential in `--from-literal`; move them to the same here-document form the server uses.
- The uninstall command leaves the receiver TLS, receiver auth and heartbeat Secrets behind.

## Interface

- Keyboard and screen-reader gaps: `RowMenu` has `role="menu"` but no arrow-key handling or focus move; category tabs have no `tabpanel`.
- The auto-reload on a stale chunk cannot protect a dialog that sits under the same error boundary that catches the chunk error.
- "Operator health" and "Created" columns freeze between data changes.

## Not yet verified on a live cluster

Grafana proxy authentication end to end; NetworkPolicy behaviour on a real CNI; hot reload of a renewed mounted Secret; a real two-cluster mTLS handshake; live status dots; Renew certificates reaching a running operator; revoke with dependents.

## Found while testing the telemetry path end to end (real collector and Prometheus binaries)

- **Failures are quiet.** A wrong CA, wrong client certificate, wrong server name or a down Prometheus produce only an `info` line "Exporting failed. Will retry" (the cause is in its `error` field); `otelcol_exporter_send_failed_metric_points` stays absent for the 30 minute retry window; the gateway logs nothing for a rejected client certificate; the health check is a plain `GET /`, so readiness stays green with a dead exporter. The collectors' own `:8888` metrics are on loopback unless the chart sets them (central has no `service.telemetry`; the operator has `selfMetrics.enabled: false`). Add a pull reader to the central config, enable self metrics by default, and show "export failing" in the UI from the operator health.
- **The command that points a cluster at an operator states only the destination.** Rendered with just those flags the agent chart deploys no telemetry workload; nothing is sent until signals are enabled. The screen should say so where the command is shown.
- **An operator address ending in `:4318`** (the OTLP/HTTP port) can never work with the product's gRPC exporters unless a load balancer maps it; warn when one is saved.
- Collector 0.160 logs deprecation warnings for the `otlp`, `otlphttp` and `hostmetrics` component aliases (`otlp_grpc`, `otlp_http`, `host_metrics`); switch the templates before the aliases are removed.
- Fixed in the same pass: FUSION's "waiting for first data" and the starter dashboard read `target_info`, which Prometheus does not write for infrastructure metrics; and series that differed only in an unpromoted resource attribute (container, StatefulSet, DaemonSet, Job, volume) were merged.

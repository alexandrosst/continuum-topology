---
id: regional-operators-lifecycle
title: Running regional operators - certificates, removal and delivery
description: How a regional operator's certificates are tracked and renewed, what revoking or deleting one does to what depends on it, how data is delivered when something is down, and what the system logs setting does and does not collect.
---

# Running regional operators

This page is about the long run: what expires, what removal touches, and what happens to data when a hop is down. How an operator is created and reached is in [Regional operators](./regional-operators.md); the bundled store and its web pages are in [Grafana and the web pages in FUSION](./fusion-grafana.md).

## Certificates: tracked, renewed from one button, reloaded without a restart

An operator authenticated by mutual TLS has three dates that matter: the receiver certificate, the client certificates its senders present, and the operator's own CA. The server records them when it issues the certificates and shows the worst of them on the operator's row: nothing while all are comfortably valid, an amber "Certificate expires in N days" within 60 days, and a red "expired" once one has passed. Operators that predate the record show a date derived from their creation time and their CA; bearer-token operators have no certificates and show none.

A daily check (and one at every start, so a server that was down for weeks catches up) writes one `operator-cert-expiring` audit entry as each operator crosses 60, 30 and 7 days and again when it has expired, never more than once per threshold.

**Every sender has its own certificate.** A client certificate names the one cluster (or operator) that holds it, as `<operator>-export-<cluster>`, and is issued for that holder alone: two clusters never share a private key, and a key that leaks is known to belong to one cluster. The server keeps a ledger of what it issued: serial, who holds it, who issued it, and when it ends, never the certificate or its key. **Issued certificates** on the row (or `GET /operators/{id}/certificates`, administrators only) shows it, so "which of my clusters can still send here" has an answer. Certificates issued before the ledger existed are not in it.

What this does not do: the receiver accepts any certificate signed by the operator's CA, and the stock collector does not expose the certificate's name to its pipeline, so the name is a record and not yet something the receiver enforces; nor is there a revocation list, so a certificate stops working when it ends, or when the operator is revoked and its release removed. Closing both is a separate piece of work; the next step that earns its cost is renewing certificates automatically, not revoking them one by one.

**Renew certificates** on the row (or `POST /operators/{id}/install`, administrators only) issues a fresh receiver certificate, and a new client certificate for each source cluster, from the operator's stored CA and returns the same ordered commands the creation screen showed, plus a restart command. An operator with a bearer token or a heartbeat secret gets a new one, because the server keeps only a hash and cannot show the old one again; the screen says so before you run anything. It is audited as `operator-install-reissued`. The CA itself is valid five years and is not renewed: that is a recreate, and the row says when it is coming. The central operator's CA is watched the same way: FUSION's daily renewal covers its receiver certificate only.

**Why a new certificate takes effect without a restart.** Every TLS block in the charts (the receiver, and every exporter that presents a client certificate) has `reload_interval: 1h`, so the collector re-reads the mounted files. A new `ca.crt` is the one exception: the trust anchor is read at start, so a changed CA needs a rollout restart. The chart also puts a `checksum/mtls` annotation on the pod, computed from the Secrets it mounts when Helm can read them, so re-running the install command with renewed Secrets rolls the pod by itself. Under `helm template` (GitOps) there is nothing to read and the annotation is left out; set `rolloutOnSecretChange=false` for accounts that may not `get` Secrets, and restart the workload yourself after changing them.

## Revoking and deleting: what depends on it

An operator can be the destination of other operators and of clusters' telemetry intents. Revoking or deleting one that is in use would leave them sending to nothing, so the server answers **409** and names what depends on it: the other operators by name, the number of intents and of clusters. The dialog shows the same list and asks you to confirm explicitly (`force`) before it proceeds; the audit entry records the counts.

Revoking stops the server from handing out commands for the operator and erases its CA key. It does **not** stop the data plane: the receiver in the cluster keeps trusting the CA it was installed with until it is removed. Both responses therefore include the `helm uninstall` command for the receiver, and the screen shows it after the action.

## Source clusters are optional

An operator is a place to send to. It can be created with no source clusters at all and clusters pointed at it afterwards, from **Connect a cluster** on its row or from the telemetry wizard's destination step, which issues each one its own client certificate. When you do name sources at creation, the commands for them carry one Secret per cluster, each with that cluster's own certificate.

## Delivery when something is down

Every exporter in the agent, regional operator and central gateway chart has a bounded `queue` (`queue.size`, 256 batches by default) and retries for `queue.retryMaxElapsedTime` (30 minutes) before giving up on a batch. A short outage of the next hop therefore loses nothing; a full queue makes the exporter refuse, and the back-pressure reaches the senders through the collector's memory limiter, whose own queues hold the data, so nothing grows without bound until the pod is killed. One queue entry is one batch (2048 items, at most 4096, or five seconds), so raise `queue.size` together with the memory limit. The central gateway and the regional operator set `GOMEMLIMIT` to 80 % of the container limit, so the collector's limiter acts before the kernel does. `queue.persistent.enabled` (off by default) also writes the queue to an `emptyDir`, so a container restart does not drop what was waiting; it does not survive a rescheduled pod, and it is not a substitute for sizing the store.

The central gateway converts delta metrics to cumulative before batching (`delta_to_cumulative`, as Prometheus stores cumulative series), and Prometheus is started with a size cap, `--storage.tsdb.retention.size`, at 85 % of its volume so a full disk is not how retention ends. Set `prometheus.retentionSize` to override it or to `0` to turn the cap off. The sizing guide is in the chart's `values.yaml`.

## System logs and scope

The **System logs** signal reads the node's container logs. It now respects the scope you chose: pods in excluded namespaces are not read, and the release's own pods (`continuum-*`) are always excluded, so debug output can never feed itself back in. Records with no namespace (the journal, if you enable it) pass through. `journaling` defaults to `none`: the pinned collector image is built from scratch and has no `journalctl`, so journald needs a `collectorImage` that ships it.

## Kubelet metrics and self-signed kubelets

Resource usage and Node runtime read the kubelet. The collector now dials the node's own address (`status.hostIP`) and verifies the kubelet's certificate against the cluster CA. Where kubelets serve a self-signed certificate (kubeadm and k3s defaults) the metrics will not arrive until you either enable serving-certificate rotation on the kubelets or set `telemetry.kubelet.insecureSkipVerify=true`, which is off by default because it lets anyone between a pod and its node read the pod's service-account token. A cluster with an IPv6-only node network needs the address bracketed, which the chart does not do yet.

## The heartbeat address

A regional operator's heartbeat reports to the server's public address: set `admin.publicURL` in the server chart (for example `https://ikhnos.example.com`). Without it the address is whichever host your browser used, which is wrong behind a port-forward or an internal name, and the screen says so for `localhost`, `.svc` and private addresses. When the server's own certificate is signed by a private CA, mount the CA file and set `admin.heartbeatCAFile`: the operator's commands then include a Secret with that CA and `heartbeat.tls.caSecretName`.

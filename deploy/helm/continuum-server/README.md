# continuum-server

Helm chart for the Continuum **server**: the control plane that agents report to, and the web UI. (The agent is a different chart, `continuum-agent`, whose install command the server's *Connect a cluster* screen prints.)

The full deployment guide, with diagrams, backup and restore, upgrades and troubleshooting, is [`deploy/README.md`](../../README.md). This file documents the chart itself.

```console
helm install continuum-server deploy/helm/continuum-server \
  --namespace continuum --create-namespace \
  --set image.repository=REGISTRY/server \
  --set agent.publicAddress=continuum.example.com:8443 \
  --set httproute.enabled=true \
  --set-json 'httproute.parentRefs=[{"name":"shared-gateway","namespace":"gateways","sectionName":"https"}]' \
  --set-json 'httproute.hostnames=["continuum.example.com"]'
```

Requires Kubernetes >= 1.25 and Helm 3 (developed and tested with 3.16). The chart validates its values (`values.schema.json`, plus checks in `templates/_helpers.tpl` that say which value to set) and fails before creating anything.

## What it deploys

| Object | When | Notes |
|---|---|---|
| Deployment `<fullname>` | always | 1 replica, `strategy: Recreate`, non-root 65532, read-only root, all capabilities dropped, no service-account token |
| PersistentVolumeClaim `<fullname>-data` | unless `persistence.existingClaim` | `/data`: SQLite and the enrollment CA key. Kept on `helm uninstall` |
| Service `<fullname>` | always | admin API and UI (ClusterIP, port 8080) |
| Service `<fullname>-agent` | always | agent gRPC over mutual TLS (LoadBalancer by default): L4 only |
| ServiceAccount | `serviceAccount.create` | bound to nothing |
| HTTPRoute `<fullname>` | `httproute.enabled` | UI, TLS terminated at the Gateway |
| TLSRoute `<fullname>-agent` | `agent.tlsRoute.enabled` | TLS **passthrough** for agents |
| NetworkPolicy `<fullname>` | `networkPolicy.enabled` | ingress on the two ports; egress DNS, Neo4j, deciders |
| PodDisruptionBudget | `podDisruptionBudget.enabled` | off: see values |
| StatefulSet, headless Service, Secret, NetworkPolicy `<fullname>-neo4j` | `neo4j.mode=bundled` | official `neo4j` Community image |
| CronJob + ServiceAccount + Role + RoleBinding `<fullname>-backup` | `backup.volumeSnapshot.enabled` | scheduled CSI VolumeSnapshots |
| anything in `extraObjects` | optional | templated with `tpl` |

## Things to know before you install

* **Two listeners, two kinds of exposure.** The agent port speaks gRPC over mutual TLS with the server's *own* CA, and agents pin that CA. TLS must reach the pod untouched: use the LoadBalancer (default) or NodePort Service, or a Gateway API TLSRoute in passthrough mode (`agent.tlsRoute`). Never an HTTP/L7 proxy. The admin port is plain HTTP in the pod and expects a TLS-terminating Gateway in front (`httproute`) or its own certificate (`admin.tls.secretName`). With neither, the chart generates and manages a self-signed certificate itself (`admin.tls.selfSigned`, on by default) rather than refusing to install - real HTTPS for a trial or LAN install with zero external dependencies, at the cost of a one-time browser warning. `admin.behindTlsProxy=true` is the explicit "I know, plain HTTP is fine" switch instead (use it with `kubectl port-forward` to `localhost`, no cert warning at all).
* **`agent.publicAddress` is required** and is exactly what agents dial. It is passed as `--agent-address` and its host becomes a name in the server certificate; `agent.extraHosts` adds more (`--agent-hosts`).
* **One replica, one volume, and it is not scalable.** The server keeps SQLite in `/data` (one writer), agent sessions in memory and the CA on disk. `replicas` is deliberately not a value. `Recreate` is used because a ReadWriteOnce volume cannot be attached to two pods, so an upgrade has a short outage; agents reconnect by themselves.
* **`/data/pki` holds the CA key that every enrolled agent trusts.** Lose it and every agent has to be enrolled again. Back it up (deploy/README.md). The PVC carries `helm.sh/resource-policy: keep`.
* **No admin password in the chart.** On a fresh database the server prints a one-time password for the user `admin` to its log; `NOTES.txt` prints the exact `kubectl logs` command. Alternatively `admin.existingPasswordSecret` feeds `CONTINUUM_ADMIN_PASSWORD` (first start only).
* **`registration` defaults to `invite`**, never `open`.

## Server version compatibility

The chart only renders flags the server understands **when you ask for the feature**. Flags marked *newer* do not exist in every server build; a build that does not know one exits at start with `flag provided but not defined`.

| Value | Flag | Availability |
|---|---|---|
| always | `--data-dir --ui-dir --agent-listen --agent-address --admin-listen --registration --org` | all builds |
| `agent.extraHosts` | `--agent-hosts` | all builds (there is no `--extra-hosts`) |
| `admin.behindTlsProxy` / `admin.tls.*` | `--admin-behind-tls-proxy`, `--admin-tls-cert/-key` | all builds |
| `agentInstall.*` | `--image-registry --image-tag --chart-ref` | all builds |
| `geoip.path` | `--geoip-db` | all builds |
| always (`neo4j.mode` is mandatory: `bundled` or `external`) | `--neo4j-url --neo4j-database --neo4j-password-file`, env `CONTINUUM_NEO4J_USER` | all builds |
| `decider.allowCIDRs` | `--decider-allow-cidrs` | builds that have it (present in this tree) |
| `pki.caKeyPassphraseSecret.name` | `--ca-key-passphrase-file` | **newer**: a server that adds it (not in the tree this chart was written against) |
| `neo4j.allowInsecureHttp=true` | `--neo4j-allow-insecure-http` | **newer**: same |

A server that will refuse plain `http://` to a non-loopback Neo4j (which includes the bundled one, in the cluster) needs `neo4j.allowInsecureHttp=true` (or an `https://` external Neo4j). Today's servers only log a warning. Set the value when you move to a build that has the flag.

## Neo4j

Neo4j is mandatory in this chart: `neo4j.mode` must be `bundled` or `external`, and rendering fails with `neo4j.mode must be bundled or external` if it is anything else (there is no `none`). Without Neo4j, topology history, events, the audit trail and workspace revisions are degraded or absent, so every deployment made from this chart has one. (The server binary itself can still run with no `--neo4j-url` at all, degrading gracefully, for a quick local trial outside this chart — see the top-level `deploy/README.md`.)

| `neo4j.mode` | What happens |
|---|---|
| `bundled` (default) | a StatefulSet running `neo4j:5.26.30-community` with its own PVC, a headless Service `<fullname>-neo4j`, and a generated password Secret (`randAlphaNum 32`, kept across upgrades with `lookup`, kept on uninstall) unless you give `neo4j.auth.existingSecret`. The server gets `--neo4j-url=http://<svc>.<ns>.svc:7474` and reads the password from a mounted file (`--neo4j-password-file`); no password is ever a command-line argument |
| `external` | `neo4j.external.url` plus a Secret with the password (and optionally the user name). https, or an in-cluster/loopback http address; anything else is refused unless `neo4j.allowInsecureHttp=true` |

Neo4j **Community edition is licensed GPLv3 and is a single instance** (no clustering). It runs as a separate process and the Continuum server only talks to it over HTTP; you are the one distributing or modifying it if you do. Use your own Neo4j (`external`) if that matters to you or if you need Enterprise features.

The Neo4j image is built for uid 7474; the chart runs it as 7474 non-root with `fsGroup: 7474` and does not set a read-only root file system (the image's entrypoint writes its generated configuration there). `NEO4J_AUTH` is built from the same Secret and only takes effect when the data volume is first initialised. Rotate the password later inside Neo4j (`ALTER CURRENT USER SET PASSWORD`) and update the Secret to match.

`lookup` does not work under `helm template` (Argo CD, Flux with `helm template`, `--dry-run=client`): a generated password would change on every render. In GitOps, create the Secret yourself and set `neo4j.auth.existingSecret`.

## Backups

`backup.volumeSnapshot.enabled` creates a CronJob that takes CSI `VolumeSnapshot`s of the data volume (and optionally the bundled Neo4j volume), waits for `readyToUse`, then prunes down to `retain`. It needs snapshot CRDs and a `VolumeSnapshotClass`. A snapshot is crash-consistent, which SQLite in WAL mode recovers from cleanly; it does not survive losing the storage system, so copy snapshots out or take a cold backup as well.

There is no copy-the-SQLite-file job on purpose: a live `cp` of `continuum.db` misses what is still in the `-wal` file, and a second pod cannot mount a ReadWriteOnce volume on another node. See deploy/README.md for the cold backup and restore procedures.

## Values

Everything is documented in `values.yaml`; these are the ones you are likely to touch.

| Value | Default | Meaning |
|---|---|---|
| `image.repository` / `tag` / `digest` | `continuum/server` / appVersion / `""` | image; a digest renders `repo@sha256:...` and wins over the tag |
| `imagePullSecrets` | `[]` | registry credentials |
| `registration` | `invite` | `invite`, `closed` or `open` |
| `org` | `default` | id of the first organization (fresh database only) |
| `agent.publicAddress` | *required* | `host:port` agents dial |
| `agent.extraHosts` | `[]` | more certificate names |
| `agent.port` | `8443` | agent listener in the pod |
| `agent.service.type` | `LoadBalancer` | `LoadBalancer`, `NodePort`, `ClusterIP` |
| `agent.service.port` / `nodePort` | `8443` / `30443` | `nodePort` only applies when `agent.service.type=NodePort`; set it `null` to let Kubernetes choose instead |
| `agent.service.externalTrafficPolicy` | `""` | `Local` keeps the agent's source address (used for geoip) |
| `agent.service.annotations`, `loadBalancerIP`, `loadBalancerClass`, `loadBalancerSourceRanges` | empty | cloud load balancer tuning |
| `agent.tlsRoute.enabled` / `apiVersion` / `parentRefs` / `hostnames` | `false` / `gateway.networking.k8s.io/v1alpha2` | Gateway API TLSRoute (passthrough) |
| `admin.port` | `8080` | admin listener in the pod |
| `admin.behindTlsProxy` | `null` (auto) | pass `--admin-behind-tls-proxy`; auto is true when `httproute` is enabled |
| `admin.tls.secretName` / `certKey` / `keyKey` | `""` / `tls.crt` / `tls.key` | serve HTTPS from the pod |
| `admin.tls.selfSigned` / `selfSignedHosts` | `true` / `[]` | fallback: generate a self-signed cert when nothing else protects the port |
| `admin.service.type` / `port` | `ClusterIP` / `8080` | |
| `admin.existingPasswordSecret` / `...Key` | `""` / `password` | first administrator's password (`CONTINUUM_ADMIN_PASSWORD`) |
| `httproute.enabled` / `parentRefs` / `hostnames` / `annotations` | `false` ... | UI Gateway API HTTPRoute |
| `persistence.existingClaim` | `""` | use your own PVC |
| `persistence.storageClass` | `""` | `""` cluster default, `"-"` none |
| `persistence.size` / `accessModes` | `5Gi` / `[ReadWriteOnce]` | |
| `persistence.keepOnUninstall` | `true` | `helm.sh/resource-policy: keep` on the PVC |
| `pki.caKeyPassphraseSecret.name` / `key` | `""` / `passphrase` | encrypt the CA key at rest (needs a newer server) |
| `neo4j.mode` | `bundled` | `bundled`, `external` (mandatory: no `none`) |
| `neo4j.allowInsecureHttp` | `false` | pass `--neo4j-allow-insecure-http` (needs a newer server) |
| `neo4j.user` / `database` | `neo4j` / `neo4j` | |
| `neo4j.auth.existingSecret` / `passwordKey` | `""` / `password` | bundled: your Secret instead of a generated one |
| `neo4j.external.url` / `existingSecret` / `passwordKey` / `usernameKey` | `""` / `""` / `password` / `""` | external Neo4j |
| `neo4j.image.repository` / `tag` / `digest` | `neo4j` / `5.26.30-community` / `""` | bundled image |
| `neo4j.persistence.size` / `storageClass` | `10Gi` / `""` | bundled volume |
| `neo4j.memory.heapInitial` / `heapMax` / `pagecache` | `512m` / `512m` / `256m` | keep the pod limit ~500Mi above heap + pagecache |
| `neo4j.resources` | req `250m` / `1Gi`, limit `1536Mi` | |
| `neo4j.networkPolicy.enabled` | `false` | only the server pod may reach Neo4j (7474) |
| `agentInstall.imageRegistry` / `imageTag` / `chartRef` | `""` | `--image-registry`, `--image-tag`, `--chart-ref` (empty: the server's own defaults) |
| `geoip.path` | `""` | `--geoip-db`; mount the file with `extraVolumes` |
| `decider.allowCIDRs` | `[]` | `--decider-allow-cidrs`: private ranges an external decider may live in |
| `terminationGracePeriodSeconds` | `30` | |
| `priorityClassName` | `""` | |
| `resources` | req `50m` / `128Mi`, limit `512Mi` | |
| `tuning.goMemLimit` | `400MiB` | `GOMEMLIMIT` |
| `probes.startup` / `readiness` / `liveness` | 2 s x 30 / 10 s / 20 s | all `GET /healthz` on the admin port (HTTPS when `admin.tls` is set) |
| `podSecurityContext`, `securityContext` | non-root 65532, `fsGroup` 65532, read-only root, drop ALL, seccomp RuntimeDefault | |
| `serviceAccount.create` / `name` / `annotations` | `true` | |
| `automountServiceAccountToken` | `false` | the server never calls the Kubernetes API |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` | empty | |
| `extraArgs`, `extraEnv`, `extraVolumes`, `extraVolumeMounts`, `extraInitContainers`, `extraObjects` | empty | escape hatches |
| `podDisruptionBudget.enabled` / `minAvailable` / `maxUnavailable` | `false` / `1` / `null` | |
| `networkPolicy.enabled` | `false` | |
| `networkPolicy.ingress.agent.from` / `admin.from` | `[]` | peers per port; empty = anywhere |
| `networkPolicy.egress.enabled` / `dnsTo` / `neo4jCIDRs` / `deciderCIDRs` / `deciderPorts` / `extra` | `true` / kube-system / `[]` / `[]` / `[443]` / `[]` | |
| `backup.volumeSnapshot.enabled` | `false` | |
| `backup.volumeSnapshot.schedule` / `timeZone` / `className` / `retain` / `readyTimeout` / `includeNeo4j` | `17 3 * * *` / `""` / `""` / `7` / `300s` / `false` | |
| `backup.volumeSnapshot.image.repository` / `tag` | `alpine/k8s` / `1.34.11` | any image with `/bin/sh`, `kubectl`, `awk`, `xargs` |

## Verifying a change to the chart

```console
for f in ci/*.yaml; do helm lint . -f $f; helm template rel . -n scratch -f $f | kubectl apply --dry-run=server -f -; done
```

`ci/` holds one values file per combination: `minimal-nodeport`, `gateway-lb-bundled-neo4j`, `external-neo4j`, `tls-secret` (admin TLS, passphrase, Gateway API), `netpol-hardened` (policies, passthrough TLSRoute, PDB), `trial-selfsigned` (no admin-TLS flag at all - the chart's self-signed fallback). The Gateway API and VolumeSnapshot kinds need their CRDs on the cluster for a server-side dry run.

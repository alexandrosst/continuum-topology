# ikhnos-controller

Optional. Keeps an Ikhnos agent installed in this cluster from an `IkhnosAgent` resource, so `kubectl apply` can be the way an agent is asked for. It installs the same agent chart the Connect a cluster page's `helm install` does, from the same choices, and never talks to the Ikhnos server. The reasoning, and what it deliberately does not do, is in [Installing and removing what Ikhnos installs](../../../docs-site/docs/architecture/lifecycle.md).

## Install

```sh
helm install ikhnos-controller oci://ghcr.io/<owner>/ikhnos-controller --namespace ikhnos-system --create-namespace
```

The controller watches the `IkhnosAgent` resources of its own namespace, and installs the agent into that namespace. The CRD is in `crds/`, which Helm installs once and **never upgrades**: after an upgrade that changes it, apply it yourself (`kubectl apply -f crds/ikhnosagents.yaml`).

Built from source, the chart's image names are placeholders. Name your own:

```sh
helm install ikhnos-controller ./deploy/helm/ikhnos-controller -n ikhnos-system --create-namespace \
  --set image.repository=<registry>/controller \
  --set agent.image.repository=<registry>/ikhnos \
  --set agent.linkImage.repository=<registry>/link
```

(`scripts/publish.sh <registry>` with `ONLY=controller` publishes the controller image and this chart.)

## Ask for an agent

```sh
kubectl -n ikhnos-system create secret generic ikhnos-enrollment --from-literal=token=<the token from the Connect page>

kubectl -n ikhnos-system apply -f - <<'YAML'
apiVersion: ikhnos.io/v1alpha1
kind: IkhnosAgent
metadata:
  name: this-cluster
spec:
  server:
    address: ikhnos.example.com:8443      # host:port of the server's agent listener
    caPin: sha256/...                      # from the Connect page
  enrollment:
    tokenSecretRef: {name: ikhnos-enrollment}
  accessTier: 1                            # 0, 1 or 2; absent: the controller's maxAccessTier
  collectors: {nodeProbe: true}
  scope: {namespaces: [shop, payments]}
  purgeIdentityOnDelete: false
YAML

kubectl -n ikhnos-system get ikhnosagents
```

The agent then appears on the server's page for approval, as any agent does. Deleting the resource uninstalls the agent (`purgeIdentityOnDelete: true` also deletes its identity Secret). One agent per namespace: a second resource is reported `Blocked`.

## Values

| Value | Default | |
|---|---|---|
| `image.repository`, `image.tag`, `image.digest`, `image.pullPolicy` | `ikhnos/controller`, appVersion | The controller's image. A digest wins over the tag. |
| `agent.image.repository`, `agent.image.tag` | `ikhnos/ikhnos`, the agent chart's appVersion | The agent image the controller installs unless a resource names its own (`spec.image`). |
| `agent.linkImage.repository` | `ikhnos/link` | The cluster-link image. |
| `maxAccessTier` | `2` | The highest tier a resource may ask for, and the most the controller holds permissions for (below). |
| `timeout` | `5m` | How long one install, upgrade or uninstall (with its hooks) may take. |

## Permissions

Kubernetes lets nobody create a ClusterRole that grants what they do not hold. To install an agent at tier 2 the controller therefore holds, cluster-wide, everything tier 2 reads (pods, services, workloads, ingresses, mesh policy); at tier 1 what tier 1 reads (nodes and classes); at tier 0 the name of one namespace. `maxAccessTier` is how to hold less: the chart renders only the rules up to it, and the controller refuses a resource that asks for more. A test renders the agent chart for every choice a resource can make and holds this chart's rules to it in both directions (nothing the agent chart creates is out of reach, nothing is held that nothing asks for).

It also holds, in its own namespace, Secrets (Helm keeps its release records there, and the token and the agent's identity are Secrets) and the kinds the agent chart makes; and, in the `default` namespace, the one small Role and binding the agent chart puts there.

## Removing

Delete the `IkhnosAgent` first (the controller uninstalls the agent), then `helm uninstall ikhnos-controller`. A resource whose uninstall cannot finish is let go without touching the release by annotating it `ikhnos.io/skip-uninstall: "true"`. Helm leaves the CRD in place; remove it with `kubectl delete crd ikhnosagents.ikhnos.io` when no resource remains.

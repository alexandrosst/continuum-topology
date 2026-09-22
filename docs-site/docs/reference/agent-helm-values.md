---
id: agent-helm-values
title: Agent Helm values
description: A rendered reference for continuum-agent's values.yaml is planned; the source is fully documented today.
---

# Agent Helm values

:::info[This page is still growing]
A rendered, searchable table is planned here. Until then, [`backend/internal/chart/continuum-agent/values.yaml`](https://github.com/alexandrosst/continuum-topology/blob/main/backend/internal/chart/continuum-agent/values.yaml) documents every field inline. In practice, you'll rarely hand-edit this chart's values directly — the server's **Connect a cluster** wizard generates a complete `helm install` for you (see [Connecting a cluster](../installation/connecting-a-cluster.md)), and the values it doesn't set are the ones covered elsewhere on this site:
:::

- `access.tier` and namespace scope (`scope.namespaces` / `scope.exclude` / `scope.selector`) — see [Namespaces and services](../user-guide/namespaces-and-services.md).
- `nodeProbe.enabled` and `flowObserver.enabled` — the optional node-probe and traffic-observer collectors, both toggleable as checkboxes in the wizard itself.

`helm show values <chart-ref>` prints the whole file with its comments, from whatever chart reference the wizard gave you.

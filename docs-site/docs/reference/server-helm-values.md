---
id: server-helm-values
title: Server Helm values
description: A rendered reference for continuum-server's values.yaml is planned; the source is fully documented today.
---

# Server Helm values

:::info[This page is still growing]
A rendered, searchable table is planned here. Until then, [`deploy/helm/continuum-server/values.yaml`](https://github.com/alexandrosst/continuum-topology/blob/main/deploy/helm/continuum-server/values.yaml) documents every field inline, including the two that matter most and are covered in depth elsewhere on this site:
:::

- `agent.publicAddress` — see [Quickstart](../getting-started/quickstart.md) and the [`agent.publicAddress is required`](../troubleshooting/common-errors.md#agentpublicaddress-is-required-) error.
- `agent.service.type` / `agent.ingress` — see [Exposing the agent port](../architecture/exposure-options.md).

`helm show values oci://ghcr.io/YOUR-GITHUB-USERNAME/continuum-server --version 0.1.0` also prints the whole file, with its comments, straight from whatever version you're about to install.

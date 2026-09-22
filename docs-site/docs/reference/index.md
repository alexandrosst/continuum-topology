---
id: index
title: Reference
description: Where to find the authoritative, field-by-field documentation for every value and flag today.
---

# Reference

:::info[This section is still growing]
Rendered, table-form reference pages for the Helm values and CLI flags are planned here. Until then, the source files below are already the authoritative, field-by-field documentation — every value in both charts is documented inline, right next to where it's used.
:::

| Looking for... | Read |
|---|---|
| Every server chart value, what it does, and its default | [`deploy/helm/continuum-server/values.yaml`](https://github.com/alexandrosst/continuum-topology/blob/main/deploy/helm/continuum-server/values.yaml) |
| Every agent chart value | [`backend/internal/chart/continuum-agent/values.yaml`](https://github.com/alexandrosst/continuum-topology/blob/main/backend/internal/chart/continuum-agent/values.yaml) |
| Every `server` binary flag | `./server --help`, or [`backend/cmd/server`](https://github.com/alexandrosst/continuum-topology/tree/main/backend/cmd/server) |
| Agent self-diagnosis codes (what each means, and the fix) | [`backend/internal/agent/DIAGNOSTICS.md`](https://github.com/alexandrosst/continuum-topology/blob/main/backend/internal/agent/DIAGNOSTICS.md) |
| The placement cost function's exact formulas and factor table | [`backend/docs/advice.md`](https://github.com/alexandrosst/continuum-topology/blob/main/backend/docs/advice.md) |

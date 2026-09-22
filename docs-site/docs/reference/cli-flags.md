---
id: cli-flags
title: CLI flags
description: Where to find every server and agent binary flag today.
---

# CLI flags

:::info[This page is still growing]
A rendered reference is planned here. Until then, `--help` on either binary is authoritative and always matches the exact build you're running:
:::

```bash
./server --help
./agent --help
```

Most flags have an equivalent Helm value or environment variable (for example `--agent-address` corresponds to `agent.publicAddress` in the [server chart](./server-helm-values.md), and `CONTINUUM_REGISTRATION` to `--registration`) — running the chart is the common path, and running the binaries directly by hand is mostly useful for local development. See [Developer guide](../contributing/developer-guide.md) for that workflow.

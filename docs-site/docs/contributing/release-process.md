---
id: release-process
title: Release process
description: What CI checks on every push, what a version tag publishes, and the one manual step in between.
---

# Release process

Three GitHub Actions workflows, each with a distinct job:

## `ci.yml` — every push and pull request

Builds, vets and race-tests the Go backend; lints, type-checks, builds and unit-tests the frontend; runs a dependency vulnerability scan on both (`govulncheck` for Go, `npm audit` for the frontend). No registry credentials are used here and nothing is published — this is what gives a commit or PR its green check.

## `release.yml` — a push to `main`, or a version tag

Builds and publishes the `continuum` and `server` images (multi-arch for the agent image; amd64 for the server) and both Helm charts to this repository's own `ghcr.io/<owner>` namespace, signs everything keylessly with cosign, and bakes that GHCR namespace into the published chart's own defaults — which is what lets every install command on this site skip `--set image.repository=...` entirely.

- A push straight to `main` publishes the **`edge`** tag and a chart version like `0.0.0-edge.<sha>` — always whatever `main` currently is, never meant to be depended on for anything you'd call a release.

  :::warning[Testing an edge build against a node that has pulled `edge` before]
  `edge` is a moving tag: pinning `--version 0.0.0-edge.<sha>` pins the *chart* to an exact commit, but `image.tag` still resolves to the plain `edge` tag, and the default `imagePullPolicy: IfNotPresent` means a node that already has anything named `edge` locally won't re-pull, even though the registry has moved on — you get the chart's new templates running an old binary, with no error saying so (a flag it defines will fail with `flag provided but not defined`, or its behavior will just be stale). Add `--set image.pullPolicy=Always` whenever you install or upgrade an edge build to force a fresh pull every time.
  :::

- Pushing a tag matching `v*.*.*` (for example `v0.3.0`) publishes that exact version and moves `latest` to it. This is a real release, and it's what every install command in this documentation assumes:

  ```bash
  git tag v0.3.0
  git push origin v0.3.0
  ```

**The one manual step:** the very first time this workflow runs, GitHub creates its four packages (`continuum`, `server`, `continuum-agent`, `continuum-server`) as **private**, even in a public repository. Flip each to public once, from the repo's **Packages** sidebar → **Package settings** → **Change visibility**. Nothing after that needs repeating — every later push publishes into the same, already-public packages. See [Common errors](../troubleshooting/common-errors.md) if you hit this after the fact.

## `docs.yml` — a push to `main` that touches `docs-site/**`

Builds this documentation site and publishes it to GitHub Pages. The one prerequisite on the repository side: **Settings → Pages → Source** has to be set to **GitHub Actions** (a one-time setting, similar in spirit to the GHCR visibility flip above). Once that's set, editing anything under `docs-site/` and pushing to `main` republishes the live site within a couple of minutes — no separate deploy step, and nothing to build locally first.

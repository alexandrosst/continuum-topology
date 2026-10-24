---
id: intro
slug: /
title: Ikhnos documentation
description: Documentation for Ikhnos, a read-only, agent-observed model of your clusters across the cloud-to-far-edge continuum, with placement advice a person decides on.
---

# Ikhnos documentation

Ikhnos shows you your clusters as one picture: where things run, how healthy each place is, what talks to what, and how far apart your sites really are. A small read-only agent in each cluster keeps the picture current. A Placement page then recommends where a service would run better and shows the evidence and the confidence behind it. **Nothing moves until a person approves it.**

<Figure name="ContinuumOverview" caption="An estate is wider than any one cluster. Ikhnos is for the part where the places differ and the links between them are weak." />

## Start where you are

<Cards>
  <Card to="/docs/learn/what-is-ikhnos" kicker="Never heard of it" title="What is Ikhnos?">The problem it solves, what it does and does not do, and who it is for. Five minutes.</Card>
  <Card to="/docs/journey/" kicker="Start here to use it" title="Your first hour">Seven steps from nothing to a connected cluster and your first advice. Written for someone who has never seen it.</Card>
  <Card to="/docs/getting-started/quickstart" kicker="I know what I am doing" title="Quickstart">One recipe that works on any cluster. Copy, paste, done.</Card>
  <Card to="/docs/user-guide/using-the-ui" kicker="It is running" title="Using the interface">Every page of the UI, what it shows and what you can do there.</Card>
  <Card to="/docs/inside/" kicker="I want to understand or contribute" title="Inside Ikhnos">How it is built: layers, the agent protocol, the model, advice and the decision loop.</Card>
  <Card to="/docs/reference/" kicker="I need a value" title="Reference">Helm values, command-line flags and the permissions table.</Card>
</Cards>

## The idea in one paragraph

The **server** is one program with one database. It has two doors that have little in common: port 8443 for agents over mutual TLS, and port 8080 for you. The **agent** is one pod per cluster with read-only permissions, and it starts every connection itself, so no cluster opens an inbound port. What an agent may see is a chain of three limits (install, approval, effective) that the server can only narrow. Everything else, such as telemetry, your own decider and the controller, is optional and is added when you need it.

For the longer version with figures, read [How it works in ten minutes](learn/how-it-works.md).

## If something is wrong

Go to [Common errors](troubleshooting/common-errors.md). It is written in the same words the interface uses, so you can search for the message you see. For what the project does not do yet, read [Status and limits](project/status-and-limits.md); it is meant to be read before you rely on Ikhnos.

:::tip Looking for the code, not the product?
[Inside Ikhnos](inside/index.md) starts with a tour of the repository, and the [developer guide](contributing/developer-guide.md) covers building from source and running the tests.
:::

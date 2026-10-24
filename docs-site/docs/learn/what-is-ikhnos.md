---
id: what-is-ikhnos
title: What is Ikhnos
description: The problem Ikhnos solves, what it does and deliberately does not do, and who it is for. No prior knowledge assumed.
---

# What is Ikhnos

Ikhnos shows you everything you run on Kubernetes, in every place you run it, and tells you where each thing would run better. It watches. It never changes anything.

If that sentence already tells you what you need, go straight to [your first hour](../journey/index.md). If you want to know why this is hard, and what Ikhnos does about it, read on. Nothing on this page assumes you have used it, or anything like it.

## The problem

Most teams start with one Kubernetes cluster, and for one cluster the tools you already have are enough. Then the estate grows outwards. A second cluster for another region. Racks of smaller clusters at regional sites, close to the people and data they serve. Then boards, plants, shops and vehicles, each running a very small cluster beside sensors and cameras. Together this is called the **compute continuum**: one continuous range from a big cloud region to a tiny box in a cabinet.

<Figure name="ContinuumOverview" caption="The further out you go, the less capacity you have, the weaker the links are, and the less you can be sure of. The four questions at the bottom are the ones a person responsible for the whole estate has to answer, and each one gets harder with distance." />

Every cluster can tell you about itself. Nothing tells you about all of them together. So four simple questions turn into a lot of manual work: where is everything, is it healthy, what talks to what, and where should this run? The last one is the hardest. Moving a service closer to its data might save a hundred milliseconds on every request, or it might overload a small site and take it down. Answering it well takes facts from many places, and an honest account of which of those facts you actually know.

## What Ikhnos does

Ikhnos does four things, in this order.

**It observes.** You install a small program, the **agent**, in each cluster. It reads what the cluster contains (nodes, namespaces, workloads, services) and, if you allow it, how the pieces talk to each other and how far apart the clusters are. It sends this to one **server**.

**It models.** The server keeps one model of the whole estate. You see it two ways that always agree: an **application view** (your services, grouped by cluster, with the dependencies between them) and an **infrastructure view** (the nodes underneath). Edit something in one place and it changes in the other, because there is only one model.

**It remembers.** Every change to anything becomes a new version with a reason attached. You can ask what a cluster looked like an hour ago, or what would be affected if a service went away.

**It advises.** For a service that could run somewhere else, Ikhnos works out whether it would fit there, what it would cost in round-trip time, and how sure it is. It shows the evidence. A person decides.

## What it deliberately does not do

It does not deploy anything. It does not move anything. It has no credentials to change a cluster, and the server never holds a kubeconfig. Placement advice is advice: acting on it is yours to do, with your own tools. This is a design choice, not a missing feature, and it is why you can point Ikhnos at clusters that matter.

It does not ask your clusters to open any inbound port. Every connection starts at the agent and goes out to the server.

It does not guess quietly. When a number is a guess, the interface says so. When there is not enough evidence to recommend a move, it says that, and names what is missing.

## Who it is for

Ikhnos is for people who run clusters in more than one place and want one picture of all of it, with a second opinion on where things should run. A platform team with a cloud region and a few edge sites is the typical case. So is a researcher studying orchestration across the continuum: the placement engine is pluggable, so you can point it at your own decision logic and compare it against the built-in baseline on the same estate, side by side.

It is pre-1.0 software. [Status and limits](../project/status-and-limits.md) says plainly what has been tested, what has never been run for real, and what is not built.

## Where to go next

<Cards>
  <Card to="/docs/learn/how-it-works" kicker="Ten minutes" title="How it works">The parts, how they trust each other, and how an answer is made, with a figure for each.</Card>
  <Card to="/docs/learn/concepts" kicker="Reference" title="Concepts in plain words">What every term in the interface means, in a sentence each.</Card>
  <Card to="/docs/journey/" kicker="Hands on" title="Your first hour">Install it and see your first cluster, step by step.</Card>
</Cards>

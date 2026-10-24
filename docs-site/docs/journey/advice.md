---
id: advice
title: "7. Ask for advice"
description: Open Placement, read a recommendation and the evidence under it, try a move on paper, and learn why an honest "not enough evidence" is a normal first answer.
---

<StepBar n={7} of={7} title="Ask for advice" />

# Ask for advice

**Placement** is where Ikhnos tells you whether a service would run better somewhere else. It is advice only. Nothing is deployed or moved, and there is no button that does it.

## What you will see first

On a new estate, the first thing Placement says is often that it cannot recommend anything yet. That is not a failure. It is the point of the design.

<Shot src="readme/ui-placement" alt="A placement recommendation that says there is not enough evidence to recommend, listing which inputs are guesses and what could not be checked" caption="Here Ikhnos declines to recommend a move, because five of ten inputs are guesses and two are unknown, and it says which. The sample estate is made up." />

A recommendation is only as good as the facts under it. Ikhnos keeps, for every input, where it came from and how sure it is, and it takes the confidence of an answer from its **weakest deciding fact**, never an average. So an answer that rests on one guess is called a guess, and when too many inputs are missing, it says "not enough evidence to recommend" and names what is missing.

## Reading a recommendation

When there is enough, a recommendation tells you four things.

**Where, and what it would save.** The destination cluster, and the saving in **points**. One point is one millisecond of round trip on a connection in constant use. Traffic that would newly cross between sites, a penalty as a cluster fills up, and the one-off cost of copying data are all counted, so a move that looks good on latency but means copying a terabyte is weighed honestly.

**How sure it is.** A confidence of measured, reported, inferred, guess or unknown.

**Why.** Each input with its source and its age, so you can judge for yourself, and the smallest change that would flip the answer.

**What was left alone.** A **Left alone** section lists services that were considered and not moved, with the reason. A good advisor is as clear about what it did not suggest as about what it did.

Underneath, the checks are strict. A cluster is only a candidate if the service can actually run there: local volumes, node pinning, data residency, trust zones, the access policy, and room for the CPU and memory it needs. Each check ends in one of three answers, **fits**, **does not fit** or **can't tell**. Free capacity is an interval, because an agent's number has an age, and an unknown is never treated as zero.

## Try a move on paper

The **What-if** tab lets you build a scenario: pick a service and a destination, and see what it would do, move by move, without changing anything. Use it to test an idea against the same checks and the same cost function that the recommendations use.

## Compare deciders

The **Deciders** tab scores several ways of choosing moves on the same estate, with the same checks and one scoring function. Two are built in, a **Baseline** and **Follow the heaviest talker**. If you run your own placement logic, you can plug it in as a webhook and see it scored beside them. This is what makes Ikhnos useful as a testbed for placement research, not only as a tool.

## Getting better advice

Advice improves with evidence. Three things help most, and each is yours to turn on.

- **Observe traffic.** Switching on the traffic observer on an agent (an extra from step 4, which you can add later with a `helm upgrade`) turns "a dependency someone declared" into "a dependency measured, with traffic".
- **Measure the paths.** Path measurements and cluster links give real round-trip times between clusters, instead of an estimate from distance.
- **Declare what only you know.** Which site a cluster is in, which services belong to one application, a data-residency rule. Ikhnos keeps declared values apart from observed ones, and a declared value is labelled as such.

[Mobility and placement](../user-guide/mobility-and-placement.md) covers the cost function and the policy weights in full, and [How a recommendation is made](../inside/placement-engine.md) draws the whole pipeline.

[What next →](./next-steps.md)

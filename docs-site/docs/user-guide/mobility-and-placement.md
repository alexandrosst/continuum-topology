---
id: mobility-and-placement
title: Mobility and placement
description: How the cost function, evidence classes and pluggable deciders behind the Placement page actually work.
---

# Mobility and placement

The **Placement** page exists to answer one question well: "where would each service be better off, and what would that change?" It never moves anything — it's advice, and it's built to never quietly hide how confident that advice actually is.

## What can move at all

Before cost ever enters the picture, a service is checked against hard constraints that are never traded away: local volumes, node pinning, data residency, trust zones, and tier policy, plus whether a candidate cluster actually has room for its CPU and memory requests. A service that fails any of these simply isn't a candidate — no amount of cost savings elsewhere overrides it.

Fit itself is deliberately three-valued: **fits**, **does not fit**, or **can't tell**. A cluster whose free capacity isn't known is a *can't tell* target with the missing fact named, never a hidden zero and never silently treated as unlimited room.

## The cost function

For a candidate placement, the engine weighs, per service: the round-trip time to everything it talks to (weighted by how busy each connection is), the traffic that would newly cross between sites, a penalty for filling a cluster close to capacity, and — counted once, subtracted from the projected gain — the cost of copying any persistent data along. The unit is **points**, where 1 point is defined as 1 ms of round-trip time on a connection in constant use. The default weights (round-trip 1/ms, cross-site traffic 8/(MB/s), data copying 0.5/GB, a headroom penalty of 15, and a minimum bar of 15% *and* 2 points of gain before a move is even suggested) live in a Policy panel you can adjust yourself, and every recommendation reflects whatever policy is currently set.

Round-trip estimates are ranked by how good the evidence behind them is, best first: the same cluster (a nominal 0.3 ms), a path that's actually been measured, a measured or declared link between sites, the same site (1 ms), an estimate purely from the distance between two points on the map, or — if none of that is available — simply unknown, with 80 ms assumed as a fallback rather than pretending to know.

## Confidence is never averaged away

Every fact behind a recommendation — free CPU, a round-trip figure, how busy a link actually is — carries one of four confidence classes: **measured**, **reported**, **inferred**, or **guessed**, each with a documented margin of error, and each one degrades a class once it's old enough to cross a staleness window. A fit is only certified at the pessimistic end of that margin, not the optimistic one. Critically, a recommendation's overall confidence is set by its **weakest deciding fact** — never averaged with the strong ones — so a recommendation rides entirely on one shaky number, it's presented that way ("Not enough evidence to recommend — 2 of 3 inputs are guesses") rather than shown with false confidence. An expandable **Why** panel lists every fact that went into a recommendation, its source and age, what would have to change for the verdict to flip, and — inside a What-if scenario — how far a reported capacity figure could be wrong before the answer changes.

## Recommendations are sequential, not simultaneous

The engine picks the single best move, treats it as if it had already happened, and re-evaluates from there — up to 40 moves. This means two services are never recommended into each other's place at once, and a cluster that just took on one service is correctly seen as having less room for the next recommendation.

## What-if

Beyond individual recommendations, you can build a scenario — a set of proposed moves, or evacuating an entire cluster — and see the projected network cost, cross-site traffic and cluster load before and after, with warnings surfaced for anything pinned, any capacity that doesn't actually exist, or any constraint the scenario doesn't fully account for.

## Pluggable deciders

This is likely the most relevant part if you're using this project as a research testbed rather than purely operationally: a **decider** proposes moves, and whatever produced those proposals — the built-in weighted-cost baseline, the deliberately naive "follow the heaviest talker" foil, or your own logic — every proposal is checked against the *same* hard constraints and scored by the *same* cost function before it's compared. That means deciders are comparable on equal footing on the **Deciders** tab, on the same real (or sample) estate.

An external decider is any HTTP service you point the server at (name, URL, a timeout of 1–25 seconds, configured by an administrator). The browser sends its input to the server, which forwards it — and only it, never a URL supplied elsewhere in the request — with no redirects followed, no link-local addresses reachable, and size limits enforced in both directions.

The request your decider receives (schema 1) gives you clusters (with tier, site, free CPU/memory), links (with round-trip time and how it was derived), services (with their resource requests, mobility, and which clusters are actually legal candidates — hard constraints are already applied before your decider ever sees a service), and observed or declared flows between them. Your decider replies with a list of `{serviceId, to, reason}` proposals; anything that would violate a hard constraint the server already checked is refused individually, with the reason shown under *Proposals that were refused*, rather than failing the whole batch. The **Preview** on the Deciders tab shows you exactly the request your own estate would generate, so you can build against real shapes without guessing at the schema.

If you want the full factor table and the exact formulas behind the cost function, the canonical source is [`backend/docs/advice.md`](https://github.com/alexandrosst/continuum-topology/blob/main/backend/docs/advice.md) in the repository — the same logic runs identically in the server and in the browser, so the two are never able to disagree with each other.

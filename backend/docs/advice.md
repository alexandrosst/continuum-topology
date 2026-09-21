# How advice handles uncertainty

Placement advice answers three questions about a service at a cluster: does it fit, how sure is that, and what
would change the answer. This document is the rules; the code is [`internal/advice`](../internal/advice) (used by
the decider and by `/decide` enrichment) and its mirror [`src/lib/advice.ts`](../../src/lib/advice.ts) (used by the
browser, so the two never disagree — a shared table of vectors, generated once and checked by both test suites,
proves it). [model-api.md](model-api.md#the-placement-decider-request) is the wire contract this produces.

## The philosophy

An unknown is not a zero. A guess is not a fact. Advice refuses to certify what it does not know, and every
recommendation names the facts it used — with source, confidence and age — and says what would change it. Nothing
here moves a workload; it is read-only advice.

## The rules

1. **Three-valued fit.** Every check answers `fits`, `doesNotFit`, or `cantTell` — never a plain yes/no with unknown
   folded into either side. Capacity is worked out as an interval (`lo`..`hi`) per dimension (CPU cores, memory
   bytes), from the free capacity of the nodes that could host the workload. **Fits** only when the workload fits at
   the pessimistic end (`lo >= need`, using the worst class among what decided it). **Does not fit** only when it
   fails even at the optimistic end (`hi < need`). Otherwise: **can't tell**, with the reason.
2. **Unknown is not a value.** A node that never reported its allocatable resources contributes nothing certain to
   `lo` and pushes `hi` to infinity for that pool; a workload with no CPU or memory request has an unknown need, and
   the dimension is `cantTell` outright — "the workload sets no CPU request, so what it needs is not known".
3. **Confidence classes and their factors.** Every fact carries one of five classes. A class believed at face value
   widens the pessimistic/optimistic interval by a factor; `unknown` has no factor (there is no interval — the bound
   is open).

   | Class | Meaning | Factor (±) |
   |---|---|---|
   | `measured` | read from the thing itself by an instrument | 5 % |
   | `reported` | an agent read it from the Kubernetes API, or a person stated it | 15 % |
   | `inferred` | worked out by a rule from other facts | 30 % |
   | `guess` | inferred from weak signals | 50 % |
   | `unknown` | not known at all | — (open bound) |

   Free capacity on Kubernetes today is always `reported` (from the API) or `unknown` (nothing is `measured`: no
   probe reports live usage). A round trip or a dependency's traffic can be `measured`, `reported` (declared),
   `inferred` (same site, or worked out from distance) or a `guess` (an assumed default for a declared-only link).
4. **Staleness demotes one class.** A fact confirmed longer ago than the staleness window (four heartbeats, default
   120 s, taken from the same Settings the model uses for observation state) counts as one class worse than the
   source claimed — `reported` becomes `inferred`, and so on down to `guess`; `unknown` stays `unknown`. This is the
   same rule, and the same window, that turns a live record stale.
5. **Combining.** Several checks (dimensions, and policy checks like data residency or trust zone) combine by
   severity: `doesNotFit` beats `cantTell` beats `fits`. The class attached to the combined verdict is the weakest
   class among the parts that produced *that* verdict (all of them for a `fits`, only the failing ones for a
   `doesNotFit`, only the undecided ones for a `cantTell`) — never an average, so one guessed input among several
   solid ones is never hidden.
6. **Confidence level.** `high` / `medium` / `low` / `none` is read off the weakest deciding class: `measured` or
   `reported` → `high`, `inferred` → `medium`, `guess` → `low`, `unknown` → `none`. A `cantTell` verdict is capped at
   `low`: an answer that could not be told is never presented as well evidenced.
7. **What would change it.** For every dimension a `Change` gives the smallest free-capacity figure that would flip
   the verdict — "if free CPU at cluster X is below 1.18 cores it can no longer be said to fit" for a fit, both the
   flip-to-fits and flip-to-does-not-fit thresholds for a can't-tell, and the flip-to-can't-tell threshold for a
   does-not-fit. Each threshold is verified by construction: applying it (`applyChange`/`ApplyChange`) recomputes the
   verdict from scratch and is checked, in the test suite, to actually flip. The same interval gives a sensitivity
   readout — "still fits if free CPU is off by up to 22 %" — from the margin between what is needed and the known
   free amount.

## What is not modelled (honestly)

* **Nothing is `measured` for capacity today.** Kubernetes reports allocatable and requested resources through its
  API (`reported`); no probe samples live usage, so a node's free capacity is never better than `reported`, and is
  `unknown` whenever pods are not read (access tier below 2) or the node itself is not read (below 1).
* **The server's pool is every node of the cluster.** It does not see a workload's node selectors, labels or
  taints — that is the browser's job (`src/lib/movability.ts`), which narrows the pool first. Where the two
  disagree, the worse (more cautious) verdict is the one to believe.
* **No fragmentation, no storage capacity.** A pool sums free CPU and memory across nodes; a workload that needs one
  node with enough of both is not distinguished from one that could be split. Ephemeral or persistent storage
  capacity is not judged at all.
* **A cluster with unknown capacity is not excluded.** An unknown is not a "no": it is a live target listed
  separately, as "can't tell", with the specific missing fact and the fix (raise the agent's access tier, or connect
  one) — never lumped in with clusters that are stale, disconnected or revoked.

## Where things are

| | |
|---|---|
| `internal/advice` | the rules: `Fit`, `Assess`, `Pool`, `Fact`, `Change`, `Document` (wire shape) |
| `internal/server/decide_advice.go` | turns the effective model into `advice.Fact`/`Pool`, judges every service × cluster pair, and adds `clusters[].free` and `services[].capacity` to a decision request |
| `src/lib/advice.ts` | the same rules in TypeScript, for the browser's own advice when there is no server (sample/offline mode) or as the client-side check before a request is sent |
| `src/lib/capacity.ts`, `src/lib/movability.ts` | turn the browser's model into facts and pools, the way `decide_advice.go` does for the server |
| `backend/internal/advice/testdata/vectors.json` | cases generated once (an independent Python implementation) and checked by both the Go and the TypeScript test suites, so the two cannot silently drift apart |

import type { Advice, Change as VerdictChange, Class, Fix, FactRow, Level, Verdict } from '../advice'
import type { MoveVerdict } from '../movability'

/**
 * How much each kind of cost matters. The unit is a cost point: 1 point is one millisecond of round trip on a
 * link that is in constant use. The defaults say "a MB/s that crosses between sites is worth 8 ms of latency,
 * copying a GB of data is worth half a millisecond, and running a cluster more than 80 % full is worth up to 15".
 * They are a starting point people are expected to change, and the Placement page says so.
 */
export interface Policy {
  /** Per millisecond of round trip, per unit of activity on the edge. */
  latency: number
  /** Per MB/s of traffic that crosses between two sites. */
  traffic: number
  /** Per percentage point of measured packet loss, per unit of activity on the edge - undefined (never
   *  measured) counts as 0, same as a missing traffic figure already does. Loss is the sharper signal a
   *  raw round trip misses entirely: a link can hold a low RTT while quietly dropping and retransmitting,
   *  which costs real time this file would otherwise attribute to nothing. Only ever set from an
   *  eBPF-observed edge (Dependency.stats.lossPct), never a guess the way a missing traffic figure falls
   *  back to DECLARED_ACTIVITY. */
  loss: number
  /** Per millisecond of round-trip jitter (mean deviation), per unit of activity on the edge - same
   *  "undefined counts as 0" treatment as loss above. A link with the same average RTT as another can
   *  still be meaningfully worse to depend on when its jitter is high (every round trip is a gamble
   *  rather than a known cost), which weightedRttMs alone - an activity-weighted average - cannot tell
   *  apart from a perfectly steady link. */
  jitter: number
  /** Per GB of persistent data that would have to be copied. */
  migration: number
  /** Up to this much when the target would be full (scaled from 80 % to 100 %). */
  headroom: number
  /** A move must improve the service's cost by at least this share of its current cost... */
  minBenefit: number
  /** ...and by at least this many points. */
  minAbsolute: number
  /** The round trip assumed when nothing is known about a path. Deliberately pessimistic. */
  fallbackMs: number
}

// loss/jitter default to a fraction of latency's own per-unit weight: 1 % of measured loss on a busy edge
// costs as much as 5 ms of extra round trip (loss on a saturated link means real retransmission delay, not
// just a count), and 1 ms of jitter costs a tenth as much as 1 ms of steady latency (volatility matters,
// but a lot less than the latency itself already measured by rtt.ms). Both are 0 on any edge that was
// never eBPF-observed, exactly like traffic already is for a purely declared dependency, so a world with
// no measured loss/jitter anywhere computes byte-identical costs to before this policy existed.
export const DEFAULT_POLICY: Policy = { latency: 1, traffic: 8, loss: 5, jitter: 0.1, migration: 0.5, headroom: 15, minBenefit: 0.15, minAbsolute: 2, fallbackMs: 80 }

/** Where the other end of a dependency is, as far as latency is concerned. */
export type Loc = { kind: 'cluster'; id: string } | { kind: 'site'; id: string } | { kind: 'external'; host: string; port?: number } | { kind: 'unknown' }

/** How a round-trip figure was obtained, best first. */
export type RttBasis = 'same-cluster' | 'measured' | 'declared' | 'same-site' | 'estimated' | 'unknown'

export interface Rtt {
  ms: number
  basis: RttBasis
}

/** One dependency as seen from the service being placed. */
export interface EdgeEvidence {
  dependencyId: string
  peerName: string
  peerKind: 'service' | 'device' | 'external'
  /** Where the peer is: a cluster or site name, an address, or "unknown". */
  peerWhere: string
  /** Bytes per second when the traffic was measured. */
  bytesPerSec?: number
  /** Retransmitted segments as a percentage of segments sent, when eBPF measured it - see
   *  Dependency.stats.lossPct's own "never a fabricated 0%" rule. Undefined, never 0, when not measured. */
  lossPct?: number
  /** Round-trip jitter (mean deviation), in ms, when eBPF measured it - see Dependency.jitterMs's own
   *  gauge doc. Undefined, never 0, when not measured. */
  jitterMs?: number
  /** How much this edge counts (0.05 idle … 1 busy). Edges with no traffic figure count as 0.3. */
  activity: number
  trafficKnown: boolean
  rtt: Rtt
  crossSite: boolean
  cost: number
}

/** One input that decides an answer: what it is, and how sure it is. */
export interface EvidenceInput {
  label: string
  class: Class
}

/** What would change a recommendation: a verdict flip on a fact, or the point at which a move stops being worth making. */
export interface WouldChange {
  text: string
  /** verdict: the fit changes. recommendation: the move is no longer worth making. */
  effect: 'verdict' | 'recommendation'
  attribute: string
  unit?: string
  direction: 'atLeast' | 'below'
  threshold: number
  current: number | null
}

export interface Evaluation {
  serviceId: string
  clusterId: string
  /** Steady-state cost of running here: latency + cross-site traffic + headroom. Lower is better. */
  cost: number
  latencyCost: number
  trafficCost: number
  /** Cost from measured packet loss and round-trip jitter (Policy.loss/Policy.jitter) - kept apart from
   *  trafficCost, which is specifically about traffic volume, not link quality. 0 on a world where
   *  nothing was ever eBPF-observed for loss/jitter, the same as on any plan computed before this field
   *  existed. */
  qualityCost: number
  headroomCost: number
  /** One-off: copying persistent data. Not part of `cost`. */
  migrationCost: number
  /** Activity-weighted average round trip to what it talks to. */
  weightedRttMs: number
  /** Traffic that crosses between sites, bytes per second. */
  crossSiteBps: number
  edges: EdgeEvidence[]
  /** Peers whose location or path is unknown, so their cost is a guess. */
  unknownPeers: number
  /**
   * How far to trust this evaluation: the weakest class among the facts that decide it (the connections that carry
   * most of the cost, and what it needs against what is free), never an average. `none` means something that decides it
   * is not known at all.
   */
  confidence: Level
  confidenceClass: Class
  /** The class of what the fit verdict alone rests on (the room and the constraints), apart from the connections. */
  fitClass: Class
  /**
   * Three-valued: it fits (every constraint holds, checked, even at the pessimistic end of every uncertain figure),
   * does not fit (something definitely rules it out), or cannot be told (something is unknown or too uncertain).
   */
  verdict: Verdict
  /** Shorthand for `verdict === 'fits'`. A cluster that cannot be checked is not a fit. */
  fits: boolean
  /** What definitely rules it out. */
  blockers: string[]
  /** What could not be checked, or is too uncertain to certify. */
  unchecked: string[]
  /** What a person can do to turn a "cannot tell" into an answer. */
  fixes: Fix[]
  /** The facts this rests on: what was used, its source, class and age. */
  facts: FactRow[]
  /** The smallest change of one fact that would change the verdict. */
  wouldChange: VerdictChange[]
  /** The weakest deciding inputs, for "2 of 3 inputs are guesses". */
  inputs: EvidenceInput[]
  /** The full capacity and constraint answer when this is a place the service is not in already. */
  advice?: Advice
  /** Share of the target's CPU that would be requested after the move, when known. */
  utilAfter?: number
}

export interface Recommendation {
  serviceId: string
  serviceName: string
  from: string
  to: string
  current: Evaluation
  target: Evaluation
  /** Steady-state improvement in cost points. */
  benefit: number
  /** `benefit` after the one-off cost of copying data. */
  net: number
  verdict: MoveVerdict
  /** Does it fit there: fits, cannot be told, or does not fit (a recommendation is never made for the last). */
  fit: Verdict
  /**
   * How far to trust the recommendation: the weakest fact that decides the ranking, never an average. `low` and `none`
   * mean it must be shown as a hint (or as not enough evidence), not as the best option.
   */
  confidence: Level
  confidenceClass: Class
  /** The deciding inputs (the connections that carry the gain, and the room at the target) and how sure each is. */
  inputs: EvidenceInput[]
  /** The facts used, each with source, class and age. */
  facts: FactRow[]
  /** What would change this recommendation. */
  wouldChange: WouldChange[]
  /** What would turn a "cannot tell" into an answer. */
  fixes: Fix[]
  /** Why, in words a person can check against the evidence. */
  reasons: string[]
  /** What to look at first. */
  caveats: string[]
  /** Other clusters that fit, best first. */
  alternatives: Evaluation[]
}

export interface Skipped {
  serviceId: string
  serviceName: string
  why: string
}

export interface Move {
  serviceId: string
  to: string
}

/** What one placement round decided. */
export interface Plan {
  recommendations: Recommendation[]
  /** Services that were looked at and should stay where they are, and services that cannot be moved at all. */
  stay: number
  skipped: Skipped[]
}

import type { Hop, State } from './operatorsView'

/** How often data is expected from each hop. Discovery agents beat every 30 s; collectors, operators and FUSION batch for seconds, so a
 *  minute is the rhythm to judge them by (FUSION's own "no new data" problem uses the same five-fold margin). */
export const EXPECTED_MS: Record<Hop['key'], number> = { agent: 30_000, local: 60_000, regional: 60_000, central: 60_000, fusion: 60_000 }
/** Late after this many expected intervals (75 s for an agent, as its own heartbeat check), stale after twice that. */
const LATE_AFTER = 2.5
const STALE_AFTER = 5

export type FreshnessLevel = 'fresh' | 'late' | 'stale' | 'none'

/** How recent a hop's newest data is against what is expected of it: `fraction` is 1 for data that just arrived and runs out at the
 *  stale mark, `none` when nothing ever arrived. */
export interface Freshness {
  level: FreshnessLevel
  fraction: number
  ageMs?: number
}

export function freshness(lastData: string | undefined, now: number, expectedMs: number): Freshness {
  const at = lastData ? Date.parse(lastData) : NaN
  if (Number.isNaN(at)) return { level: 'none', fraction: 0 }
  const ageMs = Math.max(0, now - at)
  const level = ageMs <= LATE_AFTER * expectedMs ? 'fresh' : ageMs <= STALE_AFTER * expectedMs ? 'late' : 'stale'
  return { level, fraction: Math.max(0, Math.min(1, 1 - ageMs / (STALE_AFTER * expectedMs))), ageMs }
}

/** What a connector shows: only data that is arriving moves. Stalled is amber and still, broken is red and still, unknown is grey and dashed. */
export type LinkState = 'flowing' | 'stalled' | 'broken' | 'unknown'

/** The sender's state decides, and a "healthy" sender whose data has gone late is stalled, not flowing. */
export function linkState(state: State | undefined, total: number, f: Freshness): LinkState {
  if (total === 0 || !state || state === 'unknown') return 'unknown'
  if (state === 'down') return 'broken'
  if (state === 'attention') return 'stalled'
  return f.level === 'fresh' ? 'flowing' : f.level === 'none' ? 'unknown' : 'stalled'
}

/** The chip under a node: "Healthy" while all are, else how many are in the worst state ("1 needs attention"). */
export function hopChip(h: Hop): { state: State; count?: number } | undefined {
  if (h.total === 0 || !h.state) return undefined
  return h.state === 'healthy' ? { state: 'healthy' } : { state: h.state, count: h.worst }
}

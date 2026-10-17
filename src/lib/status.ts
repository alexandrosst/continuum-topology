import type { Agent } from './types'

/**
 * The rules that decide when something is late or stale, in one place. Each screen still says it in its own words; none of them
 * decides it any more.
 */

/** An agent beats every 30 seconds; two and a half missed is "late". */
export const AGENT_LATE_AFTER_MS = 75_000
/** An agent refreshes what it says about itself every 5 minutes; three missed means it is history, not news. */
export const REPORT_STALE_AFTER_SECONDS = 15 * 60
/** A probe of a link pair that is older than this no longer shows that probing works. */
export const PROBE_STALE_AFTER_MS = 15 * 60_000

/** `live`: connected and heard from lately. `late`: connected, heartbeat overdue. `down`: the stream is closed. `unknown`: no live link to judge (a sample or hand-made agent). */
export type Liveness = 'live' | 'late' | 'down' | 'unknown'

export interface AgentLiveness {
  state: Liveness
  /** Milliseconds since the last heartbeat; undefined when none has been heard. */
  silentMs?: number
}

/** Whether a heartbeat or a sample of this age is overdue. */
export const isLate = (silentMs: number): boolean => silentMs > AGENT_LATE_AFTER_MS

export function agentLiveness(a: Pick<Agent, 'connected' | 'lastHeartbeat'>, now: number): AgentLiveness {
  const silentMs = a.lastHeartbeat ? now - Date.parse(a.lastHeartbeat) : undefined
  if (a.connected === undefined) return { state: 'unknown', silentMs }
  if (a.connected === false) return { state: 'down', silentMs }
  if (silentMs !== undefined && isLate(silentMs)) return { state: 'late', silentMs }
  return { state: 'live', silentMs }
}

/** Whether what an agent last said about itself, this many seconds ago, is history now. A date that cannot be read is not stale: it is unknown. */
export const reportIsStale = (ageSeconds: number): boolean => Number.isFinite(ageSeconds) && ageSeconds > REPORT_STALE_AFTER_SECONDS

/**
 * How something is doing, in the one set of words every screen uses. `down` is "Not working", `attention` "Needs attention"; `unknown` is
 * a real answer ("this cannot be told"), not a missing one. `setup` is for a part that has been asked for and is not up yet: neither good
 * nor bad news, so only the screens that have such parts use it.
 */
export type Health = 'healthy' | 'attention' | 'down' | 'unknown'
export type HealthOrSetup = Health | 'setup'

export const HEALTH_WORD: Record<HealthOrSetup, string> = { healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', setup: 'Setting up', unknown: 'Unknown' }

/** Worst first. Where several things are summed up in one word, the earlier one wins. */
const WORST_FIRST: readonly HealthOrSetup[] = ['down', 'attention', 'setup', 'unknown', 'healthy']

/** 0 is the worst. For sorting, and for "at least as bad as". */
export const severity = (h: HealthOrSetup): number => WORST_FIRST.indexOf(h)

/** The worst of several, or `healthy` for none. */
export function worstOf<T extends HealthOrSetup>(states: readonly T[]): T | 'healthy' {
  return states.reduce<T | 'healthy'>((w, s) => (severity(s) < severity(w) ? s : w), 'healthy')
}

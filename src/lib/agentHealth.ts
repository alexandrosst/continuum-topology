import { agentLiveness, type Liveness } from './status'
import type { Agent } from './types'

/** How an agent is doing right now, from what the server can tell. */
export type Health = 'ok' | 'late' | 'offline' | 'waiting' | 'expired' | 'gone' | 'unknown'

const OF_LIVENESS: Record<Liveness, Health> = { live: 'ok', late: 'late', down: 'offline', unknown: 'unknown' }

export function healthOf(a: Agent, now: number): Health {
  if (a.status === 'pending') return 'waiting'
  if (a.status === 'expired') return 'expired'
  if (a.status !== 'approved') return 'gone'
  // Sample and hand-made agents have no live link to judge, so they are not called disconnected.
  return OF_LIVENESS[agentLiveness(a, now).state]
}

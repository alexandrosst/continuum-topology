import { agentLiveness, type Liveness } from '../../../lib/status'
import type { Agent } from '../../../lib/types'

/** How an agent is doing right now, from what the server can tell. */
export type AgentHealth = 'ok' | 'late' | 'offline' | 'waiting' | 'expired' | 'gone' | 'unknown'

const OF_LIVENESS: Record<Liveness, AgentHealth> = { live: 'ok', late: 'late', down: 'offline', unknown: 'unknown' }

export function healthOf(a: Agent, now: number): AgentHealth {
  if (a.status === 'pending') return 'waiting'
  if (a.status === 'expired') return 'expired'
  if (a.status !== 'approved') return 'gone'
  // Sample and hand-made agents have no live link to judge, so they are not called disconnected.
  return OF_LIVENESS[agentLiveness(a, now).state]
}

/** Worst first: the order a column of connections sorts in. */
export const HEALTH_ORDER: readonly AgentHealth[] = ['offline', 'late', 'waiting', 'expired', 'gone', 'unknown', 'ok']

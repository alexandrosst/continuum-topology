import type { Agent } from './types'

/** How an agent is doing right now, from what the server can tell. */
export type Health = 'ok' | 'late' | 'offline' | 'waiting' | 'expired' | 'gone' | 'unknown'

/** Agents send a heartbeat every 30 seconds; two missed is "late". */
export const LATE_AFTER_MS = 75_000

export function healthOf(a: Agent, now: number): Health {
  if (a.status === 'pending') return 'waiting'
  if (a.status === 'expired') return 'expired'
  if (a.status !== 'approved') return 'gone'
  if (a.connected === false) return 'offline'
  // Sample and hand-made agents have no live link to judge, so they are not called disconnected.
  if (a.connected === undefined) return 'unknown'
  return a.lastHeartbeat && now - new Date(a.lastHeartbeat).getTime() > LATE_AFTER_MS ? 'late' : 'ok'
}

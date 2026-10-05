import { ago } from './observed'
import type { ReceiverAuth, RegionalOperator } from './types'

/**
 * What the UI says about whether a regional operator is alive. The server only knows what the operator's
 * opt-in heartbeat told it, so there are exactly three honest answers - and "not reported" is one of them,
 * never dressed up as offline: an operator that never opted in is not down, it is just not saying.
 */
export type OperatorLiveness =
  | { kind: 'online'; text: string }
  | { kind: 'offline'; text: string }
  | { kind: 'unreported'; text: string }

/** The text is always the whole message; the colour of any dot beside it is only a second channel. */
export function operatorLiveness(op: Pick<RegionalOperator, 'health'>, now = Date.now()): OperatorLiveness {
  const h = op.health
  if (!h || !h.reporting || h.state === 'unknown') return { kind: 'unreported', text: 'Health not reported' }
  if (h.state === 'online') return { kind: 'online', text: 'Online' }
  return { kind: 'offline', text: h.lastSeenAt ? `Offline, last seen ${ago(h.lastSeenAt, now)}` : 'Offline' }
}

/** How this operator's receiver authenticates agents. Absent (an older server, a fixture) reads as 'bearer':
 *  the safe reading, since it only ever adds a field to fill in, never removes one. */
export const receiverAuthOf = (op: Pick<RegionalOperator, 'receiverAuth'> | undefined): ReceiverAuth => (op?.receiverAuth === 'mtls' ? 'mtls' : 'bearer')

/** Whether the operator is reporting health at all - decides "Enable" versus "Rotate" for its credential. */
export const isReportingHealth = (op: Pick<RegionalOperator, 'health'>): boolean => op.health?.reporting === true

import { fusionLabel } from './fusionStatus'
import { ago } from './observed'
import type { ReceiverAuth, RegionalOperator } from './types'

/**
 * What the UI says about whether a regional operator is alive. The server only knows what the operator's
 * opt-in heartbeat told it, so "not reported" is an honest answer of its own, never dressed up as offline: an operator
 * that never opted in is not down, it is just not saying. 'waiting' is the heartbeat being on with nothing received yet.
 * 'starting' / 'off' / 'attention' only describe the central operator, whose health is FUSION's own state.
 */
export type OperatorLiveness = { kind: 'online' | 'offline' | 'unreported' | 'waiting' | 'starting' | 'off' | 'attention'; text: string }

/** The text is always the whole message; the colour of any dot beside it is only a second channel. `central` words the running
 *  state the way FUSION's own panel does ("Running"), since for that operator online means FUSION is up. */
export function operatorLiveness(op: Pick<RegionalOperator, 'health'>, now = Date.now(), opts: { central?: boolean } = {}): OperatorLiveness {
  const h = op.health
  switch (h?.state) {
    case 'waiting':
      return { kind: 'waiting', text: 'Waiting for first heartbeat' }
    case 'starting':
    case 'off':
    case 'attention':
      return { kind: h.state, text: fusionLabel(h.state) }
  }
  // The central operator has no heartbeat of its own: the server reports it online (FUSION is up) with `reporting: false`, which for any
  // other operator would mean "never heard from". So for it `reporting` says nothing, and online is taken at its word.
  if (opts.central && h?.state === 'online') return { kind: 'online', text: fusionLabel('running') }
  if (!h || !h.reporting || h.state === 'unknown') return { kind: 'unreported', text: 'Health not reported' }
  if (h.state === 'online') return { kind: 'online', text: 'Online' }
  return { kind: 'offline', text: h.lastSeenAt ? `Offline, last seen ${ago(h.lastSeenAt, now)}` : 'Offline' }
}

/** How this operator's receiver authenticates agents. Absent (an older server, a fixture) reads as 'bearer':
 *  the safe reading, since it only ever adds a field to fill in, never removes one. */
export const receiverAuthOf = (op: Pick<RegionalOperator, 'receiverAuth'> | undefined): ReceiverAuth => (op?.receiverAuth === 'mtls' ? 'mtls' : 'bearer')

/** Whether the operator is reporting health at all - decides "Enable" versus "Rotate" for its credential. */
export const isReportingHealth = (op: Pick<RegionalOperator, 'health'>): boolean => op.health?.reporting === true

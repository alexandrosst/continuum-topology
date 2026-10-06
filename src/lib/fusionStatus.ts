import type { FusionStatus } from '@/lib/api'

/** The id of the server-owned regional operator in front of FUSION (backend CentralOperatorID). */
export const CENTRAL_OPERATOR_ID = 'op-central'

export type FusionKind = 'unavailable' | 'off' | 'starting' | 'running' | 'attention'

/** FUSION's state as one sentence, the way an operator's health is one: the words carry the meaning, the dot repeats it. */
export function fusionSentence(s: FusionStatus | null): { kind: FusionKind; text: string } {
  if (!s) return { kind: 'unavailable', text: 'Checking FUSION…' }
  if (!s.available) return { kind: 'unavailable', text: s.message ?? 'FUSION cannot be switched from this server.' }
  const parts = s.components ?? []
  const up = parts.filter((c) => c.desired > 0 && c.ready >= c.desired).length
  const wanted = parts.filter((c) => c.desired > 0).length
  switch (s.state) {
    case 'running':
      return { kind: 'running', text: 'Running - the central operator and the three stores are up.' }
    case 'starting':
      return { kind: 'starting', text: `Starting - ${up} of ${wanted} parts are up.` }
    case 'attention':
      return { kind: 'attention', text: s.message ?? 'Needs attention.' }
    default:
      return { kind: 'off', text: 'Off - nothing is running. Anything already saved stays on its volumes.' }
  }
}

/** Whether a regional operator that sends to the central operator has somewhere to send right now. */
export function fusionUsable(s: FusionStatus | null): boolean {
  return !!s && s.available && (s.state === 'running' || s.state === 'starting')
}

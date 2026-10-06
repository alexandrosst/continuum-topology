import type { FusionStatus } from '@/lib/api'
import { ago } from '@/lib/observed'

/** The id of the server-owned regional operator in front of FUSION (backend CentralOperatorID). */
export const CENTRAL_OPERATOR_ID = 'op-central'

/** 'checking' = the first answer has not arrived; 'unavailable' = this server cannot switch FUSION at all. */
export type FusionKind = 'checking' | 'unavailable' | 'off' | 'starting' | 'running' | 'attention'

/** The one word for each state, used by every place that names FUSION's state (the panel, the central row, the picker), so
 *  they cannot drift apart. */
const LABEL: Record<FusionKind, string> = {
  checking: 'Checking',
  unavailable: 'Unavailable',
  starting: 'Starting',
  running: 'Running',
  off: 'Off',
  attention: 'Needs attention',
}
export const fusionLabel = (kind: FusionKind): string => LABEL[kind]

/** "12 s ago" where `ago` would only say "just now": the point of showing when data last arrived is to see it moving. */
function dataAge(iso: string, now: number): string {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  return s < 60 ? `${s} s ago` : ago(iso, now)
}

/** FUSION's state as one sentence, the way an operator's health is one: the words carry the meaning, the dot repeats it. */
export function fusionSentence(s: FusionStatus | null, now = Date.now()): { kind: FusionKind; text: string } {
  if (!s) return { kind: 'checking', text: 'Checking FUSION…' }
  if (!s.available) return { kind: 'unavailable', text: s.message ?? 'FUSION cannot be switched from this server.' }
  // Grafana is a convenience on top of the stores: it can come up late, or be switched off in the release, without FUSION being "starting".
  const parts = (s.components ?? []).filter((c) => c.component !== 'grafana')
  const up = parts.filter((c) => c.desired > 0 && c.ready >= c.desired).length
  const wanted = parts.filter((c) => c.desired > 0).length
  switch (s.state) {
    case 'running':
      return { kind: 'running', text: `Running - ${s.lastDataAt ? `last data ${dataAge(s.lastDataAt, now)}` : 'waiting for first data'}` }
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

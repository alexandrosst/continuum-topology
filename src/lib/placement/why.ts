import type { Class } from '../advice'
import type { ObsInfo, ObsKind } from '../provenance'

/** "2 of 3 inputs are guesses": how much of what decides an answer is not solid. */
export function inputsSummary(inputs: { class: Class }[]): string {
  const guesses = inputs.filter((i) => i.class === 'guess').length
  const unknown = inputs.filter((i) => i.class === 'unknown').length
  const parts: string[] = []
  const n = inputs.length
  if (guesses > 0) parts.push(`${guesses} of ${n} input${n === 1 ? ' is a guess' : 's are guesses'}`)
  if (unknown > 0) parts.push(`${unknown} of ${n} input${n === 1 ? ' is' : 's are'} unknown`)
  return parts.join(', ')
}

const NAMES: Record<string, string> = {
  cpuFree: 'Free CPU',
  memoryFree: 'Free memory',
  cpuRequest: 'CPU asked for',
  memoryRequest: 'Memory asked for',
  roundTripMs: 'Round trip',
  bytesPerSec: 'Traffic',
  dataResidency: 'Data residency',
  trustZone: 'Trust zone',
  cpuAllocatable: 'CPU the node offers',
  memoryAllocatable: 'Memory the node offers',
}
export const factName = (a: string) => NAMES[a] ?? a.replace(/([A-Z])/g, ' $1').toLowerCase().replace(/^./, (c) => c.toUpperCase())

const OBS_KINDS: ObsKind[] = ['live', 'disconnected', 'stale', 'revoked', 'gone']
const TONE = { live: 'ok', disconnected: 'warn', stale: 'warn', revoked: 'bad', gone: 'muted' } as const

/** A fact's observation state as the chip everything else uses. A state nobody observes (declared) has none. */
export function obsOfState(state: string | undefined): ObsInfo | undefined {
  if (!state || state === 'declared') return undefined
  if (!OBS_KINDS.includes(state as ObsKind)) return undefined
  const kind = state as ObsKind
  return { kind, label: kind, tone: TONE[kind], actionable: kind === 'live' }
}


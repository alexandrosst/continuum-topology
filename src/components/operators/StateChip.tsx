import clsx from 'clsx'
import { AlertTriangle, Check, CircleDashed, HelpCircle, XCircle } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { TONE_CLASS, type Tone } from '@/lib/provenance'
import { type State } from '@/lib/operatorsView'
import { HEALTH_WORD } from '@/lib/status'

const GLYPH = { healthy: Check, attention: AlertTriangle, down: XCircle, setup: CircleDashed, unknown: HelpCircle }
const TONE: Record<Exclude<State, 'setup'>, Tone> = { healthy: 'ok', attention: 'warn', down: 'bad', unknown: 'muted' }
/** "Setting up" is neither good nor bad news, so it has a colour of its own (the info blue) and a glyph that pulses: it is waiting, not stuck. */
const toneClass = (state: State): string => (state === 'setup' ? 'border-info/30 bg-info/10 text-info' : TONE_CLASS[TONE[state]])

/** A component's status: always a glyph, a word and a colour, so it never rests on telling green from red. The same chip as the Agents
 *  page's health chips. */
export default function StateChip({ state, count, compact, className }: { state: State; /** How many are in this state: "1 needs attention" instead of the bare word. */ count?: number; /** With a count: only the glyph and the number, so a chip never grows past a "Healthy" one; the words stay in its name and tooltip. */ compact?: boolean; className?: string }) {
  const Glyph = GLYPH[state]
  const full = count === undefined ? HEALTH_WORD[state] : `${count} ${HEALTH_WORD[state].toLowerCase()}`
  if (compact && count !== undefined) {
    return (
      <span className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-px text-[11px] font-medium tabular-nums', toneClass(state), className)} data-state={state} title={full} aria-label={full}>
        <Glyph size={ICON_SM} aria-hidden className={state === 'setup' ? 'animate-pulse motion-reduce:animate-none' : undefined} /> {count}
      </span>
    )
  }
  return (
    <span className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-px text-[11px] font-medium', toneClass(state), className)} data-state={state}>
      <Glyph size={ICON_SM} aria-hidden className={state === 'setup' ? 'animate-pulse motion-reduce:animate-none' : undefined} /> {full}
    </span>
  )
}

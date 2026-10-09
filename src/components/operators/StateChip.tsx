import clsx from 'clsx'
import { AlertTriangle, Check, HelpCircle, XCircle } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { TONE_CLASS, type Tone } from '@/lib/provenance'
import { STATE_WORD, type State } from '@/lib/operatorsView'

const GLYPH = { healthy: Check, attention: AlertTriangle, down: XCircle, unknown: HelpCircle }
const TONE: Record<State, Tone> = { healthy: 'ok', attention: 'warn', down: 'bad', unknown: 'muted' }

/** A component's status: always a glyph, a word and a colour, so it never rests on telling green from red. The same chip as the Agents
 *  page's health chips. */
export default function StateChip({ state, count, className }: { state: State; /** How many are in this state: "1 needs attention" instead of the bare word. */ count?: number; className?: string }) {
  const Glyph = GLYPH[state]
  return (
    <span className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-px text-[11px] font-medium', TONE_CLASS[TONE[state]], className)} data-state={state}>
      <Glyph size={ICON_SM} aria-hidden /> {count === undefined ? STATE_WORD[state] : `${count} ${STATE_WORD[state].toLowerCase()}`}
    </span>
  )
}

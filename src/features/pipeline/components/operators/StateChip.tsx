import { StatusBadge, STATUS_WORD } from '@/components/ui/Status'
import { type State } from '@/features/pipeline/lib/operatorsView'

/** A component's status: always a glyph, a word and a colour, so it never rests on telling green from red. The one `Status` chip every
 *  screen uses; `State` is the pipeline's name for the same tones. */
export default function StateChip({ state, count, compact, className }: { state: State; /** How many are in this state: "1 needs attention" instead of the bare word. */ count?: number; /** With a count: only the glyph and the number, so a chip never grows past a "Healthy" one; the words stay in its name and tooltip. */ compact?: boolean; className?: string }) {
  const label = count === undefined ? STATUS_WORD[state] : `${count} ${STATUS_WORD[state].toLowerCase()}`
  return <StatusBadge tone={state} label={label} count={compact ? count : undefined} compact={compact && count !== undefined} title={compact && count !== undefined ? label : undefined} className={className} data-state={state} />
}

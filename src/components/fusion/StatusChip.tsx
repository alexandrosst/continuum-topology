import clsx from 'clsx'
import { CircleCheck, CircleHelp, CircleX, Loader2, TriangleAlert } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { HEALTH_WORD, type Shown } from '@/lib/fusionStatus'
import { TONE_CLASS, type Tone } from '@/lib/provenance'

const LOOK: Record<Shown, { tone: Tone; icon?: typeof CircleCheck }> = {
  healthy: { tone: 'ok', icon: CircleCheck },
  attention: { tone: 'warn', icon: TriangleAlert },
  broken: { tone: 'bad', icon: CircleX },
  unknown: { tone: 'muted', icon: CircleHelp },
  starting: { tone: 'muted', icon: Loader2 },
  off: { tone: 'muted' },
}

/** A status as the glyph, the word and the colour together, the way the agents' health chip is drawn: Healthy, Needs attention, Not working or
 *  Unknown, and for a part that is coming up or switched off, Starting and Off. */
export function StatusChip({ status, className, ...p }: { status: Shown; className?: string; 'data-testid'?: string }) {
  const { tone, icon: Icon } = LOOK[status]
  return (
    <span {...p} className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-px text-[11px] font-medium', TONE_CLASS[tone], className)} data-status={status}>
      {Icon && <Icon size={ICON_SM} className={status === 'starting' ? 'animate-spin' : undefined} aria-hidden />}
      {HEALTH_WORD[status]}
    </span>
  )
}

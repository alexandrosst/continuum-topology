import clsx from 'clsx'
import { Check, CircleDashed, CircleHelp, CircleX, Info, Minus, TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { HEALTH_WORD } from '@/lib/status'

/**
 * How anything in the product is doing, drawn one way. Every screen used to draw its own chip (a health chip, a state chip,
 * FUSION's status chip, an approval pill, an observation chip); they differed in glyph, size and corner. This is the one look:
 * a glyph, a word and a colour together, so that none of it rests on telling green from red, and a dot (`StatusPip`) that repeats
 * the same shape language where only a dot fits.
 *
 *   healthy     filled green, check          Healthy
 *   attention   filled amber, triangle       Needs attention
 *   down        filled red, cross            Not working
 *   setup       hollow blue, dashed circle   Setting up   (asked for, not up yet: neither good nor bad news)
 *   unknown     hollow grey, question mark   Unknown     (a real answer: this cannot be told)
 *   off         flat grey, dash              Off         (switched off on purpose)
 *   notice      grey, info glyph             Notice      (worth reading, not a fault)
 */
export type StatusTone = 'healthy' | 'attention' | 'down' | 'setup' | 'unknown' | 'off' | 'notice'

/** The icon size of primitives.tsx's ICON_SM, restated: primitives draws its dots with this file, so it cannot import from there. */
const GLYPH = 14

const LOOK: Record<StatusTone, { cls: string; dot: string; Glyph: typeof Check; word: string; hollow?: boolean }> = {
  healthy: { cls: 'border-ok/30 bg-ok/10 text-ok', dot: 'bg-ok', Glyph: Check, word: HEALTH_WORD.healthy },
  attention: { cls: 'border-warn/30 bg-warn/10 text-warn', dot: 'bg-warn', Glyph: TriangleAlert, word: HEALTH_WORD.attention },
  down: { cls: 'border-bad/30 bg-bad/10 text-bad', dot: 'bg-bad', Glyph: CircleX, word: HEALTH_WORD.down },
  setup: { cls: 'border-info/30 bg-info/10 text-info', dot: 'border-info', Glyph: CircleDashed, word: HEALTH_WORD.setup, hollow: true },
  unknown: { cls: 'border-nb-700 bg-nb-930 text-nb-400', dot: 'border-nb-500', Glyph: CircleHelp, word: HEALTH_WORD.unknown, hollow: true },
  off: { cls: 'border-nb-700 bg-nb-930 text-nb-400', dot: 'bg-nb-600', Glyph: Minus, word: 'Off' },
  notice: { cls: 'border-nb-700 bg-nb-930 text-nb-400', dot: 'bg-nb-500', Glyph: Info, word: 'Notice' },
}

/** The words a tone gets when the caller has none of its own. */
export const STATUS_WORD: Record<StatusTone, string> = { healthy: LOOK.healthy.word, attention: LOOK.attention.word, down: LOOK.down.word, setup: LOOK.setup.word, unknown: LOOK.unknown.word, off: LOOK.off.word, notice: LOOK.notice.word }

/** True for a moment after `value` changes, so a status that flips while someone is looking is noticed instead of cutting over. */
function useChanged<T>(value: T, ms = 800): boolean {
  const prev = useRef(value)
  const [on, setOn] = useState(false)
  useEffect(() => {
    if (prev.current === value) return
    prev.current = value
    setOn(true)
    const t = setTimeout(() => setOn(false), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return on
}

/**
 * The chip (`StatusBadge`). `label` replaces the default words ("2 problems", "Fully discovered"); `count` with `compact` is the glyph and the number
 * only, the words kept in the tooltip and the accessible name. Extra props (`data-*`) land on the element so tests and callers can
 * still find it.
 */
export function StatusBadge({ tone, label, count, compact, title, className, children, ...rest }: { tone: StatusTone; label?: ReactNode; count?: number; compact?: boolean; title?: string; className?: string; children?: ReactNode } & Record<`data-${string}`, string | undefined>) {
  const { cls, Glyph, word } = LOOK[tone]
  const changed = useChanged(tone)
  const words = label ?? word
  const spoken = typeof label === 'string' ? label : count === undefined ? undefined : `${count} ${word.toLowerCase()}`
  const glyph = <Glyph size={GLYPH} aria-hidden className={clsx('shrink-0', tone === 'setup' && 'motion-safe:animate-pulse')} />
  return (
    <span
      data-tone={tone}
      {...rest}
      title={title ?? (compact ? spoken : undefined)}
      aria-label={compact ? spoken : undefined}
      className={clsx('status-chip inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-px text-[11px] font-medium leading-4', cls, changed && 'flash-bg', className)}
    >
      {glyph}
      {compact && count !== undefined ? <span className="tabular-nums">{count}</span> : (children ?? words)}
    </span>
  )
}

/**
 * The dot: the same tones as the chip, for the places a chip does not fit (a table cell beside a name, a sidebar row). `pulse` is for the
 * one state that means "alive right now": it should be the only thing that moves while everything is fine.
 */
export function StatusPip({ tone, pulse, size = 'size-1.5', className }: { tone: StatusTone; pulse?: boolean; size?: string; className?: string }) {
  const { dot, hollow } = LOOK[tone]
  const changed = useChanged(tone)
  return (
    <span className={clsx('relative inline-flex shrink-0 rounded-full', size, changed && 'flash-ring', className)} aria-hidden>
      {pulse && <span className={clsx('absolute inline-flex size-full rounded-full opacity-75 motion-safe:animate-ping', dot)} />}
      <span className={clsx('relative inline-flex rounded-full', size, hollow ? clsx('border', dot, tone === 'setup' && 'motion-safe:animate-pulse') : dot)} />
    </span>
  )
}

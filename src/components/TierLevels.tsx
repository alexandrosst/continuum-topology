import clsx from 'clsx'
import { Lock } from 'lucide-react'
import { ACCESS_TIERS, ACCESS_TIER_CAPTIONS, type AccessTier } from '@/lib/types'

// Wider bar = more is read. Kept short so five tiers still fit a narrow modal; the label and caption carry the detail.
const BAR_WIDTH: Record<AccessTier, number> = { 0: 22, 1: 40, 2: 58, 3: 76, 4: 94 }

export interface TierLevelsProps {
  /** Which tiers to render, as rungs, lowest first. */
  tiers: AccessTier[]
  /** Every rung at or below this value is drawn filled: picking a tier always includes everything narrower. */
  value?: AccessTier
  /** Rungs above this are still drawn, but locked (dimmed, with a lock mark) instead of hidden. */
  max?: AccessTier
  /** Present to make rungs clickable. Called with the rung's own tier even when it's locked — the caller decides
   *  what a locked click means (the three call sites differ: hidden entirely, or shown with an upgrade hint). */
  onSelect?: (tier: AccessTier) => void
  /** A second, lighter mark for a tier that isn't the current value but is still worth calling out (e.g. what's
   *  approved, when the agent isn't collecting that much yet). Only drawn when different from `value`. */
  markAt?: AccessTier
  markLabel?: string
  size?: 'sm' | 'md'
  className?: string
  'data-testid'?: string
  'aria-labelledby'?: string
}

/** A small stacked-levels indicator for the access-tier ladder: each rung is a strict superset of the one above it,
 *  which a radio list or a `<select>` never quite says out loud. Used both as the picker (wizard, approval card,
 *  the agent's consent panel) and, in read-only form, as a compact "what this agent can see" indicator. */
export default function TierLevels({ tiers, value, max, onSelect, markAt, markLabel, size = 'md', className, 'data-testid': testId, 'aria-labelledby': labelledBy }: TierLevelsProps) {
  const compact = size === 'sm'
  return (
    <div className={clsx('flex flex-col', compact ? 'gap-1' : 'gap-1.5', className)} data-testid={testId} role={onSelect ? 'radiogroup' : undefined} aria-labelledby={labelledBy}>
      {tiers.map((t) => {
        const filled = value !== undefined && t <= value
        const locked = max !== undefined && t > max
        const marked = markAt !== undefined && markAt === t && markAt !== value
        const label = ACCESS_TIERS.find((a) => a.value === t)?.label ?? `Tier ${t}`
        const caption = ACCESS_TIER_CAPTIONS[t]
        const isCurrent = value === t
        const Tag = onSelect ? 'button' : 'div'
        return (
          <Tag
            key={t}
            type={onSelect ? 'button' : undefined}
            role={onSelect ? 'radio' : undefined}
            aria-checked={onSelect ? isCurrent : undefined}
            aria-disabled={locked || undefined}
            title={locked ? 'Above what this install allows.' : undefined}
            onClick={onSelect ? () => onSelect(t) : undefined}
            data-testid={testId ? `${testId}-${t}` : undefined}
            className={clsx(
              'flex items-start gap-3 rounded-lg border text-left transition-colors',
              compact ? 'px-2.5 py-1.5' : 'px-4 py-3',
              onSelect && !locked && 'cursor-pointer',
              locked && 'cursor-default opacity-60',
              isCurrent ? 'border-accent/60 bg-accent-soft' : locked ? 'border-nb-850 bg-nb-930' : 'border-nb-850 bg-nb-925 hover:bg-nb-930',
            )}
          >
            <span className="mt-1 flex shrink-0 items-center" aria-hidden>
              <span
                className={clsx('rounded-sm', filled ? 'bg-accent' : locked ? 'bg-nb-800' : 'bg-nb-700', marked && 'ring-2 ring-amber-400/70')}
                style={{ width: BAR_WIDTH[t], height: compact ? 5 : 6 }}
              />
            </span>
            <span className="min-w-0 flex-1">
              <span className={clsx('flex items-center gap-1.5 font-medium text-white', compact ? 'text-xs' : 'text-sm')}>
                {label}
                {locked && <Lock size={11} className="text-nb-500" aria-hidden />}
              </span>
              {!compact && <span className="block text-sm text-nb-500">{caption}</span>}
              {marked && <span className="block text-xs text-amber-300">{markLabel ?? `Approved up to here`}</span>}
            </span>
          </Tag>
        )
      })}
    </div>
  )
}

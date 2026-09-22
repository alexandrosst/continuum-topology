import clsx from 'clsx'
import { Lock } from 'lucide-react'
import { ACCESS_TIERS, ACCESS_TIER_CAPTIONS, type AccessTier } from '@/lib/types'

const ICON_SIZE = { sm: 18, md: 24 } as const

/** Tier N's icon is N nested squares (tier 0: just a center dot — identity proven, nothing boxed up yet). Reading
 *  down the list, each rung literally draws one more box around the one before it: "Services" (tier 2) is drawn as
 *  a box around "Infrastructure" (tier 1)'s own box, the same relationship the two tiers actually have. A width- or
 *  length-scaled bar said "more" without saying "everything in the shorter one, plus"; this says the second part too. */
function TierIcon({ level, tone, marked, size }: { level: number; tone: 'filled' | 'locked' | 'idle'; marked: boolean; size: number }) {
  const border = tone === 'filled' ? 'border-accent' : tone === 'locked' ? 'border-nb-800' : 'border-nb-700'
  const dot = tone === 'filled' ? 'bg-accent' : tone === 'locked' ? 'bg-nb-800' : 'bg-nb-700'
  if (level === 0) {
    return (
      <span className="flex shrink-0 items-center justify-center" style={{ width: size, height: size }}>
        <span className={clsx('rounded-full', dot)} style={{ width: size * 0.28, height: size * 0.28 }} />
      </span>
    )
  }
  const step = size / (level * 2.4)
  return (
    <span
      className={clsx('relative shrink-0', marked && 'rounded-[3px] ring-2 ring-amber-400/70 ring-offset-1 ring-offset-nb-925')}
      style={{ width: size, height: size }}
    >
      {Array.from({ length: level }, (_, i) => (
        <span key={i} className={clsx('absolute rounded-[3px] border-[1.5px]', border)} style={{ inset: i * step }} />
      ))}
    </span>
  )
}

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

/** The access-tier ladder: each rung is a strict superset of the one above it, drawn as literally nested boxes
 *  (see TierIcon) and spelled out in its caption ("Everything in X, plus..."), which a radio list or a `<select>`
 *  never said either way. Used both as the picker (wizard, approval card, the agent's consent panel) and, in
 *  read-only form, as a compact "what this agent can see" indicator. */
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
            <span className="mt-0.5 shrink-0" aria-hidden>
              <TierIcon level={t} tone={locked ? 'locked' : filled ? 'filled' : 'idle'} marked={marked} size={ICON_SIZE[size]} />
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

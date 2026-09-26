import clsx from 'clsx'
import { Check, Lock } from 'lucide-react'
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
  // Each box insets a bit further than the last, so `level` of them nest visibly inside `size` without the
  // innermost one collapsing to a sliver. 2.4 is tuned by eye for the range this actually renders at (2-4
  // levels, an 18-24px icon): at 4 levels and the smallest icon, the innermost box is still ~6-7px on a side.
  const step = size / (level * 2.4)
  return (
    <span
      className={clsx('relative shrink-0', marked && 'rounded-[3px] ring-2 ring-warn/70 ring-offset-1 ring-offset-nb-925')}
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
  /** 'stack': one rung per row (the default - approval card, consent panel, read-only indicators).
   *  'cards': the same rungs side by side instead, sized for a handful of options a person compares
   *  before picking one. Meant for the connect wizard, where there is room and a decision to make;
   *  the stack reads better once there are more rungs than fit a row, or no room to spare. */
  layout?: 'stack' | 'cards'
  className?: string
  'data-testid'?: string
  'aria-labelledby'?: string
}

/** The access-tier ladder: each rung is a strict superset of the one above it, drawn as literally nested boxes
 *  (see TierIcon) and spelled out in its caption ("Everything in X, plus..."), which a radio list or a `<select>`
 *  never said either way. Used both as the picker (wizard, approval card, the agent's consent panel) and, in
 *  read-only form, as a compact "what this agent can see" indicator. */
export default function TierLevels({ tiers, value, max, onSelect, markAt, markLabel, size = 'md', layout = 'stack', className, 'data-testid': testId, 'aria-labelledby': labelledBy }: TierLevelsProps) {
  const compact = size === 'sm'
  const cards = layout === 'cards'
  return (
    <div
      className={clsx(cards ? 'grid gap-3 sm:grid-cols-2' : clsx('flex flex-col', compact ? 'gap-1' : 'gap-1.5'), className)}
      data-testid={testId}
      role={onSelect ? 'radiogroup' : undefined}
      aria-labelledby={labelledBy}
    >
      {tiers.map((t) => {
        const filled = value !== undefined && t <= value
        const locked = max !== undefined && t > max
        const marked = markAt !== undefined && markAt === t && markAt !== value
        const label = ACCESS_TIERS.find((a) => a.value === t)?.label ?? `Tier ${t}`
        const caption = ACCESS_TIER_CAPTIONS[t]
        const isCurrent = value === t
        const Tag = onSelect ? 'button' : 'div'
        const shared = {
          type: onSelect ? 'button' : undefined,
          role: onSelect ? 'radio' : undefined,
          'aria-checked': onSelect ? isCurrent : undefined,
          'aria-disabled': locked || undefined,
          title: locked ? 'Above what this install allows.' : undefined,
          onClick: onSelect ? () => onSelect(t) : undefined,
          'data-testid': testId ? `${testId}-${t}` : undefined,
        } as const
        if (cards) {
          return (
            <Tag
              key={t}
              {...shared}
              className={clsx(
                'relative flex flex-col items-start gap-2.5 rounded-xl border p-4 text-left transition-all',
                onSelect && !locked && 'cursor-pointer hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/20',
                locked && 'cursor-default opacity-60',
                isCurrent ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : locked ? 'border-nb-850 bg-nb-930' : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
              )}
            >
              {isCurrent && (
                <span className="absolute right-3 top-3 flex size-5 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
                  <Check size={13} strokeWidth={3} />
                </span>
              )}
              <TierIcon level={t} tone={locked ? 'locked' : filled ? 'filled' : 'idle'} marked={marked} size={28} />
              <span className="flex items-center gap-1.5 text-sm font-medium text-nb-300">
                {label}
                {locked && <Lock size={12} className="text-nb-500" aria-hidden />}
              </span>
              <span className="text-sm leading-snug text-nb-400">{caption}</span>
              {marked && <span className="text-xs text-warn">{markLabel ?? `Approved up to here`}</span>}
            </Tag>
          )
        }
        return (
          <Tag
            key={t}
            {...shared}
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
              <span className={clsx('flex items-center gap-1.5 font-medium text-nb-300', compact ? 'text-xs' : 'text-sm')}>
                {label}
                {locked && <Lock size={11} className="text-nb-500" aria-hidden />}
              </span>
              {!compact && <span className="block text-sm text-nb-500">{caption}</span>}
              {marked && <span className="block text-xs text-warn">{markLabel ?? `Approved up to here`}</span>}
            </span>
          </Tag>
        )
      })}
    </div>
  )
}

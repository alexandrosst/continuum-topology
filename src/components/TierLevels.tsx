import clsx from 'clsx'
import { Boxes, Check, Fingerprint, HardDrive, Lock, Share2, ShieldCheck, type LucideIcon } from 'lucide-react'
import { ACCESS_TIERS, ACCESS_TIER_CAPTIONS, type AccessTier } from '@/lib/types'
import { ICON_MD, ICON_SM } from '@/components/ui/primitives'

const ICON_SIZE = { sm: 18, md: 24 } as const

/** One concrete, recognizable icon per tier, not an abstract rung count. One/two/three nested boxes at the sizes
 *  this actually renders at (18-24px) read as "dots" rather than distinct tiers - a fixed icon per tier reads at
 *  a glance, independent of its position in the list. The cumulative "everything in the tier below, plus" fact is
 *  carried by ACCESS_TIER_CAPTIONS' own wording (the single source of truth for that), not by the icon - the
 *  icon's job is just "which tier is this", not "how many tiers deep are we". */
const TIER_LEVEL_ICON: Record<number, LucideIcon> = {
  0: Fingerprint, // proves identity, nothing else is read
  1: HardDrive, // nodes, storage classes, ingress classes - what the cluster is made of
  2: Boxes, // + namespaces, workloads, pods, services, ingresses - what runs on it
  3: Share2, // + dependencies between services (not available yet)
  4: ShieldCheck, // + control (not available yet)
}

function TierIcon({ level, tone, marked, size }: { level: number; tone: 'filled' | 'locked' | 'idle'; marked: boolean; size: number }) {
  const Icon = TIER_LEVEL_ICON[level] ?? HardDrive
  const color = tone === 'filled' ? 'text-accent' : tone === 'locked' ? 'text-nb-700' : 'text-nb-600'
  return (
    <span
      className={clsx('relative flex shrink-0 items-center justify-center', marked && 'rounded-full ring-2 ring-warn/70 ring-offset-1 ring-offset-nb-925')}
      style={{ width: size, height: size }}
    >
      <Icon size={size * 0.72} strokeWidth={tone === 'filled' ? 2.25 : 1.75} className={color} aria-hidden />
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
                  <Check size={ICON_MD} strokeWidth={3} />
                </span>
              )}
              <TierIcon level={t} tone={locked ? 'locked' : filled ? 'filled' : 'idle'} marked={marked} size={28} />
              <span className="flex items-center gap-1.5 text-sm font-medium text-nb-300">
                {label}
                {locked && <Lock size={ICON_SM} className="text-nb-500" aria-hidden />}
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
                {locked && <Lock size={ICON_SM} className="text-nb-500" aria-hidden />}
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

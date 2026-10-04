import clsx from 'clsx'
import { Boxes, Check, ChevronRight, Fingerprint, HardDrive, Lock, Share2, ShieldCheck, type LucideIcon } from 'lucide-react'
import { ACCESS_TIER_BASELINE_GRANT, ACCESS_TIER_GRANTS, ACCESS_TIERS, ACCESS_TIER_CAPTIONS, type AccessTier } from '@/lib/types'
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

/**
 * "What this grants" / "How you give it", collapsed by default. Deliberately a sibling of the rung's own
 * clickable element (button/radio when onSelect is set), never nested inside it: `<details>`/`<summary>`
 * is interactive content, and interactive content inside a `<button>` is both invalid HTML and would fire
 * the rung's own onClick (select this tier) on every click meant for the disclosure instead.
 *
 * Sourced from ACCESS_TIER_GRANTS (src/lib/types.ts), the one place the real RBAC verbs/resources live -
 * see that constant's own comment for how it is kept honest against rbac.yaml. An unimplemented tier (3/4)
 * has nothing to expand, so this renders their caption's own "Not available yet." directly instead of an
 * empty disclosure - true at any size, including the compact rungs below that hide the caption text itself.
 */
function GrantDisclosure({ tier, compact, testId }: { tier: AccessTier; compact: boolean; testId?: string }) {
  const g = ACCESS_TIER_GRANTS[tier]
  const unimplemented = g.grants.length === 1 && g.grants[0] === 'Not available yet.'
  const textSize = compact ? 'text-[11px]' : 'text-xs'
  if (unimplemented) {
    return <p className={clsx('mt-1', textSize, 'text-nb-600')}>Not available yet.</p>
  }
  return (
    <details className="group/grant mt-1" data-testid={testId}>
      <summary className={clsx('flex cursor-pointer select-none items-center gap-1 text-nb-600 hover:text-nb-400 marker:content-none', textSize)}>
        <ChevronRight size={ICON_SM} className="shrink-0 transition-transform group-open/grant:rotate-90" aria-hidden />
        What this grants
      </summary>
      <div className={clsx('mt-1.5 space-y-1.5 rounded-md border border-nb-850 bg-nb-950/60 px-2.5 py-2 leading-relaxed text-nb-500', textSize)}>
        <p className="text-nb-400">{tier === 0 ? 'Grants nothing beyond proving which cluster this is:' : 'Cumulative — everything this tier and every tier below it grants:'}</p>
        <ul className="list-disc space-y-0.5 pl-3.5">
          {g.grants.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
        <p>Every verb above is read-only — get/list/watch only. Never create, update, delete, exec, or Secrets/ConfigMaps at any tier.</p>
        <p className="text-nb-600">{ACCESS_TIER_BASELINE_GRANT}</p>
        <p className="text-nb-400">
          {g.setBy ? (
            <>
              Granted by <code className="font-mono text-nb-300">{g.setBy}</code> — already part of the install command above, or the <code className="font-mono text-nb-300">helm upgrade</code> shown when raising an existing install's ceiling.
            </>
          ) : (
            <>This is tier 0, the default: no flag needed, nothing to grant beyond it.</>
          )}
        </p>
      </div>
    </details>
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
 *  read-only form, as a compact "what this agent can see" indicator. Each rung also carries a collapsed-by-default
 *  "What this grants" disclosure (see GrantDisclosure) — the actual RBAC verbs/resources this tier's install
 *  applies, and the flag that applies them, so "what does this need" and "what do I do to grant it" are answered
 *  right where the tier is picked or viewed, not left to a separate doc that can drift from the chart. */
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
        const rungTestId = testId ? `${testId}-${t}` : undefined
        const shared = {
          type: onSelect ? 'button' : undefined,
          role: onSelect ? 'radio' : undefined,
          'aria-checked': onSelect ? isCurrent : undefined,
          'aria-disabled': locked || undefined,
          title: locked ? 'Above what this install allows.' : undefined,
          onClick: onSelect ? () => onSelect(t) : undefined,
          'data-testid': rungTestId,
        } as const
        if (cards) {
          return (
            <div key={t} className="flex flex-col">
              <Tag
                {...shared}
                className={clsx(
                  'relative flex flex-1 flex-col items-start gap-2.5 rounded-xl border p-4 text-left transition-all',
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
              <div className="px-1">
                <GrantDisclosure tier={t} compact={false} testId={rungTestId ? `${rungTestId}-grants` : undefined} />
              </div>
            </div>
          )
        }
        return (
          <div
            key={t}
            className={clsx(
              'rounded-lg border transition-colors',
              compact ? 'px-2.5 py-1.5' : 'px-4 py-3',
              isCurrent ? 'border-accent/60 bg-accent-soft' : locked ? 'border-nb-850 bg-nb-930' : 'border-nb-850 bg-nb-925',
            )}
          >
            <Tag
              {...shared}
              className={clsx('flex w-full items-start gap-3 text-left', onSelect && !locked && 'cursor-pointer', locked && 'cursor-default opacity-60')}
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
            <div className={compact ? 'pl-[30px]' : 'pl-11'}>
              <GrantDisclosure tier={t} compact={compact} testId={rungTestId ? `${rungTestId}-grants` : undefined} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

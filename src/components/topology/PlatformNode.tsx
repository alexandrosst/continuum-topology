import clsx from 'clsx'
import { Cable, CircleCheck, CircleHelp, CircleX, Database, DoorOpen, Funnel, Merge, TriangleAlert, type LucideIcon } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformKind, type PlatformStatus } from '@/lib/platformLayer'

export const PLATFORM_ICON: Record<PlatformKind, LucideIcon> = { agent: Cable, local: Funnel, regional: Merge, central: DoorOpen, fusion: Database }

const GLYPH: Record<PlatformStatus, { icon: LucideIcon; tone: string }> = {
  healthy: { icon: CircleCheck, tone: 'text-ok' },
  attention: { icon: TriangleAlert, tone: 'text-warn' },
  down: { icon: CircleX, tone: 'text-bad' },
  unknown: { icon: CircleHelp, tone: 'text-nb-500' },
}

/** One of the four states as a glyph - its shape says it as well as its colour - with the word as its name, for a screen reader and the tooltip. */
export function StatusGlyph({ status, size = ICON_SM, className }: { status: PlatformStatus; size?: number; className?: string }) {
  const { icon: Icon, tone } = GLYPH[status]
  return <Icon size={size} className={clsx('shrink-0', tone, className)} role="img" aria-label={PLATFORM_STATUS_WORD[status]} data-status={status} />
}

/**
 * A part of the telemetry platform, in the same node language as a service card: a bordered box with an icon tile, a name and a line
 * under it. The state is its glyph on the right; anything but Healthy is also written under the name.
 */
export function PlatformNode({ entity, selected, label, className, style, ...handlers }: {
  entity: PlatformEntity
  selected?: boolean
  /** What a screen reader hears; the visible text is not all of it. */
  label: string
  className?: string
  style?: React.CSSProperties
  onClick: () => void
  onMouseEnter?: () => void
  onMouseLeave?: () => void
  onFocus?: () => void
  onBlur?: () => void
}) {
  const Icon = PLATFORM_ICON[entity.kind]
  const healthy = entity.status === 'healthy'
  return (
    <button
      type="button"
      data-testid="platform-node"
      data-platform={entity.kind}
      aria-label={label}
      aria-pressed={selected}
      style={style}
      className={clsx(
        'flex items-center gap-2 rounded-xl border bg-nb-925 px-2.5 text-left transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        selected ? 'border-accent shadow-[inset_0_0_0_1px_var(--color-accent)]' : 'border-nb-800 hover:border-nb-700',
        entity.off && 'border-dashed',
        className,
      )}
      {...handlers}
    >
      <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-nb-900 text-nb-400">
        <Icon size={ICON_SM} aria-hidden />
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium text-nb-300" title={entity.name}>{entity.name}</div>
        <div className={clsx('truncate text-[11px]', healthy || entity.off ? 'text-nb-500' : GLYPH[entity.status].tone)}>
          {healthy || entity.off ? entity.detail : PLATFORM_STATUS_WORD[entity.status]}
        </div>
      </div>
      <StatusGlyph status={entity.status} size={ICON_SM + 2} />
    </button>
  )
}

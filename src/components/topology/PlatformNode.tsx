import { useStore, type NodeProps } from '@xyflow/react'
import clsx from 'clsx'
import { Antenna, Bot, CircleCheck, CircleHelp, CircleX, Database, Merge, TriangleAlert, Waypoints, type LucideIcon } from 'lucide-react'
import { memo } from 'react'
import { AllHandles, FAR_ZOOM } from '@/components/topology/nodes'
import { ICON_SM } from '@/components/ui/primitives'
import { PLATFORM_STATUS_WORD, type PlatformKind, type PlatformStatus } from '@/lib/platformLayer'
import type { PlatformNode as PlatformNodeType } from '@/lib/platformLayerGraph'

export const PLATFORM_ICON: Record<PlatformKind, LucideIcon> = { agent: Bot, local: Antenna, regional: Waypoints, central: Merge, fusion: Database }

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
 * A part of the telemetry platform on the canvas, in the same node language as a service card: a bordered box with an icon tile, a
 * name and a line under it. The dashed border says it belongs to the platform, like the dashed lines that join these nodes. The state
 * is its glyph on the right; anything but Healthy is also written under the name.
 */
export const PlatformNode = memo(function PlatformNode({ data, selected }: NodeProps<PlatformNodeType>) {
  const far = useStore((s) => s.transform[2] < FAR_ZOOM)
  const Icon = PLATFORM_ICON[data.platform]
  return (
    <div
      data-testid="platform-node"
      data-platform={data.platform}
      className={clsx(
        'flex h-full w-full items-center gap-2.5 rounded-xl border bg-nb-925 px-3 transition-colors',
        selected ? 'border-accent shadow-[inset_0_0_0_1px_var(--color-accent)]' : 'border-dashed border-nb-700 hover:border-nb-600',
      )}
    >
      <AllHandles />
      <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-nb-900 text-nb-400">
        <Icon size={ICON_SM} aria-hidden />
      </div>
      <div className="min-w-0 flex-1">
        <div className={clsx('truncate font-medium text-nb-300', far ? 'text-[15px]' : 'text-[13px]')} title={data.title}>{data.title}</div>
        {!far && (
          <div className={clsx('truncate text-[11px]', data.status === 'healthy' ? 'text-nb-500' : GLYPH[data.status].tone)} title={data.subtitle}>
            {data.status === 'healthy' ? data.subtitle : PLATFORM_STATUS_WORD[data.status]}
          </div>
        )}
      </div>
      <StatusGlyph status={data.status} size={far ? 18 : ICON_SM + 2} />
    </div>
  )
})

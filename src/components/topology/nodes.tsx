import { Handle, useStore, type NodeProps } from '@xyflow/react'
import clsx from 'clsx'
import { ArrowUpRight, Box, Camera, Cog, Cpu, Factory, Gauge, Globe, HardDrive, Radio, Router, Server, Tag, Truck, type LucideIcon } from 'lucide-react'
import { memo } from 'react'
import { LoadRow, peakLoad } from '@/components/topology/Load'
import { DistroIcon, Flag } from '@/components/ui/brand'
import { SIDES, type CardNode, type GroupNode } from '@/lib/graph'
import { STATUS_COLOR, TIER_COLOR, type DeviceKind } from '@/lib/types'

export const DEVICE_ICON: Record<DeviceKind, LucideIcon> = {
  sensor: Gauge,
  actuator: Cog,
  camera: Camera,
  plc: Factory,
  gateway: Router,
  tag: Tag,
  vehicle: Truck,
  other: Radio,
}

/** Invisible handles on all four sides so edges can attach wherever routing picks. */
function AllHandles() {
  return (
    <>
      {(Object.keys(SIDES) as (keyof typeof SIDES)[]).flatMap((side) => [
        <Handle key={`${side}-s`} id={`${side}-s`} type="source" position={SIDES[side]} isConnectable={false} />,
        <Handle key={`${side}-t`} id={`${side}-t`} type="target" position={SIDES[side]} isConnectable={false} />,
      ])}
    </>
  )
}

/**
 * Zoomed far out, small print is unreadable however it is drawn, so boxes keep only their name (drawn larger
 * so it stays legible) and any warning; the detail returns as soon as you zoom in. Selecting the store value
 * as a boolean means only a crossing of the threshold re-renders the nodes, not every step of a zoom.
 */
export const FAR_ZOOM = 0.62
const useFar = () => useStore((s) => s.transform[2] < FAR_ZOOM)

const TONE = { good: 'bg-emerald-400/10 text-emerald-300', warn: 'bg-amber-400/10 text-amber-300', bad: 'bg-red-400/10 text-red-300' } as const
const MESH_TONE = { in: 'bg-emerald-400/10 text-emerald-300', control: 'bg-violet-400/10 text-violet-300', out: 'bg-nb-900 text-nb-400' } as const

/* ---------- Cluster / tier boundary ---------- */
export const GroupBox = memo(function GroupBox({ data, selected }: NodeProps<GroupNode>) {
  const far = useFar()
  const color = TIER_COLOR[data.tier]
  const peak = data.load ? peakLoad(data.load) : undefined
  const tierLabel = data.extra === 'devices' ? 'Devices' : data.extra === 'external' ? 'External' : data.tier === 'far-edge' ? 'Far edge' : data.tier[0].toUpperCase() + data.tier.slice(1)
  return (
    <div
      className={clsx('h-full w-full rounded-2xl border transition-shadow', selected && 'shadow-[0_0_0_2px_var(--color-accent)]')}
      style={{
        borderColor: `color-mix(in srgb, ${color} ${selected ? 70 : 32}%, transparent)`,
        background: `color-mix(in srgb, ${color} 5%, var(--color-nb-920))`,
      }}
    >
      <AllHandles />
      <div className="flex items-start justify-between gap-3 px-5 pt-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="size-2 shrink-0 rounded-full" style={{ background: STATUS_COLOR[data.status] }} />
            {data.distribution && <DistroIcon distribution={data.distribution} size={16} />}
            <span className={clsx('truncate font-medium text-white', far ? 'text-[26px] leading-8' : 'text-sm')}>{data.title}</span>
            {far && peak !== undefined && peak >= 70 && (
              <span className={clsx('rounded px-1.5 py-0.5 text-[15px] font-medium', peak >= 90 ? 'bg-red-400/15 text-red-300' : 'bg-amber-400/15 text-amber-300')} title="Busiest resource: share requested by pods">{peak}%</span>
            )}
          </div>
          {!far && (
            <div className="mt-0.5 flex items-center gap-1.5 truncate pl-4 text-xs text-nb-500">
              {data.country && <Flag code={data.country} className="!h-2.5 !w-[15px]" />}
              <span className="truncate">{data.subtitle || ' '}</span>
              {data.mesh && (
                <span className={clsx('shrink-0 rounded px-1.5 py-px text-[10.5px]', TONE[data.mesh.tone])} title={data.mesh.title} data-testid="mesh-badge">
                  {data.mesh.label}
                </span>
              )}
            </div>
          )}
          {!far && data.load && (data.load.cpuPct !== undefined || data.load.memPct !== undefined || data.load.podPct !== undefined || data.load.ready < data.load.nodes || data.load.unready > 0) && (
            <LoadRow load={data.load} className="mt-1 pl-4" />
          )}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <span
            className="rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide"
            style={{ color, borderColor: `color-mix(in srgb, ${color} 35%, transparent)` }}
          >
            {tierLabel}
          </span>
          {!far && <span className="text-[11px] text-nb-500">{data.stats}</span>}
        </div>
      </div>
    </div>
  )
})

/* ---------- Service / machine card ---------- */
const MACHINE_ICON = { vm: Server, 'bare-metal': HardDrive, 'edge-device': Cpu } as const

export const Card = memo(function Card({ data, selected }: NodeProps<CardNode>) {
  const isMachine = data.kind === 'machine'
  const Icon =
    data.kind === 'device' ? DEVICE_ICON[data.deviceKind ?? 'other'] : data.kind === 'external' ? Globe : isMachine ? MACHINE_ICON[data.machineKind ?? 'vm'] : Box
  const far = useFar()
  const color = TIER_COLOR[data.tier]
  return (
    <div
      data-far={far ? '1' : undefined}
      className={clsx(
        'flex h-full w-full flex-col justify-center gap-2 rounded-xl border bg-nb-925 px-3.5 py-2.5 transition-colors',
        selected ? 'border-accent shadow-[0_0_0_1px_var(--color-accent)]' : 'border-nb-800 hover:border-nb-700',
      )}
    >
      <AllHandles />
      <div className="flex items-center gap-3">
        <div
          className="grid size-9 shrink-0 place-items-center rounded-lg"
          style={{ background: `color-mix(in srgb, ${color} 14%, transparent)`, color }}
        >
          <Icon size={17} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className={clsx('truncate font-medium text-white', far ? 'text-[21px]' : 'text-[13px]')}>{data.title}</span>
          </div>
          {!far && <div className="truncate text-[11px] text-nb-500">{data.subtitle}</div>}
          {!far && isMachine && <div className="truncate text-[11px] text-nb-500">{data.meta}</div>}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          <span className={clsx('rounded-full', far ? 'size-3' : 'size-2')} style={{ background: STATUS_COLOR[data.status] }} title={data.status} />
          {!far && !isMachine && <span className="text-[11px] text-nb-500">{data.meta}</span>}
          {far && (data.hint || data.notReady) && <span className={clsx('size-2.5 rounded-sm', data.notReady ? 'bg-amber-400' : 'bg-accent')} title={data.notReady ?? `Better in ${data.hint}`} />}
        </div>
      </div>
      {!far && (data.hint || data.notReady || data.mesh) && (
        <div className="flex flex-wrap items-center gap-1.5 text-[10.5px]">
          {data.mesh && (
            <span className={clsx('inline-flex max-w-full items-center truncate rounded px-1.5 py-0.5', MESH_TONE[data.mesh.tone])} title={data.mesh.title} data-testid="mesh-chip">
              <span className="truncate">{data.mesh.label}</span>
            </span>
          )}
          {data.notReady && <span className="rounded bg-amber-400/10 px-1.5 py-0.5 text-amber-300" title="Fewer replicas are ready than wanted">{data.notReady}</span>}
          {data.hint && (
            <span className="inline-flex max-w-full items-center gap-1 truncate rounded bg-accent-soft px-1.5 py-0.5 text-accent" title={`The placement advice would move this to ${data.hint}. Open it for the evidence.`} data-testid="placement-hint">
              <ArrowUpRight size={10} className="shrink-0" aria-hidden />
              <span className="truncate">better in {data.hint}</span>
            </span>
          )}
        </div>
      )}

      {!far && data.chips && (
        <div className="flex flex-wrap gap-1.5 border-t border-nb-850 pt-2">
          {data.chips.length === 0 && <span className="text-[11px] text-nb-500">No services</span>}
          {data.chips.map((c) => (
            <span
              key={c.id}
              className="inline-flex max-w-[48%] items-center gap-1 truncate rounded bg-nb-940 px-1.5 py-0.5 text-[10.5px] text-nb-400"
              title={c.name}
            >
              <Box size={10} className="shrink-0" />
              <span className="truncate">{c.name}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  )
})

export const nodeTypes = { boundary: GroupBox, card: Card }

import { Handle, useStore, type NodeProps } from '@xyflow/react'
import clsx from 'clsx'
import {
  Antenna,
  ArrowUpRight,
  BatteryCharging,
  Box,
  Cable,
  Camera,
  Clock,
  Cog,
  Cpu,
  Database,
  Factory,
  Gauge,
  Globe,
  HardDrive,
  Layers,
  Radio,
  Router,
  Server,
  Shield,
  Tag,
  Truck,
  type LucideIcon,
} from 'lucide-react'
import { ICON_MD, ICON_SM } from '@/components/ui/primitives'
import { memo, type ComponentProps, type ReactNode } from 'react'
import { LoadRow, peakLoad } from '@/components/topology/Load'
import { DistroIcon, Flag } from '@/components/ui/brand'
import { SIDES, type CardNode, type GroupNode, type NamespaceNode } from '@/lib/graph'
import { STATUS_COLOR, TIER_COLOR, type DeviceKind, type ServiceKind } from '@/lib/types'

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

const TONE = { good: 'bg-ok/10 text-ok', warn: 'bg-warn/10 text-warn', bad: 'bg-bad/10 text-bad' } as const
const MESH_TONE = { in: 'bg-ok/10 text-ok', control: 'bg-violet-400/10 text-violet-300', out: 'bg-nb-900 text-nb-400' } as const
// The shared "neutral gray, no particular status" tone - used by both the networking badge and a card's
// service chips, which previously used two adjacent-but-different grays (bg-nb-900 vs bg-nb-940) that never
// meant anything different from each other, just drifted independently.
const NEUTRAL_TONE = 'bg-nb-900 text-nb-400'

/** "1000" reads worse on a small badge than "1 Gbps" - the Inspector's own full interface list (Inspector.tsx)
 *  keeps raw Mbps since it has room to be exact about every interface; this is a single at-a-glance number
 *  for the fastest one, so the friendlier unit wins here. */
function nicSpeedLabel(mbps: number) {
  return mbps >= 1000 ? `${Number((mbps / 1000).toFixed(1))} Gbps` : `${mbps} Mbps`
}

/** One shared shape for every small text pill on a node (a cluster's mesh state, its detected networking,
 * a service's "not ready" or placement-hint chip, a service-count chip) - these had each grown their own
 * near-identical rounded/padding/icon/truncate markup, with small unintentional drift between them (radius,
 * padding, whether an icon was included, two different neutral grays) rather than genuine differences.
 * `tone` takes the caller's own pre-existing background+text color classes (TONE/MESH_TONE/NEUTRAL_TONE
 * above, or a literal pair) rather than one shared enum, since these badges span at least two genuinely
 * different semantic axes (a cluster's mesh mTLS verdict vs. a service's mesh membership state) that
 * shouldn't be forced into one vocabulary just because they're drawn the same way. `dense` is the one real,
 * deliberate size difference this keeps: the cluster subtitle row (mesh/networking badges) is tighter on
 * vertical space than a card's own dedicated chip row, so it keeps the thinner py-px padding that row
 * already had - an intentional density difference for a genuinely tighter row, not a return of the
 * inconsistency this is meant to remove.
 *
 * Deliberately NOT folded in here: the tier badge (rounded-full, a dynamic per-tier color, not one of the
 * fixed tones above) and the local-telemetry control (a circular icon button). Both are a different kind of
 * thing from an info tag - a status/category classifier and an interactive control - and forcing them into
 * this shape would blur that distinction rather than fix it. The peak-load badge (far-zoom only) is also
 * left alone: it's deliberately sized to match that zoom level's much larger title text, not this family's
 * compact scale. */
function Badge({
  tone,
  dense,
  icon: Icon,
  title,
  className,
  children,
  ...rest
}: {
  tone: string
  dense?: boolean
  icon?: LucideIcon
  title?: string
  className?: string
  children: ReactNode
} & ComponentProps<'span'>) {
  return (
    <span
      className={clsx('inline-flex shrink-0 items-center gap-1 truncate rounded px-1.5 text-[10.5px]', dense ? 'py-px' : 'py-0.5', tone, className)}
      title={title}
      {...rest}
    >
      {Icon && <Icon size={ICON_SM} className="shrink-0" aria-hidden="true" />}
      <span className="truncate">{children}</span>
    </span>
  )
}

/* ---------- Cluster / tier boundary ---------- */
export const GroupBox = memo(function GroupBox({ data, selected }: NodeProps<GroupNode>) {
  const far = useFar()
  const color = TIER_COLOR[data.tier]
  const peak = data.load ? peakLoad(data.load) : undefined
  const tierLabel =
    data.extra === 'devices'
      ? 'Devices'
      : data.extra === 'external'
        ? 'External'
        : data.extra === 'operators'
          ? 'Regional operator'
          : data.tier === 'far-edge'
            ? 'Far edge'
            : data.tier[0].toUpperCase() + data.tier.slice(1)
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
            {data.distribution && <DistroIcon distribution={data.distribution} size={ICON_SM} />}
            {data.extra === 'operators' && <Antenna size={ICON_SM} className="shrink-0 text-nb-500" />}
            <span className={clsx('truncate font-medium text-nb-300', far ? 'text-[26px] leading-8' : 'text-sm')} title={data.title}>{data.title}</span>
            {far && peak !== undefined && peak >= 70 && (
              <span className={clsx('rounded px-1.5 py-0.5 text-[15px] font-medium', peak >= 90 ? 'bg-bad/15 text-bad' : 'bg-warn/15 text-warn')} title="Busiest resource: share requested by pods">{peak}%</span>
            )}
          </div>
          {!far && (
            <div className="mt-0.5 flex items-center gap-1.5 truncate pl-4 text-xs text-nb-500">
              {data.country && <Flag code={data.country} className="!h-2.5 !w-[15px]" />}
              <span className="truncate" title={data.subtitle || undefined}>{data.subtitle || ' '}</span>
              {data.mesh && (
                <Badge tone={TONE[data.mesh.tone]} dense title={data.mesh.title} data-testid="mesh-badge">
                  {data.mesh.label}
                </Badge>
              )}
              {data.networking && (
                <Badge
                  tone={NEUTRAL_TONE}
                  dense
                  icon={Router}
                  title={`Detected in this cluster: ${[data.networking.cni && `${data.networking.cni} (CNI)`, data.networking.ingress && `${data.networking.ingress} (ingress)`].filter(Boolean).join(' · ')}`}
                  data-testid="networking-badge"
                >
                  {[data.networking.cni, data.networking.ingress].filter(Boolean).join(' · ')}
                </Badge>
              )}
            </div>
          )}
          {!far && data.load && (data.load.cpuPct !== undefined || data.load.memPct !== undefined || data.load.podPct !== undefined || data.load.ready < data.load.nodes || data.load.unready > 0) && (
            <LoadRow load={data.load} className="mt-1 pl-4" />
          )}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <div className="flex items-center gap-1">
            {data.localTelemetry && (
              <button
                type="button"
                data-testid="local-telemetry-badge"
                data-local-telemetry-cluster={data.entityId}
                title={`Local telemetry running: ${data.localTelemetry.layers.join(', ') || 'signals active'}. Click to configure.`}
                aria-label={`Local telemetry running: ${data.localTelemetry.layers.join(', ') || 'signals active'}. Click to configure.`}
                className="grid size-5 shrink-0 place-items-center rounded-full bg-accent-soft text-accent transition-colors hover:bg-accent/20"
              >
                <Antenna size={ICON_MD} />
              </button>
            )}
            <span
              // Background fill added per this round's visual review: every other badge in this header
              // (mesh, networking, the status dot) reads as "a label" because it has a tinted fill behind
              // it - this one was border-only, so at default zoom it nearly disappeared against the box's
              // own faint tier-tinted background. Same color-mix approach as the border, just lower opacity.
              className="rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide"
              style={{
                color,
                borderColor: `color-mix(in srgb, ${color} 35%, transparent)`,
                background: `color-mix(in srgb, ${color} 14%, transparent)`,
              }}
            >
              {tierLabel}
            </span>
          </div>
          {!far && <span className="text-[11px] text-nb-500">{data.stats}</span>}
        </div>
      </div>
    </div>
  )
})

/* ---------- Namespace sub-box (nests inside a cluster box, one level in from it) ---------- */
export const NamespaceBox = memo(function NamespaceBox({ data }: NodeProps<NamespaceNode>) {
  return (
    <div className="h-full w-full rounded-lg border border-dashed border-nb-800 bg-black/10">
      <AllHandles />
      <div className="flex items-center gap-1.5 px-3 pt-2">
        <span className="size-1.5 shrink-0 rounded-full" style={{ background: STATUS_COLOR[data.status] }} />
        <span className="truncate text-[11px] font-medium uppercase tracking-wide text-nb-500">{data.namespace}</span>
        <span className="ml-auto shrink-0 text-[10.5px] normal-case tracking-normal text-nb-600">{data.count}</span>
      </div>
    </div>
  )
})

/* ---------- Service / machine card ---------- */
// A card's own footprint stays legible how ever many services a busy node/namespace box has - past this
// many chips, the rest collapse into one "+N more" chip (its title lists every one of them) instead of the
// card growing to fit an unbounded list, mirroring how a crowded Grafana panel legend collapses long series.
const CHIP_LIMIT = 6
const MACHINE_ICON = { vm: Server, 'bare-metal': HardDrive, 'edge-device': Cpu } as const
// A bare Service with no controller behind it (or one whose kind isn't one of these four) keeps the
// generic Box - same as before this map existed.
export const WORKLOAD_ICON: Record<ServiceKind, LucideIcon> = {
  Deployment: Layers,
  StatefulSet: Database,
  DaemonSet: Shield,
  Job: Clock,
}

export const Card = memo(function Card({ data, selected }: NodeProps<CardNode>) {
  const isMachine = data.kind === 'machine'
  const Icon =
    data.kind === 'device'
      ? DEVICE_ICON[data.deviceKind ?? 'other']
      : data.kind === 'external'
        ? Globe
        : isMachine
          ? MACHINE_ICON[data.machineKind ?? 'vm']
          : data.serviceKind
            ? WORKLOAD_ICON[data.serviceKind]
            : Box
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
          <Icon size={ICON_SM} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            {/* title= added per this round's visual review: several common service/device names (e.g.
                "inference-regional", "Temperature sensors") truncate at this card's fixed width, and there
                was no way to see the full name short of opening the Inspector - a native tooltip on hover
                costs nothing and needs no layout change. */}
            <span className={clsx('truncate font-medium text-nb-300', far ? 'text-[21px]' : 'text-[13px]')} title={data.title}>{data.title}</span>
          </div>
          {!far && <div className="truncate text-[11px] text-nb-500" title={data.subtitle}>{data.subtitle}</div>}
          {!far && isMachine && <div className="truncate text-[11px] text-nb-500" title={data.meta}>{data.meta}</div>}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          <span className={clsx('rounded-full', far ? 'size-3' : 'size-2')} style={{ background: STATUS_COLOR[data.status] }} title={data.status} />
          {!far && !isMachine && <span className="text-[11px] text-nb-500">{data.meta}</span>}
          {far && (data.hint || data.notReady) && <span className={clsx('size-2.5 rounded-sm', data.notReady ? 'bg-warn' : 'bg-accent')} title={data.notReady ?? `Better in ${data.hint}`} />}
        </div>
      </div>
      {!far && (data.hint || data.notReady || data.mesh || data.hardware) && (
        <div className="flex flex-wrap items-center gap-1.5 text-[10.5px]">
          {data.mesh && (
            <Badge tone={MESH_TONE[data.mesh.tone]} title={data.mesh.title} data-testid="mesh-chip" className="max-w-full">
              {data.mesh.label}
            </Badge>
          )}
          {data.notReady && <Badge tone="bg-warn/10 text-warn" title="Fewer replicas are ready than wanted">{data.notReady}</Badge>}
          {data.hint && (
            <Badge tone="bg-accent-soft text-accent" icon={ArrowUpRight} title={`The placement advice would move this to ${data.hint}. Open it for the evidence.`} data-testid="placement-hint" className="max-w-full">
              better in {data.hint}
            </Badge>
          )}
          {data.hardware?.hasBattery && (
            <Badge tone={NEUTRAL_TONE} icon={BatteryCharging} title="Node probe: this machine can run without mains power (has a battery)" data-testid="battery-badge">
              Battery
            </Badge>
          )}
          {data.hardware?.nicMbps !== undefined && (
            <Badge tone={NEUTRAL_TONE} icon={Cable} title={`Node probe: fastest physical network interface seen on this machine is ${nicSpeedLabel(data.hardware.nicMbps)}`} data-testid="nic-badge">
              {nicSpeedLabel(data.hardware.nicMbps)}
            </Badge>
          )}
        </div>
      )}

      {!far && data.pods && data.pods.length > 0 && (
        <div className="flex flex-wrap items-center gap-1" data-testid="pod-dots">
          {data.pods.map((p) => (
            <span
              key={p.id}
              className={clsx(
                // Ready uses the same green as the card's own status dot above it (STATUS_COLOR.healthy) -
                // not a plain neutral gray - so "these replicas are fine" reads as the same colour language
                // in both places. The ring is `info` (not `accent`, already overloaded for selection and
                // placement hints elsewhere on this card) so a selected card with a freshly-scaled pod
                // doesn't show one colour meaning two unrelated things at once.
                'size-2 rounded-full',
                p.ready ? 'bg-ok' : 'bg-warn',
                p.recent && 'ring-2 ring-info/70 ring-offset-1 ring-offset-nb-925',
              )}
              title={`${p.id}${p.ready ? '' : ' · not ready'}${p.recent ? ' · recently added (scaling)' : ''}`}
            />
          ))}
          {!!data.podsOverflow && (
            <span
              className="rounded-full bg-nb-900 px-1 text-[9px] leading-[14px] text-nb-400"
              title={`${data.podsOverflow} more pod${data.podsOverflow === 1 ? '' : 's'} not shown`}
            >
              +{data.podsOverflow}
            </span>
          )}
        </div>
      )}

      {!far && data.chips && (
        <div className="flex flex-wrap gap-1.5 border-t border-nb-850 pt-2">
          {data.chips.length === 0 && <span className="text-[11px] text-nb-500">No services</span>}
          {data.chips.slice(0, CHIP_LIMIT).map((c) => (
            <Badge key={c.id} tone={NEUTRAL_TONE} icon={Box} title={c.name} className="max-w-[48%]">
              {c.name}
            </Badge>
          ))}
          {data.chips.length > CHIP_LIMIT && (
            <Badge
              tone={NEUTRAL_TONE}
              title={data.chips.slice(CHIP_LIMIT).map((c) => c.name).join(', ')}
              data-testid="chip-overflow"
            >
              +{data.chips.length - CHIP_LIMIT} more
            </Badge>
          )}
        </div>
      )}
    </div>
  )
})

export const nodeTypes = { boundary: GroupBox, card: Card, namespace: NamespaceBox }

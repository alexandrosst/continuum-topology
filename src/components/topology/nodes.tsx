import { Handle, useStore, type NodeProps } from '@xyflow/react'
import clsx from 'clsx'
import {
  Funnel,
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
  Network,
  Radio,
  Router,
  Server,
  Shield,
  Tag,
  Truck,
  type LucideIcon,
} from 'lucide-react'
import { ICON_MD, ICON_SM, TIER_ICON } from '@/components/ui/primitives'
import { createElement, memo, useCallback, useContext, useEffect, useState, type ComponentProps, type ReactNode } from 'react'
import { PodPopover, PodRail } from '@/components/topology/Pods'
import { LoadRow } from '@/components/topology/Load'
import { peakLoad } from '@/lib/metrics'
import { DistroIcon, Flag } from '@/components/ui/brand'
import { middleTruncate } from '@/lib/present'
import { SIDES, type CardData, type CardNode, type GroupData, type GroupNode, type NamespaceNode } from '@/lib/graph'
import { DetailContext, shownStatus, type Alert } from '@/lib/detail'
import { activeId, useCanvasFocus } from '@/store/canvasFocus'
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
export function AllHandles() {
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

/** Keeps the canvas's zoom where CSS can read it (`--zoom`), so the few things that must stay readable however far out the canvas is drawn (what is
 *  wrong, which network) are sized in screen pixels by the `fine` class. It draws nothing; render it inside the canvas. */
export function ZoomVar() {
  const zoom = useStore((s) => s.transform[2])
  const dom = useStore((s) => s.domNode)
  useEffect(() => { dom?.style.setProperty('--zoom', String(zoom)) }, [dom, zoom])
  return null
}

/** The state colours of a problem card or cluster: the same two the status dot and the rest of the app use. */
const ALERT_COLOR: Record<Alert, string> = { warn: STATUS_COLOR.degraded, bad: STATUS_COLOR.offline }
/** What a problem's words are drawn in: the theme's own text steps (amber and red that hold 4.5:1 on a light surface as well as a dark one), not the
 *  vivid fills the tint, the border and the dot use. */
const ALERT_TEXT: Record<Alert, string> = { warn: 'text-warn', bad: 'text-bad' }
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
function FullGroupBox({ data, selected }: NodeProps<GroupNode>) {
  const far = useFar()
  const color = TIER_COLOR[data.tier]
  const peak = data.load ? peakLoad(data.load) : undefined
  const tierLabel =
    data.extra === 'devices'
      ? 'Devices'
      : data.extra === 'external'
        ? 'External'
        : data.tier === 'far-edge'
          ? 'Far edge'
          : data.tier[0].toUpperCase() + data.tier.slice(1)
  // Devices/External are synthetic grouping rows (always laid out as tier: 'cloud', see graph.ts), not a
  // real tier - no tier icon for those, same as their label above already isn't a tier name.
  const TierGlyph = data.extra ? undefined : TIER_ICON[data.tier]
  return (
    // The selection ring is `inset`, not the plain outward `0_0_0_Npx` box-shadow it used to be: OffsetEdge
    // places an incoming edge's arrowhead tip with zero gap exactly on this box's true boundary (see its
    // own pullBackEnds doc comment for why), and an outward ring bleeds a couple of px past that same
    // boundary on top of it - painted after the edge, since every node sits above the edges' own SVG layer
    // regardless of z-index. The visible result was a selected card's own ring appearing to swallow the
    // very tip of any edge pointing at it. Inset keeps the identical highlight look without ever drawing
    // outside the box OffsetEdge's own math already treats as this card's exact, true extent.
    <div
      className={clsx('h-full w-full rounded-2xl border transition-shadow', selected && 'shadow-[inset_0_0_0_2px_var(--color-accent)]')}
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
                <Funnel size={ICON_MD} />
              </button>
            )}
            <span
              // Background fill added per this round's visual review: every other badge in this header
              // (mesh, networking, the status dot) reads as "a label" because it has a tinted fill behind
              // it - this one was border-only, so at default zoom it nearly disappeared against the box's
              // own faint tier-tinted background. Same color-mix approach as the border, just lower opacity.
              className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide"
              style={{
                color,
                borderColor: `color-mix(in srgb, ${color} 35%, transparent)`,
                background: `color-mix(in srgb, ${color} 14%, transparent)`,
              }}
            >
              {TierGlyph && <TierGlyph size={ICON_SM - 2} aria-hidden="true" />}
              {tierLabel}
            </span>
          </div>
          {!far && <span className="text-[11px] text-nb-500">{data.stats}</span>}
        </div>
      </div>
    </div>
  )
}

/** The glyph at a box's right edge, in place of a text chip: the box's tint already says the tier, this says what kind of thing the box is. */
const BOX_KIND = { devices: { Glyph: Radio, label: 'Devices' }, external: { Glyph: Globe, label: 'External endpoints' } } as const

const NETWORK_CHIPS = 2

/** The networks a box sits on, as quiet chips: a fact about the box, not a line to another one. Hovering or focusing a chip lights every box on that
 *  network (see CalmGroupBox's ring); the Inspector says the same in words. Past two networks the rest fold into "+N", named in the tooltip. */
function NetworkChips({ networks }: { networks: NonNullable<GroupData['networks']> }) {
  const lit = useCanvasFocus((s) => s.network)
  const setNetwork = useCanvasFocus((s) => s.setNetwork)
  const shown = networks.slice(0, NETWORK_CHIPS)
  const more = networks.slice(NETWORK_CHIPS)
  return (
    <div className="mt-1.5 flex items-center gap-1.5 pl-4" data-testid="network-chips">
      {shown.map((n) => (
        <button
          key={n.id}
          type="button"
          title={n.text}
          aria-label={n.text}
          data-testid="network-chip"
          onPointerEnter={() => setNetwork(n.id)}
          onPointerLeave={() => setNetwork(null)}
          onFocus={() => setNetwork(n.id)}
          onBlur={() => setNetwork(null)}
          className={clsx(
            'nodrag nopan inline-flex min-w-0 max-w-[9.5rem] items-center gap-1 rounded px-1.5 py-px text-[10.5px] text-info transition-colors',
            lit === n.id ? 'bg-info/20' : 'bg-info/10 hover:bg-info/15',
          )}
        >
          {n.kind === 'overlay' ? <Cable size={ICON_SM} className="shrink-0" aria-hidden="true" /> : <Network size={ICON_SM} className="shrink-0" aria-hidden="true" />}
          <span className="truncate">{n.via}</span>
        </button>
      ))}
      {more.length > 0 && (
        <span className="text-[10.5px] text-nb-500" title={more.map((n) => n.text).join('\n')}>+{more.length}</span>
      )}
    </div>
  )
}

/** What a box on a network shows when its chips are not drawn (zoomed out): the network's glyph, as large on screen as a line of text. */
function NetworkPip({ networks }: { networks: NonNullable<GroupData['networks']> }) {
  const text = networks.map((n) => n.text).join('. ')
  const Glyph = networks[0].kind === 'overlay' ? Cable : Network
  return (
    <span role="img" aria-label={text} title={text} data-testid="network-pip" className="fine inline-flex shrink-0 items-center rounded bg-info/10 px-1 text-info">
      <Glyph className="size-[1.1em]" aria-hidden="true" />
    </span>
  )
}

/** The networks lit by what the pointer or the selection is on: a hovered chip's network, else the networks the active box is a member of. `peer` is
 *  whether this box is one of the others on it (the active box itself is named, not ringed). */
function litNetworks(s: Parameters<typeof activeId>[0] & { network: string | null }, id: string, networks: GroupData['networks']) {
  const a = activeId(s)
  const nets = s.network ? networks?.filter((n) => n.id === s.network) : a ? networks?.filter((n) => n.members.includes(a)) : undefined
  return { nets: nets ?? [], peer: !!nets?.length && (!!s.network || a !== id) }
}

/** Calm: the header is what identifies the box (its name, its logo, its status dot), where it is, and what is wrong with it. The tier is a
 *  small glyph with its name on hover; the distribution, counts, load bars and telemetry control are the Inspector's. */
function CalmGroupBox({ id, data, selected }: NodeProps<GroupNode>) {
  const far = useFar()
  // Rings this box when it shares a network with whatever is hovered or selected, or when one of its own network chips is lit, and names that
  // network on the box so a ring is never a mystery (the box that is hovered itself is named too, but not ringed).
  const ringed = useCanvasFocus((s) => litNetworks(s, id, data.networks).peer)
  const ringName = useCanvasFocus((s) => litNetworks(s, id, data.networks).nets.map((n) => `${n.via} · shared by ${n.members.length}`).join(', '))
  const color = TIER_COLOR[data.tier]
  const peak = data.load ? peakLoad(data.load) : undefined
  const alert = data.alert
  const shown = shownStatus(data.status, alert)
  const kind = data.extra ? BOX_KIND[data.extra] : undefined
  const Glyph = kind?.Glyph ?? TIER_ICON[data.tier]
  const kindLabel = kind?.label ?? `${data.tier === 'far-edge' ? 'Far edge' : data.tier[0].toUpperCase() + data.tier.slice(1)} tier`
  return (
    // Inset ring: see the note in FullGroupBox.
    <div
      className={clsx(
        'group/box relative h-full w-full rounded-2xl border transition-shadow',
        selected ? 'shadow-[inset_0_0_0_2px_var(--color-nb-100)]' : ringed && 'shadow-[inset_0_0_0_2px_var(--color-info)]',
      )}
      style={{
        // The network ring is the one blue on the canvas whatever the box's own tier or alert colour, so it reads the same on every box.
        borderColor: ringed && !selected ? 'var(--color-info)' : alert && !selected ? `color-mix(in srgb, ${ALERT_COLOR[alert]} 55%, transparent)` : `color-mix(in srgb, ${color} ${selected ? 70 : 32}%, transparent)`,
        background: `color-mix(in srgb, ${color} 5%, var(--color-nb-920))`,
      }}
    >
      {ringName && (
        <span className="fine pointer-events-none absolute left-5 top-full z-10 flex -translate-y-1/2 items-center gap-1 whitespace-nowrap rounded-full border border-info bg-nb-900 px-2 py-px text-info" data-testid="network-ring-name">
          <Network className="size-[1.1em]" aria-hidden="true" />{ringName}
        </span>
      )}
      <AllHandles />
      <div className="flex items-start justify-between gap-3 px-5 pt-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className={clsx('shrink-0 rounded-full', far ? 'size-3' : 'size-2')} style={{ background: shown.color }} title={shown.word} />
            {data.distribution && <DistroIcon distribution={data.distribution} size={ICON_SM} />}
            <span className={clsx('truncate font-medium text-nb-300', far ? 'text-[26px] leading-8' : 'text-sm')} title={data.title}>{data.title}</span>
            {far && data.networks && <NetworkPip networks={data.networks} />}
            {far && peak !== undefined && peak >= 70 && (
              <span className={clsx('rounded px-1.5 py-0.5 text-[15px] font-medium', peak >= 90 ? 'bg-bad/15 text-bad' : 'bg-warn/15 text-warn')} title="Busiest resource: share requested by pods">{peak}%</span>
            )}
          </div>
          {!far && data.place && (
            <div className="mt-0.5 flex items-center gap-1.5 pl-4 text-xs text-nb-500">
              {data.country && <Flag code={data.country} className="!h-2.5 !w-[15px]" />}
              <span className="truncate" title={data.place}>{data.place}</span>
              {data.mesh && (
                <Badge tone={TONE[data.mesh.tone]} dense title={data.mesh.title} data-testid="mesh-badge">
                  {data.mesh.label}
                </Badge>
              )}
            </div>
          )}
          {alert && data.note && (
            <div className={clsx('fine mt-0.5 truncate pl-4', ALERT_TEXT[alert])} title={data.note} data-testid="box-note">{data.note}</div>
          )}
          {!far && data.networks && <NetworkChips networks={data.networks} />}
        </div>
        <span
          role="img"
          aria-label={kindLabel}
          title={kindLabel}
          className="grid size-6 shrink-0 place-items-center rounded-full"
          style={{ color, background: `color-mix(in srgb, ${color} 14%, transparent)` }}
        >
          <Glyph size={ICON_SM} aria-hidden="true" />
        </span>
      </div>
    </div>
  )
}

export const GroupBox = memo(function GroupBox(props: NodeProps<GroupNode>) {
  return useContext(DetailContext) === 'calm' ? <CalmGroupBox {...props} /> : <FullGroupBox {...props} />
})

/* ---------- Namespace sub-box (nests inside a cluster box, one level in from it) ---------- */
export const NamespaceBox = memo(function NamespaceBox({ data }: NodeProps<NamespaceNode>) {
  // Calm leaves the count out: the cards inside are right there to count.
  const calm = useContext(DetailContext) === 'calm'
  return (
    <div className="h-full w-full rounded-lg border border-dashed border-nb-800 bg-black/10">
      <AllHandles />
      <div className="flex items-center gap-1.5 px-3 pt-2">
        <span className="size-1.5 shrink-0 rounded-full" style={{ background: STATUS_COLOR[data.status] }} />
        <span className="truncate text-[11px] font-medium uppercase tracking-wide text-nb-500">{data.namespace}</span>
        {!calm && <span className="ml-auto shrink-0 text-[10.5px] normal-case tracking-normal text-nb-600">{data.count}</span>}
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

/** The icon a card wears: what kind of thing it is. */
function iconOf(data: CardData): LucideIcon {
  return data.kind === 'device'
    ? DEVICE_ICON[data.deviceKind ?? 'other']
    : data.kind === 'external'
      ? Globe
      : data.kind === 'machine'
        ? MACHINE_ICON[data.machineKind ?? 'vm']
        : data.serviceKind
          ? WORKLOAD_ICON[data.serviceKind]
          : Box
}

/** The small chips under a card's name: mesh state, replicas not ready, placement advice, hardware facts. Calm keeps the
 *  two that relate the card to something else (the mesh, a better home); not-ready replicas are its note and the hardware is the Inspector's. */
function BadgeRow({ data, calm }: { data: CardData; calm?: boolean }) {
  const notReady = !calm && data.notReady
  const hardware = !calm && data.hardware
  if (!(data.hint || notReady || data.mesh || hardware)) return null
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-[10.5px]">
      {data.mesh && (
        <Badge tone={MESH_TONE[data.mesh.tone]} title={data.mesh.title} data-testid="mesh-chip" className="max-w-full">
          {data.mesh.label}
        </Badge>
      )}
      {notReady && <Badge tone="bg-warn/10 text-warn" title="Fewer replicas are ready than wanted">{notReady}</Badge>}
      {data.hint && (
        <Badge tone="bg-accent-soft text-accent" icon={ArrowUpRight} title={`The placement advice would move this to ${data.hint}. Open it for the evidence.`} data-testid="placement-hint" className="max-w-full">
          better in {data.hint}
        </Badge>
      )}
      {hardware && hardware.hasBattery && (
        <Badge tone={NEUTRAL_TONE} icon={BatteryCharging} title="Node probe: this machine can run without mains power (has a battery)" data-testid="battery-badge">
          Battery
        </Badge>
      )}
      {hardware && hardware.nicMbps !== undefined && (
        <Badge tone={NEUTRAL_TONE} icon={Cable} title={`Node probe: fastest physical network interface seen on this machine is ${nicSpeedLabel(hardware.nicMbps)}`} data-testid="nic-badge">
          {nicSpeedLabel(hardware.nicMbps)}
        </Badge>
      )}
    </div>
  )
}

/** The services a machine runs (the Infrastructure view's "Services on nodes"), capped at CHIP_LIMIT. */
function ChipRow({ chips }: { chips: NonNullable<CardData['chips']> }) {
  return (
    <div className="flex flex-wrap gap-1.5 border-t border-nb-850 pt-2">
      {chips.length === 0 && <span className="text-[11px] text-nb-500">No services</span>}
      {chips.slice(0, CHIP_LIMIT).map((c) => (
        <Badge key={c.id} tone={NEUTRAL_TONE} icon={Box} title={c.name} className="max-w-[48%]">
          {c.name}
        </Badge>
      ))}
      {chips.length > CHIP_LIMIT && (
        <Badge tone={NEUTRAL_TONE} title={chips.slice(CHIP_LIMIT).map((c) => c.name).join(', ')} data-testid="chip-overflow">
          +{chips.length - CHIP_LIMIT} more
        </Badge>
      )}
    </div>
  )
}

/** The pod rail and the popover it opens. The popover is local, transient UI state: nothing else on the canvas needs to know a card has it open. */
function usePodPopover() {
  const [open, setOpen] = useState(false)
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const close = useCallback(() => setOpen(false), [])
  return { open, anchor, setAnchor, close, toggle: () => setOpen((v) => !v) }
}

/** The card as it has always been: everything it knows, all the time (the "Full" detail). */
function FullCard({ data, selected }: NodeProps<CardNode>) {
  const isMachine = data.kind === 'machine'
  const pods = usePodPopover()
  const far = useFar()
  const color = TIER_COLOR[data.tier]
  return (
    <div
      data-far={far ? '1' : undefined}
      className={clsx(
        'flex h-full w-full flex-col justify-start gap-2 rounded-xl border bg-nb-925 px-3.5 pb-2.5 pt-4 transition-colors',
        // inset, not outward - see the identical note on the group/boundary box above: an outward ring
        // here would bleed past this card's own true boundary and sit on top of any edge arrowhead
        // pointing at it, since edges always render beneath every node regardless of z-index.
        selected ? 'border-accent shadow-[inset_0_0_0_1px_var(--color-accent)]' : 'border-nb-800 hover:border-nb-700',
      )}
    >
      <AllHandles />
      <div className="flex items-center gap-3">
        <div
          className="grid size-9 shrink-0 place-items-center rounded-lg"
          style={{ background: `color-mix(in srgb, ${color} 14%, transparent)`, color }}
        >
          {createElement(iconOf(data), { size: ICON_SM })}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            {/* title= added per this round's visual review: several common service/device names (e.g.
                "inference-regional", "Temperature sensors") truncate at this card's fixed width, and there
                was no way to see the full name short of opening the Inspector - a native tooltip on hover
                costs nothing and needs no layout change. */}
            {isMachine ? (
              // A machine name is often a long generated one whose start and end both matter ("ip-10-0-12-34.eu-west-1...internal"), so
              // the middle goes, not the end; the full name stays in the tooltip and for screen readers.
              <>
                <span className={clsx('truncate font-medium text-nb-300', far ? 'text-[21px] leading-tight' : 'text-[13px]')} title={data.title} aria-hidden="true">{middleTruncate(data.title, far ? 15 : 24)}</span>
                <span className="sr-only">{data.title}</span>
              </>
            ) : (
              <span className={clsx('truncate font-medium text-nb-300', far ? 'text-[21px] leading-tight' : 'text-[13px]')} title={data.title}>{data.title}</span>
            )}
          </div>
          {far && data.clusterTag && <div className="truncate text-[14px] leading-4 text-nb-500">{data.clusterTag}</div>}
          {!far && <div className="truncate text-[11px] text-nb-500" title={data.subtitle}>{data.subtitle}</div>}
          {!far && isMachine && <div className="truncate text-[11px] text-nb-500" title={data.meta}>{data.meta}</div>}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          <span className={clsx('rounded-full', far ? 'size-3' : 'size-2')} style={{ background: STATUS_COLOR[data.status] }} title={data.status} />
          {!far && !isMachine && <span className="text-[11px] text-nb-500">{data.meta}</span>}
          {far && (data.hint || data.notReady) && <span className={clsx('size-2.5 rounded-sm', data.notReady ? 'bg-warn' : 'bg-accent')} title={data.notReady ?? `Better in ${data.hint}`} />}
        </div>
      </div>
      {!far && <BadgeRow data={data} />}

      {data.pods && (
        <>
          <PodRail pods={data.pods} far={far} open={pods.open} onToggle={pods.toggle} buttonRef={pods.setAnchor} />
          {pods.open && pods.anchor && <PodPopover pods={data.pods} name={data.title} anchor={pods.anchor} onClose={pods.close} />}
        </>
      )}

      {!far && data.chips && <ChipRow chips={data.chips} />}
    </div>
  )
}

/* ---------- Calm card ---------- */
// The Calm presentation draws a card as what identifies it (its icon, its name, its status dot) and, when something is wrong with it,
// one line in the state colour saying what. The pods of a service wait for hover, keyboard focus, selection and a close zoom; a card
// that is fine has nothing else to say, since the kind, namespace, replicas and hardware are the Inspector's. The layout gives a card
// only its own small height (graph.ts); what a hover reveals is drawn over the gap below it, so it never moves anything else.
// `group/card` is the card's own surface: CSS alone decides what a hover shows, no state.
/** A part of a card that waits for hover or focus. Whole class names, so Tailwind finds them. */
const QUIET = 'hidden group-hover/card:flex group-hover/card:animate-[quiet-in_120ms_ease-out] [.react-flow__node:focus-within_&]:flex [.react-flow__node:focus-within_&]:animate-[quiet-in_120ms_ease-out]'
/** From this zoom on the person is looking closely: pods are drawn without waiting for a hover. */
export const NEAR_ZOOM = 1.1
const useNear = () => useStore((s) => s.transform[2] >= NEAR_ZOOM)

function CalmCard({ data, selected }: NodeProps<CardNode>) {
  const isMachine = data.kind === 'machine'
  const pods = usePodPopover()
  const far = useFar()
  const near = useNear()
  const color = TIER_COLOR[data.tier]
  const alert = data.alert
  const tone = alert ? ALERT_COLOR[alert] : undefined
  const shown = shownStatus(data.status, alert)
  const more = !far && (data.pods || data.hint || data.mesh || data.chips)
  // A services-on-nodes list is something the person asked to see, so it stays up.
  const out = !!data.chips || selected || near || pods.open
  return (
    <div data-far={far ? '1' : undefined} data-alert={alert} className="relative h-full w-full">
      <AllHandles />
      {/* The surface is at least the card's own box and grows downward over the gap when it has more to show. */}
      <div
        className={clsx(
          'group/card absolute inset-x-0 top-0 min-h-full rounded-xl border bg-nb-925 px-3.5 py-[7px] transition-colors',
          selected ? 'border-nb-100 shadow-[inset_0_0_0_1px_var(--color-nb-100)]' : alert ? '' : 'border-nb-800 hover:border-nb-700',
        )}
        title={alert && data.note ? `${data.title}: ${data.note}` : undefined}
        style={tone && !selected ? { borderColor: `color-mix(in srgb, ${tone} 70%, transparent)`, background: `color-mix(in srgb, ${tone} 7%, var(--color-nb-925))` } : undefined}
      >
        <div className="flex h-9 items-center gap-3">
          <div className="grid size-8 shrink-0 place-items-center rounded-lg" style={{ background: `color-mix(in srgb, ${color} 14%, transparent)`, color }}>
            {createElement(iconOf(data), { size: ICON_SM })}
          </div>
          <div className="min-w-0 flex-1">
            {isMachine ? (
              <>
                <span className={clsx('block truncate font-medium text-nb-300', far ? 'text-[21px] leading-tight' : 'text-[13px]')} title={data.title} aria-hidden="true">{middleTruncate(data.title, far ? 15 : 24)}</span>
                <span className="sr-only">{data.title}</span>
              </>
            ) : (
              <span className={clsx('block truncate font-medium text-nb-300', far ? 'text-[21px] leading-tight' : 'text-[13px]')} title={data.title}>{data.title}</span>
            )}
            {far && data.clusterTag && <div className="truncate text-[14px] leading-4 text-nb-500">{data.clusterTag}</div>}
          </div>
          {!far && data.kind === 'device' && data.meta && <span className="shrink-0 text-[11px] text-nb-500" title="Devices in this group">{data.meta}</span>}
          <span className={clsx('shrink-0 rounded-full', far ? 'size-3' : 'size-2')} style={{ background: shown.color }} title={shown.word} />
        </div>
        {alert && data.note && (
          <div className={clsx('fine -mt-0.5 truncate pl-11', ALERT_TEXT[alert])} title={data.note} data-testid="card-note">{data.note}</div>
        )}
        {more && (
          <div className={clsx('flex-col gap-1.5 pb-1 pt-1.5', out ? 'flex' : QUIET)}>
            <BadgeRow data={data} calm />
            {data.pods && (
              <>
                <PodRail pods={data.pods} far={far} open={pods.open} onToggle={pods.toggle} buttonRef={pods.setAnchor} />
                {pods.open && pods.anchor && <PodPopover pods={data.pods} name={data.title} anchor={pods.anchor} onClose={pods.close} />}
              </>
            )}
            {data.chips && <ChipRow chips={data.chips} />}
          </div>
        )}
      </div>
    </div>
  )
}

export const Card = memo(function Card(props: NodeProps<CardNode>) {
  return useContext(DetailContext) === 'calm' ? <CalmCard {...props} /> : <FullCard {...props} />
})

export const nodeTypes = { boundary: GroupBox, card: Card, namespace: NamespaceBox }

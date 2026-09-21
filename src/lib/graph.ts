/**
 * Projects the topology model onto a React Flow graph.
 *
 * One model, several planes: `buildGraph` is the only place that knows how a
 * "view" turns entities into boxes and edges, so adding a new plane (network,
 * data-flow, cost…) means adding a branch here + optionally a node component.
 */
import { placeLabel } from './present'
import { MarkerType, Position, type Edge, type Node } from '@xyflow/react'
import { isObserved } from './observed'
import { clusterMeshLine, connectionVerdict, inMesh, meshName, proxyWords, type MeshVerdict } from './mesh'
import { clusterLoad, pathQuality, type ClusterLoad, type PathQuality } from './metrics'
import {
  DEVICE_KINDS,
  TIER_ORDER,
  type Cluster,
  type Dependency,
  type Device,
  type DeviceKind,
  type ExternalEndpoint,
  type GroupBy,
  type MachineKind,
  type MachineNode,
  type Path,
  type Site,
  type Status,
  type Tier,
  type Topology,
  type ViewKind,
  type Service,
} from './types'

/* ---------- node data ---------- */
export type GroupData = {
  kind: 'group'
  /** Cluster id, tier name, or (device / external groups) site id · 'all' · 'none'. */
  entityId: string
  groupBy: GroupBy
  /** Set for groups that are not clusters or tiers. */
  extra?: 'devices' | 'external'
  title: string
  subtitle: string
  /** Distribution of the cluster, for its logo. */
  distribution?: string
  /** ISO country code of the site, for its flag. */
  country?: string
  tier: Tier
  status: Status
  stats: string
  empty: string
  /** How loaded a cluster is, from what its nodes report. Only cluster groups have it. */
  load?: ClusterLoad
  /** Service mesh overlay: what mesh the cluster runs, e.g. "Istio 1.22 · sidecar · mTLS permissive". */
  mesh?: { label: string; tone: 'good' | 'warn' | 'bad'; title: string }
}

export type CardData = {
  kind: 'service' | 'machine' | 'device' | 'external'
  entityId: string
  title: string
  subtitle: string
  meta: string
  status: Status
  tier: Tier
  clusterName: string
  machineKind?: MachineKind
  deviceKind?: DeviceKind
  /** Units a device group stands for (used for the group total). */
  units?: number
  control?: boolean
  /** Services drawn inside a machine card (infrastructure view overlay). */
  chips?: { id: string; name: string }[]
  /** Placement advice: where this service would be better off (a cluster name), when the engine has an opinion. */
  hint?: string
  /** Replicas wanted and ready, for a service that is not fully up. */
  notReady?: string
  /** Service mesh overlay: how the mesh treats this service. `tone` picks the chip's colour. */
  mesh?: { label: string; tone: 'in' | 'control' | 'out'; title: string }
}

export type GroupNode = Node<GroupData, 'boundary'>
export type CardNode = Node<CardData, 'card'>
export type TopoNode = GroupNode | CardNode

export type EdgeData = {
  crossGroup: boolean
  aggregated: boolean
  from: string
  to: string
  sources?: string[]
  confidence?: string
  /** Seen in traffic, not only declared. */
  observed?: boolean
  /** Seen once, but not for a long time. */
  stale?: boolean
  /** 0..1, how much traffic (log scale); drives the line width and the speed of the moving dashes. */
  weight?: number
  /** Measured round trip and loss between the two clusters, when the ends are in different ones. */
  quality?: PathQuality
  /** Service mesh overlay: what the mesh does to this connection, inferred from configuration. */
  mesh?: MeshVerdict
  /** Pixels to shift the line sideways (across the line from the caller to the callee) so parallel lines between the same two boxes stay apart. */
  offset?: number
}
export type TopoEdge = Edge<EdgeData>

export interface GraphOptions {
  view: ViewKind
  groupBy: GroupBy
  servicesOnNodes: boolean
  links: boolean
  /** Application view: also draw devices (IoT) and external endpoints that dependencies point at. */
  devices?: boolean
  /** Also draw traffic that is machinery (DNS lookups, system namespaces); off by default. */
  noise?: boolean
  /** Measured paths, to put round trip and loss on links between clusters. */
  paths?: Path[]
  /** Show the service mesh: its control plane, which services are in it, and what it does to each connection. */
  mesh?: boolean
  /** Service id → name of the cluster the placement engine would move it to. */
  hints?: Map<string, string>
}

export const groupId = (key: string) => `g:${key}`
export const cardId = (id: string) => `c:${id}`

/* ---------- layout constants ---------- */
const PAD = 20
const HEADER = 76
const GAP_X = 72
const GAP_Y = 44
const GROUP_GAP_X = 64
const ROW_GAP = 150
const APP_CARD = { w: 244, h: 68 }
const MACHINE_CARD = { w: 248, h: 84 }
const CHIP_ROW = 22

interface Box {
  x: number
  y: number
  w: number
  h: number
}

interface Item {
  id: string // react-flow id
  data: CardData
  w: number
  h: number
}

/** Rows 0-2 are the cluster tiers; devices sit below the far edge, external endpoints below that. */
const DEVICE_ROW = 3
const EXTERNAL_ROW = 4

interface GroupAcc {
  key: string
  row: number
  tier: Tier
  cluster?: Cluster
  /** Device / external groups: what to show in the header. */
  extra?: { kind: 'devices' | 'external'; entityId: string; title: string; subtitle: string; country?: string }
  items: Item[]
}

interface Placed {
  g: GroupAcc
  w: number
  h: number
  children: { item: Item; x: number; y: number; h: number }[]
}

const worstStatus = (ss: Status[]): Status => {
  for (const s of ['offline', 'degraded', 'unknown', 'healthy'] as const) if (ss.includes(s)) return s
  return 'unknown'
}

export function buildGraph(topology: Topology, o: GraphOptions): { nodes: TopoNode[]; edges: TopoEdge[] } {
  // Machinery traffic (DNS, kube-system) would bury the applications' own edges; it is opt-in.
  const t: Topology = o.noise ? topology : { ...topology, dependencies: topology.dependencies.filter((d) => !d.noise) }
  const clusterById = new Map(t.clusters.map((c) => [c.id, c]))
  const serviceById = new Map(t.services.map((w) => [w.id, w]))
  const siteById = new Map(t.sites.map((s) => [s.id, s]))

  /* 1. Build the items (cards) of the chosen plane, keyed by their group. */
  const groups = new Map<string, GroupAcc>()
  const ensureGroup = (c: Cluster) => {
    const key = o.groupBy === 'cluster' ? c.id : c.tier
    if (!groups.has(key)) groups.set(key, { key, row: TIER_ORDER[c.tier], tier: c.tier, cluster: o.groupBy === 'cluster' ? c : undefined, items: [] })
    return groups.get(key)!
  }
  // Empty clusters still get a (placeholder) group so they are visible.
  t.clusters.forEach(ensureGroup)

  const groupKeyOfCluster = (cid: string) => {
    const c = clusterById.get(cid)
    return c ? (o.groupBy === 'cluster' ? c.id : c.tier) : undefined
  }
  /** Which group each card ended up in, by entity id (ids are unique across entity kinds). */
  const groupOfEntity = new Map<string, string>()

  if (o.view === 'application') {
    const sorted = [...t.services].sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name))
    for (const w of sorted) {
      const c = clusterById.get(w.clusterId)
      if (!c) continue
      // The mesh's own workloads (istiod, ztunnel, gateways) are machinery: shown only with the mesh overlay.
      if (w.mesh?.controlPlane && !o.mesh) continue
      const g = ensureGroup(c)
      g.items.push(serviceItem(w, c, o.groupBy === 'tier', o.hints?.get(w.id), o.mesh))
      groupOfEntity.set(w.id, g.key)
    }

    if (o.devices) {
      const siteById = new Map(t.sites.map((s) => [s.id, s]))
      const perSite = o.groupBy === 'cluster'
      for (const dv of [...t.devices].sort((a, b) => a.name.localeCompare(b.name))) {
        const s = dv.siteId ? siteById.get(dv.siteId) : undefined
        const entityId = perSite ? (s?.id ?? 'none') : 'all'
        const key = `dev:${entityId}`
        if (!groups.has(key)) {
          groups.set(key, {
            key,
            row: DEVICE_ROW,
            tier: 'far-edge',
            extra: {
              kind: 'devices',
              entityId,
              title: perSite ? (s?.name ?? 'Unplaced devices') : 'Devices',
              subtitle: perSite ? (s ? [s.kind.replace('-', ' '), placeLabel(s)].filter(Boolean).join(' · ') : 'No site assigned') : '',
              country: perSite ? s?.country : undefined,
            },
            items: [],
          })
        }
        groups.get(key)!.items.push(deviceItem(dv, s, !perSite))
        groupOfEntity.set(dv.id, key)
      }

      // External endpoints only appear when something actually calls them (or is called by them).
      const wanted = new Set(t.dependencies.flatMap((d) => [d.fromKind === 'external' ? d.from : '', d.toKind === 'external' ? d.to : '']).filter(Boolean))
      const ext = t.externalEndpoints.filter((e) => wanted.has(e.id)).sort((a, b) => a.host.localeCompare(b.host))
      if (ext.length) {
        const key = 'ext:all'
        groups.set(key, { key, row: EXTERNAL_ROW, tier: 'cloud', extra: { kind: 'external', entityId: 'all', title: 'External', subtitle: 'Outside every onboarded cluster' }, items: [] })
        for (const e of ext) {
          groups.get(key)!.items.push(externalItem(e))
          groupOfEntity.set(e.id, key)
        }
      }
    }
  } else {
    const sorted = [...t.nodes].sort((a, b) => Number(b.role === 'control-plane') - Number(a.role === 'control-plane') || a.name.localeCompare(b.name))
    for (const n of sorted) {
      const c = clusterById.get(n.clusterId)
      if (!c) continue
      const chips = o.servicesOnNodes
        ? t.services.filter((w) => w.nodeIds.includes(n.id)).map((w) => ({ id: w.id, name: w.name }))
        : undefined
      ensureGroup(c).items.push(machineItem(n, c, chips, o.groupBy === 'tier'))
    }
  }

  /* 2. Lay out: tiers are rows (cloud on top → far edge at the bottom), groups sit side by side. */
  const rows = new Map<number, GroupAcc[]>()
  ;[...groups.values()]
    .sort((a, b) => a.row - b.row || (a.cluster?.name ?? a.extra?.title ?? '').localeCompare(b.cluster?.name ?? b.extra?.title ?? ''))
    .forEach((g) => {
      rows.set(g.row, [...(rows.get(g.row) ?? []), g])
    })

  const layoutGroup = (g: GroupAcc): Placed => {
    const n = g.items.length
    const cols = Math.max(1, Math.min(4, Math.ceil(Math.sqrt(n))))
    const cw = g.items[0]?.w ?? 0
    const children: Placed['children'] = []
    let y = HEADER
    for (let i = 0; i < n; i += cols) {
      const slice = g.items.slice(i, i + cols)
      const rowH = Math.max(...slice.map((s) => s.h))
      slice.forEach((item, j) => children.push({ item, x: PAD + j * (cw + GAP_X), y, h: rowH }))
      y += rowH + GAP_Y
    }
    const usedCols = Math.min(cols, n)
    const w = n === 0 ? 248 : PAD * 2 + usedCols * cw + (usedCols - 1) * GAP_X
    const h = n === 0 ? HEADER + 52 : y - GAP_Y + PAD
    return { g, w: Math.max(w, 248), h, children }
  }

  const placedRows = [...rows.entries()].sort((a, b) => a[0] - b[0]).map(([, gs]) => gs.map(layoutGroup))
  const rowWidths = placedRows.map((r) => r.reduce((s, p) => s + p.w, 0) + (r.length - 1) * GROUP_GAP_X)
  const maxW = Math.max(0, ...rowWidths)

  const nodes: TopoNode[] = []
  const abs = new Map<string, Box>() // absolute boxes by react-flow id (for edge routing)
  let y = 0
  placedRows.forEach((row, ri) => {
    let x = (maxW - rowWidths[ri]) / 2
    for (const p of row) {
      const { g } = p
      const gid = groupId(g.key)
      const cl = g.cluster
      const groupStatus = worstStatus(cl ? [cl.status, ...g.items.map((i) => i.data.status)] : g.items.map((i) => i.data.status))
      const clustersInTier = t.clusters.filter((c) => c.tier === g.tier).length
      const ex = g.extra
      const units = g.items.reduce((s, i) => s + (i.data.units ?? 1), 0)
      const siteCount = new Set(g.items.map((i) => i.data.clusterName).filter(Boolean)).size
      nodes.push({
        id: gid,
        type: 'boundary',
        position: { x, y },
        style: { width: p.w, height: p.h },
        zIndex: 0,
        draggable: true,
        data: {
          kind: 'group',
          entityId: ex ? ex.entityId : g.key,
          groupBy: o.groupBy,
          extra: ex?.kind,
          title: ex ? ex.title : cl ? cl.name : tierLabel(g.tier),
          subtitle: ex
            ? ex.subtitle || (ex.kind === 'devices' ? `${siteCount} site${siteCount === 1 ? '' : 's'}` : '')
            : cl
              ? [cl.distribution, placeLabel(siteById.get(cl.siteId ?? '')) || cl.region].filter(Boolean).join(' · ')
              : `${clustersInTier} cluster${clustersInTier === 1 ? '' : 's'}`,
          distribution: cl?.distribution,
          country: ex ? ex.country : cl ? siteById.get(cl.siteId ?? '')?.country : undefined,
          tier: g.tier,
          status: groupStatus,
          load: cl ? clusterLoad(cl, t.nodes, t.services) : undefined,
          mesh: o.mesh && o.view === 'application' && cl?.mesh ? groupMesh(cl.mesh) : undefined,
          stats: ex?.kind === 'devices' ? `${units} devices` : ex ? `${g.items.length} endpoints` : `${g.items.length} ${o.view === 'application' ? 'services' : 'nodes'}`,
          empty: o.view === 'application' ? 'No services' : 'No nodes',
        },
      })
      abs.set(gid, { x, y, w: p.w, h: p.h })
      for (const c of p.children) {
        nodes.push({
          id: c.item.id,
          type: 'card',
          parentId: gid,
          extent: 'parent',
          position: { x: c.x, y: c.y },
          style: { width: c.item.w, height: c.h },
          zIndex: 10,
          data: c.item.data,
        })
        abs.set(c.item.id, { x: x + c.x, y: y + c.y, w: c.item.w, h: c.h })
      }
      x += p.w + GROUP_GAP_X
    }
    y += Math.max(...row.map((p) => p.h)) + ROW_GAP
  })

  /* 3. Edges. */
  const edges: TopoEdge[] = []
  if (o.view === 'application') {
    for (const d of t.dependencies) {
      const s = cardId(d.from)
      const tg = cardId(d.to)
      if (!abs.has(s) || !abs.has(tg)) continue // an end is not drawn (e.g. devices hidden)
      const cross = groupOfEntity.get(d.from) !== groupOfEntity.get(d.to)
      const fc = serviceById.get(d.from)?.clusterId
      const tc = serviceById.get(d.to)?.clusterId
      const quality = o.paths && fc && tc && fc !== tc ? pathQuality(o.paths, fc, tc) : undefined
      const verdict = o.mesh ? connectionVerdict(d, serviceById.get(d.from), serviceById.get(d.to), fc ? clusterById.get(fc) : undefined, t.namespaces) : undefined
      edges.push(makeEdge(d.id, s, tg, abs, {
        // The line says what it is; what the mesh does to it is the colour (see the legend) and the inspector's words.
        label: edgeLabel(d),
        mesh: verdict,
        quality,
        cross: cross || !!d.crossCluster,
        aggregated: false,
        from: d.from,
        to: d.to,
        sources: d.sources,
        confidence: d.confidence,
        observed: isObserved(d),
        stale: d.stale,
        weight: weightOf(d),
      }))
    }
  } else if (o.links) {
    // Aggregate service dependencies into group ↔ group links.
    const agg = new Map<string, { a: string; b: string; count: number }>()
    for (const d of t.dependencies) {
      const from = serviceById.get(d.from)
      const to = serviceById.get(d.to)
      if (!from || !to) continue
      const a = groupKeyOfCluster(from.clusterId)
      const b = groupKeyOfCluster(to.clusterId)
      if (!a || !b || a === b) continue
      const [k1, k2] = a < b ? [a, b] : [b, a]
      const key = `${k1}|${k2}`
      const cur = agg.get(key) ?? { a: k1, b: k2, count: 0 }
      cur.count++
      agg.set(key, cur)
    }
    for (const { a, b, count } of agg.values()) {
      if (!abs.has(groupId(a)) || !abs.has(groupId(b))) continue
      edges.push(makeEdge(`agg:${a}|${b}`, groupId(a), groupId(b), abs, {
        label: `${count} ${count === 1 ? 'dependency' : 'dependencies'}`,
        cross: true,
        aggregated: true,
        from: a,
        to: b,
      }))
    }
  }

  spreadParallel(edges)
  return { nodes, edges }
}

/** Two lines between the same pair of boxes (two ports, or one each way) would be drawn on top of each other and one would vanish. */
function spreadParallel(edges: TopoEdge[]) {
  const by = new Map<string, TopoEdge[]>()
  for (const e of edges) {
    const key = e.source < e.target ? `${e.source}|${e.target}` : `${e.target}|${e.source}`
    by.set(key, [...(by.get(key) ?? []), e])
  }
  for (const group of by.values()) {
    if (group.length < 2) continue
    group.sort((a, b) => a.id.localeCompare(b.id))
    group.forEach((e, i) => {
      // The sideways direction is taken from the canonical order of the two boxes, so a line back the other way lands on the other side.
      const sign = e.source < e.target ? 1 : -1
      e.type = 'offset'
      e.data = { ...e.data!, offset: (i - (group.length - 1) / 2) * 14 * sign }
    })
  }
}

/* ---------- helpers ---------- */
function tierLabel(t: Tier) {
  return t === 'far-edge' ? 'Far edge' : t[0].toUpperCase() + t.slice(1)
}

function serviceItem(w: Service, c: Cluster, withCluster: boolean, hint?: string, mesh?: boolean): Item {
  const notReady = w.readyReplicas !== undefined && w.readyReplicas < w.replicas
  return {
    id: cardId(w.id),
    w: APP_CARD.w,
    // One row of small chips under the name; two when the mesh chip has to share it with advice or a warning.
    h: APP_CARD.h + (mesh && w.mesh && (hint || notReady) ? 46 : hint || notReady || (mesh && w.mesh) ? 24 : 0),
    data: {
      kind: 'service',
      entityId: w.id,
      title: w.name,
      subtitle: [withCluster ? c.name : '', w.namespace, w.kind].filter(Boolean).join(' · '),
      meta: `×${w.replicas}`,
      status: w.status,
      tier: c.tier,
      clusterName: c.name,
      hint,
      notReady: notReady ? `${w.readyReplicas}/${w.replicas} ready` : undefined,
      mesh: mesh && w.mesh ? meshChip(w) : undefined,
    },
  }
}

function groupMesh(m: NonNullable<Cluster['mesh']>): NonNullable<GroupData['mesh']> {
  const tone = m.mtls === 'strict' || m.mtls === 'automatic' ? 'good' : m.mtls === 'disabled' ? 'bad' : 'warn'
  const note = m.policyRead ? '' : ` ${m.policyNote ?? 'The mesh policy could not be read.'}`
  return { label: `${meshName(m.kind)} · ${m.mode}`, tone, title: `${clusterMeshLine(m)}. Inferred from configuration.${note}` }
}

function meshChip(w: Service): NonNullable<CardData['mesh']> {
  const m = w.mesh!
  const words = proxyWords(m)
  const src = m.source === 'pods' ? 'A proxy container was seen in its pods.' : m.source === 'namespace' ? 'Only what its namespace asks for; no proxy was seen yet.' : 'What its workload asks for.'
  return { label: words, tone: m.controlPlane ? 'control' : inMesh(m) ? 'in' : 'out', title: `${words}. ${src} Inferred from configuration.` }
}

const DEVICE_LABEL = Object.fromEntries(DEVICE_KINDS.map((k) => [k.value, k.label])) as Record<DeviceKind, string>

function deviceItem(dv: Device, s: Site | undefined, withSite: boolean): Item {
  return {
    id: cardId(dv.id),
    w: APP_CARD.w,
    h: APP_CARD.h,
    data: {
      kind: 'device',
      entityId: dv.id,
      title: dv.name,
      subtitle: [withSite ? s?.name : '', DEVICE_LABEL[dv.kind], dv.protocol].filter(Boolean).join(' · '),
      meta: dv.count > 1 ? `×${dv.count}` : '',
      status: dv.status,
      tier: 'far-edge',
      clusterName: s?.name ?? '',
      deviceKind: dv.kind,
      units: dv.count,
    },
  }
}

function externalItem(e: ExternalEndpoint): Item {
  return {
    id: cardId(e.id),
    w: APP_CARD.w,
    h: APP_CARD.h,
    data: {
      kind: 'external',
      entityId: e.id,
      title: e.name ?? e.host,
      subtitle: [e.kind, e.name ? e.host : '', e.port ? `port ${e.port}` : ''].filter(Boolean).join(' · '),
      meta: '',
      status: 'unknown',
      tier: 'cloud',
      clusterName: '',
    },
  }
}

function machineItem(n: MachineNode, c: Cluster, chips: { id: string; name: string }[] | undefined, withCluster: boolean): Item {
  const chipRows = chips && chips.length ? Math.ceil(chips.length / 2) : 0
  return {
    id: cardId(n.id),
    w: MACHINE_CARD.w,
    h: MACHINE_CARD.h + (chips ? (chipRows ? chipRows * CHIP_ROW + 14 : 26) : 0),
    data: {
      kind: 'machine',
      entityId: n.id,
      title: n.name,
      subtitle: [withCluster ? c.name : '', n.role === 'control-plane' ? 'Control plane' : 'Worker', n.ip].filter(Boolean).join(' · '),
      meta: `${n.cpu} vCPU · ${n.memoryGb} GB`,
      status: n.status,
      tier: c.tier,
      clusterName: c.name,
      machineKind: n.kind,
      control: n.role === 'control-plane',
      chips,
    },
  }
}

type Side = 'top' | 'bottom' | 'left' | 'right'
const POS: Record<Side, Position> = { top: Position.Top, bottom: Position.Bottom, left: Position.Left, right: Position.Right }
export const SIDES = POS

function pickSides(a: Box, b: Box): [Side, Side] {
  const dx = b.x + b.w / 2 - (a.x + a.w / 2)
  const dy = b.y + b.h / 2 - (a.y + a.h / 2)
  if (Math.abs(dy) >= Math.abs(dx) * 0.6 && Math.abs(dy) > 8) return dy > 0 ? ['bottom', 'top'] : ['top', 'bottom']
  return dx >= 0 ? ['right', 'left'] : ['left', 'right']
}

function makeEdge(
  id: string,
  source: string,
  target: string,
  abs: Map<string, Box>,
  d: { label: string; mesh?: MeshVerdict; cross: boolean; aggregated: boolean; from: string; to: string; sources?: string[]; confidence?: string; observed?: boolean; stale?: boolean; weight?: number; quality?: PathQuality },
): TopoEdge {
  const [ss, ts] = pickSides(abs.get(source)!, abs.get(target)!)
  return {
    id,
    source,
    target,
    sourceHandle: `${ss}-s`,
    targetHandle: `${ts}-t`,
    label: d.label,
    animated: false,
    className: d.cross ? 'edge-animated' : undefined,
    // React Flow adds the higher of the two end nodes' z to this. Card↔card edges must land just below
    // the cards (10) so they never steal clicks; group↔group links sit just above the group boxes (0).
    zIndex: d.aggregated ? 5 : -1,
    markerEnd: d.aggregated ? undefined : { type: MarkerType.ArrowClosed, width: 14, height: 14 },
    data: { crossGroup: d.cross, aggregated: d.aggregated, from: d.from, to: d.to, sources: d.sources, confidence: d.confidence, observed: d.observed, stale: d.stale, weight: d.weight, quality: d.quality, mesh: d.mesh },
  }
}

/**
 * What is written on the line: only what it is ("TCP:5432"). The numbers (traffic, round trip, loss, how it
 * was found) are in the inspector when the line is selected, so a line never carries a paragraph.
 */
function edgeLabel(d: Dependency): string {
  return d.label ?? (d.port ? `${d.protocol}:${d.port}` : d.protocol)
}

/** How busy an edge is on a log scale, 0..1, so a chatty database link does not flatten everything else. */
function weightOf(d: Dependency): number | undefined {
  const s = d.stats
  const x = s?.bytesPerSec ? s.bytesPerSec : s?.connectionsPerMin ? s.connectionsPerMin * 200 : 0
  if (!x) return undefined
  return Math.min(1, Math.log10(1 + x) / 6)
}

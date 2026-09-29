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
  type Service,
  type ServiceKind,
  type Site,
  type Status,
  type Tier,
  type Topology,
  type ViewKind,
} from './types'

/* ---------- node data ---------- */
export type GroupData = {
  kind: 'group'
  /** Cluster id, tier name, or (device / external groups) site id · 'all' · 'none'. */
  entityId: string
  groupBy: GroupBy
  /** Set for groups that are not clusters or tiers. */
  extra?: 'devices' | 'external' | 'operators'
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
  /** Set on a real cluster box (groupBy 'cluster' only) when one of its approved agents has at least one
   *  telemetry signal actually running - a "local operator" is a property of that agent, not a separate
   *  node on the canvas (see nodes.tsx's antenna badge). */
  localTelemetry?: { layers: string[] }
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
  /** Controller kind for a `service` card: Deployment, StatefulSet, DaemonSet or Job. */
  serviceKind?: ServiceKind
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

export type NamespaceData = {
  kind: 'namespace'
  /** Unique across the graph: the cluster's group key plus the namespace. */
  entityId: string
  clusterId: string
  namespace: string
  /** How many cards sit inside, for the header. */
  count: number
  status: Status
  tier: Tier
}
export type NamespaceNode = Node<NamespaceData, 'namespace'>

export type TopoNode = GroupNode | CardNode | NamespaceNode

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
  /** Pixels to shift this line's source end sideways (perpendicular to the caller->callee line). */
  sourceOffset?: number
  /** Pixels to shift this line's target end sideways (perpendicular to the caller->callee line). Independent from sourceOffset, so a line can fan out at a busy node while still landing cleanly at a quiet one. */
  targetOffset?: number
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
  /** Application view, grouped by cluster: draw a sub-box per namespace inside each cluster's box. */
  namespaces?: boolean
  /** Application view: lay services out as a left-to-right dependency chain instead of grouping them into
   * cluster/tier boxes. One flat ranking across every cluster - cross-cluster calls are already first-class
   * (see the `crossCluster` flag on Dependency), so a chain that also respected cluster boundaries would
   * fight the very ordering this is for. Each card still names its cluster in the subtitle. */
  chain?: boolean
  /** Cluster id → its active local-operator summary (which layers are actually live, and which agent to
   *  open when the badge is clicked). Computed by the caller from `agents`/`installedTelemetry`, not part
   *  of the core Topology model - purely a canvas annotation, the same role `hints` plays for placement. */
  localOperators?: Map<string, { layers: string[]; agentId: string }>
}

export const groupId = (key: string) => `g:${key}`
export const cardId = (id: string) => `c:${id}`

/* ---------- layout constants ---------- */
const PAD = 20
// The box header (title, subtitle/country/mesh line, and - when a cluster has load - a fourth load-meter row)
// runs up to about 72px tall at normal zoom; HEADER is the y where the first row of children starts, so it needs
// real breathing room past that, not just enough to avoid overlap.
const HEADER = 96
const GAP_X = 72
const GAP_Y = 44
const GROUP_GAP_X = 64
const ROW_GAP = 150
const APP_CARD = { w: 244, h: 68 }
const MACHINE_CARD = { w: 248, h: 84 }
const CHIP_ROW = 22
// A namespace sub-box nests one level inside a cluster box: a little padding and a short header for its
// name, then the same card grid a cluster box would use on its own.
const NS_PAD = 14
const NS_HEADER = 32
const NS_GAP_Y = 22

export interface Box {
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
  /** Set for service cards (application view): which namespace to nest it under when that option is on. */
  namespace?: string
}

/** Rows 0-2 are the cluster tiers; devices sit below the far edge, external endpoints below that, regional
 * operators below that again - the row order mirrors "how far this is from the workload itself". */
const DEVICE_ROW = 3
const EXTERNAL_ROW = 4
const OPERATOR_ROW = 5

interface GroupAcc {
  key: string
  row: number
  tier: Tier
  cluster?: Cluster
  /** Device / external / operator groups: what to show in the header. */
  extra?: { kind: 'devices' | 'external' | 'operators'; entityId: string; title: string; subtitle: string; country?: string }
  items: Item[]
}

interface PlacedChild {
  item: Item
  x: number
  y: number
  h: number
}

interface NsBox {
  namespace: string
  x: number
  y: number
  w: number
  h: number
  children: PlacedChild[]
}

interface Placed {
  g: GroupAcc
  w: number
  h: number
  children: PlacedChild[]
  /** Set instead of (never alongside) `children` when this group nests its items under namespace sub-boxes. */
  nsBoxes?: NsBox[]
}

/** One row of cards, left to right, wrapping at `cols`: shared by a cluster box and a namespace sub-box. */
function packItems(items: Item[], headerY: number, pad = PAD): { w: number; h: number; children: PlacedChild[] } {
  const n = items.length
  const cols = Math.max(1, Math.min(4, Math.ceil(Math.sqrt(n))))
  const cw = items[0]?.w ?? 0
  const children: PlacedChild[] = []
  let y = headerY
  for (let i = 0; i < n; i += cols) {
    const slice = items.slice(i, i + cols)
    const rowH = Math.max(...slice.map((s) => s.h))
    slice.forEach((item, j) => children.push({ item, x: pad + j * (cw + GAP_X), y, h: rowH }))
    y += rowH + GAP_Y
  }
  const usedCols = Math.min(cols, n)
  const w = n === 0 ? 0 : pad * 2 + usedCols * cw + (usedCols - 1) * GAP_X
  const h = n === 0 ? headerY : y - GAP_Y + pad
  return { w, h, children }
}

/**
 * Buckets a cluster's cards by namespace (in the order they already come in - callers sort services by
 * namespace first, so this does not need to sort again) and stacks the sub-boxes vertically. Every sub-box
 * gets the same width, the widest one's, so the stack reads as one aligned column rather than a jumble.
 */
function layoutNamespaces(items: Item[]): { w: number; h: number; boxes: NsBox[] } {
  const byNs = new Map<string, Item[]>()
  for (const it of items) {
    const key = it.namespace || 'no namespace'
    if (!byNs.has(key)) byNs.set(key, [])
    byNs.get(key)!.push(it)
  }
  const laidOut = [...byNs.entries()].map(([namespace, its]) => ({ namespace, its, ...packItems(its, NS_HEADER, NS_PAD) }))
  const w = Math.max(0, ...laidOut.map((b) => b.w))
  let y = 0
  const boxes: NsBox[] = laidOut.map((b) => {
    const box: NsBox = { namespace: b.namespace, x: 0, y, w, h: b.h, children: b.children }
    y += b.h + NS_GAP_Y
    return box
  })
  return { w, h: Math.max(0, y - NS_GAP_Y), boxes }
}

const worstStatus = (ss: Status[]): Status => {
  for (const s of ['offline', 'degraded', 'unknown', 'healthy'] as const) if (ss.includes(s)) return s
  return 'unknown'
}

export function buildGraph(topology: Topology, o: GraphOptions): { nodes: TopoNode[]; edges: TopoEdge[] } {
  // Machinery traffic (DNS, kube-system) would bury the applications' own edges; it is opt-in.
  const t: Topology = o.noise ? topology : { ...topology, dependencies: topology.dependencies.filter((d) => !d.noise) }
  if (o.view === 'application' && o.chain) return buildChainGraph(t, o)
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

  // Regional operators: a peer-group row, same mechanism as devices/external, but not gated to either
  // view branch above - an operator aggregates telemetry at the cluster level, which means the same thing
  // whether the canvas is currently showing services or nodes. An operator whose source clusters were all
  // filtered out (or don't exist) gets no box: a box with no arrows into it would just be noise. The group
  // has no items of its own (unlike devices/external) - the box itself *is* the operator.
  const operatorSourceKeys = new Map<string, string[]>()
  for (const op of (t.operators ?? []).filter((o2) => o2.status === 'active')) {
    const sourceKeys = [...new Set(op.sourceClusterIds.map(groupKeyOfCluster).filter((k): k is string => !!k))]
    if (!sourceKeys.length) continue
    operatorSourceKeys.set(op.id, sourceKeys)
    const key = `op:${op.id}`
    groups.set(key, {
      key,
      row: OPERATOR_ROW,
      tier: 'cloud',
      extra: {
        kind: 'operators',
        entityId: op.id,
        title: op.name,
        subtitle: `${sourceKeys.length} source cluster${sourceKeys.length === 1 ? '' : 's'}`,
      },
      items: [],
    })
  }

  /* 2. Lay out: tiers are rows (cloud on top → far edge at the bottom), groups sit side by side. */
  const rows = new Map<number, GroupAcc[]>()
  ;[...groups.values()]
    .sort((a, b) => a.row - b.row || (a.cluster?.name ?? a.extra?.title ?? '').localeCompare(b.cluster?.name ?? b.extra?.title ?? ''))
    .forEach((g) => {
      rows.set(g.row, [...(rows.get(g.row) ?? []), g])
    })

  // Namespace sub-boxes only make sense for a real cluster box in the application view: a tier box already
  // mixes several clusters together, and infrastructure cards (nodes) have no namespace.
  const nsEnabled = o.namespaces && o.view === 'application' && o.groupBy === 'cluster'

  const layoutGroup = (g: GroupAcc): Placed => {
    const n = g.items.length
    if (nsEnabled && g.cluster && n > 0) {
      const { w: nsW, h: nsH, boxes } = layoutNamespaces(g.items)
      return { g, w: Math.max(PAD * 2 + nsW, 248), h: HEADER + nsH + PAD, children: [], nsBoxes: boxes }
    }
    const { w, h, children } = packItems(g.items, HEADER)
    return { g, w: Math.max(w || 248, 248), h: n === 0 ? HEADER + 52 : h, children }
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
          localTelemetry: cl && o.groupBy === 'cluster' ? o.localOperators?.get(cl.id) : undefined,
          stats:
            ex?.kind === 'devices'
              ? `${units} devices`
              : ex?.kind === 'operators'
                ? 'Regional operator'
                : ex
                  ? `${g.items.length} endpoints`
                  : `${g.items.length} ${o.view === 'application' ? 'services' : 'nodes'}`,
          empty: o.view === 'application' ? 'No services' : 'No nodes',
        },
      })
      abs.set(gid, { x, y, w: p.w, h: p.h })
      if (p.nsBoxes) {
        for (const nb of p.nsBoxes) {
          const nsId = `ns:${g.key}:${nb.namespace}`
          nodes.push({
            id: nsId,
            type: 'namespace',
            parentId: gid,
            extent: 'parent',
            draggable: false,
            position: { x: PAD + nb.x, y: HEADER + nb.y },
            style: { width: nb.w, height: nb.h },
            zIndex: 5,
            data: {
              kind: 'namespace',
              entityId: nsId,
              clusterId: cl!.id,
              namespace: nb.namespace,
              count: nb.children.length,
              status: worstStatus(nb.children.map((c) => c.item.data.status)),
              tier: g.tier,
            },
          })
          for (const c of nb.children) {
            nodes.push({
              id: c.item.id,
              type: 'card',
              parentId: nsId,
              extent: 'parent',
              position: { x: c.x, y: c.y },
              style: { width: c.item.w, height: c.h },
              zIndex: 10,
              data: c.item.data,
            })
            abs.set(c.item.id, { x: x + PAD + nb.x + c.x, y: y + HEADER + nb.y + c.y, w: c.item.w, h: c.h })
          }
        }
      } else {
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

  // Regional operators: a real arrow from each source cluster's group box to the operator's box - this is
  // a declared relationship (RegionalOperator.sourceClusterIds), not one inferred from traffic, so unlike
  // the dependency edges above it draws the same way in every view/groupBy combination.
  for (const [opId, sourceKeys] of operatorSourceKeys) {
    const opGid = groupId(`op:${opId}`)
    if (!abs.has(opGid)) continue
    for (const gk of sourceKeys) {
      const gid = groupId(gk)
      if (!abs.has(gid)) continue
      edges.push(makeEdge(`op:${opId}:${gk}`, gid, opGid, abs, {
        label: 'telemetry',
        cross: true,
        aggregated: false,
        groupLevel: true,
        from: gk,
        to: `op:${opId}`,
      }))
    }
  }

  spreadFanned(edges, abs)
  spreadParallel(edges)
  return { nodes, edges }
}

/**
 * Several different edges landing on the same side of a busy node would otherwise all converge on that
 * one handle's point. Groups edges by (node, side) independently at each end (a line can fan out at one
 * end and land cleanly at the other), and within a group orders by where each line is actually headed -
 * so the fan-out visually tracks its destination instead of crossing itself near the node. Edges that
 * share the very same other box land in the same slot here; spreadParallel (run right after) adds the
 * fine, symmetric sub-offset that keeps those particular lines apart from each other.
 */
function spreadFanned(edges: TopoEdge[], abs: Map<string, Box>) {
  const FAN_GAP = 16
  type End = { edge: TopoEdge; role: 'source' | 'target' }
  const groups = new Map<string, End[]>()
  const add = (key: string, end: End) => groups.set(key, [...(groups.get(key) ?? []), end])
  for (const e of edges) {
    if (!e.sourceHandle || !e.targetHandle) continue
    add(`${e.source}|${e.sourceHandle.split('-')[0]}`, { edge: e, role: 'source' })
    add(`${e.target}|${e.targetHandle.split('-')[0]}`, { edge: e, role: 'target' })
  }
  const otherId = (en: End) => (en.role === 'source' ? en.edge.target : en.edge.source)
  for (const [key, ends] of groups) {
    const side = key.slice(key.indexOf('|') + 1) as Side
    const axis: 'x' | 'y' = side === 'top' || side === 'bottom' ? 'x' : 'y'
    const otherCenter = (en: End) => {
      const box = abs.get(otherId(en))
      if (!box) return 0
      return axis === 'x' ? box.x + box.w / 2 : box.y + box.h / 2
    }
    const clusters = new Map<string, End[]>()
    for (const en of ends) clusters.set(otherId(en), [...(clusters.get(otherId(en)) ?? []), en])
    if (clusters.size < 2) continue // one destination on this side: nothing to fan out from another
    const ordered = [...clusters.values()].sort((a, b) => otherCenter(a[0]) - otherCenter(b[0]) || a[0].edge.id.localeCompare(b[0].edge.id))
    ordered.forEach((cluster, i) => {
      const v = (i - (ordered.length - 1) / 2) * FAN_GAP
      for (const en of cluster) {
        en.edge.type = 'offset'
        en.edge.data = { ...en.edge.data!, [en.role === 'source' ? 'sourceOffset' : 'targetOffset']: v }
      }
    })
  }
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
      const v = (i - (group.length - 1) / 2) * 14 * sign
      e.type = 'offset'
      // Additive on top of any coarse fan-out offset already set: this pass only needs to separate the
      // handful of lines that share both endpoints, not decide where that whole bundle sits on the node.
      e.data = { ...e.data!, sourceOffset: (e.data?.sourceOffset ?? 0) + v, targetOffset: (e.data?.targetOffset ?? 0) + v }
    })
  }
}


/* ---------- Application view: chain layout ---------- */

const CHAIN_COL_GAP = 96
const CHAIN_ROW_GAP = 28

/**
 * Left-to-right dependency chain: one column per rank (longest path from a source, over service-to-service
 * calls only), ordered top-to-bottom within a column by a barycenter heuristic so lines cross as little as
 * possible. Cycles (two services depending on each other) are broken with a DFS feedback-arc pass before
 * ranking - the dropped back-edge is still drawn later, just without influencing anyone's column. Devices
 * and external endpoints are not part of the ranking; each is hung one column past whichever service it's
 * paired with (its first caller or callee found), stacked near that service's row.
 */
function layoutChain(
  serviceIds: string[],
  serviceDeps: { from: string; to: string }[],
  leafIds: string[],
  leafNear: Map<string, string | undefined>,
): Map<string, { x: number; y: number }> {
  const svc = new Set(serviceIds)
  const adj = new Map<string, Set<string>>(serviceIds.map((id) => [id, new Set<string>()]))
  const radj = new Map<string, Set<string>>(serviceIds.map((id) => [id, new Set<string>()]))
  const byFrom = new Map<string, { from: string; to: string }[]>()
  for (const e of serviceDeps) {
    if (!svc.has(e.from) || !svc.has(e.to) || e.from === e.to) continue
    byFrom.set(e.from, [...(byFrom.get(e.from) ?? []), e])
  }

  // DFS feedback-arc pass: an edge to a node still on the current path (GRAY) is a back-edge and is
  // dropped from the DAG used for ranking (it's still drawn - see the caller - just doesn't set anyone's rank).
  const WHITE = 0, GRAY = 1, BLACK = 2
  const color = new Map<string, number>(serviceIds.map((id) => [id, WHITE]))
  const dag: { from: string; to: string }[] = []
  const visit = (id: string) => {
    color.set(id, GRAY)
    for (const e of byFrom.get(id) ?? []) {
      if (color.get(e.to) === GRAY) continue
      dag.push(e)
      if (color.get(e.to) === WHITE) visit(e.to)
    }
    color.set(id, BLACK)
  }
  for (const id of serviceIds) if (color.get(id) === WHITE) visit(id)
  for (const e of dag) {
    adj.get(e.from)!.add(e.to)
    radj.get(e.to)!.add(e.from)
  }

  // Rank = longest path from a source. Bounded relaxation rather than a topo-sort walk: simple, and safe
  // even if a residual cycle somehow slipped through (it just stops after n rounds instead of looping).
  const rank = new Map<string, number>(serviceIds.map((id) => [id, 0]))
  for (let i = 0; i < serviceIds.length; i++) {
    let changed = false
    for (const e of dag) {
      const r = (rank.get(e.from) ?? 0) + 1
      if (r > (rank.get(e.to) ?? 0)) {
        rank.set(e.to, r)
        changed = true
      }
    }
    if (!changed) break
  }

  const byRank = new Map<number, string[]>()
  for (const id of serviceIds) {
    const r = rank.get(id)!
    byRank.set(r, [...(byRank.get(r) ?? []), id])
  }
  const maxRank = Math.max(0, ...byRank.keys())
  const order = new Map<string, number>()
  for (const ids of byRank.values()) ids.forEach((id, i) => order.set(id, i))

  // A couple of sweeps - toward already-placed callers, then toward already-placed callees - untangles
  // most of the avoidable crossings without needing a real crossing-count optimizer.
  const sweep = (neighbors: (id: string) => Set<string>) => {
    for (let r = 0; r <= maxRank; r++) {
      const ids = byRank.get(r)
      if (!ids || ids.length < 2) continue
      const bc = (id: string) => {
        const ns = [...neighbors(id)]
        return ns.length ? ns.reduce((s, n) => s + (order.get(n) ?? 0), 0) / ns.length : (order.get(id) ?? 0)
      }
      ids.sort((a, b) => bc(a) - bc(b) || a.localeCompare(b))
      ids.forEach((id, i) => order.set(id, i))
    }
  }
  sweep((id) => radj.get(id)!)
  sweep((id) => adj.get(id)!)

  const pos = new Map<string, { x: number; y: number }>()
  for (const [r, ids] of byRank) ids.forEach((id, i) => pos.set(id, { x: r * (APP_CARD.w + CHAIN_COL_GAP), y: i * (APP_CARD.h + CHAIN_ROW_GAP) }))

  const leafRank = new Map<string, number>()
  for (const id of leafIds) {
    const near = leafNear.get(id)
    leafRank.set(id, near !== undefined ? (rank.get(near) ?? 0) + 1 : maxRank + 1)
  }
  const byLeafRank = new Map<number, string[]>()
  for (const id of leafIds) {
    const r = leafRank.get(id)!
    byLeafRank.set(r, [...(byLeafRank.get(r) ?? []), id])
  }
  for (const [r, ids] of byLeafRank) {
    const nearY = (id: string) => {
      const near = leafNear.get(id)
      return near ? (pos.get(near)?.y ?? 0) : 0
    }
    ids.sort((a, b) => nearY(a) - nearY(b) || a.localeCompare(b))
    ids.forEach((id, i) => pos.set(id, { x: r * (APP_CARD.w + CHAIN_COL_GAP), y: i * (APP_CARD.h + CHAIN_ROW_GAP) }))
  }

  return pos
}

/** The Application view's alternate layout: every service (across every cluster) placed in one flat
 * left-to-right dependency chain instead of nested inside cluster/tier boxes. See `chain` on GraphOptions. */
function buildChainGraph(t: Topology, o: GraphOptions): { nodes: TopoNode[]; edges: TopoEdge[] } {
  const clusterById = new Map(t.clusters.map((c) => [c.id, c]))
  const siteById = new Map(t.sites.map((s) => [s.id, s]))
  const serviceById = new Map(t.services.map((w) => [w.id, w]))

  const services = t.services.filter((w) => {
    const c = clusterById.get(w.clusterId)
    return c && !(w.mesh?.controlPlane && !o.mesh)
  })
  const serviceIds = services.map((w) => w.id)

  const serviceDeps = t.dependencies.filter((d) => d.fromKind === 'service' && d.toKind === 'service')

  const wantedLeaves = new Set(
    t.dependencies.flatMap((d) => [d.fromKind !== 'service' ? d.from : '', d.toKind !== 'service' ? d.to : '']).filter(Boolean),
  )
  const deviceLeaves = o.devices ? t.devices.filter((dv) => wantedLeaves.has(dv.id)) : []
  const externalLeaves = o.devices ? t.externalEndpoints.filter((e) => wantedLeaves.has(e.id)) : []
  const leafIds = [...deviceLeaves.map((d) => d.id), ...externalLeaves.map((e) => e.id)]

  const leafNear = new Map<string, string | undefined>()
  for (const id of leafIds) {
    const dep = t.dependencies.find((d) => (d.from === id && serviceById.has(d.to)) || (d.to === id && serviceById.has(d.from)))
    leafNear.set(id, dep ? (serviceById.has(dep.from) ? dep.from : dep.to) : undefined)
  }

  const positions = layoutChain(serviceIds, serviceDeps, leafIds, leafNear)

  const nodes: TopoNode[] = []
  const abs = new Map<string, Box>()

  for (const w of services) {
    const c = clusterById.get(w.clusterId)!
    const item = serviceItem(w, c, true, o.hints?.get(w.id), o.mesh)
    const p = positions.get(w.id) ?? { x: 0, y: 0 }
    nodes.push({ id: item.id, type: 'card', position: { x: p.x, y: p.y }, style: { width: item.w, height: item.h }, zIndex: 10, data: item.data })
    abs.set(item.id, { x: p.x, y: p.y, w: item.w, h: item.h })
  }
  for (const dv of deviceLeaves) {
    const s = dv.siteId ? siteById.get(dv.siteId) : undefined
    const item = deviceItem(dv, s, true)
    const p = positions.get(dv.id) ?? { x: 0, y: 0 }
    nodes.push({ id: item.id, type: 'card', position: { x: p.x, y: p.y }, style: { width: item.w, height: item.h }, zIndex: 10, data: item.data })
    abs.set(item.id, { x: p.x, y: p.y, w: item.w, h: item.h })
  }
  for (const e of externalLeaves) {
    const item = externalItem(e)
    const p = positions.get(e.id) ?? { x: 0, y: 0 }
    nodes.push({ id: item.id, type: 'card', position: { x: p.x, y: p.y }, style: { width: item.w, height: item.h }, zIndex: 10, data: item.data })
    abs.set(item.id, { x: p.x, y: p.y, w: item.w, h: item.h })
  }

  const edges: TopoEdge[] = []
  for (const d of t.dependencies) {
    const s = cardId(d.from)
    const tg = cardId(d.to)
    if (!abs.has(s) || !abs.has(tg)) continue
    const fc = serviceById.get(d.from)?.clusterId
    const tc = serviceById.get(d.to)?.clusterId
    const quality = o.paths && fc && tc && fc !== tc ? pathQuality(o.paths, fc, tc) : undefined
    const verdict = o.mesh ? connectionVerdict(d, serviceById.get(d.from), serviceById.get(d.to), fc ? clusterById.get(fc) : undefined, t.namespaces) : undefined
    edges.push(makeEdge(d.id, s, tg, abs, {
      label: edgeLabel(d),
      mesh: verdict,
      quality,
      // No boxes to cross here, so "crossGroup" styling is driven only by the model's own crossCluster flag.
      cross: !!d.crossCluster,
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

  spreadFanned(edges, abs)
  spreadParallel(edges)
  return { nodes, edges }
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
    namespace: w.namespace,
    data: {
      kind: 'service',
      entityId: w.id,
      title: w.name,
      subtitle: [withCluster ? c.name : '', w.namespace, w.kind].filter(Boolean).join(' · '),
      meta: `×${w.replicas}`,
      status: w.status,
      tier: c.tier,
      clusterName: c.name,
      serviceKind: w.kind,
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

export type Side = 'top' | 'bottom' | 'left' | 'right'
const POS: Record<Side, Position> = { top: Position.Top, bottom: Position.Bottom, left: Position.Left, right: Position.Right }
export const SIDES = POS

/** Which side of each box a line between them should leave/enter from, purely from their relative
 * position. Also used, with each box collapsed to a single point (w=h=0), to re-derive an edge's
 * direction live from its current endpoints - see OffsetEdge.tsx, which needs the exact same heuristic
 * so a node dragged around inside its box doesn't leave the arrowhead pointing the wrong way. */
export function pickSides(a: Box, b: Box): [Side, Side] {
  const dx = b.x + b.w / 2 - (a.x + a.w / 2)
  const dy = b.y + b.h / 2 - (a.y + a.h / 2)
  if (Math.abs(dy) >= Math.abs(dx) * 0.6 && Math.abs(dy) > 8) return dy > 0 ? ['bottom', 'top'] : ['top', 'bottom']
  return dx >= 0 ? ['right', 'left'] : ['left', 'right']
}

/** Which service entity ids a canvas selection (a set of React Flow node ids, e.g. from a shift-drag
 * box-select) resolves to - the topology's "Define scope from selection" quick action (ScopeFromSelection.tsx)
 * uses this to turn a raw selection into a telemetry scope draft, silently dropping anything selected that
 * isn't a service card: a cluster/tier box, a namespace sub-box, or a machine/device/external card. Order
 * follows `nodes`, not `selectedIds`, so a scope built from the same selection is stable across re-renders. */
export function selectedServiceIds(nodes: TopoNode[], selectedIds: string[]): string[] {
  const ids = new Set(selectedIds)
  return nodes.filter((n) => ids.has(n.id) && n.data.kind === 'service').map((n) => n.data.entityId)
}

/** Folds a freshly computed layout (`next`, e.g. from a poll refresh or a toggled view option) onto
 * whatever React Flow currently has on screen (`prev`), instead of just returning `next` outright - this
 * is what lets `TopologyPage.tsx`'s node-resync effect run on every poll without fighting a manual drag or
 * resetting a node's position on every unrelated refresh. Three rules, in order of how much they cost to
 * get wrong:
 *  1. A node React Flow is actively mid-gesture with (its own `dragging` flag, set by `onNodesChange` for
 *     as long as the pointer is down) is returned completely untouched - not just position-preserved. React
 *     Flow's own drag tracking keys off the node *object* it started the gesture with; handing it a brand
 *     new object for that same id mid-drag (even one with an identical x/y) can desync that internal
 *     tracking, which is a very plausible way for a long drag - and this page polls every 2-5s - to leave
 *     the canvas, or the whole page, unresponsive to clicks until a reload. Every other node still resyncs
 *     normally; only the one actually being dragged is left alone until it's dropped.
 *  2. Otherwise, a node's on-screen position is kept as-is (not overwritten by the newly computed layout)
 *     as long as it still has the same parent - React Flow positions are parent-relative (or
 *     canvas-relative with no parent), so a stale position only still means what it used to while the
 *     parent hasn't changed. This is what lets a manual drag "stick" across the next poll instead of
 *     snapping back to `buildGraph`'s computed position.
 *  3. A changed parent (e.g. toggling namespace sub-boxes re-parents every card in a cluster from the
 *     cluster box straight to a namespace box without changing the card's id) always takes the freshly
 *     computed position - an old, differently-relative position would otherwise land the card in the wrong
 *     spot, often overlapping another card, until something forced a fresh layout.
 * `selectedRfId` just threads through the single-click Inspector highlight so callers don't need a second
 * pass over the result to re-apply it. */
export function resyncNodes(prev: TopoNode[], next: TopoNode[], selectedRfId: string | null): TopoNode[] {
  const prevById = new Map(prev.map((n) => [n.id, n]))
  return next.map((n) => {
    const old = prevById.get(n.id)
    if (old?.dragging) return old
    const keepOldPosition = old && old.parentId === n.parentId
    return { ...n, position: keepOldPosition ? old.position : n.position, selected: n.id === selectedRfId }
  })
}

function makeEdge(
  id: string,
  source: string,
  target: string,
  abs: Map<string, Box>,
  d: {
    label: string
    mesh?: MeshVerdict
    cross: boolean
    aggregated: boolean
    /** True for any other group↔group edge that, unlike the aggregated dependency-count lines, still
     *  wants a real arrowhead (e.g. a regional operator's source-cluster arrows) - controls only the
     *  z-index (group↔group edges sit just above the group boxes, at 5), independently of `aggregated`,
     *  which is what actually hides the marker below. */
    groupLevel?: boolean
    from: string
    to: string
    sources?: string[]
    confidence?: string
    observed?: boolean
    stale?: boolean
    weight?: number
    quality?: PathQuality
  },
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
    zIndex: d.aggregated || d.groupLevel ? 5 : -1,
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

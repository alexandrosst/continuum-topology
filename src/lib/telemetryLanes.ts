/**
 * The Telemetry tab of the topology: the platform (see platformLayer.ts) as lanes. One lane per cluster, left to right in the order
 * data travels - Discovery agent, Local operator, Regional operator, Central operator, FUSION - so the eye reads the pipeline as
 * columns. The regional operators, the central one and FUSION are shared: each is drawn once, level with the middle of what sends to it.
 * Plain data in, plain coordinates out; the component only draws them.
 */
import { PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformKind, type PlatformModel, type PlatformStatus } from './platformLayer'

export const LANE = { nodeW: 192, nodeH: 52, colGap: 48, padX: 4, head: 28, laneH: 88, caption: 20, minGap: 16 } as const

export const LANE_COLUMNS: { kind: PlatformKind; label: string }[] = [
  { kind: 'agent', label: 'Discovery agent' },
  { kind: 'local', label: 'Local operator' },
  { kind: 'regional', label: 'Regional operator' },
  { kind: 'central', label: 'Central operator' },
  { kind: 'fusion', label: 'FUSION' },
]
const COL = new Map(LANE_COLUMNS.map((c, i) => [c.kind, i]))

export interface LaneNode { entity: PlatformEntity; x: number; y: number }
export interface LaneHop {
  id: string
  from: string
  to: string
  /** The sender's state. */
  status: PlatformStatus
  /** When the last data crossed it ("27 s"), when something knows. */
  age?: string
  /** SVG path, from the sender's right edge to the receiver's left edge. */
  d: string
  /** Where its label sits. */
  mid: { x: number; y: number }
  /** Only a healthy hop has data moving on it. */
  flowing: boolean
}
export interface LaneLayout {
  width: number
  height: number
  columns: { kind: PlatformKind; label: string; x: number }[]
  lanes: { clusterId: string; name: string; y: number }[]
  nodes: LaneNode[]
  hops: LaneHop[]
}

const isBad = (s: PlatformStatus) => s === 'attention' || s === 'down'
const colX = (kind: PlatformKind) => LANE.padX + COL.get(kind)! * (LANE.nodeW + LANE.colGap)

/** What a screen reader hears for a part: its name and cluster, its state, and when its data last arrived. */
export function nodeLabel(e: PlatformEntity, age?: string): string {
  const state = e.off ? 'Not turned on' : PLATFORM_STATUS_WORD[e.status]
  return `${e.name}${e.clusterName ? `, ${e.clusterName}` : ''}: ${state}${age ? `, last data ${age}` : ''}`
}

/** A hop is as bad as the thing that sends: the label of a hop is only worth showing when something is wrong with it. */
export const hopNeedsLabel = (h: Pick<LaneHop, 'status' | 'age'>) => !!h.age && h.status !== 'healthy'

/**
 * Places the platform in lanes. A lane is a cluster with a Discovery agent; "problems only" keeps the lanes where something on the
 * way to FUSION needs attention or is not working (the shared parts it goes through included), and the shared parts those lanes reach.
 */
export function layoutLanes(model: PlatformModel, opts: { problemsOnly?: boolean } = {}): LaneLayout {
  const byId = new Map(model.entities.map((e) => [e.id, e]))
  const out = new Map<string, string[]>()
  const into = new Map<string, string[]>()
  for (const h of model.edges) {
    out.set(h.from, [...(out.get(h.from) ?? []), h.to])
    into.set(h.to, [...(into.get(h.to) ?? []), h.from])
  }
  const downstream = (id: string, seen = new Set<string>()): Set<string> => {
    for (const n of out.get(id) ?? []) if (!seen.has(n)) downstream(n, seen.add(n))
    return seen
  }

  const regionalIndex = new Map(model.entities.filter((e) => e.kind === 'regional').map((e, i) => [e.id, i]))
  let lanes = model.entities
    .filter((e) => e.kind === 'agent' && e.clusterId)
    .map((agent) => {
      const local = model.entities.find((e) => e.kind === 'local' && e.agentId === agent.agentId)
      const path = [agent, ...(local ? [local, ...[...downstream(local.id)].map((id) => byId.get(id)!)] : [])]
      // Lanes that send to the same regional operator sit together, so it can stand level with them.
      const rank = Math.min(...(local ? out.get(local.id) ?? [] : []).map((id) => regionalIndex.get(id) ?? Infinity), Infinity)
      return { agent, local, path, rank, name: agent.clusterName ?? agent.clusterId! }
    })
    .sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name))
  if (opts.problemsOnly) lanes = lanes.filter((l) => l.path.some((e) => isBad(e.status)))

  const nodes: LaneNode[] = []
  const placed = new Map<string, LaneNode>()
  const put = (entity: PlatformEntity, centre: number) => {
    const n = { entity, x: colX(entity.kind), y: centre - LANE.nodeH / 2 }
    nodes.push(n)
    placed.set(entity.id, n)
  }
  const centreOf = (id: string) => placed.get(id)!.y + LANE.nodeH / 2
  const mean = (ids: string[]) => {
    const ys = ids.filter((id) => placed.has(id)).map(centreOf)
    return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : undefined
  }

  lanes.forEach((l, i) => {
    const centre = LANE.head + i * LANE.laneH + LANE.caption + LANE.nodeH / 2
    put(l.agent, centre)
    if (l.local) put(l.local, centre)
  })
  const middle = mean(lanes.map((l) => l.agent.id)) ?? LANE.head + LANE.nodeH
  const reached = new Set(lanes.flatMap((l) => l.path.map((e) => e.id)))
  // "Problems only" leaves out the shared parts no kept lane goes through, unless they are a problem of their own.
  const shown = (e: PlatformEntity) => !opts.problemsOnly || reached.has(e.id) || (isBad(e.status) && !into.has(e.id))

  // Regional operators, each as high as the middle of what sends to it, pushed down where two would touch; one nothing shown sends to goes last.
  let floor = -Infinity
  const regional = model.entities.filter((e) => e.kind === 'regional' && shown(e)).map((e) => ({ e, want: mean(into.get(e.id) ?? []) })).sort((a, b) => (a.want ?? Infinity) - (b.want ?? Infinity))
  for (const { e, want } of regional) {
    const centre = Math.max(want ?? floor + LANE.nodeH + LANE.minGap, floor + LANE.nodeH + LANE.minGap)
    put(e, centre)
    floor = centre
  }
  const central = model.entities.find((e) => e.kind === 'central' && shown(e))
  if (central) put(central, mean(into.get(central.id) ?? []) ?? middle)
  const fusion = model.entities.find((e) => e.kind === 'fusion' && shown(e))
  if (fusion) put(fusion, central ? centreOf(central.id) : mean(into.get(fusion.id) ?? []) ?? middle)

  const hops: LaneHop[] = []
  for (const h of model.edges) {
    const a = placed.get(h.from)
    const b = placed.get(h.to)
    if (!a || !b) continue
    const x1 = a.x + LANE.nodeW
    const y1 = a.y + LANE.nodeH / 2
    const y2 = b.y + LANE.nodeH / 2
    const xm = (x1 + b.x) / 2
    hops.push({
      id: h.id, from: h.from, to: h.to, status: h.status, age: h.age, flowing: h.status === 'healthy',
      d: y1 === y2 ? `M${x1} ${y1}H${b.x}` : `M${x1} ${y1}C${xm} ${y1} ${xm} ${y2} ${b.x} ${y2}`,
      mid: { x: xm, y: (y1 + y2) / 2 },
    })
  }

  const bottom = Math.max(LANE.head + lanes.length * LANE.laneH, ...nodes.map((n) => n.y + LANE.nodeH + LANE.minGap))
  return {
    width: LANE.padX * 2 + LANE_COLUMNS.length * LANE.nodeW + (LANE_COLUMNS.length - 1) * LANE.colGap,
    height: bottom,
    columns: LANE_COLUMNS.map((c) => ({ ...c, x: colX(c.kind) })),
    lanes: lanes.map((l, i) => ({ clusterId: l.agent.clusterId!, name: l.name, y: LANE.head + i * LANE.laneH })),
    nodes,
    hops,
  }
}

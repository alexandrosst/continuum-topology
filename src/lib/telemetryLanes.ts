/**
 * The Telemetry tab of the topology: the platform (see platformLayer.ts) as a grid. One row per cluster, left to right in the order
 * data travels - the cluster, its Discovery agent, its Local operator - and then the shared parts: Regional operators, the Central
 * operator and FUSION, each drawn once, on the row nearest the middle of what sends to it, so every box sits on the same grid.
 * Lines are orthogonal: each runs along the gutters between the columns, never across a box, and merges into one trunk per receiver.
 * Plain data in, plain coordinates out; the component only draws them.
 */
import { PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformKind, type PlatformModel, type PlatformStatus } from './platformLayer'

/** One spacing scale: boxes 48 high on a 64 pitch (a 16 gap), a cluster's name column 128 wide, boxes 156 wide; the gutters give where there is room. */
export const LANE = { nodeW: 156, nodeH: 48, pitch: 64, maxPitch: 80, head: 40, nameW: 128, nameGap: 8, agentGap: 64, fusionGap: 48, minGap: 16, gutterMin: 56, gutterMax: 120, corner: 8 } as const

export const LANE_COLUMNS: { kind: PlatformKind; label: string }[] = [
  { kind: 'agent', label: 'Discovery agent' },
  { kind: 'local', label: 'Local operator' },
  { kind: 'regional', label: 'Regional operator' },
  { kind: 'central', label: 'Central operator' },
  { kind: 'fusion', label: 'FUSION' },
]

export interface LaneNode {
  entity: PlatformEntity
  x: number
  y: number
  /** How many parts shown here send to it: "3 clusters" under a regional operator. */
  senders: number
}
export interface LaneHop {
  id: string
  from: string
  to: string
  /** The sender's state. */
  status: PlatformStatus
  /** When the last data crossed it ("27 s"), when something knows. */
  age?: string
  /** SVG path, from the sender's right edge to the receiver's left edge: straight, or square with rounded corners. */
  d: string
  /** The small arrow at the receiver's end. */
  tip: string
  /** Where its label sits: the middle of its longest straight run. */
  mid: { x: number; y: number }
  /** Only a healthy hop has data moving on it. */
  flowing: boolean
}
export interface LaneLayout {
  width: number
  height: number
  nodeW: number
  columns: { kind: PlatformKind; label: string; x: number }[]
  /** `y` is the middle of the row, which is where its boxes are centred. */
  lanes: { clusterId: string; name: string; y: number }[]
  nodes: LaneNode[]
  hops: LaneHop[]
}
export interface LaneOptions {
  problemsOnly?: boolean
  /** The room there is: the gutters between the columns (not the boxes) take what the boxes leave, up to a limit; rows take a little more height. */
  width?: number
  height?: number
}

const isBad = (s: PlatformStatus) => s === 'attention' || s === 'down'

/** What a screen reader hears for a part: its name and cluster, its state, and the sentence that says what that means. */
export function nodeLabel(e: PlatformEntity): string {
  const state = e.off ? 'Not turned on' : PLATFORM_STATUS_WORD[e.status]
  return `${e.name}${e.clusterName ? `, ${e.clusterName}` : ''}: ${state}${e.sentence && !e.off ? `. ${e.sentence}` : ''}`
}

/** A hop is as bad as the thing that sends: the label of a hop is only worth showing when something is wrong with it. */
export const hopNeedsLabel = (h: Pick<LaneHop, 'status' | 'age'>) => !!h.age && h.status !== 'healthy'

type Pt = [number, number]
const r1 = (n: number) => Math.round(n * 10) / 10

/** A path through the points, square, with each corner rounded as far as its two straight runs allow (at most `corner`). */
export function roundedPath(pts: Pt[], corner: number = LANE.corner): string {
  if (pts.length === 2 && pts[0][1] === pts[1][1]) return `M${r1(pts[0][0])} ${r1(pts[0][1])}H${r1(pts[1][0])}`
  let d = `M${r1(pts[0][0])} ${r1(pts[0][1])}`
  for (let i = 1; i < pts.length - 1; i++) {
    const [p, c, n] = [pts[i - 1], pts[i], pts[i + 1]]
    const a = Math.hypot(c[0] - p[0], c[1] - p[1])
    const b = Math.hypot(n[0] - c[0], n[1] - c[1])
    const r = Math.min(corner, a / 2, b / 2)
    d += `L${r1(c[0] - ((c[0] - p[0]) / a) * r)} ${r1(c[1] - ((c[1] - p[1]) / a) * r)}Q${r1(c[0])} ${r1(c[1])} ${r1(c[0] + ((n[0] - c[0]) / b) * r)} ${r1(c[1] + ((n[1] - c[1]) / b) * r)}`
  }
  const last = pts[pts.length - 1]
  return `${d}${pts.length > 1 ? `L${r1(last[0])} ${r1(last[1])}` : ''}`
}

/** The middle of the longest straight run of a path given as points, so a label sits on a line and not on a corner. */
function middleOf(pts: Pt[]): { x: number; y: number } {
  let best = { len: -1, x: pts[0][0], y: pts[0][1] }
  for (let i = 1; i < pts.length; i++) {
    const len = Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1])
    if (len > best.len) best = { len, x: (pts[i][0] + pts[i - 1][0]) / 2, y: (pts[i][1] + pts[i - 1][1]) / 2 }
  }
  return { x: r1(best.x), y: r1(best.y) }
}

/**
 * Places the platform on the grid. A row is a cluster with a Discovery agent; "problems only" keeps the rows where something on the way
 * to FUSION needs attention or is not working (the shared parts it goes through included), and the shared parts those rows reach.
 */
export function layoutLanes(model: PlatformModel, opts: LaneOptions = {}): LaneLayout {
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
      // Rows that send to the same regional operator sit together, so it can stand level with them.
      const rank = Math.min(...(local ? out.get(local.id) ?? [] : []).map((id) => regionalIndex.get(id) ?? Infinity), Infinity)
      return { agent, local, path, rank, name: agent.clusterName ?? agent.clusterId! }
    })
    .sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name))
  if (opts.problemsOnly) lanes = lanes.filter((l) => l.path.some((e) => isBad(e.status)))

  // Columns: the gutters between them take the room left over, so the grid fills the space it is given without stretching a box. With no
  // central operator (FUSION off) there is no such column, so FUSION stands next to the regional operators instead of behind a gap.
  const cols = LANE_COLUMNS.filter((c) => c.kind !== 'central' || model.entities.some((e) => e.kind === 'central'))
  const COL = new Map(cols.map((c, i) => [c.kind, i]))
  const withCentral = cols.length === LANE_COLUMNS.length
  const fixed = LANE.nameW + LANE.nameGap + cols.length * LANE.nodeW + LANE.agentGap + (withCentral ? LANE.fusionGap : 0)
  const open = cols.length - 1 - (withCentral ? 2 : 1) // the gutters that give
  const gutter = Math.round(Math.min(LANE.gutterMax, Math.max(LANE.gutterMin, ((opts.width ?? 0) - fixed) / open)))
  const gapAfter = (i: number) => (cols[i].kind === 'agent' ? LANE.agentGap : cols[i + 1].kind === 'fusion' && cols[i].kind === 'central' ? LANE.fusionGap : gutter)
  const colLeft: number[] = []
  cols.forEach((_, i) => colLeft.push(i === 0 ? LANE.nameW + LANE.nameGap : colLeft[i - 1] + LANE.nodeW + gapAfter(i - 1)))
  const colX = (kind: PlatformKind) => colLeft[COL.get(kind)!]
  const width = colLeft[colLeft.length - 1] + LANE.nodeW

  // Rows: a little taller when the space allows, never shorter than the pitch of the scale.
  const pitch = opts.height && lanes.length > 0 ? Math.max(LANE.pitch, Math.min(LANE.maxPitch, Math.floor((opts.height - LANE.head - 24) / lanes.length))) : LANE.pitch
  const rowY = (i: number) => LANE.head + i * pitch + LANE.nodeH / 2 + (pitch - LANE.pitch) / 2
  const rowOf = (y: number) => Math.max(0, Math.round((y - rowY(0)) / pitch))

  const nodes: LaneNode[] = []
  const placed = new Map<string, LaneNode>()
  const put = (entity: PlatformEntity, centre: number) => {
    const n = { entity, x: colX(entity.kind), y: centre - LANE.nodeH / 2, senders: (into.get(entity.id) ?? []).filter((id) => placed.has(id)).length }
    nodes.push(n)
    placed.set(entity.id, n)
  }
  const centreOf = (id: string) => placed.get(id)!.y + LANE.nodeH / 2
  const mean = (ids: string[]) => {
    const ys = ids.filter((id) => placed.has(id)).map(centreOf)
    return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : undefined
  }

  lanes.forEach((l, i) => {
    put(l.agent, rowY(i))
    if (l.local) put(l.local, rowY(i))
  })
  const reached = new Set(lanes.flatMap((l) => l.path.map((e) => e.id)))
  // "Problems only" leaves out the shared parts no kept row goes through, unless they are a problem of their own.
  const shown = (e: PlatformEntity) => !opts.problemsOnly || reached.has(e.id) || (isBad(e.status) && !into.has(e.id))

  // Shared parts sit on the row nearest the middle of what sends to them, and move down a row where two would share one.
  let nextFree = 0
  const regional = model.entities.filter((e) => e.kind === 'regional' && shown(e)).map((e) => ({ e, want: mean(into.get(e.id) ?? []) })).sort((a, b) => (a.want ?? Infinity) - (b.want ?? Infinity))
  for (const { e, want } of regional) {
    const row = Math.max(nextFree, want === undefined ? nextFree : rowOf(want))
    put(e, rowY(row))
    nextFree = row + 1
  }
  const middle = lanes.length ? rowY(Math.floor((lanes.length - 1) / 2)) : rowY(0)
  const central = model.entities.find((e) => e.kind === 'central' && shown(e))
  if (central) put(central, rowY(rowOf(mean(into.get(central.id) ?? []) ?? middle)))
  const fusion = model.entities.find((e) => e.kind === 'fusion' && shown(e))
  if (fusion) put(fusion, central ? centreOf(central.id) : rowY(rowOf(mean(into.get(fusion.id) ?? []) ?? middle)))

  // Each receiver gets its own trunk in the gutter in front of it, spread across the gutter so two trunks never lie on one another.
  const trunk = new Map<string, number>()
  for (let col = 2; col < cols.length; col++) {
    const receivers = nodes.filter((n) => COL.get(n.entity.kind) === col && (into.get(n.entity.id) ?? []).some((id) => placed.has(id))).sort((a, b) => a.y - b.y)
    receivers.forEach((n, i) => trunk.set(n.entity.id, colLeft[col] - gapAfter(col - 1) + (gapAfter(col - 1) * (i + 1)) / (receivers.length + 1)))
  }
  // A run across columns in between must clear their boxes: the nearest height to the sender's that none of them is on.
  const clearY = (between: number[], y: number) => {
    const boxes = nodes.filter((n) => between.includes(COL.get(n.entity.kind)!)).map((n) => [n.y - LANE.minGap / 2, n.y + LANE.nodeH + LANE.minGap / 2])
    const hit = boxes.find(([a, b]) => y >= a && y <= b)
    return hit ? (y - hit[0] < hit[1] - y ? hit[0] : hit[1]) : y
  }

  const hops: LaneHop[] = []
  for (const h of model.edges) {
    const a = placed.get(h.from)
    const b = placed.get(h.to)
    if (!a || !b) continue
    const [ca, cb] = [COL.get(a.entity.kind)!, COL.get(b.entity.kind)!]
    const x1 = a.x + LANE.nodeW
    const y1 = a.y + LANE.nodeH / 2
    const y2 = b.y + LANE.nodeH / 2
    let pts: Pt[]
    if (y1 === y2 && cb === ca + 1) pts = [[x1, y1], [b.x, y2]]
    else {
      // From the sender along its row to the trunk, along the trunk to the receiver's row (by a clear row if columns lie between), in.
      const bx = trunk.get(h.to) ?? (x1 + b.x) / 2
      const skipped = Array.from({ length: Math.max(0, cb - ca - 1) }, (_, i) => ca + 1 + i)
      const yc = skipped.length ? clearY(skipped, y1) : y2
      pts = skipped.length ? [[x1, y1], [(x1 + colLeft[ca + 1]) / 2, y1], [(x1 + colLeft[ca + 1]) / 2, yc], [bx, yc], [bx, y2], [b.x, y2]] : [[x1, y1], [bx, y1], [bx, y2], [b.x, y2]]
      pts = pts.filter((p, i) => i === 0 || p[0] !== pts[i - 1][0] || p[1] !== pts[i - 1][1])
    }
    hops.push({
      id: h.id, from: h.from, to: h.to, status: h.status, age: h.age, flowing: h.status === 'healthy',
      d: roundedPath(pts),
      tip: `M${r1(b.x - 5)} ${r1(y2 - 3.5)}L${r1(b.x)} ${r1(y2)}L${r1(b.x - 5)} ${r1(y2 + 3.5)}`,
      mid: middleOf(pts),
    })
  }

  return {
    width,
    height: Math.max(LANE.head + lanes.length * pitch, ...nodes.map((n) => n.y + LANE.nodeH + LANE.minGap)) + 8,
    nodeW: LANE.nodeW,
    columns: cols.map((c) => ({ ...c, x: colX(c.kind) })),
    lanes: lanes.map((l, i) => ({ clusterId: l.agent.clusterId!, name: l.name, y: rowY(i) })),
    nodes,
    hops,
  }
}

/**
 * The Telemetry tab of the topology: the platform (see platformLayer.ts) as lanes. One lane per cluster - a tinted band with the cluster's
 * name on top - and left to right the order data travels: Discovery agent, Local operator, Regional operator, Central operator, FUSION. The
 * regional operators, the central one and FUSION are shared: each is drawn once, on the row nearest the middle of what sends to it.
 * Lines are smooth curves that leave a box by its right edge and arrive at the left edge of the next, floating along that edge: where two
 * lines share an edge, each takes its own place on it, in the order of the boxes at their other ends (so lines never cross at a box), and
 * a line that skips a column runs straight through the gap between that column's boxes. Plain data in, plain coordinates out.
 */
import { PLATFORM_STATUS_WORD, platformFacts, type PlatformEntity, type PlatformKind, type PlatformModel, type PlatformStatus } from './platformLayer'

/** One scale: boxes 192 x 68, a lane's band 22 for its name above the row and 6 below it, bands 8 apart; the gutters between columns give where there is room. */
export const LANE = { nodeW: 192, nodeH: 68, head: 40, caption: 22, bandBelow: 6, bandGap: 8, padX: 8, gutterMin: 40, gutterMax: 96, minGap: 16, edgeMargin: 12, edgeStep: 10 } as const
const BAND_H = LANE.caption + LANE.nodeH + LANE.bandBelow
const PITCH = BAND_H + LANE.bandGap

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
  /** SVG path, from the sender's right edge to the receiver's left edge: a straight line, or a smooth curve. */
  d: string
  /** The small arrow at the receiver's end, on the box's edge. */
  tip: string
  /** Where its label sits: the middle of the curve (or of the first, when it skips a column). */
  mid: { x: number; y: number }
  /** Only a healthy hop has data moving on it. */
  flowing: boolean
}
export interface LaneLayout {
  width: number
  height: number
  nodeW: number
  columns: { kind: PlatformKind; label: string; x: number }[]
  /** `top` and `height` are the lane's band; `y` is the middle of its row, which is where its boxes are centred. */
  lanes: { clusterId: string; name: string; top: number; height: number; y: number }[]
  nodes: LaneNode[]
  hops: LaneHop[]
}
export interface LaneOptions {
  problemsOnly?: boolean
  /** The room there is: the gutters between the columns (not the boxes) take what the boxes leave, up to a limit. */
  width?: number
}

const isBad = (s: PlatformStatus) => s === 'attention' || s === 'down'

/** What a screen reader hears for a part: its name and cluster, its state, the sentence that says what that means, and what it carries and where it sends. */
export function nodeLabel(e: PlatformEntity): string {
  const state = e.off ? 'Not turned on' : PLATFORM_STATUS_WORD[e.status]
  const facts = platformFacts(e)
  return `${e.name}${e.clusterName ? `, ${e.clusterName}` : ''}: ${state}${e.sentence && !e.off ? `. ${e.sentence}` : ''}${facts ? ` ${facts}` : ''}`
}

/** A hop is as bad as the thing that sends: the label of a hop is only worth showing when something is wrong with it. */
export const hopNeedsLabel = (h: Pick<LaneHop, 'status' | 'age'>) => !!h.age && h.status !== 'healthy'

const r1 = (n: number) => Math.round(n * 10) / 10

/** The way on from (x1, y1) to (x2, y2): a straight run when level, else a smooth curve, level where it leaves and where it arrives. */
const run = (x1: number, y1: number, x2: number, y2: number) => {
  const m = (x1 + x2) / 2
  return Math.abs(y1 - y2) < 0.5 ? `H${r1(x2)}` : `C${r1(m)} ${r1(y1)} ${r1(m)} ${r1(y2)} ${r1(x2)} ${r1(y2)}`
}
/** A line from one box's edge to the next. */
export const curve = (x1: number, y1: number, x2: number, y2: number) => `M${r1(x1)} ${r1(y1)}${run(x1, y1, x2, y2)}`

/**
 * Places the platform in lanes. A lane is a cluster with a Discovery agent; "problems only" keeps the lanes where something on the way
 * to FUSION needs attention or is not working (the shared parts it goes through included), and the shared parts those lanes reach.
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
      // Lanes that send to the same regional operator sit together, so it can stand level with them.
      const rank = Math.min(...(local ? out.get(local.id) ?? [] : []).map((id) => regionalIndex.get(id) ?? Infinity), Infinity)
      return { agent, local, path, rank, name: agent.clusterName ?? agent.clusterId! }
    })
    .sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name))
  if (opts.problemsOnly) lanes = lanes.filter((l) => l.path.some((e) => isBad(e.status)))

  // Columns: the gutters between them take the room left over, so the lanes fill the space they are given without stretching a box. With no
  // central operator (FUSION off) there is no such column, so FUSION stands next to the regional operators instead of behind a gap.
  const cols = LANE_COLUMNS.filter((c) => c.kind !== 'central' || model.entities.some((e) => e.kind === 'central'))
  const COL = new Map(cols.map((c, i) => [c.kind, i]))
  const gutter = Math.round(Math.min(LANE.gutterMax, Math.max(LANE.gutterMin, ((opts.width ?? 0) - 2 * LANE.padX - cols.length * LANE.nodeW) / (cols.length - 1))))
  const colLeft = cols.map((_, i) => LANE.padX + i * (LANE.nodeW + gutter))
  const colX = (kind: PlatformKind) => colLeft[COL.get(kind)!]
  const width = colLeft[colLeft.length - 1] + LANE.nodeW + LANE.padX

  // Rows: a band each, its boxes on the line under the name.
  const rowY = (i: number) => LANE.head + i * PITCH + LANE.caption + LANE.nodeH / 2
  const rowOf = (y: number) => Math.max(0, Math.round((y - rowY(0)) / PITCH))

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
  // "Problems only" leaves out the shared parts no kept lane goes through, unless they are a problem of their own.
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

  // Where a line meets a box: it floats along the edge towards the box at its other end, and two lines on one edge keep apart, in the order of
  // those boxes, so each arrow can be told from the next and they never cross there. Alone, a line to a box on the same row is level.
  const wired = model.edges.filter((h) => placed.has(h.from) && placed.has(h.to))
  const slot = new Map<string, { out: number; in: number }>()
  const spread = (ends: { id: string; other: number }[], mine: number): Map<string, number> => {
    const room = LANE.nodeH / 2 - LANE.edgeMargin
    const sorted = [...ends].sort((a, b) => a.other - b.other || a.id.localeCompare(b.id))
    const at = sorted.map((e) => Math.max(-room, Math.min(room, (e.other - mine) * 0.12)))
    for (let i = 1; i < at.length; i++) at[i] = Math.max(at[i], at[i - 1] + LANE.edgeStep)
    const over = at.length ? Math.max(0, at[at.length - 1] - room) : 0
    return new Map(sorted.map((e, i) => [e.id, mine + at[i] - over]))
  }
  for (const n of nodes) {
    const mine = n.y + LANE.nodeH / 2
    const outs = spread(wired.filter((h) => h.from === n.entity.id).map((h) => ({ id: h.id, other: centreOf(h.to) })), mine)
    const ins = spread(wired.filter((h) => h.to === n.entity.id).map((h) => ({ id: h.id, other: centreOf(h.from) })), mine)
    outs.forEach((y, id) => slot.set(id, { out: y, in: slot.get(id)?.in ?? 0 }))
    ins.forEach((y, id) => slot.set(id, { in: y, out: slot.get(id)?.out ?? 0 }))
  }
  // A run across columns in between must clear their boxes: the nearest height to the sender's that none of them is on.
  const clearY = (between: number[], y: number) => {
    const boxes = nodes.filter((n) => between.includes(COL.get(n.entity.kind)!)).map((n) => [n.y - LANE.minGap / 2, n.y + LANE.nodeH + LANE.minGap / 2])
    const hit = boxes.find(([a, b]) => y >= a && y <= b)
    return hit ? (y - hit[0] < hit[1] - y ? hit[0] : hit[1]) : y
  }

  const hops: LaneHop[] = []
  for (const h of wired) {
    const a = placed.get(h.from)!
    const b = placed.get(h.to)!
    const [ca, cb] = [COL.get(a.entity.kind)!, COL.get(b.entity.kind)!]
    const x1 = a.x + LANE.nodeW
    const y1 = slot.get(h.id)!.out
    const y2 = slot.get(h.id)!.in
    const skipped = Array.from({ length: Math.max(0, cb - ca - 1) }, (_, i) => ca + 1 + i)
    let d: string
    let mid: { x: number; y: number }
    if (skipped.length === 0) {
      d = curve(x1, y1, b.x, y2)
      mid = { x: (x1 + b.x) / 2, y: (y1 + y2) / 2 }
    } else {
      // Out along a curve to the gap the skipped column leaves between its boxes, straight through it, and in along a curve.
      const yc = clearY(skipped, y1)
      const xs = colLeft[skipped[0]]
      const xe = colLeft[skipped[skipped.length - 1]] + LANE.nodeW
      d = `${curve(x1, y1, xs, yc)}H${r1(xe)}${run(xe, yc, b.x, y2)}`
      mid = { x: (x1 + xs) / 2, y: (y1 + yc) / 2 }
    }
    hops.push({
      id: h.id, from: h.from, to: h.to, status: h.status, age: h.age, flowing: h.status === 'healthy',
      d,
      tip: `M${r1(b.x - 5)} ${r1(y2 - 3.5)}L${r1(b.x)} ${r1(y2)}L${r1(b.x - 5)} ${r1(y2 + 3.5)}`,
      mid: { x: r1(mid.x), y: r1(mid.y) },
    })
  }

  return {
    width,
    height: Math.max(LANE.head + lanes.length * PITCH, ...nodes.map((n) => n.y + LANE.nodeH + LANE.bandBelow)) + 8,
    nodeW: LANE.nodeW,
    columns: cols.map((c) => ({ ...c, x: colX(c.kind) })),
    lanes: lanes.map((l, i) => ({ clusterId: l.agent.clusterId!, name: l.name, top: LANE.head + i * PITCH, height: BAND_H, y: rowY(i) })),
    nodes,
    hops,
  }
}

import { BaseEdge, useStore, type EdgeProps, type InternalNode } from '@xyflow/react'
import { createContext, useContext } from 'react'
import { HEADER, NS_HEADER, type TopoEdge } from '@/lib/graph'
import { DETOUR_STUB, detourRoute, polylineMidpoint } from './detour'
import { orthoRoute } from './ortho'

/** A node's absolute (canvas-space, parent offsets already applied) bounding box, or null while React Flow
 *  hasn't measured it yet (the very first render or two after it mounts). */
function boxOf(n: InternalNode | undefined): { x: number; y: number; w: number; h: number } | null {
  if (!n?.measured.width || !n.measured.height) return null
  return { x: n.internals.positionAbsolute.x, y: n.internals.positionAbsolute.y, w: n.measured.width, h: n.measured.height }
}

/** Where the straight line from `box`'s center to `toward` crosses `box`'s own rectangular edge - the
 * standard "floating edge" rectangle-intersection formula (React Flow's own docs use this exact one): treat
 * the box as a unit square centered at its own center, transform `toward` into that square's coordinate
 * space, normalize to whichever axis is furthest from center (that's the side the line actually exits
 * through), then transform back. Give it two equal centers (a degenerate, zero-length line - two boxes
 * exactly on top of each other) and it would divide by zero; that can't happen here since it's only ever
 * called with two distinct nodes' centers.
 */
export function intersection(box: { x: number; y: number; w: number; h: number }, toward: { x: number; y: number }): { x: number; y: number } {
  const halfW = box.w / 2
  const halfH = box.h / 2
  const cx = box.x + halfW
  const cy = box.y + halfH
  const dx = (toward.x - cx) / (2 * halfW) - (toward.y - cy) / (2 * halfH)
  const dy = (toward.x - cx) / (2 * halfW) + (toward.y - cy) / (2 * halfH)
  const scale = 1 / (Math.abs(dx) + Math.abs(dy) || 1)
  const nx = dx * scale
  const ny = dy * scale
  return { x: cx + halfW * (nx + ny), y: cy + halfH * (-nx + ny) }
}

/** Which side of `box` the boundary point `p` sits on, as `box`'s own outward-facing unit normal there:
 *  (0,-1) top, (0,1) bottom, (-1,0) left, (1,0) right. `p` is assumed to already be a point on `box`'s own
 *  perimeter - `intersection()`'s own return value, in practice - so this just asks which of the 4 edges
 *  it's closest to rather than re-deriving anything. Used to give curvedPath's tangent at the source end a
 *  "flows straight out of the box" direction instead of pointing wherever the target happens to be - see
 *  curvedPath's own doc comment for the problem that solves. */
export function outwardNormal(box: { x: number; y: number; w: number; h: number }, p: { x: number; y: number }): { x: number; y: number } {
  const distLeft = Math.abs(p.x - box.x)
  const distRight = Math.abs(p.x - (box.x + box.w))
  const distTop = Math.abs(p.y - box.y)
  const distBottom = Math.abs(p.y - (box.y + box.h))
  const min = Math.min(distLeft, distRight, distTop, distBottom)
  if (min === distLeft) return { x: -1, y: 0 }
  if (min === distRight) return { x: 1, y: 0 }
  if (min === distTop) return { x: 0, y: -1 }
  return { x: 0, y: 1 }
}

/** A box `curvedPath` should route its curve clear of - any OTHER card (not this edge's own source or
 *  target) whose own boundary the bow might otherwise sweep through on the way between them. Passed in
 *  by OffsetEdge, which is the one with a live view of every node on the canvas; curvedPath itself stays a
 *  pure function of whatever obstacles it's handed, so it's still testable without rendering anything. */
export type PathObstacle = { x: number; y: number; w: number; h: number; soft?: boolean }

/** How round a squared route's corners are (px): a tidy wiring diagram, not a curve. */
export const SQUARE_CORNER_RADIUS = 12

// Least recently used goes first: a drag changes the key of the few lines near the dragged card on every frame, and the
// untouched lines must keep their entries (read below re-inserts a hit) instead of aging out with them. The cap has to
// cover every line on the canvas, or one pass over them all evicts each entry before it is read again (SquareRoutes sizes it).
const orthoCache = new Map<string, Pt[] | null>()
let orthoCap = 400
export const orthoCacheCapFor = (edgeCount: number) => Math.max(400, edgeCount * 3)
export function setOrthoCacheCap(n: number) {
  orthoCap = n
}
export function cachedOrtho(p1: Pt, n1: Pt, p2: Pt, n2: Pt, obstacles: PathObstacle[]) {
  const hard = obstacles.filter((o) => !o.soft)
  const soft = obstacles.filter((o) => o.soft)
  const r = (v: number) => Math.round(v * 2) / 2
  const key = [p1.x, p1.y, n1.x, n1.y, p2.x, p2.y, n2.x, n2.y, ...hard.flatMap((o) => [o.x, o.y, o.w, o.h]), -99999, ...soft.flatMap((o) => [o.x, o.y, o.w, o.h])].map(r).join(',')
  if (orthoCache.has(key)) {
    const hit = orthoCache.get(key)!
    orthoCache.delete(key)
    orthoCache.set(key, hit)
    return hit
  }
  const route = orthoRoute(p1, n1, p2, n2, hard, soft)
  while (orthoCache.size >= orthoCap) orthoCache.delete(orthoCache.keys().next().value!)
  orthoCache.set(key, route)
  return route
}

/** How round a detour's turns are (px): enough to read as a route, not a wiring diagram. */
const DETOUR_CORNER_RADIUS = 40

// A detour is a search over the boxes' corners, and an edge is redrawn on every frame of a drag or a zoom while its
// geometry has not changed, so the answer for the same geometry is kept (the last few hundred).
const detourCache = new Map<string, ReturnType<typeof detourRoute>>()
function cachedDetour(p1: { x: number; y: number }, n1: { x: number; y: number }, p2: { x: number; y: number }, n2: { x: number; y: number }, obstacles: PathObstacle[]) {
  const r = (v: number) => Math.round(v * 2) / 2
  const key = [p1.x, p1.y, n1.x, n1.y, p2.x, p2.y, n2.x, n2.y, ...obstacles.flatMap((o) => [o.x, o.y, o.w, o.h])].map(r).join(',')
  if (detourCache.has(key)) return detourCache.get(key)!
  const route = detourRoute(p1, n1, p2, n2, obstacles)
  if (detourCache.size >= 300) detourCache.delete(detourCache.keys().next().value!)
  detourCache.set(key, route)
  return route
}

/** The path, and label position, for a line between two anchors that each sit on a box's side: it leaves the source
 *  straight out along the side's normal and arrives at the target straight in along its normal, so the arrowhead
 *  always meets a box square-on, and the two ends are free to slide along the sides (the caller computes them from
 *  where the other end is). Between a short straight leg at each end the line is one cubic Bezier whose control
 *  points follow those same normals: an S when the ends are offset, almost straight when they are not.
 *
 *  When that curve would run through a box (`obstacles`: every card, group and namespace box it must not cross, the
 *  ends' own containers excluded), the line is routed round it instead: straight segments between the corners of the
 *  boxes in the way (see detour.ts), joined with rounded turns so it still reads as one line, and still leaving and
 *  arriving square. A curve that clears everything keeps its shape; the anchors never move.
 *
 *  `sourceNormal`/`targetNormal` are unit vectors pointing out of their boxes. Pure, so it is testable without
 *  rendering anything. */
export function curvedPath(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  sourceNormal: { x: number; y: number },
  targetNormal: { x: number; y: number },
  obstacles: PathObstacle[] = [],
): { path: string; labelX: number; labelY: number } {
  const dist = Math.hypot(x2 - x1, y2 - y1) || 1
  // A short edge must not have legs longer than itself.
  const leg = Math.min(DETOUR_STUB, dist * 0.22)
  const s1 = { x: x1 + sourceNormal.x * leg, y: y1 + sourceNormal.y * leg }
  const s2 = { x: x2 + targetNormal.x * leg, y: y2 + targetNormal.y * leg }
  const span = Math.hypot(s2.x - s1.x, s2.y - s1.y)

  const build = (scale: number) => {
    const reach = Math.min(Math.max(span * 0.42 * scale, 16), 150 * scale)
    return {
      c1: { x: s1.x + sourceNormal.x * reach, y: s1.y + sourceNormal.y * reach },
      c2: { x: s2.x + targetNormal.x * reach, y: s2.y + targetNormal.y * reach },
    }
  }
  const at = (c: { c1: { x: number; y: number }; c2: { x: number; y: number } }, t: number) => {
    const mt = 1 - t
    return {
      x: mt * mt * mt * s1.x + 3 * mt * mt * t * c.c1.x + 3 * mt * t * t * c.c2.x + t * t * t * s2.x,
      y: mt * mt * mt * s1.y + 3 * mt * mt * t * c.c1.y + 3 * mt * t * t * c.c2.y + t * t * t * s2.y,
    }
  }
  // How many sample points of a candidate curve fall inside any obstacle. 40 samples keep a narrow graze from slipping
  // between two of them.
  const hits = (c: ReturnType<typeof build>) => {
    if (obstacles.length === 0) return 0
    let n = 0
    for (let i = 0; i <= 40; i++) {
      const p = at(c, i / 40)
      for (const b of obstacles) if (p.x > b.x && p.x < b.x + b.w && p.y > b.y && p.y < b.y + b.h) n++
    }
    return n
  }
  const curve = (c: ReturnType<typeof build>) => {
    const mid = at(c, 0.5)
    return { path: `M${x1},${y1} L${s1.x},${s1.y} C${c.c1.x},${c.c1.y} ${c.c2.x},${c.c2.y} ${s2.x},${s2.y} L${x2},${y2}`, labelX: mid.x, labelY: mid.y }
  }

  let best = build(1)
  let bestHits = hits(best)
  if (bestHits === 0) return curve(best)
  // Something is in the way: go round it if there is a way, through the gaps between boxes. The route keeps this
  // line's own legs, so it still leaves and arrives square.
  const route = cachedDetour({ x: x1, y: y1 }, sourceNormal, { x: x2, y: y2 }, targetNormal, obstacles)
  if (route) {
    const mid = polylineMidpoint(route)
    return { path: roundedPolylinePath(route, DETOUR_CORNER_RADIUS), labelX: mid.x, labelY: mid.y }
  }
  // No way round (an end boxed in, or too many boxes to search): the longer sweep that collides least.
  for (const scale of [1.6, 2.4]) {
    const c = build(scale)
    const n = hits(c)
    if (n < bestHits) {
      best = c
      bestHits = n
    }
  }
  return curve(best)
}

/** A polyline through `points`, with each interior corner rounded off by `radius` (clamped to at most half
 *  of whichever adjoining segment is shorter, so a tight elbow never overshoots into an adjacent corner or
 *  past the line's own endpoints). Built from straight `L` segments that stop `radius` short of each corner
 *  and a `Q` quadratic through the corner itself - the standard "rounded polyline" construction, kept as its
 *  own pure function (same reasoning as curvedPath below: testable without rendering anything, and reusable
 *  for any n-point route, such as the ones SquareRoutes builds). */
export function roundedPolylinePath(points: { x: number; y: number }[], radius: number): string {
  if (points.length < 2) return ''
  let d = `M${points[0].x},${points[0].y}`
  for (let i = 1; i < points.length - 1; i++) {
    const prev = points[i - 1]
    const curr = points[i]
    const next = points[i + 1]
    const d1 = Math.hypot(curr.x - prev.x, curr.y - prev.y)
    const d2 = Math.hypot(next.x - curr.x, next.y - curr.y)
    const r = Math.min(radius, d1 / 2, d2 / 2)
    const t1 = d1 ? r / d1 : 0
    const t2 = d2 ? r / d2 : 0
    const p1 = { x: curr.x + (prev.x - curr.x) * t1, y: curr.y + (prev.y - curr.y) * t1 }
    const p2 = { x: curr.x + (next.x - curr.x) * t2, y: curr.y + (next.y - curr.y) * t2 }
    d += ` L${p1.x},${p1.y} Q${curr.x},${curr.y} ${p2.x},${p2.y}`
  }
  const last = points[points.length - 1]
  d += ` L${last.x},${last.y}`
  return d
}

type Pt = { x: number; y: number }

/** Moves the line's two ends off their boxes by `gap` px, straight out along each box's own normal, so the gap is
 *  the same square-on distance wherever on a side the end sits. The source end never has an arrowhead in this app -
 *  dependencies only point one way - so its plain line-end always gets pulled back off whatever box it would
 *  otherwise run flush into. The target end only needs the same treatment when it has nothing pointed to place there
 *  instead (an aggregated edge, with no markerEnd - see graph.ts's buildGraph): a real arrowhead's own tip is
 *  already exactly on (tx,ty) by construction (its SVG marker's refX=0 puts the tip at the path's own endpoint), so
 *  pulling the line back there too would just open a visible gap between the tip and the boundary it is meant to
 *  touch. Pure, for the same reason as `intersection` and `curvedPath`: testable without rendering anything. */
export function pullBackEnds(
  s: { x: number; y: number },
  sourceNormal: { x: number; y: number },
  t: { x: number; y: number },
  targetNormal: { x: number; y: number },
  gap: number,
  hasTargetMarker: boolean,
): { sx: number; sy: number; tx: number; ty: number } {
  return {
    sx: s.x + sourceNormal.x * gap,
    sy: s.y + sourceNormal.y * gap,
    tx: hasTargetMarker ? t.x : t.x + targetNormal.x * gap,
    ty: hasTargetMarker ? t.y : t.y + targetNormal.y * gap,
  }
}

/** Where on `box`'s `side` a line toward `toward` should touch: level with it, so the end keeps sliding along the
 *  side as the other end moves, but kept `inset` px in from the corners so a line never leaves from the very corner
 *  of a rounded box. */
export function slideOnSide(box: PathObstacle, side: 'top' | 'bottom' | 'left' | 'right', toward: { x: number; y: number }, inset = SIDE_INSET): { x: number; y: number } {
  const clamp = (v: number, lo: number, hi: number) => (hi < lo ? (lo + hi) / 2 : Math.min(hi, Math.max(lo, v)))
  if (side === 'top' || side === 'bottom') return { x: clamp(toward.x, box.x + inset, box.x + box.w - inset), y: side === 'top' ? box.y : box.y + box.h }
  return { x: side === 'left' ? box.x : box.x + box.w, y: clamp(toward.y, box.y + inset, box.y + box.h - inset) }
}

/** How far in from a corner (px) an end may sit on a side. */
export const SIDE_INSET = 12

/** `p`, a point on the perimeter of `box`, moved along its side to be at least `SIDE_INSET` from the corners. */
export function insetFromCorners(box: PathObstacle, p: { x: number; y: number }, normal: { x: number; y: number }): { x: number; y: number } {
  const clamp = (v: number, lo: number, hi: number) => (hi < lo ? (lo + hi) / 2 : Math.min(hi, Math.max(lo, v)))
  if (normal.y !== 0) return { x: clamp(p.x, box.x + SIDE_INSET, box.x + box.w - SIDE_INSET), y: p.y }
  return { x: p.x, y: clamp(p.y, box.y + SIDE_INSET, box.y + box.h - SIDE_INSET) }
}

/** How far past the straight-line span between an edge's two anchors to still consider a card a possible
 *  obstacle for it - generous enough to cover the widest bow curvedPath's own escalation ever tries (see
 *  its doc comment), and reused as pickClearSide's own "is this side's approach corridor clear" depth. */
export const OBSTACLE_MARGIN = 160

/** How much clear room in front of a side (px) a line needs to arrive there: its straight leg, the turn out of the
 *  curve, and a little air. The line arrives square-on, so it needs a short run in front of the side, not the wide
 *  cone an inclined arrowhead needed. */
export const ARRIVAL_DEPTH = 40

/** The 4 cardinal sides a box can be entered from, and the boundary point/outward-normal `intersection`/
 *  `outwardNormal` would compute for each - used only as a fallback (see `pickClearSide` below) when the
 *  side those two would naturally pick is one curvedPath's own bow escalation can't route around. */
const CARDINAL_SIDES: { side: 'top' | 'bottom' | 'left' | 'right'; normal: { x: number; y: number } }[] = [
  { side: 'top', normal: { x: 0, y: -1 } },
  { side: 'bottom', normal: { x: 0, y: 1 } },
  { side: 'left', normal: { x: -1, y: 0 } },
  { side: 'right', normal: { x: 1, y: 0 } },
]

export function sideMidpoint(box: PathObstacle, side: 'top' | 'bottom' | 'left' | 'right'): { x: number; y: number } {
  if (side === 'top') return { x: box.x + box.w / 2, y: box.y }
  if (side === 'bottom') return { x: box.x + box.w / 2, y: box.y + box.h }
  if (side === 'left') return { x: box.x, y: box.y + box.h / 2 }
  return { x: box.x + box.w, y: box.y + box.h / 2 }
}

/** Whether the open corridor directly in front of `box`'s given `side` - the strip an arrowhead has to
 *  approach through, since curvedPath's own angle clamp keeps it within ~38 degrees of straight-on (see
 *  MAX_ARROWHEAD_APPROACH_ANGLE) - is free of every obstacle, out to `depth` px. */
export function sideIsClear(box: PathObstacle, side: 'top' | 'bottom' | 'left' | 'right', obstacles: PathObstacle[], depth: number): boolean {
  const rect =
    side === 'top'
      ? { x: box.x, y: box.y - depth, w: box.w, h: depth }
      : side === 'bottom'
        ? { x: box.x, y: box.y + box.h, w: box.w, h: depth }
        : side === 'left'
          ? { x: box.x - depth, y: box.y, w: depth, h: box.h }
          : { x: box.x + box.w, y: box.y, w: depth, h: box.h }
  return !obstacles.some((o) => o.x < rect.x + rect.w && o.x + o.w > rect.x && o.y < rect.y + rect.h && o.y + o.h > rect.y)
}

/** Where an edge should touch `box`, and the outward-facing normal there, given `natural` - the point/
 *  normal `intersection()`/`outwardNormal()` already computed from the straight line to the other end's
 *  center, which is what almost every edge should still use unchanged (it's what keeps the touch point
 *  varying continuously around the box's perimeter rather than snapping to one of 4 spots, per
 *  curvedPath/OffsetEdge's own design - see their doc comments).
 *
 *  The exception `obstacles` exists for: a box tucked directly behind another one, on the one side an edge
 *  would naturally enter from - two device cards stacked with barely more gap between them than the
 *  arrowhead's own approach cone needs, say. curvedPath's bow escalation (see its own doc comment) can
 *  route the *middle* of a curve around a nearby obstacle, but not the last ~38-degree-wide stretch right
 *  before the tip - that part is anchored to whichever single side the touch point sits on. When that
 *  side's own approach corridor is blocked, no amount of bowing the middle of the curve fixes it, so this
 *  instead falls back to entering from a different side entirely - only when the natural one is genuinely
 *  obstructed, and preferring the two sides perpendicular to it (typically the ones with the most spare
 *  room in a packed grid) before trying the opposite side. */
export function pickClearSide(
  box: PathObstacle,
  natural: { point: { x: number; y: number }; normal: { x: number; y: number } },
  obstacles: PathObstacle[],
  toward?: { x: number; y: number },
): { point: { x: number; y: number }; normal: { x: number; y: number } } {
  if (obstacles.length === 0) return natural
  const naturalSide = CARDINAL_SIDES.find((s) => s.normal.x === natural.normal.x && s.normal.y === natural.normal.y)?.side ?? 'top'
  const depth = ARRIVAL_DEPTH
  if (sideIsClear(box, naturalSide, obstacles, depth)) return natural
  const perpendicular = naturalSide === 'top' || naturalSide === 'bottom' ? (['left', 'right'] as const) : (['top', 'bottom'] as const)
  const opposite: 'top' | 'bottom' | 'left' | 'right' =
    naturalSide === 'top' ? 'bottom' : naturalSide === 'bottom' ? 'top' : naturalSide === 'left' ? 'right' : 'left'
  for (const side of [...perpendicular, opposite]) {
    if (sideIsClear(box, side, obstacles, depth)) {
      const normal = CARDINAL_SIDES.find((s) => s.side === side)!.normal
      return { point: toward ? slideOnSide(box, side, toward) : sideMidpoint(box, side), normal }
    }
  }
  // Every side is obstructed - no fallback helps, so keep the natural one (curvedPath's own bow escalation
  // still gets a chance at it, and picks whichever candidate collides least even then).
  return natural
}

/** The shape `collectObstacles` needs from `nodeLookup` - just enough of React Flow's own `InternalNode`
 *  to find each node's type, parent and live absolute box, without importing its full internal type. */
type ObstacleCandidate = { type?: string; parentId?: string; measured: { width?: number; height?: number }; internals: { positionAbsolute: { x: number; y: number } } }

/** How far below its cluster's header (px) a card may sit and still count as being in the cluster's first row. */
const FIRST_ROW_SLACK = 20
/** How much clear room a side needs to leave from (px): enough to get past a neighbouring card and out into the gap,
 *  not the long corridor an arrowhead needs to arrive along. */
const SIDE_EXIT_DEPTH = 36

/**
 * A card in the first row of its cluster sits right under the cluster's header (its name, chips, load meters and
 * "N services"), so a line that leaves or arrives through the top of that card, on its way to somewhere above or
 * beside, runs straight across that text. When that is where `natural` puts it, take the nearer free side instead,
 * the one toward the other end first, so the line goes out into the gap beside the card and round, not over the
 * words. Only when such a side has room; otherwise the natural one stays (and the line may cross the header).
 * Anything but a top exit from a first-row card is returned unchanged.
 */
export function avoidHeaderSide(
  box: PathObstacle,
  parent: PathObstacle | undefined,
  natural: { point: { x: number; y: number }; normal: { x: number; y: number } },
  toward: { x: number; y: number },
  obstacles: PathObstacle[],
): { point: { x: number; y: number }; normal: { x: number; y: number } } {
  if (!parent || natural.normal.x !== 0 || natural.normal.y !== -1) return natural
  if (box.y - (parent.y + HEADER) > FIRST_ROW_SLACK) return natural
  const sides = toward.x >= box.x + box.w / 2 ? (['right', 'left'] as const) : (['left', 'right'] as const)
  for (const side of sides) {
    if (sideIsClear(box, side, obstacles, SIDE_EXIT_DEPTH)) return { point: slideOnSide(box, side, toward), normal: CARDINAL_SIDES.find((c) => c.side === side)!.normal }
  }
  return natural
}

/** The cluster or group box a node sits directly in, when it is one. */
function clusterBoxOf(nodeLookup: Map<string, ObstacleCandidate>, id: string): (PathObstacle & { id: string }) | undefined {
  const parentId = nodeLookup.get(id)?.parentId
  const p = parentId === undefined ? undefined : nodeLookup.get(parentId)
  if (!p || parentId === undefined || p.type !== 'boundary' || !p.measured.width || !p.measured.height) return undefined
  return { id: parentId, x: p.internals.positionAbsolute.x, y: p.internals.positionAbsolute.y, w: p.measured.width, h: p.measured.height }
}

/** Every node in `nodeLookup` that should count as a routing obstacle for the edge from `source` to
 *  `target`, restricted to `bounds` (the same generous margin-expanded box around both ends OffsetEdge
 *  already builds, reused here so this never has to re-derive it) - pulled out of OffsetEdge's own body so
 *  it's testable with a plain Map, the same reasoning as curvedPath/pickClearSide above.
 *
 *  Three node types ever qualify: 'card' (an ordinary service/machine card - the only type the obstacle
 *  list covered before this), and 'boundary'/'namespace' (the cluster/tier and namespace group BOXES
 *  themselves - the big background rectangles cards sit inside). Leaving those two out was the actual gap
 *  behind "the arrow passes through other entities": a bow between two cards in different groups had
 *  nothing stopping it from sweeping straight across a third, unrelated group's visible box, since that
 *  box was never in the obstacle list at all - only the individual cards inside it were.
 *
 *  Including every 'boundary'/'namespace' box unconditionally would be wrong, though: the group (and, for a
 *  namespaced card, the namespace box inside it) that CONTAINS the source or the target is not a real
 *  obstacle - the edge necessarily starts or ends inside it, so its own box would always register as a
 *  "hit" right at the anchor point, for every single cross-group edge. `skip` is exactly that container
 *  chain - every ancestor of `source` and of `target`, walked up via `parentId` - excluded so only a
 *  sibling or unrelated group/namespace box (one neither end is actually inside) ever counts. */
export function collectObstacles(
  nodeLookup: Map<string, ObstacleCandidate>,
  source: string,
  target: string,
  bounds: { minX: number; maxX: number; minY: number; maxY: number },
  /** When true a cluster or namespace box is not a wall: only its header strip (the text) is an obstacle, so a line may
   *  cross the empty space between the cards inside it. */
  through = false,
  /** Also return each such box whole, flagged `soft`, for a router that wants to keep off its border. */
  soft = false,
): PathObstacle[] {
  const ancestorsOf = (id: string): Set<string> => {
    const out = new Set<string>()
    let cur = nodeLookup.get(id)
    while (cur?.parentId !== undefined) {
      out.add(cur.parentId)
      cur = nodeLookup.get(cur.parentId)
    }
    return out
  }
  const skip = ancestorsOf(source)
  for (const id of ancestorsOf(target)) skip.add(id)
  const obstacles: PathObstacle[] = []
  for (const [nodeId, n] of nodeLookup) {
    if (nodeId === source || nodeId === target || skip.has(nodeId)) continue
    if (n.type !== 'card' && n.type !== 'boundary' && n.type !== 'namespace') continue
    const w = n.measured.width
    const h = n.measured.height
    if (!w || !h) continue
    const bx = n.internals.positionAbsolute.x
    const by = n.internals.positionAbsolute.y
    if (!(bx < bounds.maxX && bx + w > bounds.minX && by < bounds.maxY && by + h > bounds.minY)) continue
    if (through && n.type !== 'card') {
      obstacles.push({ x: bx, y: by, w, h: Math.min(h, n.type === 'boundary' ? HEADER : NS_HEADER) })
      if (soft) obstacles.push({ x: bx, y: by, w, h, soft: true })
    }
    else obstacles.push({ x: bx, y: by, w, h })
  }
  return obstacles
}


/**
 * A line like the default one, with its source end moved sideways by `data.sourceOffset` pixels and its
 * target end by `data.targetOffset`, independently. Equal values give the old parallel shift (two lines
 * between the same pair of boxes, kept apart along their whole length); different values let a line fan
 * out from a busy node while still landing cleanly at a quiet one on the other end.
 *
 * The anchor points themselves are computed fresh every render from each node's live, current geometry
 * (`useInternalNode`, React Flow's own "floating edges" pattern - see `intersection` above), not from the
 * `sourceX/Y`/`targetX/Y` props React Flow derives from whichever fixed handle side `pickSides` guessed at
 * build time (graph.ts). That guess only ever gets 4 candidate points per box (one per cardinal side, each
 * pinned to that side's exact midpoint) and is never revisited after a card moves relative to its neighbors
 * - dragged within a chain layout, say - so the line kept leaving from a point that no longer made
 * geometric sense, reading as unnecessarily steep. Recomputing the true boundary intersection of the line
 * between both nodes' actual current centers, every render, fixes both problems at once: the exit point
 * varies continuously around each box's perimeter instead of snapping to one of 4 spots, and it tracks a
 * drag live rather than waiting for the next full graph rebuild. Falls back to React Flow's given
 * `sourceX/Y`/`targetX/Y` on the rare render where a node hasn't been measured yet (mount, or a brand new
 * node this exact frame) - a plausible instant, not-yet-perfect placement beats no edge at all.
 *
 * Builds its own cubic-curve path rather than calling React Flow's `getStraightPath` or `getBezierPath`,
 * for the reason the original version of this component already established for the straight-line
 * predecessor of this one: `getBezierPath` needs a `sourcePosition`/`targetPosition` (one of 4 cardinal
 * values) to place its control points, and an SVG marker's `orient="auto"` arrowhead rotation derives from
 * the path's own local tangent - so a `getBezierPath` curve's arrowhead can only ever snap to one of 4 fixed
 * angles, never the real, continuous direction between two live points. Hand-building a `C` (cubic Bezier)
 * path from the same continuously-computed anchor points sidesteps that limitation entirely at the target
 * end: `orient="auto"` follows whatever tangent the path actually has there, cardinal or not, so the tip
 * still lands exactly on `intersection()`'s boundary point with the approach angle free to curve. The source
 * end deliberately does the opposite - see curvedPath's own doc comment for why it leaves along the box's
 * own outward normal (one of only 4 directions, since a rectangle only has 4 sides) instead of a continuous
 * angle: there's no arrowhead there for a cardinal-ish angle to look wrong on, and "flows straight out of the
 * box" reads better than "points at wherever the target happens to be" when nothing is there to justify it.
 */
/** How far past the straight-line span between an edge's two anchors to still consider a card a possible
 *  obstacle for it - generous enough to cover the widest bow curvedPath's own escalation ever tries (see
 *  its doc comment), so nothing that could plausibly end up under the curve gets missed, while still
 *  skipping the large majority of an even moderately busy canvas that's nowhere near this one edge's route. */
/** Everything a line needs to be routed, worked out from the live node boxes: the anchors on each box's boundary (sliding
 *  along the side, fanned by the edge's offsets), the outward normals there, and the boxes in the way. */
export type EdgeLayout = {
  sourceBox: PathObstacle
  targetBox: PathObstacle
  p1: { x: number; y: number }
  n1: { x: number; y: number }
  p2: { x: number; y: number }
  n2: { x: number; y: number }
  obstacles: PathObstacle[]
  soft: PathObstacle[]
}

export function layoutEdge(
  nodeLookup: Map<string, InternalNode>,
  source: string,
  target: string,
  sourceOffset: number,
  targetOffset: number,
  /** The squared style: a cluster or namespace box is not a wall (only its header is), and its border is reported as `soft`. */
  squared: boolean,
): EdgeLayout | null {
  const sourceBox = boxOf(nodeLookup.get(source))
  const targetBox = boxOf(nodeLookup.get(target))
  if (!sourceBox || !targetBox) return null
  // A generous box around both ends' own boxes, not just the eventual touch points - the one obstacle list is reused for
  // pickClearSide (which runs before the touch points are final) and for routing (which runs after).
  const minX = Math.min(sourceBox.x, targetBox.x) - OBSTACLE_MARGIN
  const maxX = Math.max(sourceBox.x + sourceBox.w, targetBox.x + targetBox.w) + OBSTACLE_MARGIN
  const minY = Math.min(sourceBox.y, targetBox.y) - OBSTACLE_MARGIN
  const maxY = Math.max(sourceBox.y + sourceBox.h, targetBox.y + targetBox.h) + OBSTACLE_MARGIN
  const all = collectObstacles(nodeLookup, source, target, { minX, maxX, minY, maxY }, squared, squared)
  const obstacles = all.filter((o) => !o.soft)
  const soft = all.filter((o) => o.soft)

  const targetCenter = { x: targetBox.x + targetBox.w / 2, y: targetBox.y + targetBox.h / 2 }
  const sourceCenter = { x: sourceBox.x + sourceBox.w / 2, y: sourceBox.y + sourceBox.h / 2 }
  const from = intersection(sourceBox, targetCenter)
  const naturalTo = intersection(targetBox, sourceCenter)
  // Each end first takes the side the straight line to the other end would use, then slides along it as the other end
  // moves. A target whose natural side is blocked gets another (pickClearSide), and a first-row card in a cluster keeps
  // its line off the cluster's header text by using a side instead of its top (avoidHeaderSide); not when both ends sit in
  // the same cluster, as the line then never has to cross that header. Whichever side wins, the line meets the box
  // square-on, along that side's normal.
  const sourceCluster = clusterBoxOf(nodeLookup, source)
  const targetCluster = clusterBoxOf(nodeLookup, target)
  const sameCluster = sourceCluster !== undefined && sourceCluster.id === targetCluster?.id
  const sourceNatural = { point: from, normal: outwardNormal(sourceBox, from) }
  const fromSide = sameCluster ? sourceNatural : avoidHeaderSide(sourceBox, sourceCluster, sourceNatural, targetCenter, obstacles)
  const picked = pickClearSide(targetBox, { point: naturalTo, normal: outwardNormal(targetBox, naturalTo) }, obstacles, sourceCenter)
  const chosen = sameCluster ? picked : avoidHeaderSide(targetBox, targetCluster, picked, sourceCenter, obstacles)
  const n1 = fromSide.normal
  const n2 = chosen.normal
  const a = insetFromCorners(sourceBox, fromSide.point, n1)
  const b = insetFromCorners(targetBox, chosen.point, n2)

  // A fan of lines between the same two boxes is spread along the boxes' own sides, not across the line, so an end never
  // slides off its box.
  const spread = (p: { x: number; y: number }, normal: { x: number; y: number }, amount: number) => {
    if (!amount) return p
    const tangent = { x: -normal.y, y: normal.x }
    const sign = tangent.x * (b.y - a.y) * -1 + tangent.y * (b.x - a.x) >= 0 ? 1 : -1 // the same side of the line as before
    return { x: p.x + tangent.x * sign * amount, y: p.y + tangent.y * sign * amount }
  }
  return { sourceBox, targetBox, p1: spread(a, n1, sourceOffset), n1, p2: spread(b, n2, targetOffset), n2, obstacles, soft }
}

/** React Flow keeps `nodeLookup` as one Map it edits in place, so reading it never re-renders anything by itself. This is a
 *  string that changes exactly when some node moves or is resized, to subscribe to alongside it. */
export function useNodeGeometryKey(): string {
  return useStore((s) => {
    let k = ''
    for (const n of s.nodeLookup.values()) k += `${n.id}:${n.internals.positionAbsolute.x},${n.internals.positionAbsolute.y},${n.measured.width ?? 0},${n.measured.height ?? 0}${n.dragging ? 'd' : ''};`
    return k
  })
}

/** A squared line already routed together with all the others (see SquareRoutes.tsx), by edge id. */
export type RoutedPath = { path: string; labelX: number; labelY: number }
export const SquareRoutesContext = createContext<Map<string, RoutedPath> | null>(null)

/**
 * A line between two boxes, with its source end moved sideways by `data.sourceOffset` pixels and its target end by
 * `data.targetOffset`, independently (equal values keep two lines between the same pair parallel; different values fan one
 * out of a busy node while it still lands cleanly at a quiet one).
 *
 * The anchors are computed fresh every render from each node's live geometry (React Flow's own "floating edges" pattern),
 * not from the fixed handle sides the graph build guessed at, so an end tracks a drag live and slides along its box side.
 * The line is hand-built rather than drawn with `getBezierPath` so it can leave and arrive square-on (see curvedPath), and
 * the arrowhead (an SVG marker, `orient="auto"`) follows whatever tangent the path really has.
 *
 * In the squared style the line comes from SquareRoutesContext: every line is routed together, so parallel lines can be
 * spaced apart. Without it (the curved fallback, or before the first measure) the line is routed on its own.
 */
export function OffsetEdge(props: EdgeProps<TopoEdge>) {
  // The squared style is the normal case and needs nothing from the store, so it must not subscribe to anything that
  // changes on a pan or a node moving: that would run an O(nodes) selector per edge on every frame. Only the fallback
  // (an edge SquareRoutes could not lay out yet) reads the live geometry.
  const routed = useContext(SquareRoutesContext)?.get(props.id)
  return routed ? <EdgeLine {...props} result={routed} /> : <UnroutedOffsetEdge {...props} />
}

function EdgeLine({ id, result, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge> & { result: RoutedPath }) {
  return (
    <BaseEdge
      id={id}
      path={result.path}
      labelX={result.labelX}
      labelY={result.labelY}
      label={label}
      labelStyle={labelStyle}
      labelShowBg={labelShowBg}
      labelBgStyle={labelBgStyle}
      labelBgPadding={labelBgPadding}
      labelBgBorderRadius={labelBgBorderRadius}
      markerEnd={markerEnd}
      style={style}
      interactionWidth={interactionWidth}
    />
  )
}

function UnroutedOffsetEdge(props: EdgeProps<TopoEdge>) {
  const { source, target, sourceX, sourceY, targetX, targetY, data, markerEnd } = props
  // Every other card on the canvas, live. Read reactively (not just this edge's own two ends), so every edge re-routes on
  // any node moving; cheap at this app's scale, and it keeps a dragged card's already-avoiding edges from lagging.
  const nodeLookup = useStore((s) => s.nodeLookup)
  useNodeGeometryKey()
  // Only the zoom matters here; useViewport() would also re-render the line on every pan.
  const zoom = useStore((s) => s.transform[2])

  const layout = layoutEdge(nodeLookup, source, target, data?.sourceOffset ?? 0, data?.targetOffset ?? 0, false)
  let p1 = { x: sourceX, y: sourceY }
  let p2 = { x: targetX, y: targetY }
  // "Up" is a reasonable guess for the very first render or two before a node is measured.
  let n1 = { x: 0, y: -1 }
  let n2 = { x: 0, y: -1 }
  let obstacles: PathObstacle[] = []
  if (layout) {
    p1 = layout.p1
    p2 = layout.p2
    n1 = layout.n1
    n2 = layout.n2
    obstacles = layout.obstacles
  }
  // The bare line-end itself should never be the thing touching a box - the only contact is meant to be an arrowhead's
  // own tip. The ends sit exactly ON each box's boundary; pullBackEnds shrinks that back off by GAP along the normal at
  // whichever end(s) have no arrowhead tip (see its doc comment). GAP is a *screen* size, converted to flow space by
  // the zoom, so it reads the same fit-to-view zoomed out as it does close up.
  const pulled = pullBackEnds(p1, n1, p2, n2, 5 / zoom, !!markerEnd)
  // The line must not run across its own two boxes either.
  const result = curvedPath(pulled.sx, pulled.sy, pulled.tx, pulled.ty, n1, n2, layout ? [...obstacles, layout.sourceBox, layout.targetBox] : obstacles)
  return <EdgeLine {...props} result={result} />
}

export const edgeTypes = { offset: OffsetEdge }

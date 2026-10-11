/**
 * Routing a line around boxes it cannot bow past.
 *
 * `curvedPath` already bows a line wider (and to the other side) until it clears the boxes between its two ends.
 * That fails the moment a box sits squarely between them and is wider than any bow can swing: a line from one
 * cluster to another with a third cluster in the row between them. Then the only honest route goes around, through
 * the gaps between boxes. This finds it: a shortest path over the corners of the (slightly grown) boxes, the
 * classic visibility graph, so the line runs along clear channels rather than across a box.
 *
 * Pure functions of the geometry they are given, so they are testable without rendering anything.
 */

export type Pt = { x: number; y: number }
export type Rect = { x: number; y: number; w: number; h: number }

/** Clear space kept between a detour and the box it goes around (px). */
export const DETOUR_MARGIN = 14
/** How far a detour runs straight out of a box before it may turn (px), so it leaves and arrives square. */
export const DETOUR_STUB = 16
/** Past this many boxes in the way, no detour is attempted and the caller keeps its own best effort: the search
 *  grows with the square of the corners, and a canvas this crowded has no clear channel to find anyway. */
export const DETOUR_MAX_OBSTACLES = 40
/** Added to every leg, so of two routes of about the same length the one with fewer turns wins. */
const TURN_COST = 14
/** How much each pixel a turn lies off the straight line between the two ends costs, on top of the length: a route
 *  that stays near the straight line beats a slightly shorter one that swings far out and back. */
const OFF_LINE_COST = 0.6

const EPS = 1e-6

const inflate = (r: Rect, m: number): Rect => ({ x: r.x - m, y: r.y - m, w: r.w + 2 * m, h: r.h + 2 * m })
const strictlyInside = (p: Pt, r: Rect) => p.x > r.x + EPS && p.x < r.x + r.w - EPS && p.y > r.y + EPS && p.y < r.y + r.h - EPS

/** Does the segment a-b pass through the inside of `r`? Running along its edge, or touching a corner, does not count. */
export function segmentCrosses(a: Pt, b: Pt, r: Rect): boolean {
  let t0 = 0
  let t1 = 1
  const dx = b.x - a.x
  const dy = b.y - a.y
  const sides: [number, number][] = [
    [-dx, a.x - r.x],
    [dx, r.x + r.w - a.x],
    [-dy, a.y - r.y],
    [dy, r.y + r.h - a.y],
  ]
  for (const [p, q] of sides) {
    if (Math.abs(p) < EPS) {
      if (q <= EPS) return false // parallel to this side and on or outside it
      continue
    }
    const t = q / p
    if (p < 0) {
      if (t > t1) return false
      if (t > t0) t0 = t
    } else {
      if (t < t0) return false
      if (t < t1) t1 = t
    }
  }
  return t1 - t0 > EPS
}

/**
 * The corners to pass through to get from `p1` (leaving its box along `n1`) to `p2` (arriving along `n2`) without
 * entering any of `obstacles`, or null when there is no way (an end boxed in, or too many boxes to search). The
 * result runs p1, a short stub, the turns, a short stub, p2, with collinear points dropped.
 */
export function detourRoute(p1: Pt, n1: Pt, p2: Pt, n2: Pt, obstacles: Rect[]): Pt[] | null {
  if (obstacles.length === 0 || obstacles.length > DETOUR_MAX_OBSTACLES) return null
  const rects = obstacles.map((o) => inflate(o, DETOUR_MARGIN))
  const s1 = { x: p1.x + n1.x * DETOUR_STUB, y: p1.y + n1.y * DETOUR_STUB }
  const s2 = { x: p2.x + n2.x * DETOUR_STUB, y: p2.y + n2.y * DETOUR_STUB }
  const inAny = (p: Pt) => rects.some((r) => strictlyInside(p, r))
  if (inAny(s1) || inAny(s2)) return null

  const nodes: Pt[] = [s1, s2]
  for (const r of rects) {
    for (const c of [{ x: r.x, y: r.y }, { x: r.x + r.w, y: r.y }, { x: r.x, y: r.y + r.h }, { x: r.x + r.w, y: r.y + r.h }]) {
      if (!inAny(c)) nodes.push(c)
    }
  }
  const visible = (a: Pt, b: Pt) => !rects.some((r) => segmentCrosses(a, b, r))
  const straight = Math.hypot(s2.x - s1.x, s2.y - s1.y) || 1
  const offLine = (p: Pt) => Math.abs((s2.x - s1.x) * (s1.y - p.y) - (s1.x - p.x) * (s2.y - s1.y)) / straight

  // Dijkstra over the corners; the graph is small (four corners a box), so a linear scan for the nearest is plenty.
  const dist = nodes.map(() => Infinity)
  const prev = nodes.map(() => -1)
  const done = nodes.map(() => false)
  dist[0] = 0
  for (;;) {
    let u = -1
    for (let i = 0; i < nodes.length; i++) if (!done[i] && dist[i] < Infinity && (u === -1 || dist[i] < dist[u])) u = i
    if (u === -1 || u === 1) break
    done[u] = true
    for (let v = 0; v < nodes.length; v++) {
      if (done[v]) continue
      const d = dist[u] + Math.hypot(nodes[v].x - nodes[u].x, nodes[v].y - nodes[u].y) + TURN_COST + OFF_LINE_COST * offLine(nodes[v])
      if (d < dist[v] && visible(nodes[u], nodes[v])) {
        dist[v] = d
        prev[v] = u
      }
    }
  }
  if (!Number.isFinite(dist[1])) return null

  const inner: Pt[] = []
  for (let i = 1; i !== -1; i = prev[i]) inner.unshift(nodes[i])
  const pts = [p1, ...inner, p2]
  // Drop a point that lies on the straight line between its neighbours: it is not a turn.
  return pts.filter((p, i) => {
    if (i === 0 || i === pts.length - 1) return true
    const a = pts[i - 1]
    const b = pts[i + 1]
    return Math.abs((p.x - a.x) * (b.y - p.y) - (p.y - a.y) * (b.x - p.x)) > 0.5
  })
}

/** The point halfway along a polyline's length, where a label belongs. */
export function polylineMidpoint(pts: Pt[]): Pt {
  const lens = pts.slice(1).map((p, i) => Math.hypot(p.x - pts[i].x, p.y - pts[i].y))
  let left = lens.reduce((a, b) => a + b, 0) / 2
  for (let i = 0; i < lens.length; i++) {
    if (left <= lens[i] || i === lens.length - 1) {
      const t = lens[i] ? left / lens[i] : 0
      return { x: pts[i].x + (pts[i + 1].x - pts[i].x) * t, y: pts[i].y + (pts[i + 1].y - pts[i].y) * t }
    }
    left -= lens[i]
  }
  return pts[0]
}

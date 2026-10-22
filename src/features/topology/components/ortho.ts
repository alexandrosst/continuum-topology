/**
 * Squared ("circuit board") routing for a topology line.
 *
 * A line leaves its box along the box's outward normal, runs horizontally and vertically only, and arrives along the
 * target's normal. The route is the cheapest path over a grid made of the boxes' own edges (grown by a margin), the
 * ends' stubs and the middle of every gap between them, so it runs down the clear channels between cards and never
 * through one. Cost is length plus a price per bend, so it takes the fewest turns; of two routes with as many turns it
 * prefers the one that turns near the middle between the two ends, which keeps a plain jog centred.
 *
 * Pure functions of the geometry, so they are testable without rendering anything.
 */

export type Pt = { x: number; y: number }
export type Rect = { x: number; y: number; w: number; h: number }

/** Clear space kept between a route and any box it passes (px). */
export const ORTHO_MARGIN = 12
/** How far a route runs straight out of a box before it may turn (px). */
export const ORTHO_STUB = 18
/** Past this many boxes in the way no search is attempted: the grid grows with the square of the boxes. */
export const ORTHO_MAX_OBSTACLES = 40
/** Added per bend: a route with fewer turns beats a slightly shorter one with more. */
const BEND_COST = 24
/** Cost per px a turn lies away from the middle between the ends (centres a jog; kept small so a box in the way wins). */
const CENTER_COST = 0.1
const EPS = 0.5
/** A route running along (parallel to, within SOFT_BAND of) the border of a `soft` box pays this much extra per px: it
 *  reads as part of the box's frame, so a line should cross the border, not ride it. */
const BORDER_COST = 1.5
const SOFT_BAND = 22

const DIRS: Pt[] = [{ x: 1, y: 0 }, { x: -1, y: 0 }, { x: 0, y: 1 }, { x: 0, y: -1 }]
const dirIndex = (v: Pt) => DIRS.findIndex((d) => d.x === Math.sign(v.x) && d.y === Math.sign(v.y))

const inside = (x: number, y: number, r: Rect) => x > r.x + 1e-6 && x < r.x + r.w - 1e-6 && y > r.y + 1e-6 && y < r.y + r.h - 1e-6

/** Sorted, de-duplicated grid lines with the middle of each gap added. */
function lines(values: number[]): number[] {
  const sorted = [...values].sort((a, b) => a - b)
  const out: number[] = []
  for (const v of sorted) if (out.length === 0 || v - out[out.length - 1] > EPS) out.push(v)
  const withMid: number[] = []
  out.forEach((v, i) => {
    withMid.push(v)
    if (i < out.length - 1 && out[i + 1] - v > 2 * EPS) withMid.push((v + out[i + 1]) / 2)
  })
  return withMid
}

/** A binary min-heap of [cost, state] pairs. */
class Heap {
  private costs: number[] = []
  private items: number[] = []
  get size() {
    return this.items.length
  }
  push(cost: number, item: number) {
    let i = this.items.length
    this.costs.push(cost)
    this.items.push(item)
    while (i > 0) {
      const p = (i - 1) >> 1
      if (this.costs[p] <= this.costs[i]) break
      this.swap(i, p)
      i = p
    }
  }
  pop(): [number, number] {
    const top: [number, number] = [this.costs[0], this.items[0]]
    const lastC = this.costs.pop()!
    const lastI = this.items.pop()!
    if (this.items.length > 0) {
      this.costs[0] = lastC
      this.items[0] = lastI
      let i = 0
      for (;;) {
        const l = 2 * i + 1
        const r = l + 1
        let m = i
        if (l < this.items.length && this.costs[l] < this.costs[m]) m = l
        if (r < this.items.length && this.costs[r] < this.costs[m]) m = r
        if (m === i) break
        this.swap(i, m)
        i = m
      }
    }
    return top
  }
  private swap(a: number, b: number) {
    ;[this.costs[a], this.costs[b]] = [this.costs[b], this.costs[a]]
    ;[this.items[a], this.items[b]] = [this.items[b], this.items[a]]
  }
}

/** Drop points that lie on the straight line between their neighbours: they are not turns. */
function dropCollinear(pts: Pt[]): Pt[] {
  const out: Pt[] = []
  for (const p of pts) {
    const last = out[out.length - 1]
    if (last && Math.abs(last.x - p.x) < 1e-6 && Math.abs(last.y - p.y) < 1e-6) continue
    out.push(p)
  }
  return out.filter((p, i) => {
    if (i === 0 || i === out.length - 1) return true
    const a = out[i - 1]
    const b = out[i + 1]
    return Math.abs((p.x - a.x) * (b.y - p.y) - (p.y - a.y) * (b.x - p.x)) > 0.5
  })
}

/** A route with no search: out of the source, one centred jog, into the target. Used when no clear route exists. */
export function simpleOrthoRoute(p1: Pt, n1: Pt, p2: Pt, n2: Pt): Pt[] {
  const s1 = { x: p1.x + n1.x * ORTHO_STUB, y: p1.y + n1.y * ORTHO_STUB }
  const s2 = { x: p2.x + n2.x * ORTHO_STUB, y: p2.y + n2.y * ORTHO_STUB }
  const horizontalOut = n1.x !== 0
  const mid = horizontalOut ? { x: (s1.x + s2.x) / 2, y: s1.y } : { x: s1.x, y: (s1.y + s2.y) / 2 }
  const mid2 = horizontalOut ? { x: mid.x, y: s2.y } : { x: s2.x, y: mid.y }
  return dropCollinear([p1, s1, mid, mid2, s2, p2])
}

/**
 * The corners of the cheapest squared route from `p1` (leaving its box along `n1`) to `p2` (arriving along `n2`) that
 * stays out of every one of `obstacles`, or null when there is none (an end boxed in, or too many boxes). Both normals
 * must be axis-aligned unit vectors.
 */
export function orthoRoute(p1: Pt, n1: Pt, p2: Pt, n2: Pt, obstacles: Rect[], soft: Rect[] = []): Pt[] | null {
  if (obstacles.length > ORTHO_MAX_OBSTACLES) return null
  const d1 = dirIndex(n1)
  const d2 = dirIndex({ x: -n2.x, y: -n2.y })
  if (d1 < 0 || d2 < 0) return null
  const m = ORTHO_MARGIN
  const rects = obstacles.map((o) => ({ x: o.x - m, y: o.y - m, w: o.w + 2 * m, h: o.h + 2 * m }))
  const s1 = { x: p1.x + n1.x * ORTHO_STUB, y: p1.y + n1.y * ORTHO_STUB }
  const s2 = { x: p2.x + n2.x * ORTHO_STUB, y: p2.y + n2.y * ORTHO_STUB }
  if (rects.some((r) => inside(s1.x, s1.y, r) || inside(s2.x, s2.y, r))) return null

  const midX = (s1.x + s2.x) / 2
  const midY = (s1.y + s2.y) / 2
  const xs = lines([s1.x, s2.x, midX, ...rects.flatMap((r) => [r.x, r.x + r.w]), ...soft.flatMap((r) => [r.x, r.x + r.w])])
  const ys = lines([s1.y, s2.y, midY, ...rects.flatMap((r) => [r.y, r.y + r.h]), ...soft.flatMap((r) => [r.y, r.y + r.h])])
  // Extra cost of a step along a box border (see BORDER_COST): `vertical` for a step in y, which rides a left/right border.
  const riding = (mx: number, my: number, vertical: boolean) =>
    soft.some((r) =>
      vertical
        ? my > r.y && my < r.y + r.h && (Math.abs(mx - r.x) < SOFT_BAND || Math.abs(mx - r.x - r.w) < SOFT_BAND)
        : mx > r.x && mx < r.x + r.w && (Math.abs(my - r.y) < SOFT_BAND || Math.abs(my - r.y - r.h) < SOFT_BAND),
    )
  const nx = xs.length
  const ny = ys.length
  const at = (v: number, arr: number[]) => {
    let best = 0
    for (let i = 1; i < arr.length; i++) if (Math.abs(arr[i] - v) < Math.abs(arr[best] - v)) best = i
    return best
  }
  const si = at(s1.x, xs)
  const sj = at(s1.y, ys)
  const ti = at(s2.x, xs)
  const tj = at(s2.y, ys)

  const blockedNode = new Uint8Array(nx * ny)
  for (let j = 0; j < ny; j++) for (let i = 0; i < nx; i++) if (rects.some((r) => inside(xs[i], ys[j], r))) blockedNode[j * nx + i] = 1
  // Is the step from node (i, j) to its neighbour in direction d through the inside of a box?
  const stepBlocked = (i: number, j: number, i2: number, j2: number) => {
    if (blockedNode[j2 * nx + i2]) return true
    const mx = (xs[i] + xs[i2]) / 2
    const my = (ys[j] + ys[j2]) / 2
    return rects.some((r) => inside(mx, my, r))
  }

  const stateOf = (i: number, j: number, d: number) => (j * nx + i) * 4 + d
  const total = nx * ny * 4
  const dist = new Float64Array(total).fill(Infinity)
  const prev = new Int32Array(total).fill(-1)
  const heap = new Heap()
  const start = stateOf(si, sj, d1)
  const goal = stateOf(ti, tj, d2)
  dist[start] = 0
  heap.push(0, start)
  while (heap.size > 0) {
    const [c, u] = heap.pop()
    if (c > dist[u]) continue
    if (u === goal) break
    const d = u % 4
    const cell = (u - d) / 4
    const i = cell % nx
    const j = (cell - i) / nx
    // Straight on.
    const ni = i + DIRS[d].x
    const nj = j + DIRS[d].y
    if (ni >= 0 && ni < nx && nj >= 0 && nj < ny && !stepBlocked(i, j, ni, nj)) {
      const v = stateOf(ni, nj, d)
      const len = Math.abs(xs[ni] - xs[i]) + Math.abs(ys[nj] - ys[j])
      const extra = soft.length > 0 && riding((xs[i] + xs[ni]) / 2, (ys[j] + ys[nj]) / 2, d >= 2) ? BORDER_COST * len : 0
      const nc = c + len + extra
      if (nc < dist[v]) {
        dist[v] = nc
        prev[v] = u
        heap.push(nc, v)
      }
    }
    // Turn (never back on itself): a new segment starts here.
    const horizontal = d < 2
    for (const nd of horizontal ? [2, 3] : [0, 1]) {
      const v = stateOf(i, j, nd)
      const bias = horizontal ? Math.abs(xs[i] - midX) : Math.abs(ys[j] - midY)
      const nc = c + BEND_COST + CENTER_COST * bias
      if (nc < dist[v]) {
        dist[v] = nc
        prev[v] = u
        heap.push(nc, v)
      }
    }
  }
  if (!Number.isFinite(dist[goal])) return null

  const pts: Pt[] = []
  for (let s = goal; s !== -1; s = prev[s]) {
    const cell = (s - (s % 4)) / 4
    const i = cell % nx
    pts.unshift({ x: xs[i], y: ys[(cell - i) / nx] })
  }
  return dropCollinear([p1, ...pts, p2])
}

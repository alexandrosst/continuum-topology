/**
 * Lines between two boxes that run in the gutters, never across a box. The canvas is rows of boxes with room between them, so a line that
 * leaves one box through the side that faces the other, and travels in the middle of whatever gap is free, never strikes a title through.
 *
 * The route is the cheapest one over a grid whose lines are the gaps between boxes (and the two ends' own centres): length plus a price per
 * turn, so it is as straight as the boxes allow. Pure, so it can be tested without drawing anything.
 */
export type Rect = { x: number; y: number; w: number; h: number }
export type Pt = { x: number; y: number }

/** What a line keeps clear of the boxes it passes (px). */
const CLEAR = 10
/** What one turn costs, in px of extra length: a straighter line beats a shorter one up to this much. */
const TURN = 36
/** A gap narrower than this is not a lane. */
const MIN_GAP = 2 * CLEAR + 4
/** How far a line may leave the canvas's own extent to get round everything, and the room kept inside a box's edge for where it attaches. */
const OUTSIDE = 48
const EDGE_ROOM = 20

/** The middle of every gap between two neighbouring edges on one axis, and the outside of the lot. `nudge` moves a line off the middle of each gap
 *  (never closer than CLEAR to a box), so lines that run in the same gutter can each have a track of their own. */
function lanes(edges: number[], extra: number[], nudge = 0): number[] {
  const sorted = [...new Set(edges.map(Math.round))].sort((a, b) => a - b)
  const out = new Set(extra.map(Math.round))
  for (let i = 1; i < sorted.length; i++) {
    const gap = sorted[i] - sorted[i - 1]
    if (gap < MIN_GAP) continue
    const room = gap / 2 - CLEAR
    out.add(Math.round((sorted[i] + sorted[i - 1]) / 2 + Math.max(-room, Math.min(room, nudge))))
  }
  out.add(sorted[0] - OUTSIDE)
  out.add(sorted[sorted.length - 1] + OUTSIDE)
  return [...out].sort((a, b) => a - b)
}

/** Whether the straight, axis-aligned run from `a` to `b` goes through any of `boxes` (grown by CLEAR). */
function blocked(a: Pt, b: Pt, boxes: Rect[]): boolean {
  const x0 = Math.min(a.x, b.x)
  const x1 = Math.max(a.x, b.x)
  const y0 = Math.min(a.y, b.y)
  const y1 = Math.max(a.y, b.y)
  return boxes.some((r) => x1 > r.x - CLEAR && x0 < r.x + r.w + CLEAR && y1 > r.y - CLEAR && y0 < r.y + r.h + CLEAR)
}

type Side = { point: Pt; normal: Pt }

/** The side of `from` that faces `to`, and where on it a line attaches (`shift` along the side, kept inside its edge). Rows are the canvas's
 *  structure, so boxes that do not overlap vertically face each other top to bottom, otherwise side to side. */
function facing(from: Rect, to: Rect, shift: number): Side {
  const below = to.y >= from.y + from.h
  const above = to.y + to.h <= from.y
  if (below || above) {
    const x = Math.min(from.x + from.w - EDGE_ROOM, Math.max(from.x + EDGE_ROOM, from.x + from.w / 2 + shift))
    return { point: { x, y: below ? from.y + from.h : from.y }, normal: { x: 0, y: below ? 1 : -1 } }
  }
  const right = to.x >= from.x + from.w
  const y = Math.min(from.y + from.h - EDGE_ROOM, Math.max(from.y + EDGE_ROOM, from.y + from.h / 2 + shift))
  return { point: { x: right ? from.x + from.w : from.x, y }, normal: { x: right ? 1 : -1, y: 0 } }
}

/** A binary min-heap of [priority, item]. */
class Heap<T> {
  private a: [number, T][] = []
  get size() { return this.a.length }
  push(p: number, v: T) {
    const a = this.a
    a.push([p, v])
    for (let i = a.length - 1; i > 0; ) {
      const j = (i - 1) >> 1
      if (a[j][0] <= a[i][0]) break
      ;[a[j], a[i]] = [a[i], a[j]]
      i = j
    }
  }
  pop(): [number, T] {
    const a = this.a
    const top = a[0]
    const last = a.pop()!
    if (a.length) {
      a[0] = last
      for (let i = 0; ; ) {
        const l = 2 * i + 1
        const r = l + 1
        let m = i
        if (l < a.length && a[l][0] < a[m][0]) m = l
        if (r < a.length && a[r][0] < a[m][0]) m = r
        if (m === i) break
        ;[a[m], a[i]] = [a[i], a[m]]
        i = m
      }
    }
    return top
  }
}

/**
 * The corners of the line from `from` to `to`: out of the side of `from` that faces `to`, along the gaps between `others`, into the side of
 * `to` that faces `from`. `fromShift`/`toShift` slide where it attaches along those sides (lines that share a side stay apart), and `nudge` slides
 * where it runs in a gutter (lines that share a gutter stay apart). Null when
 * the boxes leave no way through (they touch, or one sits on the other), and the caller draws something simpler.
 */
export function laneRoute(from: Rect, to: Rect, others: Rect[], fromShift = 0, toShift = 0, nudge = 0): Pt[] | null {
  if (from.x < to.x + to.w && to.x < from.x + from.w && from.y < to.y + to.h && to.y < from.y + from.h) return null
  const a = facing(from, to, fromShift)
  const b = facing(to, from, toShift)
  const walls = [...others, from, to]
  const xs = lanes(walls.flatMap((r) => [r.x, r.x + r.w]), [a.point.x, b.point.x], nudge)
  const ys = lanes(walls.flatMap((r) => [r.y, r.y + r.h]), [a.point.y, b.point.y], nudge)
  const at = (i: number, j: number): Pt => ({ x: xs[i], y: ys[j] })
  const ia = xs.indexOf(Math.round(a.point.x))
  const ja = ys.indexOf(Math.round(a.point.y))
  const ib = xs.indexOf(Math.round(b.point.x))
  const jb = ys.indexOf(Math.round(b.point.y))
  // A run that starts or ends on one of the two boxes is only held off by the others: it is the box it leaves.
  const free = (p: Pt, q: Pt, i: number, j: number, ni: number, nj: number) => {
    const touchesA = (i === ia && j === ja) || (ni === ia && nj === ja)
    const touchesB = (i === ib && j === jb) || (ni === ib && nj === jb)
    return !blocked(p, q, touchesA && touchesB ? others : touchesA ? [...others, to] : touchesB ? [...others, from] : walls)
  }

  // State: grid cell and the axis the line arrived along (0 across, 1 down), so a turn can be charged for.
  const key = (i: number, j: number, d: number) => (j * xs.length + i) * 2 + d
  const dist = new Map<number, number>()
  const prev = new Map<number, number>()
  const heap = new Heap<[number, number, number]>()
  const startAxis = a.normal.x === 0 ? 1 : 0
  const endAxis = b.normal.x === 0 ? 1 : 0
  dist.set(key(ia, ja, startAxis), 0)
  heap.push(0, [ia, ja, startAxis])
  let best: number | undefined
  while (heap.size) {
    const [cost, [i, j, d]] = heap.pop()
    const k = key(i, j, d)
    if (cost > (dist.get(k) ?? Infinity)) continue
    if (i === ib && j === jb && d === endAxis) { best = k; break }
    for (const [di, dj] of [[1, 0], [-1, 0], [0, 1], [0, -1]]) {
      const ni = i + di
      const nj = j + dj
      if (ni < 0 || nj < 0 || ni >= xs.length || nj >= ys.length) continue
      const nd = di === 0 ? 1 : 0
      // Out of the first box only along its side's normal, and into the last only along its.
      if (i === ia && j === ja && (nd !== startAxis || Math.sign(di + dj) !== Math.sign(a.normal.x + a.normal.y))) continue
      if (ni === ib && nj === jb && (nd !== endAxis || Math.sign(di + dj) !== -Math.sign(b.normal.x + b.normal.y))) continue
      const p = at(i, j)
      const q = at(ni, nj)
      if (!free(p, q, i, j, ni, nj)) continue
      const c = cost + Math.abs(q.x - p.x) + Math.abs(q.y - p.y) + (nd === d ? 0 : TURN)
      const nk = key(ni, nj, nd)
      if (c < (dist.get(nk) ?? Infinity)) {
        dist.set(nk, c)
        prev.set(nk, k)
        heap.push(c, [ni, nj, nd])
      }
    }
  }
  if (best === undefined) return null
  const cells: Pt[] = []
  for (let k: number | undefined = best; k !== undefined; k = prev.get(k)) {
    const cell = k >> 1
    cells.push(at(cell % xs.length, Math.floor(cell / xs.length)))
  }
  cells.reverse()
  // Drop the points that sit in the middle of a straight run.
  return cells.filter((p, i) => {
    if (i === 0 || i === cells.length - 1) return true
    const o = cells[i - 1]
    const n = cells[i + 1]
    return !((o.x === p.x && p.x === n.x) || (o.y === p.y && p.y === n.y))
  })
}

/** A key that says whether two routes are the same problem, so a route is worked out once however many times a line is redrawn. The boxes that are in
 *  the way count in any order: the same canvas read from a different place in the node list is the same problem. */
export const laneKey = (from: Rect, to: Rect, others: Rect[], fromShift: number, toShift: number, nudge = 0) => {
  const k = (r: Rect) => `${Math.round(r.x)},${Math.round(r.y)},${Math.round(r.w)},${Math.round(r.h)}`
  return [k(from), k(to), ...others.map(k).sort()].join('|') + `|${Math.round(fromShift)}|${Math.round(toShift)}|${Math.round(nudge)}`
}

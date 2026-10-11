/**
 * Keeping squared lines apart.
 *
 * Each line is routed on its own, so two lines that want the same channel run on top of each other, or a hair apart, and
 * read as one. After all of them are routed, this finds the runs (a horizontal or vertical stretch of a line) that
 * overlap or lie closer than LANE_GAP, and fans each such group out into evenly spaced lanes, centred where they were,
 * inside the free space around them. Which line takes which lane follows where it comes from and goes to, so lines that
 * turn the same way stay on the same side and cross as little as they can.
 *
 * Only the bends move: the first and last run of a line (the legs at its two boxes) keep their position, so an end never
 * leaves its box side. Pure functions of the geometry; the work is a sort and a sweep per direction.
 */

export type Pt = { x: number; y: number }
export type Rect = { x: number; y: number; w: number; h: number }
export type Route = { id: string; pts: Pt[] }

/** Distance between two neighbouring lanes (px, flow space). */
export const LANE_GAP = 14
/** Runs closer than this (and overlapping along their length) are one group. */
const CLOSE = LANE_GAP - 0.5
/** Two runs must share at least this much length to count as running together. */
const MIN_SHARED = 3
/** A lane keeps this far from any box. */
const BOX_CLEARANCE = 3
/** A moved run must leave at least this much of the leg that joins it to a box. */
const MIN_LEG = 8

type Run = { r: number; i: number; at: number; lo: number; hi: number; before: number; after: number }

/** Move the horizontal (`horizontal`) or vertical runs of `routes` apart; returns how many runs moved. Mutates the points. */
function spreadRuns(routes: Route[], boxes: Rect[], horizontal: boolean): number {
  // `at` is the coordinate across the run (y for a horizontal one), `lo`..`hi` the extent along it.
  const across = (p: Pt) => (horizontal ? p.y : p.x)
  const along = (p: Pt) => (horizontal ? p.x : p.y)
  const setAcross = (p: Pt, v: number) => {
    if (horizontal) p.y = v
    else p.x = v
  }

  const runs: Run[] = []
  routes.forEach((route, r) => {
    const pts = route.pts
    // Interior runs only: the first and last are the legs at the boxes.
    for (let i = 1; i + 2 < pts.length; i++) {
      const a = pts[i]
      const b = pts[i + 1]
      if (Math.abs(across(a) - across(b)) > 1e-6 || Math.abs(along(a) - along(b)) < 1e-6) continue
      const prev = pts[i - 1]
      const next = pts[i + 2]
      runs.push({
        r,
        i,
        at: across(a),
        lo: Math.min(along(a), along(b)),
        hi: Math.max(along(a), along(b)),
        // Which side of the run the line comes from and goes to (-1 below the coordinate, +1 above... in the run's own axis).
        before: Math.sign(across(prev) - across(a)),
        after: Math.sign(across(next) - across(b)),
      })
    }
  })
  if (runs.length < 2) return 0

  // Union runs that lie close together and share some length.
  const parent = runs.map((_, k) => k)
  const find = (k: number): number => (parent[k] === k ? k : (parent[k] = find(parent[k])))
  const order = runs.map((_, k) => k).sort((a, b) => runs[a].at - runs[b].at)
  for (let x = 0; x < order.length; x++) {
    for (let y = x + 1; y < order.length; y++) {
      const A = runs[order[x]]
      const B = runs[order[y]]
      if (B.at - A.at >= CLOSE) break
      if (A.r === B.r) continue
      if (Math.min(A.hi, B.hi) - Math.max(A.lo, B.lo) >= MIN_SHARED) parent[find(order[y])] = find(order[x])
    }
  }
  const groups = new Map<number, number[]>()
  runs.forEach((_, k) => {
    const g = find(k)
    if (!groups.has(g)) groups.set(g, [])
    groups.get(g)!.push(k)
  })

  let moved = 0
  for (const members of groups.values()) {
    if (members.length < 2) continue
    const group = members.map((k) => runs[k])
    const lo = Math.min(...group.map((g) => g.lo))
    const hi = Math.max(...group.map((g) => g.hi))
    const center = group.reduce((s, g) => s + g.at, 0) / group.length

    // The free band the lanes must stay in: between the nearest boxes either side of the group, and clear of the legs.
    let floor = -Infinity
    let ceil = Infinity
    for (const b of boxes) {
      const bLo = horizontal ? b.x : b.y
      const bHi = bLo + (horizontal ? b.w : b.h)
      if (bHi <= lo || bLo >= hi) continue // not alongside this group
      const near = horizontal ? b.y : b.x
      const far = near + (horizontal ? b.h : b.w)
      if (far <= center + 0.5) floor = Math.max(floor, far + BOX_CLEARANCE)
      else if (near >= center - 0.5) ceil = Math.min(ceil, near - BOX_CLEARANCE)
    }
    for (const g of group) {
      const pts = routes[g.r].pts
      if (g.i === 1) {
        const leg = pts[0]
        const dir = Math.sign(g.at - across(leg)) || 1
        if (dir > 0) floor = Math.max(floor, across(leg) + MIN_LEG)
        else ceil = Math.min(ceil, across(leg) - MIN_LEG)
      }
      if (g.i + 2 === pts.length - 1) {
        const leg = pts[pts.length - 1]
        const dir = Math.sign(g.at - across(leg)) || 1
        if (dir > 0) floor = Math.max(floor, across(leg) + MIN_LEG)
        else ceil = Math.min(ceil, across(leg) - MIN_LEG)
      }
    }
    const room = ceil - floor
    const k = group.length
    // Evenly spaced; squeezed when the channel is narrow, never wider than LANE_GAP.
    const gap = Math.min(LANE_GAP, Number.isFinite(room) ? room / (k + 1) : LANE_GAP)
    if (gap < 3) continue // no real room: leave them as routed
    const half = ((k - 1) * gap) / 2
    const mid = Math.min(Math.max(center, floor + half), ceil - half)

    // Runs that come from and go to the low side take the low lanes; ties keep a stable order by line id.
    const sorted = [...group].sort((a, b) => a.before + a.after - (b.before + b.after) || a.before - b.before || routes[a.r].id.localeCompare(routes[b.r].id))
    sorted.forEach((g, idx) => {
      const v = mid - half + idx * gap
      if (Math.abs(v - g.at) < 1e-6) return
      const pts = routes[g.r].pts
      setAcross(pts[g.i], v)
      setAcross(pts[g.i + 1], v)
      moved++
    })
  }
  return moved
}

/** Space apart the runs of `routes` that overlap or crowd each other. `boxes` are everything a lane must not enter. */
export function nudgeRoutes(routes: Route[], boxes: Rect[]): void {
  spreadRuns(routes, boxes, true)
  spreadRuns(routes, boxes, false)
}

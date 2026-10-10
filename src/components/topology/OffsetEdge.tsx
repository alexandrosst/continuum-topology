import { BaseEdge, EdgeLabelRenderer, useInternalNode, useStore, useViewport, type EdgeProps } from '@xyflow/react'
import clsx from 'clsx'
import { createContext, useContext } from 'react'
import type { TopoEdge } from '@/lib/graph'
import { DetailContext } from '@/lib/detail'
import { focusedBy, useCanvasFocus } from '@/store/canvasFocus'
import { laneKey, laneRoute } from '@/lib/lanes'

/** Which path generator OffsetEdge draws with - 'curved' (the default hand-built bow, see curvedPath below)
 *  or 'elbow' (the opt-in rounded-orthogonal style, see elbowPath below). Read via context rather than a
 *  per-edge data field: it's a single canvas-wide view preference (the Options menu\'s "Edge style" control
 *  in TopologyPage.tsx), not something that varies edge to edge, so there is no reason for every edge object
 *  passing through graph.ts to carry its own copy. TopologyPage wraps its <ReactFlow> in this context\'s
 *  Provider; OffsetEdge (rendered by React Flow for every edge, still within that same tree) reads it back. */
export const EdgeStyleContext = createContext<'curved' | 'elbow'>('curved')

/** A node's absolute (canvas-space, parent offsets already applied) bounding box, or null while React Flow
 *  hasn't measured it yet (the very first render or two after it mounts). */
function boxOf(n: ReturnType<typeof useInternalNode>): { x: number; y: number; w: number; h: number } | null {
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

/** The steepest an arrowhead's own two back corners can be off the tip's straight-in direction before one
 *  of them ends up on the wrong side of the target box's boundary - i.e. before the marker would visibly
 *  poke into the box it's pointing at, rather than just touching it with its tip. React Flow's built-in
 *  `ArrowClosed` marker is a fixed triangle (see its own `points`, "-5,-4 0,0 -5,4 -5,-4", in
 *  @xyflow/react's MarkerSymbols) with a back half-width of 4 and a back length of 5 in its own local
 *  (rotation-relative) space; at approach angle theta off dead-on, the corner on the box's own inward side
 *  sits `5*cos(theta) - 4*sin(theta)` past the tip along the box's own outward normal, which crosses zero -
 *  the corner crosses the boundary - at theta = atan(4/5) ~= 38.66 deg off dead-on, i.e. at
 *  90 - atan(4/5) ~= 51.34 deg off the box's own edge (a near-tangential, grazing approach). Kept with a
 *  margin below that hard limit rather than exactly at it, both so floating-point/zoom rounding near the
 *  boundary can't tip a corner over anyway, and because a somewhat steeper minimum just reads better - an
 *  arrowhead arriving nearly edge-on never looks like it's "pointing at" the box it grazes past.
 */
const MAX_ARROWHEAD_APPROACH_ANGLE = (38 * Math.PI) / 180

/** Rotates unit vector `dir` no more than `maxAngle` (radians) away from unit vector `toward`: returns
 *  `dir` unchanged when it's already within that cone, otherwise the closest vector on the cone's edge -
 *  `toward` itself rotated by exactly `maxAngle`, on whichever side `dir` was on. Used to keep
 *  curvedPath's target-end tangent from ever approaching so close to tangential-to-the-box (see
 *  MAX_ARROWHEAD_APPROACH_ANGLE's own doc comment) that the arrowhead's own back corners would dip inside
 *  the box's boundary, while still letting it vary continuously - not snap to one of 4 cardinal angles -
 *  everywhere short of that limit, which is the whole reason curvedPath treats the target end differently
 *  from the source end in the first place (see curvedPath's own doc comment). */
export function clampTowardNormal(dir: { x: number; y: number }, toward: { x: number; y: number }, maxAngle: number): { x: number; y: number } {
  const cos = Math.min(1, Math.max(-1, dir.x * toward.x + dir.y * toward.y))
  const angle = Math.acos(cos)
  if (angle <= maxAngle) return dir
  const cross = toward.x * dir.y - toward.y * dir.x // >0: dir is counter-clockwise from toward
  const sign = cross >= 0 ? 1 : -1
  const rot = sign * maxAngle
  const cosR = Math.cos(rot)
  const sinR = Math.sin(rot)
  return { x: toward.x * cosR - toward.y * sinR, y: toward.x * sinR + toward.y * cosR }
}

/** A box `curvedPath` should route its curve clear of - any OTHER card (not this edge's own source or
 *  target) whose own boundary the bow might otherwise sweep through on the way between them. Passed in
 *  by OffsetEdge, which is the one with a live view of every node on the canvas; curvedPath itself stays a
 *  pure function of whatever obstacles it's handed, so it's still testable without rendering anything. */
export type PathObstacle = { x: number; y: number; w: number; h: number }

/** The cubic Bezier `C` path, and label position, for a gentle bow between two anchor points - the math half
 *  of OffsetEdge's own curve-building (see its doc comment for why a hand-built curve and not `getBezierPath`
 *  at all), pulled out so it's testable without rendering anything.
 *
 *  Cubic rather than the single-control-point quadratic this used to be: a shared control point gives BOTH
 *  ends a tangent pointing roughly toward each other (nudged sideways by the bow), which looks fine at the
 *  target end but reads badly at the source - an edge leaving a wide box near one corner would leave at a
 *  shallow angle, grazing along the box's own edge for a stretch before actually separating from it, rather
 *  than looking like it flows straight out. The two control points here split that: `c1`, near the source,
 *  is offset from (x1,y1) along `sourceNormal` (the box's own outward-facing direction at that exact exit
 *  point, from `outwardNormal` above) - so the curve always leaves perpendicular-ish to the box it came from,
 *  however off-center that exit point is. `c2`, near the target, is built the way the old shared control
 *  point was (the straight-line midpoint nudged by `nx`/`ny`*bow) - keeping the target end's own tangent
 *  continuous, not snapped to one of 4 cardinal angles, which is what OffsetEdge's own arrowhead design
 *  depends on (see its doc comment) - EXCEPT that tangent is then clamped so it never gets so close to
 *  tangential-to-the-target-box that the arrowhead marker's own fixed-width back corners would dip inside
 *  the box (see MAX_ARROWHEAD_APPROACH_ANGLE and clampTowardNormal below): a shallow enough approach angle
 *  left uncorrected would put part of the marker's shape past the boundary, not just its tip.
 *
 *  That angle clamp only ever looks at the box the arrowhead is actually pointing at - it says nothing
 *  about some OTHER, unrelated card the curve's own bow happens to sweep across on the way there (two
 *  cards packed into the same row with an edge between them, or a distant cross-cluster edge whose natural
 *  bow passes straight over whatever sits in the tier between source and target). `obstacles`, when given,
 *  covers that: after building the curve at the normal bow, if any sampled point along it - not just the
 *  tip - lands inside one of them, the bow is escalated (wider, then the mirrored side) until a candidate
 *  clears everything, or the least-bad one if none fully does. This only ever changes the *shape* of the
 *  bow, never the two anchor points themselves - `x1,y1`/`x2,y2` (and so the tip's own touch point) stay
 *  exactly where the caller put them either way. */
export function curvedPath(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  nx: number,
  ny: number,
  sourceNormal: { x: number; y: number },
  targetNormal: { x: number; y: number },
  obstacles: PathObstacle[] = [],
): { path: string; labelX: number; labelY: number } {
  const segLen = Math.hypot(x2 - x1, y2 - y1) || 1
  const baseBow = Math.min(segLen * 0.12, 36)
  // How far the curve travels straight out from the source before c2 starts pulling it toward the target -
  // proportional to length (a short edge shouldn't get a stub longer than the edge itself) but capped so a
  // long edge doesn't get an oddly long straight run before it starts curving.
  const exit = Math.min(segLen * 0.35, 40)
  const c1x = x1 + sourceNormal.x * exit
  const c1y = y1 + sourceNormal.y * exit

  // Builds the curve's own end control point (c2) for one bow strength ("scale" multiplies baseBow, sign
  // and all - negative bows the curve to the mirrored side), including the arrowhead-approach-angle clamp
  // curvedPath always applied here (see the doc comment above) - so every candidate this tries is already a
  // fully valid curve on its own, not just a raw, unclamped bow.
  function build(scale: number): { mx: number; my: number } {
    let mx = (x1 + x2) / 2 + nx * baseBow * scale
    let my = (y1 + y2) / 2 + ny * baseBow * scale
    const tdx = x2 - mx
    const tdy = y2 - my
    const tlen = Math.hypot(tdx, tdy) || 1
    const naturalDir = { x: tdx / tlen, y: tdy / tlen }
    const inward = { x: -targetNormal.x, y: -targetNormal.y }
    const clamped = clampTowardNormal(naturalDir, inward, MAX_ARROWHEAD_APPROACH_ANGLE)
    if (clamped !== naturalDir) {
      mx = x2 - clamped.x * tlen
      my = y2 - clamped.y * tlen
    }
    return { mx, my }
  }

  // How many of curvedPath's own sample points (excluding the two anchors, which are meant to sit exactly
  // on the source/target boundary) fall inside any obstacle - 0 means this candidate's curve is clear.
  // SAMPLES needs to be high enough that a narrow graze (the curve clipping a box's corner for only a short
  // arc-length) can't slip through between two consecutive samples and get scored as a false 0 - 14 was
  // fine for the roughly-diagonal sweeps this was first tuned against, but a curve that runs close to and
  // nearly parallel with an obstacle edge for a while (seen after dragging cards into tight, non-default
  // layouts) can dip in and out of the box within a much narrower t-window than 1/14 of the curve.
  function hits(mx: number, my: number): number {
    if (obstacles.length === 0) return 0
    let n = 0
    const SAMPLES = 40
    for (let i = 1; i < SAMPLES; i++) {
      const t = i / SAMPLES
      const mt = 1 - t
      const px = mt * mt * mt * x1 + 3 * mt * mt * t * c1x + 3 * mt * t * t * mx + t * t * t * x2
      const py = mt * mt * mt * y1 + 3 * mt * mt * t * c1y + 3 * mt * t * t * my + t * t * t * y2
      for (const b of obstacles) {
        if (px > b.x && px < b.x + b.w && py > b.y && py < b.y + b.h) n++
      }
    }
    return n
  }

  let { mx, my } = build(1)
  if (obstacles.length > 0 && hits(mx, my) > 0) {
    // Escalate the bow until something clears every obstacle: wider on the same side first (the least
    // visually surprising change from the default), then the mirrored side. Picks whichever candidate has
    // the fewest remaining hits (0 if anything manages it), preferring the smallest, same-side change on a
    // tie so an edge that's already fine doesn't visibly jump around as its neighbors move.
    let best = { mx, my, n: hits(mx, my) }
    for (const scale of [1, -1, 1.8, -1.8, 2.6, -2.6, 3.4, -3.4, 4.2, -4.2]) {
      const c = build(scale)
      const n = hits(c.mx, c.my)
      if (n < best.n) best = { mx: c.mx, my: c.my, n }
      if (n === 0) break
    }
    mx = best.mx
    my = best.my
  }

  return {
    path: `M${x1},${y1} C${c1x},${c1y} ${mx},${my} ${x2},${y2}`,
    // Cubic Bezier at t=0.5: (P0 + 3*C1 + 3*C2 + P2) / 8 - the label sits along the actual curve, not the
    // straight-line midpoint, so it doesn't appear to float off to one side of a strongly bowed edge.
    labelX: (x1 + 3 * c1x + 3 * mx + x2) / 8,
    labelY: (y1 + 3 * c1y + 3 * my + y2) / 8,
  }
}

/** A polyline through `points`, with each interior corner rounded off by `radius` (clamped to at most half
 *  of whichever adjoining segment is shorter, so a tight elbow never overshoots into an adjacent corner or
 *  past the line's own endpoints). Built from straight `L` segments that stop `radius` short of each corner
 *  and a `Q` quadratic through the corner itself - the standard "rounded polyline" construction, kept as its
 *  own pure function (same reasoning as curvedPath below: testable without rendering anything, and reusable
 *  for any n-point route, not just the 4-point one elbowPath happens to build). */
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

/** How far a squared route runs out of a box before it turns (px): inside the gap between rows of cards, so the first
 *  turn never clips a neighbour. */
const ELBOW_STUB = 22
/** Clear space kept between a squared route and any box it passes. */
const ELBOW_CLEARANCE = 8

type Pt = { x: number; y: number }
const flip = (pts: Pt[]): Pt[] => pts.map((p) => ({ x: p.y, y: p.x }))
const flip1 = (v: Pt): Pt => ({ x: v.y, y: v.x })
const flip1R = (o: PathObstacle): PathObstacle => ({ x: o.y, y: o.x, w: o.h, h: o.w })

/** How many boxes the axis-aligned legs of a route run through. */
function routeHits(points: Pt[], obstacles: PathObstacle[]): number {
  const c = ELBOW_CLEARANCE / 2
  let n = 0
  for (const o of obstacles) {
    for (let i = 1; i < points.length; i++) {
      const a = points[i - 1]
      const b = points[i]
      if (Math.min(a.x, b.x) < o.x + o.w + c && Math.max(a.x, b.x) > o.x - c && Math.min(a.y, b.y) < o.y + o.h + c && Math.max(a.y, b.y) > o.y - c) {
        n++
        break
      }
    }
  }
  return n
}

/** A squared route that runs through a box it should go around (a long vertical leg down a column of cards, say) is replaced
 *  by one that steps out of the source, runs along a free channel between boxes and steps into the target: the channel is
 *  tried at the sides of every box in the way, and the route with the fewest boxes crossed wins (the shortest on a tie), the
 *  given one if nothing is better. Two vertical ends only; the horizontal case calls it with the axes swapped. */
function clearJog(points: Pt[], sourceNormal: Pt, targetNormal: Pt, obstacles: PathObstacle[]): Pt[] {
  if (points.length !== 4 || obstacles.length === 0) return points
  let best = points
  let bestHits = routeHits(points, obstacles)
  if (bestHits === 0) return points
  const [a, , , b] = points
  const ya = a.y + (sourceNormal.y || -1) * ELBOW_STUB
  const yb = b.y + (targetNormal.y || -1) * ELBOW_STUB
  const length = (r: Pt[]) => r.reduce((sum, p, i) => (i ? sum + Math.abs(p.x - r[i - 1].x) + Math.abs(p.y - r[i - 1].y) : 0), 0)
  let bestLen = length(points)
  const channels = new Set<number>([(a.x + b.x) / 2])
  for (const o of obstacles) {
    channels.add(o.x - ELBOW_CLEARANCE)
    channels.add(o.x + o.w + ELBOW_CLEARANCE)
  }
  for (const cx of channels) {
    const route = [a, { x: a.x, y: ya }, { x: cx, y: ya }, { x: cx, y: yb }, { x: b.x, y: yb }, b]
    const hits = routeHits(route, obstacles)
    const len = length(route)
    if (hits < bestHits || (hits === bestHits && len < bestLen)) {
      best = route
      bestHits = hits
      bestLen = len
    }
  }
  return best
}

/** The "squared but soft" alternative to curvedPath, added per the UI/UX pass's arrow-style review: two
 *  or three axis-aligned legs joined by short rounded jogs between the two anchors, the same general shape
 *  React Flow's own built-in `smoothstep` edge type draws - except computed from OffsetEdge's own
 *  continuously-floating anchor points AND their actual box-relative exit/entry sides (`sourceNormal`,
 *  `targetNormal` - the same two curvedPath itself takes, from `outwardNormal`/`pickClearSide`) rather than
 *  from a fixed cardinal `sourcePosition`/`targetPosition` or a naive dx-vs-dy guess.
 *
 *  An earlier version of this picked its route shape purely from whichever axis carried more of the raw
 *  distance between the two anchor points, ignoring which side of each box they actually sat on. That reads
 *  fine when the dominant axis happens to agree with both normals (the common case - two boxes stacked in
 *  different rows, both normals vertical), but breaks visibly the moment it doesn't: an anchor correctly
 *  placed on a box's TOP edge (because that's the clear, unobstructed side - see pickClearSide) could still
 *  end up approached HORIZONTALLY if the two anchors happened to be more spread out sideways than vertically
 *  - the route runs the last leg sideways into a point that's meant to be entered from above, so it skims
 *  along just outside the box's own edge for a stretch rather than visibly plunging into it. Building the
 *  route from the normals themselves instead - matching curvedPath's own approach, just with hard corners
 *  instead of a bow - fixes that: the leg touching each anchor always runs along that anchor's own normal
 *  axis, so the line always looks like it leaves/arrives perpendicular to the box it touches, exactly like
 *  the curved style already does.
 *
 *  Both normals on the same axis (both vertical, or both horizontal - two boxes in different rows/columns,
 *  the common case) still gets the original two-bend "jog" shape, just keyed off the normals' axis instead
 *  of dx-vs-dy. Normals on different axes (a corner-ish relationship - leaving a box's side but entering
 *  another's top, say) gets a single-bend "L" instead: there's no room for a jog when one end's own leg
 *  already has to run along the other axis, so a two-bend shape there would only add a redundant corner
 *  without changing which side either end is entered from.
 *
 *  Still deliberately an opt-in look (the Options menu's "Edge style" control), not a replacement for
 *  curvedPath: a real orthogonal route reintroduces a small set of fixed angles, and a graph as dense and
 *  cross-crossing as this app's can end up busier, not cleaner, with hard elbows everywhere - it's a genuine
 *  style choice, not a strict upgrade. */
export function elbowPath(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  sourceNormal: { x: number; y: number },
  targetNormal: { x: number; y: number },
  obstacles: PathObstacle[] = [],
  radius = 14,
): { path: string; labelX: number; labelY: number } {
  const sourceVertical = sourceNormal.x === 0
  const targetVertical = targetNormal.x === 0
  const points =
    sourceVertical && targetVertical
      ? clearJog([{ x: x1, y: y1 }, { x: x1, y: (y1 + y2) / 2 }, { x: x2, y: (y1 + y2) / 2 }, { x: x2, y: y2 }], sourceNormal, targetNormal, obstacles)
      : !sourceVertical && !targetVertical
        ? flip(clearJog(flip([{ x: x1, y: y1 }, { x: (x1 + x2) / 2, y: y1 }, { x: (x1 + x2) / 2, y: y2 }, { x: x2, y: y2 }]), flip1(sourceNormal), flip1(targetNormal), obstacles.map(flip1R)))
        : sourceVertical
          ? // Leaves vertically (along sourceNormal), arrives horizontally (along targetNormal): a single
            // bend at the point directly below/above the source and level with the target.
            [{ x: x1, y: y1 }, { x: x1, y: y2 }, { x: x2, y: y2 }]
          : // The mirror: leaves horizontally, arrives vertically - single bend level with the source and
            // directly above/below the target.
            [{ x: x1, y: y1 }, { x: x2, y: y1 }, { x: x2, y: y2 }]
  const jog = points.length === 4 ? 1 : points.length === 6 ? 2 : 0
  const mid = jog ? points[jog] : undefined
  const mid2 = jog ? points[jog + 1] : undefined
  // For the 4-point jog shape, the label sits on the short middle leg - the one part of the path that's
  // never right on top of either box, unlike curvedPath's true midpoint label placement. The 3-point single-
  // bend shape has no such middle leg (the two legs meet directly at the bend), so the label instead goes on
  // the midpoint of whichever of the two legs is longer - the one more likely to actually clear both boxes.
  let labelX: number
  let labelY: number
  if (mid && mid2) {
    labelX = (mid.x + mid2.x) / 2
    labelY = (mid.y + mid2.y) / 2
  } else {
    const [p0, p1, p2] = points
    const leg1 = Math.hypot(p1.x - p0.x, p1.y - p0.y)
    const leg2 = Math.hypot(p2.x - p1.x, p2.y - p1.y)
    const [a, b] = leg1 >= leg2 ? [p0, p1] : [p1, p2]
    labelX = (a.x + b.x) / 2
    labelY = (a.y + b.y) / 2
  }
  return { path: roundedPolylinePath(points, radius), labelX, labelY }
}

/** Shrinks the segment from (sx,sy) to (tx,ty) by `gap` px at the source end, and at the target end too
 *  unless `hasTargetMarker` is set. The source end never has an arrowhead in this app - dependencies only
 *  point one way - so its plain line-end always gets pulled back off whatever box it would otherwise run
 *  flush into. The target end only needs the same treatment when it has nothing pointed to place there
 *  instead (an aggregated edge, with no markerEnd - see graph.ts's buildGraph): a real arrowhead's own tip is
 *  already exactly on (tx,ty) by construction (its SVG marker's refX=0 puts the tip at the path's own
 *  endpoint), so pulling the line back there too would just open a visible gap between the tip and the
 *  boundary it's meant to touch. Kept as its own pure function for the same reason as intersection/curvedPath
 *  above: testable in isolation, without rendering anything. */
export function pullBackEnds(sx: number, sy: number, tx: number, ty: number, gap: number, hasTargetMarker: boolean): { sx: number; sy: number; tx: number; ty: number } {
  const dx = tx - sx
  const dy = ty - sy
  const len = Math.hypot(dx, dy) || 1
  const ux = dx / len
  const uy = dy / len
  return {
    sx: sx + ux * gap,
    sy: sy + uy * gap,
    tx: hasTargetMarker ? tx : tx - ux * gap,
    ty: hasTargetMarker ? ty : ty - uy * gap,
  }
}

/** How far past the straight-line span between an edge's two anchors to still consider a card a possible
 *  obstacle for it - generous enough to cover the widest bow curvedPath's own escalation ever tries (see
 *  its doc comment), and reused as pickClearSide's own "is this side's approach corridor clear" depth. */
export const OBSTACLE_MARGIN = 160

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
): { point: { x: number; y: number }; normal: { x: number; y: number } } {
  if (obstacles.length === 0) return natural
  const naturalSide = CARDINAL_SIDES.find((s) => s.normal.x === natural.normal.x && s.normal.y === natural.normal.y)?.side ?? 'top'
  const depth = OBSTACLE_MARGIN
  if (sideIsClear(box, naturalSide, obstacles, depth)) return natural
  const perpendicular = naturalSide === 'top' || naturalSide === 'bottom' ? (['left', 'right'] as const) : (['top', 'bottom'] as const)
  const opposite: 'top' | 'bottom' | 'left' | 'right' =
    naturalSide === 'top' ? 'bottom' : naturalSide === 'bottom' ? 'top' : naturalSide === 'left' ? 'right' : 'left'
  for (const side of [...perpendicular, opposite]) {
    if (sideIsClear(box, side, obstacles, depth)) {
      const normal = CARDINAL_SIDES.find((s) => s.side === side)!.normal
      return { point: sideMidpoint(box, side), normal }
    }
  }
  // Every side is obstructed - no fallback helps, so keep the natural one (curvedPath's own bow escalation
  // still gets a chance at it, and picks whichever candidate collides least even then).
  return natural
}

/** The shape `collectObstacles` needs from `nodeLookup` - just enough of React Flow's own `InternalNode`
 *  to find each node's type, parent and live absolute box, without importing its full internal type. */
type ObstacleCandidate = { type?: string; parentId?: string; measured: { width?: number; height?: number }; internals: { positionAbsolute: { x: number; y: number } } }

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
  kinds: readonly string[] = ['card', 'boundary', 'namespace'],
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
  // A box at either end of the line (a bundle runs between two cluster boxes) holds its own cards and namespaces: they are not in its way either.
  const insideEnd = (n: ObstacleCandidate) => {
    for (let cur: ObstacleCandidate | undefined = n; cur?.parentId !== undefined; cur = nodeLookup.get(cur.parentId)) if (cur.parentId === source || cur.parentId === target) return true
    return false
  }
  const obstacles: PathObstacle[] = []
  for (const [nodeId, n] of nodeLookup) {
    if (nodeId === source || nodeId === target || skip.has(nodeId) || insideEnd(n)) continue
    if (!n.type || !kinds.includes(n.type)) continue
    const w = n.measured.width
    const h = n.measured.height
    if (!w || !h) continue
    const bx = n.internals.positionAbsolute.x
    const by = n.internals.positionAbsolute.y
    if (bx < bounds.maxX && bx + w > bounds.minX && by < bounds.maxY && by + h > bounds.minY) obstacles.push({ x: bx, y: by, w, h })
  }
  return obstacles
}


const WORLD = { minX: -Infinity, maxX: Infinity, minY: -Infinity, maxY: Infinity }
/** How far the end of a line stays off the box it belongs to (px). */
const LANE_GAP = 6
/** The distance between two lines that run in the same gutter (px). */
const TRACK_GAP = 8
const laneCache = new Map<string, Pt[] | null>()

/** The gutter route of a line between two boxes (lib/lanes.ts), worked out once per layout however often the line is redrawn (a zoom or a
 *  hover redraws every line, the route only changes when a box moves), with its two ends held off the boxes. */
function lanePath(from: PathObstacle, to: PathObstacle, others: PathObstacle[], fromShift: number, toShift: number, nudge: number): Pt[] | null {
  const key = laneKey(from, to, others, fromShift, toShift, nudge)
  let route = laneCache.get(key)
  if (route === undefined) {
    route = laneRoute(from, to, others, fromShift, toShift, nudge)?.map((p) => ({ ...p })) ?? null
    if (route) {
      const pull = (p: Pt, q: Pt): Pt => ({ x: p.x + Math.sign(q.x - p.x) * LANE_GAP, y: p.y + Math.sign(q.y - p.y) * LANE_GAP })
      route[0] = pull(route[0], route[1])
      route[route.length - 1] = pull(route[route.length - 1], route[route.length - 2])
    }
    if (laneCache.size > 400) laneCache.clear()
    laneCache.set(key, route)
  }
  return route
}

/** Where a line's label sits: the middle of its longest straight run, which is clear of the corners. */
function longestRun(points: Pt[]): { x: number; y: number } {
  let best = { x: points[0].x, y: points[0].y }
  let len = -1
  for (let i = 1; i < points.length; i++) {
    const l = Math.abs(points[i].x - points[i - 1].x) + Math.abs(points[i].y - points[i - 1].y)
    if (l > len) { len = l; best = { x: (points[i].x + points[i - 1].x) / 2, y: (points[i].y + points[i - 1].y) / 2 } }
  }
  return best
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
function DrawnEdge({ id, source, target, sourceX, sourceY, targetX, targetY, data, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge>) {
  const sourceNode = useInternalNode(source)
  const targetNode = useInternalNode(target)
  const sourceBox = boxOf(sourceNode)
  const targetBox = boxOf(targetNode)
  // Every other card on the canvas, live - reused below both to pick a different entry side when the
  // target's natural one is blocked (pickClearSide) and by curvedPath's own bow escalation (see
  // PathObstacle's doc comment). Reads the whole node lookup reactively (not just this edge's own source/
  // target, the way useInternalNode above does), so every edge re-renders on any node moving, not only its
  // own two ends - a broader subscription than the rest of this component needs, but cheap at this app's
  // scale (the actual per-render work below is a handful of point-in-rect checks), and it's what keeps a
  // dragged card's already-avoiding edges from lagging a stale detour behind it.
  const nodeLookup = useStore((s) => s.nodeLookup)

  let sx = sourceX
  let sy = sourceY
  let tx = targetX
  let ty = targetY
  // The direction curvedPath's cubic leaves the source in, and the box side the target's own tip sits on -
  // both default to "up" (a reasonable guess for the very first render or two before a node is measured)
  // and get replaced with the real answer below the moment both boxes are.
  let sourceNormal = { x: 0, y: -1 }
  let targetNormal = { x: 0, y: -1 }
  const obstacles: PathObstacle[] = []
  if (sourceBox && targetBox) {
    // A generous box around both ends' own boxes, not just the eventual touch points - available before
    // those are computed, and this component's one obstacle list ends up reused for both pickClearSide
    // (which runs before the touch points are final) and curvedPath's escalation (which runs after).
    const minX = Math.min(sourceBox.x, targetBox.x) - OBSTACLE_MARGIN
    const maxX = Math.max(sourceBox.x + sourceBox.w, targetBox.x + targetBox.w) + OBSTACLE_MARGIN
    const minY = Math.min(sourceBox.y, targetBox.y) - OBSTACLE_MARGIN
    const maxY = Math.max(sourceBox.y + sourceBox.h, targetBox.y + targetBox.h) + OBSTACLE_MARGIN
    obstacles.push(...collectObstacles(nodeLookup, source, target, { minX, maxX, minY, maxY }))

    const targetCenter = { x: targetBox.x + targetBox.w / 2, y: targetBox.y + targetBox.h / 2 }
    const sourceCenter = { x: sourceBox.x + sourceBox.w / 2, y: sourceBox.y + sourceBox.h / 2 }
    const from = intersection(sourceBox, targetCenter)
    const naturalTo = intersection(targetBox, sourceCenter)
    sourceNormal = outwardNormal(sourceBox, from)
    const chosen = pickClearSide(targetBox, { point: naturalTo, normal: outwardNormal(targetBox, naturalTo) }, obstacles)
    targetNormal = chosen.normal
    sx = from.x
    sy = from.y
    tx = chosen.point.x
    ty = chosen.point.y
  }

  // The bare line-end itself should never be the thing touching a box - per this round's UI/UX pass, the
  // only contact anywhere on an edge is meant to be an arrowhead's own tip. `intersection()` above already
  // placed sx/sy and tx/ty exactly ON each box's boundary; pullBackEnds shrinks that back off, by GAP, at
  // whichever end(s) have no arrowhead tip of their own to place there instead (see its own doc comment).
  //
  // GAP is expressed as a target *screen* size (a gap that reads the same whether the canvas is zoomed in or
  // fit-to-view zoomed way out over a big multi-cluster graph) and converted to flow-space by dividing by the
  // current zoom - a flat flow-space constant would shrink to sub-pixel, invisible nothing at the ~0.5-0.6x
  // zoom this app's own overview layouts commonly fit to, which is exactly the zoom level this was first
  // reported unnoticeable at.
  const { zoom } = useViewport()
  const GAP = 5 / zoom
  const pulled = pullBackEnds(sx, sy, tx, ty, GAP, !!markerEnd)
  sx = pulled.sx
  sy = pulled.sy
  tx = pulled.tx
  ty = pulled.ty

  const sourceOff = data?.sourceOffset ?? 0
  const targetOff = data?.targetOffset ?? 0
  const dx = tx - sx
  const dy = ty - sy
  const len = Math.hypot(dx, dy) || 1
  const nx = -dy / len
  const ny = dx / len
  const x1 = sx + nx * sourceOff
  const y1 = sy + ny * sourceOff
  const x2 = tx + nx * targetOff
  const y2 = ty + ny * targetOff

  const edgeStyle = useContext(EdgeStyleContext)
  const calm = useContext(DetailContext) === 'calm'
  // The hover or selection is on one of the bundle's boxes or ends: lit means the bundle is drawn firmly (CSS), and the cards it joins ring (nodes.tsx).
  // A store read with a boolean answer, so a pointer moving over the canvas re-renders only the lines whose answer changes.
  const focusIds = data?.focusIds
  const focused = useCanvasFocus((s) => focusedBy(s, focusIds))
  const lit = focused
  // Calm: a line that stands for several (a bundle, or the link between two clusters) runs in the gutters between boxes, never across one.
  const lane = calm && data?.aggregated && sourceBox && targetBox ? lanePath(sourceBox, targetBox, collectObstacles(nodeLookup, source, target, WORLD, ['boundary']), sourceOff, targetOff, (data.track ?? 0) * TRACK_GAP) : null
  const laneAt = lane && longestRun(lane)
  const { path, labelX, labelY } = lane
    ? { path: roundedPolylinePath(lane, 14), labelX: laneAt!.x, labelY: laneAt!.y }
    : edgeStyle === 'elbow'
      ? elbowPath(x1, y1, x2, y2, sourceNormal, targetNormal, obstacles)
      : curvedPath(x1, y1, x2, y2, nx, ny, sourceNormal, targetNormal, obstacles)
  // An "overlay" cluster link (joined through a tunnel, not a flat shared subnet) gets a second, wider,
  // low-opacity path drawn behind the real one - a "pipe" the already-dashed line now visibly runs
  // through, rather than just another plain line. Non-interactive (pointerEvents: 'none') so hovering or
  // selecting still binds only to the real path underneath; same hue as the edge's own current stroke, so
  // it tracks selection/focus recoloring (e.g. turning orange when this link is the thing selected)
  // instead of needing its own color logic duplicated from TopologyPage's CLUSTER_LINK_COLOR. "Subnet"
  // links (no tunnel involved) deliberately keep today's plain solid line - a flat shared network segment
  // isn't a tunnel, so a pipe around it would claim evidence that was never actually found.
  const isOverlayLink = data?.clusterLink?.kind === 'overlay'
  const baseOpacity = typeof style?.opacity === 'number' ? style.opacity : 1
  return (
    <g className={clsx(data?.role && `edge-${data.role}`, lit && 'edge-lit', data?.problem && 'edge-problem')}>
      {isOverlayLink && (
        <path
          d={path}
          className={calm ? 'cluster-link-pipe calm-pipe' : 'cluster-link-pipe'}
          style={{ stroke: style?.stroke, opacity: baseOpacity * 0.35 }}
          fill="none"
          pointerEvents="none"
        />
      )}
      <BaseEdge
        id={id}
        path={path}
        labelX={labelX}
        labelY={labelY}
        label={calm ? undefined : label}
        labelStyle={labelStyle}
        labelShowBg={labelShowBg}
        labelBgStyle={labelBgStyle}
        labelBgPadding={labelBgPadding}
        labelBgBorderRadius={labelBgBorderRadius}
        markerEnd={markerEnd}
        style={style}
        interactionWidth={calm ? 20 / zoom : interactionWidth}
      />
      {calm && label && (
        // Calm names a line with a pill in the layer above every line (an SVG label is painted with its own line, so a line drawn after it strikes
        // it through), opaque so no line shows through, and the same size on screen at every zoom.
        <EdgeLabelRenderer>
          <div
            data-testid="edge-pill"
            className="edge-pill nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px) scale(${1 / zoom})`, color: labelStyle?.fill, opacity: labelStyle?.opacity }}
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      )}
    </g>
  )
}

/**
 * Calm draws the calls between two boxes as the one bundle line between them, and never the calls themselves: a call is a line across the canvas to a
 * card far away, which runs over titles and boxes whatever the route. What a hover or a selection brings forward instead is the bundle (lit) and, on the
 * cards at its ends, a ring (nodes.tsx); the calls are in the Inspector and the hover card. So a 'detail' line draws nothing, and costs nothing.
 */
export function OffsetEdge(props: EdgeProps<TopoEdge>) {
  return props.data?.role === 'detail' ? null : <DrawnEdge {...props} />
}

export const edgeTypes = { offset: OffsetEdge }

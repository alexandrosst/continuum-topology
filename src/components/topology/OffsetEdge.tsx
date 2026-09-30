import { BaseEdge, useInternalNode, useViewport, type EdgeProps } from '@xyflow/react'
import { createContext, useContext } from 'react'
import type { TopoEdge } from '@/lib/graph'

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

/** The quadratic Bezier `Q` path, and label position, for a gentle bow between two anchor points - the
 *  math half of OffsetEdge's own curve-building (see its doc comment for why a hand-built quadratic and
 *  not `getBezierPath`), pulled out so it's testable without rendering anything. `nx`/`ny` is the already-
 *  computed unit normal of the source-to-target line (the same one `sourceOffset`/`targetOffset` fan
 *  along), so a curved edge bows toward the same side its parallel siblings fan toward. */
export function curvedPath(x1: number, y1: number, x2: number, y2: number, nx: number, ny: number): { path: string; labelX: number; labelY: number } {
  const segLen = Math.hypot(x2 - x1, y2 - y1) || 1
  const bow = Math.min(segLen * 0.12, 36)
  const mx = (x1 + x2) / 2 + nx * bow
  const my = (y1 + y2) / 2 + ny * bow
  return {
    path: `M${x1},${y1} Q${mx},${my} ${x2},${y2}`,
    // Quadratic Bezier at t=0.5: (P0 + 2*C + P2) / 4 - the label sits along the actual curve, not the
    // straight-line midpoint, so it doesn't appear to float off to one side of a strongly bowed edge.
    labelX: (x1 + 2 * mx + x2) / 4,
    labelY: (y1 + 2 * my + y2) / 4,
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

/** The "squared but soft" alternative to curvedPath, added per the UI/UX pass's arrow-style review: two
 *  axis-aligned legs joined by a short rounded jog partway between the two anchors, the same two-bend shape
 *  React Flow's own built-in `smoothstep` edge type draws - except computed from OffsetEdge's own
 *  continuously-floating anchor points (see this file's top-level doc comment for why those exist) rather
 *  than from a fixed cardinal `sourcePosition`/`targetPosition`. Whichever axis carries more of the distance
 *  between the two points gets the two long legs (a mostly-vertical edge - the common case here, since
 *  clusters stack in rows - gets a vertical-jog-vertical route; a mostly-horizontal one the mirror image),
 *  matching the rule smoothstep itself uses to decide its own bend. This is deliberately an opt-in look
 *  (the Options menu's "Edge style" control), not a replacement for curvedPath: a real orthogonal route
 *  reintroduces a small set of fixed angles, and a graph as dense and cross-crossing as this app's can end up
 *  busier, not cleaner, with hard elbows everywhere - it's a genuine style choice, not a strict upgrade. */
export function elbowPath(x1: number, y1: number, x2: number, y2: number, radius = 14): { path: string; labelX: number; labelY: number } {
  const dx = x2 - x1
  const dy = y2 - y1
  const points =
    Math.abs(dy) >= Math.abs(dx)
      ? [{ x: x1, y: y1 }, { x: x1, y: (y1 + y2) / 2 }, { x: x2, y: (y1 + y2) / 2 }, { x: x2, y: y2 }]
      : [{ x: x1, y: y1 }, { x: (x1 + x2) / 2, y: y1 }, { x: (x1 + x2) / 2, y: y2 }, { x: x2, y: y2 }]
  return {
    path: roundedPolylinePath(points, radius),
    // The midpoint of the route's own middle leg (the short jog between the two long legs) - the one part of
    // the path that's never right on top of either box, unlike curvedPath's true midpoint label placement.
    labelX: (points[1].x + points[2].x) / 2,
    labelY: (points[1].y + points[2].y) / 2,
  }
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
 * Builds its own quadratic-curve path rather than calling React Flow's `getStraightPath` or
 * `getBezierPath`, for the reason the original version of this component already established for the
 * straight-line predecessor of this one: `getBezierPath` needs a `sourcePosition`/`targetPosition` (one of
 * 4 cardinal values) to place its control points, and an SVG marker's `orient="auto"` arrowhead rotation
 * derives from the path's own local tangent - so a `getBezierPath` curve's arrowhead can only ever snap to
 * one of 4 fixed angles, never the real, continuous direction between two live points. Hand-building a `Q`
 * (quadratic Bezier) path from the same continuously-computed anchor points sidesteps that limitation
 * entirely: `orient="auto"` follows whatever tangent the path actually has at its end, cardinal or not, so
 * a gentle, deliberate bow (bowed away from the straight line by a small, length-proportional amount, capped
 * so it never looks exaggerated on a long edge) is free - it costs nothing in arrowhead precision, since the
 * tip still lands exactly on `intersection()`'s boundary point either way, only the approach angle curves.
 */
export function OffsetEdge({ id, source, target, sourceX, sourceY, targetX, targetY, data, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge>) {
  const sourceNode = useInternalNode(source)
  const targetNode = useInternalNode(target)
  const sourceBox = boxOf(sourceNode)
  const targetBox = boxOf(targetNode)

  let sx = sourceX
  let sy = sourceY
  let tx = targetX
  let ty = targetY
  if (sourceBox && targetBox) {
    const targetCenter = { x: targetBox.x + targetBox.w / 2, y: targetBox.y + targetBox.h / 2 }
    const sourceCenter = { x: sourceBox.x + sourceBox.w / 2, y: sourceBox.y + sourceBox.h / 2 }
    const from = intersection(sourceBox, targetCenter)
    const to = intersection(targetBox, sourceCenter)
    sx = from.x
    sy = from.y
    tx = to.x
    ty = to.y
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
  const { path, labelX, labelY } = edgeStyle === 'elbow' ? elbowPath(x1, y1, x2, y2) : curvedPath(x1, y1, x2, y2, nx, ny)
  return (
    <BaseEdge
      id={id}
      path={path}
      labelX={labelX}
      labelY={labelY}
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

export const edgeTypes = { offset: OffsetEdge }

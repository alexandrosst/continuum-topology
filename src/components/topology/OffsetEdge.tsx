import { BaseEdge, getStraightPath, useInternalNode, type EdgeProps } from '@xyflow/react'
import type { TopoEdge } from '@/lib/graph'

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
 * Still uses `getStraightPath` rather than `getBezierPath`, for the reason the original version of this
 * component already established: a bezier curve needs a `sourcePosition`/`targetPosition` (one of 4
 * cardinal values) for its tangent, and an SVG marker's `orient="auto"` arrowhead rotation derives from
 * that same tangent - so a bezier's arrowhead can only ever snap to one of 4 fixed angles, never the real,
 * continuous direction between two live points. A straight path has no such quantization.
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

  const sourceOff = data?.sourceOffset ?? 0
  const targetOff = data?.targetOffset ?? 0
  const dx = tx - sx
  const dy = ty - sy
  const len = Math.hypot(dx, dy) || 1
  const nx = -dy / len
  const ny = dx / len
  const [path, labelX, labelY] = getStraightPath({
    sourceX: sx + nx * sourceOff,
    sourceY: sy + ny * sourceOff,
    targetX: tx + nx * targetOff,
    targetY: ty + ny * targetOff,
  })
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

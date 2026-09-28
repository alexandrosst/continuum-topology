import { BaseEdge, getStraightPath, type EdgeProps } from '@xyflow/react'
import type { TopoEdge } from '@/lib/graph'

/**
 * A line like the default one, with its source end moved sideways by `data.sourceOffset` pixels and its
 * target end by `data.targetOffset`, independently. Equal values give the old parallel shift (two lines
 * between the same pair of boxes, kept apart along their whole length); different values let a line fan
 * out from a busy node while still landing cleanly at a quiet one on the other end.
 *
 * Uses `getStraightPath` rather than `getBezierPath`, deliberately: a bezier curve needs a `sourcePosition`/
 * `targetPosition` (one of the 4 cardinal `Position` values) to know its tangent at each end, and an SVG
 * marker's `orient="auto"` arrowhead derives its rotation from that same tangent - so with a bezier curve
 * the arrowhead can only ever snap to one of 4 fixed angles, never the real, continuous direction between
 * the two live points, even when the side is recomputed every render (it was, via `pickSides`, and still
 * looked "fixed" because the 4-way bucket rarely changes as a card is dragged around). A straight path has
 * no such quantization - its tangent, and so the arrowhead's rotation, is always the literal angle between
 * `sourceX/Y` and `targetX/Y`, which already track a dragged card live. This is the same "floating edge"
 * idea React Flow's own docs use, just landing on a straight rather than curved line.
 */
export function OffsetEdge({ id, sourceX, sourceY, targetX, targetY, data, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge>) {
  const sourceOff = data?.sourceOffset ?? 0
  const targetOff = data?.targetOffset ?? 0
  const dx = targetX - sourceX
  const dy = targetY - sourceY
  const len = Math.hypot(dx, dy) || 1
  const nx = -dy / len
  const ny = dx / len
  const [path, labelX, labelY] = getStraightPath({
    sourceX: sourceX + nx * sourceOff,
    sourceY: sourceY + ny * sourceOff,
    targetX: targetX + nx * targetOff,
    targetY: targetY + ny * targetOff,
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

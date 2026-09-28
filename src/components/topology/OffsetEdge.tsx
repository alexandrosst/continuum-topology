import { BaseEdge, getBezierPath, type EdgeProps } from '@xyflow/react'
import { pickSides, SIDES, type TopoEdge } from '@/lib/graph'

/**
 * A line like the default one, with its source end moved sideways by `data.sourceOffset` pixels and its
 * target end by `data.targetOffset`, independently. Equal values give the old parallel shift (two lines
 * between the same pair of boxes, kept apart along their whole length); different values let a line fan
 * out from a busy node while still landing cleanly at a quiet one on the other end.
 *
 * `sourcePosition`/`targetPosition` are which side of each box the edge was assigned to at layout time
 * (see pickSides in graph.ts) and stay fixed after that - including while a card is dragged around inside
 * its own box (extent: 'parent'). The line itself still reaches the dragged point fine, since sourceX/Y
 * and targetX/Y already track it live, but a bezier curve's tangent at each end - and so the arrowhead's
 * angle, which an SVG marker orients from that tangent - is dictated by the fixed side, not by the actual
 * live direction between the two points. So the arrowhead stops turning to match, even as the line visibly
 * bends to keep up. Re-deriving both sides from the live points on every render (the same heuristic
 * pickSides itself uses, just applied to two points instead of two boxes) fixes that - this is the same
 * "floating edge" approach React Flow's own docs use for exactly this reason.
 */
export function OffsetEdge({ id, sourceX, sourceY, targetX, targetY, data, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge>) {
  const sourceOff = data?.sourceOffset ?? 0
  const targetOff = data?.targetOffset ?? 0
  const dx = targetX - sourceX
  const dy = targetY - sourceY
  const len = Math.hypot(dx, dy) || 1
  const nx = -dy / len
  const ny = dx / len
  const [liveSourceSide, liveTargetSide] = pickSides({ x: sourceX, y: sourceY, w: 0, h: 0 }, { x: targetX, y: targetY, w: 0, h: 0 })
  const [path, labelX, labelY] = getBezierPath({
    sourceX: sourceX + nx * sourceOff,
    sourceY: sourceY + ny * sourceOff,
    sourcePosition: SIDES[liveSourceSide],
    targetX: targetX + nx * targetOff,
    targetY: targetY + ny * targetOff,
    targetPosition: SIDES[liveTargetSide],
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

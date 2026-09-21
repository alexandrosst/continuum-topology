import { BaseEdge, getBezierPath, type EdgeProps } from '@xyflow/react'
import type { TopoEdge } from '@/lib/graph'

/**
 * A line like the default one, moved sideways by `data.offset` pixels. Lines that join the same two boxes
 * (two ports, or one each way) would otherwise lie on top of each other and only the last would be visible.
 */
export function OffsetEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, labelShowBg, markerEnd, style, interactionWidth }: EdgeProps<TopoEdge>) {
  const off = data?.offset ?? 0
  const dx = targetX - sourceX
  const dy = targetY - sourceY
  const len = Math.hypot(dx, dy) || 1
  const nx = (-dy / len) * off
  const ny = (dx / len) * off
  const [path, labelX, labelY] = getBezierPath({ sourceX: sourceX + nx, sourceY: sourceY + ny, sourcePosition, targetX: targetX + nx, targetY: targetY + ny, targetPosition })
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

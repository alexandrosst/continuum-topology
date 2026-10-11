import { MiniMap, useStore, type MiniMapProps } from '@xyflow/react'

export type GraphBounds = { minX: number; minY: number; maxX: number; maxY: number }

/**
 * The minimap, shown only when it has something to say: when the whole graph is already on screen it only sits on top of
 * the boxes in the corner, so it appears once part of the graph is out of view (zoomed in, or a graph bigger than the
 * window) and goes away again when everything fits.
 */
export function AutoMiniMap({ bounds, hidden, ...props }: { bounds: GraphBounds | null; hidden?: boolean } & MiniMapProps) {
  const x = useStore((s) => s.transform[0])
  const y = useStore((s) => s.transform[1])
  const zoom = useStore((s) => s.transform[2])
  const width = useStore((s) => s.width)
  const height = useStore((s) => s.height)
  if (hidden || !bounds || !width || !height) return null
  const slack = 24 // px of the graph's edge allowed to touch the window's edge and still count as "all in view"
  const fits =
    bounds.minX * zoom + x >= -slack &&
    bounds.minY * zoom + y >= -slack &&
    bounds.maxX * zoom + x <= width + slack &&
    bounds.maxY * zoom + y <= height + slack
  if (fits) return null
  return <MiniMap {...props} />
}

import { useStore, useViewport } from '@xyflow/react'
import { useMemo, type ReactNode } from 'react'
import { HEADER, NS_HEADER, type TopoEdge } from '@/lib/graph'
import { polylineMidpoint } from './detour'
import { cachedOrtho, layoutEdge, useNodeGeometryKey, roundedPolylinePath, SQUARE_CORNER_RADIUS, SquareRoutesContext, type RoutedPath } from './OffsetEdge'
import { nudgeRoutes, type Pt, type Rect } from './nudge'
import { simpleOrthoRoute } from './ortho'

type Laid = { id: string; pts: Pt[]; n1: Pt; n2: Pt; hasMarker: boolean }

/**
 * Routes every line of the canvas together in the squared style and hands each its path through context.
 *
 * Each line is searched for on its own (cached, so only a line whose ends or neighbours moved is searched again), then
 * all of them are given to `nudgeRoutes`, which spaces apart the runs that would overlap or crowd. The ends are pulled back
 * off the boxes last, by a screen-sized gap, so zooming only redoes that cheap last step, not the search.
 */
export function SquareRoutes({ edges, enabled, children }: { edges: TopoEdge[]; enabled: boolean; children: ReactNode }) {
  const nodeLookup = useStore((s) => s.nodeLookup)
  const geometry = useNodeGeometryKey()
  const { zoom } = useViewport()
  // Selection and styling change the edge objects but not where a line goes.
  const key = enabled ? edges.map((e) => `${e.id}|${e.source}|${e.target}|${e.data?.sourceOffset ?? 0}|${e.data?.targetOffset ?? 0}|${e.markerEnd ? 1 : 0}`).join(';') : ''

  const laid = useMemo<Laid[]>(() => {
    if (!enabled) return []
    const routes: Laid[] = []
    for (const e of edges) {
      const l = layoutEdge(nodeLookup, e.source, e.target, e.data?.sourceOffset ?? 0, e.data?.targetOffset ?? 0, true)
      if (!l) continue
      const route = cachedOrtho(l.p1, l.n1, l.p2, l.n2, [...l.obstacles, l.sourceBox, l.targetBox, ...l.soft]) ?? simpleOrthoRoute(l.p1, l.n1, l.p2, l.n2)
      routes.push({ id: e.id, pts: route.map((p) => ({ ...p })), n1: l.n1, n2: l.n2, hasMarker: !!e.markerEnd })
    }
    // What a lane must not enter: every card, and the header strip of every cluster and namespace.
    const boxes: Rect[] = []
    for (const n of nodeLookup.values()) {
      const w = n.measured.width
      const h = n.measured.height
      if (!w || !h) continue
      const x = n.internals.positionAbsolute.x
      const y = n.internals.positionAbsolute.y
      if (n.type === 'card') boxes.push({ x, y, w, h })
      else if (n.type === 'boundary') boxes.push({ x, y, w, h: Math.min(h, HEADER) })
      else if (n.type === 'namespace') boxes.push({ x, y, w, h: Math.min(h, NS_HEADER) })
    }
    // While a card is being dragged every line is routed again on each frame, so the spacing waits until it is dropped.
    if (!geometry.includes('d;')) nudgeRoutes(routes, boxes)
    return routes
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `key` stands for the parts of `edges` that matter
  }, [nodeLookup, geometry, key, enabled])

  const zoomStep = Math.max(0.1, Math.round(zoom * 25) / 25)
  const paths = useMemo<Map<string, RoutedPath> | null>(() => {
    if (!enabled) return null
    const gap = 5 / zoomStep
    const out = new Map<string, RoutedPath>()
    for (const r of laid) {
      // The ends sit on the box boundary; step the source (and a target without an arrowhead) off it, as the other styles do.
      const pts = r.pts.map((p) => ({ ...p }))
      pts[0] = { x: pts[0].x + r.n1.x * gap, y: pts[0].y + r.n1.y * gap }
      if (!r.hasMarker) {
        const last = pts.length - 1
        pts[last] = { x: pts[last].x + r.n2.x * gap, y: pts[last].y + r.n2.y * gap }
      }
      const mid = polylineMidpoint(pts)
      out.set(r.id, { path: roundedPolylinePath(pts, SQUARE_CORNER_RADIUS), labelX: mid.x, labelY: mid.y })
    }
    return out
  }, [laid, zoomStep, enabled])

  return <SquareRoutesContext.Provider value={paths}>{children}</SquareRoutesContext.Provider>
}

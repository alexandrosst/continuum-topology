/**
 * Where the platform layer goes on the canvas (see platformLayer.ts for what it says). Inside a cluster box, its Discovery agent and
 * Local operator sit in one row under the cards; the Regional operators, the Central operator and FUSION stand in columns to the right
 * of every cluster, each regional level with the clusters that send to it. graph.ts calls these while it lays the clusters out, so the
 * layer goes through the same engine (and the same edge routing) as everything else, and costs nothing while it is off.
 */
import type { Node } from '@xyflow/react'
import type { Box } from './graph'
import type { PlatformEntity, PlatformKind, PlatformModel, PlatformStatus } from './platformLayer'
import type { Tier } from './types'

export type PlatformData = {
  kind: 'platform'
  /** The PlatformEntity id, which is also what the Inspector selects. */
  entityId: string
  platform: PlatformKind
  title: string
  subtitle: string
  status: PlatformStatus
  tier: Tier
}
export type PlatformNode = Node<PlatformData, 'platform'>

export const platformNodeId = (entityId: string) => `p:${entityId}`

export const PLATFORM_NODE = { w: 224, h: 56 }
/** Space between a cluster's cards and its platform row. */
const STRIP_GAP = 28
const NODE_GAP = 16
/** Between the clusters and the first column, and between columns: room for the line and the age written on it. */
const COLUMN_GAP = 170
const ROW_GAP = 28

/** A cluster box grown by one row of platform nodes: its new size, and where the row starts. */
export function reserveStrip(base: { w: number; h: number }, count: number, pad: number, headerY: number, hasCards: boolean): { w: number; h: number; y: number } {
  const y = hasCards ? base.h - pad + STRIP_GAP : headerY + 8
  return { w: Math.max(base.w, pad * 2 + count * PLATFORM_NODE.w + (count - 1) * NODE_GAP), h: y + PLATFORM_NODE.h + pad, y }
}

export function platformNode(e: PlatformEntity, tier: Tier, place: { x: number; y: number; parentId?: string; extent?: [[number, number], [number, number]] }): PlatformNode {
  return {
    id: platformNodeId(e.id),
    type: 'platform',
    position: { x: place.x, y: place.y },
    parentId: place.parentId,
    extent: place.extent,
    style: { width: PLATFORM_NODE.w, height: PLATFORM_NODE.h },
    zIndex: 10,
    data: { kind: 'platform', entityId: e.id, platform: e.kind, title: e.name, subtitle: e.detail, status: e.status, tier },
  }
}

/** In-cluster nodes, left to right from the box's padding. */
export const stripPosition = (i: number, pad: number, y: number) => ({ x: pad + i * (PLATFORM_NODE.w + NODE_GAP), y })

/**
 * The nodes outside the clusters. `placed` holds the boxes already on the canvas (absolute), `right` the right edge of the widest
 * cluster row. Regional operators form the first column, each as high as the middle of what sends to it (pushed down where two would
 * overlap); the central operator and FUSION follow, level with the middle of what sends to them.
 */
export function layoutOutside(model: PlatformModel, placed: Map<string, Box>, right: number): { nodes: PlatformNode[]; boxes: Map<string, Box> } {
  const boxes = new Map<string, Box>()
  const nodes: PlatformNode[] = []
  const entity = new Map(model.entities.map((e) => [e.id, e]))
  const centreOf = (id: string) => {
    const b = placed.get(platformNodeId(id)) ?? boxes.get(platformNodeId(id))
    return b ? b.y + b.h / 2 : undefined
  }
  const meanFrom = (id: string) => {
    const ys = model.edges.filter((e) => e.to === id).map((e) => centreOf(e.from)).filter((y): y is number => y !== undefined)
    return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : undefined
  }
  const put = (id: string, x: number, centre: number) => {
    const e = entity.get(id)!
    const box = { x, y: centre - PLATFORM_NODE.h / 2, w: PLATFORM_NODE.w, h: PLATFORM_NODE.h }
    boxes.set(platformNodeId(id), box)
    nodes.push(platformNode(e, 'cloud', { x: box.x, y: box.y }))
  }

  let x = right + COLUMN_GAP
  const regional = model.entities.filter((e) => e.kind === 'regional')
  if (regional.length) {
    // Operators nobody shown sends to (declared sources only) have no height of their own: they go below the rest.
    const wanted = regional.map((e) => ({ id: e.id, y: meanFrom(e.id) })).sort((a, b) => (a.y ?? Infinity) - (b.y ?? Infinity))
    let floor = -Infinity
    for (const w of wanted) {
      const y = Math.max(w.y ?? floor + PLATFORM_NODE.h + ROW_GAP, floor + PLATFORM_NODE.h + ROW_GAP)
      put(w.id, x, y)
      floor = y
    }
    x += PLATFORM_NODE.w + COLUMN_GAP
  }
  for (const id of ['central', 'fusion']) {
    if (!entity.has(id)) continue
    const own = id === 'central' ? meanFrom(id) : centreOf('central')
    put(id, x, own ?? PLATFORM_NODE.h)
    x += PLATFORM_NODE.w + COLUMN_GAP
  }
  return { nodes, boxes }
}

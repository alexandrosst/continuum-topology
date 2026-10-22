/**
 * Focus on the topology canvas: what stays lit while everything else fades. A pure function of the nodes and edges, so it is
 * testable without rendering the canvas.
 */

export type FocusNode = {
  id: string
  type?: string
  parentId?: string
  position: { x: number; y: number }
  measured?: { width?: number; height?: number }
  width?: number
  height?: number
}
export type FocusEdge = { id: string; source: string; target: string }

export type Focus = {
  /** Every node that stays at full strength: the focus, what its lines reach, the boxes they sit in, and what is inside a lit box. */
  keep: Set<string>
  /** The focus itself and the nodes at the other end of its lines (no boxes, no contents). */
  core: Set<string>
  /** The lines that belong to the focus. */
  hotIds: Set<string>
  /** For each node that fades, how long to wait (ms) before it does: more the further it is from the focus, so the fade spreads out. */
  ripple: Map<string, number>
}

/** The longest a faded node waits (ms). */
export const MAX_RIPPLE = 520
/** Milliseconds of waiting per px of distance from the focus. */
const RIPPLE_PER_PX = 0.3

/**
 * `selectedId` is the canvas node that was clicked (a card, or a cluster / group box) and `pickedEdgeId` a line that was clicked
 * instead. For a card the lit lines are its own; for a cluster or group box they are the lines that cross its edge, to whatever
 * they reach outside. Null when nothing is connected, since fading everything for an entity with no lines would only hide it.
 */
export function computeFocus(nodes: FocusNode[], edges: FocusEdge[], selectedId: string | null, pickedEdgeId: string | null): Focus | null {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const ancestors = (id: string) => {
    const out: string[] = []
    for (let n = byId.get(id); n?.parentId; n = byId.get(n.parentId)) out.push(n.parentId)
    return out
  }
  // Is `id` the focused node, or somewhere inside it (a cluster box holds cards, a namespace box more)?
  const inside = (id: string) => id === selectedId || (selectedId !== null && ancestors(id).includes(selectedId))
  const isBox = selectedId !== null && byId.get(selectedId)?.type === 'boundary'
  const hot = edges.filter((e) => e.id === pickedEdgeId || (selectedId !== null && (isBox ? inside(e.source) !== inside(e.target) : e.source === selectedId || e.target === selectedId)))
  if (hot.length === 0) return null

  const centre = (id: string) => {
    let x = 0
    let y = 0
    for (let n = byId.get(id); n; n = n.parentId ? byId.get(n.parentId) : undefined) {
      x += n.position.x
      y += n.position.y
    }
    const n = byId.get(id)
    return { x: x + (n?.measured?.width ?? n?.width ?? 0) / 2, y: y + (n?.measured?.height ?? n?.height ?? 0) / 2 }
  }
  const core = new Set<string>()
  if (selectedId) core.add(selectedId)
  for (const e of hot) {
    core.add(e.source)
    core.add(e.target)
  }
  const origin = centre(selectedId && byId.has(selectedId) ? selectedId : hot[0].source)
  const keep = new Set(core)
  for (const id of core) for (const a of ancestors(id)) keep.add(a)
  for (const n of nodes) if (ancestors(n.id).some((a) => core.has(a))) keep.add(n.id)
  const ripple = new Map<string, number>()
  for (const n of nodes) {
    if (keep.has(n.id)) continue
    const c = centre(n.id)
    ripple.set(n.id, Math.min(MAX_RIPPLE, Math.round(Math.hypot(c.x - origin.x, c.y - origin.y) * RIPPLE_PER_PX)))
  }
  return { keep, core, hotIds: new Set(hot.map((e) => e.id)), ripple }
}

import type { TopoNode } from './graph'
import type { Alert } from './detail'

export type Problem = { id: string; alert: Alert }

/**
 * The places the Calm canvas tints because something is wrong with them, counted once each and the same way in every layer: the innermost node
 * carrying an alert. A cluster whose cards already show the trouble is a frame around it, not a second problem; it counts only when it is worse
 * than everything inside it (the cluster itself is not working while its cards are merely warned). Worst first, then top to bottom and left to
 * right, so cycling through them reads the way the canvas does.
 */
export function problemsOf(nodes: TopoNode[]): Problem[] {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const at = (n: TopoNode): { x: number; y: number } => {
    const p = n.parentId ? byId.get(n.parentId) : undefined
    const o = p ? at(p) : { x: 0, y: 0 }
    return { x: o.x + n.position.x, y: o.y + n.position.y }
  }
  const alertOf = (n: TopoNode) => (n.data as { alert?: Alert }).alert
  // The worst alert drawn somewhere inside each ancestor: what makes a box a frame around a problem rather than a problem of its own.
  const inside = new Map<string, Alert>()
  for (const n of nodes) {
    const a = alertOf(n)
    if (!a) continue
    for (let p = n.parentId ? byId.get(n.parentId) : undefined; p; p = p.parentId ? byId.get(p.parentId) : undefined) if (inside.get(p.id) !== 'bad') inside.set(p.id, a)
  }
  return nodes
    .flatMap((n) => {
      const alert = alertOf(n)
      const framing = inside.has(n.id) && !(alert === 'bad' && inside.get(n.id) !== 'bad')
      return alert && !framing ? [{ id: n.id, alert, ...at(n) }] : []
    })
    .sort((a, b) => Number(b.alert === 'bad') - Number(a.alert === 'bad') || a.y - b.y || a.x - b.x || a.id.localeCompare(b.id))
    .map(({ id, alert }) => ({ id, alert }))
}

/** The problem after (or, going back, before) the one selected, wrapping around; the first (or last) when none of them is selected. */
export function nextProblem(problems: Problem[], selectedId: string | null, back = false): Problem | undefined {
  if (!problems.length) return undefined
  const i = problems.findIndex((p) => p.id === selectedId)
  if (i < 0) return back ? problems[problems.length - 1] : problems[0]
  return problems[(i + (back ? -1 : 1) + problems.length) % problems.length]
}

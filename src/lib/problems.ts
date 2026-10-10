import type { TopoNode } from './graph'
import type { Alert } from './detail'

export type Problem = { id: string; alert: Alert }

/**
 * The boxes and cards the Calm canvas tints because something is wrong with them: exactly the nodes carrying an alert, so the count a person
 * is told is the count they can see. Worst first, then top to bottom and left to right, so cycling through them reads the way the canvas does.
 */
export function problemsOf(nodes: TopoNode[]): Problem[] {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const at = (n: TopoNode): { x: number; y: number } => {
    const p = n.parentId ? byId.get(n.parentId) : undefined
    const o = p ? at(p) : { x: 0, y: 0 }
    return { x: o.x + n.position.x, y: o.y + n.position.y }
  }
  return nodes
    .flatMap((n) => {
      const alert = (n.data as { alert?: Alert }).alert
      return alert ? [{ id: n.id, alert, ...at(n) }] : []
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

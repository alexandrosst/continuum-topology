import type { Plan } from './types'
import type { World } from './world'

/** Which cluster each service the plan would move should go to, by name: the small marker on the canvas. */
export const placementHints = (plan: Plan, world: World): Map<string, string> =>
  new Map(plan.recommendations.map((r) => [r.serviceId, world.byCluster.get(r.to)?.name ?? r.to]))

/** The earlier hints when the new ones say the same, so what is keyed on them (the canvas graph) does not rebuild for nothing. */
export const keepHints = (prev: Map<string, string>, next: Map<string, string>) =>
  prev.size === next.size && [...next].every(([id, to]) => prev.get(id) === to) ? prev : next

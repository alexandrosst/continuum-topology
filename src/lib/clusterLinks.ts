import type { LinkPair } from './history'
import type { Agent } from './types'

/** What the server and the agents accept for a pair's throughput tests (they clamp anything outside; the form refuses it first). */
export const INTERVAL_MIN = 10
export const INTERVAL_MAX = 10080
export const BUDGET_MIN = 1
export const BUDGET_MAX = 1048576

export type Throughput = NonNullable<LinkPair['throughput']>
export type PresetId = 'light' | 'standard' | 'heavy'

/** The three named amounts of testing. "Custom" is whatever is not one of these. */
export const PRESETS: Record<PresetId, { label: string; intervalMinutes: number; budgetMB: number }> = {
  light: { label: 'Light', intervalMinutes: 1440, budgetMB: 50 },
  standard: { label: 'Standard', intervalMinutes: 360, budgetMB: 500 },
  heavy: { label: 'Heavy', intervalMinutes: 60, budgetMB: 5120 },
}

export const pairKey = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`)

/** A pair as the server keeps it: ids in order, so the same two clusters are never listed twice. */
export function newPair(x: string, y: string): LinkPair {
  return x < y ? { a: x, b: y } : { a: y, b: x }
}

export function hasPair(links: LinkPair[], x: string, y: string): boolean {
  return links.some((l) => pairKey(l.a, l.b) === pairKey(x, y))
}

export function replacePair(links: LinkPair[], next: LinkPair): LinkPair[] {
  return links.map((l) => (pairKey(l.a, l.b) === pairKey(next.a, next.b) ? next : l))
}

export function removePair(links: LinkPair[], x: string, y: string): LinkPair[] {
  return links.filter((l) => pairKey(l.a, l.b) !== pairKey(x, y))
}

/** Which named amount a pair's throughput setting is, or 'custom'. Undefined: the tests are off. */
export function presetOf(t: LinkPair['throughput']): PresetId | 'custom' | undefined {
  if (!t) return undefined
  for (const id of Object.keys(PRESETS) as PresetId[]) {
    if (t.intervalMinutes === PRESETS[id].intervalMinutes && t.budgetMB === PRESETS[id].budgetMB) return id
  }
  return 'custom'
}

export function throughputFor(id: PresetId): Throughput {
  return { intervalMinutes: PRESETS[id].intervalMinutes, budgetMB: PRESETS[id].budgetMB }
}

const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

/** "every 6 hours", "every 90 minutes", "every 2 days". */
export function everyWords(minutes: number): string {
  if (minutes % 1440 === 0) return `every ${minutes === 1440 ? 'day' : plural(minutes / 1440, 'day')}`
  if (minutes % 60 === 0) return `every ${minutes === 60 ? 'hour' : plural(minutes / 60, 'hour')}`
  return `every ${plural(minutes, 'minute')}`
}

export function sizeWords(mb: number): string {
  if (mb >= 1024 && mb % 1024 === 0) return `${mb / 1024} GB`
  return `${mb} MB`
}

/** The table's second line for the throughput column. */
export function throughputSummary(t: Throughput): { when: string; budget: string } {
  return {
    when: t.intervalMinutes ? everyWords(t.intervalMinutes) : "on the cluster's own schedule",
    budget: t.budgetMB ? `up to ${sizeWords(t.budgetMB)} a day` : "within the cluster's own daily budget",
  }
}

/** What the form needs fixed before it can save; empty when fine. Hours may be fractional. */
export function throughputProblems(hours: string, budget: string): string[] {
  const out: string[] = []
  const h = Number(hours)
  const minutes = Math.round(h * 60)
  if (hours.trim() === '' || !Number.isFinite(h) || minutes < INTERVAL_MIN || minutes > INTERVAL_MAX) {
    out.push(`The time between tests must be between ${INTERVAL_MIN} minutes and ${INTERVAL_MAX / 1440} days`)
  }
  const b = Number(budget)
  if (budget.trim() === '' || !Number.isInteger(b) || b < BUDGET_MIN || b > BUDGET_MAX) {
    out.push(`The daily budget must be a whole number of MB between ${BUDGET_MIN} and ${BUDGET_MAX}`)
  }
  return out
}

/** How many sets of tests a day this schedule means, in words, for the form's cost line. */
export function costLine(t: Throughput): string {
  const every = t.intervalMinutes ?? PRESETS.standard.intervalMinutes
  const perDay = 1440 / every
  const runs = perDay >= 1 ? `about ${Math.round(perDay * 10) / 10} sets of tests a day` : `one set of tests every ${everyWords(every).slice(6)}`
  const cap = t.budgetMB ? `, stopping for the day at ${sizeWords(t.budgetMB)}` : ", within the cluster's own daily budget"
  return `${runs[0].toUpperCase()}${runs.slice(1)}${cap}. Tests wait while the link is busy with real traffic.`
}

export type Check = { ok: boolean; text: string; fix?: string }

/** What a cluster needs for the active measurements, from what the server already knows about its agent. */
export function clusterChecks(clusterId: string, name: string, agents: Agent[]): Check[] {
  const agent = agents.find((a) => a.clusterId === clusterId && a.status === 'approved')
  if (!agent) return [{ ok: false, text: `${name} has no approved agent`, fix: 'Connect one in Agents' }]
  const out: Check[] = []
  out.push(agent.connected === false ? { ok: false, text: `The agent in ${name} is not connected`, fix: 'The settings are kept and apply when it returns' } : { ok: true, text: `The agent in ${name} is connected` })
  out.push(agent.accessTier >= 2 ? { ok: true, text: `${name} is at access level ${agent.accessTier}` } : { ok: false, text: `${name} is at access level ${agent.accessTier}: probing needs level 2`, fix: 'Raise it in Agents' })
  out.push((agent.observer?.collectors.length ?? 0) > 0 ? { ok: true, text: `${name} runs the flow collector` } : { ok: false, text: `${name} is not running the flow collector`, fix: 'Turn it on in the agent’s install' })
  return out
}

/** The id of the cluster that runs a pair's throughput tests: the one whose id sorts first. */
export const runnerOf = (p: LinkPair) => (p.a < p.b ? p.a : p.b)

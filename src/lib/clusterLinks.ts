import type { LinkPair } from './history'
import { ago } from './observed'
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

/** What the form needs fixed, per field; a field is absent when fine. Hours may be fractional. */
export function throughputFieldProblems(hours: string, budget: string): { hours?: string; budget?: string } {
  const out: { hours?: string; budget?: string } = {}
  const h = Number(hours)
  const minutes = Math.round(h * 60)
  if (hours.trim() === '' || !Number.isFinite(h) || minutes < INTERVAL_MIN || minutes > INTERVAL_MAX) {
    out.hours = `The time between tests must be between ${INTERVAL_MIN} minutes and ${INTERVAL_MAX / 1440} days`
  }
  const b = Number(budget)
  if (budget.trim() === '' || !Number.isInteger(b) || b < BUDGET_MIN || b > BUDGET_MAX) {
    out.budget = `The daily budget must be a whole number of MB between ${BUDGET_MIN} and ${BUDGET_MAX}`
  }
  return out
}

/** What the form needs fixed before it can save; empty when fine. */
export function throughputProblems(hours: string, budget: string): string[] {
  const p = throughputFieldProblems(hours, budget)
  return [p.hours, p.budget].filter((m): m is string => !!m)
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

/** The id of the cluster that runs a pair's probing and throughput tests: the one whose id sorts first. The other only answers. */
export const runnerOf = (p: LinkPair) => (p.a < p.b ? p.a : p.b)

/** The line under a pair's name: what runs for it, and from which cluster (probing and tests both run from the first one). */
export function runsFromWords(p: LinkPair, runner: string): string {
  const probing = !p.noProbe
  if (probing && p.throughput) return `probing and tests run from ${runner}`
  if (probing) return `probing runs from ${runner}; no throughput tests`
  if (p.throughput) return `tests run from ${runner}`
  return 'real traffic only'
}

/** What the server knows about one pair from the link series in FUSION (GET /links/latest). A figure FUSION does not hold is absent. "A to B" is
 * from the pair's first cluster to its second, in the order the pair is stored (the order the table shows them in). */
export interface LinkPairLatest {
  a: string
  b: string
  rttSeconds?: number
  lossAToB?: number
  lossBToA?: number
  jitterSeconds?: number
  mtuBytes?: number
  throughputAToB?: number
  throughputBToA?: number
  probedAt?: string
  testedAt?: string
}

export interface LinksLatest {
  available: boolean
  /** Why the figures cannot be had: this server has no FUSION, or it could not be read just now. */
  reason?: 'no-fusion' | 'unavailable'
  pairs: LinkPairLatest[]
  at: string
}

export const normalizeLinksLatest = (d: Partial<LinksLatest> | undefined): LinksLatest => ({ available: !!d?.available, reason: d?.reason, pairs: d?.pairs ?? [], at: d?.at ?? '' })

const num = (n: number | undefined): n is number => typeof n === 'number' && Number.isFinite(n)

/** "0.0 %", "1.8 %", "12 %". */
export function lossWords(ratio: number): string {
  const p = Math.max(0, ratio) * 100
  return `${p >= 10 ? Math.round(p) : p.toFixed(1)} %`
}

/** "0.42 ms", "6.2 ms", "48 ms", "1.3 s". */
export function delayWords(seconds: number): string {
  const ms = seconds * 1000
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} s`
  return `${ms < 1 ? ms.toFixed(2) : ms < 10 ? ms.toFixed(1) : Math.round(ms)} ms`
}

/** "920 Mbit/s", "1.2 Gbit/s", "48 kbit/s". */
export function rateWords(bits: number): string {
  if (bits >= 1e9) return `${(bits / 1e9).toFixed(bits >= 1e10 ? 0 : 1)} Gbit/s`
  if (bits >= 1e6) return `${Math.round(bits / 1e6)} Mbit/s`
  return `${Math.max(0, Math.round(bits / 1e3))} kbit/s`
}

/** A probe older than this is shown as old: the probe runs about once a minute, so this is many missed runs. */
export const STALE_AFTER_MS = 15 * 60_000

export interface LatestView {
  /** The loss line, with its tone: the worse of the two directions decides it. */
  loss?: { text: string; tone: 'ok' | 'warn' | 'bad' }
  /** The rest of the figures, each a short phrase. */
  details: string[]
  /** When the figures are from. */
  when?: { text: string; stale: boolean }
}

/** The words for a pair's latest figures, or null when there is nothing to show. Arrows read in the order the pair is displayed. */
export function latestView(l: LinkPairLatest | undefined, now = Date.now()): LatestView | null {
  if (!l) return null
  const v: LatestView = { details: [] }
  const loss = [num(l.lossAToB) ? { r: l.lossAToB, arrow: '→' } : null, num(l.lossBToA) ? { r: l.lossBToA, arrow: '←' } : null].filter((x) => x !== null)
  if (loss.length > 0) {
    const worst = Math.max(...loss.map((x) => x.r))
    v.loss = { text: `loss ${loss.map((x) => `${lossWords(x.r)} ${x.arrow}`).join(' · ')}`, tone: worst >= 0.05 ? 'bad' : worst >= 0.01 ? 'warn' : 'ok' }
  }
  if (num(l.rttSeconds)) v.details.push(`round trip ${delayWords(l.rttSeconds)}`)
  if (num(l.jitterSeconds)) v.details.push(`jitter ${delayWords(l.jitterSeconds)}`)
  if (num(l.mtuBytes)) v.details.push(`MTU ${l.mtuBytes}`)
  const tp = [num(l.throughputAToB) ? `${rateWords(l.throughputAToB)} →` : '', num(l.throughputBToA) ? `${rateWords(l.throughputBToA)} ←` : ''].filter(Boolean)
  if (tp.length > 0) v.details.push(tp.join(' · '))
  if (l.probedAt) {
    v.when = { text: `probed ${ago(l.probedAt, now)}`, stale: now - new Date(l.probedAt).getTime() > STALE_AFTER_MS }
  }
  if (l.testedAt && tp.length > 0) v.when = { text: `${v.when ? `${v.when.text} · ` : ''}tested ${ago(l.testedAt, now)}`, stale: v.when?.stale ?? false }
  return v.loss || v.details.length > 0 ? v : null
}

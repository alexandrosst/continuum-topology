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

/** `code` says what the check is about, so the row can leave out the ones the server's own checklist already says (see `uncoveredChecks`). */
export type Check = { ok: boolean; text: string; fix?: string; code?: 'agent' | 'connected' | 'tier' | 'flow'; cluster?: string }

/** What a cluster needs for the active measurements, from what the server already knows about its agent. */
export function clusterChecks(clusterId: string, name: string, agents: Agent[]): Check[] {
  const agent = agents.find((a) => a.clusterId === clusterId && a.status === 'approved')
  if (!agent) return [{ ok: false, code: 'agent', cluster: clusterId, text: `${name} has no approved agent`, fix: 'Connect one in Agents' }]
  const out: Check[] = []
  out.push(agent.connected === false ? { ok: false, code: 'connected', cluster: clusterId, text: `The agent in ${name} is not connected`, fix: 'The settings are kept and apply when it returns' } : { ok: true, code: 'connected', text: `The agent in ${name} is connected` })
  out.push(agent.accessTier >= 2 ? { ok: true, code: 'tier', text: `${name} is at access level ${agent.accessTier}` } : { ok: false, code: 'tier', cluster: clusterId, text: `${name} is at access level ${agent.accessTier}: probing needs level 2`, fix: 'Raise it in Agents' })
  out.push((agent.observer?.collectors.length ?? 0) > 0 ? { ok: true, code: 'flow', text: `${name} runs the flow collector` } : { ok: false, code: 'flow', cluster: clusterId, text: `${name} is not running the flow collector`, fix: 'Turn it on in the agent’s install' })
  return out
}

export type Via = NonNullable<LinkPair['via']>

/** What the "Measure through" select offers: '' follows the traffic, the others pin the pair to one kind of address. */
export const VIA_OPTIONS: { value: Via | ''; label: string }[] = [
  { value: '', label: 'The address your traffic uses (recommended)' },
  { value: 'mesh', label: 'Mesh addresses (100.64.x.x and similar)' },
  { value: 'private', label: 'Private addresses' },
  { value: 'public', label: 'Public addresses' },
]

/** The sentence under the select. */
export const VIA_HINT =
  'Probing and throughput tests follow the address your clusters’ real traffic is seen using, and try the others if it does not answer. Choose a kind to measure only that path; the figures then describe it, and the measurement fails if the other cluster has no address of that kind.'

/** The kind as it reads in a sentence: "mesh addresses". */
export const viaWords = (v: Via) => `${v} addresses`

/** The id of the cluster that runs a pair's probing and throughput tests: the one whose id sorts first. The other only answers. */
export const runnerOf = (p: LinkPair) => (p.a < p.b ? p.a : p.b)

/** The line under a pair's name: what runs for it, and from which cluster (probing and tests both run from the first one). */
export function runsFromWords(p: LinkPair, runner: string): string {
  const probing = !p.noProbe
  const through = p.via ? `, through ${viaWords(p.via)}` : ''
  if (probing && p.throughput) return `probing and tests run from ${runner}${through}`
  if (probing) return `probing runs from ${runner}${through}; no throughput tests`
  if (p.throughput) return `tests run from ${runner}${through}`
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

/** Why a pair is not measured from one of its clusters, as the server worked it out. */
export interface LinkNote {
  cluster: string
  peer?: string
  code: 'no-agent' | 'tier' | 'paused' | 'peer-silent' | 'shared-address' | 'no-address-of-kind'
  text: string
}

/** One line of a pair's checklist, as the server worked it out (`setup` in GET /links/latest). */
export type SetupState = 'ok' | 'todo' | 'unknown' | 'optional'
export interface SetupStep {
  /** agent | addresses | capabilities | links-on | export | prober | capacity | peer-export */
  code: string
  /** The cluster the step is about; absent when it is about the pair. */
  cluster?: string
  state: SetupState
  /** What is wrong and what to do (or what is fine), naming the cluster. */
  text: string
}
export interface PairSetup {
  a: string
  b: string
  steps: SetupStep[]
}

export interface LinksLatest {
  available: boolean
  /** Why the figures cannot be had: this server has no FUSION, or it could not be read just now. */
  reason?: 'no-fusion' | 'unavailable'
  pairs: LinkPairLatest[]
  /** Reasons a pair is not measured, whether or not FUSION could be read. */
  notes: LinkNote[]
  /** The checklist of each pair, whether or not FUSION could be read. Empty from a server that does not send it. */
  setup: PairSetup[]
  at: string
}

const SETUP_STATES: readonly string[] = ['ok', 'todo', 'unknown', 'optional']
const MAX_SETUP_PAIRS = 200
const MAX_SETUP_STEPS = 40

/** The checklists as the page uses them. A server that does not send them gives none; a state it does not know reads as "unknown", never as done. */
export function normalizeSetup(raw: unknown): PairSetup[] {
  if (!Array.isArray(raw)) return []
  const out: PairSetup[] = []
  for (const p of raw.slice(0, MAX_SETUP_PAIRS)) {
    if (!p || typeof p !== 'object' || typeof p.a !== 'string' || typeof p.b !== 'string') continue
    const steps: SetupStep[] = []
    for (const s of (Array.isArray(p.steps) ? p.steps : []).slice(0, MAX_SETUP_STEPS)) {
      if (!s || typeof s !== 'object' || typeof s.text !== 'string' || s.text === '') continue
      steps.push({
        code: typeof s.code === 'string' ? s.code : '',
        ...(typeof s.cluster === 'string' && s.cluster !== '' ? { cluster: s.cluster } : {}),
        state: SETUP_STATES.includes(s.state) ? (s.state as SetupState) : 'unknown',
        text: s.text,
      })
    }
    out.push({ a: p.a, b: p.b, steps })
  }
  return out
}

export const normalizeLinksLatest = (d: Partial<LinksLatest> | undefined): LinksLatest => ({ available: !!d?.available, reason: d?.reason, pairs: d?.pairs ?? [], notes: d?.notes ?? [], setup: normalizeSetup(d?.setup), at: d?.at ?? '' })

/** What a pair needs, said where the person starts: the section's intro, the Add pair dialog and the telemetry wizard. */
export const LINK_REQUIREMENT = 'A pair needs two clusters that each run the Ikhnos agent with Cluster links on; the cluster that runs the probes also has to send telemetry to FUSION (a local operator).'

/** The marks of the four states. The words are for screen readers and for the title; the shapes differ, so colour is never the only difference. */
export const STEP_MARK: Record<SetupState, { word: string }> = {
  ok: { word: 'Done' },
  todo: { word: 'To do' },
  unknown: { word: 'Not known' },
  optional: { word: 'Optional' },
}

export interface SetupSummary {
  /** The steps that still have to be done. */
  todo: SetupStep[]
  /** How many steps could not be told (an agent too old to say, or silent). */
  unknown: number
  /** Everything required is done, and nothing is unknown. */
  ready: boolean
  /** The quiet word on the Setup button: "ready", "2 to do", "not fully known". */
  label: string
}

export function setupSummary(steps: SetupStep[]): SetupSummary {
  const todo = steps.filter((s) => s.state === 'todo')
  const unknown = steps.filter((s) => s.state === 'unknown').length
  const ready = todo.length === 0 && unknown === 0
  return { todo, unknown, ready, label: todo.length > 0 ? `${todo.length} to do` : unknown > 0 ? 'not fully known' : 'ready' }
}

/** The first `max` lines of a list of unmet things, and how many were left out. */
export function firstLines(texts: string[], max = 2): { shown: string[]; more: number } {
  return { shown: texts.slice(0, max), more: Math.max(0, texts.length - max) }
}

/** The note codes that say a cluster is not told about its peer, and the checklist step that says the same. */
const NOTE_STEP: Record<LinkNote['code'], string> = {
  'no-agent': 'agent', tier: 'agent', paused: 'agent', 'peer-silent': 'addresses', 'shared-address': 'addresses', 'no-address-of-kind': 'addresses',
}

/** The notes of a pair that its checklist does not already say: a note and an unmet step about the same cluster are one thing, shown once (as the step). */
export function uncoveredNotes(notes: LinkNote[], setup: PairSetup | undefined): LinkNote[] {
  if (!setup) return notes
  return notes.filter((n) => !setup.steps.some((s) => s.state === 'todo' && s.cluster === n.cluster && s.code === NOTE_STEP[n.code]))
}

/** The client's own unmet checks that the server's checklist does not already say: a missing agent or a low access level of a cluster whose `agent` step is unmet there too. */
export function uncoveredChecks(checks: Check[], setup: PairSetup | undefined): Check[] {
  if (!setup) return checks
  return checks.filter((c) => !((c.code === 'agent' || c.code === 'tier') && setup.steps.some((s) => s.state === 'todo' && s.code === 'agent' && s.cluster === c.cluster)))
}

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

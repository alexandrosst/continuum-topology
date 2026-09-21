/**
 * What an agent says about itself, and what an administrator may ask of it.
 *
 * Consent lives with the owner of the cluster. The chart's `access.tier`, its RBAC and its scope are a CEILING inside the
 * cluster that this server can never exceed; the server may only NARROW within it (a lower tier, a paused collector, more
 * namespaces left out). Widening is done by the cluster's owner with `helm upgrade`, and this file builds the exact command.
 * Everything here is pure so that it can be tested without a browser.
 */
import { scopeProblems, splitNames } from './install'

export type Severity = 'info' | 'warn' | 'error'

export interface AgentProblem {
  /** Stable: the documentation and this page key on it (rbac_forbidden, informer_not_synced, sync_too_large, ...). */
  code: string
  severity: Severity
  message: string
  since?: string
}

export interface AgentCollector {
  name: string
  /** The cluster's owner installed it (a chart value). */
  configured: boolean
  /** It is running now. */
  enabled: boolean
  pausedByServer?: boolean
  producing: boolean
  reporting: number
  expected: number
  note?: string
  lastData?: string
}

export interface AgentInformer {
  name: string
  module?: string
  synced: boolean
  objects: number
  lastError?: string
}

export interface AgentDiagnostics {
  reportedAt: string
  /** Only what the hello carried: the agent had not yet looked at the cluster. */
  partial?: boolean
  agentVersion: string
  arch?: string
  os?: string
  uptimeSeconds: number
  installedTier: number
  approvedTier: number
  effectiveTier: number
  scope?: { description: string; namespaces: number; inScope: number }
  ownExcludedNamespaces?: number
  pausedCollectors: string[]
  excludedNamespaces: number
  flowDropped?: number
  collectors: AgentCollector[]
  informers: AgentInformer[]
  problems: AgentProblem[]
}

/** What an administrator has asked one agent to leave out. */
export interface AgentConsent {
  pausedCollectors: string[]
  excludedNamespaces: string[]
}

export interface AgentExtras {
  diagnostics?: AgentDiagnostics
  consent?: AgentConsent
}

/** The two fields of an agent that only editors and administrators receive. */
export function extrasOf(agents: readonly unknown[] | undefined, id: string): AgentExtras {
  const a = (agents ?? []).find((x) => (x as { id?: string }).id === id) as AgentExtras | undefined
  if (!a) return {}
  return {
    diagnostics: a.diagnostics ? { ...a.diagnostics, pausedCollectors: a.diagnostics.pausedCollectors ?? [], collectors: a.diagnostics.collectors ?? [], informers: a.diagnostics.informers ?? [], problems: a.diagnostics.problems ?? [] } : undefined,
    consent: a.consent ? { pausedCollectors: a.consent.pausedCollectors ?? [], excludedNamespaces: a.consent.excludedNamespaces ?? [] } : undefined,
  }
}

/* ---------- health ---------- */

export interface HealthSummary {
  level: 'unknown' | 'healthy' | 'warn' | 'error'
  /** One line: "Healthy", "1 problem", "3 problems". */
  line: string
  /** Problems that need a person (warnings and errors); notices do not count. */
  count: number
}

/** Agents refresh their self-report every 5 minutes; three missed means what it says is history, not news. */
export const STALE_REPORT_SECONDS = 15 * 60

/**
 * The agent's health in one line. With `fresh` (the time now, and whether the agent is connected) a report that is old, or
 * from an agent that is not connected, is not called "Healthy": it says how old it is, because that is what is known.
 */
export function healthSummary(d?: AgentDiagnostics, fresh?: { now: number; connected?: boolean }): HealthSummary {
  if (!d) return { level: 'unknown', line: 'No report yet', count: 0 }
  const serious = d.problems.filter((p) => p.severity !== 'info')
  if (fresh) {
    const age = (fresh.now - Date.parse(d.reportedAt)) / 1000
    if (fresh.connected === false || (Number.isFinite(age) && age > STALE_REPORT_SECONDS)) {
      const line = Number.isFinite(age) ? `Last report ${uptimeWords(Math.max(0, age))} ago` : 'Report date unknown'
      return { level: 'unknown', line: fresh.connected === false ? `Not connected · ${line.toLowerCase()}` : line, count: serious.length }
    }
  }
  if (serious.length === 0) return { level: 'healthy', line: 'Healthy', count: 0 }
  const worst = serious.some((p) => p.severity === 'error') ? 'error' : 'warn'
  return { level: worst, line: `${serious.length} problem${serious.length === 1 ? '' : 's'}`, count: serious.length }
}

const RANK: Record<Severity, number> = { error: 0, warn: 1, info: 2 }
/** Worst first, then in the order the agent sent them. */
export const sortProblems = (ps: readonly AgentProblem[]): AgentProblem[] => [...ps].sort((a, b) => RANK[a.severity] - RANK[b.severity])

/* ---------- tiers ---------- */

export const TIER_NAMES = ['Registered only', 'Infrastructure', 'Services', 'Dependencies', 'Control'] as const
export const tierName = (t: number) => TIER_NAMES[t] ?? `Tier ${t}`

export interface TierChoice {
  value: number
  label: string
  /** Above what the install allows: only the cluster's owner can make it available. */
  aboveCeiling: boolean
}

/** Every tier this server release can grant; those above the install's ceiling are marked so they can be shown disabled. */
export function tierChoices(installed: number, implemented: number): TierChoice[] {
  const out: TierChoice[] = []
  for (let t = 0; t <= implemented; t++) out.push({ value: t, label: tierName(t), aboveCeiling: t > installed })
  return out
}

export interface InstallInfo {
  chartFile?: string
  chartRef?: string
  chartVersion?: string
}

/**
 * The command the cluster's owner runs to raise the ceiling of an agent's install. It mirrors what the server prints in its
 * own refusal, so what a person reads here and there is the same. `--reuse-values` keeps everything else about the install.
 */
export function helmUpgradeCommand(install: InstallInfo | undefined, tier: number): string {
  const ref = install?.chartRef || `./${install?.chartFile || 'continuum-agent.tgz'}`
  const version = install?.chartRef && !install.chartRef.endsWith('.tgz') && install.chartVersion ? ` --version ${install.chartVersion}` : ''
  return `helm upgrade continuum-agent ${ref}${version} --namespace continuum-system --reuse-values --set access.tier=${tier}`
}

/** What the agent is doing at a tier, against what was approved: the reason it may be lower is worth saying. */
export function effectiveNote(d: AgentDiagnostics): string | undefined {
  if (d.effectiveTier < d.approvedTier) {
    return d.effectiveTier < d.installedTier
      ? 'The agent has not yet applied the approved access.'
      : `Held back by the install: the cluster's owner allows only ${tierName(d.installedTier).toLowerCase()} (Helm access.tier).`
  }
  return undefined
}

/* ---------- the optional collectors ---------- */

export const COLLECTORS: { id: string; label: string; what: string; chart: string }[] = [
  { id: 'probes', label: 'Node probe', what: 'Facts about each machine (virtual machine or bare metal, model, kernel).', chart: 'nodeProbe.enabled' },
  { id: 'flow', label: 'Traffic observer', what: 'Which workloads talk to which, from the nodes.', chart: 'flowObserver.enabled' },
  { id: 'measure', label: 'Path measurements', what: 'Timing of TCP connections to addresses this server names.', chart: 'measurements.enabled' },
]

export interface CollectorState {
  tone: 'off' | 'paused' | 'ok' | 'warn'
  label: string
}

export function collectorState(c: AgentCollector | undefined): CollectorState {
  if (!c || !c.configured) return { tone: 'off', label: 'Not installed in this cluster' }
  if (c.pausedByServer) return { tone: 'paused', label: 'Paused by an administrator' }
  if (!c.enabled) return { tone: 'off', label: 'Off' }
  if (c.name !== 'measure' && c.expected > 0) {
    const of = `${c.reporting} of ${c.expected} nodes`
    return c.producing ? { tone: c.reporting < c.expected ? 'warn' : 'ok', label: `On, reporting from ${of}` } : { tone: 'warn', label: `On, but silent (${of} reporting)` }
  }
  return c.producing ? { tone: 'ok', label: c.note ? `On, ${c.note}` : 'On' } : { tone: 'warn', label: c.note ? `On, ${c.note}` : 'On, nothing yet' }
}

/* ---------- what an administrator may ask ---------- */

/** kube-system and friends: the agent always reads them to recognise the cluster's own components, so they cannot be left out. */
const SYSTEM_NAMESPACES = ['kube-system', 'kube-public', 'kube-node-lease']
export const MAX_EXCLUDED = 200

export interface ExclusionInput {
  names: string[]
  problems: string[]
}

/** "shop, batch" → the names, and what is wrong with them (uses the same rules as the install wizard's scope). */
export function parseExclusions(text: string): ExclusionInput {
  const names = splitNames(text)
  const problems = scopeProblems({ namespaces: [], exclude: names, selector: '' })
  for (const n of names) if (SYSTEM_NAMESPACES.includes(n)) problems.push(`${n} is a system namespace: the agent always reads those to recognise the cluster's own components`)
  if (names.length > MAX_EXCLUDED) problems.push(`At most ${MAX_EXCLUDED} namespaces can be left out from here; use the scope of the install (Helm) for more`)
  return { names: [...names].sort(), problems }
}

const sameSet = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().every((x, i) => x === [...b].sort()[i])

export interface ConsentDraft {
  tier: number
  paused: string[]
  excluded: string[]
}

export interface ConsentChange {
  tier: boolean
  overrides: boolean
}

/** What of the draft differs from what is stored; nothing to save when both are false. */
export function consentChange(draft: ConsentDraft, approvedTier: number, stored: AgentConsent | undefined): ConsentChange {
  return {
    tier: draft.tier !== approvedTier,
    overrides: !sameSet(draft.paused, stored?.pausedCollectors ?? []) || !sameSet(draft.excluded, stored?.excludedNamespaces ?? []),
  }
}

/**
 * Whether the agent has confirmed what was asked (its own account of what is in force matches). Until it has, the page says the change
 * is on its way rather than done. Approved tier is compared with what the agent says it collects at, bounded by its ceiling.
 */
export function inForce(consent: AgentConsent | undefined, approvedTier: number, d: AgentDiagnostics | undefined): boolean {
  if (!d || d.partial) return false
  const want = Math.min(approvedTier, d.installedTier)
  return d.effectiveTier === want && sameSet(d.pausedCollectors, consent?.pausedCollectors ?? []) && d.excludedNamespaces === (consent?.excludedNamespaces.length ?? 0)
}

/** "2 of 30 namespaces" and how the rest are left out, in words. */
export function scopeWords(d: AgentDiagnostics): string {
  const s = d.scope
  if (!s) return 'not reported'
  const own = d.ownExcludedNamespaces ?? 0
  const extra = d.excludedNamespaces
  const rest = [own > 0 && `${own} left out by the install`, extra > 0 && `${extra} more by an administrator`].filter(Boolean).join(', ')
  const count = s.namespaces > 0 ? `${s.inScope} of ${s.namespaces} namespaces in view` : 'no namespaces read at this tier'
  return `${count}${rest ? ` (${rest})` : ''}`
}

/** "3h", "12 min": for an uptime. */
export function uptimeWords(seconds: number): string {
  if (seconds < 90) return `${Math.max(0, Math.round(seconds))} s`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`
  if (seconds < 129_600) return `${Math.round(seconds / 3600)} h`
  return `${Math.round(seconds / 86_400)} d`
}

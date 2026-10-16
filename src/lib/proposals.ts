import { atLeast, type Role } from './api'
import { COLLECTORS } from './consent'
import { bytesPerSec, bytesTotal } from './observed'
import { ACCESS_TIERS } from './types'

// The proposals an advisor made about one agent's access, as the server returns them (see backend/docs/decision-loop.md). An
// advisor never changes anything; a person accepts or rejects. Everything here only turns the server's documents into words and
// decides who may press which button; the server decides again, so a wrong guess here costs a refused request, not a wrong change.

export type ProposalStatus = 'pending' | 'accepted' | 'rejected' | 'expired' | 'applied' | 'failed'
export type EvidenceClass = 'measured' | 'derived' | 'declared' | 'unknown'

/** Exactly one kind of change: a new approved tier, or additions and removals on the agent's overrides. */
export interface ProposalChange {
  agentId: string
  tier?: number
  pause?: string[]
  resume?: string[]
  exclude?: string[]
  include?: string[]
}

export interface EvidenceFact {
  name: string
  value?: string
  class: EvidenceClass
  observedAt?: string
  source?: string
}

/** What the change touches, worked out by the server. */
export interface ProposalBlast {
  agents: number
  clusterId?: string
  tierFrom?: number
  tierTo?: number
  collectors?: string[]
  namespaces?: number
}

/** Expected (or observed) relative change per metric: -0.5 halves it. */
export type MetricChanges = Record<string, number>

export interface ProposalOutcome {
  result: 'measured' | 'unconfirmed' | 'no_data'
  predicted: MetricChanges
  observed: { before?: Record<string, number>; after?: Record<string, number>; change?: MetricChanges }
  inForceAt?: string
  recordedAt: string
}

export interface Proposal {
  id: string
  agentId: string
  advisor: string
  modelVersion: number
  change: ProposalChange
  evidence: EvidenceFact[]
  confidence: EvidenceClass
  blast: ProposalBlast
  predicted: MetricChanges
  /** Worked out by the server, never taken from the advisor. */
  widens: boolean
  mode: string
  status: ProposalStatus
  createdAt: string
  expiresAt: string
  decidedBy?: string
  decidedAt?: string
  note?: string
  /** Only on a single proposal read, once one has been recorded. */
  outcome?: ProposalOutcome
}

const tierLabel = (t: number) => ACCESS_TIERS.find((x) => x.value === t)?.label ?? `tier ${t}`
const collectorLabel = (id: string) => (COLLECTORS.find((c) => c.id === id)?.label ?? id).toLowerCase()
const list = (xs: string[]) => (xs.length <= 2 ? xs.join(' and ') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`)
const namespaces = (xs: string[]) => `${xs.length === 1 ? 'namespace' : 'namespaces'} ${list(xs)}`

/** The change as sentences, one per part. The agent is named by the caller. */
export function changeLines(change: ProposalChange, blast?: ProposalBlast): string[] {
  const out: string[] = []
  if (change.tier !== undefined) {
    out.push(blast?.tierFrom !== undefined && blast.tierFrom !== change.tier
      ? `Change the approved access from ${tierLabel(blast.tierFrom)} to ${tierLabel(change.tier)}`
      : `Set the approved access to ${tierLabel(change.tier)}`)
  }
  if (change.pause?.length) out.push(`Pause the ${list(change.pause.map(collectorLabel))}`)
  if (change.resume?.length) out.push(`Resume the ${list(change.resume.map(collectorLabel))}`)
  if (change.exclude?.length) out.push(`Stop collecting from ${namespaces(change.exclude)}`)
  if (change.include?.length) out.push(`Collect from ${namespaces(change.include)} again`)
  return out
}

export const METRIC_LABEL: Record<string, string> = {
  link_bytes_per_sec: 'Traffic to the server',
  agent_cpu_cores: 'Agent CPU',
  agent_rss_bytes: 'Agent memory',
}
export const metricLabel = (m: string) => METRIC_LABEL[m] ?? m

/** "−50 %", "+20 %", "0 %": a relative change as a signed whole percentage. */
export function percent(v: number): string {
  const n = Math.round(v * 100)
  return n === 0 ? '0 %' : `${n > 0 ? '+' : '−'}${Math.abs(n)} %`
}

/** A metric's own value in its own unit. */
export function metricValue(metric: string, v: number): string {
  switch (metric) {
    case 'link_bytes_per_sec':
      return bytesPerSec(v)
    case 'agent_rss_bytes':
      return bytesTotal(v)
    case 'agent_cpu_cores':
      return v > 0 && v < 0.01 ? '<0.01 cores' : `${v.toFixed(2)} cores`
    default:
      return String(v)
  }
}

/** The predicted effects, in a stable order (the known metrics first, then any other by name). */
export function effectLines(predicted: MetricChanges): { metric: string; label: string; text: string }[] {
  const known = Object.keys(METRIC_LABEL)
  const names = Object.keys(predicted).sort((a, b) => (known.indexOf(a) + 1 || 99) - (known.indexOf(b) + 1 || 99) || a.localeCompare(b))
  return names.map((metric) => ({ metric, label: metricLabel(metric), text: percent(predicted[metric]) }))
}

export interface OutcomeRow {
  metric: string
  label: string
  predicted?: string
  before?: string
  after?: string
  observed?: string
}

/** The predicted change beside the observed one, per metric, for the metrics either side mentions. */
export function outcomeRows(o: ProposalOutcome): OutcomeRow[] {
  const names = new Set([...Object.keys(o.predicted ?? {}), ...Object.keys(o.observed?.change ?? {})])
  return effectLines(Object.fromEntries([...names].map((n) => [n, 0]))).map(({ metric, label }) => {
    const p = o.predicted?.[metric]
    const b = o.observed?.before?.[metric]
    const a = o.observed?.after?.[metric]
    const c = o.observed?.change?.[metric]
    return {
      metric,
      label,
      predicted: p === undefined ? undefined : percent(p),
      before: b === undefined ? undefined : metricValue(metric, b),
      after: a === undefined ? undefined : metricValue(metric, a),
      observed: c === undefined ? undefined : percent(c),
    }
  })
}

export const OUTCOME_WORDS: Record<ProposalOutcome['result'], string> = {
  measured: 'The agent’s own load was compared before and after the change.',
  unconfirmed: 'The agent did not report the change in force within 30 minutes, so nothing was compared.',
  no_data: 'The change is in force, but there were too few samples to compare (they are kept in memory, so a server restart in between leaves none).',
}

export const STATUS: Record<ProposalStatus, { label: string; tone: 'warn' | 'ok' | 'bad' | 'muted' }> = {
  pending: { label: 'Waiting for a decision', tone: 'warn' },
  accepted: { label: 'Being applied', tone: 'warn' },
  applied: { label: 'Applied', tone: 'ok' },
  rejected: { label: 'Rejected', tone: 'muted' },
  expired: { label: 'Expired', tone: 'muted' },
  failed: { label: 'Could not be applied', tone: 'bad' },
}

export const CLASS_WORDS: Record<EvidenceClass, { label: string; help: string }> = {
  measured: { label: 'Measured', help: 'Read from an agent or the server.' },
  derived: { label: 'Worked out', help: 'Calculated from measurements.' },
  declared: { label: 'Stated', help: 'Written down by a person or in a configuration, not checked.' },
  unknown: { label: 'Unknown source', help: 'The advisor did not say how it knows.' },
}

/**
 * Who may accept: a widening takes an administrator, a narrowing an editor (the server enforces both). A proposal that is
 * not waiting can not be decided at all.
 */
export function canAccept(role: Role | undefined, p: Pick<Proposal, 'widens' | 'status'>): boolean {
  return p.status === 'pending' && atLeast(role, p.widens ? 'admin' : 'editor')
}
export const canReject = (role: Role | undefined, p: Pick<Proposal, 'status'>) => p.status === 'pending' && atLeast(role, 'editor')

/** How long a waiting proposal still has: "3 h", "20 min", or "expired". */
export function timeLeft(expiresAt: string, now = Date.now()): string {
  const ms = new Date(expiresAt).getTime() - now
  if (!(ms > 0)) return 'expired'
  if (ms < 3_600_000) return `${Math.max(1, Math.round(ms / 60_000))} min`
  if (ms < 48 * 3_600_000) return `${Math.round(ms / 3_600_000)} h`
  return `${Math.round(ms / 86_400_000)} d`
}

/** Waiting proposals first (newest first within each group, as the server sends them), then the decided ones. */
export const waitingFirst = (ps: Proposal[]): Proposal[] => [...ps].sort((a, b) => Number(b.status === 'pending') - Number(a.status === 'pending'))

/** What one advisor has done so far, as the server counts it (GET /proposals/advisors). */
export interface AdvisorRecord {
  advisor: string
  proposals: number
  pending: number
  /** People said yes to these, whether or not the change then applied. */
  accepted: number
  /** Of the accepted ones, how many could not be made. */
  failed: number
  rejected: number
  expired: number
  /** Applied changes whose before and after load was compared, could not be confirmed, or had too little data. */
  measured: number
  unconfirmed: number
  noData: number
  /** Of the compared ones whose prediction could be checked: it went the predicted way and by at least half as much, or it did not. */
  held: number
  missed: number
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** The words for one row of the advisors table. Counts are given as they are: with this few decisions a percentage would say more than the data does. */
export function advisorSummary(r: AdvisorRecord): { proposed: string; decisions: string; predictions: string; predictionNote: string } {
  const decided: string[] = []
  if (r.accepted) decided.push(`${r.accepted} accepted`)
  if (r.rejected) decided.push(`${r.rejected} rejected`)
  if (r.expired) decided.push(`${r.expired} expired unanswered`)
  const checked = r.held + r.missed
  const notes: string[] = []
  if (r.failed) notes.push(`${plural(r.failed, 'accepted change', 'accepted changes')} could not be made`)
  if (r.unconfirmed) notes.push(`${r.unconfirmed} never confirmed by the agent`)
  if (r.noData) notes.push(`${r.noData} had too few samples`)
  if (r.measured > checked) notes.push(`${r.measured - checked} predicted nothing that could be checked`)
  return {
    proposed: r.pending ? `${r.proposals} (${r.pending} waiting)` : String(r.proposals),
    decisions: decided.length ? decided.join(', ') : 'Nothing decided yet',
    predictions: checked ? `${r.held} of ${checked} held` : '—',
    predictionNote: notes.join('; '),
  }
}

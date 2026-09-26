import type { Evidence, ObservationState, Provenance, Tombstone } from './types'

/**
 * What the system knows, and how it knows it: who said it, how sure, how fresh.
 *
 * Two ideas run through the UI and are decided here, in pure functions, so every page says the same thing:
 *  - a record is observed (live, disconnected, stale, revoked, gone) or declared by a person, and only live
 *    records are ever a target for anything;
 *  - a value is measured, reported, inferred, a guess, or unknown. Unknown is a value of its own, and is never
 *    shown as a zero.
 */

/* ---------- the effective model, as GET /api/v1/orgs/{org}/model returns it ---------- */

export type EvidenceLevel = 'measured' | 'reported' | 'inferred' | 'guess' | 'unknown'

export interface ModelAttr {
  /** null when the confidence is unknown. Never zero-as-unknown. */
  value: unknown
  unit?: string
  source: 'agent' | 'probe' | 'measured' | 'inferred' | 'declared'
  agentId?: string
  confidence: EvidenceLevel
  observedAt?: string
  state: string
  evidence?: string
  /** The observed value a declaration overrides. */
  shadowed?: ModelAttr
}

export interface ModelEntity {
  kind: string
  id: string
  name: string
  clusterId?: string
  origin: 'observed' | 'declared' | 'observed+declared'
  state: ObservationState | 'gone' | 'declared'
  stateReason?: string
  stateSince?: string
  lastObservedAt?: string
  goneAt?: string
  agentId?: string
  identity?: { basis: string; key?: string; aliases?: string[]; note?: string }
  attributes: Record<string, ModelAttr>
}

export interface EffectiveModel {
  contract: string
  modelVersion: number
  generatedAt: string
  observation: { staleAfterSeconds: number; tombstoneRetentionDays: number }
  entities: ModelEntity[]
  warnings: string[]
}

/** The entities of a model by kind and id. */
export function indexModel(m: EffectiveModel | undefined): Map<string, ModelEntity> {
  return new Map((m?.entities ?? []).map((e) => [`${e.kind}|${e.id}`, e]))
}

/* ---------- ages ---------- */

/** "45 s", "12 min", "2 h", "3 d": how a person reads an age. The server writes its reasons the same way. */
export function ageLabel(fromIso: string | undefined, now: number = Date.now()): string {
  if (!fromIso) return ''
  const t = Date.parse(fromIso)
  if (!Number.isFinite(t)) return ''
  const s = Math.max(0, (now - t) / 1000)
  if (s < 90) return `${Math.round(s)} s`
  if (s < 59.5 * 60) return `${Math.round(s / 60)} min`
  if (s < 48 * 3600) return `${Math.round(s / 3600)} h`
  return `${Math.round(s / 86400)} d`
}

/* ---------- observation state ---------- */

export type ObsKind = ObservationState | 'gone'
export type Tone = 'ok' | 'warn' | 'bad' | 'muted'

/** The app's one ok/warn/bad/muted palette. Reuse this for any new status/verdict badge instead of hand-picking
 * emerald/amber/red shades again - map your enum to a `Tone` (see `VERDICT_TONE` in lib/movability.ts for the
 * pattern), then look up its classes here. */
export const TONE_CLASS: Record<Tone, string> = {
  ok: 'border-ok/30 bg-ok/10 text-ok',
  warn: 'border-warn/30 bg-warn/10 text-warn',
  bad: 'border-bad/30 bg-bad/10 text-bad',
  muted: 'border-nb-700 bg-nb-930 text-nb-400',
}

export interface ObsInfo {
  kind: ObsKind
  /** The chip text: live · stale 2 h · disconnected · revoked · gone. */
  label: string
  /** The sentence behind it. */
  reason?: string
  tone: Tone
  /** Whether anything may act on the record (place workloads on it, recommend moves to it). */
  actionable: boolean
}

const TONE: Record<ObsKind, Tone> = { live: 'ok', disconnected: 'warn', stale: 'warn', revoked: 'bad', gone: 'muted' }

/**
 * The observation state of a record, or undefined for one nobody observes (typed by hand, or a sample): those have
 * nothing to be stale about.
 */
export function observation(rec: Pick<Provenance, 'source' | 'state' | 'stateReason' | 'stale' | 'deletedAt'> | undefined, gone?: { at?: string; reason?: string }): ObsInfo | undefined {
  if (!rec || rec.source !== 'discovered') return undefined
  if (rec.deletedAt || gone) {
    return { kind: 'gone', label: 'gone', reason: gone?.reason ?? 'no longer reported by its agent', tone: TONE.gone, actionable: false }
  }
  const kind: ObservationState = rec.state ?? (rec.stale ? 'stale' : 'live')
  let label: string = kind
  if (kind === 'stale') {
    const m = /stale for (.+)$/.exec(rec.stateReason ?? '')
    label = m ? `stale ${m[1]}` : 'stale'
  }
  return { kind, label, reason: kind === 'live' ? undefined : rec.stateReason || undefined, tone: TONE[kind], actionable: kind === 'live' }
}

/** A tombstone as a chip. */
export function goneInfo(t: Pick<Tombstone, 'goneAt' | 'reason'>, now: number = Date.now()): ObsInfo {
  const age = ageLabel(t.goneAt, now)
  return { kind: 'gone', label: 'gone', reason: `${t.reason}${age ? ` (${age} ago)` : ''}`, tone: 'muted', actionable: false }
}

/* ---------- placement targets ---------- */

export interface TargetStatus {
  eligible: boolean
  state: ObsKind | 'declared'
  /** Why it is not: "stale for 2 h", "agent revoked 3 h ago", "capacity unknown". */
  reason?: string
}

/**
 * May workloads be placed on this cluster? Only if it is live, and (when an agent observes it) its capacity is
 * known: a target whose room cannot be checked is not a target, and unknown is not zero. A cluster a person
 * declared has no observation; its unknowns are reported as unchecked, not excluded.
 */
export function targetStatus(c: Pick<Provenance, 'source' | 'state' | 'stateReason' | 'stale' | 'deletedAt'>, capacityKnown: boolean): TargetStatus {
  const o = observation(c)
  if (!o) return { eligible: true, state: 'declared' }
  if (!o.actionable) return { eligible: false, state: o.kind, reason: o.reason ?? (o.kind === 'gone' ? 'the cluster is gone' : o.label) }
  if (!capacityKnown) return { eligible: false, state: o.kind, reason: 'capacity unknown: no node reported what it has free' }
  return { eligible: true, state: o.kind }
}

/* ---------- evidence ---------- */

export const EVIDENCE_LABEL: Record<EvidenceLevel, string> = {
  measured: 'measured',
  reported: 'reported',
  inferred: 'inferred',
  guess: 'guess',
  unknown: 'unknown',
}

export const EVIDENCE_HELP: Record<EvidenceLevel, string> = {
  measured: 'Read from the thing itself by an instrument: a node probe, a timed connection, counted traffic.',
  reported: 'An agent read it from the Kubernetes API, or a person stated it. Taken at its word.',
  inferred: 'Worked out by a rule from other facts. The signal is named.',
  guess: 'Inferred from weak signals. Likely to be wrong sometimes.',
  unknown: 'Not known. This is not zero, empty or false.',
}

export const EVIDENCE_TONE: Record<EvidenceLevel, Tone> = { measured: 'ok', reported: 'ok', inferred: 'muted', guess: 'warn', unknown: 'warn' }

/** Should a chip be shown for this? Reported and measured values are the normal case and carry none. */
export const needsEvidenceChip = (c: EvidenceLevel) => c === 'guess' || c === 'unknown' || c === 'inferred'

/**
 * How sure the interpreter's own evidence is, in the same words the server uses in the model: a probed value
 * that it rates high was measured; otherwise high and medium are inferred and low is a guess.
 */
export function evidenceLevel(ev: Pick<Evidence, 'confidence'> | undefined, probed = false): EvidenceLevel {
  if (!ev) return 'inferred'
  if (probed) return ev.confidence === 'high' ? 'measured' : ev.confidence === 'medium' ? 'inferred' : 'guess'
  return ev.confidence === 'low' ? 'guess' : 'inferred'
}

export interface Weak {
  field: string
  label: string
  level: 'guess' | 'unknown'
  /** What the value rests on, or why it is not known. */
  why: string
}

type Loose = Record<string, unknown>

/**
 * The attributes of a discovered record that are guessed or not known, from the record alone (used where the
 * effective model is not at hand). A value a person set is theirs and is not weak.
 */
export function weakAttributes(kind: 'cluster' | 'node' | 'service', rec: Provenance & Loose): Weak[] {
  if (rec.source !== 'discovered') return []
  const out: Weak[] = []
  const overridden = (f: string) => rec.overrides !== undefined && f in rec.overrides
  const guess = (field: string, label: string) => {
    const ev = rec.evidence?.[field]
    if (ev && evidenceLevel(ev, rec.probed === true) === 'guess' && !overridden(field)) out.push({ field, label, level: 'guess', why: `${ev.signal}${ev.detail ? ` (${ev.detail})` : ''}` })
  }
  const unknown = (field: string, label: string, missing: boolean, why: string) => {
    if (missing && !overridden(field)) out.push({ field, label, level: 'unknown', why })
  }
  if (kind === 'cluster') {
    guess('distribution', 'Distribution')
    guess('provider', 'Provider')
    guess('tier', 'Tier')
    guess('region', 'Region')
    unknown('region', 'Region', !rec.region, 'no node carries a region label')
    unknown('version', 'Version', !rec.version, 'the agent did not report the API server version')
  } else if (kind === 'node') {
    guess('kind', 'Machine kind')
    guess('hardwareModel', 'Hardware')
    guess('virtualization', 'Virtualization')
    guess('connectivity', 'Connectivity')
    unknown('allocatable', 'Free capacity', !rec.allocatable, 'the node reported no allocatable resources')
    unknown('requested', 'Requested', !rec.requested, 'pods are not read at this access tier')
    unknown('arch', 'Architecture', !rec.arch, 'the node did not report it')
  } else {
    unknown('cpuRequestM', 'CPU request', !rec.cpuRequestM, 'no CPU request is set, so nothing is reserved and the need is not known')
    unknown('memRequestMi', 'Memory request', !rec.memRequestMi, 'no memory request is set, so nothing is reserved and the need is not known')
  }
  return out
}

/* ---------- formatting a model attribute ---------- */

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

/** A value with its unit, the way a person reads it. Unknown says so. */
export function formatAttr(a: ModelAttr): string {
  if (a.confidence === 'unknown' || a.value === null || a.value === undefined) return 'unknown'
  const v = a.value
  if (typeof v === 'number') {
    if (a.unit === 'bytes') {
      let n = v
      let i = 0
      while (n >= 1024 && i < BYTE_UNITS.length - 1) {
        n /= 1024
        i++
      }
      return `${Number.isInteger(n) ? n : n.toFixed(n < 10 ? 2 : 1)} ${BYTE_UNITS[i]}`
    }
    const s = Number.isInteger(v) ? String(v) : String(Math.round(v * 1000) / 1000)
    return a.unit && a.unit !== 'count' ? `${s} ${a.unit}` : s
  }
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  if (Array.isArray(v)) return v.length === 0 ? 'none' : v.map((x) => (typeof x === 'object' ? JSON.stringify(x) : String(x))).join(', ')
  if (v && typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

/** Who said it, in words. */
export function sourceLabel(a: Pick<ModelAttr, 'source' | 'agentId'>, agentName?: string): string {
  const who = agentName ? ` (${agentName})` : ''
  switch (a.source) {
    case 'agent':
      return `agent${who}`
    case 'probe':
      return `node probe${who}`
    case 'measured':
      return `measured by the agent${who}`
    case 'inferred':
      return 'inferred by the server'
    case 'declared':
      return 'declared by a person'
  }
}

export interface EvidenceRow {
  attribute: string
  value: string
  unit?: string
  source: string
  confidence: EvidenceLevel
  observedAt?: string
  why?: string
  /** The observed value a declaration overrides. */
  shadowed?: string
}

/** The rows of an Evidence section for one entity: every attribute, weakest first, then by name. */
export function evidenceRows(e: ModelEntity | undefined, agentName?: (id: string) => string | undefined): EvidenceRow[] {
  if (!e) return []
  const rank: Record<EvidenceLevel, number> = { unknown: 0, guess: 1, inferred: 2, reported: 3, measured: 4 }
  return Object.entries(e.attributes)
    .map(([attribute, a]) => ({
      attribute,
      value: formatAttr(a),
      unit: a.unit && a.unit !== 'count' ? a.unit : undefined,
      source: sourceLabel(a, a.agentId ? agentName?.(a.agentId) : undefined),
      confidence: a.confidence,
      observedAt: a.observedAt,
      why: a.evidence,
      shadowed: a.shadowed ? formatAttr(a.shadowed) : undefined,
    }))
    .sort((x, y) => rank[x.confidence] - rank[y.confidence] || x.attribute.localeCompare(y.attribute))
}

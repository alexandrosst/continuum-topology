/**
 * Fits, does not fit, or can't tell: the rules behind every placement answer, and how far to trust each.
 *
 * This is the browser's copy of backend/internal/advice (the server uses the same rules for external deciders). The
 * two are kept identical by backend/internal/advice/testdata/vectors.json, which both test suites read: if you change
 * a rule, change it in both places and regenerate the vectors. The rules, in words (docs/advice.md has them in full):
 *
 *  1. Unknown is not zero. A fact with no value has no interval; the answer is "can't tell" and the reason names it.
 *  2. Each class widens a value by a fixed factor: measured ±5 %, reported ±15 %, inferred ±30 %, guess ±50 %.
 *  3. A fact last confirmed longer ago than the staleness window counts as one class worse (a guess stays a guess).
 *  4. Free capacity is allocatable minus requested per node, pooled over the nodes that could host the workload. With
 *     requested unknown the node is only bounded above by what it has; with allocatable unknown there is no bound.
 *  5. "Fits" needs the low end of the interval to cover the need; "does not fit" needs the high end to fall short;
 *     anything in between is "can't tell", with the amounts at which it would flip.
 *  6. Verdicts combine by severity: any definite no wins, then any can't-tell, then fits.
 *  7. Confidence is the weakest class among the facts that decide the answer (never an average): measured and reported
 *     are high, inferred medium, a guess low, unknown none; a can't-tell is never above low.
 */
import type { EvidenceLevel } from './provenance'

export type Class = EvidenceLevel
export type Verdict = 'fits' | 'cantTell' | 'doesNotFit'
export type Level = 'high' | 'medium' | 'low' | 'none'

/** Relative half-width of the interval for each class. Unknown has none: it has no interval. */
export const FACTORS: Record<Exclude<Class, 'unknown'>, number> = { measured: 0.05, reported: 0.15, inferred: 0.3, guess: 0.5 }

export const factor = (c: Class): number | undefined => (c === 'unknown' ? undefined : FACTORS[c])

const RANK: Record<Class, number> = { measured: 4, reported: 3, inferred: 2, guess: 1, unknown: 0 }
export const rankOf = (c: Class) => RANK[c] ?? 0
export const worse = (a: Class, b: Class): Class => (rankOf(a) <= rankOf(b) ? norm(a) : norm(b))
const norm = (c: Class): Class => (rankOf(c) === 0 ? 'unknown' : c)
const DEMOTE: Record<Class, Class> = { measured: 'reported', reported: 'inferred', inferred: 'guess', guess: 'guess', unknown: 'unknown' }
export const demote = (c: Class): Class => DEMOTE[c] ?? 'unknown'

/** Any definite no wins, otherwise any can't-tell, otherwise fits. */
export function combine(vs: Verdict[]): Verdict {
  let out: Verdict = 'fits'
  for (const v of vs) {
    if (v === 'doesNotFit') return 'doesNotFit'
    if (v === 'cantTell') out = 'cantTell'
  }
  return out
}

export const levelOf = (c: Class): Level => (c === 'measured' || c === 'reported' ? 'high' : c === 'inferred' ? 'medium' : c === 'guess' ? 'low' : 'none')
const LEVEL_RANK: Record<Level, number> = { high: 3, medium: 2, low: 1, none: 0 }
export const weakerLevel = (a: Level, b: Level): Level => (LEVEL_RANK[a] <= LEVEL_RANK[b] ? a : b)

/** One input: a value with its class and age. `value` is null or undefined when it is not known. */
export interface Fact {
  value?: number | null
  class: Class
  /** When the source last vouched for it, in ms since the epoch. Absent: it has no age (a person typed it). */
  observedAt?: number
}

export const known = (f: Fact | undefined): f is Fact & { value: number } => !!f && f.value !== null && f.value !== undefined && Number.isFinite(f.value) && rankOf(f.class) > 0

/** The class the fact counts as at `now`: one worse when it was last confirmed longer ago than the window. */
export function effective(f: Fact, now: number, windowMs: number): { class: Class; aged: boolean } {
  if (!known(f)) return { class: 'unknown', aged: false }
  if (f.observedAt !== undefined && windowMs > 0 && now - f.observedAt > windowMs) return { class: demote(f.class), aged: true }
  return { class: f.class, aged: false }
}

/** One node's contribution to a pool. */
export interface CapNode {
  id: string
  name: string
  allocatable: Fact
  requested: Fact
  /** Why requested is unknown, when it is. */
  requestedWhy?: string
}

/** The free capacity of a group of nodes as an interval. `hi` is Infinity when nothing bounds it from above. */
export interface Pool {
  lo: number
  hi: number
  /** Sum of the free amounts of the nodes where it is known (knownNodes of them). */
  knownFree: number
  knownNodes: number
  /** True when every node's free amount is known: knownFree is then the pool's free capacity as reported. */
  nominalKnown: boolean
  nodes: number
  /** Weakest effective class overall; of the nodes where free is known (a fit rests on those); of the upper end (a no rests on it). */
  class: Class
  loClass: Class
  hiClass: Class
  /** Oldest observation time among the facts (ms), and whether any was demoted for age. */
  oldest?: number
  aged: boolean
  notes: string[]
  /** The part of hi that comes from nodes whose free amount is known. */
  hiKnown: number
}

export function newPool(nodes: CapNode[], now: number, windowMs: number): Pool {
  if (nodes.length === 0) {
    return { lo: 0, hi: Infinity, knownFree: 0, knownNodes: 0, nominalKnown: false, nodes: 0, class: 'unknown', loClass: 'unknown', hiClass: 'unknown', aged: false, notes: ['no nodes are known'], hiKnown: 0 }
  }
  const p: Pool = { lo: 0, hi: 0, knownFree: 0, knownNodes: 0, nominalKnown: true, nodes: nodes.length, class: 'measured', loClass: 'measured', hiClass: 'measured', aged: false, notes: [], hiKnown: 0 }
  for (const n of nodes) {
    const a = effective(n.allocatable, now, windowMs)
    const r = effective(n.requested, now, windowMs)
    p.aged = p.aged || a.aged || r.aged
    for (const f of [n.allocatable, n.requested]) {
      if (known(f) && f.observedAt !== undefined && (p.oldest === undefined || f.observedAt < p.oldest)) p.oldest = f.observedAt
    }
    if (a.aged || r.aged) p.notes.push(`${n.name || n.id} was last confirmed more than ${roundDur(windowMs)} ago, so its figures count as one class worse`)
    if (!known(n.allocatable)) {
      p.hi = Infinity
      p.nominalKnown = false
      p.class = p.hiClass = 'unknown'
      p.notes.push(`the allocatable resources of ${n.name || n.id} are not reported`)
    } else if (!known(n.requested)) {
      p.hi += n.allocatable.value * (1 + (factor(a.class) ?? 0))
      p.nominalKnown = false
      p.class = 'unknown'
      p.hiClass = worse(p.hiClass, a.class)
      p.notes.push(`what is already requested on ${n.name || n.id} is not known (${n.requestedWhy || 'pods are not read there'}), so its free capacity is only bounded by what it has`)
    } else {
      const free = Math.max(0, n.allocatable.value - n.requested.value)
      const c = worse(a.class, r.class)
      const f = factor(c) ?? 0
      p.lo += free * (1 - f)
      p.hiKnown += free * (1 + f)
      p.hi += free * (1 + f)
      p.knownFree += free
      p.knownNodes++
      p.class = worse(p.class, c)
      p.loClass = worse(p.loClass, c)
      p.hiClass = worse(p.hiClass, c)
    }
  }
  if (p.knownNodes === 0) p.loClass = 'unknown'
  return p
}

function roundDur(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 90) return `${s} s`
  if (s < 90 * 60) return `${Math.round(s / 60)} min`
  return `${Math.round(s / 3600)} h`
}

/** The pool after workloads move in (negative delta) or out (positive). A delta of null (no request) removes the floor. */
export function adjustPool(p: Pool, delta: number | null): Pool {
  if (delta === null) return { ...p, lo: 0, knownFree: 0, knownNodes: 0, hiKnown: 0, nominalKnown: false, class: 'unknown', loClass: 'unknown', notes: [...p.notes, 'a workload moved in has no request, so what it takes is not known'] }
  return { ...p, lo: Math.max(0, p.lo + delta), hi: Math.max(0, p.hi + delta), hiKnown: Math.max(0, p.hiKnown + delta), knownFree: Math.max(0, p.knownFree + delta) }
}

/** The pool with the known nodes' free capacity scaled to total `free`: how "what would change this" is stated and checked. */
export function scaledPool(p: Pool, free: number): Pool {
  if (p.knownNodes > 0 && p.knownFree > 0) {
    const s = free / p.knownFree
    const extra = p.hi - p.hiKnown
    const hiKnown = p.hiKnown * s
    return { ...p, lo: p.lo * s, hiKnown, knownFree: free, hi: hiKnown + extra }
  }
  const c: Class = rankOf(p.loClass) === 0 ? 'reported' : p.loClass
  const f = factor(c) ?? 0
  let extra = p.hi - p.hiKnown
  let nominalKnown = p.nominalKnown
  let knownNodes = p.knownNodes
  if (p.knownNodes === 0) {
    extra = 0
    nominalKnown = true
    knownNodes = 1
  }
  const hiKnown = free * (1 + f)
  return { ...p, lo: free * (1 - f), hiKnown, knownFree: free, hi: hiKnown + extra, nominalKnown, knownNodes, class: c, loClass: c, hiClass: c }
}

export interface Dimension {
  name: 'cpu' | 'memory'
  /** How the dimension is written in a sentence ("CPU", "memory"). */
  label: string
  unit: string
  fmt: (v: number) => string
}

const trim = (v: number): string => {
  if (v >= 100) return v.toFixed(0)
  if (v >= 10) return String(Number(v.toFixed(1)))
  return String(Number(v.toFixed(2)))
}
export const GIB = 1024 ** 3
export const CPU: Dimension = { name: 'cpu', label: 'CPU', unit: 'cores', fmt: (v) => `${trim(v)} cores` }
/** Memory is in bytes in the model and shown in GiB. */
export const MEMORY: Dimension = { name: 'memory', label: 'memory', unit: 'bytes', fmt: (v) => `${trim(v / GIB)} GiB` }

/** What the workload asks for in one dimension, all replicas together. `value` is null or undefined when no request is set. */
export interface Need {
  value?: number | null
  class: Class
}

export interface Change {
  dimension: string
  /** The derived fact the change is about, for example "memoryFree". */
  attribute: string
  unit: string
  /** The verdict changes when the fact is at least, or below, the threshold. */
  direction: 'atLeast' | 'below'
  threshold: number
  current: number | null
  becomes: Verdict
  text: string
}

export interface Fix {
  action: string
  text: string
  link?: string
}

export interface DimResult {
  dimension: Dimension
  verdict: Verdict
  need: Need
  pool: Pool
  /** The weakest class among what the verdict rests on. */
  class: Class
  reason: string
  /** How far free capacity could be below what is reported before it stops fitting; negative when it already does not. */
  margin?: number
  wouldChange: Change[]
  fix?: Fix
}

const orUnknown = (n: Need): Class => (n.value === null || n.value === undefined ? 'unknown' : rankOf(n.class) === 0 ? 'reported' : n.class)
const pct = (f: number) => `${Math.round(f * 100)} %`

export function fit(dim: Dimension, need: Need, pool: Pool, where: string): DimResult {
  const base = { dimension: dim, need, pool, wouldChange: [] as Change[] }
  if (need.value === null || need.value === undefined) {
    return { ...base, verdict: 'cantTell', class: 'unknown', reason: `the workload sets no ${dim.label} request, so what it needs is not known` }
  }
  if (need.value <= 0) return { ...base, verdict: 'fits', class: 'reported', reason: `it asks for no ${dim.label}` }
  const n = need.value
  let verdict: Verdict
  let cls: Class
  if (pool.hi < n) {
    verdict = 'doesNotFit'
    cls = worse(pool.hiClass, orUnknown(need))
  } else if (pool.lo >= n) {
    verdict = 'fits'
    cls = worse(pool.loClass, orUnknown(need))
  } else {
    verdict = 'cantTell'
    cls = worse(pool.class, orUnknown(need))
  }
  const r: DimResult = { ...base, verdict, class: cls, reason: '' }
  if (pool.knownNodes > 0 && pool.knownFree > 0) r.margin = 1 - n / pool.knownFree
  r.wouldChange = wouldChange(dim, n, pool, verdict, where)
  r.reason = reasonFor(dim, n, pool, r, where)
  return r
}

function thresholdFits(p: Pool, n: number): number {
  if (p.knownNodes > 0 && p.knownFree > 0 && p.lo > 0) return (p.knownFree * n) / p.lo
  const f = factor(p.loClass) ?? FACTORS.reported
  return n / (1 - f)
}

function thresholdDoesNot(p: Pool, n: number): number | undefined {
  const extra = p.hi - p.hiKnown
  if (!Number.isFinite(extra) || extra >= n) return undefined
  if (p.knownNodes > 0 && p.knownFree > 0 && p.hiKnown > 0) return (p.knownFree * (n - extra)) / p.hiKnown
  return undefined
}

function wouldChange(dim: Dimension, n: number, p: Pool, v: Verdict, where: string): Change[] {
  const attribute = `${dim.name}Free`
  const current = p.knownNodes > 0 ? p.knownFree : null
  const mk = (direction: Change['direction'], threshold: number, becomes: Verdict, text: string): Change => ({ dimension: dim.name, attribute, unit: dim.unit, direction, threshold, current, becomes, text })
  const fitAt = thresholdFits(p, n)
  const dnf = thresholdDoesNot(p, n)
  if (v === 'fits') return [mk('below', fitAt, 'cantTell', `if free ${dim.label} at ${where} is below ${dim.fmt(fitAt)} it can no longer be said to fit`)]
  if (v === 'cantTell') {
    if (p.knownNodes === 0) return [mk('atLeast', fitAt, 'fits', `if free ${dim.label} at ${where} were reported, it would fit at ${dim.fmt(fitAt)} or more`)]
    const out = [mk('atLeast', fitAt, 'fits', `if free ${dim.label} at ${where} is at least ${dim.fmt(fitAt)} it fits`)]
    if (dnf !== undefined) out.push(mk('below', dnf, 'doesNotFit', `if free ${dim.label} at ${where} is below ${dim.fmt(dnf)} it does not fit`))
    return out
  }
  if (dnf === undefined) return []
  return [
    mk('atLeast', dnf, 'cantTell', `if free ${dim.label} at ${where} reaches ${dim.fmt(dnf)} it is no longer ruled out`),
    mk('atLeast', fitAt, 'fits', `if free ${dim.label} at ${where} reaches ${dim.fmt(fitAt)} it fits`),
  ]
}

function reasonFor(dim: Dimension, n: number, p: Pool, r: DimResult, where: string): string {
  const need = dim.fmt(n)
  const l = dim.label
  if (p.nominalKnown) {
    const f = factor(p.class) ?? 0
    const free = `${dim.fmt(p.knownFree)} free (${p.class}, ±${pct(f)})`
    if (r.verdict === 'fits') return `${l} at ${where}: needs ${need}, ${free}; it fits even at the low end (${dim.fmt(p.lo)})`
    if (r.verdict === 'doesNotFit') return `not enough free ${l} at ${where}: needs ${need}, ${free}; it does not fit even at the high end (${dim.fmt(p.hi)})`
    return `${l} at ${where} is ${p.class}: needs ${need}, ${free} (${dim.fmt(p.lo)} to ${dim.fmt(p.hi)}); it fits if free ${l} is at least ${dim.fmt(thresholdFits(p, n))}`
  }
  const why = p.notes.join('; ') || 'not reported'
  if (r.verdict === 'doesNotFit') return `not enough free ${l} at ${where}: needs ${need} but even all of what the nodes have (${dim.fmt(p.hi)}) is less; ${why}`
  if (r.verdict === 'fits') return `${l} at ${where}: needs ${need} and the nodes that report it have at least ${dim.fmt(p.lo)} free; ${why}`
  return `free ${l} at ${where} is not known: ${why}; it needs ${need}`
}

/** The verdict this dimension would have if the derived free fact were `delta` away from a change's threshold. */
export const applyChange = (r: DimResult, c: Change, delta: number): Verdict => fit(r.dimension, r.need, scaledPool(r.pool, c.threshold + delta), '').verdict

/** "still fits if free memory is off by up to 22 %": how fragile the verdict is, from the same interval. */
export function sensitivityText(r: DimResult): string {
  if (r.margin === undefined) return ''
  const m = r.margin
  const f = factor(r.pool.class) ?? 0
  const name = r.dimension.label
  if (r.verdict === 'fits') return `still fits if free ${name} is off by up to ${pct(Math.min(m, 1))}`
  if (r.verdict === 'doesNotFit') return `would need ${pct(-m)} more free ${name} than reported`
  if (m > 0) return `fits only if the reported free ${name} is off by less than ${pct(m)}; the source allows ±${pct(f)}`
  return `needs ${pct(-m)} more free ${name} than reported to fit; the source allows ±${pct(f)}`
}

/** A yes/no constraint that may also be unknown: architecture, data residency, trust zone, node selectors. */
export interface Check {
  name: string
  verdict: Verdict
  class: Class
  reason: string
  fix?: Fix
}

export interface Advice {
  verdict: Verdict
  /** The weakest class among what decides the verdict, and that as a level (never above low for a can't-tell). */
  class: Class
  confidence: Level
  dims: DimResult[]
  checks: Check[]
  /** One line for everything that is not a plain pass, worst first. */
  reasons: string[]
  fixes: Fix[]
  changes: Change[]
}

const SEV: Record<Verdict, number> = { doesNotFit: 2, cantTell: 1, fits: 0 }

/** Combine dimensions and checks. The class is the weakest among the parts that decide: for a fit all of them, for a no the parts that fail, for a can't-tell the parts that could not be decided. */
export function assess(dims: DimResult[], checks: Check[]): Advice {
  const verdict = combine([...dims.map((d) => d.verdict), ...checks.map((c) => c.verdict)])
  let cls: Class = 'reported'
  for (const d of dims) if (d.verdict === verdict) cls = worse(cls, d.class)
  for (const c of checks) if (c.verdict === verdict) cls = worse(cls, c.class)
  let confidence = levelOf(cls)
  if (verdict === 'cantTell') confidence = weakerLevel(confidence, 'low')
  const lines: { v: Verdict; s: string }[] = []
  const fixes: Fix[] = []
  const addFix = (f: Fix | undefined) => {
    if (f && !fixes.some((x) => x.action === f.action && x.text === f.text)) fixes.push(f)
  }
  const changes: Change[] = []
  for (const d of dims) {
    if (d.verdict !== 'fits') {
      lines.push({ v: d.verdict, s: d.reason })
      addFix(d.fix)
    }
    if (d.verdict === verdict) changes.push(...d.wouldChange)
  }
  for (const c of checks) {
    if (c.verdict !== 'fits') {
      lines.push({ v: c.verdict, s: c.reason })
      addFix(c.fix)
    }
  }
  lines.sort((a, b) => SEV[b.v] - SEV[a.v])
  return { verdict, class: cls, confidence, dims, checks, reasons: lines.map((l) => l.s), fixes, changes }
}

/* ---------- what is sent and shown: facts ---------- */

/** A fact as it is listed under "Why": what was used, where it came from, how sure, and how old. */
export interface FactRow {
  attribute: string
  entity?: string
  entityName?: string
  /** null when not known: unknown is never zero. */
  value: number | string | null
  unit?: string
  source: string
  confidence: Class
  /** ISO time the source last vouched for it. */
  observedAt?: string
  state?: string
  evidence?: string
  low?: number | null
  high?: number | null
  aged?: boolean
}

/** Cheap, stable text for a value with its unit, for a facts list. Unknown says so. */
export function formatFactValue(v: number | string | null, unit?: string): string {
  if (v === null) return 'unknown'
  if (typeof v === 'string') return v
  if (unit === 'bytes') return `${trim(v / GIB)} GiB`
  if (unit === 'cores') return `${trim(v)} cores`
  if (unit === 'ms') return `${trim(v)} ms`
  if (unit === 'bytes/s') return v >= 1e6 ? `${(v / 1e6).toFixed(1)} MB/s` : v >= 1e3 ? `${(v / 1e3).toFixed(0)} KB/s` : `${Math.round(v)} B/s`
  return unit ? `${trim(v)} ${unit}` : trim(v)
}

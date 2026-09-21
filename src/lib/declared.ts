import type { DeclaredRef, Model } from './types'

/**
 * The declared side of the twin: what people said, kept apart from what agents observed.
 *
 * The workspace document (and Export) holds sites, devices, declared links, decisions and the values people
 * overrode. It does not hold clusters, nodes, namespaces or services that discovery produced: those are the
 * server's observations and arrive with every poll. What a person said about one of them (overrides, the
 * application or site they assigned) is kept as a ref, keyed by the record's stable id, so it survives while the
 * record itself comes and goes.
 *
 * These functions mirror internal/workspace on the server, which applies the same rules to whatever it is sent.
 */

type RefKind = DeclaredRef['kind']
const KINDS: { key: 'clusters' | 'nodes' | 'namespaces' | 'services'; kind: RefKind }[] = [
  { key: 'clusters', kind: 'cluster' },
  { key: 'nodes', kind: 'node' },
  { key: 'namespaces', kind: 'namespace' },
  { key: 'services', kind: 'service' },
]

/** Fields that describe an observation, not a decision. Removed from records a person accepted. */
const OBSERVED_META = ['lastSeen', 'detectedAt', 'revision', 'stale', 'evidence', 'agentId', 'state', 'stateReason'] as const

export interface DeclareReport {
  /** Observed records removed, by kind. */
  stripped: Record<string, number>
  /** How many left a ref behind (a person had overridden or assigned something on them). */
  refs: number
  /** Other observed data removed: open suggestions, observed dependencies, measured links, agents. */
  dropped: Record<string, number>
}

const isRefEmpty = (r: DeclaredRef) => !(r.overrides && Object.keys(r.overrides).length > 0) && !r.applicationId && !r.siteId

type Rec = { id: string; source?: string; overrides?: Record<string, unknown>; applicationId?: string; applicationHint?: string; siteId?: string } & Record<string, unknown>

/** Is this a record an agent produced (as opposed to one a person typed)? */
export const isDiscovered = (r: { source?: string }) => r.source === 'discovered'

/** The ref a discovered record leaves behind: only what a person said about it. */
export function refOf(kind: RefKind, r: Rec): DeclaredRef | undefined {
  const ref: DeclaredRef = { kind }
  // An application the service's own label suggests (applicationHint) is derived, not something a person chose.
  if (r.applicationId && r.applicationId !== r.applicationHint) ref.applicationId = r.applicationId
  if (kind === 'cluster' && r.siteId) ref.siteId = r.siteId
  if (r.overrides && Object.keys(r.overrides).length > 0) ref.overrides = r.overrides
  return isRefEmpty(ref) ? undefined : ref
}

/**
 * Reduce a model to what people declared. Discovered records are removed (their overrides and assignments become
 * refs), and so is everything else that only an agent knows. Idempotent.
 */
export function toDeclared(m: Model): { model: Model; report: DeclareReport } {
  const report: DeclareReport = { stripped: {}, refs: 0, dropped: {} }
  const refs: Record<string, DeclaredRef> = { ...m.refs }
  const lists: Partial<Record<(typeof KINDS)[number]['key'], unknown[]>> = {}
  for (const { key, kind } of KINDS) {
    const kept: unknown[] = []
    for (const r of m[key] as unknown as Rec[]) {
      if (!isDiscovered(r)) {
        kept.push(r)
        continue
      }
      report.stripped[kind] = (report.stripped[kind] ?? 0) + 1
      const ref = refOf(kind, r)
      if (ref && r.id) refs[r.id] = ref
    }
    lists[key] = kept
  }
  for (const id of Object.keys(refs)) if (!id || isRefEmpty(refs[id])) delete refs[id]
  report.refs = Object.keys(refs).length

  const clean = <T extends object>(l: T[]): T[] =>
    l.map((r) => {
      const c = { ...r } as Record<string, unknown>
      for (const k of OBSERVED_META) delete c[k]
      return c as T
    })
  const dropped = (what: string, n: number) => {
    if (n > 0) report.dropped[what] = n
  }
  const suggestions = m.suggestions.filter((s) => s.status === 'accepted' || s.status === 'dismissed')
  dropped('open suggestions', m.suggestions.length - suggestions.length)
  let observedDeps = 0
  const dependencies = m.dependencies.flatMap((d) => {
    const seen = d.sources.includes('observed')
    if (seen && d.sources.every((s) => s === 'observed')) {
      observedDeps++
      return []
    }
    return [seen ? { ...d, sources: d.sources.filter((s) => s !== 'observed') } : d]
  })
  dropped('observed dependencies', observedDeps)
  const siteLinks = m.siteLinks.filter((l) => l.source !== 'measured').map((l) => ({ ...l, measuredAt: undefined }))
  dropped('measured links', m.siteLinks.length - siteLinks.length)
  dropped('agents', m.agents.length)

  const model: Model = {
    ...m,
    ...(lists as Pick<Model, 'clusters' | 'nodes' | 'namespaces' | 'services'>),
    applications: clean(m.applications),
    externalEndpoints: clean(m.externalEndpoints),
    devices: clean(m.devices),
    suggestions,
    dependencies,
    siteLinks,
    agents: [],
    auditLog: m.auditLog.filter((e) => e.id.startsWith('ev-')),
    refs,
  }
  return { model, report }
}

const plural = (w: string, n: number) => (n === 1 ? w : w === 'service' || w === 'cluster' || w === 'node' || w === 'namespace' || w === 'record' || w === 'override' ? w + 's' : w)

/** The sentence shown when a document was reduced; empty when there was nothing observed in it. */
export function declaredNote(r: DeclareReport): string {
  const kinds = Object.keys(r.stripped).sort()
  const total = kinds.reduce((a, k) => a + r.stripped[k], 0)
  const other = Object.keys(r.dropped).sort()
  if (total === 0 && other.length === 0) return ''
  let s = 'Removed observed data from the workspace'
  if (total > 0) s = `Removed ${total} discovered ${plural('record', total)} (${kinds.map((k) => `${r.stripped[k]} ${plural(k, r.stripped[k])}`).join(', ')}) from the workspace`
  s += ': observed data is kept apart from what people declare, and agents will report it again.'
  if (r.refs > 0) s += ` Your ${r.refs} ${plural('override', r.refs)} and assignments on discovered records were kept.`
  if (other.length > 0) s += ` Also dropped: ${other.map((k) => `${r.dropped[k]} ${k}`).join(', ')}.`
  return s
}

/** Put a ref's overrides and assignments onto a discovered record. */
export function applyRef<T extends Rec>(record: T, ref: DeclaredRef | undefined, kind: RefKind): T {
  const out: Rec = { ...record, overrides: ref?.overrides }
  if (ref?.applicationId) out.applicationId = ref.applicationId
  else if (out.applicationId && !record.applicationHint) delete out.applicationId
  if (kind === 'cluster') {
    if (ref?.siteId) out.siteId = ref.siteId
    else delete out.siteId
  }
  return out as T
}

/**
 * Bring a loaded workspace (declared only) together with the discovered records this browser already holds, so a
 * reload of the workspace does not empty the pages until the next poll. The workspace's refs are applied to
 * the held records; refs for records not held stay in `refs` until discovery reports them.
 */
export function rehydrate(incoming: Model, held: Model): Model {
  const refs = { ...incoming.refs }
  const out: Model = { ...incoming, refs }
  for (const { key, kind } of KINDS) {
    const list = [...(incoming[key] as unknown as Rec[])]
    for (const r of held[key] as unknown as Rec[]) {
      if (!isDiscovered(r)) continue
      const ref = refs[r.id]?.kind === kind ? refs[r.id] : undefined
      delete refs[r.id]
      list.push(applyRef(r, ref, kind))
    }
    ;(out as unknown as Record<string, unknown>)[key] = list
  }
  return out
}

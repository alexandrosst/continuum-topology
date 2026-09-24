import { ipScope } from './present'
import { applyRef } from './declared'
import type { AccessTier, Agent, AgentLink, AgentScope, AuditEvent, ConsistencyStatus, DeclaredRef, ObserverStatus, Path, Cluster, Dependency, ExternalEndpoint, GeoHint, GeoUnlocatableReason, MachineNode, Model, Namespace, Service, Suggestion, Tombstone } from './types'

/** What the Continuum server returns from GET /api/v1/state. */
export interface ServerAgent {
  id: string
  orgId: string
  name: string
  clusterId?: string
  version: string
  kubernetesVersion?: string
  accessTier: AccessTier
  installedTier: AccessTier
  tierCap: AccessTier
  status: Agent['status']
  fingerprint: string
  connectingIp?: string
  /** Where the GeoIP database (when the server has one) places `connectingIp`. */
  connectingGeo?: GeoHint
  /** Set only when connectingGeo is absent because connectingIp itself could not be located at all -
   * never when it was simply not in the database. See GeoUnlocatableReason. */
  connectingGeoReason?: GeoUnlocatableReason
  certExpiresAt?: string
  lastHeartbeat?: string
  requestedAt: string
  reason?: string
  connected: boolean
  /** The server holds a full set of facts from this agent, so records it no longer reports are really gone. */
  synced: boolean
  modules: { name: string; status: 'ok' | 'skipped' | 'error'; reason?: string }[]
  /** What the traffic observer says about itself; absent until a collector has reported. */
  observer?: ObserverStatus
  consistency?: ConsistencyStatus
  measuring?: number
  /** What this agent has sent since the server started. */
  link?: AgentLink
  scope?: AgentScope
  /** The agent's clock minus the server's, in milliseconds (absent when in step or not measured). */
  clockSkewMs?: number
  /** Pending agents: an older agent with no approval code, the attempts left, and when the request runs out. */
  legacyEnrollment?: boolean
  approvalAttemptsLeft?: number
  pendingExpiresAt?: string
}

export interface ServerState {
  generatedAt: string
  agents: ServerAgent[]
  topology: {
    clusters: Cluster[]
    nodes: MachineNode[]
    namespaces: Namespace[]
    services: Service[]
    suggestions: Suggestion[]
    /** Seen on the wire: derived on every request, never part of the stored workspace. */
    dependencies: Dependency[]
    externalEndpoints: ExternalEndpoint[]
    /** Measured network paths between clusters and the addresses they talk to. */
    paths: Path[]
  }
  auditLog: AuditEvent[]
  /** Records that disappeared from what their agents report, kept for a retention window (see Tombstone). */
  tombstones: Tombstone[]
  /** The rules the records' observation states were computed with. */
  observation?: { staleAfterSeconds: number; tombstoneRetentionDays: number }
}

/**
 * The server is written in Go, where an empty list can arrive as null and a missing one as nothing at all.
 * Fill every list the UI iterates so one absent field can never take a page down.
 */
export function normalizeServerState(s: Partial<ServerState> | null | undefined): ServerState {
  const t: Partial<ServerState['topology']> = s?.topology ?? {}
  return {
    generatedAt: s?.generatedAt ?? new Date().toISOString(),
    agents: (s?.agents ?? []).map((a) => ({ ...a, modules: a.modules ?? [], observer: a.observer ? { ...a.observer, collectors: a.observer.collectors ?? [] } : undefined })),
    auditLog: s?.auditLog ?? [],
    tombstones: s?.tombstones ?? [],
    observation: s?.observation,
    topology: {
      clusters: t.clusters ?? [],
      nodes: t.nodes ?? [],
      namespaces: t.namespaces ?? [],
      services: t.services ?? [],
      suggestions: t.suggestions ?? [],
      dependencies: t.dependencies ?? [],
      externalEndpoints: t.externalEndpoints ?? [],
      paths: t.paths ?? [],
    },
  }
}

type Rec = { id: string; source: string; agentId?: string; overrides?: Record<string, unknown>; deletedAt?: string }

/** Values people (or other flows) own on a record; discovery does not report them, so they must survive a merge. */
const LOCAL_KEYS = ['applicationId', 'siteId'] as const

/**
 * Bring what an agent reported into the model.
 *  - Discovered values replace the stored base values; human overrides are kept untouched.
 *  - A record an agent no longer reports is tombstoned, but only when the server has a complete
 *    picture from that agent (so an empty or restarted server never wipes anything).
 *  - Records and settings that were typed by hand, and agents this server does not know, are left alone.
 */
function mergeList<T extends Rec>(existing: T[], incoming: T[], synced: Set<string>, now: string, tweak?: (merged: T) => T, refs?: { kind: DeclaredRef['kind']; left: Record<string, DeclaredRef> }): T[] {
  const inc = new Map(incoming.map((r) => [r.id, r]))
  const out = existing.map((e) => {
    const n = inc.get(e.id)
    if (n) {
      inc.delete(e.id)
      const merged: Record<string, unknown> = { ...n, overrides: e.overrides }
      const old = e as unknown as Record<string, unknown>
      for (const k of LOCAL_KEYS) if (old[k] !== undefined && merged[k] === undefined) merged[k] = old[k]
      return tweak ? tweak(merged as unknown as T) : (merged as unknown as T)
    }
    if (e.source === 'discovered' && e.agentId && synced.has(e.agentId) && !e.deletedAt) return { ...e, deletedAt: now }
    return e
  })
  for (const n of inc.values()) {
    // Something a person said about this record before it was known here (a reload, another browser) lands on it now.
    const ref = refs?.left[n.id]
    const rec = ref && refs?.kind === ref.kind ? (applyRef(n as never, ref, refs.kind) as T) : { ...n }
    if (ref && refs) delete refs.left[n.id]
    out.push(tweak ? tweak(rec) : rec)
  }
  return out
}

export function mergeDiscovered(m: Model, doc: ServerState): Partial<Model> {
  const now = doc.generatedAt
  const synced = new Set(doc.agents.filter((a) => a.synced && a.status === 'approved').map((a) => a.id))
  const t = doc.topology

  // The address an agent connects from is the cluster's exit address, when it is a public one.
  const exit = new Map(doc.agents.filter((a) => a.clusterId && a.connectingIp && ipScope(a.connectingIp) === 'public').map((a) => [a.clusterId as string, a.connectingIp as string]))
  const left: Record<string, DeclaredRef> = { ...m.refs }
  const ref = (kind: DeclaredRef['kind']) => ({ kind, left })
  const clusters = mergeList(m.clusters, t.clusters, synced, now, (c) => (exit.has(c.id) ? { ...c, egressIp: exit.get(c.id) } : c), ref('cluster'))
  const nodes = mergeList(m.nodes, t.nodes, synced, now, undefined, ref('node'))
  const namespaces = mergeList(m.namespaces, t.namespaces, synced, now, undefined, ref('namespace'))
  const appIds = new Set(m.applications.filter((a) => !a.deletedAt).map((a) => a.id))
  const services = mergeList(m.services, t.services, synced, now, (s) =>
    !s.applicationId && s.applicationHint && appIds.has(s.applicationHint) ? { ...s, applicationId: s.applicationHint } : s,
    ref('service'),
  )

  // Agents: server-known ones are replaced by id; local (sample) agents stay.
  const known = new Map(doc.agents.map((a) => [a.id, a]))
  const mapped: Agent[] = doc.agents.map((a) => ({
    id: a.id,
    orgId: a.orgId,
    name: a.name,
    clusterId: a.clusterId || undefined,
    version: a.version,
    accessTier: a.accessTier,
    status: a.status,
    fingerprint: a.fingerprint,
    connectingIp: a.connectingIp,
    connectingGeo: a.connectingGeo ?? undefined,
    connectingGeoReason: a.connectingGeoReason ?? undefined,
    certExpiresAt: a.certExpiresAt,
    lastHeartbeat: a.lastHeartbeat,
    modules: a.modules,
    kubernetesVersion: a.kubernetesVersion,
    installedTier: a.installedTier,
    tierCap: a.tierCap,
    requestedAt: a.requestedAt,
    reason: a.reason,
    connected: a.connected,
    observer: a.observer,
    consistency: a.consistency,
    measuring: a.measuring,
    link: a.link,
    scope: a.scope,
    clockSkewMs: a.clockSkewMs,
    legacyEnrollment: a.legacyEnrollment,
    approvalAttemptsLeft: a.approvalAttemptsLeft,
    pendingExpiresAt: a.pendingExpiresAt,
  }))
  const agents = [...m.agents.filter((a) => !known.has(a.id)), ...mapped]

  // Suggestions: new ones arrive open; a decision a person made is never reopened; derived
  // grouping suggestions that discovery no longer makes disappear.
  const inc = new Map(t.suggestions.map((s) => [s.id, s]))
  const nothingToDo = (s: Suggestion) => {
    const a = s.apply
    if (a?.type !== 'create-application') return false
    const byId = new Map(services.map((x) => [x.id, x]))
    return appIds.has(a.application.id) && a.serviceIds.every((id) => !byId.has(id) || byId.get(id)!.applicationId === a.application.id)
  }
  const suggestions: Suggestion[] = []
  for (const s of m.suggestions) {
    const n = inc.get(s.id)
    if (n) {
      inc.delete(s.id)
      suggestions.push(s.status === 'open' ? { ...s, title: n.title, detail: n.detail, apply: n.apply } : s)
    } else if (s.status === 'open' && s.id.startsWith('sg-app-') && s.agentId && synced.has(s.agentId)) {
      continue
    } else if (s.status === 'open' && s.id.startsWith('sg-unk-') && doc.agents.some((a) => a.synced && a.status === 'approved')) {
      continue // a suspicion the traffic no longer supports (the cluster was onboarded, or the calls stopped)
    } else suggestions.push(s)
  }
  for (const n of inc.values()) if (!nothingToDo(n)) suggestions.push(n)

  // Server audit events are append-only and keyed by id.
  const have = new Set(m.auditLog.map((e) => e.id))
  const auditLog = [...m.auditLog, ...doc.auditLog.filter((e) => !have.has(e.id))]

  return { clusters, nodes, namespaces, services, agents, suggestions, auditLog, refs: left }
}

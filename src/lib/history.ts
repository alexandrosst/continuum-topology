import type { AccessTier, Agent, Cluster, Dependency, ExternalEndpoint, MachineNode, Model, Namespace, Path, Service } from './types'

/* ---------- what the server sends ---------- */

/** The knobs an administrator can turn. Everything else in Continuum works without them. */
export interface AppSettings {
  snapshotMinutes: number
  retentionDays: number
  maxHistoryMb: number
  /** How long a record that disappeared from an agent's report is still shown as gone before it is removed. */
  tombstoneRetentionDays: number
  /** How long the event/drift log is kept before old entries are pruned. 0 (the default) keeps it forever; this
   * never affects the audit trail, which is never pruned from here. */
  eventRetentionDays: number
  consistencyMinutes: number
  staleAfterBeats: number
  /** How long an observed link may go unseen before it is shown as quiet, in seconds (not hours) - a
   * deliberate change (a migration, decommissioning a service) can be reflected quickly instead of waiting
   * out a coarse unit. The Settings UI offers a seconds/minutes/hours/days picker over this same value. */
  flowStaleSeconds: number
  measureSeconds: number
  probeTargets: ProbeTarget[]
  /** Observability backends (Jaeger, Prometheus) an administrator quick-started an install command for - see QuickStartBackend. */
  quickStartBackends: QuickStartBackend[]
  /** Only administrators receive the address; everyone else learns that one is configured. */
  deciderUrl: string
  deciderName: string
  deciderTimeoutSec: number
  deciderConfigured: boolean
  /** Whether a shared secret is set to sign requests to the external decider (HMAC-SHA256; see the decider
   * webhook contract). Write-only: once saved, the secret itself is never sent back to anyone, administrators
   * included - only this flag is. To set one, save `deciderSecret`; to remove it, save `clearDeciderSecret: true`
   * (see `api.saveSettings`). Derived; never sent back either. */
  deciderSecretSet: boolean
  /** Where install commands get the agent image and chart (Settings → Installation); empty registry: use the server's default. */
  imageRegistry: string
  imageTag: string
  imageDigest: string
  /** What the server's --image-* flags say, used while the three above are empty. Derived; never sent back. */
  imageDefaults: { registry: string; tag: string; digest: string }
}

export interface ProbeTarget {
  id: string
  clusterId: string
  label: string
  host: string
  port: number
}

export type QuickStartKind = 'jaeger' | 'prometheus'

/** A quick-start observability backend a person set up from the telemetry destination picker - see
 * quickStartBackends.ts for what each kind's install command actually does. The server never deploys or
 * dials any of this; it's a note kept so the picker can offer it again and "open this tool" has somewhere
 * to go once toolUrl is filled in. */
export interface QuickStartBackend {
  id: string
  kind: QuickStartKind
  modality: 'traces' | 'metrics'
  namespace: string
  retention: string
  toolUrl?: string
  label: string
}

export const DEFAULT_SETTINGS: AppSettings = {
  snapshotMinutes: 5,
  retentionDays: 30,
  maxHistoryMb: 512,
  tombstoneRetentionDays: 7,
  eventRetentionDays: 0,
  consistencyMinutes: 15,
  staleAfterBeats: 4,
  flowStaleSeconds: 300,
  measureSeconds: 120,
  probeTargets: [],
  quickStartBackends: [],
  deciderUrl: '',
  deciderName: '',
  deciderTimeoutSec: 10,
  deciderConfigured: false,
  deciderSecretSet: false,
  imageRegistry: '',
  imageTag: '',
  imageDigest: '',
  imageDefaults: { registry: '', tag: '', digest: '' },
}

export function normalizeSettings(s: Partial<AppSettings> | null | undefined): AppSettings {
  return { ...DEFAULT_SETTINGS, ...s, probeTargets: s?.probeTargets ?? [], quickStartBackends: s?.quickStartBackends ?? [], imageDefaults: { ...DEFAULT_SETTINGS.imageDefaults, ...s?.imageDefaults } }
}

/** Event retention is opt-in: off means "keep forever" (0, always valid, whatever text is left in the box).
 * On, the box must hold an integer in this range. A pure function so the UI's validation is easy to unit-test. */
export const EVENT_RETENTION_MIN = 7
export const EVENT_RETENTION_MAX = 3650

export function parseEventRetention(enabled: boolean, raw: string): { days: number; ok: boolean } {
  if (!enabled) return { days: 0, ok: true }
  const trimmed = raw.trim()
  if (trimmed === '') return { days: 0, ok: false }
  const n = Number(trimmed)
  if (!Number.isInteger(n)) return { days: NaN, ok: false }
  return { days: n, ok: n >= EVENT_RETENTION_MIN && n <= EVENT_RETENTION_MAX }
}

export interface HistoryPoint {
  at: string
  bytes: number
}

export interface HistoryIndex {
  points: HistoryPoint[]
  snapshotMinutes: number
  retentionDays: number
}

/** The recorded estate at one moment. It holds what the server derived (base values, no human edits). */
export interface SnapshotTopology {
  clusters: Cluster[]
  nodes: MachineNode[]
  namespaces: Namespace[]
  services: Service[]
  dependencies: Dependency[]
  externalEndpoints: ExternalEndpoint[]
  paths: Path[]
}

/**
 * An agent's graph-recorded state as of a recorded moment: what the server's history has kept about it
 * (see the backend's HistoricAgent/AgentSnapshot), deliberately narrower than the live `Agent` type -
 * never an identity secret, and silent about anything purely observational (connection state, reported
 * diagnostics) that was never worth versioning in the first place.
 */
export interface HistoricAgent {
  id: string
  name: string
  status: string
  clusterId?: string
  installedTier: number
  tierCap: number
  accessTier: number
  version?: string
  k8sVersion?: string
  createdAt?: string
  approvedAt?: string
  approvedBy?: string
  revokedAt?: string
  reason?: string
  lastSeen?: string
  pausedCollectors?: string[]
  excludedNamespaces?: string[]
}

export interface Snapshot {
  at: string
  topology: SnapshotTopology
  agents: HistoricAgent[]
}

export function normalizeSnapshot(s: { at: string; topology?: Partial<SnapshotTopology> | null; agents?: HistoricAgent[] | null }): Snapshot {
  const t = s.topology ?? {}
  return {
    at: s.at,
    topology: {
      clusters: t.clusters ?? [],
      nodes: t.nodes ?? [],
      namespaces: t.namespaces ?? [],
      services: t.services ?? [],
      dependencies: t.dependencies ?? [],
      externalEndpoints: t.externalEndpoints ?? [],
      paths: t.paths ?? [],
    },
    agents: s.agents ?? [],
  }
}

export type Severity = 'info' | 'notice' | 'warning'

export interface ChangeEvent {
  id: string
  at: string
  kind: string
  targetKind: 'cluster' | 'node' | 'service' | 'dependency' | 'agent'
  targetId: string
  name: string
  clusterId?: string
  clusterName?: string
  detail: string
  /** What most likely caused it, when that is known. */
  cause?: string
  severity: Severity
}

export interface TrafficRate {
  id: string
  avgBytesPerSec: number
  peakBytesPerSec: number
  seconds: number
}

/** One sample in a dependency's RTT/loss/throughput trend (see api.dependencySeries) - the per-point
 *  analogue of TrafficRate's aggregate average/peak, for a sparkline rather than a summary number.
 *  rttMs/lossPct/bytesPerSec are all omitted (not a fabricated 0) whenever that snapshot has no
 *  measured sample - bytesPerSec is always missing on the series' first point for exactly that
 *  reason: there is no earlier sample yet to derive a rate from. */
export interface DependencySeriesPoint {
  at: string
  rttMs?: number
  lossPct?: number
  bytesPerSec?: number
}

/** Plain-language names for the kinds of change the server records. */
export const EVENT_KINDS: { value: string; label: string; group: 'cluster' | 'node' | 'service' | 'traffic' | 'consistency' }[] = [
  { value: 'cluster-added', label: 'Cluster appeared', group: 'cluster' },
  { value: 'cluster-removed', label: 'Cluster gone', group: 'cluster' },
  { value: 'cluster-unreachable', label: 'Cluster unreachable', group: 'cluster' },
  { value: 'cluster-reachable', label: 'Cluster reachable again', group: 'cluster' },
  { value: 'cluster-status', label: 'Cluster status', group: 'cluster' },
  { value: 'cluster-version', label: 'Cluster upgraded', group: 'cluster' },
  { value: 'node-added', label: 'Node joined', group: 'node' },
  { value: 'node-removed', label: 'Node left', group: 'node' },
  { value: 'node-status', label: 'Node status', group: 'node' },
  { value: 'service-added', label: 'Service appeared', group: 'service' },
  { value: 'service-removed', label: 'Service removed', group: 'service' },
  { value: 'service-scaled', label: 'Service scaled', group: 'service' },
  { value: 'service-migrated', label: 'Service moved', group: 'service' },
  { value: 'service-rescheduled', label: 'Service rescheduled', group: 'service' },
  { value: 'service-image', label: 'Image changed', group: 'service' },
  { value: 'service-status', label: 'Service status', group: 'service' },
  { value: 'service-restarts', label: 'Service restarting', group: 'service' },
  { value: 'dependency-seen', label: 'Traffic seen', group: 'traffic' },
  { value: 'dependency-quiet', label: 'Traffic went quiet', group: 'traffic' },
  { value: 'drift', label: 'Missed change found', group: 'consistency' },
  { value: 'many-changes', label: 'Many changes', group: 'consistency' },
]

export const kindLabel = (k: string) => EVENT_KINDS.find((x) => x.value === k)?.label ?? k.replace(/-/g, ' ')

/* ---------- the estate as it was ---------- */

type Rec = { id: string; overrides?: Record<string, unknown>; source?: string; siteId?: string; applicationId?: string }

/** Values people own on a record, which a recording of what agents saw cannot hold. */
const KEEP = ['siteId', 'applicationId'] as const

function past<T extends Rec>(recorded: T[], now: T[]): T[] {
  const cur = new Map(now.map((r) => [r.id, r]))
  const out = recorded.map((r) => {
    const c = cur.get(r.id)
    if (!c) return r
    const merged: Record<string, unknown> = { ...r, overrides: c.overrides }
    const old = c as unknown as Record<string, unknown>
    for (const k of KEEP) if (old[k] !== undefined && merged[k] === undefined) merged[k] = old[k]
    return merged as unknown as T
  })
  // records a person typed by hand have no recording; they are shown as they are now
  const have = new Set(recorded.map((r) => r.id))
  for (const r of now) if (r.source === 'manual' && !have.has(r.id)) out.push(r)
  return out
}

/**
 * The raw model as it stood at a recorded moment, for the same merging the live view does (`applyEffective`,
 * `withObserved`). What a cluster's agents discovered comes from the recording; what people own (overrides, sites,
 * applications, devices, hand-drawn dependencies, policy) is as it is now, because it was never recorded. So a
 * past view shows the past infrastructure and services under today's naming and policy. It says so in the banner.
 */
export function atSnapshot(raw: Model, snap: SnapshotTopology): Model {
  const t = snap
  const clusters = past(t.clusters, raw.clusters)
  const nodes = past(t.nodes, raw.nodes)
  const namespaces = past(t.namespaces, raw.namespaces)
  const services = past(t.services, raw.services)
  const ids = new Set(services.map((s) => s.id))
  // Dependencies people declared stay, but only between things that existed then.
  const dependencies = raw.dependencies.filter((d) => (d.fromKind !== 'service' || ids.has(d.from)) && (d.toKind !== 'service' || ids.has(d.to)))
  return { ...raw, clusters, nodes, namespaces, services, dependencies }
}

/**
 * Agents as they were recorded, for the same "past view" `atSnapshot` gives the seven polled kinds -
 * but merged the other way around. Those kinds' recordings are already a complete, valid record on
 * their own (the graph's doc IS the model type), so `past` takes the recording as the base and grafts a
 * few always-current fields on. An agent's recording (`HistoricAgent`) is deliberately not a complete
 * `Agent` - it never carried an identity secret or a purely observational field like its live connection
 * state to begin with - so this goes the other way: the live agent is the base (it alone has the id's
 * permanent identity, its modules, its connection telemetry), with the recording's own fields laid over
 * the top, since those are exactly what changed and are worth showing as they were.
 *
 * An agent recorded then but gone from the live list entirely (never observed since, in practice never
 * happens - agents are revoked or rejected, not deleted) has no live record to build from and is left
 * out, the same way a service the graph forgot would be: there is nothing honest to show for it.
 */
export function historicAgents(historic: HistoricAgent[], live: Agent[]): Agent[] {
  const byId = new Map(live.map((a) => [a.id, a]))
  const out: Agent[] = []
  for (const h of historic) {
    const base = byId.get(h.id)
    if (!base) continue
    out.push({
      ...base,
      name: h.name,
      status: h.status as Agent['status'],
      clusterId: h.clusterId,
      installedTier: h.installedTier as AccessTier,
      tierCap: h.tierCap as AccessTier,
      accessTier: h.accessTier as AccessTier,
      version: h.version ?? base.version,
      kubernetesVersion: h.k8sVersion ?? base.kubernetesVersion,
      reason: h.reason ?? base.reason,
      lastHeartbeat: h.lastSeen ?? base.lastHeartbeat,
    })
  }
  return out
}

/** The recorded point closest to a moment (at or before it; the first one when it is earlier than all). */
export function pointAt(points: HistoryPoint[], iso: string): HistoryPoint | undefined {
  if (points.length === 0) return undefined
  const t = new Date(iso).getTime()
  let best = points[0]
  for (const p of points) {
    if (new Date(p.at).getTime() <= t) best = p
    else break
  }
  return best
}

/** How long a recorded moment is from now, in the words people use. */
export function ageOf(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 90) return 'a moment ago'
  if (s < 5400) return `${Math.round(s / 60)} minutes ago`
  if (s < 129600) return `${Math.round(s / 3600)} hours ago`
  return `${Math.round(s / 86400)} days ago`
}

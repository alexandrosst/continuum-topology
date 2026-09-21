import type { Cluster, Dependency, ExternalEndpoint, MachineNode, Model, Namespace, Path, Service } from './types'

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
  flowStaleHours: number
  measureSeconds: number
  probeTargets: ProbeTarget[]
  /** Only administrators receive the address; everyone else learns that one is configured. */
  deciderUrl: string
  deciderName: string
  deciderTimeoutSec: number
  deciderConfigured: boolean
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

export const DEFAULT_SETTINGS: AppSettings = {
  snapshotMinutes: 5,
  retentionDays: 30,
  maxHistoryMb: 512,
  tombstoneRetentionDays: 7,
  eventRetentionDays: 0,
  consistencyMinutes: 15,
  staleAfterBeats: 4,
  flowStaleHours: 24,
  measureSeconds: 120,
  probeTargets: [],
  deciderUrl: '',
  deciderName: '',
  deciderTimeoutSec: 10,
  deciderConfigured: false,
  imageRegistry: '',
  imageTag: '',
  imageDigest: '',
  imageDefaults: { registry: '', tag: '', digest: '' },
}

export function normalizeSettings(s: Partial<AppSettings> | null | undefined): AppSettings {
  return { ...DEFAULT_SETTINGS, ...s, probeTargets: s?.probeTargets ?? [], imageDefaults: { ...DEFAULT_SETTINGS.imageDefaults, ...s?.imageDefaults } }
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

export interface Snapshot {
  at: string
  topology: SnapshotTopology
}

export function normalizeSnapshot(s: { at: string; topology?: Partial<SnapshotTopology> | null }): Snapshot {
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

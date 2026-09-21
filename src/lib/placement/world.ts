import { distanceKm } from '../places'
import type { MoveModel } from '../movability'
import { CPU, MEMORY, type Fix } from '../advice'
import { adjustPools, capCtxOf, fixForPool, needOf, type Place, poolsOf, type CapCtx, type Pools } from '../capacity'
import { observation, targetStatus, type EffectiveModel, type TargetStatus } from '../provenance'
import type { Agent, Cluster, Dependency, Device, ExternalEndpoint, MachineNode, Path, Service, Site, SiteLink } from '../types'
import type { Loc, Rtt } from './types'

/** Everything the placement engine reads. It is the model as the UI shows it, plus the measured paths. */
export interface WorldInput {
  clusters: Cluster[]
  nodes: MachineNode[]
  services: Service[]
  devices: Device[]
  dependencies: Dependency[]
  sites: Site[]
  siteLinks: SiteLink[]
  externalEndpoints: ExternalEndpoint[]
  agents: Agent[]
  paths: Path[]
  /**
   * The server's effective model, when it is loaded: it says when each node's figures were last confirmed, which is
   * what lets an old figure count as less certain. Without it the figures have no age.
   */
  model?: EffectiveModel
  /** The time the world is judged at (ms). Default now. Pass the server's clock when the browser's may differ. */
  asOf?: number
}

/** A snapshot of the estate to reason about. Immutable: what-ifs make a new one with `withMoves`. */
export interface World {
  clusters: Cluster[]
  nodes: MachineNode[]
  services: Service[]
  devices: Device[]
  /** Dependencies worth costing: machinery traffic (DNS, system) is left out. */
  deps: Dependency[]
  sites: Site[]
  links: SiteLink[]
  externals: ExternalEndpoint[]
  /** Measured paths that can be trusted: recent, and not all attempts failing. */
  paths: Path[]
  agents: Agent[]
  /** Where each service runs in this world (it differs from `Service.clusterId` after a what-if move). */
  placement: Map<string, string>
  /** Services this world has moved, with where they started. */
  moved: Map<string, { from: string; to: string }>
  byService: Map<string, Service>
  byCluster: Map<string, Cluster>
  bySite: Map<string, Site>
  byDevice: Map<string, Device>
  byExternal: Map<string, ExternalEndpoint>
  byNode: Map<string, MachineNode>
  /** When the capacity facts are judged, and how old a fact may be before it counts as one class worse. */
  cap: CapCtx
}

const alive = <T extends { deletedAt?: string }>(xs: T[]) => xs.filter((x) => !x.deletedAt)

export function buildWorld(input: WorldInput): World {
  const clusters = alive(input.clusters)
  const services = alive(input.services)
  const nodes = alive(input.nodes)
  const devices = alive(input.devices)
  const externals = alive(input.externalEndpoints)
  const usable = input.paths.filter((p) => !p.stale && p.samples > 0 && p.lossPct < 100 && p.rttP50Ms > 0)
  const cap = capCtxOf(input.model, input.asOf ?? Date.now())
  return {
    cap,
    clusters,
    nodes,
    services,
    devices,
    deps: input.dependencies.filter((d) => !d.noise),
    sites: input.sites,
    links: input.siteLinks,
    externals,
    paths: usable,
    agents: input.agents,
    placement: new Map(services.map((s) => [s.id, s.clusterId])),
    moved: new Map(),
    byService: new Map(services.map((s) => [s.id, s])),
    byCluster: new Map(clusters.map((c) => [c.id, c])),
    bySite: new Map(input.sites.map((s) => [s.id, s])),
    byDevice: new Map(devices.map((d) => [d.id, d])),
    byExternal: new Map(externals.map((e) => [e.id, e])),
    byNode: new Map(nodes.map((n) => [n.id, n])),
  }
}

/** A world in which the given services run in different clusters. The input is not changed. */
export function withMoves(w: World, moves: { serviceId: string; to: string }[]): World {
  const placement = new Map(w.placement)
  const moved = new Map(w.moved)
  for (const m of moves) {
    const s = w.byService.get(m.serviceId)
    if (!s || !w.byCluster.has(m.to)) continue
    const from = moved.get(m.serviceId)?.from ?? w.placement.get(m.serviceId) ?? s.clusterId
    placement.set(m.serviceId, m.to)
    if (m.to === from) moved.delete(m.serviceId)
    else moved.set(m.serviceId, { from, to: m.to })
  }
  return { ...w, placement, moved }
}

export const clusterOfService = (w: World, id: string) => w.placement.get(id) ?? w.byService.get(id)?.clusterId

export const siteOfCluster = (w: World, clusterId: string | undefined): Site | undefined => {
  const c = clusterId ? w.byCluster.get(clusterId) : undefined
  return c?.siteId ? w.bySite.get(c.siteId) : undefined
}

/** The model movability() reads, for the service as it currently is (not where a what-if put it). */
export function moveModel(w: World, storageKnown?: boolean): MoveModel {
  return { clusters: w.clusters, nodes: w.nodes, services: w.services, devices: w.devices, dependencies: w.deps, sites: [...w.bySite.values()], storageKnown, agents: w.agents, cap: w.cap }
}

/** Does this cluster's agent read volumes? Without that, "no volumes" proves nothing. */
export function storageKnown(w: World, clusterId: string): boolean {
  const a = w.agents.find((x) => x.clusterId === clusterId)
  if (!a || a.modules.length === 0) return true
  return a.modules.some((m) => m.name === 'storage' && m.status === 'ok')
}

/** Where the other end of a dependency is. */
export function locOf(w: World, kind: 'service' | 'device' | 'external', id: string): Loc {
  if (kind === 'service') {
    const c = clusterOfService(w, id)
    return c ? { kind: 'cluster', id: c } : { kind: 'unknown' }
  }
  if (kind === 'device') {
    const d = w.byDevice.get(id)
    if (!d) return { kind: 'unknown' }
    if (d.siteId && w.bySite.has(d.siteId)) return { kind: 'site', id: d.siteId }
    const n = d.gatewayNodeId ? w.byNode.get(d.gatewayNodeId) : undefined
    return n ? { kind: 'cluster', id: n.clusterId } : { kind: 'unknown' }
  }
  const e = w.byExternal.get(id)
  return e ? { kind: 'external', host: e.host, port: e.port } : { kind: 'unknown' }
}

export function locName(w: World, loc: Loc): string {
  switch (loc.kind) {
    case 'cluster':
      return w.byCluster.get(loc.id)?.name ?? loc.id
    case 'site':
      return w.bySite.get(loc.id)?.name ?? loc.id
    case 'external':
      return loc.port ? `${loc.host}:${loc.port}` : loc.host
    default:
      return 'unknown'
  }
}

/** The site an endpoint sits at, when it has one. */
export function siteIdOf(w: World, loc: Loc): string | undefined {
  if (loc.kind === 'site') return loc.id
  if (loc.kind === 'cluster') return w.byCluster.get(loc.id)?.siteId
  return undefined
}

// Light in fibre covers about 200 km per millisecond one way, so 1 ms of round trip per 100 km of straight
// line; real routes are longer than that and add equipment delay. The factor and the floor are deliberately
// rough: an estimate is only ever used when nothing was measured, and is labelled as one.
const MS_PER_KM = 0.01
const ROUTE_FACTOR = 1.6
const FLOOR_MS = 2

function siteToSite(w: World, a?: Site, b?: Site): Rtt | undefined {
  if (!a || !b) return undefined
  if (a.id === b.id) return { ms: 1, basis: 'same-site' }
  const link = w.links.find((l) => (l.a === a.id && l.b === b.id) || (l.a === b.id && l.b === a.id))
  if (link?.rttMs !== undefined && link.rttMs > 0) return { ms: link.rttMs, basis: link.source === 'measured' ? 'measured' : 'declared' }
  if (Number.isFinite(a.lat) && Number.isFinite(a.lng) && Number.isFinite(b.lat) && Number.isFinite(b.lng)) {
    const km = distanceKm(a.lat, a.lng, b.lat, b.lng)
    return { ms: Math.round((FLOOR_MS + km * MS_PER_KM * ROUTE_FACTOR) * 10) / 10, basis: 'estimated' }
  }
  return undefined
}

/** A measured path between two onboarded clusters, in either direction. */
function measuredBetween(w: World, a: string, b: string): number | undefined {
  const hits = w.paths.filter((p) => (p.fromCluster === a && p.toCluster === b) || (p.fromCluster === b && p.toCluster === a))
  if (hits.length === 0) return undefined
  return Math.min(...hits.map((p) => p.rttP50Ms))
}

/** The round trip between a cluster and where a peer is. Best evidence first: measured, then declared or measured
 * between sites, then the same site, then distance, and only then nothing. */
export function rtt(w: World, cluster: string, to: Loc): Rtt {
  if (to.kind === 'cluster' && to.id === cluster) return { ms: 0.3, basis: 'same-cluster' }
  if (to.kind === 'unknown') return { ms: NaN, basis: 'unknown' }
  const mine = siteOfCluster(w, cluster)

  if (to.kind === 'cluster') {
    const m = measuredBetween(w, cluster, to.id)
    if (m !== undefined) return { ms: m, basis: 'measured' }
    return siteToSite(w, mine, siteOfCluster(w, to.id)) ?? { ms: NaN, basis: 'unknown' }
  }
  if (to.kind === 'site') return siteToSite(w, mine, w.bySite.get(to.id)) ?? { ms: NaN, basis: 'unknown' }

  // an address outside every cluster: timed from here, or worked out through a cluster that timed it
  const direct = w.paths.filter((p) => p.fromCluster === cluster && p.host === to.host && (to.port === undefined || p.port === to.port || !p.port))
  if (direct.length > 0) return { ms: Math.min(...direct.map((p) => p.rttP50Ms)), basis: 'measured' }
  let via: number | undefined
  for (const p of w.paths) {
    if (p.host !== to.host || p.fromCluster === cluster) continue
    const hop = rtt(w, cluster, { kind: 'cluster', id: p.fromCluster })
    if (hop.basis === 'unknown') continue
    const total = hop.ms + p.rttP50Ms
    if (via === undefined || total < via) via = total
  }
  return via !== undefined ? { ms: Math.round(via * 10) / 10, basis: 'estimated' } : { ms: NaN, basis: 'unknown' }
}

/** The free capacity of a cluster's nodes as intervals, after the services this world moved in and out. */
export function clusterPools(w: World, clusterId: string, nodes?: MachineNode[]): Pools {
  const base = poolsOf(nodes ?? w.nodes.filter((n) => n.clusterId === clusterId), w.cap)
  const moves: { s: Service; dir: 'in' | 'out' }[] = []
  for (const [id, mv] of w.moved) {
    const s = w.byService.get(id)
    if (!s) continue
    if (mv.to === clusterId) moves.push({ s, dir: 'in' })
    if (mv.from === clusterId) moves.push({ s, dir: 'out' })
  }
  return moves.length > 0 ? adjustPools(base, moves) : base
}

/**
 * Free CPU (cores) and memory (GB) of a cluster, as reported, adjusted for the services this world moved in and out.
 * `known` says whether any node reported what it has; `exact` whether every node also reported what is already
 * requested on it: without that, free is not known and `cpu` and `memGb` are 0 only because nothing is known, so read
 * them only when `exact` is true.
 */
export function freeCapacity(w: World, clusterId: string): { cpu: number; memGb: number; known: boolean; exact: boolean } {
  const ns = w.nodes.filter((n) => n.clusterId === clusterId)
  const known = ns.some((n) => n.allocatable)
  if (!known) return { cpu: 0, memGb: 0, known: false, exact: false }
  const p = clusterPools(w, clusterId)
  const exact = p.cpu.nominalKnown && p.memory.nominalKnown
  return { cpu: exact ? p.cpu.knownFree : 0, memGb: exact ? p.memory.knownFree / 1024 ** 3 : 0, known: true, exact }
}

export function totalCapacity(w: World, clusterId: string): { cpu: number; memGb: number } | undefined {
  const ns = w.nodes.filter((n) => n.clusterId === clusterId && n.allocatable)
  if (ns.length === 0) return undefined
  return { cpu: ns.reduce((a, n) => a + n.allocatable!.cpu, 0), memGb: ns.reduce((a, n) => a + n.allocatable!.memoryGb, 0) }
}

/** What all replicas of a service ask for, in cores and GB. Zero when nothing is declared (which is "not known", not "nothing"; see needOf). */
export function serviceNeed(s: Service): { cpu: number; memGb: number } {
  const n = needOf(s)
  return { cpu: n.cpu.value ?? 0, memGb: (n.memory.value ?? 0) / 1024 ** 3 }
}

/**
 * Can workloads be placed on this cluster in this world? Only if it is live (or declared). A live cluster whose
 * capacity is unknown is not ruled out: it is "can't tell", and `capacityUnknown` says so (with the fix) where the
 * Placement page lists it apart from the clusters that are left out.
 */
export function clusterStatus(w: World, clusterId: string): TargetStatus & { capacityUnknown: boolean } {
  const c = w.byCluster.get(clusterId)
  if (!c) return { eligible: false, state: 'gone', reason: 'the cluster is gone', capacityUnknown: false }
  const live = targetStatus(c, true)
  const unknown = live.eligible && observation(c) !== undefined && !freeCapacity(w, clusterId).known
  return { ...live, capacityUnknown: unknown }
}

/** Clusters that are left out of placement because they are not live, each with the reason ("stale for 2 h", "agent revoked"). */
export function excludedClusters(w: World): { cluster: Cluster; status: TargetStatus }[] {
  return w.clusters.flatMap((cluster) => {
    const status = clusterStatus(w, cluster.id)
    return status.eligible ? [] : [{ cluster, status }]
  })
}

/** A live cluster nothing (or not enough) is known about: what is missing, and what would make it known. */
export interface Unverifiable {
  cluster: Cluster
  status: TargetStatus
  /** Why free capacity cannot be worked out: what no node reported. */
  why: string
  fix?: Fix
  /** True when no node reports what it has at all (as opposed to some figure missing). */
  nothing: boolean
}

/**
 * Live clusters where free capacity is not fully known: not ruled out (an unknown is not a no), but nothing can be
 * certified to fit there until it is. Listed apart from the clusters left out for not being live.
 */
export function unverifiableClusters(w: World): Unverifiable[] {
  const out: Unverifiable[] = []
  for (const cluster of w.clusters) {
    const status = clusterStatus(w, cluster.id)
    if (!status.eligible || observation(cluster) === undefined) continue
    const pools = clusterPools(w, cluster.id)
    if (pools.cpu.nominalKnown && pools.memory.nominalKnown) continue
    const place: Place = {
      clusterId: cluster.id,
      clusterName: cluster.name,
      declared: cluster.source !== 'discovered',
      agent: w.agents.find((a) => a.clusterId === cluster.id && a.status === 'approved'),
      nodeCount: w.nodes.filter((n) => n.clusterId === cluster.id).length,
    }
    const pool = pools.cpu.nominalKnown ? pools.memory : pools.cpu
    const dim = pools.cpu.nominalKnown ? MEMORY : CPU
    out.push({ cluster, status, why: pool.notes.join('; ') || 'not reported', fix: fixForPool(pool, place, dim), nothing: status.capacityUnknown })
  }
  return out
}

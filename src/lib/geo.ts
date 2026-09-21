// Pure helpers for the map view: which sites have anything on them, which of them are close enough on
// screen to be drawn as one dot, and which places are connected. No rendering and no map library here.
import { countryName } from './present'
import { ipScope } from './present'
import { TIER_ORDER, type Cluster, type Dependency, type Device, type MachineNode, type Path, type Service, type Site, type SiteLink, type Status, type Tier } from './types'

const RANK: Record<Status, number> = { healthy: 0, unknown: 1, degraded: 2, offline: 3 }

/** The most worrying of several statuses. Nothing at all is "unknown". */
export function worstStatus(statuses: Status[]): Status {
  let worst: Status | undefined
  for (const s of statuses) if (worst === undefined || RANK[s] > RANK[worst]) worst = s
  return worst ?? 'unknown'
}

export interface MapSite {
  site: Site
  clusters: Cluster[]
  devices: Device[]
  nodes: number
  services: number
  status: Status
  /** What kind of place this is on the continuum: the tier most of its clusters are in (devices alone count as far edge). */
  tier?: Tier
  /** Public addresses its clusters reach the outside world from, without duplicates. */
  exitIps: string[]
}

/**
 * The tier that stands for a set of clusters: the one with the most clusters, the more central one on a tie.
 * With no clusters, devices alone mean the far edge; an empty place has no tier.
 */
export function dominantTier(tiers: Tier[], hasDevices = false): Tier | undefined {
  if (tiers.length === 0) return hasDevices ? 'far-edge' : undefined
  const count = new Map<Tier, number>()
  for (const t of tiers) count.set(t, (count.get(t) ?? 0) + 1)
  return [...count.entries()].sort((a, b) => b[1] - a[1] || TIER_ORDER[a[0]] - TIER_ORDER[b[0]])[0][0]
}

/** The public exit addresses among some clusters. A private address says nothing about where a cluster is seen from. */
export const exitIps = (clusters: Pick<Cluster, 'egressIp'>[]): string[] => [
  ...new Set(clusters.map((c) => c.egressIp?.trim()).filter((ip): ip is string => !!ip && ipScope(ip) === 'public')),
]

interface Live {
  deletedAt?: string
}
const alive = <T extends Live>(xs: T[]): T[] => xs.filter((x) => !x.deletedAt)

const validCoord = (s: Site) => Number.isFinite(s.lat) && Number.isFinite(s.lng) && Math.abs(s.lat) <= 90 && Math.abs(s.lng) <= 180

/**
 * Every site that can be drawn (valid coordinates), with what lives there. Sites nobody uses are kept: an
 * empty site is still a place a person set up on purpose.
 */
export function buildMapSites(sites: Site[], clusters: Cluster[], devices: Device[], nodes: MachineNode[], services: Service[]): MapSite[] {
  const cs = alive(clusters)
  const ds = alive(devices)
  const ns = alive(nodes)
  const ws = alive(services)
  return sites.filter(validCoord).map((site) => {
    const here = cs.filter((c) => c.siteId === site.id)
    const ids = new Set(here.map((c) => c.id))
    const dev = ds.filter((d) => d.siteId === site.id)
    return {
      site,
      clusters: here,
      devices: dev,
      nodes: ns.filter((n) => ids.has(n.clusterId)).length,
      services: ws.filter((w) => ids.has(w.clusterId)).length,
      status: worstStatus([...here.map((c) => c.status), ...dev.map((d) => d.status)]),
      tier: dominantTier(here.map((c) => c.tier), dev.length > 0),
      exitIps: exitIps(here),
    }
  })
}

/** Clusters that cannot be drawn: no site chosen, or the site is gone or has no valid position. */
export function unplacedClusters(sites: Site[], clusters: Cluster[]): Cluster[] {
  const ok = new Set(sites.filter(validCoord).map((s) => s.id))
  return alive(clusters).filter((c) => !c.siteId || !ok.has(c.siteId))
}

/**
 * Greedy grouping by distance on screen. `items` carry base (zoom 1) coordinates, `k` is the current zoom
 * and `radiusPx` how close two dots may be before they merge. Zoomed out, a whole country collapses to one
 * dot; zoomed in, the cities separate. Items are visited in the order given, so callers sort by importance.
 */
export function groupByProximity<T extends { x: number; y: number }>(items: T[], k: number, radiusPx: number): T[][] {
  const r = radiusPx / Math.max(k, 1e-6)
  const r2 = r * r
  const taken = new Array<boolean>(items.length).fill(false)
  const groups: T[][] = []
  for (let i = 0; i < items.length; i++) {
    if (taken[i]) continue
    taken[i] = true
    const g = [items[i]]
    for (let j = i + 1; j < items.length; j++) {
      if (taken[j]) continue
      const dx = items[j].x - items[i].x
      const dy = items[j].y - items[i].y
      if (dx * dx + dy * dy <= r2) {
        taken[j] = true
        g.push(items[j])
      }
    }
    groups.push(g)
  }
  return groups
}

/** "Patras" for one place, "Greece" when several places share a country, "5 sites" otherwise. */
export function groupLabel(members: MapSite[]): string {
  if (members.length === 1) return members[0].site.city || members[0].site.name
  const countries = new Set(members.map((m) => m.site.country.toUpperCase()))
  if (countries.size === 1) return countryName([...countries][0]) || `${members.length} sites`
  return `${members.length} sites`
}

export interface SiteConnection {
  a: string
  b: string
  /** Measured or declared round-trip time, when a link exists. */
  rttMs?: number
  /** How many service/device dependencies cross between the two sites. */
  dependencies: number
  /** How many of those were seen in real traffic and are not quiet. */
  active: number
  /** Bytes per second seen from a to b and from b to a, summed over their dependencies. 0 when nothing was measured. */
  bpsAB: number
  bpsBA: number
  /** Share of connection attempts that failed, when a measurement says so. */
  lossPct?: number
  /** rttMs came from a measurement (not a declared figure). */
  measured?: boolean
}

/** Pairs of sites that talk to each other: explicit links plus dependencies that cross sites. */
export function siteConnections(
  sites: Site[],
  clusters: Cluster[],
  services: Service[],
  devices: Device[],
  dependencies: Dependency[],
  siteLinks: SiteLink[],
  paths: Path[] = [],
): SiteConnection[] {
  const known = new Set(sites.map((s) => s.id))
  const clusterSite = new Map(alive(clusters).map((c) => [c.id, c.siteId]))
  const where = new Map<string, string | undefined>()
  for (const w of alive(services)) where.set(w.id, clusterSite.get(w.clusterId))
  for (const d of alive(devices)) where.set(d.id, d.siteId)

  const key = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`)
  const out = new Map<string, SiteConnection>()
  const get = (a: string, b: string) => {
    const k = key(a, b)
    let c = out.get(k)
    if (!c) out.set(k, (c = a < b ? { a, b, dependencies: 0, active: 0, bpsAB: 0, bpsBA: 0 } : { a: b, b: a, dependencies: 0, active: 0, bpsAB: 0, bpsBA: 0 }))
    return c
  }
  for (const l of siteLinks) {
    if (l.a === l.b || !known.has(l.a) || !known.has(l.b)) continue
    const c = get(l.a, l.b)
    c.rttMs = l.rttMs
    c.lossPct = l.lossPct
    c.measured = l.source === 'measured'
  }
  for (const d of dependencies) {
    const a = where.get(d.from)
    const b = where.get(d.to)
    if (a && b && a !== b && known.has(a) && known.has(b)) {
      const c = get(a, b)
      c.dependencies++
      if (d.sources.includes('observed') && !d.stale) c.active++
      const bps = d.stats?.bytesPerSec ?? 0
      if (a === c.a) c.bpsAB += bps
      else c.bpsBA += bps
    }
  }
  // Measured round trips between clusters beat declared ones between their sites.
  for (const p of paths) {
    if (p.stale || !p.toCluster || p.samples <= 0) continue
    const a = clusterSite.get(p.fromCluster)
    const b = clusterSite.get(p.toCluster)
    if (!a || !b || a === b || !known.has(a) || !known.has(b)) continue
    const c = get(a, b)
    c.rttMs = c.measured && c.rttMs !== undefined ? Math.min(c.rttMs, p.rttP50Ms) : p.rttP50Ms
    c.measured = true
    c.lossPct = Math.max(c.lossPct ?? 0, p.lossPct)
  }
  return [...out.values()].sort((x, y) => (x.a + x.b).localeCompare(y.a + y.b))
}

/** Keep a pan offset inside the map so it can never be dragged off screen. `size` is the viewBox side. */
export const clampPan = (offset: number, k: number, size: number): number => Math.min(0, Math.max(size * (1 - k), offset))

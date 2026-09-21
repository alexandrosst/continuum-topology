// The numbers the topology shows next to its shapes: how loaded a cluster is, how well two clusters can
// reach each other, how busy a link is. Each says what it is measured from, and none is invented: a value
// that nobody reported is absent, never zero.
import type { Cluster, MachineNode, Path, Service } from './types'

export interface ClusterLoad {
  /** Share of allocatable CPU that pods request, over the nodes that report it. */
  cpuPct?: number
  memPct?: number
  /** Pods placed over the pod capacity of the nodes that report both. */
  podPct?: number
  pods?: number
  podCap?: number
  nodes: number
  ready: number
  services: number
  /** Services with fewer ready replicas than wanted. */
  unready: number
}

const live = <T extends { deletedAt?: string }>(xs: T[]) => xs.filter((x) => !x.deletedAt)
const pct = (used: number, of: number) => (of > 0 ? Math.min(100, Math.round((used / of) * 100)) : undefined)

export function clusterLoad(cluster: Pick<Cluster, 'id'>, nodes: MachineNode[], services: Service[]): ClusterLoad {
  const ns = live(nodes).filter((n) => n.clusterId === cluster.id)
  const ws = live(services).filter((s) => s.clusterId === cluster.id)
  let cpuReq = 0, cpuAlloc = 0, memReq = 0, memAlloc = 0, pods = 0, cap = 0
  for (const n of ns) {
    const a = n.allocatable
    const r = n.requested
    if (a && r) {
      cpuReq += r.cpu
      cpuAlloc += a.cpu
      memReq += r.memoryGb
      memAlloc += a.memoryGb
    }
    if (n.podCount !== undefined && n.podCapacity) {
      pods += n.podCount
      cap += n.podCapacity
    }
  }
  return {
    cpuPct: pct(cpuReq, cpuAlloc),
    memPct: pct(memReq, memAlloc),
    podPct: pct(pods, cap),
    pods: cap > 0 ? pods : undefined,
    podCap: cap > 0 ? cap : undefined,
    nodes: ns.length,
    ready: ns.filter((n) => n.status === 'healthy').length,
    services: ws.length,
    unready: ws.filter((s) => s.readyReplicas !== undefined && s.readyReplicas < s.replicas).length,
  }
}

export interface PathQuality {
  rttMs: number
  lossPct: number
  /** The measurement was taken in the other direction (round-trip time is the same both ways; loss may not be). */
  reversed?: boolean
}

/** What measurements say about reaching cluster `to` from cluster `from`: the best round trip and the worst loss among fresh ones. */
export function pathQuality(paths: Path[], from: string, to: string): PathQuality | undefined {
  const fresh = paths.filter((p) => !p.stale && p.samples > 0)
  const pick = (a: string, b: string) => fresh.filter((p) => p.fromCluster === a && p.toCluster === b)
  const direct = pick(from, to)
  const use = direct.length ? direct : pick(to, from)
  if (!use.length) return undefined
  return { rttMs: Math.min(...use.map((p) => p.rttP50Ms)), lossPct: Math.max(...use.map((p) => p.lossPct)), reversed: direct.length === 0 }
}

export type Band = 'ok' | 'warn' | 'hot'
/** Loss is worth noticing early: 1% already hurts TCP. Round-trip time only matters relative to the application, so it is not banded. */
export const lossBand = (lossPct: number): Band => (lossPct >= 5 ? 'hot' : lossPct >= 1 ? 'warn' : 'ok')

export const rttLabel = (ms: number) => (ms < 10 ? `${ms.toFixed(1)} ms` : `${Math.round(ms)} ms`)
export const qualityLabel = (q: PathQuality) => `${rttLabel(q.rttMs)}${q.lossPct > 0 ? ` · ${q.lossPct < 10 ? q.lossPct.toFixed(1) : Math.round(q.lossPct)}% loss` : ''}`

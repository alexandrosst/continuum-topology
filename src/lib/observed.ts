import type { Confidence, Dependency, Device, ExternalEndpoint, Service } from './types'

/**
 * Lay what was seen on the wire over what people and Kubernetes declared.
 *
 * - A dependency that was both declared and seen becomes one edge with both sources and the traffic numbers.
 * - One that was only seen is added, but only when both of its ends exist in the model (a workload a person
 *   deleted does not come back through a stale flow).
 * - An outside address that a person already named (an external endpoint with the same host) is that endpoint;
 *   otherwise it is added as an unknown one, to be named later.
 *
 * Pure: it returns new lists and never touches the stored workspace.
 */
const RANK: Record<Confidence, number> = { low: 0, medium: 1, high: 2 }

export function withObserved(
  base: { services: Service[]; devices: Device[]; dependencies: Dependency[]; externalEndpoints: ExternalEndpoint[] },
  observed: { dependencies: Dependency[]; externalEndpoints: ExternalEndpoint[] },
): { dependencies: Dependency[]; externalEndpoints: ExternalEndpoint[] } {
  if (!observed.dependencies.length) return { dependencies: base.dependencies, externalEndpoints: base.externalEndpoints }

  const services = new Set(base.services.map((s) => s.id))
  const externals = [...base.externalEndpoints]
  const byHost = new Map(base.externalEndpoints.map((e) => [`${e.host}:${e.port ?? 0}`, e.id]))
  const remap = new Map<string, string>()
  for (const e of observed.externalEndpoints) {
    const own = byHost.get(`${e.host}:${e.port ?? 0}`) ?? byHost.get(`${e.host}:0`)
    if (own) remap.set(e.id, own)
    else {
      externals.push(e)
      byHost.set(`${e.host}:${e.port ?? 0}`, e.id)
    }
  }
  const externalIds = new Set(externals.map((e) => e.id))
  const has = (kind: Dependency['toKind'], id: string) => (kind === 'external' ? externalIds.has(id) : kind === 'service' ? services.has(id) : true)

  const out = base.dependencies.map((d) => ({ ...d }))
  for (const o of observed.dependencies) {
    const from = o.fromKind === 'external' ? (remap.get(o.from) ?? o.from) : o.from
    const to = o.toKind === 'external' ? (remap.get(o.to) ?? o.to) : o.to
    if (!has(o.fromKind, from) || !has(o.toKind, to)) continue
    const seen: Dependency = { ...o, from, to }
    const same = out.find((d) => d.from === from && d.to === to && d.fromKind === o.fromKind && d.toKind === o.toKind && (d.port === undefined || d.port === o.port))
    if (!same) {
      out.push(seen)
      continue
    }
    same.sources = [...new Set([...same.sources, 'observed' as const])]
    same.confidence = RANK[o.confidence] > RANK[same.confidence] ? o.confidence : same.confidence
    same.port ??= o.port
    same.firstSeen = [same.firstSeen, o.firstSeen].filter(Boolean).sort()[0]
    same.lastSeen = o.lastSeen
    same.stats = { ...same.stats, ...o.stats }
    Object.assign(same, { stale: o.stale, noise: o.noise, crossCluster: o.crossCluster, via: o.via, note: o.note, connections: o.connections, bytes: o.bytes })
  }
  return { dependencies: out, externalEndpoints: externals }
}

/** True when the dependency was seen in traffic, not only declared. */
export const isObserved = (d: Pick<Dependency, 'sources'>) => d.sources.includes('observed')

/** "12 connections/min · 3.4 KB/s" — only the parts that were actually measured. */
export function trafficSummary(d: Dependency): string {
  const bits: string[] = []
  const s = d.stats
  if (s?.connectionsPerMin !== undefined) bits.push(`${rate(s.connectionsPerMin)} conn/min`)
  if (s?.bytesPerSec !== undefined && d.via === 'ebpf') bits.push(bytesPerSec(s.bytesPerSec))
  else if (d.via === 'conntrack' && s?.bytesPerSec) bits.push(bytesPerSec(s.bytesPerSec))
  return bits.join(' · ')
}

const rate = (n: number) => (n >= 100 ? Math.round(n).toString() : n >= 10 ? n.toFixed(0) : n >= 1 ? n.toFixed(1) : n > 0 ? '<1' : '0')

export function bytesPerSec(n: number): string {
  if (n < 1) return '0 B/s'
  const u = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 ? Math.round(n) : n.toFixed(1)} ${u[i]}`
}

export function bytesTotal(n: number): string {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`
}

/** How long ago, for "last seen". */
export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return 'never'
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 90) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}

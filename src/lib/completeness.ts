import type { Cluster, Dependency, MachineNode, Service } from './types'

export interface Completeness {
  infra: boolean
  services: boolean
  dependencies: boolean
  /** One line for tooltips / inspector, e.g. "Infrastructure + services, no dependencies". */
  label: string
}

/** How much of a cluster we actually know. A missing layer is a visible state, never an error. */
export function completeness(c: Cluster, nodes: MachineNode[], services: Service[], deps: Dependency[]): Completeness {
  const infra = nodes.some((n) => n.clusterId === c.id)
  const ids = new Set(services.filter((s) => s.clusterId === c.id).map((s) => s.id))
  const hasServices = ids.size > 0
  const dependencies = deps.some((d) => ids.has(d.from) || ids.has(d.to))
  const have = [infra && 'infrastructure', hasServices && 'services', dependencies && 'dependencies'].filter(Boolean) as string[]
  const missing = [!infra && 'infrastructure', !hasServices && 'services', !dependencies && 'dependencies'].filter(Boolean) as string[]
  const label = have.length === 0 ? 'Nothing discovered yet' : `${cap(have.join(' + '))}${missing.length ? `, no ${missing.join(' or ')}` : ''}`
  return { infra, services: hasServices, dependencies, label }
}

const cap = (s: string) => s[0].toUpperCase() + s.slice(1)

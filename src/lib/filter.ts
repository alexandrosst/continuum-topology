// Filtering the topology to some clusters and/or some applications. A pure function of the model, so the
// graph, the map and the tests all agree on what "only these" means.
import type { Topology } from './types'

/** The stand-in id for "services that belong to no application". */
export const NO_APP = '_none'

export interface Filter {
  clusters: string[]
  apps: string[]
}

export type FilterModel = Pick<Topology, 'clusters' | 'nodes' | 'namespaces' | 'services' | 'devices' | 'dependencies' | 'applications' | 'sites' | 'siteLinks' | 'externalEndpoints'>

const list = (v: string | null): string[] =>
  (v ?? '')
    .split(',')
    .map((x) => {
      try {
        return decodeURIComponent(x).trim()
      } catch {
        return ''
      }
    })
    .filter(Boolean)

/** Read the filter from the page's URL options (`clusters=a,b&apps=x`). Ids are percent-encoded so a comma in one is safe. */
export const parseFilter = (sp: URLSearchParams): Filter => ({ clusters: list(sp.get('clusters')), apps: list(sp.get('apps')) })

/** The URL form of one list; null (no option) for an empty one. */
export const encodeList = (ids: string[]): string | null => (ids.length ? ids.map(encodeURIComponent).join(',') : null)

export const filterActive = (f: Filter) => f.clusters.length > 0 || f.apps.length > 0

/** Drop ids that no longer exist (a cluster that was removed) so a stale link cannot filter everything away. */
export function knownOnly(f: Filter, m: Pick<FilterModel, 'clusters' | 'applications'>): Filter {
  const c = new Set(m.clusters.filter((x) => !x.deletedAt).map((x) => x.id))
  const a = new Set(m.applications.filter((x) => !x.deletedAt).map((x) => x.id))
  return { clusters: f.clusters.filter((x) => c.has(x)), apps: f.apps.filter((x) => a.has(x) || x === NO_APP) }
}

/**
 * Keep only the chosen clusters and applications, and whatever hangs off them:
 *  - a service stays if its cluster is chosen (when clusters are) and its application is chosen (when applications are);
 *  - a node stays if its cluster is chosen and, when applications are chosen, it runs one of the kept services;
 *  - a dependency stays only if both of its ends stay, so a filtered view never draws a line to nothing;
 *  - a device or external endpoint stays if something kept talks to it, or (devices) it is attached to a kept node
 *    or belongs to a chosen application;
 *  - sites and links stay if a kept cluster or device is there.
 * Nothing is edited: this is a view.
 */
export function applyFilter<M extends FilterModel>(m: M, f: Filter): M {
  if (!filterActive(f)) return m
  const C = new Set(f.clusters)
  const A = new Set(f.apps)
  const clusterOk = (id: string) => C.size === 0 || C.has(id)
  const appOk = (id?: string) => A.size === 0 || A.has(id ?? NO_APP)

  const services = m.services.filter((s) => clusterOk(s.clusterId) && appOk(s.applicationId))
  const svc = new Set(services.map((s) => s.id))
  const hosts = new Set(services.flatMap((s) => s.nodeIds))
  const nodes = m.nodes.filter((n) => clusterOk(n.clusterId) && (A.size === 0 || hosts.has(n.id)))
  const nodeIds = new Set(nodes.map((n) => n.id))

  // Dependencies that touch a kept service pull in the device or endpoint on the other end.
  const touching = m.dependencies.filter((d) => (d.fromKind === 'service' && svc.has(d.from)) || (d.toKind === 'service' && svc.has(d.to)))
  const linked = new Set(touching.flatMap((d) => [d.from, d.to]))
  const devices = m.devices.filter((d) => linked.has(d.id) || ((C.size === 0 || (!!d.gatewayNodeId && nodeIds.has(d.gatewayNodeId))) && (A.size === 0 ? C.size === 0 || !!d.gatewayNodeId : A.has(d.applicationId ?? NO_APP))))
  const externalEndpoints = m.externalEndpoints.filter((e) => linked.has(e.id))
  const kept = new Set([...svc, ...devices.map((d) => d.id), ...externalEndpoints.map((e) => e.id)])
  const dependencies = touching.filter((d) => kept.has(d.from) && kept.has(d.to))

  const hasService = (clusterId: string) => services.some((s) => s.clusterId === clusterId)
  const clusters = m.clusters.filter((c) => clusterOk(c.id) && (A.size === 0 || hasService(c.id)))
  const cIds = new Set(clusters.map((c) => c.id))
  const namespaces = m.namespaces.filter((n) => cIds.has(n.clusterId) && (A.size === 0 || services.some((s) => s.clusterId === n.clusterId && s.namespace === n.name)))

  const siteIds = new Set([...clusters.map((c) => c.siteId), ...devices.map((d) => d.siteId)].filter((x): x is string => !!x))
  const sites = m.sites.filter((s) => siteIds.has(s.id))
  const siteLinks = m.siteLinks.filter((l) => siteIds.has(l.a) && siteIds.has(l.b))

  return { ...m, clusters, nodes, namespaces, services, devices, dependencies, externalEndpoints, sites, siteLinks }
}

// Filtering the topology to some clusters, applications and/or workload kinds. A pure function of the
// model, so the graph, the map and the tests all agree on what "only these" means.
import type { ServiceKind, Topology } from './types'

/** The stand-in id for "services that belong to no application". */
export const NO_APP = '_none'

/** Every value ServiceKind can take, in the order they read best in a list - the fixed vocabulary `kinds`
 * is validated against (see knownOnly), since unlike a cluster or application id it never comes from data
 * that could legitimately grow or shrink. */
export const SERVICE_KINDS: ServiceKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job']

export interface Filter {
  clusters: string[]
  apps: string[]
  /** Only these workload kinds, e.g. ['Deployment'] to see the "actual services" and hide DaemonSets,
   *  StatefulSets and Jobs. Empty (the default): every kind. */
  kinds: ServiceKind[]
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

/** Read the filter from the page's URL options (`clusters=a,b&apps=x&kinds=Deployment`). Ids are
 * percent-encoded so a comma in one is safe. */
export const parseFilter = (sp: URLSearchParams): Filter => ({
  clusters: list(sp.get('clusters')),
  apps: list(sp.get('apps')),
  kinds: list(sp.get('kinds')) as ServiceKind[],
})

/** The URL form of one list; null (no option) for an empty one. */
export const encodeList = (ids: string[]): string | null => (ids.length ? ids.map(encodeURIComponent).join(',') : null)

export const filterActive = (f: Filter) => f.clusters.length > 0 || f.apps.length > 0 || f.kinds.length > 0

/** Whether the Application view's Kind filter should default to Deployment-only ("real services") - true
 * only for a page that arrived with no `kinds`, `clusters` or `apps` of its own to lose. Kept as a pure
 * function of the URL rather than inline in the page component so it's cheap to test on its own, and so a
 * caller can check it once, at first mount, without needing React: the whole point of defaulting this way
 * (see the call site in TopologyPage.tsx) is that it only ever applies once, when there is nothing yet to
 * conflict with - a person who explicitly asks for every kind (or a specific cluster/application) is always
 * respected, and unchecking Deployment back down to "everything" later never gets silently overwritten. */
export const isFreshApplicationView = (sp: URLSearchParams): boolean =>
  sp.get('view') !== 'infrastructure' && sp.get('view') !== 'map' && !sp.has('kinds') && !sp.has('clusters') && !sp.has('apps')

/** Drop ids that no longer exist (a cluster that was removed), and any kind that isn't a real one (a
 * hand-edited or stale link), so neither can filter everything away. */
export function knownOnly(f: Filter, m: Pick<FilterModel, 'clusters' | 'applications'>): Filter {
  const c = new Set(m.clusters.filter((x) => !x.deletedAt).map((x) => x.id))
  const a = new Set(m.applications.filter((x) => !x.deletedAt).map((x) => x.id))
  const k = new Set<string>(SERVICE_KINDS)
  return {
    clusters: f.clusters.filter((x) => c.has(x)),
    apps: f.apps.filter((x) => a.has(x) || x === NO_APP),
    kinds: f.kinds.filter((x) => k.has(x)),
  }
}

/**
 * Keep only the chosen clusters, applications and workload kinds, and whatever hangs off them:
 *  - a service stays if its cluster is chosen (when clusters are), its application is chosen (when
 *    applications are) and its kind is chosen (when kinds are - e.g. kinds: ['Deployment'] hides every
 *    StatefulSet, DaemonSet and Job so an application view shows just its "ordinary" services);
 *  - a node stays if its cluster is chosen and, when applications or kinds narrow which services are kept,
 *    it still runs one of them;
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
  const K = new Set(f.kinds)
  const clusterOk = (id: string) => C.size === 0 || C.has(id)
  const appOk = (id?: string) => A.size === 0 || A.has(id ?? NO_APP)
  const kindOk = (k: ServiceKind) => K.size === 0 || K.has(k)
  // Whether a service could be filtered away by something other than its own cluster - used below wherever
  // "only keep this if it still has a service" was previously gated on applications alone.
  const narrowsServices = A.size > 0 || K.size > 0

  const services = m.services.filter((s) => clusterOk(s.clusterId) && appOk(s.applicationId) && kindOk(s.kind))
  const svc = new Set(services.map((s) => s.id))
  const hosts = new Set(services.flatMap((s) => s.nodeIds))
  const nodes = m.nodes.filter((n) => clusterOk(n.clusterId) && (!narrowsServices || hosts.has(n.id)))
  const nodeIds = new Set(nodes.map((n) => n.id))

  // Dependencies that touch a kept service pull in the device or endpoint on the other end.
  const touching = m.dependencies.filter((d) => (d.fromKind === 'service' && svc.has(d.from)) || (d.toKind === 'service' && svc.has(d.to)))
  const linked = new Set(touching.flatMap((d) => [d.from, d.to]))
  const devices = m.devices.filter((d) => linked.has(d.id) || ((C.size === 0 || (!!d.gatewayNodeId && nodeIds.has(d.gatewayNodeId))) && (A.size === 0 ? C.size === 0 || !!d.gatewayNodeId : A.has(d.applicationId ?? NO_APP))))
  const externalEndpoints = m.externalEndpoints.filter((e) => linked.has(e.id))
  const kept = new Set([...svc, ...devices.map((d) => d.id), ...externalEndpoints.map((e) => e.id)])
  const dependencies = touching.filter((d) => kept.has(d.from) && kept.has(d.to))

  const hasService = (clusterId: string) => services.some((s) => s.clusterId === clusterId)
  const clusters = m.clusters.filter((c) => clusterOk(c.id) && (!narrowsServices || hasService(c.id)))
  const cIds = new Set(clusters.map((c) => c.id))
  const namespaces = m.namespaces.filter((n) => cIds.has(n.clusterId) && (!narrowsServices || services.some((s) => s.clusterId === n.clusterId && s.namespace === n.name)))

  const siteIds = new Set([...clusters.map((c) => c.siteId), ...devices.map((d) => d.siteId)].filter((x): x is string => !!x))
  const sites = m.sites.filter((s) => siteIds.has(s.id))
  const siteLinks = m.siteLinks.filter((l) => siteIds.has(l.a) && siteIds.has(l.b))

  return { ...m, clusters, nodes, namespaces, services, devices, dependencies, externalEndpoints, sites, siteLinks }
}

/**
 * Backstage-style "max depth": keep only entities within `hops` steps of `focus` in the dependency graph,
 * walked in both directions (what it calls, and what calls it) so the neighborhood is symmetric - a focused
 * service's callers matter exactly as much as what it calls. Independent of Filter/applyFilter above (this
 * is about distance from one entity, not a persisted attribute selection), so a caller runs it as a second,
 * optional pass over whatever applyFilter already kept. `focus` not existing in the model is a no-op (the
 * selection it was reading from may have just been cleared or filtered away) rather than an empty graph.
 */
export function hopNeighborhood<M extends FilterModel>(m: M, focus: string, hops: number): M {
  const known = new Set([...m.services.map((s) => s.id), ...m.devices.map((d) => d.id), ...m.externalEndpoints.map((e) => e.id)])
  if (!known.has(focus)) return m
  const adj = new Map<string, Set<string>>()
  const link = (a: string, b: string) => {
    let set = adj.get(a)
    if (!set) {
      set = new Set()
      adj.set(a, set)
    }
    set.add(b)
  }
  for (const d of m.dependencies) {
    link(d.from, d.to)
    link(d.to, d.from)
  }
  const reached = new Set([focus])
  let frontier = [focus]
  for (let i = 0; i < hops && frontier.length > 0; i++) {
    const next: string[] = []
    for (const id of frontier) {
      for (const nb of adj.get(id) ?? []) {
        if (!reached.has(nb)) {
          reached.add(nb)
          next.push(nb)
        }
      }
    }
    frontier = next
  }

  const services = m.services.filter((s) => reached.has(s.id))
  const svc = new Set(services.map((s) => s.id))
  const devices = m.devices.filter((d) => reached.has(d.id))
  const externalEndpoints = m.externalEndpoints.filter((e) => reached.has(e.id))
  const kept = new Set([...svc, ...devices.map((d) => d.id), ...externalEndpoints.map((e) => e.id)])
  const dependencies = m.dependencies.filter((d) => kept.has(d.from) && kept.has(d.to))

  const nodeIds = new Set(services.flatMap((s) => s.nodeIds))
  const nodes = m.nodes.filter((n) => nodeIds.has(n.id))
  const cIds = new Set(services.map((s) => s.clusterId))
  const clusters = m.clusters.filter((c) => cIds.has(c.id))
  const namespaces = m.namespaces.filter((n) => cIds.has(n.clusterId) && services.some((s) => s.clusterId === n.clusterId && s.namespace === n.name))
  const siteIds = new Set([...clusters.map((c) => c.siteId), ...devices.map((d) => d.siteId)].filter((x): x is string => !!x))
  const sites = m.sites.filter((s) => siteIds.has(s.id))
  const siteLinks = m.siteLinks.filter((l) => siteIds.has(l.a) && siteIds.has(l.b))

  return { ...m, clusters, nodes, namespaces, services, devices, dependencies, externalEndpoints, sites, siteLinks }
}

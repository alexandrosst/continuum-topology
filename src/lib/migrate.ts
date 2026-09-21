import { DEFAULT_ORG, SCHEMA_VERSION, type Dependency, type Device, type ExternalEndpoint, type Model, type Service } from './types'

type Loose = Record<string, unknown>
const arr = (v: unknown): Loose[] => (Array.isArray(v) ? (v as Loose[]) : [])

/**
 * Upgrade any known file/persisted shape to the current schema.
 *  v1 (no schemaVersion): "workloads" instead of "services", no org ids,
 *     dependencies without sources/confidence, no applications/sites/endpoints.
 *  v2: no namespaces, devices, site links, dependency endpoint kinds, or control-plane records.
 *  v3: discovered records are part of the document.
 *  v4: declared intent only; what people said about discovered records is under `refs`.
 * Unknown future versions are rejected instead of being silently mangled.
 */
export function upgrade(input: unknown): Model {
  if (!input || typeof input !== 'object') throw new Error('File is not a JSON object.')
  const t = input as Loose
  const v = typeof t.schemaVersion === 'number' ? t.schemaVersion : 1
  if (v > SCHEMA_VERSION) throw new Error(`This file uses schema version ${v}, newer than this app understands (${SCHEMA_VERSION}).`)

  const servicesRaw = t.services ?? t.workloads // v1 name
  for (const [k, val] of [['clusters', t.clusters], ['nodes', t.nodes], ['services', servicesRaw], ['dependencies', t.dependencies]] as const) {
    if (!Array.isArray(val)) throw new Error(`Missing or invalid "${k}" array.`)
  }

  const stamp = (e: Loose): Loose => ({ orgId: DEFAULT_ORG, source: 'manual', ...e })
  const labelled = (e: Loose): Loose => ({ labels: {}, ...stamp(e) })

  return {
    clusters: arr(t.clusters).map(labelled),
    nodes: arr(t.nodes).map(labelled),
    namespaces: arr(t.namespaces).map(labelled),
    services: arr(servicesRaw).map((e) => ({ nodeIds: [], ...labelled(e) })),
    devices: arr(t.devices).map((e) => ({ kind: 'other', count: 1, protocol: '', connectivity: 'unknown', status: 'unknown', ...labelled(e) })),
    dependencies: arr(t.dependencies).map((e) => ({
      orgId: DEFAULT_ORG,
      fromKind: 'service',
      toKind: 'service',
      sources: ['manual'],
      confidence: 'high',
      ...e,
    })),
    applications: arr(t.applications).map((e) => ({ description: '', origin: 'explicit', confidence: 'high', ...stamp(e) })),
    sites: arr(t.sites).map((e) => ({ orgId: DEFAULT_ORG, ...e, country: String((e as { country?: unknown }).country ?? '').trim().toUpperCase() })),
    siteLinks: arr(t.siteLinks).map((e) => ({ orgId: DEFAULT_ORG, source: 'declared', ...e })),
    externalEndpoints: arr(t.externalEndpoints).map((e) => ({ kind: 'unknown', ...stamp(e) })),
    agents: arr(t.agents).map((e) => ({ orgId: DEFAULT_ORG, accessTier: 0, status: 'pending', modules: [], ...e })),
    suggestions: arr(t.suggestions).map((e) => ({ orgId: DEFAULT_ORG, status: 'open', ...e })),
    auditLog: arr(t.auditLog).map((e) => ({ orgId: DEFAULT_ORG, ...e })),
    savedViews: arr(t.savedViews).filter((e) => typeof e.name === 'string' && typeof e.params === 'string').map((e) => ({ orgId: DEFAULT_ORG, createdAt: '', ...e })),
    refs: refsOf(t.refs),
  } as unknown as Model
}

/** The refs of a v4 document; anything that is not a well-formed ref is ignored. */
function refsOf(v: unknown): Model['refs'] {
  const out: Model['refs'] = {}
  if (!v || typeof v !== 'object' || Array.isArray(v)) return out
  for (const [id, r] of Object.entries(v as Loose)) {
    const kind = (r as Loose | null)?.kind
    if (kind === 'cluster' || kind === 'node' || kind === 'namespace' || kind === 'service') out[id] = r as unknown as Model['refs'][string]
  }
  return out
}

/** Keep only dependencies whose both ends still exist (a service, a device or an external endpoint). */
export function pruneDependencies(
  deps: Dependency[],
  ends: { services: Pick<Service, 'id'>[]; devices: Pick<Device, 'id'>[]; externalEndpoints: Pick<ExternalEndpoint, 'id'>[] },
): Dependency[] {
  const ids = {
    service: new Set(ends.services.map((x) => x.id)),
    device: new Set(ends.devices.map((x) => x.id)),
    external: new Set(ends.externalEndpoints.map((x) => x.id)),
  }
  return deps.filter((d) => ids[d.fromKind]?.has(d.from) && ids[d.toKind]?.has(d.to))
}

/**
 * Upgrade, then repair: drop anything with a dangling reference so the graph
 * views can never crash (missing site/application links are simply cleared).
 */
export function normalize(input: unknown): Model {
  const t = upgrade(input)
  const sites = t.sites
  const sIds = new Set(sites.map((s) => s.id))
  const siteLinks = t.siteLinks.filter((l) => sIds.has(l.a) && sIds.has(l.b) && l.a !== l.b)
  const clusters = t.clusters.map((c) => (c.siteId && !sIds.has(c.siteId) ? { ...c, siteId: undefined } : c))
  const cIds = new Set(clusters.map((c) => c.id))
  const nodes = t.nodes.filter((n) => cIds.has(n.clusterId))
  const nIds = new Set(nodes.map((n) => n.id))
  const applications = t.applications
  const aIds = new Set(applications.map((a) => a.id))
  const app = (id?: string) => (id && aIds.has(id) ? id : undefined)
  const namespaces = t.namespaces.filter((n) => cIds.has(n.clusterId)).map((n) => ({ ...n, applicationId: app(n.applicationId) }))
  const services = t.services
    .filter((w) => cIds.has(w.clusterId))
    .map((w) => ({
      ...w,
      nodeIds: (w.nodeIds ?? []).filter((id) => nIds.has(id)),
      applicationId: app(w.applicationId),
    }))
  const devices = t.devices.map((d) => ({
    ...d,
    applicationId: app(d.applicationId),
    siteId: d.siteId && sIds.has(d.siteId) ? d.siteId : undefined,
    gatewayNodeId: d.gatewayNodeId && nIds.has(d.gatewayNodeId) ? d.gatewayNodeId : undefined,
  }))
  const dependencies = pruneDependencies(t.dependencies, { services, devices, externalEndpoints: t.externalEndpoints })
  const agents = t.agents.map((a) => (a.clusterId && !cIds.has(a.clusterId) ? { ...a, clusterId: undefined } : a))
  return {
    clusters,
    nodes,
    namespaces,
    services,
    devices,
    dependencies,
    applications,
    sites,
    siteLinks,
    externalEndpoints: t.externalEndpoints,
    agents,
    suggestions: t.suggestions,
    auditLog: t.auditLog,
    savedViews: t.savedViews,
    refs: t.refs,
  }
}

import { applyEdit } from './effective'
import type { Model, Suggestion } from './types'

const upsert = <T extends { id: string }>(list: T[], item: T): T[] =>
  list.some((x) => x.id === item.id) ? list.map((x) => (x.id === item.id ? item : x)) : [...list, item]

/**
 * What accepting a suggestion changes in the model. Pure, so it can be tested and
 * (later) run on the server. Edits to discovered records go through applyEdit, so
 * they land as human overrides and survive rediscovery. A suggestion without an
 * `apply` action is informational: accepting it only records the decision.
 */
export function applySuggestion(m: Model, s: Suggestion): Partial<Model> {
  const a = s.apply
  if (!a) return {}
  switch (a.type) {
    case 'add-device':
      return { devices: upsert(m.devices, a.device) }
    case 'add-external':
      return {
        externalEndpoints: upsert(m.externalEndpoints, a.endpoint),
        dependencies: upsert(m.dependencies, a.dependency),
      }
    case 'set-cluster-site':
      return {
        clusters: m.clusters.map((c) => (c.id === a.clusterId ? applyEdit(c, { ...c, siteId: a.siteId }) : c)),
      }
    case 'place-cluster': {
      const existing = m.sites.find((x) => x.id === a.site.id)
      return {
        sites: existing ? m.sites : upsert(m.sites, a.site),
        clusters: m.clusters.map((c) => (c.id === a.clusterId ? applyEdit(c, { ...c, siteId: a.site.id }) : c)),
      }
    }
    case 'create-application': {
      // The application keeps its discovered provenance; the people who accepted it decide membership,
      // so the services get it as an override that survives rediscovery.
      const ids = new Set(a.serviceIds)
      const existing = m.applications.find((x) => x.id === a.application.id)
      return {
        applications: existing && !existing.deletedAt ? m.applications : upsert(m.applications, { ...a.application, deletedAt: undefined }),
        services: m.services.map((w) => (ids.has(w.id) ? applyEdit(w, { ...w, applicationId: a.application.id }) : w)),
      }
    }
    case 'connect-cluster':
      return {} // accepting only records the decision; the Discovery page opens the Connect wizard
    case 'set-service-application': {
      const ids = new Set(a.serviceIds)
      return {
        services: m.services.map((w) => (ids.has(w.id) ? applyEdit(w, { ...w, applicationId: a.applicationId }) : w)),
      }
    }
  }
}

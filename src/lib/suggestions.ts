import { applyEdit } from './effective'
import type { GroupingAlternative, Model, Suggestion } from './types'

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

/**
 * Other labels this application could have been grouped by instead, gathered from the create-application
 * suggestion(s) that produced it - one per cluster it was first seen in, since a cross-cluster application
 * (the same explicit label used in two clusters, say) can be suggested more than once. Each suggestion's
 * alternatives are frozen at the moment discovery made it, same as the winning label was; a person can
 * regroup onto one of these any time afterwards, not only while the original suggestion is still open, but
 * a label that only appeared on services added since is not offered - accepting the suggestion again (or a
 * fresh one, if membership changes enough to trigger one) would pick that up. Empty for an application
 * nobody ever suggested: made by hand, or grouped by hand from the Services page.
 */
export function groupingAlternativesFor(app: { id: string; name: string }, suggestions: Suggestion[]): GroupingAlternative[] {
  const seen = new Set([app.name.toLowerCase()])
  const out: GroupingAlternative[] = []
  for (const s of suggestions) {
    if (s.apply?.type !== 'create-application' || s.apply.application.id !== app.id) continue
    for (const alt of s.apply.alternatives ?? []) {
      const key = alt.name.toLowerCase()
      if (seen.has(key)) continue
      seen.add(key)
      out.push(alt)
    }
  }
  return out
}

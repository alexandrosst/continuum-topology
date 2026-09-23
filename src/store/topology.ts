import { useMemo } from 'react'
import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { applyEdit, applyEffective, describeEdit, effective } from '@/lib/effective'
import { normalize, pruneDependencies } from '@/lib/migrate'
import { withObserved } from '@/lib/observed'
import { atSnapshot } from '@/lib/history'
import { refuseEdit, useHistoryView, viewingThePast } from './history'
import { useObserved } from './observed'
import { seedTopology } from '@/lib/seed'
import { applySuggestion } from '@/lib/suggestions'
import {
  DEFAULT_ORG,
  SCHEMA_VERSION,
  type Agent,
  type Application,
  type AuditEvent,
  type Cluster,
  type Dependency,
  type Device,
  type ExternalEndpoint,
  type GroupingAlternative,
  type MachineNode,
  type Model,
  type Namespace,
  type Service,
  type Site,
  type SavedView,
  type SiteLink,
  type Suggestion,
  type TopologyFile,
} from '@/lib/types'

export { normalize }

export const uid = (prefix: string) =>
  `${prefix}-${Math.random().toString(36).slice(2, 8)}${Date.now().toString(36).slice(-3)}`

type Layered = 'cluster' | 'node' | 'service' | 'device'

interface Actions {
  /** Edit-aware saves used by the forms: on discovered entities they write overrides, not base values. */
  saveCluster: (c: Cluster) => void
  saveNode: (n: MachineNode) => void
  saveService: (s: Service) => void
  saveDevice: (d: Device) => void
  /** Raw upserts (used by importers / the future agent, which own the base values). */
  upsertCluster: (c: Cluster) => void
  upsertNode: (n: MachineNode) => void
  upsertService: (s: Service) => void
  upsertDevice: (d: Device) => void
  deleteCluster: (id: string) => void
  deleteNode: (id: string) => void
  deleteService: (id: string) => void
  deleteDevice: (id: string) => void
  upsertNamespace: (n: Namespace) => void
  deleteNamespace: (id: string) => void
  upsertDependency: (d: Dependency) => void
  deleteDependency: (id: string) => void
  upsertApplication: (a: Application) => void
  deleteApplication: (id: string) => void
  upsertSite: (s: Site) => void
  deleteSite: (id: string) => void
  upsertSiteLink: (l: SiteLink) => void
  deleteSiteLink: (id: string) => void
  upsertExternalEndpoint: (e: ExternalEndpoint) => void
  deleteExternalEndpoint: (id: string) => void
  upsertAgent: (a: Agent) => void
  /** Accept (applies its action, if any) or dismiss a suggestion; both are audited. */
  decideSuggestion: (id: string, decision: 'accepted' | 'dismissed') => void
  /** Decide a suggestion that was worked out on the fly (not stored yet): the decision is what gets stored. */
  decideDerived: (sug: Suggestion, decision: 'accepted' | 'dismissed') => void
  /**
   * Accept an application-grouping suggestion, but under one of its alternatives instead of the label
   * that won by default - a fresh application named after that label, not the one the suggestion offered.
   */
  applyAlternative: (suggestionId: string, alt: GroupingAlternative) => void
  addAudit: (e: Omit<AuditEvent, 'id' | 'orgId' | 'at'>) => void
  /** Save the given view options under a name; a view with the same name is replaced. */
  saveView: (name: string, params: string) => void
  deleteView: (id: string) => void
  /** Drop human overrides so an entity shows its detected values again. */
  resetOverrides: (kind: Layered, id: string) => void
  replaceAll: (t: Model) => void
  reset: () => void
  clear: () => void
}

type RawState = Model & Actions

const upsert = <T extends { id: string }>(list: T[], item: T): T[] =>
  list.some((x) => x.id === item.id) ? list.map((x) => (x.id === item.id ? item : x)) : [...list, item]

const EMPTY: Model = {
  clusters: [],
  nodes: [],
  namespaces: [],
  services: [],
  devices: [],
  dependencies: [],
  applications: [],
  sites: [],
  siteLinks: [],
  externalEndpoints: [],
  agents: [],
  suggestions: [],
  auditLog: [],
  savedViews: [],
  refs: {},
}

const STATE_KEYS = Object.keys(EMPTY) as (keyof Model)[]
const ACTOR = 'you'

const pick = (s: RawState): Model => Object.fromEntries(STATE_KEYS.map((k) => [k, s[k]])) as unknown as Model

export const useRawTopology = create<RawState>()(
  persist(
    (rawSet) => {
      // Edits are refused while a recorded moment is shown: it is a picture of the past, not something to change.
      const set = ((...a: Parameters<typeof rawSet>) => {
        if (viewingThePast()) return refuseEdit()
        return rawSet(...a)
      }) as typeof rawSet
      const decide = (s: RawState, sug: Suggestion | undefined, decision: 'accepted' | 'dismissed'): Partial<RawState> => {
        if (!sug || sug.status !== 'open') return {}
        const changes = decision === 'accepted' ? applySuggestion(pick(s), sug) : {}
        const next = { ...pick(s), ...changes }
        return {
          ...changes,
          dependencies: pruneDependencies(next.dependencies, next),
          suggestions: s.suggestions.map((x) =>
            x.id === sug.id ? { ...x, status: decision, decidedBy: ACTOR, decidedAt: new Date().toISOString() } : x,
          ),
          auditLog: audit(s, { actor: ACTOR, action: decision === 'accepted' ? 'accept-suggestion' : 'dismiss-suggestion', targetKind: 'suggestion', targetId: sug.id, detail: sug.title }),
        }
      }
      const audit = (s: RawState, e: Omit<AuditEvent, 'id' | 'orgId' | 'at'>): AuditEvent[] => [
        ...s.auditLog,
        { id: uid('ev'), orgId: DEFAULT_ORG, at: new Date().toISOString(), ...e },
      ]
      const prune = (s: RawState, over: Partial<Model>) => {
        const next = { ...pick(s), ...over }
        return { ...over, dependencies: pruneDependencies(next.dependencies, next) }
      }
      // A save is only worth an audit entry when it changes something a person already had in front of
      // them - a brand-new manual entity (raw undefined) has nothing to diff against and would otherwise
      // log every one of its fields as "changed", so creation is left to speak for itself by existing
      // (same as `deleteCluster` logging deletes but not creates, further down).
      const editAudit = <T extends { overrides?: Record<string, unknown>; name?: string }>(s: RawState, raw: T | undefined, kind: string, id: string, next: T): AuditEvent[] => {
        if (!raw) return s.auditLog
        const diff = describeEdit(effective(raw), next)
        return diff ? audit(s, { actor: ACTOR, action: 'edit', targetKind: kind, targetId: id, detail: next.name ? `${next.name} — ${diff}` : diff }) : s.auditLog
      }

      return {
        ...seedTopology(),

        saveCluster: (c) =>
          set((s) => {
            const raw = s.clusters.find((x) => x.id === c.id)
            return { clusters: upsert(s.clusters, applyEdit(raw, c)), auditLog: editAudit(s, raw, 'cluster', c.id, c) }
          }),
        saveNode: (n) =>
          set((s) => {
            const raw = s.nodes.find((x) => x.id === n.id)
            const saved = applyEdit(raw, n)
            // If a node moved cluster, drop placements that no longer make sense.
            const services = s.services.map((w) =>
              w.nodeIds.includes(saved.id) && w.clusterId !== saved.clusterId
                ? { ...w, nodeIds: w.nodeIds.filter((id) => id !== saved.id) }
                : w,
            )
            return { nodes: upsert(s.nodes, saved), services, auditLog: editAudit(s, raw, 'node', n.id, n) }
          }),
        saveService: (w) =>
          set((s) => {
            const raw = s.services.find((x) => x.id === w.id)
            return { services: upsert(s.services, applyEdit(raw, w)), auditLog: editAudit(s, raw, 'service', w.id, w) }
          }),
        saveDevice: (d) =>
          set((s) => {
            const raw = s.devices.find((x) => x.id === d.id)
            return { devices: upsert(s.devices, applyEdit(raw, d)), auditLog: editAudit(s, raw, 'device', d.id, d) }
          }),

        upsertCluster: (c) => set((s) => ({ clusters: upsert(s.clusters, c) })),
        upsertNode: (n) => set((s) => ({ nodes: upsert(s.nodes, n) })),
        upsertService: (w) => set((s) => ({ services: upsert(s.services, w) })),
        upsertDevice: (d) => set((s) => ({ devices: upsert(s.devices, d) })),

        deleteCluster: (id) =>
          set((s) => {
            const nodes = s.nodes.filter((n) => n.clusterId !== id)
            const nIds = new Set(nodes.map((n) => n.id))
            const services = s.services.filter((w) => w.clusterId !== id)
            return {
              clusters: s.clusters.filter((c) => c.id !== id),
              nodes,
              namespaces: s.namespaces.filter((n) => n.clusterId !== id),
              services,
              devices: s.devices.map((d) => (d.gatewayNodeId && !nIds.has(d.gatewayNodeId) ? { ...d, gatewayNodeId: undefined } : d)),
              agents: s.agents.map((a) => (a.clusterId === id ? { ...a, clusterId: undefined } : a)),
              ...prune(s, { services }),
              auditLog: audit(s, { actor: ACTOR, action: 'delete', targetKind: 'cluster', targetId: id }),
            }
          }),
        deleteNode: (id) =>
          set((s) => ({
            nodes: s.nodes.filter((n) => n.id !== id),
            services: s.services.map((w) => ({ ...w, nodeIds: w.nodeIds.filter((x) => x !== id) })),
            devices: s.devices.map((d) => (d.gatewayNodeId === id ? { ...d, gatewayNodeId: undefined } : d)),
          })),
        deleteService: (id) =>
          set((s) => {
            const services = s.services.filter((w) => w.id !== id)
            return { services, ...prune(s, { services }) }
          }),
        deleteDevice: (id) =>
          set((s) => {
            const devices = s.devices.filter((d) => d.id !== id)
            return { devices, ...prune(s, { devices }) }
          }),

        upsertNamespace: (n) => set((s) => ({ namespaces: upsert(s.namespaces, n) })),
        deleteNamespace: (id) => set((s) => ({ namespaces: s.namespaces.filter((n) => n.id !== id) })),

        upsertDependency: (d) => set((s) => ({ dependencies: upsert(s.dependencies, d) })),
        deleteDependency: (id) => set((s) => ({ dependencies: s.dependencies.filter((d) => d.id !== id) })),

        upsertApplication: (a) => set((s) => ({ applications: upsert(s.applications, a) })),
        deleteApplication: (id) =>
          set((s) => ({
            applications: s.applications.filter((a) => a.id !== id),
            services: s.services.map((w) => (w.applicationId === id ? { ...w, applicationId: undefined } : w)),
            devices: s.devices.map((d) => (d.applicationId === id ? { ...d, applicationId: undefined } : d)),
            namespaces: s.namespaces.map((n) => (n.applicationId === id ? { ...n, applicationId: undefined } : n)),
          })),

        upsertSite: (site) =>
          set((s) => {
            const raw = s.sites.find((x) => x.id === site.id)
            const diff = raw && describeEdit(raw, site)
            const detail = diff ? `${site.name} — ${diff}` : ''
            return { sites: upsert(s.sites, site), ...(detail ? { auditLog: audit(s, { actor: ACTOR, action: 'edit', targetKind: 'site', targetId: site.id, detail }) } : {}) }
          }),
        deleteSite: (id) =>
          set((s) => ({
            sites: s.sites.filter((x) => x.id !== id),
            siteLinks: s.siteLinks.filter((l) => l.a !== id && l.b !== id),
            clusters: s.clusters.map((c) => (c.siteId === id ? { ...c, siteId: undefined } : c)),
            devices: s.devices.map((d) => (d.siteId === id ? { ...d, siteId: undefined } : d)),
          })),
        upsertSiteLink: (l) => set((s) => ({ siteLinks: upsert(s.siteLinks, l) })),
        deleteSiteLink: (id) => set((s) => ({ siteLinks: s.siteLinks.filter((l) => l.id !== id) })),

        upsertExternalEndpoint: (e) => set((s) => ({ externalEndpoints: upsert(s.externalEndpoints, e) })),
        deleteExternalEndpoint: (id) =>
          set((s) => {
            const externalEndpoints = s.externalEndpoints.filter((e) => e.id !== id)
            return { externalEndpoints, ...prune(s, { externalEndpoints }) }
          }),

        upsertAgent: (a) => set((s) => ({ agents: upsert(s.agents, a) })),
        decideSuggestion: (id, decision) => set((s) => decide(s, s.suggestions.find((x) => x.id === id), decision)),
        decideDerived: (sug, decision) =>
          set((s) => {
            const stored = s.suggestions.find((x) => x.id === sug.id)
            if (stored) return decide(s, stored, decision)
            return decide({ ...s, suggestions: [...s.suggestions, sug] }, sug, decision)
          }),
        applyAlternative: (suggestionId, alt) =>
          set((s) => {
            const sug = s.suggestions.find((x) => x.id === suggestionId)
            if (!sug || sug.status !== 'open' || sug.apply?.type !== 'create-application') return {}
            // A fresh application, named after the chosen label instead of the one that won by default.
            // It gets its own id rather than the server's stable hash: a person picking an alternative
            // here is overriding discovery's own answer, not confirming it, so there is nothing to keep
            // in step with what a future resync would compute for that name.
            const app: Application = { ...sug.apply.application, id: uid('app'), name: alt.name, origin: alt.origin, confidence: alt.confidence }
            const reworked: Suggestion = {
              ...sug,
              title: sug.title.replace(/“[^”]*”$/, `“${alt.name}”`),
              detail: `Grouped as "${alt.name}" instead - ${alt.signal}${alt.confidence !== 'high' ? ` (${alt.confidence} confidence)` : ''}.`,
              apply: { ...sug.apply, application: app },
            }
            // Replace the stored suggestion with the reworked one before deciding, so status/decidedAt
            // land on the version that reflects what was actually applied, not the original wording.
            return decide({ ...s, suggestions: s.suggestions.map((x) => (x.id === suggestionId ? reworked : x)) }, reworked, 'accepted')
          }),
        addAudit: (e) => set((s) => ({ auditLog: audit(s, e) })),
        saveView: (name, params) =>
          set((s) => {
            const n = name.trim()
            if (!n) return {}
            const same = s.savedViews.find((v) => v.name.toLowerCase() === n.toLowerCase())
            const view: SavedView = { id: same?.id ?? uid('view'), orgId: DEFAULT_ORG, name: n, params, createdAt: same?.createdAt ?? new Date().toISOString() }
            return { savedViews: upsert(s.savedViews, view), auditLog: audit(s, { actor: ACTOR, action: 'save-view', targetKind: 'view', targetId: view.id, detail: n }) }
          }),
        deleteView: (id) => set((s) => ({ savedViews: s.savedViews.filter((v) => v.id !== id) })),

        resetOverrides: (kind, id) =>
          set((s) => {
            const strip = <T extends { id: string }>(l: T[]) => l.map((x) => (x.id === id ? { ...x, overrides: undefined } : x))
            switch (kind) {
              case 'cluster':
                return { clusters: strip(s.clusters) }
              case 'node':
                return { nodes: strip(s.nodes) }
              case 'device':
                return { devices: strip(s.devices) }
              default:
                return { services: strip(s.services) }
            }
          }),

        replaceAll: (t) => set({ ...normalize(t) }),
        reset: () => set({ ...seedTopology() }),
        clear: () => rawSet({ ...EMPTY }),
      }
    },
    {
      name: 'continuum-topology/v1', // key kept so existing browsers upgrade in place via `version` + `migrate`
      version: SCHEMA_VERSION,
      partialize: (s) => pick(s),
      migrate: (persisted) => {
        try {
          return normalize(persisted) as unknown as RawState
        } catch {
          return seedTopology() as unknown as RawState
        }
      },
    },
  ),
)

/**
 * The store as the UI sees it: entity values are already merged with human
 * overrides and tombstoned records are left out. Pass a selector for a slice,
 * or nothing for everything.
 */
export function useTopology(): RawState
export function useTopology<T>(selector: (s: RawState) => T): T
export function useTopology<T>(selector?: (s: RawState) => T) {
  const raw = useRawTopology()
  const liveDeps = useObserved((s) => s.dependencies)
  const liveExt = useObserved((s) => s.externalEndpoints)
  const past = useHistoryView((s) => s.snapshot)
  const eff = useMemo<RawState>(() => {
    const model = past ? { ...raw, ...atSnapshot(raw, past) } : raw
    const e = { ...model, ...applyEffective(model) }
    const seen = past ? { dependencies: past.dependencies, externalEndpoints: past.externalEndpoints } : { dependencies: liveDeps, externalEndpoints: liveExt }
    return { ...e, ...withObserved(e, seen) }
  }, [raw, liveDeps, liveExt, past])
  return selector ? selector(eff) : eff
}

/** Raw (un-merged) data with overrides intact: what Export writes. */
export function exportTopology(): TopologyFile {
  return { schemaVersion: SCHEMA_VERSION, ...pick(useRawTopology.getState()) }
}

/** Measured network paths: the recorded ones while a past moment is shown. */
export function usePaths() {
  const live = useObserved((s) => s.paths)
  const past = useHistoryView((s) => s.snapshot)
  return past ? past.paths : live
}

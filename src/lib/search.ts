// The command palette's index and ranking. Pure: it takes the model and returns things you can go to.
import type { Model, SavedView } from './types'
import { placeLabel } from './present'

export type SearchKind = 'page' | 'view' | 'cluster' | 'node' | 'service' | 'device' | 'site' | 'application' | 'external'

export interface SearchItem {
  id: string
  kind: SearchKind
  title: string
  subtitle: string
  /** Extra words that should find the item but are not shown. */
  keywords: string
  /** Router location to open. */
  to: string
}

export const KIND_LABEL: Record<SearchKind, string> = {
  page: 'Page', view: 'View', cluster: 'Cluster', node: 'Node', service: 'Service', device: 'Device', site: 'Site', application: 'Application', external: 'External',
}

const KIND_ORDER: SearchKind[] = ['page', 'view', 'cluster', 'service', 'application', 'node', 'device', 'site', 'external']

export const PAGES: { to: string; label: string; keywords: string }[] = [
  { to: '/topology', label: 'Topology', keywords: 'graph canvas map' },
  { to: '/clusters', label: 'Clusters', keywords: 'kubernetes k8s' },
  { to: '/nodes', label: 'Nodes', keywords: 'machines vms' },
  { to: '/namespaces', label: 'Namespaces', keywords: 'ns scope mesh workloads' },
  { to: '/services', label: 'Services', keywords: 'microservices workloads deployments' },
  { to: '/devices', label: 'Devices', keywords: 'iot sensors plc cameras' },
  { to: '/applications', label: 'Applications', keywords: 'apps' },
  { to: '/sites', label: 'Sites', keywords: 'places locations regions' },
  { to: '/discovery', label: 'Discovery', keywords: 'inbox suggestions found gone' },
  { to: '/agents', label: 'Agents', keywords: 'approve approval enroll connect consent health' },
  { to: '/history', label: 'History', keywords: 'past changes events time' },
  { to: '/settings', label: 'Settings', keywords: 'preferences server' },
]

const topo = (params: Record<string, string>) => `/topology?${new URLSearchParams(params).toString()}`

type Searchable = Pick<Model, 'clusters' | 'nodes' | 'services' | 'devices' | 'applications' | 'sites' | 'externalEndpoints'> & { savedViews?: SavedView[] }

export function buildSearchIndex(m: Searchable): SearchItem[] {
  const alive = <T extends { deletedAt?: string }>(xs: T[]) => xs.filter((x) => !x.deletedAt)
  const cname = new Map(m.clusters.map((c) => [c.id, c.name]))
  const items: SearchItem[] = []
  for (const p of PAGES) items.push({ id: `page:${p.to}`, kind: 'page', title: p.label, subtitle: 'Go to', keywords: p.keywords, to: p.to })
  for (const v of m.savedViews ?? []) items.push({ id: `view:${v.id}`, kind: 'view', title: v.name, subtitle: 'Saved topology view', keywords: v.params, to: `/topology${v.params ? `?${v.params}` : ''}` })
  for (const c of alive(m.clusters)) items.push({ id: `cluster:${c.id}`, kind: 'cluster', title: c.name, subtitle: [c.distribution, c.provider, c.region].filter(Boolean).join(' · '), keywords: `${c.tier} ${c.version}`, to: topo({ sel: `cluster:${c.id}` }) })
  for (const n of alive(m.nodes)) items.push({ id: `node:${n.id}`, kind: 'node', title: n.name, subtitle: [cname.get(n.clusterId), n.ip].filter(Boolean).join(' · '), keywords: `${n.role} ${n.kind} ${n.hardwareModel ?? ''} ${n.instanceType ?? ''} ${n.ip}`, to: topo({ view: 'infrastructure', sel: `node:${n.id}` }) })
  for (const w of alive(m.services)) items.push({ id: `service:${w.id}`, kind: 'service', title: w.name, subtitle: [w.namespace, cname.get(w.clusterId)].filter(Boolean).join(' · '), keywords: `${w.image} ${w.kind}`, to: topo({ sel: `service:${w.id}` }) })
  for (const d of alive(m.devices)) items.push({ id: `device:${d.id}`, kind: 'device', title: d.name, subtitle: [d.kind, d.protocol].filter(Boolean).join(' · '), keywords: `${d.hardwareModel ?? ''} ${d.connectivity}`, to: topo({ sel: `device:${d.id}` }) })
  for (const a of alive(m.applications)) items.push({ id: `application:${a.id}`, kind: 'application', title: a.name, subtitle: a.description, keywords: a.origin, to: '/applications' })
  for (const s of m.sites) items.push({ id: `site:${s.id}`, kind: 'site', title: s.name, subtitle: placeLabel(s), keywords: `${s.kind} ${s.country}`, to: topo({ view: 'map', sel: `site:${s.id}` }) })
  for (const e of alive(m.externalEndpoints)) items.push({ id: `external:${e.id}`, kind: 'external', title: e.name ?? e.host, subtitle: e.kind, keywords: e.name ? e.host : '', to: topo({ sel: `external:${e.id}` }) })
  return items
}

const fold = (s: string) => s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase()

/** How well a token matches: at the start of the title is best, then a word start, then anywhere, then the small print. */
function scoreToken(title: string, rest: string, token: string): number {
  if (title.startsWith(token)) return 100
  if (title.split(/[\s\-_./:]+/).some((w) => w.startsWith(token))) return 70
  if (title.includes(token)) return 45
  if (rest.includes(token)) return 15
  return 0
}

/** Items matching every word typed, best first. With nothing typed, the pages and saved views. */
export function searchItems(items: SearchItem[], query: string, limit = 40): SearchItem[] {
  const tokens = fold(query).split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return items.filter((i) => i.kind === 'page' || i.kind === 'view').slice(0, limit)
  const scored: { item: SearchItem; score: number }[] = []
  for (const item of items) {
    const title = fold(item.title)
    const rest = fold(`${item.subtitle} ${item.keywords}`)
    let total = 0
    let ok = true
    for (const t of tokens) {
      const s = scoreToken(title, rest, t)
      if (s === 0) {
        ok = false
        break
      }
      total += s
    }
    if (ok) scored.push({ item, score: total })
  }
  return scored
    .sort((a, b) => b.score - a.score || KIND_ORDER.indexOf(a.item.kind) - KIND_ORDER.indexOf(b.item.kind) || a.item.title.localeCompare(b.item.title))
    .slice(0, limit)
    .map((s) => s.item)
}

/** Parse the `sel` URL option ("service:w-gw") into a selection to open, or undefined when it is malformed. */
export function parseSel(v: string | null): { kind: 'cluster' | 'node' | 'service' | 'device' | 'site' | 'external'; id: string } | undefined {
  if (!v) return undefined
  const i = v.indexOf(':')
  if (i < 1) return undefined
  const kind = v.slice(0, i)
  const id = v.slice(i + 1)
  if (!id || !['cluster', 'node', 'service', 'device', 'site', 'external'].includes(kind)) return undefined
  return { kind: kind as 'cluster', id }
}

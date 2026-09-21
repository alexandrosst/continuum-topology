// Saved Topology views are named sets of URL options. These helpers make two spellings of the same view
// compare equal ("view=application" is what no view option means) and describe a view in words.
import type { SavedView } from './types'

/** The options a saved view keeps, with the value that means "the default", which is left out of the URL. */
const OPTIONS: Record<string, string> = { view: 'application', group: 'cluster', services: '0', links: '1', devices: '1', labels: '0', mesh: '0', clusters: '', apps: '' }

/** Canonical form of the view options in a URL: known options only, defaults dropped, sorted. */
export function viewParams(input: URLSearchParams | string): string {
  const sp = typeof input === 'string' ? new URLSearchParams(input) : input
  const out = new URLSearchParams()
  for (const k of Object.keys(OPTIONS).sort()) {
    const v = sp.get(k)
    if (v !== null && v !== OPTIONS[k]) out.set(k, v)
  }
  return out.toString()
}

export const sameView = (a: string, b: string) => viewParams(a) === viewParams(b)

const VIEW_NAME: Record<string, string> = { application: 'Application', infrastructure: 'Infrastructure', map: 'Map' }

/** "Infrastructure, grouped by tier, services on nodes" */
export function describeView(params: string): string {
  const sp = new URLSearchParams(viewParams(params))
  const view = sp.get('view') ?? 'application'
  const bits = [VIEW_NAME[view] ?? view]
  if (view !== 'map') {
    if (sp.get('group') === 'tier') bits.push('grouped by tier')
    if (sp.get('services') === '1') bits.push('services on nodes')
    if (sp.get('links') === '0') bits.push('no cross-cluster links')
    if (sp.get('devices') === '0') bits.push('no devices')
    if (sp.get('labels') === '1') bits.push('edge labels')
    if (sp.get('mesh') === '1') bits.push('service mesh')
  }
  const n = (k: string) => (sp.get(k) ? sp.get(k)!.split(',').filter(Boolean).length : 0)
  if (n('clusters')) bits.push(`${n('clusters')} cluster${n('clusters') === 1 ? '' : 's'} only`)
  if (n('apps')) bits.push(`${n('apps')} application${n('apps') === 1 ? '' : 's'} only`)
  return bits.join(', ')
}

/** The saved view whose options match the URL, if any (used to highlight it in the menu). */
export const activeView = (views: SavedView[], sp: URLSearchParams): SavedView | undefined => {
  const now = viewParams(sp)
  return views.find((v) => viewParams(v.params) === now)
}

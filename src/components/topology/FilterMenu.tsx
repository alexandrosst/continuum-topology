import { Filter as FilterIcon, X } from 'lucide-react'
import { Button, ICON_SM, MenuPanel } from '@/components/ui/primitives'
import { filterActive, NO_APP, SERVICE_KINDS, type Filter } from '@/lib/filter'
import type { Application, Cluster } from '@/lib/types'

/** A short label next to each workload kind in the Filter menu's "Kind" section - Deployment is the
 * common case and needs no explanation. */
const KIND_HINT: Record<(typeof SERVICE_KINDS)[number], string | undefined> = {
  Deployment: undefined,
  StatefulSet: 'stateful',
  DaemonSet: 'per node',
  Job: 'runs once',
}

/**
 * "Show only these": a popover with a checklist of clusters, one of applications, and one of workload kinds
 * (e.g. tick just Deployment to hide DaemonSets, StatefulSets and Jobs - the "real services" view). Nothing
 * chosen in a list means everything. The filter lives in the URL, so a filtered view can be linked and saved.
 *
 * Controlled (`open`/`onOpenChange`) rather than managing its own state: the Topology toolbar has several of
 * these popovers side by side (this one, saved views, options, add), and only one of them may be open at a
 * time - a page-level switch enforces that, click-outside and Escape included, so two never fight over the screen.
 */
export default function FilterMenu({
  open,
  onOpenChange,
  filter,
  clusters,
  applications,
  onChange,
  platform,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  filter: Filter
  clusters: Cluster[]
  applications: Application[]
  onChange: (f: Filter) => void
  /** Only while the platform layer is on: a filter of its own, for the parts of the layer that are not working. */
  platform?: { problemsOnly: boolean; onChange: (v: boolean) => void }
}) {
  const count = filter.clusters.length + filter.apps.length + filter.kinds.length + (platform?.problemsOnly ? 1 : 0)

  const toggle = (key: 'clusters' | 'apps' | 'kinds', id: string) => {
    const cur = filter[key]
    onChange({ ...filter, [key]: cur.includes(id as never) ? cur.filter((x) => x !== id) : [...cur, id] } as Filter)
  }

  const list = (key: 'clusters' | 'apps' | 'kinds', items: { id: string; name: string; hint?: string }[], empty: string) => (
    <div>
      {items.length === 0 && <p className="px-3 py-2 text-xs text-nb-500">{empty}</p>}
      {items.map((it) => (
        <label key={it.id} className="flex cursor-pointer items-center gap-2 rounded-md px-3 py-1.5 text-sm text-nb-300 hover:bg-nb-940">
          <input type="checkbox" className="accent-[var(--color-accent,#f68330)]" checked={(filter[key] as string[]).includes(it.id)} onChange={() => toggle(key, it.id)} data-testid={`filter-${key}-${it.id}`} />
          <span className="min-w-0 flex-1 truncate">{it.name}</span>
          {it.hint && <span className="shrink-0 text-xs text-nb-500">{it.hint}</span>}
        </label>
      ))}
    </div>
  )

  return (
    <div className="relative">
      <Button onClick={() => onOpenChange(!open)} aria-haspopup="dialog" aria-expanded={open} aria-label="Filter the topology" data-testid="filter-button">
        <FilterIcon size={ICON_SM} className={filterActive(filter) ? 'text-accent' : ''} />
        <span>Filter</span>
        {count > 0 && (
          <span className="rounded-full bg-accent/20 px-1.5 text-xs text-accent" data-testid="filter-count">
            {count}
          </span>
        )}
      </Button>
      <MenuPanel open={open} onClose={() => onOpenChange(false)} className="w-80 overflow-hidden" role="dialog" aria-label="Filter the topology" data-testid="filter-menu">
        <div className="flex items-center justify-between border-b border-nb-850 px-3 py-2">
          <span className="text-sm font-medium text-nb-200">Show only…</span>
          <button
            className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-nb-400 hover:bg-nb-850 hover:text-nb-200 disabled:opacity-40"
            disabled={!filterActive(filter)}
            onClick={() => onChange({ clusters: [], apps: [], kinds: [] })}
            data-testid="filter-clear"
          >
            <X size={ICON_SM} /> Clear
          </button>
        </div>
        <div className="max-h-[26rem] overflow-y-auto p-1">
          <div className="px-3 pb-1 pt-2 text-xs uppercase tracking-wide text-nb-500">Clusters</div>
          {list('clusters', clusters.map((c) => ({ id: c.id, name: c.name, hint: c.tier })), 'No clusters yet.')}
          <div className="px-3 pb-1 pt-3 text-xs uppercase tracking-wide text-nb-500">Applications</div>
          {list('apps', [...applications.map((a) => ({ id: a.id, name: a.name })), { id: NO_APP, name: 'No application', hint: 'unassigned' }], '')}
          <div className="px-3 pb-1 pt-3 text-xs uppercase tracking-wide text-nb-500">Kind</div>
          {list('kinds', SERVICE_KINDS.map((k) => ({ id: k, name: k, hint: KIND_HINT[k] })), '')}
          {platform && (
            <>
              <div className="px-3 pb-1 pt-3 text-xs uppercase tracking-wide text-nb-500">Platform</div>
              <label className="flex cursor-pointer items-center gap-2 rounded-md px-3 py-1.5 text-sm text-nb-300 hover:bg-nb-940">
                <input type="checkbox" className="accent-[var(--color-accent,#f68330)]" checked={platform.problemsOnly} onChange={(e) => platform.onChange(e.target.checked)} data-testid="filter-platform-problems" />
                <span className="min-w-0 flex-1 truncate">Problems only</span>
                <span className="shrink-0 text-xs text-nb-500">hides what is healthy</span>
              </label>
            </>
          )}
        </div>
        <p className="border-t border-nb-850 px-3 py-2 text-xs text-nb-500">Nothing ticked in a list means all of it. A dependency is shown only if both ends are.</p>
      </MenuPanel>
    </div>
  )
}

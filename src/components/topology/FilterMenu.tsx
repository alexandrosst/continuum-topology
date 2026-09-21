import { Filter as FilterIcon, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/primitives'
import { filterActive, NO_APP, type Filter } from '@/lib/filter'
import type { Application, Cluster } from '@/lib/types'

/**
 * "Show only these": a popover with a checklist of clusters and one of applications. Nothing chosen in a
 * list means everything. The filter lives in the URL, so a filtered view can be linked and saved.
 */
export default function FilterMenu({
  filter,
  clusters,
  applications,
  onChange,
}: {
  filter: Filter
  clusters: Cluster[]
  applications: Application[]
  onChange: (f: Filter) => void
}) {
  const [open, setOpen] = useState(false)
  const count = filter.clusters.length + filter.apps.length

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const toggle = (key: 'clusters' | 'apps', id: string) => {
    const cur = filter[key]
    onChange({ ...filter, [key]: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] })
  }

  const list = (key: 'clusters' | 'apps', items: { id: string; name: string; hint?: string }[], empty: string) => (
    <div>
      {items.length === 0 && <p className="px-3 py-2 text-xs text-nb-500">{empty}</p>}
      {items.map((it) => (
        <label key={it.id} className="flex cursor-pointer items-center gap-2 rounded-md px-3 py-1.5 text-sm text-nb-300 hover:bg-nb-940">
          <input type="checkbox" className="accent-[var(--color-accent,#f68330)]" checked={filter[key].includes(it.id)} onChange={() => toggle(key, it.id)} data-testid={`filter-${key}-${it.id}`} />
          <span className="min-w-0 flex-1 truncate">{it.name}</span>
          {it.hint && <span className="shrink-0 text-xs text-nb-500">{it.hint}</span>}
        </label>
      ))}
    </div>
  )

  return (
    <div className="relative">
      <Button onClick={() => setOpen((o) => !o)} aria-haspopup="dialog" aria-expanded={open} aria-label="Filter the topology" data-testid="filter-button">
        <FilterIcon size={15} className={filterActive(filter) ? 'text-accent' : ''} />
        <span>Filter</span>
        {count > 0 && (
          <span className="rounded-full bg-accent/20 px-1.5 text-xs text-accent" data-testid="filter-count">
            {count}
          </span>
        )}
      </Button>
      {open && (
        <>
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
          <div role="dialog" aria-label="Filter the topology" className="absolute right-0 top-11 z-20 w-80 overflow-hidden rounded-lg border border-nb-850 bg-nb-920 shadow-xl" data-testid="filter-menu">
            <div className="flex items-center justify-between border-b border-nb-850 px-3 py-2">
              <span className="text-sm font-medium text-nb-200">Show only…</span>
              <button
                className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-nb-400 hover:bg-nb-850 hover:text-nb-200 disabled:opacity-40"
                disabled={!filterActive(filter)}
                onClick={() => onChange({ clusters: [], apps: [] })}
                data-testid="filter-clear"
              >
                <X size={12} /> Clear
              </button>
            </div>
            <div className="max-h-[26rem] overflow-y-auto p-1">
              <div className="px-3 pb-1 pt-2 text-xs uppercase tracking-wide text-nb-500">Clusters</div>
              {list('clusters', clusters.map((c) => ({ id: c.id, name: c.name, hint: c.tier })), 'No clusters yet.')}
              <div className="px-3 pb-1 pt-3 text-xs uppercase tracking-wide text-nb-500">Applications</div>
              {list('apps', [...applications.map((a) => ({ id: a.id, name: a.name })), { id: NO_APP, name: 'No application', hint: 'unassigned' }], '')}
            </div>
            <p className="border-t border-nb-850 px-3 py-2 text-xs text-nb-500">Nothing ticked in a list means all of it. A dependency is shown only if both ends are.</p>
          </div>
        </>
      )}
    </div>
  )
}

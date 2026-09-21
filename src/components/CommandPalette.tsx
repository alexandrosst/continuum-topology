import { Search } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { buildSearchIndex, KIND_LABEL, searchItems } from '@/lib/search'
import { useTopology } from '@/store/topology'

/** Cmd-K / Ctrl-K: jump to any cluster, node, service, device, site, application, page or saved view. */
export default function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  return open ? <Dialog onClose={onClose} /> : null
}

function Dialog({ onClose }: { onClose: () => void }) {
  const { clusters, nodes, services, devices, applications, sites, externalEndpoints, savedViews } = useTopology()
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [cursor, setCursor] = useState(0)
  const list = useRef<HTMLUListElement>(null)
  const index = useMemo(
    () => buildSearchIndex({ clusters, nodes, services, devices, applications, sites, externalEndpoints, savedViews }),
    [clusters, nodes, services, devices, applications, sites, externalEndpoints, savedViews],
  )
  const results = useMemo(() => searchItems(index, q), [index, q])
  const at = Math.min(cursor, Math.max(0, results.length - 1))

  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [at, results])

  const go = (i: number) => {
    const r = results[i]
    if (!r) return
    onClose()
    navigate(r.to)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 p-4 pt-[12vh] backdrop-blur-[2px]" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search"
        className="w-full max-w-xl overflow-hidden rounded-xl border border-nb-850 bg-nb-920 shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3 border-b border-nb-850 px-4">
          <Search size={16} className="shrink-0 text-nb-500" aria-hidden />
          <input
            autoFocus
            role="combobox"
            aria-expanded
            aria-controls="palette-results"
            aria-activedescendant={results.length ? `palette-opt-${at}` : undefined}
            aria-label="Search clusters, services, sites and more"
            placeholder="Search clusters, services, nodes, sites, pages…"
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setCursor(0)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Escape') onClose()
              else if (e.key === 'ArrowDown') {
                e.preventDefault()
                setCursor(Math.min(results.length - 1, at + 1))
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                setCursor(Math.max(0, at - 1))
              } else if (e.key === 'Enter') {
                e.preventDefault()
                go(at)
              }
            }}
            className="h-12 w-full bg-transparent text-sm text-white placeholder:text-nb-500 focus:outline-none"
          />
          <kbd className="rounded border border-nb-800 px-1.5 py-0.5 text-[11px] text-nb-500">Esc</kbd>
        </div>
        <ul id="palette-results" ref={list} role="listbox" className="max-h-[50vh] overflow-y-auto p-1">
          {results.map((r, i) => (
            <li
              key={r.id}
              id={`palette-opt-${i}`}
              role="option"
              aria-selected={i === at}
              data-kind={r.kind}
              onMouseMove={() => setCursor(i)}
              onClick={() => go(i)}
              className={`flex cursor-pointer items-center gap-3 rounded-md px-3 py-2 ${i === at ? 'bg-nb-940' : ''}`}
            >
              <span className="w-20 shrink-0 text-[11px] uppercase tracking-wide text-nb-500">{KIND_LABEL[r.kind]}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm text-nb-300">{r.title}</span>
                {r.subtitle && <span className="block truncate text-xs text-nb-500">{r.subtitle}</span>}
              </span>
            </li>
          ))}
          {results.length === 0 && <li className="px-3 py-8 text-center text-sm text-nb-500" role="presentation">Nothing matches “{q}”.</li>}
        </ul>
        <div className="flex items-center justify-between border-t border-nb-850 px-4 py-2 text-[11px] text-nb-500">
          <span>↑ ↓ to move · Enter to open</span>
          <span>{results.length} result{results.length === 1 ? '' : 's'}</span>
        </div>
      </div>
    </div>
  )
}

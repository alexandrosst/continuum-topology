import { Info } from 'lucide-react'
import { useState, type ReactNode } from 'react'

/** A titled block of the Inspector: a rule above it, a small upper-case heading, then its rows. */
export function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="border-t border-nb-850 px-5 py-4">
      <div className="mb-2 flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">
        {title}
        {hint && <span title={hint} className="cursor-help text-nb-600"><Info size={13} aria-label="What this means" /></span>}
      </div>
      {children}
    </div>
  )
}

export function LinkRow({ label, sub, onClick, stacked }: { label: string; sub?: string; onClick?: () => void; stacked?: boolean }) {
  // `stacked` puts a long sub-label (protocol, sources, rates) under the name instead of squeezing it.
  return (
    <button onClick={onClick} disabled={!onClick} className={'flex w-full rounded-md px-2 py-1.5 text-left text-sm text-nb-300 hover:bg-nb-940 disabled:hover:bg-transparent ' + (stacked ? 'flex-col gap-0.5' : 'items-center justify-between')}>
      <span className="w-full truncate">{label}</span>
      {sub && <span className={stacked ? 'w-full truncate text-xs text-nb-500' : 'ml-3 shrink-0 text-xs text-nb-500'}>{sub}</span>}
    </button>
  )
}

/** MOCKUP: the first few rows of a long list, the rest behind "Show all". */
export function ShortList({ items, limit = 5 }: { items: ReactNode[]; limit?: number }) {
  const [all, setAll] = useState(false)
  const shown = all ? items : items.slice(0, limit)
  return (
    <>
      {shown}
      {items.length > limit && (
        <button onClick={() => setAll(!all)} className="mt-1 px-2 text-xs text-accent hover:underline">{all ? 'Show fewer' : `Show all ${items.length}`}</button>
      )}
    </>
  )
}

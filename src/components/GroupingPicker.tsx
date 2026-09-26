import { ChevronDown } from 'lucide-react'
import { useState } from 'react'
import { originLabel } from '@/lib/present'
import type { GroupingAlternative } from '@/lib/types'

/**
 * A collapsible row of alternative grouping labels. Used both while a create-application suggestion is
 * still open (DiscoveryPage, before the application exists) and any time later from the application
 * itself (the application form, once it does), so regrouping always looks and behaves the same way -
 * one control, two call sites.
 */
export function GroupingPicker({ label, alternatives, onPick }: { label: string; alternatives: GroupingAlternative[]; onPick: (alt: GroupingAlternative) => void }) {
  const [open, setOpen] = useState(false)
  if (alternatives.length === 0) return null
  return (
    <div className="mt-1.5">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1 text-xs text-nb-500 hover:text-accent"
        aria-expanded={open}
      >
        <ChevronDown size={12} className={open ? 'rotate-180' : ''} aria-hidden />
        {label}
      </button>
      {open && (
        <div className="mt-1.5 flex flex-wrap gap-1.5" role="group" aria-label="Alternative labels">
          {alternatives.map((alt) => (
            <button
              key={`${alt.origin}-${alt.name}`}
              type="button"
              title={alt.signal}
              onClick={() => onPick(alt)}
              className="rounded-full border border-nb-800 bg-nb-930 px-2.5 py-1 text-xs text-nb-400 transition-colors hover:border-accent/60 hover:text-nb-300"
            >
              {originLabel(alt.origin)}: <span className="font-medium text-nb-200">{alt.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

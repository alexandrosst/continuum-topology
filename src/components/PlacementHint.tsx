import { Check, MapPin, X } from 'lucide-react'
import type { PlacementSuggestion } from '@/lib/places'
import { useTopology } from '@/store/topology'

const CONF: Record<PlacementSuggestion['confidence'], string> = {
  high: 'text-emerald-300',
  medium: 'text-amber-300',
  low: 'text-nb-400',
}

/**
 * "Looks like Frankfurt, Germany" with one click to accept. Shown where a cluster has no place yet. The reason
 * and how sure we are are in the tooltip: the suggestion is worked out from tables, a person makes the call.
 */
export default function PlacementHint({ suggestion, compact }: { suggestion?: PlacementSuggestion; compact?: boolean }) {
  const decide = useTopology((s) => s.decideDerived)
  if (!suggestion) return null
  return (
    <div className={compact ? 'mt-1' : 'mt-2 rounded-lg border border-nb-850 bg-nb-930 px-3 py-2'} data-testid="placement-hint">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs" title={suggestion.detail}>
        <MapPin size={12} className="shrink-0 text-accent" aria-hidden />
        <span className="text-nb-300">
          Looks like <span className="font-medium text-white">{suggestion.place}</span>
        </span>
        <span className={CONF[suggestion.confidence]}>{suggestion.confidence}</span>
        <button
          type="button"
          className="inline-flex items-center gap-1 rounded border border-accent/40 px-1.5 py-0.5 text-accent hover:bg-accent/10"
          onClick={() => decide(suggestion, 'accepted')}
          aria-label={`Put ${suggestion.place} on the map`}
        >
          <Check size={11} /> Use
        </button>
        <button
          type="button"
          className="inline-flex items-center rounded p-0.5 text-nb-500 hover:text-white"
          onClick={() => decide(suggestion, 'dismissed')}
          aria-label={`Dismiss the suggestion ${suggestion.place}`}
          title="Not there"
        >
          <X size={12} />
        </button>
      </div>
      {!compact && <p className="mt-1 text-xs text-nb-500">{suggestion.detail}</p>}
    </div>
  )
}

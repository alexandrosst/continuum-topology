import { Check, MapPin, X } from 'lucide-react'
import { Flag } from '@/components/ui/brand'
import type { PlacementSuggestion } from '@/lib/places'
import type { Confidence } from '@/lib/types'
import { IP_SCOPE_HELP, ipScope, type IpScope } from '@/lib/present'
import { useTopology } from '@/store/topology'

const CONF: Record<Confidence, string> = {
  high: 'text-ok',
  medium: 'text-warn',
  low: 'text-nb-400',
}

const DOT: Record<Confidence, string> = {
  high: 'bg-ok',
  medium: 'bg-warn',
  low: 'bg-nb-500',
}

/** Every signal on its own line, for a plain-text (native `title`) tooltip. */
function evidenceTitle(suggestion: PlacementSuggestion): string {
  return suggestion.evidence.map((e) => `${e.signal} (${e.confidence}): ${e.detail ?? ''}`).join('\n')
}

// Address ranges that can never resolve to a place, whatever database is configured: telling a person
// this (rather than showing nothing) is the difference between "not placed yet" and "can't be placed
// this way at all, add a site by hand".
const NEVER_LOCATABLE = new Set<IpScope>(['private', 'shared', 'loopback', 'link-local'])
const SCOPE_LABEL: Partial<Record<IpScope, string>> = { shared: 'carrier-grade NAT (CGNAT)', private: 'private', loopback: 'loopback', 'link-local': 'link-local' }

/**
 * "Looks like Frankfurt, Germany" with one click to accept. Shown where a cluster has no place yet. The reason
 * and how sure we are are in the tooltip: the suggestion is worked out from tables, a person makes the call.
 *
 * When there is no suggestion at all, `egressIp` lets it say why rather than showing nothing: a private,
 * CGNAT, loopback or link-local address can never be geolocated by any database, so a person should stop
 * waiting for a suggestion and add the site by hand instead. A public address with no result is left quiet -
 * that just means the lookup found nothing, which is already explained on the Settings page.
 */
export default function PlacementHint({ suggestion, compact, egressIp }: { suggestion?: PlacementSuggestion; compact?: boolean; egressIp?: string }) {
  const decide = useTopology((s) => s.decideDerived)
  if (!suggestion) {
    const scope = egressIp ? ipScope(egressIp) : 'unknown'
    if (!NEVER_LOCATABLE.has(scope)) return null
    const label = SCOPE_LABEL[scope]
    if (compact) {
      return (
        <p className="mt-0.5 text-xs text-nb-600" title={`${egressIp} — ${IP_SCOPE_HELP[scope]}`} data-testid="placement-hint-none">
          No location: {label} address
        </p>
      )
    }
    return (
      <p className="mt-2 flex items-start gap-1.5 text-xs text-nb-500" data-testid="placement-hint-none">
        <MapPin size={12} className="mt-0.5 shrink-0 text-nb-600" aria-hidden />
        <span>
          Reaches the server from a {label} address ({egressIp}) — IP geolocation can never place one of those, whatever database is configured. Add a site by hand if you know where it is.
        </span>
      </p>
    )
  }
  return (
    <div className={compact ? 'mt-1' : 'mt-2 rounded-lg border border-nb-850 bg-nb-930 px-3 py-2'} data-testid="placement-hint">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs" title={compact ? evidenceTitle(suggestion) : undefined}>
        <MapPin size={12} className="shrink-0 text-accent" aria-hidden />
        <span className="inline-flex items-center gap-1.5 text-nb-300">
          Looks like <Flag code={suggestion.country} /> <span className="font-medium text-nb-300">{suggestion.place}</span>
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
          className="inline-flex items-center rounded p-0.5 text-nb-500 hover:text-nb-300"
          onClick={() => decide(suggestion, 'dismissed')}
          aria-label={`Dismiss the suggestion ${suggestion.place}`}
          title="Not there"
        >
          <X size={12} />
        </button>
      </div>
      {/* Why this confidence: every signal that went into it, not just the flattened sentence - an
          agreement between two signals (see places.ts's merge) is otherwise invisible to a person
          deciding whether to trust it. */}
      {!compact && (
        <ul className="mt-1.5 space-y-1 text-xs text-nb-500" data-testid="placement-hint-evidence">
          {suggestion.evidence.map((e, i) => (
            <li key={i} className="flex items-start gap-1.5">
              <span className={`mt-1 inline-block size-1.5 shrink-0 rounded-full ${DOT[e.confidence]}`} aria-hidden />
              <span>
                {e.detail ?? e.signal} <span className={CONF[e.confidence]}>({e.confidence})</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

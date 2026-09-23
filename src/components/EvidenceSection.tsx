import { useState } from 'react'
import { EvidenceChip, ObservationChip } from '@/components/ui/primitives'
import { ageLabel, evidenceRows, observation, weakAttributes, type ModelEntity } from '@/lib/provenance'
import type { Provenance } from '@/lib/types'
import { useEntityEvidence } from '@/store/effectiveModel'
import { useServer } from '@/store/server'

const SHOWN = 8

const stateInfo = (e: ModelEntity) =>
  e.origin === 'declared'
    ? undefined
    : observation({ source: 'discovered', state: e.state === 'gone' || e.state === 'declared' ? undefined : e.state, stateReason: e.stateReason, deletedAt: e.state === 'gone' ? e.goneAt : undefined })

/**
 * Where each fact about a record came from: the source (which agent, a node probe, a measurement, a rule the server
 * applied, or a person), how sure that source is, the unit, and when it was last confirmed. It comes from the
 * server's effective model, so what is shown here is what a placement or a decider would be told.
 */
export function EvidenceSection({ kind, id }: { kind: string; id: string }) {
  const entity = useEntityEvidence(kind, id)
  const agents = useServer((s) => s.state?.agents)
  const [all, setAll] = useState(false)
  if (!entity) return null
  const name = (agentId: string) => agents?.find((a) => a.id === agentId)?.name
  const rows = evidenceRows(entity, name)
  const shown = all ? rows : rows.slice(0, SHOWN)
  const info = stateInfo(entity)
  return (
    <div className="border-t border-nb-850 px-5 py-4" data-testid="evidence-section">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-nb-500">Evidence</span>
        <ObservationChip info={info} />
      </div>
      {entity.origin === 'observed+declared' && <p className="mb-2 text-xs text-nb-500">A person has changed some values; the declared value wins, and the observed one is shown beneath it.</p>}
      {entity.identity && (
        <p className="mb-2 text-xs text-nb-500" data-testid="evidence-identity">
          Identified by {entity.identity.basis.replaceAll('-', ' ')}
          {entity.identity.aliases && entity.identity.aliases.length > 0 ? `; was called ${entity.identity.aliases.join(', ')}` : ''}.
          {entity.identity.note ? ` ${entity.identity.note}.` : ''}
        </p>
      )}
      <ul className="space-y-2" data-testid="evidence-rows">
        {shown.map((r) => (
          <li key={r.attribute} className="text-sm" data-attribute={r.attribute}>
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-nb-500">{r.attribute}</span>
              <span className="flex min-w-0 items-baseline gap-1.5 text-right text-nb-300">
                <span className="scrollbar-none min-w-0 overflow-x-auto whitespace-nowrap" title={r.value}>{r.value}</span>
                <EvidenceChip level={r.confidence} always why={r.why} />
              </span>
            </div>
            <div className="text-[11px] leading-4 text-nb-500">
              {r.source}
              {r.observedAt ? ` · confirmed ${ageLabel(r.observedAt)} ago` : ''}
              {r.unit ? ` · unit ${r.unit}` : ''}
              {r.confidence === 'unknown' && r.why ? ` · ${r.why}` : r.why && r.confidence !== 'reported' ? ` · ${r.why}` : ''}
              {r.shadowed ? ` · observed: ${r.shadowed}` : ''}
            </div>
          </li>
        ))}
      </ul>
      {rows.length > SHOWN && (
        <button onClick={() => setAll(!all)} className="mt-2 text-xs text-accent hover:underline">
          {all ? 'Show fewer' : `Show all ${rows.length}`}
        </button>
      )}
    </div>
  )
}

/** The values of a discovered record that are guessed or not known, each with its evidence chip. Nothing when there are none. */
export function WeakValues({ kind, rec }: { kind: 'cluster' | 'node' | 'service'; rec: Provenance }) {
  const weak = weakAttributes(kind, rec as Provenance & Record<string, unknown>)
  if (weak.length === 0) return null
  return (
    <div className="py-1.5 text-sm" data-testid="weak-values">
      <div className="mb-1 text-nb-500">Guessed or not known</div>
      <ul className="space-y-1">
        {weak.map((w) => (
          <li key={w.field} className="flex items-baseline justify-between gap-3" title={w.why}>
            <span className="text-nb-400">{w.label}</span>
            <EvidenceChip level={w.level} always why={w.why} />
          </li>
        ))}
      </ul>
    </div>
  )
}

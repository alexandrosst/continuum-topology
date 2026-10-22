import { useState } from 'react'
import { EvidenceChip, ObservationChip } from '@/components/ui/primitives'
import { ageLabel, evidenceRows, observation, weakAttributes, type ModelEntity } from '@/lib/provenance'
import type { Provenance } from '@/lib/types'
import { useEntityEvidence } from '@/store/effectiveModel'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

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

/**
 * The values of a discovered record that are guessed or not known, at the top of the sidebar where a person
 * will see them, each with the signal it rests on. Nothing when there are none (the section is not drawn).
 * A "guess" carries a real value discovery is unsure of, so it can be kept as it is in one click (see
 * `confirmField`); an "unknown" has no value to freeze and genuinely needs the edit form instead.
 */
export function NeedsLook({ kind, rec }: { kind: 'cluster' | 'node' | 'service'; rec: Provenance & { id: string } }) {
  const weak = weakAttributes(kind, rec as unknown as Provenance & Record<string, unknown>)
  const confirmField = useTopology((s) => s.confirmField)
  const by = useServer((s) => s.user?.username)
  if (weak.length === 0) return null
  return (
    <div className="border-t border-nb-850 bg-warn/5 px-5 py-4" data-testid="needs-look">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">Needs a look ({weak.length})</div>
      <ul className="space-y-2.5">
        {weak.map((w) => (
          <li key={w.field} className="text-sm">
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-nb-400">{w.label}</span>
              <EvidenceChip level={w.level} always why={w.why} />
            </div>
            <div className="mt-0.5 flex items-baseline justify-between gap-3 text-[11px] leading-4 text-nb-500">
              <span>{w.why}</span>
              {w.level === 'guess' && by && (
                <button
                  onClick={() => confirmField(kind, rec.id, w.field, by)}
                  className="shrink-0 text-accent hover:underline"
                  title="Keep this value as it is; it will not be overwritten by rediscovery."
                >
                  Keep as is
                </button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * One quiet line saying where a record's facts come from, with the full list (the Evidence rows above) opening
 * only on request. What needs attention is NeedsLook's job at the top of the sidebar; repeating every confirmed
 * value here as well was noise.
 */
export function SourcesFooter({ kind, id }: { kind: string; id: string }) {
  const entity = useEntityEvidence(kind, id)
  const agents = useServer((s) => s.state?.agents)
  const [open, setOpen] = useState(false)
  if (!entity) return null
  const rows = evidenceRows(entity, (a) => agents?.find((x) => x.id === a)?.name)
  if (rows.length === 0) return null
  const count = (levels: string[]) => rows.filter((r) => levels.includes(r.confidence)).length
  const parts = [
    [count(['reported', 'measured']), 'reported or measured'],
    [count(['inferred']), 'inferred'],
    [count(['guess', 'unknown']), 'to check'],
  ]
    .filter(([n]) => n)
    .map(([n, label]) => `${n} ${label}`)
  return (
    <div className="border-t border-nb-850 px-5 py-3" data-testid="sources-footer">
      <div className="flex items-center justify-between gap-3 text-xs text-nb-500">
        <span>{rows.length} {rows.length === 1 ? 'fact' : 'facts'} · {parts.join(' · ')}</span>
        <button onClick={() => setOpen(!open)} aria-expanded={open} className="shrink-0 text-accent hover:underline">
          {open ? 'Hide sources' : 'Show sources'}
        </button>
      </div>
      {open && <div className="-mx-5 mt-2"><EvidenceSection kind={kind} id={id} /></div>}
    </div>
  )
}

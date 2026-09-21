import clsx from 'clsx'
import { Plus, Trash2, TriangleAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Button, Select } from '@/components/ui/primitives'
import { bytesPerSec } from '@/lib/observed'
import { evacuate, isMovableKind, whatIf, type Evacuation } from '@/lib/placement/engine'
import type { Move, Policy } from '@/lib/placement/types'
import { clusterOfService, clusterStatus, unverifiableClusters, type World } from '@/lib/placement/world'
import { Card, Confidence, pts } from './shared'
import { FitBadge, Hedge, Why } from './Why'

const Delta = ({ before, after, unit, fmt }: { before: number; after: number; unit?: string; fmt: (n: number) => string }) => {
  const d = after - before
  return (
    <div>
      <div className="text-2xl font-medium tabular-nums text-white">{fmt(after)}<span className="ml-1 text-sm text-nb-500">{unit}</span></div>
      <div className="text-xs tabular-nums text-nb-500">
        was {fmt(before)} ·{' '}
        <span className={clsx(d < 0 ? 'text-emerald-300' : d > 0 ? 'text-amber-300' : 'text-nb-500')}>{d === 0 ? 'no change' : `${d < 0 ? '' : '+'}${fmt(d)}`}</span>
      </div>
    </div>
  )
}

/**
 * Build a scenario, see what it does. Moves are judged in order, so a later move sees the capacity and the
 * neighbours the earlier ones left. Nothing is applied: this is arithmetic on the picture Continuum already has.
 */
export default function WhatIf({ world, policy, initial }: { world: World; policy: Policy; initial?: Move[] }) {
  const [moves, setMoves] = useState<Move[]>(initial ?? [])
  const [service, setService] = useState('')
  const [target, setTarget] = useState('')
  const [drain, setDrain] = useState('')
  const [evac, setEvac] = useState<Evacuation | null>(null)
  useEffect(() => {
    if (initial && initial.length) setMoves(initial)
  }, [initial])

  const movable = useMemo(() => world.services.filter(isMovableKind).sort((a, b) => a.name.localeCompare(b.name)), [world])
  const result = useMemo(() => (moves.length ? whatIf(world, policy, moves) : null), [world, policy, moves])
  const name = (id: string) => world.byCluster.get(id)?.name ?? id
  const svcName = (id: string) => world.byService.get(id)?.name ?? id
  const add = () => {
    if (!service || !target) return
    setMoves((m) => [...m.filter((x) => x.serviceId !== service), { serviceId: service, to: target }])
    setEvac(null)
    setService('')
  }
  const runEvac = () => {
    if (!drain) return
    const e = evacuate(world, policy, drain)
    setEvac(e)
    setMoves(e.moves)
  }
  const currentOf = (id: string) => clusterOfService(world, id)
  const unsure = useMemo(() => new Set(unverifiableClusters(world).map((u) => u.cluster.id)), [world])

  return (
    <div className="space-y-4" data-testid="whatif">
      <Card title="Build a scenario">
        <div className="grid gap-3 md:grid-cols-[1fr_1fr_auto] md:items-end">
          <label className="block">
            <span className="mb-1.5 block text-sm text-nb-300">Move this service</span>
            <Select value={service} onChange={(e) => setService(e.target.value)} aria-label="Service to move" placeholder="Choose a service">
              {movable.map((s) => (
                <option key={s.id} value={s.id}>{s.name} · {name(currentOf(s.id) ?? s.clusterId)}</option>
              ))}
            </Select>
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm text-nb-300">to this cluster</span>
            <Select value={target} onChange={(e) => setTarget(e.target.value)} aria-label="Target cluster" placeholder="Choose a cluster">
              {world.clusters.filter((c) => c.id !== currentOf(service)).map((c) => {
                const st = clusterStatus(world, c.id)
                return <option key={c.id} value={c.id} disabled={!st.eligible}>{c.name}{st.eligible ? (unsure.has(c.id) ? ' (room not known: can’t tell)' : '') : ` (excluded: ${st.reason})`}</option>
              })}
            </Select>
          </label>
          <Button onClick={add} disabled={!service || !target}><Plus size={15} /> Add the move</Button>
        </div>
        <div className="mt-4 flex flex-wrap items-end gap-3 border-t border-nb-850 pt-4">
          <label className="block min-w-56">
            <span className="mb-1.5 block text-sm text-nb-300">Or: what if a whole cluster went away?</span>
            <Select value={drain} onChange={(e) => setDrain(e.target.value)} aria-label="Cluster that goes away" placeholder="Choose a cluster">
              {world.clusters.map((c) => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </Select>
          </label>
          <Button onClick={runEvac} disabled={!drain} data-testid="evacuate">Where would its services go?</Button>
          {moves.length > 0 && (
            <Button variant="ghost" onClick={() => { setMoves([]); setEvac(null) }}>
              <Trash2 size={14} /> Clear the scenario
            </Button>
          )}
        </div>
      </Card>

      {!result && <p className="rounded-xl border border-dashed border-nb-850 px-5 py-8 text-center text-sm text-nb-500">Add a move, or ask what would happen if a cluster went away. Nothing is changed anywhere; this only calculates.</p>}

      {result && (
        <>
          <Card title="What it would do">
            <div className="grid gap-6 sm:grid-cols-3">
              <div>
                <div className="mb-1 text-xs uppercase tracking-wide text-nb-500">Network cost</div>
                <Delta before={result.before.cost} after={result.after.cost} fmt={pts} unit="points" />
              </div>
              <div>
                <div className="mb-1 text-xs uppercase tracking-wide text-nb-500">Traffic between sites</div>
                <Delta before={result.before.crossSiteBps} after={result.after.crossSiteBps} fmt={bytesPerSec} />
              </div>
              <div>
                <div className="mb-1 text-xs uppercase tracking-wide text-nb-500">Moves</div>
                <div className="text-2xl font-medium tabular-nums text-white">{result.moves.length}</div>
                <div className="text-xs text-nb-500" data-testid="move-counts">
                  {result.moves.filter((m) => m.verdict === 'doesNotFit').length} blocked by hard limits · {result.moves.filter((m) => m.verdict === 'cantTell').length} can’t be told
                </div>
              </div>
            </div>
            {result.warnings.length > 0 && (
              <ul className="mt-4 space-y-1 rounded-lg border border-amber-400/20 bg-amber-400/5 px-3 py-2 text-xs text-amber-200/90" role="alert">
                {result.warnings.map((w) => (
                  <li key={w} className="flex gap-2"><TriangleAlert size={13} className="mt-0.5 shrink-0" aria-hidden />{w}</li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Move by move">
            <ul className="divide-y divide-nb-850/70">
              {result.moves.map((m) => (
                <li key={m.serviceId} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-sm" data-testid="whatif-move" data-fits={m.fits} data-verdict={m.verdict} data-confidence={m.confidence}>
                  <FitBadge verdict={m.verdict} />
                  <span className="font-medium text-white">{svcName(m.serviceId)}</span>
                  <span className="text-nb-400">{name(m.from)} → {name(m.to)}</span>
                  <Confidence level={m.confidence} />
                  <span className={clsx('ml-auto tabular-nums', m.benefit > 0 ? 'text-emerald-300' : m.benefit < 0 ? 'text-amber-300' : 'text-nb-500')}>
                    {m.benefit > 0 ? 'saves' : m.benefit < 0 ? 'costs' : 'no change'} {m.benefit !== 0 && `${pts(Math.abs(m.benefit))} points`}
                  </span>
                  <button onClick={() => setMoves((x) => x.filter((y) => y.serviceId !== m.serviceId))} aria-label={`Remove ${svcName(m.serviceId)} from the scenario`} className="rounded p-1 text-nb-500 hover:bg-nb-940 hover:text-nb-300">
                    <Trash2 size={13} />
                  </button>
                  {m.verdict === 'doesNotFit' && <div className="basis-full text-xs text-red-300/90">{m.blockers.join('; ')}</div>}
                  {m.verdict === 'cantTell' && <div className="basis-full text-xs text-amber-200/90" data-testid="cant-tell-why">Can’t tell: {m.unchecked.join('; ')}</div>}
                  {m.verdict === 'fits' && m.unchecked.length > 0 && <div className="basis-full text-xs text-nb-500">Not checked: {m.unchecked.join('; ')}</div>}
                  {m.sensitivity.length > 0 && (
                    <ul className="basis-full space-y-0.5 text-xs text-nb-400" data-testid="sensitivity">
                      {m.sensitivity.map((t) => (
                        <li key={t}>{t}</li>
                      ))}
                    </ul>
                  )}
                  {(m.confidence === 'low' || m.confidence === 'none') && <div className="basis-full"><Hedge level={m.confidence} inputs={m.after.inputs} verdict={m.verdict} /></div>}
                  <div className="basis-full"><Why facts={m.facts} wouldChange={m.wouldChange.map((c) => c.text)} fixes={m.fixes} now={world.cap.now} /></div>
                </li>
              ))}
            </ul>
          </Card>

          {result.loads.some((l) => l.before !== undefined) && (
            <Card title="How full the clusters get (CPU requested)">
              <ul className="space-y-3">
                {result.loads.filter((l) => l.before !== undefined || l.after !== undefined).map((l) => {
                  const b = Math.round((l.before ?? 0) * 100)
                  const a = Math.round((l.after ?? 0) * 100)
                  return (
                    <li key={l.clusterId} className="text-sm">
                      <div className="flex justify-between text-nb-300"><span>{l.name}</span><span className="tabular-nums text-nb-400">{b}% → <span className={clsx(a > 100 ? 'text-red-300' : a > 90 ? 'text-amber-300' : 'text-white')}>{a}%</span></span></div>
                      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-nb-850">
                        <div className={clsx('h-full rounded-full', a > 100 ? 'bg-red-400' : a > 90 ? 'bg-amber-400' : 'bg-emerald-400')} style={{ width: `${Math.min(100, a)}%` }} />
                      </div>
                    </li>
                  )
                })}
              </ul>
            </Card>
          )}
        </>
      )}

      {evac && evac.lost.length > 0 && (
        <Card title={`Would stop: ${evac.lost.length} service${evac.lost.length === 1 ? '' : 's'} with nowhere to go`}>
          <ul className="space-y-1.5 text-sm">
            {evac.lost.map((l) => (
              <li key={l.serviceId} data-testid="evac-lost"><span className="font-medium text-white">{l.serviceName}</span> <span className="text-nb-400">— {l.why}</span></li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  )
}

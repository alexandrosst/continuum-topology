import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { Confidence } from '@/components/placement/shared'
import { FitBadge, Hedge, Why } from '@/components/placement/Why'
import { ObservationChip, TierBadge } from '@/components/ui/primitives'
import { capCtxOf } from '@/lib/capacity'
import { useJudgedAt } from '@/lib/placement/usePlacement'
import { observation, TONE_CLASS } from '@/lib/provenance'
import { movability, moveTargets, VERDICT_HELP, VERDICT_ICON, VERDICT_LABEL, VERDICT_TONE, type MoveModel, type MoveTarget, type ReasonSeverity } from '@/lib/movability'
import { sensitivityText, type Verdict } from '@/lib/advice'
import type { Service } from '@/lib/types'
import { useEffectiveModel } from '@/store/effectiveModel'
import { useTopology } from '@/store/topology'

const DOT: Record<ReasonSeverity, string> = { blocker: 'bg-red-400', caution: 'bg-amber-400', info: 'bg-nb-500' }

/** The model as movability() wants it, plus which clusters actually report their volumes. */
export function useMoveModel(): { model: MoveModel; forService: (w: Service) => MoveModel } {
  const { clusters, nodes, services, devices, dependencies, sites, agents } = useTopology()
  const effective = useEffectiveModel()
  const asOf = useJudgedAt()
  return useMemo(() => {
    // facts are aged on the server's clock, with the server's staleness window
    const model: MoveModel = { clusters, nodes, services, devices, dependencies, sites, agents, cap: capCtxOf(effective, asOf) }
    const storageKnown = (clusterId: string) => {
      const a = agents.find((x) => x.clusterId === clusterId)
      if (!a || a.modules.length === 0) return true // typed by hand, or a sample: what is written is what there is
      return a.modules.some((m) => m.name === 'storage' && m.status === 'ok')
    }
    return { model, forService: (w) => ({ ...model, storageKnown: storageKnown(w.clusterId) }) }
  }, [clusters, nodes, services, devices, dependencies, sites, agents, effective, asOf])
}

/** A small pill for tables: Free to move / Move with care / Pinned. The first reason is its tooltip. The caller passes
 * `forService` from one useMoveModel() so a long table does not rebuild the whole model per row. */
export function MobilityChip({ service, forService }: { service: Service; forService: (w: Service) => MoveModel }) {
  const m = useMemo(() => movability(service, forService(service)), [service, forService])
  const Icon = VERDICT_ICON[m.verdict]
  const why = m.reasons.find((r) => r.severity !== 'info')?.text ?? VERDICT_HELP[m.verdict]
  return (
    <span
      className={`inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-0.5 text-xs ${TONE_CLASS[VERDICT_TONE[m.verdict]]}`}
      title={why}
      data-testid="mobility-chip"
      data-verdict={m.verdict}
    >
      <Icon size={12} aria-hidden />
      {VERDICT_LABEL[m.verdict]}
    </span>
  )
}

/** Why a service can or cannot move, and which other clusters could take it. */
export default function MobilityPanel({ service, onSelectCluster }: { service: Service; onSelectCluster?: (id: string) => void }) {
  const { forService } = useMoveModel()
  const model = useMemo(() => forService(service), [forService, service])
  const m = useMemo(() => movability(service, model), [service, model])
  const targets = useMemo(() => moveTargets(service, model), [service, model])
  const Icon = VERDICT_ICON[m.verdict]
  const groups: { verdict: Verdict; title: string; rows: MoveTarget[] }[] = [
    { verdict: 'fits', title: 'Fit', rows: targets.filter((t) => t.verdict === 'fits') },
    { verdict: 'cantTell', title: 'Can’t tell', rows: targets.filter((t) => t.verdict === 'cantTell') },
    { verdict: 'doesNotFit', title: 'Do not fit', rows: targets.filter((t) => t.verdict === 'doesNotFit') },
  ]
  const now = model.cap?.now ?? 0
  return (
    <div data-testid="mobility-panel" data-verdict={m.verdict}>
      <div className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-sm ${TONE_CLASS[VERDICT_TONE[m.verdict]]}`}>
        <Icon size={15} className="mt-0.5 shrink-0" aria-hidden />
        <div>
          <div className="font-medium">{VERDICT_LABEL[m.verdict]}</div>
          <div className="text-xs opacity-80">{VERDICT_HELP[m.verdict]}</div>
        </div>
      </div>
      {m.reasons.length > 0 && (
        <ul className="mt-2 space-y-1.5">
          {m.reasons.map((r) => (
            <li key={r.code + r.text} className="flex gap-2 text-xs text-nb-400">
              <span className={`mt-1.5 size-1.5 shrink-0 rounded-full ${DOT[r.severity]}`} aria-label={r.severity} />
              <span>{r.text}</span>
            </li>
          ))}
        </ul>
      )}
      <div className="mb-1 mt-3 text-xs font-medium uppercase tracking-wide text-nb-500">
        Clusters that could host it{' '}
        {targets.length > 0 && (
          <span className="normal-case text-nb-600" data-testid="target-counts">
            ({groups[0].rows.length} fit, {groups[1].rows.length} can’t tell, {groups[2].rows.length} do not, of {targets.length})
          </span>
        )}
      </div>
      {m.verdict === 'pinned' && targets.length > 0 && <p className="mb-1.5 text-xs text-nb-500">Only once what pins it is dealt with. This checks policy, labels and capacity, not the data.</p>}
      {targets.length === 0 && <p className="text-xs text-nb-500">There is no other cluster.</p>}
      {groups.map(
        (g) =>
          g.rows.length > 0 && (
            <section key={g.verdict} className="mt-2" aria-label={g.title} data-testid={`targets-${g.verdict}`}>
              <h4 className="mb-1 text-[11px] font-medium text-nb-400">{g.title} ({g.rows.length})</h4>
              <ul className="space-y-2.5">
                {g.rows.map((t) => (
                  <TargetRow key={t.cluster.id} t={t} now={now} onSelectCluster={onSelectCluster} />
                ))}
              </ul>
            </section>
          ),
      )}
      <p className="mt-3 text-[11px] leading-4 text-nb-600">Worked out from what was discovered and the policy you set. It does not move anything.</p>
    </div>
  )
}

function TargetRow({ t, now, onSelectCluster }: { t: MoveTarget; now: number; onSelectCluster?: (id: string) => void }) {
  const dim = t.fits ? 'text-nb-300' : t.verdict === 'cantTell' ? 'text-amber-200' : 'text-nb-500'
  const hedged = t.confidence === 'low' || t.confidence === 'none'
  return (
    <li className="text-xs" data-testid="move-target" data-fits={t.fits} data-verdict={t.verdict} data-confidence={t.confidence}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <FitBadge verdict={t.verdict} />
        {onSelectCluster ? (
          <button className={`${dim} hover:text-white hover:underline`} onClick={() => onSelectCluster(t.cluster.id)}>{t.cluster.name}</button>
        ) : (
          <span className={dim}>{t.cluster.name}</span>
        )}
        <TierBadge tier={t.cluster.tier} />
        <ObservationChip info={observation(t.cluster)} />
        {t.excluded && <span className="rounded border border-red-400/30 bg-red-400/10 px-1.5 py-px text-[11px] text-red-300" data-testid="excluded-reason" title={t.excluded.reason}>excluded</span>}
        {t.verdict !== 'doesNotFit' && <Confidence level={t.confidence} />}
      </div>
      {t.blockers.length > 0 && <div className="mt-0.5 text-red-300/90" data-testid="blockers">{t.blockers.join('; ')}</div>}
      {t.unknown.length > 0 && (
        <div className="mt-0.5 text-amber-200/90" data-testid="cant-tell-why">
          {t.verdict === 'cantTell' ? 'Can’t tell: ' : 'Not checked: '}
          {t.unknown.join('; ')}
        </div>
      )}
      {t.verdict === 'cantTell' &&
        t.fixes.map((f) => (
          <div key={f.action + f.text} className="mt-0.5 text-nb-400">
            To find out:{' '}
            {f.link ? (
              <Link to={f.link} className="text-accent hover:underline" data-testid="fix-link">{f.text}</Link>
            ) : (
              f.text
            )}
          </div>
        ))}
      {hedged && t.verdict === 'fits' && <div className="mt-1"><Hedge level={t.confidence} inputs={t.advice.dims.map((d) => ({ class: d.class }))} verdict={t.verdict} /></div>}
      {t.advice.dims.some((d) => d.verdict !== 'doesNotFit' && sensitivityText(d)) && (
        <ul className="mt-0.5 space-y-0.5 text-nb-400" data-testid="sensitivity">
          {t.advice.dims.filter((d) => d.verdict !== 'doesNotFit').map((d) => sensitivityText(d)).filter(Boolean).map((x) => <li key={x}>{x}</li>)}
        </ul>
      )}
      <Why facts={t.facts} wouldChange={t.advice.changes.map((c) => c.text)} fixes={[]} now={now} />
    </li>
  )
}

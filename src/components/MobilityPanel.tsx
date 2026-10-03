import { Star } from 'lucide-react'
import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { Confidence, pts } from '@/components/placement/shared'
import { FitBadge, Hedge, Why } from '@/components/placement/Why'
import { ICON_SM, ObservationChip, TierBadge } from '@/components/ui/primitives'
import { capCtxOf } from '@/lib/capacity'
import { isMovableKind } from '@/lib/placement/engine'
import { useJudgedAt, usePlan } from '@/lib/placement/usePlacement'
import { ageLabel, observation, TONE_CLASS } from '@/lib/provenance'
import { movability, moveTargets, VERDICT_HELP, VERDICT_ICON, VERDICT_LABEL, VERDICT_TONE, type MoveModel, type MoveTarget, type ReasonSeverity } from '@/lib/movability'
import { sensitivityText, type Verdict } from '@/lib/advice'
import type { Recommendation } from '@/lib/placement/types'
import type { Service } from '@/lib/types'
import { useEffectiveModel } from '@/store/effectiveModel'
import { useTopology } from '@/store/topology'

const DOT: Record<ReasonSeverity, string> = { blocker: 'bg-bad', caution: 'bg-warn', info: 'bg-nb-500' }

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
      <Icon size={ICON_SM} aria-hidden />
      {VERDICT_LABEL[m.verdict]}
    </span>
  )
}

/**
 * Why a service can or cannot move, which other clusters could take it, and - folded in rather than shown as
 * its own section (see the Inspector, where this used to sit above a separate ServiceAdvice) - where the last
 * optimization pass thinks it should actually go. The two answer different questions on different clocks
 * (this is an instant, client-side feasibility sweep across every cluster; the plan's pick is one
 * globally-cost-optimized recommendation that is only as fresh as the last run), so they stay two separate
 * computations; only their *presentation* is merged, as a highlight on the one fit-list row they agree on
 * plus one bridge sentence, rather than two near-identical badge rows repeating the same clusters.
 */
export default function MobilityPanel({ service, onSelectCluster }: { service: Service; onSelectCluster?: (id: string) => void }) {
  const { forService } = useMoveModel()
  const model = useMemo(() => forService(service), [forService, service])
  const m = useMemo(() => movability(service, model), [service, model])
  const targets = useMemo(() => moveTargets(service, model), [service, model])
  const { world, plan } = usePlan()
  const rec = plan.recommendations.find((r) => r.serviceId === service.id)
  const skipped = plan.skipped.find((s) => s.serviceId === service.id)
  const recTarget = rec ? targets.find((t) => t.cluster.id === rec.to) : undefined
  // The plan only ever recommends a cluster it itself certified as fitting; if it is not sitting in this
  // sweep's own "Fit" group too (a stale plan run, most likely, since the two run on different schedules),
  // the two computations disagree and the highlight has no row of this sweep's own to attach to.
  const recInFitList = !!recTarget && recTarget.verdict === 'fits'
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
        <Icon size={ICON_SM} className="mt-0.5 shrink-0" aria-hidden />
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
                  <TargetRow
                    key={t.cluster.id}
                    t={t}
                    now={now}
                    onSelectCluster={onSelectCluster}
                    recommended={recInFitList && g.verdict === 'fits' && t.cluster.id === rec!.to ? rec : undefined}
                    planNow={world.cap.now}
                  />
                ))}
              </ul>
            </section>
          ),
      )}
      <PlanBridge
        service={service}
        rec={rec}
        targetName={rec ? world.byCluster.get(rec.to)?.name ?? rec.to : undefined}
        recInFitList={recInFitList}
        skipped={skipped?.why}
        planNow={world.cap.now}
      />
      <p className="mt-3 text-[11px] leading-4 text-nb-600">Worked out from what was discovered and the policy you set. It does not move anything.</p>
    </div>
  )
}

/** The relationship between this instant feasibility sweep and the last global optimization pass: which of
 * the fits, if any, the plan actually picked, or - when it has nothing to recommend here at all - why. Shown
 * once, below the fit list, instead of as ServiceAdvice's own separate "Where should this run?" section. The
 * freshness note is deliberately its own line: the fit list above is live and client-side, this pick is only
 * as fresh as the plan's last run, and a reader should never have to guess which clock either one is on. */
function PlanBridge({ service, rec, targetName, recInFitList, skipped, planNow }: { service: Service; rec?: Recommendation; targetName?: string; recInFitList: boolean; skipped?: string; planNow: number }) {
  const ranAgo = ageLabel(new Date(planNow).toISOString())
  const freshness = <p className="mt-1 text-[11px] text-nb-600" data-testid="plan-freshness">Plan run {ranAgo} ago.</p>
  if (rec) {
    return (
      <div className="mt-3 text-xs text-nb-400" data-testid="plan-bridge">
        <p>
          {recInFitList ? (
            <>Of the clusters that fit, <span className="font-medium text-nb-300">{targetName}</span> is the current pick from the last optimization pass (saves {pts(rec.net)} points).</>
          ) : (
            <>The last optimization pass picked <span className="font-medium text-nb-300">{targetName}</span> (saves {pts(rec.net)} points), but it is not in this sweep’s own fit list right now - the two run on different schedules.</>
          )}
        </p>
        {freshness}
      </div>
    )
  }
  if (skipped) {
    return (
      <div className="mt-3 text-xs text-nb-400" data-testid="plan-bridge">
        <p>{skipped}</p>
        {freshness}
      </div>
    )
  }
  if (!isMovableKind(service)) {
    return (
      <div className="mt-3 text-xs text-nb-400" data-testid="plan-bridge">
        <p>A {service.kind} does not move.</p>
        {freshness}
      </div>
    )
  }
  return (
    <div className="mt-3 text-xs text-nb-400" data-testid="plan-bridge">
      <p>
        Nothing beats where it runs now, or nothing is known yet about what it talks to. <Link to="/placement" className="text-accent hover:underline">Placement</Link>
      </p>
      {freshness}
    </div>
  )
}

function TargetRow({ t, now, onSelectCluster, recommended, planNow }: { t: MoveTarget; now: number; onSelectCluster?: (id: string) => void; recommended?: Recommendation; planNow: number }) {
  const dim = t.fits ? 'text-nb-300' : t.verdict === 'cantTell' ? 'text-warn' : 'text-nb-500'
  const hedged = t.confidence === 'low' || t.confidence === 'none'
  return (
    <li className="text-xs" data-testid="move-target" data-fits={t.fits} data-verdict={t.verdict} data-confidence={t.confidence}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <FitBadge verdict={t.verdict} />
        {onSelectCluster ? (
          <button className={`${dim} hover:text-nb-300 hover:underline`} onClick={() => onSelectCluster(t.cluster.id)}>{t.cluster.name}</button>
        ) : (
          <span className={dim}>{t.cluster.name}</span>
        )}
        <TierBadge tier={t.cluster.tier} />
        <ObservationChip info={observation(t.cluster)} />
        {t.excluded && <span className="rounded border border-bad/30 bg-bad/10 px-1.5 py-px text-[11px] text-bad" data-testid="excluded-reason" title={t.excluded.reason}>excluded</span>}
        {t.verdict !== 'doesNotFit' && <Confidence level={t.confidence} />}
      </div>
      {t.blockers.length > 0 && <div className="mt-0.5 text-bad/90" data-testid="blockers">{t.blockers.join('; ')}</div>}
      {t.unknown.length > 0 && (
        <div className="mt-0.5 text-warn/90" data-testid="cant-tell-why">
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
      {recommended && <RecommendedHighlight rec={recommended} now={planNow} />}
    </li>
  )
}

/** The plan's own pick, as a small accent strip under the one fit-list row it agrees with - not a second
 * FitBadge/Confidence/Why trio repeating the same cluster a second time. */
function RecommendedHighlight({ rec, now }: { rec: Recommendation; now: number }) {
  return (
    <div className="mt-2 rounded-lg border border-accent/30 bg-accent-soft px-3 py-2" data-testid="recommended-highlight">
      <div className="flex flex-wrap items-center gap-2">
        <Star size={ICON_SM} className="shrink-0 text-accent" aria-hidden />
        <span className="font-medium text-accent">Recommended — {rec.reasons[0] ?? 'The combined effect of several small differences.'}</span>
        <span className="whitespace-nowrap rounded-md bg-accent/15 px-2 py-0.5 text-[11px] font-medium text-accent" title="Steady-state improvement in cost points after the one-off cost of copying data. See how points are weighed under Policy.">
          saves {pts(rec.net)} points
        </span>
      </div>
      {(rec.confidence === 'low' || rec.confidence === 'none') && (
        <div className="mt-1.5">
          <Hedge level={rec.confidence} inputs={rec.inputs} verdict={rec.fit} />
        </div>
      )}
      <div className="mt-1.5">
        <Link to={`/placement?tab=whatif&service=${encodeURIComponent(rec.serviceId)}&to=${encodeURIComponent(rec.to)}`} className="text-accent hover:underline">
          See the evidence
        </Link>
      </div>
      <Why facts={rec.facts} wouldChange={rec.wouldChange.map((c) => c.text)} fixes={rec.fixes} now={now} label="Why this pick" />
    </div>
  )
}

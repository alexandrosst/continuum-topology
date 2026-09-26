import clsx from 'clsx'
import { Info } from 'lucide-react'
import { useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import CantTell from '@/components/placement/CantTell'
import Excluded from '@/components/placement/Excluded'
import Deciders from '@/components/placement/Deciders'
import PolicyPanel from '@/components/placement/PolicyPanel'
import RecommendationCard from '@/components/placement/Recommendation'
import { Card, pts } from '@/components/placement/shared'
import WhatIf from '@/components/placement/WhatIf'
import { EmptyState, PageHeader, useFlash } from '@/components/ui/primitives'
import { coverage, totals, whatIf } from '@/lib/placement/engine'
import type { Move } from '@/lib/placement/types'
import { usePlan } from '@/lib/placement/usePlacement'
import { ageOf } from '@/lib/history'
import { useHistoryView } from '@/store/history'

const TABS = [
  { id: 'recommendations', label: 'Recommendations' },
  { id: 'whatif', label: 'What-if' },
  { id: 'deciders', label: 'Deciders' },
] as const
type Tab = (typeof TABS)[number]['id']

export default function PlacementPage() {
  const [params, setParams] = useSearchParams()
  const tab: Tab = TABS.some((t) => t.id === params.get('tab')) ? (params.get('tab') as Tab) : 'recommendations'
  const { world, plan, policy } = usePlan()
  // Signals "new advice" when the recommendation set itself changes (a real replan, not a data refresh
  // that happened to change nothing) - see usePlan/useWorld's stability fix, which is what makes this
  // array's identity actually mean something now.
  const recsChanged = useFlash(plan.recommendations)
  const shownAt = useHistoryView((s) => s.at)

  const initial: Move[] | undefined = useMemo(() => {
    const service = params.get('service')
    const to = params.get('to')
    return service && to && world.byService.has(service) ? [{ serviceId: service, to }] : undefined
  }, [params, world])

  const cov = useMemo(() => coverage(world), [world])
  const now = useMemo(() => totals(world, policy), [world, policy])
  const all = useMemo(
    () => (plan.recommendations.length ? whatIf(world, policy, plan.recommendations.map((r) => ({ serviceId: r.serviceId, to: r.to }))) : null),
    [world, policy, plan],
  )
  const empty = world.services.length === 0 || world.clusters.length < 2

  return (
    <>
      <PageHeader
        title="Placement"
        description="Where each service would be better off running, and what that would change. This is advice with its evidence: Continuum never moves anything."
      />

      {shownAt && (
        <p className="mb-4 flex items-start gap-2 rounded-lg border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-sm text-amber-200" role="status">
          <Info size={15} className="mt-0.5 shrink-0" aria-hidden />
          You are looking at the estate as it was {ageOf(shownAt)}. The advice below is computed for that moment, which is useful for asking “what would I have been told then?”, not for deciding today.
        </p>
      )}

      <div className="mb-5 flex flex-wrap gap-1 border-b border-nb-850" role="tablist" aria-label="Placement views">
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => {
              const next = new URLSearchParams(params)
              next.set('tab', t.id)
              if (t.id !== 'whatif') {
                next.delete('service')
                next.delete('to')
              }
              setParams(next, { replace: true })
            }}
            className={clsx('-mb-px border-b-2 px-4 py-2 text-sm transition-colors', tab === t.id ? 'border-accent text-white' : 'border-transparent text-nb-400 hover:text-white')}
            data-testid={`tab-${t.id}`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {empty ? (
        <EmptyState
          title="Placement needs somewhere to compare"
          description="Advice needs at least two clusters and some services. Connect clusters and let their agents report, then come back."
          action={<Link className="text-sm text-accent hover:underline" to="/discovery">Go to discovery</Link>}
        />
      ) : (
        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_20rem]">
          <div className="min-w-0 space-y-4">
            <Excluded world={world} />
            <CantTell world={world} />
            {tab === 'recommendations' && (
              <>
                <Coverage cov={cov} />
                {plan.recommendations.length === 0 ? (
                  <EmptyState
                    title="Nothing is clearly better placed"
                    description={
                      cov.edges === 0
                        ? 'No connections are known yet, so there is nothing to weigh. Turn on the flow observer on your agents, or declare dependencies, and this fills in.'
                        : 'Under this policy no move beats staying put by the margin you set. Lower the smallest worthwhile gain, or check what-if for a specific idea.'
                    }
                  />
                ) : (
                  <>
                    {all && (
                      <Card title={`If you did all ${plan.recommendations.length} of these`} aside={<span className="text-xs text-nb-500">in the order shown</span>}>
                        <div className="flex flex-wrap gap-x-10 gap-y-3" data-testid="plan-summary">
                          <Stat label="Network cost" value={`${pts(all.after.cost)}`} sub={`was ${pts(all.before.cost)}`} good={all.after.cost < all.before.cost} />
                          <Stat label="Traffic between sites" value={`${bytes(all.after.crossSiteBps)}`} sub={`was ${bytes(all.before.crossSiteBps)}`} good={all.after.crossSiteBps < all.before.crossSiteBps} />
                          <Stat label="One-off data copying" value={`${pts(plan.recommendations.reduce((n, r) => n + r.target.migrationCost, 0))}`} sub="points, already subtracted" />
                        </div>
                        {all.warnings.length > 0 && (
                          <ul className="mt-3 space-y-1 rounded-lg border border-amber-400/20 bg-amber-400/5 px-3 py-2 text-xs text-amber-200/90">
                            {all.warnings.map((x) => (
                              <li key={x}>{x}</li>
                            ))}
                          </ul>
                        )}
                        <p className="mt-3 text-xs text-nb-500">Each recommendation assumes the ones above it have happened, so their numbers add up and two services are never told to swap places.</p>
                      </Card>
                    )}
                    <div className={clsx('space-y-4', recsChanged && 'fade-in')}>
                      {plan.recommendations.map((r) => (
                        <RecommendationCard key={r.serviceId} r={r} world={world} />
                      ))}
                    </div>
                  </>
                )}
                {(plan.skipped.length > 0 || plan.stay > 0) && (
                  <Card title="Left alone">
                    <p className="text-sm text-nb-400">
                      {plan.stay} service{plan.stay === 1 ? '' : 's'} looked at and best where {plan.stay === 1 ? 'it is' : 'they are'} (network cost now {pts(now.cost)} points).
                    </p>
                    {plan.skipped.length > 0 && (
                      <details className="mt-3 text-sm">
                        <summary className="cursor-pointer text-nb-300">{plan.skipped.length} cannot be moved</summary>
                        <ul className="mt-2 space-y-1 text-nb-400">
                          {plan.skipped.map((s) => (
                            <li key={s.serviceId}><span className="text-white">{s.serviceName}</span> — {s.why}</li>
                          ))}
                        </ul>
                      </details>
                    )}
                  </Card>
                )}
              </>
            )}
            {tab === 'whatif' && <WhatIf world={world} policy={policy} initial={initial} />}
            {tab === 'deciders' && <Deciders world={world} policy={policy} />}
          </div>
          <aside className="min-w-0">
            <PolicyPanel />
          </aside>
        </div>
      )}
    </>
  )
}

const bytes = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)} MB/s` : n >= 1e3 ? `${(n / 1e3).toFixed(0)} KB/s` : `${Math.round(n)} B/s`)

function Stat({ label, value, sub, good }: { label: string; value: string; sub?: string; good?: boolean }) {
  return (
    <div>
      <div className="text-xs text-nb-500">{label}</div>
      <div className={clsx('text-2xl font-medium tabular-nums', good ? 'text-emerald-300' : 'text-white')}>{value}</div>
      {sub && <div className="text-xs tabular-nums text-nb-500">{sub}</div>}
    </div>
  )
}

/** What the advice stands on, so an estimate is never mistaken for a measurement. */
function Coverage({ cov }: { cov: ReturnType<typeof coverage> }) {
  const known = cov.rtt['same-cluster'] + cov.rtt.measured + cov.rtt.declared + cov.rtt['same-site']
  const guessed = cov.rtt.estimated + cov.rtt.unknown
  return (
    <p className="rounded-lg border border-nb-850 bg-nb-925 px-4 py-3 text-sm text-nb-400" data-testid="coverage">
      Built on {cov.edges} connection{cov.edges === 1 ? '' : 's'}: {cov.trafficMeasured} with measured traffic; round trips are known for {known}
      {cov.rtt.measured > 0 && <> ({cov.rtt.measured} measured)</>} and estimated or unknown for {guessed}.{' '}
      {guessed > 0 && (
        <Link to="/sites" className="text-accent hover:underline">
          Measure the paths
        </Link>
      )}
    </p>
  )
}

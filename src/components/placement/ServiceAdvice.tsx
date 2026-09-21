import { ArrowRight } from 'lucide-react'
import { Link } from 'react-router-dom'
import { isMovableKind } from '@/lib/placement/engine'
import { usePlan } from '@/lib/placement/usePlacement'
import { Confidence, pts } from './shared'
import { FitBadge, Hedge, Why } from './Why'

/** What the placement advice says about one service, in the inspector. Advice only. */
export default function ServiceAdvice({ serviceId }: { serviceId: string }) {
  const { world, plan } = usePlan()
  const svc = world.byService.get(serviceId)
  if (!svc) return null
  const rec = plan.recommendations.find((r) => r.serviceId === serviceId)
  const skipped = plan.skipped.find((s) => s.serviceId === serviceId)
  const name = (id: string) => world.byCluster.get(id)?.name ?? id
  return (
    <div className="px-2 text-sm" data-testid="service-advice">
      {rec ? (
        <>
          <p className="flex flex-wrap items-center gap-2 text-nb-300">
            <span>{name(rec.from)}</span> <ArrowRight size={13} aria-hidden /> <span className="text-white">{name(rec.to)}</span>
            <span className="rounded-md bg-accent-soft px-2 py-0.5 text-xs font-medium text-accent">saves {pts(rec.net)} points</span>
          </p>
          <p className="mt-2 text-xs text-nb-400">{rec.reasons[0] ?? 'The combined effect of several small differences.'}</p>
          {(rec.confidence === 'low' || rec.confidence === 'none') && (
            <div className="mt-2">
              <Hedge level={rec.confidence} inputs={rec.inputs} verdict={rec.fit} />
            </div>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-3 text-xs">
            <FitBadge verdict={rec.fit} />
            <Confidence level={rec.confidence} />
            <Link to={`/placement?tab=whatif&service=${encodeURIComponent(serviceId)}&to=${encodeURIComponent(rec.to)}`} className="text-accent hover:underline">
              See the evidence
            </Link>
          </div>
          <Why facts={rec.facts} wouldChange={rec.wouldChange.map((c) => c.text)} fixes={rec.fixes} now={world.cap.now} />
        </>
      ) : skipped ? (
        <p className="text-xs text-nb-400">{skipped.why}</p>
      ) : !isMovableKind(svc) ? (
        <p className="text-xs text-nb-400">A {svc.kind} does not move.</p>
      ) : (
        <p className="text-xs text-nb-400">
          Nothing beats where it runs now, or nothing is known yet about what it talks to. <Link to="/placement" className="text-accent hover:underline">Placement</Link>
        </p>
      )}
    </div>
  )
}

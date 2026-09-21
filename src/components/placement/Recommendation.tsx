import clsx from 'clsx'
import { ArrowRight, ChevronDown, ChevronRight } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { TierBadge } from '@/components/ui/primitives'
import { bytesPerSec } from '@/lib/observed'
import type { Recommendation } from '@/lib/placement/types'
import type { World } from '@/lib/placement/world'
import { sensitivityText } from '@/lib/advice'
import { Basis, Confidence, fmtMs, pts, Verdict } from './shared'
import { FitBadge, Hedge, Why } from './Why'

/** One recommendation, with the evidence it stands on, ready to argue with. */
export default function RecommendationCard({ r, world }: { r: Recommendation; world: World }) {
  const [open, setOpen] = useState(false)
  const from = world.byCluster.get(r.from)
  const to = world.byCluster.get(r.to)
  const after = new Map(r.target.edges.map((e) => [e.dependencyId, e]))
  const hedged = r.confidence === 'low' || r.confidence === 'none'
  const sensitivity = (r.target.advice?.dims ?? []).map(sensitivityText).filter((t) => t !== '')
  return (
    <article
      className={clsx('rounded-xl border bg-nb-925 p-5', hedged ? 'border-dashed border-amber-400/40' : 'border-nb-850')}
      data-testid="recommendation"
      data-service={r.serviceId}
      data-confidence={r.confidence}
      data-fit={r.fit}
    >
      <header className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h3 className="text-base font-medium text-white">{r.serviceName}</h3>
        <span className="flex items-center gap-2 text-sm text-nb-400">
          {from?.name ?? r.from} {from && <TierBadge tier={from.tier} />}
          <ArrowRight size={14} aria-hidden />
          <span className="text-white">{to?.name ?? r.to}</span> {to && <TierBadge tier={to.tier} />}
        </span>
        <span className="ml-auto flex flex-wrap items-center gap-2">
          <Verdict v={r.verdict} />
          <FitBadge verdict={r.fit} />
          <Confidence level={r.confidence} />
          <span className="whitespace-nowrap rounded-md bg-accent-soft px-2 py-0.5 text-xs font-medium text-accent" title="Steady-state improvement in cost points after the one-off cost of copying data. See how points are weighed under Policy.">
            saves {pts(r.net)} points
          </span>
        </span>
      </header>

      {hedged && (
        <div className="mt-3">
          <Hedge level={r.confidence} inputs={r.inputs} verdict={r.fit} />
        </div>
      )}
      <ul className="mt-3 space-y-1.5 text-sm text-nb-300">
        {r.reasons.map((x) => (
          <li key={x} className="flex gap-2">
            <span className="mt-2 size-1.5 shrink-0 rounded-full bg-accent" aria-hidden />
            {x}
          </li>
        ))}
        {r.reasons.length === 0 && <li className="text-nb-500">The combined effect of several small differences.</li>}
      </ul>
      {r.caveats.length > 0 && (
        <ul className="mt-3 space-y-1 rounded-lg border border-amber-400/20 bg-amber-400/5 px-3 py-2 text-xs text-amber-200/90">
          {r.caveats.map((x) => (
            <li key={x}>{x}</li>
          ))}
        </ul>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-3 text-xs">
        <button onClick={() => setOpen((o) => !o)} className="inline-flex items-center gap-1 text-nb-400 hover:text-white" aria-expanded={open}>
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />} Evidence ({r.current.edges.length} connection{r.current.edges.length === 1 ? '' : 's'})
        </button>
        <Link to={`/placement?tab=whatif&service=${encodeURIComponent(r.serviceId)}&to=${encodeURIComponent(r.to)}`} className="text-accent hover:underline">Try it in what-if</Link>
        <Link to={`/topology?sel=service:${encodeURIComponent(r.serviceId)}`} className="text-nb-400 hover:text-white hover:underline">Open the service</Link>
        {r.alternatives.length > 0 && (
          <span className="text-nb-500">
            Other options: {r.alternatives.map((a) => `${world.byCluster.get(a.clusterId)?.name ?? a.clusterId} (${pts(r.current.cost - a.cost)})`).join(', ')}
          </span>
        )}
      </div>

      <Why facts={r.facts} wouldChange={r.wouldChange.map((c) => c.text)} fixes={r.fixes} sensitivity={sensitivity} now={world.cap.now} />

      {open && (
        <div className="mt-3 overflow-x-auto rounded-lg border border-nb-850">
          <table className="w-full text-left text-xs">
            <thead className="text-nb-500">
              <tr>
                <th className="px-3 py-2 font-medium">Talks to</th>
                <th className="px-3 py-2 font-medium">Where</th>
                <th className="px-3 py-2 font-medium">Traffic</th>
                <th className="px-3 py-2 font-medium">Round trip now</th>
                <th className="px-3 py-2 font-medium">Round trip after</th>
              </tr>
            </thead>
            <tbody>
              {r.current.edges.map((e) => {
                const a = after.get(e.dependencyId)
                return (
                  <tr key={e.dependencyId} className="border-t border-nb-850/60 text-nb-300">
                    <td className="px-3 py-2">{e.peerName} <span className="text-nb-600">({e.peerKind})</span></td>
                    <td className="px-3 py-2 text-nb-400">{e.peerWhere}</td>
                    <td className="px-3 py-2 tabular-nums">{e.bytesPerSec !== undefined ? bytesPerSec(e.bytesPerSec) : <span className="text-nb-500">not measured</span>}</td>
                    <td className="px-3 py-2 tabular-nums">{fmtMs(e.rtt.ms)} ms <Basis b={e.rtt.basis} /></td>
                    <td className="px-3 py-2 tabular-nums">{a ? <>{fmtMs(a.rtt.ms)} ms <Basis b={a.rtt.basis} /></> : '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </article>
  )
}

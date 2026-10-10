import clsx from 'clsx'
import type { ClusterLoad, NodeLoad } from '@/lib/metrics'
import { loadBand } from '@/lib/present'

const BAND = { ok: 'bg-ok', warn: 'bg-warn', hot: 'bg-bad' } as const

/** A thin bar with its number: what share of something is already promised. */
export function MiniBar({ label, pct, title }: { label: string; pct?: number; title?: string }) {
  if (pct === undefined) return null
  return (
    <span className="inline-flex items-center gap-1" title={title ?? `${label}: ${pct}% requested by pods`}>
      <span className="text-[10px] text-nb-500">{label}</span>
      <span className="relative h-1 w-9 overflow-hidden rounded-full bg-nb-850" aria-hidden>
        <span className={clsx('absolute inset-y-0 left-0 rounded-full', BAND[loadBand(pct)])} style={{ width: `${pct}%` }} />
      </span>
      <span className="w-6 text-[10px] tabular-nums text-nb-400">{pct}%</span>
    </span>
  )
}

/** CPU, memory and pods of one cluster, and how many of its nodes and services are up. Says nothing about what nobody reported. */
export function LoadRow({ load, className }: { load: ClusterLoad; className?: string }) {
  const down = load.nodes - load.ready
  return (
    <div className={clsx('flex flex-wrap items-center gap-x-3 gap-y-0.5', className)} data-testid="cluster-load">
      <MiniBar label="CPU" pct={load.cpuPct} title={load.cpuPct === undefined ? undefined : `${load.cpuPct}% of the cluster's allocatable CPU is requested by pods`} />
      <MiniBar label="Mem" pct={load.memPct} title={load.memPct === undefined ? undefined : `${load.memPct}% of the cluster's allocatable memory is requested by pods`} />
      <MiniBar label="Pods" pct={load.podPct} title={load.pods === undefined ? undefined : `${load.pods} of ${load.podCap} pods`} />
      {load.nodes > 0 && down > 0 && <span className="text-[10px] text-warn">{load.ready}/{load.nodes} nodes ready</span>}
      {load.unready > 0 && <span className="text-[10px] text-warn">{load.unready} service{load.unready === 1 ? '' : 's'} not fully up</span>}
    </div>
  )
}

/** The same three shares as a column of full-width meters, for the Inspector, which now owns them: the canvas only says when one is under pressure. */
export function LoadMeters({ load }: { load: ClusterLoad | NodeLoad }) {
  const rows = [['CPU', load.cpuPct, 'of allocatable CPU requested by pods'], ['Memory', load.memPct, 'of allocatable memory requested by pods'], ['Pods', load.podPct, 'of the pod slots in use']] as const
  const full = 'nodes' in load ? load : undefined
  return (
    <div className="space-y-1.5" data-testid="load-meters">
      {rows.map(([label, pct, what]) =>
        pct === undefined ? null : (
          <div key={label} className="flex items-center gap-3 text-sm" title={`${pct}% ${what}`}>
            <span className="w-16 shrink-0 text-nb-500">{label}</span>
            <span className="relative h-1.5 flex-1 overflow-hidden rounded-full bg-nb-850" aria-hidden>
              <span className={clsx('absolute inset-y-0 left-0 rounded-full', BAND[loadBand(pct)])} style={{ width: `${pct}%` }} />
            </span>
            <span className="w-10 text-right tabular-nums text-nb-300">{pct}%</span>
          </div>
        ),
      )}
      {full && full.nodes > full.ready && <div className="text-sm text-warn">{full.ready}/{full.nodes} nodes ready</div>}
      {full && full.unready > 0 && <div className="text-sm text-warn">{full.unready} service{full.unready === 1 ? '' : 's'} not fully up</div>}
    </div>
  )
}

import clsx from 'clsx'
import type { ClusterLoad } from '@/lib/metrics'
import { loadBand } from '@/lib/present'

const BAND = { ok: 'bg-emerald-400', warn: 'bg-amber-400', hot: 'bg-red-400' } as const

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
      {load.nodes > 0 && down > 0 && <span className="text-[10px] text-amber-300">{load.ready}/{load.nodes} nodes ready</span>}
      {load.unready > 0 && <span className="text-[10px] text-amber-300">{load.unready} service{load.unready === 1 ? '' : 's'} not fully up</span>}
    </div>
  )
}

/** The highest utilisation a cluster reports, for a warning marker; undefined when it reports none. */
export const peakLoad = (l: ClusterLoad): number | undefined => {
  const xs = [l.cpuPct, l.memPct, l.podPct].filter((x): x is number => x !== undefined)
  return xs.length ? Math.max(...xs) : undefined
}

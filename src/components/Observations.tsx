import { ObservationChip } from '@/components/ui/primitives'
import { ageLabel, goneInfo, observation } from '@/lib/provenance'
import type { Cluster, Tombstone } from '@/lib/types'
import { useObserved } from '@/store/observed'
import { useTopology } from '@/store/topology'

const KIND_LABEL: Record<Tombstone['kind'], string> = { cluster: 'Cluster', node: 'Node', namespace: 'Namespace', service: 'Service' }

/**
 * Records that disappeared from what their agent reports. The server keeps them for a retention window, so a
 * deletion is explained (when, and why) instead of the record silently vanishing.
 */
export function GoneRecords({ kinds, className }: { kinds?: Tombstone['kind'][]; className?: string }) {
  const all = useObserved((s) => s.tombstones)
  const { clusters } = useTopology()
  const list = all.filter((t) => !kinds || kinds.includes(t.kind))
  if (list.length === 0) return null
  const clusterName = (id?: string) => clusters.find((c) => c.id === id)?.name ?? id ?? ''
  return (
    <section className={className} data-testid="gone-records" aria-label="Records that are gone">
      <h2 className="mb-2 text-sm font-medium text-white">
        Gone <span className="ml-1 text-nb-500">({list.length})</span>
      </h2>
      <p className="mb-2 text-xs text-nb-500">No longer reported by their agents. Kept for a while so a change can be explained; not used for anything.</p>
      <ul className="divide-y divide-nb-850 rounded-xl border border-nb-850 bg-nb-925">
        {list.map((t) => (
          <li key={`${t.kind}:${t.id}`} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-sm" data-testid="gone-row" data-kind={t.kind}>
            <span className="min-w-0 text-nb-400">
              <span className="text-nb-300 line-through decoration-nb-600">{t.name}</span>
              <span className="ml-2 text-xs text-nb-500">
                {KIND_LABEL[t.kind]}
                {t.clusterId ? ` in ${clusterName(t.clusterId)}` : ''}
              </span>
            </span>
            <span className="flex items-center gap-2 text-xs text-nb-500">
              <span title={t.reason}>gone {ageLabel(t.goneAt)} ago</span>
              <ObservationChip info={goneInfo(t)} />
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** The clusters agents observe, each with how far it can be trusted right now. */
export function ObservedClusters({ className }: { className?: string }) {
  const { clusters, nodes, services } = useTopology()
  const seen = clusters.filter((c: Cluster) => c.source === 'discovered')
  if (seen.length === 0) return null
  return (
    <section className={className} data-testid="observed-clusters">
      <h2 className="mb-2 text-sm font-medium text-white">What the agents see</h2>
      <ul className="divide-y divide-nb-850 rounded-xl border border-nb-850 bg-nb-925">
        {seen.map((c) => {
          const info = observation(c)
          return (
            <li key={c.id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-sm" data-testid="observed-cluster" data-state={info?.kind}>
              <span className="min-w-0 text-nb-300">
                {c.name}
                <span className="ml-2 text-xs text-nb-500">
                  {nodes.filter((n) => n.clusterId === c.id).length} nodes · {services.filter((s) => s.clusterId === c.id).length} services
                </span>
              </span>
              <span className="flex items-center gap-2 text-xs text-nb-500">
                {info?.reason && <span>{info.reason}</span>}
                <ObservationChip info={info} />
              </span>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

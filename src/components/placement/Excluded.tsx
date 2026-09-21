import { ObservationChip } from '@/components/ui/primitives'
import { observation } from '@/lib/provenance'
import { excludedClusters, type World } from '@/lib/placement/world'
import { Card } from './shared'

/**
 * Clusters the advice will not place anything on, each with the reason. Only what is known to be there right now
 * (live) and whose room is known can take a workload; showing the rest, rather than hiding them, is what lets
 * someone see why a cluster they expected is never suggested.
 */
export default function Excluded({ world }: { world: World }) {
  const rows = excludedClusters(world)
  if (rows.length === 0) return null
  return (
    <Card title={`${rows.length} cluster${rows.length === 1 ? '' : 's'} left out of placement`} className="border-amber-400/20">
      <ul className="space-y-1.5" data-testid="excluded-targets">
        {rows.map(({ cluster, status }) => (
          <li key={cluster.id} className="flex flex-wrap items-center gap-2 text-sm" data-testid="excluded-target" data-state={status.state}>
            <span className="text-white">{cluster.name}</span>
            <ObservationChip info={observation(cluster)} />
            <span className="text-nb-400" data-testid="excluded-reason">{status.reason}</span>
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs text-nb-500">Services running there are not advised on, and nothing is advised to move there, until the cluster is live again.</p>
    </Card>
  )
}

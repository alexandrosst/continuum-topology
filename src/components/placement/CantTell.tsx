import { CircleHelp } from 'lucide-react'
import { Link } from 'react-router-dom'
import { ObservationChip } from '@/components/ui/primitives'
import { observation } from '@/lib/provenance'
import { unverifiableClusters, type World } from '@/lib/placement/world'
import { Card } from './shared'

/**
 * Clusters that are live, so not left out, but whose room is not fully known: nothing can be certified to fit there.
 * They are not "excluded" (an unknown is not a no); they are listed apart, with what is missing and what fixes it.
 */
export default function CantTell({ world }: { world: World }) {
  const rows = unverifiableClusters(world)
  if (rows.length === 0) return null
  return (
    <Card title={`Can’t tell: ${rows.length} cluster${rows.length === 1 ? '' : 's'} where room is not known`} className="border-dashed border-warn/40">
      <ul className="space-y-3" data-testid="cant-tell-targets">
        {rows.map((r) => (
          <li key={r.cluster.id} className="text-sm" data-testid="cant-tell-target" data-nothing={r.nothing}>
            <div className="flex flex-wrap items-center gap-2">
              <CircleHelp size={14} className="shrink-0 text-warn" aria-hidden />
              <span className="text-nb-300">{r.cluster.name}</span>
              <ObservationChip info={observation(r.cluster)} />
              <span className="rounded border border-dashed border-warn/50 px-1.5 py-px text-[11px] text-warn">can’t tell</span>
            </div>
            <p className="mt-1 pl-6 text-xs text-nb-400" data-testid="cant-tell-why">Missing: {r.why}.</p>
            {r.fix && (
              <p className="mt-0.5 pl-6 text-xs">
                <span className="text-nb-500">To find out: </span>
                {r.fix.link ? <Link to={r.fix.link} className="text-accent hover:underline" data-testid="fix-link">{r.fix.text}</Link> : <span className="text-nb-300">{r.fix.text}</span>}
              </p>
            )}
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs text-nb-500">Nothing is recommended to move to these until what they have free is known: not because it would not fit, but because it cannot be said that it would.</p>
    </Card>
  )
}

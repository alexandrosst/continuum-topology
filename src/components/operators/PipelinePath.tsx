import { ArrowRight } from 'lucide-react'
import { Fragment } from 'react'
import { Link } from 'react-router-dom'
import StateChip from '@/components/operators/StateChip'
import { ICON_SM, StatTile } from '@/components/ui/primitives'
import { lastDataText, type Hop, type Kind } from '@/lib/operatorsView'

/** What a hop's figure says: how many of its components are healthy. FUSION is one thing, on or off. */
function figure(h: Hop): string {
  if (h.key === 'fusion') return h.total === 0 ? 'Off' : h.state === 'unknown' ? 'Starting' : 'On'
  return h.total === 0 ? '0' : `${h.healthy} of ${h.total}`
}

function sub(h: Hop, now: number): string {
  if (h.total === 0) return h.key === 'fusion' ? 'nothing is saved' : h.key === 'regional' ? 'optional' : 'none yet'
  return h.lastData ? `last data ${lastDataText(h.lastData, now)}` : 'no data yet'
}

/**
 * The path telemetry takes, hop by hop - discovery agents, local operators, regional operators, the central operator, FUSION - as the same
 * tiles the Agents page uses for its figures: how many are healthy, the worst state among them and when data last arrived. Choosing a hop
 * shows just its components in the table below; FUSION's own tile opens its section.
 */
export default function PipelinePath({ hops, now, selected, onSelect }: { hops: Hop[]; now: number; selected: string; onSelect: (kind: Kind) => void }) {
  return (
    <ol className="mb-6 grid grid-cols-2 gap-3 lg:flex lg:items-stretch lg:gap-2" aria-label="Data path" data-testid="pipeline-path">
      {hops.map((h, i) => {
        const tile = (
          <StatTile label={h.label} value={figure(h)} sub={sub(h, now)} tone={h.state === 'attention' || h.state === 'down' ? 'warn' : undefined} className={selected === h.key ? 'ring-1 ring-accent/40' : undefined}>
            {h.state && h.total > 0 && <StateChip state={h.state} />}
          </StatTile>
        )
        const focus = 'block h-full w-full rounded-xl text-left focus-visible:outline-2 focus-visible:outline-accent/60'
        return (
          <Fragment key={h.key}>
            {i > 0 && <li className="hidden items-center text-nb-600 lg:flex" aria-hidden><ArrowRight size={ICON_SM} /></li>}
            <li className="min-w-0 lg:flex-1" data-testid={`hop-${h.key}`}>
              {h.key === 'fusion' ? (
                <Link to="/fusion" className={focus} aria-label="FUSION: open its section">{tile}</Link>
              ) : (
                <button type="button" className={focus} aria-pressed={selected === h.key} aria-label={`${h.label}: show them in the table`} onClick={() => onSelect(h.key as Kind)}>{tile}</button>
              )}
            </li>
          </Fragment>
        )
      })}
    </ol>
  )
}

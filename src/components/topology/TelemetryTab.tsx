import clsx from 'clsx'
import { Plug } from 'lucide-react'
import { useMemo, useState } from 'react'
import Inspector, { type Selection } from '@/components/topology/Inspector'
import { PlatformNode } from '@/components/topology/PlatformNode'
import { usePlatformLayer } from '@/components/topology/usePlatformLayer'
import { Button, EmptyState, ICON_SM, SkeletonBlock } from '@/components/ui/primitives'
import { hopNeedsLabel, LANE, layoutLanes, nodeLabel, type LaneHop, type LaneLayout } from '@/lib/telemetryLanes'
import type { Agent, Cluster } from '@/lib/types'

/** A hop's line: moving dashes while data flows, still and solid amber or red when it has stalled or broken, grey and dashed when not known. */
const HOP_LINE: Record<LaneHop['status'], string> = {
  healthy: 'flow-dash stroke-ok/70',
  attention: 'stroke-warn',
  down: 'stroke-bad',
  unknown: 'stroke-nb-600 [stroke-dasharray:4_6]',
}
const HOP_TEXT: Record<LaneHop['status'], string> = { healthy: 'text-nb-500', attention: 'text-warn', down: 'text-bad', unknown: 'text-nb-500' }

function Lanes({ layout, selectedId, onSelect }: { layout: LaneLayout; selectedId?: string; onSelect: (id: string) => void }) {
  // Whose hops say their age: any that is not healthy, and those of the part under the pointer, the keyboard focus or the selection.
  const [active, setActive] = useState<string | null>(null)
  const lit = active ?? selectedId
  const ageFrom = new Map(layout.hops.filter((h) => h.age).map((h) => [h.from, h.age]))
  return (
    <div className="relative" style={{ width: layout.width, height: layout.height }} data-testid="telemetry-lanes">
      {layout.columns.map((c) => (
        <div key={c.kind} className="absolute truncate text-[11px] font-medium uppercase tracking-wide text-nb-500" style={{ left: c.x, top: 0, width: LANE.nodeW }}>{c.label}</div>
      ))}
      {layout.lanes.map((l) => (
        <div key={l.clusterId} className="absolute inset-x-0 rounded-xl bg-nb-925/60" style={{ top: l.y, height: LANE.laneH - 10 }} data-testid="lane">
          <div className="truncate px-1 pt-1 text-xs text-nb-400" style={{ width: 2 * LANE.nodeW + LANE.colGap }}>{l.name}</div>
        </div>
      ))}
      <svg className="absolute left-0 top-0" width={layout.width} height={layout.height} aria-hidden>
        {layout.hops.map((h) => (
          <g key={h.id} data-testid="hop" data-status={h.status} data-flowing={h.flowing}>
            <path d={h.d} fill="none" strokeWidth={1.6} strokeLinecap="round" className={HOP_LINE[h.status]} />
            <path d={h.d} fill="none" strokeWidth={14} className="stroke-transparent" style={{ pointerEvents: 'stroke' }} onMouseEnter={() => setActive(h.from)} onMouseLeave={() => setActive(null)} />
          </g>
        ))}
      </svg>
      {layout.hops.filter((h) => hopNeedsLabel(h) || (h.age && h.from === lit)).map((h) => (
        <span key={h.id} className={clsx('pointer-events-none absolute -translate-x-1/2 -translate-y-1/2 rounded bg-nb-910 px-1 text-[10.5px]', HOP_TEXT[h.status])} style={{ left: h.mid.x, top: h.mid.y }} data-testid="hop-age">{h.age}</span>
      ))}
      {layout.nodes.map(({ entity, x, y }) => (
        <PlatformNode
          key={entity.id}
          entity={entity}
          selected={entity.id === selectedId}
          label={nodeLabel(entity, ageFrom.get(entity.id))}
          className="absolute"
          style={{ left: x, top: y, width: LANE.nodeW, height: LANE.nodeH }}
          onClick={() => onSelect(entity.id)}
          onMouseEnter={() => setActive(entity.id)}
          onMouseLeave={() => setActive(null)}
          onFocus={() => setActive(entity.id)}
          onBlur={() => setActive(null)}
        />
      ))}
    </div>
  )
}

/**
 * The Telemetry tab: the path of telemetry as lanes (lib/telemetryLanes.ts), one per cluster. Mounted only while the tab is open, which
 * is what keeps the operators and intents from being read anywhere else. A part opens in the same Inspector as on the other tabs.
 */
export default function TelemetryTab({ clusters, agents, problemsOnly, selection, onSelect, onShowAll, onConnect, onClose }: {
  clusters: Cluster[]
  agents: Agent[]
  problemsOnly: boolean
  selection: Selection
  onSelect: (s: Selection) => void
  onShowAll: () => void
  onConnect: () => void
  onClose: () => void
}) {
  const known = useMemo(() => clusters.map((c) => ({ id: c.id, name: c.name })), [clusters])
  const model = usePlatformLayer(known, agents)
  const layout = useMemo(() => model && layoutLanes(model, { problemsOnly }), [model, problemsOnly])
  const hasAgents = !!model?.entities.some((e) => e.kind === 'agent')
  return (
    <div className="flex min-h-0 flex-1">
      <div className="relative min-w-0 flex-1 overflow-auto p-4 sm:p-6">
        {!layout ? (
          <SkeletonBlock className="h-64 w-full" />
        ) : layout.lanes.length > 0 ? (
          <Lanes layout={layout} selectedId={selection?.kind === 'platform' ? selection.id : undefined} onSelect={(id) => onSelect({ kind: 'platform', id })} />
        ) : hasAgents ? (
          <EmptyState title="No problems" description="Every part of the pipeline is healthy, or not known yet, so there is nothing to show here." action={<Button onClick={onShowAll} data-testid="show-all-lanes">Show every cluster</Button>} />
        ) : (
          <EmptyState
            title="No cluster sends telemetry yet"
            description="Each cluster with a Discovery agent gets a lane here, from the agent to FUSION. Connect a cluster to start."
            action={<Button variant="primary" onClick={onConnect} data-testid="telemetry-connect"><Plug size={ICON_SM} /> Connect a cluster</Button>}
          />
        )}
      </div>
      <Inspector selection={selection} platform={model} onSelect={onSelect} onEdit={() => {}} onClose={onClose} />
    </div>
  )
}

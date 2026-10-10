import clsx from 'clsx'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import Inspector, { type Selection } from '@/components/topology/Inspector'
import { PlatformNode } from '@/components/topology/PlatformNode'
import { ClusterCell, ColumnHead, ConnectAction, PhoneList, Quiet, SummaryBar, type ClusterFacts } from '@/components/topology/TelemetryParts'
import { usePlatformLayer } from '@/components/topology/usePlatformLayer'
import { Button, SkeletonBlock } from '@/components/ui/primitives'
import { nextProblem } from '@/lib/problems'
import { hopNeedsLabel, LANE, layoutLanes, nodeLabel, type LaneHop, type LaneLayout } from '@/lib/telemetryLanes'
import { summarize } from '@/lib/telemetrySummary'
import type { Agent, Cluster } from '@/lib/types'

/**
 * A hop's line. Quiet while all is well: a thin neutral line whose slow dashes say data is moving. Loud only when something is wrong: amber
 * or red, still and a little thicker, with its age on a pill sitting on the line. Not known is a fine grey dotted line.
 */
const HOP_LINE: Record<LaneHop['status'], string> = {
  healthy: 'flow-dash stroke-nb-600/60',
  attention: 'stroke-warn',
  down: 'stroke-bad',
  unknown: 'stroke-nb-600/60 [stroke-dasharray:1.5_5]',
}
const HOP_TIP: Record<LaneHop['status'], string> = { healthy: 'stroke-nb-600/60', attention: 'stroke-warn', down: 'stroke-bad', unknown: 'stroke-nb-600/60' }
const HOP_PILL: Record<LaneHop['status'], string> = { healthy: 'border-nb-800 text-nb-500', attention: 'border-warn/40 text-warn', down: 'border-bad/40 text-bad', unknown: 'border-nb-800 text-nb-500' }
const LOUD = { healthy: 0, unknown: 0, attention: 1, down: 2 } as const

/** Everything on the way from one part to FUSION and back to its cluster: what lights up while a part is hovered or focused. */
function chainOf(hops: LaneHop[], id: string): Set<string> {
  const lit = new Set<string>()
  const walk = (from: string, dir: 'down' | 'up') => {
    for (const h of hops) {
      const [a, b] = dir === 'down' ? [h.from, h.to] : [h.to, h.from]
      if (a === from && !lit.has(h.id)) { lit.add(h.id); walk(b, dir) }
    }
  }
  walk(id, 'down')
  walk(id, 'up')
  return lit
}

function Lanes({ layout, facts, selectedId, onSelect }: { layout: LaneLayout; facts: Map<string, ClusterFacts>; selectedId?: string; onSelect: (id: string) => void }) {
  // The line a part belongs to lights up while the pointer or the keyboard is on it, or it is selected.
  const [active, setActive] = useState<string | null>(null)
  const lit = useMemo(() => {
    const id = active ?? selectedId
    return id ? chainOf(layout.hops, id) : new Set<string>()
  }, [layout.hops, active, selectedId])
  const hops = useMemo(() => [...layout.hops].sort((a, b) => LOUD[a.status] - LOUD[b.status]), [layout.hops])
  const oy = LANE.head
  return (
    <div className="px-4 pb-8 sm:px-6" style={{ width: layout.width + 48 }}>
      <div className="sticky top-0 z-20 -mx-4 bg-nb-900 px-4 pt-2 sm:-mx-6 sm:px-6">
        <ColumnHead layout={layout} />
      </div>
      <div className="relative" style={{ width: layout.width, height: layout.height - oy }} data-testid="telemetry-lanes">
        {layout.lanes.map((l) => (
          <ClusterCell key={l.clusterId} name={l.name} facts={facts.get(l.clusterId)} className="absolute left-0" style={{ top: l.y - LANE.nodeH / 2 - oy, width: LANE.nameW, height: LANE.nodeH }} />
        ))}
        <svg className="absolute left-0 top-0" width={layout.width} height={layout.height - oy} viewBox={`0 ${oy} ${layout.width} ${layout.height - oy}`} aria-hidden>
          {hops.map((h) => (
            <g key={h.id} data-testid="hop" data-status={h.status} data-flowing={h.flowing} fill="none" strokeLinecap="round" strokeLinejoin="round" className="transition-opacity duration-150 motion-reduce:transition-none">
              <path d={h.d} strokeWidth={h.status === 'healthy' || h.status === 'unknown' ? 1.25 : 1.75} className={clsx(HOP_LINE[h.status], lit.has(h.id) && (h.status === 'healthy' || h.status === 'unknown') && '!stroke-nb-400')} />
              <path d={h.tip} strokeWidth={h.status === 'healthy' || h.status === 'unknown' ? 1.25 : 1.75} className={clsx(HOP_TIP[h.status], lit.has(h.id) && (h.status === 'healthy' || h.status === 'unknown') && '!stroke-nb-400')} />
            </g>
          ))}
        </svg>
        {layout.hops.filter(hopNeedsLabel).map((h) => (
          <span key={h.id} aria-hidden className={clsx('pointer-events-none absolute -translate-x-1/2 -translate-y-1/2 whitespace-nowrap rounded-full border bg-nb-900 px-1.5 py-px text-[11px] leading-4 tabular-nums', HOP_PILL[h.status])} style={{ left: h.mid.x, top: h.mid.y - oy }} data-testid="hop-age">{h.age}</span>
        ))}
        {layout.nodes.map(({ entity, x, y, senders }) => (
          <PlatformNode
            key={entity.id}
            entity={entity}
            selected={entity.id === selectedId}
            label={nodeLabel(entity)}
            senders={senders}
            tip
            tipAlign={x + 240 > layout.width ? 'right' : 'left'}
            className="absolute"
            style={{ left: x, top: y - oy, width: layout.nodeW, height: LANE.nodeH }}
            onClick={() => onSelect(entity.id)}
            onMouseEnter={() => setActive(entity.id)}
            onMouseLeave={() => setActive(null)}
            onFocus={() => setActive(entity.id)}
            onBlur={() => setActive(null)}
          />
        ))}
      </div>
    </div>
  )
}

/** The size of an element, kept up to date. Undefined until it is measured, and where nothing can measure (a test), so the grid is drawn at its own size. */
function useBox<T extends HTMLElement>() {
  const [box, setBox] = useState<{ w: number; h: number } | undefined>()
  const ref = useRef<T | null>(null)
  const set = useCallback((el: T | null) => { ref.current = el }, [])
  useLayoutEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    // The outer size, not the inner one: a scrollbar appearing must not change it, or the layout it causes would toggle the scrollbar again.
    const read = () => setBox((b) => (b && b.w === el.offsetWidth && b.h === el.offsetHeight ? b : { w: el.offsetWidth, h: el.offsetHeight }))
    read()
    const ro = new ResizeObserver(read)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [box, set] as const
}

/** Narrower than this the five columns do not fit, whatever is done with the gutters: the pipeline becomes a list. */
const LIST_BELOW = 720

/**
 * The Telemetry tab: the path of telemetry as a grid (lib/telemetryLanes.ts), one row per cluster, under one sentence on where it stands
 * (lib/telemetrySummary.ts). Mounted only while the tab is open, which is what keeps the operators and intents from being read anywhere
 * else. A part opens in the same Inspector as on the other tabs.
 */
export default function TelemetryTab({ clusters, agents, problemsOnly, selection, onSelect, onShowAll, onSetProblemsOnly, onConnect, onClose }: {
  clusters: Cluster[]
  agents: Agent[]
  problemsOnly: boolean
  selection: Selection
  onSelect: (s: Selection) => void
  onShowAll: () => void
  /** Turns the "only problems" filter on or off (the page keeps it in the URL). */
  onSetProblemsOnly?: (on: boolean) => void
  onConnect: () => void
  onClose: () => void
}) {
  const known = useMemo(() => clusters.map((c) => ({ id: c.id, name: c.name })), [clusters])
  const facts = useMemo(() => new Map<string, ClusterFacts>(clusters.map((c) => [c.id, { tier: c.tier, distribution: c.distribution }])), [clusters])
  const model = usePlatformLayer(known, agents)
  const [box, scroller] = useBox<HTMLDivElement>()
  const list = !!box && box.w < LIST_BELOW
  // Less the page's padding and room for a scrollbar on each side.
  const [width, height] = box ? [box.w - 48 - 12, box.h - 8 - 12] : [undefined, undefined]
  const layout = useMemo(() => model && layoutLanes(model, { problemsOnly, width, height }), [model, problemsOnly, width, height])
  // The problems are those of the whole pipeline, whatever the filter keeps, in the order the grid shows them.
  const whole = useMemo(() => (model && problemsOnly ? layoutLanes(model, { width, height }) : layout), [model, layout, problemsOnly, width, height])
  const summary = useMemo(() => {
    if (!model || !whole) return undefined
    const order = new Map(whole.nodes.map((n) => [n.entity.id, n.y * 10_000 + n.x]))
    return summarize(model, (id) => order.get(id) ?? 0)
  }, [model, whole])
  const hasAgents = !!model?.entities.some((e) => e.kind === 'agent')
  const selectedId = selection?.kind === 'platform' ? selection.id : undefined
  const select = (id: string) => onSelect({ kind: 'platform', id })

  // The pill counts places, and a place has one part to open: the selected part of a cluster counts as that place being selected.
  const places = useMemo(() => summary?.places ?? [], [summary])
  const placeSelected = places.find((p) => p.id === selectedId || (!!selectedId && p.id.split(':')[1] === selectedId.split(':')[1] && /^(agent|local):/.test(p.id) && /^(agent|local):/.test(selectedId)))?.id ?? null
  // Opening a place brings it into view, whatever the scroll or the size of the grid: smoothly, unless the person asked for less motion.
  const reveal = useCallback((id: string) => {
    select(id)
    requestAnimationFrame(() => {
      const el = Array.from(document.querySelectorAll('[data-platform-id]')).find((n) => n.getAttribute('data-platform-id') === id)
      el?.scrollIntoView({ block: 'center', inline: 'center', behavior: window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const go = useCallback((back: boolean) => {
    const p = nextProblem(places, placeSelected, back)
    if (p) reveal(p.id)
  }, [places, placeSelected, reveal])
  const any = places.length > 0
  useEffect(() => {
    if (!any) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.key !== 'n' && e.key !== 'N') || e.metaKey || e.ctrlKey || e.altKey) return
      const el = document.activeElement
      if (el instanceof HTMLElement && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable)) return
      e.preventDefault()
      go(e.shiftKey)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [any, go])

  const showSummary = !!summary && (hasAgents || !!layout?.lanes.length)
  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        {showSummary && <SummaryBar summary={summary} selectedId={placeSelected} problemsOnly={problemsOnly} onGo={go} onShow={() => (placeSelected ? reveal(placeSelected) : go(false))} onToggle={() => (problemsOnly ? onShowAll() : onSetProblemsOnly?.(true))} />}
        <div ref={scroller} className={clsx('relative min-h-0 flex-1 overflow-auto', !layout && 'p-4 sm:p-6')} data-testid="telemetry-scroll">
          {!layout ? (
            <SkeletonBlock className="h-64 w-full" />
          ) : layout.lanes.length > 0 ? (
            list ? <PhoneList layout={layout} facts={facts} selectedId={selectedId} onSelect={select} /> : <Lanes layout={layout} facts={facts} selectedId={selectedId} onSelect={select} />
          ) : hasAgents ? (
            <Quiet title="No problems" description="Every part of the pipeline is healthy, or not known yet, so there is nothing to show here." path={false} action={<Button onClick={onShowAll} data-testid="show-all-lanes">Show every cluster</Button>} />
          ) : (
            <Quiet title="No data yet" description="Each cluster with a Discovery agent gets a row here, from the agent to FUSION. Connect a cluster to start." action={<ConnectAction onConnect={onConnect} />} />
          )}
        </div>
      </div>
      <Inspector selection={selection} platform={model} onSelect={onSelect} onEdit={() => {}} onClose={onClose} />
    </div>
  )
}

import clsx from 'clsx'
import { Plug } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'
import { PLATFORM_ICON, PlatformNode, StatusGlyph } from '@/components/topology/PlatformNode'
import { ProblemsPill } from '@/components/topology/ProblemsPill'
import { Button, ICON_SM } from '@/components/ui/primitives'
import type { PlatformEntity, PlatformKind } from '@/lib/platformLayer'
import { aside, headline, type TelemetrySummary } from '@/lib/telemetrySummary'
import { LANE_COLUMNS, nodeLabel, type LaneLayout } from '@/lib/telemetryLanes'
import type { Tier } from '@/lib/types'

const TIER_WORD: Record<Tier, string> = { cloud: 'Cloud', edge: 'Edge', 'far-edge': 'Far edge' }

/** What the cluster rows are labelled with: its name, and one muted line saying what kind of place it is. */
export interface ClusterFacts { tier?: Tier; distribution?: string }

/**
 * The thin line on top of the tab: how many of the clusters that are meant to send are sending, and, when something is wrong, the same
 * problems pill the other tabs have (it counts places - clusters and operators - with a problem, tinted by the worst of them) with a
 * switch that keeps only those places on the grid. It replaces a lone "Problems only" button: the sentence says where things stand,
 * the pill is the way to the first problem.
 */
export function SummaryBar({ summary, selectedId, problemsOnly, onGo, onShow, onToggle }: {
  summary: TelemetrySummary
  selectedId: string | null
  problemsOnly: boolean
  onGo: (back: boolean) => void
  onShow: () => void
  onToggle: () => void
}) {
  const note = aside(summary)
  const lead = summary.waiting > 0 || summary.clusters === 0 ? 'unknown' : 'healthy'
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-nb-850 px-4 py-2 sm:px-6" data-testid="telemetry-summary">
      <p className="flex min-h-8 items-center gap-2 text-[13px] tabular-nums text-nb-300">
        {summary.places.length === 0 && <StatusGlyph status={lead} />}
        <span><span className="font-medium">{headline(summary)}</span>{note && <span className="text-nb-500"> · {note}</span>}</span>
      </p>
      <ProblemsPill problems={summary.places} selectedId={selectedId} onGo={onGo} onShow={onShow} hint="a cluster's agent and its local operator count as one" />
      {(summary.places.length > 0 || problemsOnly) && (
        <button
          type="button"
          onClick={onToggle}
          aria-pressed={problemsOnly}
          data-testid="problems-only"
          className={clsx('ml-auto h-8 rounded-lg px-2.5 text-xs transition-colors duration-150 motion-reduce:transition-none focus-visible:outline-2 focus-visible:outline-accent/60', problemsOnly ? 'bg-accent-soft text-accent' : 'text-nb-400 hover:bg-nb-925 hover:text-nb-300')}
          title="Keep only the clusters where something needs attention or is not working"
        >
          Only problems
        </button>
      )}
    </div>
  )
}

/** The column names, the only place the role is spelled out in the grid: an icon and a small caps label, level with the text of the boxes below. */
export function ColumnHead({ layout }: { layout: LaneLayout }) {
  return (
    <div className="relative border-b border-nb-850" style={{ width: layout.width, height: 40 }} data-testid="telemetry-columns">
      <span className="absolute bottom-2 left-0 text-[11px] font-medium uppercase tracking-wider text-nb-400">Cluster</span>
      {layout.columns.map((c) => {
        const Icon = PLATFORM_ICON[c.kind]
        return (
          <span key={c.kind} className="absolute bottom-2 flex items-center gap-1.5 whitespace-nowrap text-[11px] font-medium uppercase tracking-wider text-nb-400" style={{ left: c.x + 12 }}>
            <Icon size={ICON_SM} aria-hidden className="text-nb-500" />
            {c.label}
          </span>
        )
      })}
    </div>
  )
}

/** A cluster's row header: its name, and what kind of place it is (tier, distribution), in the type scale of every other card. */
export function ClusterCell({ name, facts, className, style }: { name: string; facts?: ClusterFacts; className?: string; style?: React.CSSProperties }) {
  const meta = [facts?.tier && TIER_WORD[facts.tier], facts?.distribution].filter(Boolean).join(' · ')
  return (
    <div className={clsx('flex flex-col justify-center pr-3', className)} style={style} data-testid="lane">
      <span className="truncate text-[13px] font-semibold leading-[18px] text-nb-300" title={name}>{name}</span>
      {meta && <span className="truncate text-[11px] leading-4 text-nb-500">{meta}</span>}
    </div>
  )
}

/** Nothing to draw: a faint outline of the path, one sentence, one action. */
export function Quiet({ title, description, action, path = true }: { title: string; description: string; action?: ReactNode; path?: boolean }) {
  const kinds = LANE_COLUMNS.map((c) => c.kind) as PlatformKind[]
  return (
    <div className="mx-auto flex max-w-md flex-col items-center px-4 py-16 text-center" data-testid="telemetry-quiet">
      {path && (
        <ol className="mb-6 flex items-center" aria-hidden>
          {kinds.map((k, i) => {
            const Icon = PLATFORM_ICON[k]
            return (
              <Fragment key={k}>
                {i > 0 && <li className="h-px w-5 bg-nb-800" />}
                <li className="grid size-9 place-items-center rounded-xl border border-dashed border-nb-800 text-nb-600"><Icon size={ICON_SM + 2} /></li>
              </Fragment>
            )
          })}
        </ol>
      )}
      <h2 className="text-sm font-semibold text-nb-300">{title}</h2>
      <p className="mt-1.5 text-sm text-nb-500">{description}</p>
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

export function ConnectAction({ onConnect }: { onConnect: () => void }) {
  return <Button variant="primary" onClick={onConnect} data-testid="telemetry-connect"><Plug size={ICON_SM} /> Connect a cluster</Button>
}

/**
 * The same pipeline on a phone, as a list: a card per cluster with its agent and local operator, then the shared parts. There is no room
 * for five columns and lines between them, and a list is what a thumb scrolls; each box says its role (there is no header to do it) and
 * where it sends to, so the story the lines tell is still in words.
 */
export function PhoneList({ layout, facts, selectedId, onSelect }: { layout: LaneLayout; facts: Map<string, ClusterFacts>; selectedId?: string; onSelect: (id: string) => void }) {
  const at = (e: PlatformEntity) => layout.nodes.find((n) => n.entity.id === e.id)
  const rows = layout.lanes.map((l) => ({ lane: l, parts: layout.nodes.filter((n) => n.entity.clusterId === l.clusterId).sort((a, b) => a.x - b.x) }))
  const shared = layout.nodes.filter((n) => !n.entity.clusterId).sort((a, b) => a.x - b.x || a.y - b.y)
  const node = (n: NonNullable<ReturnType<typeof at>>) => (
    <PlatformNode key={n.entity.id} entity={n.entity} selected={n.entity.id === selectedId} label={nodeLabel(n.entity)} senders={n.senders} withRole className="relative h-12" onClick={() => onSelect(n.entity.id)} />
  )
  return (
    <div className="flex flex-col gap-6 px-4 py-4" data-testid="telemetry-list">
      {rows.map(({ lane, parts }) => (
        <section key={lane.clusterId} aria-label={lane.name} className="flex flex-col gap-2">
          <ClusterCell name={lane.name} facts={facts.get(lane.clusterId)} className="px-1" />
          {parts.map(node)}
        </section>
      ))}
      {shared.length > 0 && (
        <section aria-label="Shared operators" className="flex flex-col gap-2">
          <h3 className="px-1 text-[11px] font-medium uppercase tracking-wider text-nb-400">Shared</h3>
          {shared.map(node)}
        </section>
      )}
    </div>
  )
}

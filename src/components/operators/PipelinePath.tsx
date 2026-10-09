import clsx from 'clsx'
import { Cable, ChevronDown, ChevronRight, Database, DoorOpen, Funnel, Merge, type LucideIcon } from 'lucide-react'
import { Fragment } from 'react'
import { Link } from 'react-router-dom'
import StateChip from '@/components/operators/StateChip'
import { MeterBar, type BarTone } from '@/components/ui/MeterBar'
import { ICON_MD, ICON_SM } from '@/components/ui/primitives'
import { lastDataText, type Hop, type Kind } from '@/lib/operatorsView'
import { EXPECTED_MS, freshness, hopChip, linkState, type FreshnessLevel, type LinkState } from '@/lib/pipelineFlow'

const ICON: Record<Hop['key'], LucideIcon> = { agent: Cable, local: Funnel, regional: Merge, central: DoorOpen, fusion: Database }

/** What the count says: how many there are. FUSION is one thing, on or off. */
function figure(h: Hop): string {
  if (h.key === 'fusion') return h.total === 0 ? 'Off' : h.state === 'unknown' ? 'Starting' : 'On'
  return String(h.total)
}

const TONE: Record<LinkState, string> = { flowing: 'text-ok', stalled: 'text-warn', broken: 'text-bad', unknown: 'text-nb-700' }
/** The line itself: dashes that move, a still bar, or a still dashed rule (a border, so it has no fill). `v` is the vertical, narrow-screen one. */
const line = (l: LinkState, v: boolean) => ({ flowing: v ? 'flow-dash-bg-v' : 'flow-dash-bg', stalled: 'bg-current', broken: 'bg-current', unknown: v ? 'w-0 border-l-2 border-dashed border-current' : 'h-0 border-t-2 border-dashed border-current' })[l]
const BAR: Record<FreshnessLevel, BarTone> = { fresh: 'ok', late: 'warn', stale: 'bad', none: 'neutral' }
const AGE_TEXT: Record<LinkState, string> = { flowing: 'hidden text-nb-500 group-hover:block group-focus-within:block', stalled: 'text-warn', broken: 'text-bad', unknown: 'text-nb-500' }

function Node({ h, selected, onSelect }: { h: Hop; selected: boolean; onSelect: (kind: Kind) => void }) {
  const Icon = ICON[h.key]
  const chip = hopChip(h)
  const tile = (
    <div className={clsx('flex h-full flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border bg-nb-925 px-3 py-2.5 transition-colors xl:flex-col xl:items-start', selected ? 'border-accent/50 ring-1 ring-accent/40' : 'border-nb-850 hover:border-nb-800', h.total === 0 && 'text-nb-500')}>
      <span className="flex items-center gap-2">
        <Icon size={ICON_MD} className="shrink-0 text-nb-500" aria-hidden />
        <span className={clsx('text-xl font-medium tabular-nums', h.total === 0 ? 'text-nb-500' : 'text-nb-300')}>{figure(h)}</span>
      </span>
      <span className="min-w-0 flex-1 truncate text-xs text-nb-500 xl:w-full xl:flex-none">{h.label}</span>
      {chip && <StateChip state={chip.state} count={chip.count} className="xl:mt-0.5" />}
    </div>
  )
  const focus = 'block h-full w-full rounded-xl text-left'
  return h.key === 'fusion' ? (
    <Link to="/fusion" className={focus} aria-label="FUSION: open its section">{tile}</Link>
  ) : (
    <button type="button" className={focus} aria-pressed={selected} aria-label={`${h.label}: show them in the table`} onClick={() => onSelect(h.key as Kind)}>{tile}</button>
  )
}

/** The line from one hop to the next carries the sender's state: it moves while data is arriving and is still, amber, red or grey dashed
 *  otherwise. Under it a thin freshness bar says how recent the data is; the age itself shows on hover or focus, and always when not fine. */
function Connector({ from, to, now }: { from: Hop; to: Hop; now: number }) {
  // Data into FUSION is judged by FUSION's own last data; every other line by the newest data its sender reports.
  const lastData = to.key === 'fusion' ? to.lastData : from.lastData
  const f = freshness(lastData, now, EXPECTED_MS[from.key])
  const link = linkState(from.state, from.total, f)
  const measured = from.total > 0 && to.total > 0
  const age = lastDataText(lastData, now)
  return (
    <li className={clsx('group relative h-10 xl:h-auto xl:min-w-14 xl:flex-1', TONE[link])} data-testid={`link-${from.key}`} data-link={link} aria-hidden={measured ? undefined : true}>
      <span className={clsx('absolute bottom-3 left-5 top-0 w-0.5 xl:hidden', line(link, true))} />
      <span className={clsx('absolute left-0 right-3 top-6 hidden h-0.5 xl:block', line(link, false))} />
      <ChevronDown size={ICON_SM} className="absolute bottom-0 left-3.5 xl:hidden" aria-hidden />
      <ChevronRight size={ICON_SM} className="absolute right-0 top-6 hidden -translate-y-1/2 xl:block" aria-hidden />
      {measured && (
        <div className="absolute left-10 top-1/2 flex -translate-y-1/2 items-center gap-2 xl:left-1/2 xl:top-8 xl:-translate-x-1/2 xl:translate-y-0 xl:flex-col xl:gap-1">
          <MeterBar pct={f.fraction * 100} tone={BAR[f.level]} label={`Data from ${from.label} to ${to.label}`} valueText={`Last data ${age}`} title={`Last data ${age}`} tabIndex={0} className="w-10" />
          <span className={clsx('whitespace-nowrap text-[11px] tabular-nums', AGE_TEXT[link])}>{age}</span>
        </div>
      )}
    </li>
  )
}

/**
 * The path telemetry takes, as one connected strip: discovery agents, local operators, regional operators, the central operator, FUSION. Each
 * node says how many there are and the worst state among them, and chooses just its components in the table below (FUSION's opens its
 * section); each line between two carries the sender's state and how fresh its data is. Stacks vertically on a narrow screen.
 */
export default function PipelinePath({ hops, now, selected, onSelect }: { hops: Hop[]; now: number; selected: string; onSelect: (kind: Kind) => void }) {
  return (
    <ol className="mb-6 flex flex-col xl:flex-row xl:items-stretch" aria-label="Data path" data-testid="pipeline-path">
      {hops.map((h, i) => (
        <Fragment key={h.key}>
          {i > 0 && <Connector from={hops[i - 1]} to={h} now={now} />}
          <li className="min-w-0 xl:w-36 xl:shrink-0" data-testid={`hop-${h.key}`}>
            <Node h={h} selected={selected === h.key} onSelect={onSelect} />
          </li>
        </Fragment>
      ))}
    </ol>
  )
}

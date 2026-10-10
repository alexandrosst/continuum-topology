import clsx from 'clsx'
import { Cable, Clock, Database, DoorOpen, Funnel, Merge, type LucideIcon } from 'lucide-react'
import { SIGNAL_ICON } from '@/components/topology/signalIcons'
import { ICON_SM } from '@/components/ui/primitives'
import { StatusMark } from '@/components/topology/StatusMark'
import { MODALITY_WORD, PLATFORM_STATUS_WORD, platformFacts, type PlatformEntity, type PlatformKind, type PlatformStatus } from '@/lib/platformLayer'
import type { Modality } from '@/lib/install'

export const PLATFORM_ICON: Record<PlatformKind, LucideIcon> = { agent: Cable, local: Funnel, regional: Merge, central: DoorOpen, fusion: Database }
export const KIND_WORD: Record<PlatformKind, string> = { agent: 'Discovery agent', local: 'Local operator', regional: 'Regional operator', central: 'Central operator', fusion: 'FUSION' }

const TONE: Record<PlatformStatus, string> = { healthy: 'text-ok', attention: 'text-warn', down: 'text-bad', unknown: 'text-nb-500' }

/** The state as the canvas draws it (the same StatusMark: a dot, a triangle, a crossed circle, a ring), so a state reads the same in every layer. */
export function StatusGlyph({ status, className }: { status: PlatformStatus; className?: string }) {
  return <StatusMark state={status} className={className} />
}

/** What a box says about itself, as the same anatomy in every column: a name, one line under it, and a row of small chips. The column header names the role too, but a
 *  box stands on its own (in a list, in a screenshot, to a screen reader), so the role leads wherever it is not the part's own name. Whatever is not Healthy takes the
 *  second line, in words, in its colour; the chips stay. */
interface Chip { text: string; icon?: LucideIcon; title?: string; tone?: 'warn' | 'bad' }
interface Face { title: string; meta?: string; chips: Chip[] }

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const signal = (m: Modality): Chip => ({ text: MODALITY_WORD[m], icon: SIGNAL_ICON[m] })

/** Where a part sends to, in a few words: the part, how many, or the address of a backend of the organisation's own. */
function sendsTo(e: PlatformEntity): string | undefined {
  const to = [...e.sendsTo.map((t) => t.name), ...(e.elsewhere ? [e.elsewhere] : [])]
  return to.length ? `→ ${to.length === 1 ? to[0] : plural(to.length, 'destination', 'destinations')}` : undefined
}

function face(e: PlatformEntity, senders: number | undefined, age: string | undefined): Face {
  const stale = e.status === 'attention' || e.status === 'down' ? e.status === 'down' ? 'bad' : 'warn' : undefined
  switch (e.kind) {
    case 'agent': return { title: KIND_WORD.agent, meta: e.detail, chips: age ? [{ text: `Heard ${age} ago`, icon: Clock, tone: stale }] : [] }
    case 'local': return { title: KIND_WORD.local, meta: sendsTo(e) ?? 'No destination yet', chips: (e.collecting ?? []).map(signal) }
    case 'regional': return { title: e.name, meta: KIND_WORD.regional, chips: senders ? [{ text: plural(senders, 'cluster', 'clusters') }] : [] }
    case 'central': return { title: KIND_WORD.central, meta: 'FUSION gateway', chips: senders ? [{ text: plural(senders, 'operator', 'operators') }] : [] }
    case 'fusion': return e.off ? { title: e.name, meta: 'Not turned on', chips: [] } : { title: e.name, meta: 'Telemetry store', chips: (['metrics', 'logs', 'traces'] as Modality[]).map(signal) }
  }
}

const CHIP_TONE = { warn: 'bg-warn/10 text-warn', bad: 'bg-bad/10 text-bad', none: 'bg-nb-900 text-nb-400' } as const

/**
 * A part of the telemetry platform, in the node language of a service card: a bordered box with an icon tile, a name and a line under it,
 * its state as a glyph, and a row of chips (what it collects, how many send to it, when it last reported). A box that is not well is
 * tinted and says its state in words, so that is what the eye finds; the rest of what there is to know is in its tooltip (on hover and
 * on focus) and in the Inspector.
 */
export function PlatformNode({ entity, selected, label, senders, age, withRole, tip, tipAlign = 'left', className, style, ...handlers }: {
  entity: PlatformEntity
  selected?: boolean
  /** What a screen reader hears: the whole sentence, since the visible text is not all of it. */
  label: string
  /** How many parts send to it, for the chip on a shared operator. */
  senders?: number
  /** How long ago it last reported ("8 s"), when something knows. */
  age?: string
  /** In a list, where there is room and no line to say why: the reason beside the state, and where it sends to on a line of its own. */
  withRole?: boolean
  /** The tooltip: the part's own sentence, and what to do about it. Left out where the text is already all on show. */
  tip?: boolean
  tipAlign?: 'left' | 'right'
  className?: string
  style?: React.CSSProperties
  onClick: () => void
  onMouseEnter?: () => void
  onMouseLeave?: () => void
  onFocus?: () => void
  onBlur?: () => void
}) {
  const bad = entity.status === 'attention' || entity.status === 'down'
  const reason = withRole && bad ? entity.sentence.replace(/\.$/, '') : undefined
  // The reason beside the state already says when it was last heard from.
  const f = face(entity, senders, reason ? undefined : age)
  const Icon = PLATFORM_ICON[entity.kind]
  const meta = bad ? (reason ? `${PLATFORM_STATUS_WORD[entity.status]} \u00b7 ${reason}` : PLATFORM_STATUS_WORD[entity.status]) : f.meta
  const sub = reason ? f.meta : undefined
  return (
    <div className={clsx('group/node', className ?? 'relative')} style={style}>
      <button
        type="button"
        data-testid="platform-node"
        data-platform={entity.kind}
        data-platform-id={entity.id}
        aria-label={label}
        aria-pressed={selected}
        className={clsx(
          'flex h-full w-full flex-col justify-center gap-1.5 rounded-xl border px-2.5 py-1.5 text-left transition-[background-color,border-color] duration-150 motion-reduce:transition-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
          selected ? 'border-accent bg-nb-925 shadow-[inset_0_0_0_1px_var(--color-accent)]' : bad ? (entity.status === 'down' ? 'border-bad/35 bg-bad/5 hover:bg-bad/10' : 'border-warn/35 bg-warn/5 hover:bg-warn/10') : 'border-nb-800 bg-nb-925 hover:border-nb-700',
          entity.off && 'border-dashed bg-transparent',
        )}
        {...handlers}
      >
        <span className="flex w-full items-center gap-2">
          <span className={clsx('grid size-8 shrink-0 place-items-center rounded-lg', bad ? (entity.status === 'down' ? 'bg-bad/10 text-bad' : 'bg-warn/10 text-warn') : 'bg-nb-900 text-nb-400')}>
            <Icon size={ICON_SM} aria-hidden />
          </span>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[13px] font-semibold leading-[18px] text-nb-300" title={f.title}>{f.title}</span>
            {meta && <span className={clsx('block truncate text-[11px] leading-4', bad ? TONE[entity.status] : 'text-nb-500')} title={meta}>{meta}</span>}
            {sub && <span className="block truncate text-[11px] leading-4 text-nb-500">{sub}</span>}
          </span>
          {!entity.off && <StatusGlyph status={entity.status} className="self-start" />}
        </span>
        {f.chips.length > 0 && (
          <span className="flex w-full items-center gap-[3px] overflow-hidden" data-testid="chips">
            {f.chips.map((c) => (
              <span key={c.text} title={c.title} data-signal={c.icon && !c.tone ? c.text.toLowerCase() : undefined} className={clsx('inline-flex shrink-0 items-center gap-[3px] whitespace-nowrap rounded px-1 py-px text-[10.5px] leading-4', CHIP_TONE[c.tone ?? 'none'])}>
                {c.icon && <c.icon size={10} aria-hidden />}
                {c.text}
              </span>
            ))}
          </span>
        )}
      </button>
      {tip && (
        <span
          role="presentation"
          aria-hidden
          className={clsx(
            'pointer-events-none invisible absolute top-full z-30 mt-1.5 block w-60 rounded-lg border border-nb-800 bg-nb-900 p-3 text-left opacity-0 shadow-md transition-opacity delay-300 duration-150 motion-reduce:transition-none',
            'group-hover/node:visible group-hover/node:opacity-100 group-has-[:focus-visible]/node:visible group-has-[:focus-visible]/node:opacity-100',
            tipAlign === 'right' ? 'right-0' : 'left-0',
          )}
        >
          <span className="block text-[11px] font-medium uppercase tracking-wide text-nb-500">{KIND_WORD[entity.kind]}{entity.clusterName ? ` · ${entity.clusterName}` : ''}</span>
          <span className={clsx('mt-1 block text-xs', entity.status === 'attention' ? 'text-warn' : entity.status === 'down' ? 'text-bad' : 'text-nb-300')}>{entity.off ? 'Not turned on.' : entity.sentence}</span>
          {platformFacts(entity) && <span className="mt-1 block text-xs text-nb-500">{platformFacts(entity)}</span>}
          {entity.todo && <span className="mt-1 block text-xs text-nb-500">{entity.todo}</span>}
        </span>
      )}
    </div>
  )
}

import clsx from 'clsx'
import { Activity, Cable, Database, DoorOpen, FileText, Funnel, Merge, Waypoints, type LucideIcon } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { StatusMark } from '@/components/topology/StatusMark'
import { MODALITY_WORD, PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformKind, type PlatformStatus } from '@/lib/platformLayer'
import type { Modality } from '@/lib/install'

export const PLATFORM_ICON: Record<PlatformKind, LucideIcon> = { agent: Cable, local: Funnel, regional: Merge, central: DoorOpen, fusion: Database }
export const KIND_WORD: Record<PlatformKind, string> = { agent: 'Discovery agent', local: 'Local operator', regional: 'Regional operator', central: 'Central operator', fusion: 'FUSION' }
const SIGNAL_ICON: Record<Modality, LucideIcon> = { metrics: Activity, logs: FileText, traces: Waypoints }

const TONE: Record<PlatformStatus, string> = { healthy: 'text-ok', attention: 'text-warn', down: 'text-bad', unknown: 'text-nb-500' }

/** The state as the canvas draws it (the same StatusMark: a dot, a triangle, a crossed circle, a ring), so a state reads the same in every layer. */
export function StatusGlyph({ status, className }: { status: PlatformStatus; className?: string }) {
  return <StatusMark state={status} className={className} />
}

/** What a part collects or keeps, as small glyphs instead of a sentence that has to be cut short: the words are in its tooltip and its name. */
function Signals({ of }: { of: Modality[] }) {
  return (
    <span className="flex h-[18px] items-center gap-1.5 text-nb-500" data-testid="signals">
      {of.map((m) => {
        const Icon = SIGNAL_ICON[m]
        return <Icon key={m} size={ICON_SM} aria-hidden />
      })}
    </span>
  )
}

/**
 * What a box says about itself. In a column the column's header already names the role, so a box leads with what is specific to it: an
 * agent its version, a local operator what it collects, a shared operator its name and how many send to it. In a list there are no
 * columns, so the role leads. Whatever is not Healthy takes the second line, in words.
 */
function face(e: PlatformEntity, senders: number | undefined, withRole: boolean): { title?: string; /** Not a name (a version): set quieter. */ muted?: boolean; meta?: string; signals?: Modality[] } {
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
  const all: Modality[] = ['metrics', 'logs', 'traces']
  if (withRole && (e.kind === 'agent' || e.kind === 'local')) {
    const to = e.sendsTo.length ? ` → ${e.sendsTo.length === 1 ? e.sendsTo[0].name : plural(e.sendsTo.length, 'destination', 'destinations')}` : ''
    return { title: KIND_WORD[e.kind], meta: e.kind === 'local' ? `${(e.collecting ?? []).map((m) => MODALITY_WORD[m]).join(', ') || e.detail}${to}` : e.detail }
  }
  switch (e.kind) {
    case 'agent': return { title: e.detail, muted: true }
    case 'local': return e.collecting?.length ? { signals: e.collecting } : { title: e.detail }
    case 'regional': return { title: e.name, meta: [withRole ? 'Regional operator' : '', senders ? plural(senders, 'cluster', 'clusters') : ''].filter(Boolean).join(' · ') || undefined }
    case 'central': return { title: e.name, meta: senders ? plural(senders, 'operator', 'operators') : undefined }
    case 'fusion': return e.off ? { title: e.name, meta: 'Off' } : { title: e.name, signals: all }
  }
}

/**
 * A part of the telemetry platform: one calm box, the same anatomy as a cluster's header and a service card. A name or what is specific to
 * it, at most one muted line under it, and its state as a single glyph; everything else is in its tooltip (on hover and on focus) and in
 * the Inspector. A box that is not well is tinted, so that is what the eye finds.
 */
export function PlatformNode({ entity, selected, label, senders, withRole, tip, tipAlign = 'left', className, style, ...handlers }: {
  entity: PlatformEntity
  selected?: boolean
  /** What a screen reader hears: the whole sentence, since the visible text is not all of it. */
  label: string
  /** How many parts send to it, for the line under a shared operator. */
  senders?: number
  /** In a list, where no column header names the role: lead with it. */
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
  const f = face(entity, senders, !!withRole)
  const bad = entity.status === 'attention' || entity.status === 'down'
  const meta = bad ? PLATFORM_STATUS_WORD[entity.status] : f.meta
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
          'flex h-full w-full items-center gap-2 rounded-xl border px-3 text-left transition-[background-color,border-color] duration-150 motion-reduce:transition-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
          selected ? 'border-accent bg-nb-925 shadow-[inset_0_0_0_1px_var(--color-accent)]' : bad ? (entity.status === 'down' ? 'border-bad/35 bg-bad/5 hover:bg-bad/10' : 'border-warn/35 bg-warn/5 hover:bg-warn/10') : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
          entity.off && 'border-dashed bg-transparent',
        )}
        {...handlers}
      >
        <span className="min-w-0 flex-1">
          {f.title !== undefined && <span className={clsx('block truncate text-[13px] leading-[18px]', f.muted ? 'font-medium text-nb-400' : 'font-semibold text-nb-300')}>{f.title}</span>}
          {f.signals && !(f.title !== undefined && meta) && <Signals of={f.signals} />}
          {meta && <span className={clsx('block truncate text-[11px] leading-4', bad ? TONE[entity.status] : 'text-nb-500')}>{meta}</span>}
        </span>
        {!entity.off && <StatusGlyph status={entity.status} />}
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
          {entity.todo && <span className="mt-1 block text-xs text-nb-500">{entity.todo}</span>}
        </span>
      )}
    </div>
  )
}

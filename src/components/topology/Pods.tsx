import { useStore } from '@xyflow/react'
import clsx from 'clsx'
import { RotateCw } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { CRASH_RESTARTS, HEALTHY_LISTED, needsAttention, type PodNodeGroup, type PodRow, type PodsView, type PodState } from '@/lib/pods'

const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

const CELL: Record<PodState, string> = {
  ready: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  // Hollow: nothing is running yet, so nothing to colour.
  unscheduled: 'border-[1.5px] border-nb-500',
}

/** One pod as a small rounded cell, a different shape from the round status dot above it. A young pod
 *  (a scaling event) carries a small pip, `info` rather than `accent` (already selection and advice). */
export function Cell({ pod, far, title }: { pod: Pick<PodRow, 'state' | 'recent'>; far?: boolean; title?: string }) {
  return (
    <span title={title} className={clsx('relative shrink-0 rounded-[3px]', far ? 'h-[22px] w-4' : 'h-3.5 w-2.5', CELL[pod.state])}>
      {pod.recent && <span className={clsx('absolute rounded-full bg-info ring-1 ring-nb-925', far ? '-right-1 -top-1 size-2' : '-right-[3px] -top-[3px] size-[5px]')} />}
    </span>
  )
}

function Chip({ tone, children, title }: { tone: string; children: ReactNode; title?: string }) {
  return <span className={clsx('inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-px text-[10.5px]', tone)} title={title}>{children}</span>
}

/** The card's pod row: a cell per pod (at most RAIL_LIMIT, then "+N") and one summary, "27/30 ready". Also the
 *  button that opens the popover. Drawn in the far presentation too, larger, so it is visible at the fit zoom. */
export function PodRail({ pods, far, open, onToggle, buttonRef }: { pods: PodsView; far: boolean; open: boolean; onToggle: () => void; buttonRef: React.Ref<HTMLButtonElement> }) {
  const tone = pods.bad > 0 ? 'text-bad' : pods.ready < pods.total ? 'text-warn' : 'text-nb-500'
  return (
    <button
      ref={buttonRef}
      type="button"
      onClick={(e) => { e.stopPropagation(); onToggle() }}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={`${pods.ready} of ${plural(pods.total, 'pod')} ready${pods.bad ? `, ${pods.bad} crash-looping` : ''}. Show the pods`}
      className="nodrag flex w-full items-center gap-[3px] rounded text-left outline-none focus-visible:ring-2 focus-visible:ring-accent"
      data-testid="pod-rail"
    >
      {pods.rail.map((p) => <Cell key={p.id} pod={p} far={far} title={p.title} />)}
      {pods.overflow > 0 && <span className={clsx('ml-0.5 text-nb-500', far ? 'text-[15px]' : 'text-[10.5px]')} title={`${plural(pods.overflow, 'more pod')} not drawn`}>+{pods.overflow}</span>}
      <span className={clsx('ml-auto shrink-0 whitespace-nowrap pl-2', far ? 'text-[18px]' : 'text-[11px]', tone)}>{pods.ready}/{pods.total} ready</span>
    </button>
  )
}

function Row({ pod, open, onToggle }: { pod: PodRow; open: boolean; onToggle: () => void }) {
  const traffic = !!pod.traffic?.length
  const Tag = traffic ? 'button' : 'div'
  return (
    <Tag
      {...(traffic ? { type: 'button' as const, onClick: onToggle, 'aria-expanded': open } : {})}
      title={pod.title}
      data-testid="pod-row"
      className={clsx('flex w-full items-center gap-2 rounded px-1 py-1 text-left', traffic && 'cursor-pointer hover:bg-nb-900', open && 'bg-nb-900')}
    >
      <Cell pod={pod} />
      <span className="min-w-0 truncate font-mono text-[11px] text-nb-300">{pod.label}</span>
      {pod.restarts > 0 && (
        <Chip tone={pod.state === 'bad' ? 'bg-bad/10 text-bad' : 'bg-nb-900 text-nb-400'} title={plural(pod.restarts, 'restart')}>
          <RotateCw size={10} aria-hidden="true" />
          {pod.restarts}<span className="sr-only"> restarts</span>
        </Chip>
      )}
      {pod.phase && <Chip tone="bg-warn/10 text-warn">{pod.phase}</Chip>}
      {pod.recent && <Chip tone="bg-info/10 text-info" title="Recently added (scaling)">new</Chip>}
      <span className="ml-auto shrink-0 text-[10.5px] text-nb-500">{pod.age}</span>
    </Tag>
  )
}

function Group({ group, expanded, onExpand, openPod, onPod }: { group: PodNodeGroup; expanded: boolean; onExpand: () => void; openPod: string | null; onPod: (id: string) => void }) {
  const flagged = group.pods.filter(needsAttention)
  const healthy = group.pods.filter((p) => !needsAttention(p))
  const listed = expanded ? healthy : healthy.slice(0, HEALTHY_LISTED)
  const more = healthy.length - listed.length
  return (
    <div>
      {group.nodeId ? (
        <button type="button" data-select-node={group.nodeId} className="truncate px-1 text-[11px] text-info underline-offset-2 hover:underline" title={group.nodeName}>{group.nodeName}</button>
      ) : (
        <span className="block truncate px-1 text-[11px] text-nb-500">{group.nodeName}</span>
      )}
      {[...flagged, ...listed].map((p) => <Row key={p.id} pod={p} open={openPod === p.id} onToggle={() => onPod(p.id)} />)}
      {more > 0 && (
        <button type="button" onClick={onExpand} className="px-1 py-1 text-[11px] text-nb-500 hover:text-nb-300 hover:underline">+{more} more healthy</button>
      )}
    </div>
  )
}

/** The pods of one service: header summary, rows by node (what needs a look first), one pod's own traffic on
 *  request. Drawn in a portal at a fixed screen position, so no card or box can paint over it and zooming the
 *  canvas does not shrink it; it closes on Escape, a click elsewhere, or any pan/zoom (it would no longer
 *  sit next to its card). */
export function PodPopover({ pods, name, anchor, onClose }: { pods: PodsView; name: string; anchor: HTMLElement; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number }>()
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const [openPod, setOpenPod] = useState<string | null>(null)
  const transform = useStore((s) => s.transform)
  const first = useRef(transform)
  useEffect(() => { if (transform !== first.current) onClose() }, [transform, onClose])

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const a = anchor.getBoundingClientRect()
    const { offsetWidth: w, offsetHeight: h } = el
    const below = a.bottom + 6 + h <= innerHeight - 8 || a.top - 6 - h < 8
    setPos({ left: Math.max(8, Math.min(a.left, innerWidth - w - 8)), top: Math.max(8, below ? a.bottom + 6 : a.top - 6 - h) })
  }, [anchor])

  useEffect(() => {
    ref.current?.focus({ preventScroll: true })
    const away = (e: PointerEvent) => { if (!ref.current?.contains(e.target as Node) && !anchor.contains(e.target as Node)) onClose() }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); onClose(); anchor.focus() } }
    document.addEventListener('pointerdown', away, true)
    document.addEventListener('keydown', key, true)
    addEventListener('resize', onClose)
    return () => { document.removeEventListener('pointerdown', away, true); document.removeEventListener('keydown', key, true); removeEventListener('resize', onClose) }
  }, [anchor, onClose])

  const selected = pods.groups.flatMap((g) => g.pods).find((p) => p.id === openPod)
  return createPortal(
    <div
      ref={ref}
      role="dialog"
      aria-label={`Pods of ${name}`}
      tabIndex={-1}
      data-testid="pod-popover"
      style={{ left: pos?.left, top: pos?.top, visibility: pos ? 'visible' : 'hidden' }}
      // Only a node-name button should reach the canvas's own click handler (it selects that node).
      onClick={(e) => { if (!(e.target as Element).closest('[data-select-node]')) e.stopPropagation() }}
      className="fixed z-50 flex max-h-[min(420px,calc(100vh-16px))] w-[min(340px,calc(100vw-16px))] flex-col rounded-xl border border-nb-800 bg-nb-920 text-xs shadow-xl outline-none"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-nb-850 px-3 py-2">
        <span className="font-medium text-nb-300">{pods.ready}/{pods.total} ready{pods.nodes > 0 && ` · ${plural(pods.nodes, 'node')}`}</span>
        {pods.warn > 0 && <Chip tone="bg-warn/10 text-warn">{pods.warn} not ready</Chip>}
        {pods.bad > 0 && <Chip tone="bg-bad/10 text-bad">{pods.bad} crash-looping</Chip>}
      </div>
      <div className="flex min-h-0 max-h-[280px] flex-col gap-2 overflow-y-auto overscroll-contain p-2">
        {pods.groups.map((g) => (
          <Group
            key={g.nodeId || 'unscheduled'}
            group={g}
            expanded={expanded.has(g.nodeId)}
            onExpand={() => setExpanded((s) => new Set(s).add(g.nodeId))}
            openPod={openPod}
            onPod={(id) => setOpenPod((v) => (v === id ? null : id))}
          />
        ))}
      </div>
      {selected?.traffic?.length ? (
        <div className="flex flex-col gap-1 border-t border-nb-850 px-3 py-2" data-testid="pod-traffic">
          <span className="truncate font-mono text-[10.5px] text-nb-400">{selected.label}'s own traffic right now</span>
          {selected.traffic.map((t, i) => (
            <div key={i} className="flex items-center gap-1 truncate text-[10.5px] text-nb-300">
              <span className="shrink-0 text-nb-500">{t.direction === 'out' ? '→' : '←'}</span>
              <span className="truncate" title={t.peer}>{t.peer}</span>
              <span className="shrink-0 text-nb-500">:{t.port}/{t.protocol}</span>
              <span className="ml-auto shrink-0 text-nb-500">{t.connections} conn</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>,
    document.body,
  )
}

/** The legend entry: what a cell's colour and pip mean. */
export function PodLegend() {
  const items: [Pick<PodRow, 'state' | 'recent'>, string][] = [
    [{ state: 'ready', recent: false }, 'ready'],
    [{ state: 'warn', recent: false }, 'not ready'],
    [{ state: 'bad', recent: false }, `crash-looping (${CRASH_RESTARTS}+ restarts)`],
    [{ state: 'unscheduled', recent: false }, 'no node yet'],
    [{ state: 'ready', recent: true }, 'new (scaling)'],
  ]
  return (
    <>
      <span className="text-nb-500">Pods</span>
      {items.map(([pod, label]) => (
        <span key={label} className="flex items-center gap-1.5"><Cell pod={pod} />{label}</span>
      ))}
    </>
  )
}

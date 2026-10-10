import clsx from 'clsx'
import type { ReactNode } from 'react'
import { ChevronLeft, ChevronRight, TriangleAlert } from 'lucide-react'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import { StatusMark } from '@/components/topology/StatusMark'
import { ICON_SM } from '@/components/ui/primitives'
import { unitOf, type Problem } from '@/lib/problems'

/**
 * "3 services need attention": one small segmented control, the same on every layer, only while something is wrong, tinted by the worst of it
 * (red when anything is not working, amber when it only needs attention). It counts places, each once, and says what it counts: services, nodes,
 * clusters, or "places" when the problems are of several kinds. The label takes you to the problem (the first, or the selected one again); the
 * chevrons walk to the previous and next, worst first. A single problem is only the label. "2 of 3" says where the selection is, and N and
 * Shift+N do the same from the keyboard.
 */
export function ProblemsPill({ problems, selectedId, onGo, onShow, hint }: { problems: Problem[]; selectedId: string | null; onGo: (back: boolean) => void; onShow: () => void; /** What is being counted, where it is not the canvas's boxes: said in the tooltip. */ hint?: string }) {
  if (!problems.length) return null
  const n = problems.length
  const at = problems.findIndex((p) => p.id === selectedId) + 1
  const worst = problems.some((p) => p.alert === 'bad') ? 'bad' : 'warn'
  const unit = unitOf(problems)
  const words = `${n} ${unit}${n === 1 ? '' : 's'} ${n === 1 ? 'needs' : 'need'} attention`
  const tint = worst === 'bad' ? 'border-bad/40 bg-bad/10' : 'border-warn/40 bg-warn/10'
  const step = 'px-2 text-nb-400 hover:bg-nb-100/10 hover:text-nb-100 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:grid pointer-coarse:place-items-center'
  return (
    <div className={clsx('flex items-stretch overflow-hidden rounded-lg border text-xs backdrop-blur-sm', tint)} data-testid="problems-pill" data-worst={worst}>
      {n > 1 && (
        <button type="button" onClick={() => onGo(true)} className={clsx(step, 'border-r', worst === 'bad' ? 'border-bad/25' : 'border-warn/25', PRESS_CLASS)} aria-label="Previous problem" title="Previous (Shift+N)">
          <ChevronLeft size={ICON_SM} aria-hidden />
        </button>
      )}
      <button
        type="button"
        onClick={onShow}
        className={clsx('flex items-center gap-1.5 px-2.5 py-1.5 text-nb-200 hover:text-nb-100', PRESS_CLASS)}
        aria-label={`${words}. Go to ${at ? 'it again' : n === 1 ? 'it' : 'the first one'}.`}
        title={`${n === 1 ? 'Take me to it' : 'Take me to the worst one (N: next, Shift+N: previous)'}. Counts the ${unit}s in this layer that need attention or are not working, each once: ${hint ?? 'a cluster whose cards show the trouble is not counted again'}.`}
      >
        <TriangleAlert size={ICON_SM} className={worst === 'bad' ? 'text-bad' : 'text-warn'} aria-hidden />
        <span className="tabular-nums">{at && n > 1 ? `${at} of ${words}` : words}</span>
      </button>
      {n > 1 && (
        <button type="button" onClick={() => onGo(false)} className={clsx(step, 'border-l', worst === 'bad' ? 'border-bad/25' : 'border-warn/25', PRESS_CLASS)} aria-label="Next problem" title="Next (N)">
          <ChevronRight size={ICON_SM} aria-hidden />
        </button>
      )}
    </div>
  )
}

/** The keys that walk the problems, said where the pill is: quiet, and only where there is a keyboard to press them on. */
export function WalkHint({ className }: { className?: string }) {
  const key = 'rounded border border-nb-800 px-1 text-[10.5px] leading-4 text-nb-400'
  return (
    <span className={clsx('hidden items-center gap-1.5 text-[11px] text-nb-500 sm:flex pointer-coarse:hidden', className)} aria-hidden>
      <kbd className={key}>N</kbd> next <kbd className={key}>Shift N</kbd> previous
    </span>
  )
}

/**
 * The thin line under the toolbar on the Application and Infrastructure tabs, where the Telemetry tab has its summary: the answer to "is anything
 * wrong?" before anything else on the screen, in the same place whatever the layer. The pill when something is, "No problems" when not.
 */
export function CanvasBar({ problems, selectedId, onGo, onShow, trailing }: { problems: Problem[]; selectedId: string | null; onGo: (back: boolean) => void; onShow: () => void; trailing?: ReactNode }) {
  return (
    <div className="flex min-h-12 shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-nb-850 px-4 py-2 sm:px-6" data-testid="canvas-status">
      {problems.length > 0 ? (
        <ProblemsPill problems={problems} selectedId={selectedId} onGo={onGo} onShow={onShow} />
      ) : (
        <p className="flex min-h-8 items-center gap-2 text-[13px] text-nb-300"><StatusMark state="healthy" /> No problems</p>
      )}
      <div className="ml-auto flex flex-wrap items-center gap-x-4 gap-y-2">
        {trailing}
        {problems.length > 1 && <WalkHint />}
      </div>
    </div>
  )
}

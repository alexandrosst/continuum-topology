import clsx from 'clsx'
import { ChevronLeft, ChevronRight, TriangleAlert } from 'lucide-react'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import { ICON_SM } from '@/components/ui/primitives'
import type { Problem } from '@/lib/problems'

/**
 * "3 need attention": one small segmented control on the Calm canvas, only while something is wrong, tinted by the worst of it (red when
 * anything is broken, amber when it only needs a look). The label takes you to the problem (the first, or the selected one again); the
 * chevrons walk to the previous and next, worst first. A single problem is only the label. "2 of 3" says where the selection is, and N and
 * Shift+N do the same from the keyboard.
 */
export function ProblemsPill({ problems, selectedId, onGo, onShow, hint }: { problems: Problem[]; selectedId: string | null; onGo: (back: boolean) => void; onShow: () => void; /** What is being counted, where it is not the canvas's boxes: said in the tooltip. */ hint?: string }) {
  if (!problems.length) return null
  const n = problems.length
  const at = problems.findIndex((p) => p.id === selectedId) + 1
  const worst = problems.some((p) => p.alert === 'bad') ? 'bad' : 'warn'
  const words = `${n} ${n === 1 ? 'needs' : 'need'} attention`
  const tint = worst === 'bad' ? 'border-bad/40 bg-bad/10' : 'border-warn/40 bg-warn/10'
  const step = 'px-1.5 text-nb-400 hover:bg-nb-100/10 hover:text-nb-100'
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
        title={`${n === 1 ? 'Take me to it' : 'Take me to the worst one (N: next, Shift+N: previous)'}. Counts places, each once: ${hint ?? 'a cluster whose cards show the trouble is not counted again'}.`}
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

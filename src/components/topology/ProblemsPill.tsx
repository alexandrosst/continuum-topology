import clsx from 'clsx'
import { ChevronLeft, TriangleAlert } from 'lucide-react'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import { ICON_SM } from '@/components/ui/primitives'
import type { Problem } from '@/lib/problems'

/**
 * "3 need attention": one small pill on the Calm canvas, only while something is wrong. Each press takes the next problem (worst first) to
 * the middle of the canvas and selects it; Shift+press, or the chevron, goes back. "2 of 3" says where in the list the selection is.
 */
export function ProblemsPill({ problems, selectedId, onGo }: { problems: Problem[]; selectedId: string | null; onGo: (back: boolean) => void }) {
  if (!problems.length) return null
  const n = problems.length
  const at = problems.findIndex((p) => p.id === selectedId) + 1
  const worst = problems.some((p) => p.alert === 'bad') ? 'bad' : 'warn'
  const words = `${n} ${n === 1 ? 'needs' : 'need'} attention`
  return (
    <div className="flex items-stretch overflow-hidden rounded-lg border border-nb-850 bg-nb-925/95 text-xs" data-testid="problems-pill">
      <button
        type="button"
        onClick={(e) => onGo(e.shiftKey)}
        className={clsx('flex items-center gap-1.5 px-2.5 py-1.5 text-nb-300 hover:text-nb-100', PRESS_CLASS)}
        aria-label={`${words}. Go to the ${at ? 'next' : 'first'} one. Shift and press for the previous.`}
        title="Take me to the next one (Shift: the previous)"
      >
        <TriangleAlert size={ICON_SM} className={worst === 'bad' ? 'text-bad' : 'text-warn'} aria-hidden />
        <span className="tabular-nums">{at ? `${at} of ${words}` : words}</span>
      </button>
      {n > 1 && (
        <button
          type="button"
          onClick={() => onGo(true)}
          className={clsx('border-l border-nb-850 px-1.5 text-nb-500 hover:text-nb-300', PRESS_CLASS)}
          aria-label="Previous problem"
          title="Previous"
        >
          <ChevronLeft size={ICON_SM} aria-hidden />
        </button>
      )}
    </div>
  )
}

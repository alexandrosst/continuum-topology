import clsx from 'clsx'
import { Check } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import type { SetupStep } from '@/features/pipeline/lib/operatorsView'

/** Where a new component is on its way to its first data: what is done, the one thing it is waiting on (pulsing, so a quiet page still looks
 *  alive) and what comes after. Shown under the "Setting up" chip; the words carry the state, the marks are a second channel. */
export default function SetupSteps({ steps, className }: { steps: SetupStep[]; className?: string }) {
  return (
    <ol className={clsx('flex flex-wrap gap-x-4 gap-y-1', className)} aria-label="Setting up" data-testid="setup-steps">
      {steps.map((s) => (
        <li key={s.label} className={clsx('flex items-center gap-1.5 text-xs', s.state === 'todo' ? 'text-nb-500' : 'text-nb-300')} data-step={s.state} title={s.detail}>
          {s.state === 'done' ? (
            <Check size={ICON_SM} className="shrink-0 text-ok" aria-hidden />
          ) : s.state === 'now' ? (
            <span className="inline-flex size-3.5 shrink-0 items-center justify-center rounded-full border border-info motion-safe:animate-pulse" aria-hidden><span className="size-1.5 rounded-full bg-info" /></span>
          ) : (
            <span className="inline-block size-3.5 shrink-0 rounded-full border border-nb-700" aria-hidden />
          )}
          <span>{s.label}<span className="sr-only">{s.state === 'done' ? ', done' : s.state === 'now' ? ', waiting for this' : ', not yet'}</span></span>
        </li>
      ))}
    </ol>
  )
}

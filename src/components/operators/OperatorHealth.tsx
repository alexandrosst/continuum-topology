import clsx from 'clsx'
import { PulseDot } from '@/components/ui/primitives'
import { operatorLiveness } from '@/lib/operatorHealth'
import type { RegionalOperator } from '@/lib/types'

/**
 * One operator's health as a dot and a sentence. The words carry the meaning on their own - Online, Offline
 * with how long ago, or "Health not reported" - and the dot only repeats it: filled blue for online, hollow
 * orange for offline, hollow grey for not reported (shape as well as colour, and blue/orange rather than
 * green/red, as everywhere else a choice exists). Not reported is deliberately neutral: it is not a fault.
 */
export function OperatorHealth({ operator, className, testId = 'operator-health' }: { operator: Pick<RegionalOperator, 'health'>; className?: string; testId?: string }) {
  const l = operatorLiveness(operator)
  return (
    <span className={clsx('inline-flex items-center gap-1.5 text-xs', l.kind === 'unreported' ? 'text-nb-500' : 'text-nb-400', className)} data-testid={testId} data-health={l.kind}>
      {l.kind === 'online' ? (
        <PulseDot color="bg-info" size="size-1.5" />
      ) : (
        <span className={clsx('inline-block size-1.5 shrink-0 rounded-full border', l.kind === 'offline' ? 'border-warn' : 'border-nb-500')} aria-hidden />
      )}
      <span>{l.text}</span>
    </span>
  )
}

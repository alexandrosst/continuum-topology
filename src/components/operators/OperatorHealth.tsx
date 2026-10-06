import clsx from 'clsx'
import { LiveDot, type LiveKind } from '@/components/ui/primitives'
import { operatorLiveness, type OperatorLiveness } from '@/lib/operatorHealth'
import type { RegionalOperator } from '@/lib/types'

/** The dot for each liveness: the product's one vocabulary (LiveDot). Waiting is the hollow fading ring, like starting - the only
 *  spinner on a page is the one Waiting component, so a table of operators never has several. */
export const LIVENESS_DOT: Record<OperatorLiveness['kind'], LiveKind> = {
  online: 'online',
  offline: 'offline',
  waiting: 'starting',
  starting: 'starting',
  attention: 'late',
  off: 'idle',
  unreported: 'idle',
}

/**
 * One operator's health as a dot and a sentence. The words carry the meaning on their own - Online, Offline
 * with how long ago, Waiting for the first heartbeat, or "Health not reported" - and the dot only repeats it, in the product's
 * one liveness vocabulary: green and pulsing for online, red for offline, amber for needs attention, a hollow fading ring for
 * on its way, hollow grey for not reported or off. Not reported is deliberately neutral: it is not a fault.
 */
export function OperatorHealth({ operator, central, className, testId = 'operator-health' }: { operator: Pick<RegionalOperator, 'health'>; central?: boolean; className?: string; testId?: string }) {
  const l = operatorLiveness(operator, Date.now(), { central })
  return (
    <span className={clsx('inline-flex items-center gap-1.5 text-xs', l.kind === 'unreported' || l.kind === 'off' ? 'text-nb-500' : 'text-nb-400', className)} data-testid={testId} data-health={l.kind}>
      <LiveDot kind={LIVENESS_DOT[l.kind]} />
      <span>{l.text}</span>
    </span>
  )
}

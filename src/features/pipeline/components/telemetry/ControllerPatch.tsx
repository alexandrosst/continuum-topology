import { Loader2 } from 'lucide-react'
import { CopyCommand } from '@/components/ui/CopyCommand'
import { ICON_SM } from '@/components/ui/primitives'
import type { UpgradeCommand } from '@/lib/agentUpgrade'

/**
 * The telemetry change as a patch of the IkhnosAgent resource, for an agent a controller keeps. A helm upgrade of such a release does not
 * last: the controller installs the chart again from the resource, with only what the resource says. The server writes the patch from the
 * same request as the helm command (the `controller` block of its answer); this shows it, or says why the resource cannot state the
 * settings.
 */
export function ControllerPatch({ command, testId = 'controller-patch' }: { command: UpgradeCommand; testId?: string }) {
  if (command.status === 'idle') return null
  if (command.status === 'waiting') {
    return (
      <p role="status" className="mt-1 flex items-center gap-1.5 text-nb-500" data-testid={`${testId}-pending`}>
        <Loader2 size={ICON_SM} className="animate-spin" aria-hidden /> Writing the command…
      </p>
    )
  }
  if (command.status === 'failed') {
    return (
      <p role="alert" className="mt-1 text-bad" data-testid={`${testId}-error`}>
        The command could not be written: {command.message}
      </p>
    )
  }
  const c = command.answer.controller
  // Problems with the draft itself are the helm command's too, and are said once, where the command would be.
  const problems = command.answer.problems.length > 0 ? command.answer.problems : c.problems
  return (
    <div className="mt-1 space-y-2" data-testid={testId}>
      <p className="text-nb-400" data-testid={`${testId}-why`}>
        For an agent that a controller keeps. The controller installs the agent from its <code className="font-mono text-nb-300">IkhnosAgent</code> resource and puts back anything
        a <code className="font-mono text-nb-300">helm upgrade</code> changes, so the telemetry is patched into the resource (named <code className="font-mono text-nb-300">this-cluster</code>)
        instead.
      </p>
      {problems.length > 0 ? (
        <p role="status" className="text-warn" data-testid={`${testId}-problems`}>
          There is no patch yet: {problems[0]}
          {problems.length > 1 ? ` (and ${problems.length - 1} more)` : ''}.
        </p>
      ) : c.command === '' ? (
        <p role="status" className="text-nb-400" data-testid={`${testId}-nothing`}>
          There is nothing to change in the resource.
        </p>
      ) : (
        <>
          <CopyCommand text={c.command} testId={`${testId}-command`} />
          {c.needsTelemetryPermission && (
            <p className="text-nb-400" data-testid={`${testId}-permission`}>
              The controller must have been given the permissions telemetry needs: its chart&apos;s <code className="font-mono text-nb-300">allowTelemetry</code> value is on. Without it the
              controller refuses the resource and says so in its status.
            </p>
          )}
        </>
      )}
    </div>
  )
}

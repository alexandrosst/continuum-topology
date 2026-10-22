import { Loader2 } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { CopyCommand } from '@/components/ui/CopyCommand'
import type { UpgradeCommand } from '@/lib/agentUpgrade'

/**
 * A command the server writes (see useAgentUpgradeCommand), shown as it stands: the command once it is written for what is on screen,
 * and until then not the last one, which would be wrong now.
 */
export function ServerCommand({ command, testId = 'helm-command', label }: { command: UpgradeCommand; testId?: string; label?: string }) {
  switch (command.status) {
    case 'idle':
      return null
    case 'waiting':
      return (
        <p role="status" className="mt-1 flex items-center gap-1.5 text-nb-500" data-testid={`${testId}-pending`}>
          <Loader2 size={ICON_SM} className="animate-spin" aria-hidden /> Writing the command…
        </p>
      )
    case 'failed':
      return (
        <p role="alert" className="mt-1 text-bad" data-testid={`${testId}-error`}>
          The command could not be written: {command.message}
        </p>
      )
    case 'ready':
      // A draft the server cannot write a command from: said where the command would be, so nothing partial can be copied.
      return command.answer.problems.length > 0 ? (
        <p role="status" className="mt-1 text-warn" data-testid={`${testId}-problems`}>
          There is no command yet: {command.answer.problems[0]}
          {command.answer.problems.length > 1 ? ` (and ${command.answer.problems.length - 1} more)` : ''}.
        </p>
      ) : (
        <CopyCommand text={command.answer.command} testId={testId} label={label} />
      )
  }
}

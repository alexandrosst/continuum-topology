import { CopyCommand } from '@/components/agents/AgentInsight'
import { Button, Modal, RunStep } from '@/components/ui/primitives'
import type { ComponentRow, Todo } from '@/lib/operatorsView'

/**
 * What to do about one amber or red component: the sentence that says what is wrong, then numbered steps, each with the exact command to
 * run where the component is installed. When this page can do the last part itself (open the wizard, record an address, renew), that is
 * the one primary button.
 */
export default function WhatToDoModal({ row, todo, onClose, onAction, busy }: { row: ComponentRow; todo: Todo; onClose: () => void; onAction: (kind: NonNullable<Todo['action']>['kind']) => void; /** Why the button cannot be used right now (another renewal is running), if it cannot. */ busy?: string }) {
  const { action } = todo
  return (
    <Modal
      open
      onClose={onClose}
      title={`What to do about ${row.name}`}
      width="max-w-xl"
      footer={<><Button onClick={onClose}>Close</Button>{action && <Button variant="primary" onClick={() => onAction(action.kind)} disabled={!!busy} title={busy} data-testid="what-to-do-action">{action.label}</Button>}</>}
    >
      <p className="text-sm text-nb-400" data-testid="what-to-do-reason">{row.verdict.reason}</p>
      <ol className="mt-4 space-y-4" data-testid="what-to-do-steps">
        {todo.steps.map((s, i) => (
          <RunStep key={s.title} n={i + 1} title={s.title}>
            {s.text}
            {s.command && <CopyCommand text={s.command} label={`Copy the command for: ${s.title}`} testId={`what-to-do-command-${i + 1}`} />}
          </RunStep>
        ))}
      </ol>
    </Modal>
  )
}

import { useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { Button, ErrorBanner, Modal } from '@/components/ui/primitives'
import { api, ApiError, type OperatorRemoval } from '@/lib/api'
import type { OperatorUsedBy, RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'

/** What still depends on the operator, as a list of short lines. Empty when nothing does. */
export function usedByLines(u: OperatorUsedBy | undefined): string[] {
  if (!u) return []
  const out: string[] = []
  if (u.operators.length > 0) out.push(`${u.operators.length === 1 ? 'Operator' : 'Operators'} sending to it: ${u.operators.map((o) => o.name).join(', ')}`)
  if (u.clusters > 0) out.push(`${u.clusters} ${u.clusters === 1 ? 'cluster sends' : 'clusters send'} to it`)
  if (u.intents > 0) out.push(`${u.intents} telemetry ${u.intents === 1 ? 'request names' : 'requests name'} it`)
  return out
}

const COPY = {
  revoke: {
    title: (n: string) => `Disconnect ${n}?`,
    confirm: 'Disconnect',
    body: 'This only marks it disconnected here - there is no channel back to the deployed collector, so its receiver keeps accepting what it accepted before (a bearer token until you delete the Kubernetes Secret holding it, a client certificate until you uninstall the release). Its health reports are refused from now on. The record stays for the audit trail; use Stop and remove to take it off this list as well.',
    failure: 'Could not disconnect the operator.',
    done: 'Disconnected',
  },
  delete: {
    title: (n: string) => `Stop and remove ${n}?`,
    confirm: 'Stop and remove',
    body: 'Removes the record for good. If it is still connected, disconnect it first (or the source clusters keep exporting to a receiver that no longer exists).',
    failure: 'Could not remove the operator.',
    done: 'Removed',
  },
} as const

/**
 * Disconnecting or removing an operator, said plainly: what depends on it is listed, and going ahead while anything does needs an explicit
 * tick (the server's `force`), so nobody breaks a working path by clicking through. Neither action reaches the deployed collector, so
 * once it is done the command that removes it is shown - the server gives it, this page only displays it.
 */
export default function RemoveOperatorModal({ operator, mode, onClose, onDone }: { operator: RegionalOperator; mode: 'revoke' | 'delete'; onClose: () => void; onDone: () => void }) {
  const conn = useServer((s) => s.conn)
  const copy = COPY[mode]
  const [ack, setAck] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // The list may be older than the server's own check: a 409 answers with what it found, and the tick appears then.
  const [refused, setRefused] = useState(false)
  const [result, setResult] = useState<OperatorRemoval | null>(null)
  const lines = usedByLines(operator.usedBy)
  const needsAck = lines.length > 0 || refused

  const go = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      const r = mode === 'revoke' ? await api.revokeOperator(c, operator.id, 'revoked in the UI', ack) : await api.deleteOperator(c, operator.id, ack)
      onDone()
      if (r?.uninstall) setResult(r)
      else onClose()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setRefused(true)
      setError(e instanceof ApiError ? e.message : copy.failure)
    } finally {
      setBusy(false)
    }
  }

  if (result) {
    return (
      <Modal open onClose={onClose} title={`${copy.done} ${operator.name}`} width="max-w-xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-remove-done">Done</Button>}>
        <p className="text-sm text-nb-400" data-testid="operator-remove-uninstall-note">
          Its receiver keeps running in the cluster until you remove it. Run this where it is installed:
        </p>
        <CopyCommand text={result.uninstall!} testId="operator-uninstall-command" label="Copy the uninstall command" />
      </Modal>
    )
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={copy.title(operator.name)}
      width="max-w-md"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="danger" onClick={() => void go()} disabled={busy || (needsAck && !ack)} data-testid="operator-remove-confirm">{copy.confirm}</Button></>}
    >
      <p className="text-sm text-nb-400">{copy.body}</p>
      {lines.length > 0 && (
        <div className="mt-3 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" data-testid="operator-used-by">
          <div className="font-medium">Still in use</div>
          <ul className="mt-1 list-disc space-y-0.5 pl-4">{lines.map((l) => <li key={l}>{l}</li>)}</ul>
        </div>
      )}
      {needsAck && (
        <label className="mt-3 flex cursor-pointer items-start gap-2 text-sm">
          <input type="checkbox" className="mt-0.5 size-4 accent-[var(--color-accent)]" checked={ack} onChange={(e) => setAck(e.target.checked)} data-testid="operator-remove-force" />
          <span className="text-nb-300">I understand that what depends on it stops working</span>
        </label>
      )}
      {error && <ErrorBanner className="mt-3">{error}</ErrorBanner>}
    </Modal>
  )
}

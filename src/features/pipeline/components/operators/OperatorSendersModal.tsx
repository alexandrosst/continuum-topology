import { useEffect, useState } from 'react'
import { Button, ErrorBanner, Modal } from '@/components/ui/primitives'
import { api, ApiError } from '@/lib/api'
import type { OperatorSender, OperatorSenderAddress, OperatorSenders, RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'

const SOURCE: Record<OperatorSenderAddress['source'], string> = {
  chosen: 'chosen by this cluster',
  recorded: 'recorded for the operator',
  'in-cluster': 'the operator’s name inside its own cluster',
}

const WHERE: Record<NonNullable<OperatorSender['diagnosis']>['where'], string> = {
  'sender-network': 'The sender’s network',
  address: 'The address',
  certificate: 'The certificate',
  credential: 'The credentials',
  operator: 'The operator',
  sender: 'The sender',
  unclear: 'Not settled',
}

/** The delivery state in a word, and whether it is cause for concern. */
export function deliveryWord(s: OperatorSender): { text: string; tone: 'ok' | 'bad' | 'muted' } {
  if (!s.delivery) return { text: 'Not reported', tone: 'muted' }
  switch (s.delivery.state) {
    case 'exporting':
      return { text: 'Sending', tone: 'ok' }
    case 'failing':
      return { text: 'Failing', tone: 'bad' }
    case 'silent':
      return { text: 'Quiet', tone: 'muted' }
    default:
      return { text: 'Nothing sent yet', tone: 'muted' }
  }
}

const TONE = { ok: 'text-ok', bad: 'text-bad', muted: 'text-nb-400' } as const

/**
 * Which clusters send to a regional operator, the address each of them dials, and how delivery is going. Where a sender is not getting
 * through, it says which end the evidence points at: that cluster's network or address, its certificate, or the operator itself. The
 * evidence is what the server already holds (the sender's own export counters, and whether other senders reach the same operator), so
 * "the operator is up, another cluster reaches it, this one does not" is something it can say, and it says when it cannot tell.
 */
export default function OperatorSendersModal({ operator, onClose }: { operator: RegionalOperator; onClose: () => void }) {
  const conn = useServer((s) => s.conn)
  const [data, setData] = useState<OperatorSenders | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    const c = conn()
    if (!c) return
    let live = true
    api
      .operatorSenders(c, operator.id)
      .then((r) => live && setData(r))
      .catch((e) => {
        if (live) setError(e instanceof ApiError ? e.message : 'Could not read the clusters sending here.')
      })
    return () => {
      live = false
    }
  }, [conn, operator.id])

  return (
    <Modal open onClose={onClose} title={`Clusters sending to ${operator.name}`} width="max-w-3xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-senders-done">Done</Button>}>
      {error && <ErrorBanner>{error}</ErrorBanner>}
      {!error && data === null && <p className="text-sm text-nb-400">Reading…</p>}
      {data !== null && data.senders.length === 0 && (
        <p className="text-sm text-nb-400" data-testid="operator-senders-empty">No cluster is set up to send telemetry to this operator. Choose it as the destination in a cluster’s telemetry setup.</p>
      )}
      {data !== null && data.senders.length > 0 && (
        <ul className="space-y-3" data-testid="operator-senders">
          {data.senders.map((s) => {
            const w = deliveryWord(s)
            return (
              <li key={s.intentId} className="rounded-lg border border-nb-800 px-4 py-3" data-testid="operator-sender">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm font-medium text-nb-100" data-testid="operator-sender-name">{s.agentName || s.intentName}</span>
                  <span className={`text-xs ${TONE[w.tone]}`} data-testid="operator-sender-delivery">
                    {w.text}
                    {s.delivery?.state === 'exporting' && s.delivery.failed > 0 ? ` (${s.delivery.failed} failed so far)` : ''}
                  </span>
                </div>
                <ul className="mt-1.5 space-y-0.5 text-xs text-nb-400">
                  {s.addresses.map((a) => (
                    <li key={a.address} data-testid="operator-sender-address">
                      <span className="font-mono text-nb-200">{a.address}</span> · {SOURCE[a.source]} · {a.signals.join(', ')}
                      {a.reach && (
                        <span className={a.reach.state === 'reachable' ? 'text-nb-500' : 'text-bad'} data-testid="operator-sender-reach" data-state={a.reach.state} title={`Tested from ${s.agentName || 'this cluster'} at ${new Date(a.reach.at).toLocaleString()}`}>
                          {' · '}{a.reach.state === 'reachable' ? `answers from there${a.reach.rttP50Ms ? ` (${Math.round(a.reach.rttP50Ms)} ms)` : ''}` : 'no answer from there'}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
                {s.diagnosis && (
                  <div role="note" className="mt-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-nb-200" data-testid="operator-sender-diagnosis" data-where={s.diagnosis.where}>
                    <div className="font-medium text-warn">{WHERE[s.diagnosis.where]}: {s.diagnosis.title}</div>
                    <p className="mt-1">{s.diagnosis.detail}</p>
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Modal>
  )
}

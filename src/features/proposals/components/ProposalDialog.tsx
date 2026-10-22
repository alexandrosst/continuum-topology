import clsx from 'clsx'
import { useEffect, useState } from 'react'
import { Button, ErrorBanner, Input, Modal, Pill, Table, Td, Th } from '@/components/ui/primitives'
import { api, type Role } from '@/lib/api'
import { ago } from '@/lib/observed'
import {
  canAccept,
  canReject,
  changeLines,
  CLASS_WORDS,
  effectLines,
  OUTCOME_WORDS,
  outcomeRows,
  STATUS,
  timeLeft,
  type Proposal,
} from '@/lib/proposals'
import { useConn } from '@/store/server'

export const TONE_TEXT = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', muted: 'text-nb-400' } as const
const CLASS_TONE = { measured: 'text-ok', derived: 'text-nb-300', declared: 'text-warn', unknown: 'text-warn' } as const

export function StatusPill({ status }: { status: Proposal['status'] }) {
  const s = STATUS[status]
  return <Pill className={clsx('text-xs', TONE_TEXT[s.tone])}>{s.label}</Pill>
}

export function ConfidencePill({ confidence }: { confidence: Proposal['confidence'] }) {
  const c = CLASS_WORDS[confidence]
  return <Pill title={`The weakest evidence the advisor used. ${c.help}`} className={clsx('text-xs', CLASS_TONE[confidence])}>Evidence: {c.label.toLowerCase()}</Pill>
}

const stamp = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/**
 * One proposal in full: what it would change, why the advisor thinks so, what it expects, and, for a waiting one, the buttons to
 * decide. Deciding is the same act as changing the agent's access by hand (the server makes the change through the same code and
 * audits it the same way), so a widening needs an administrator and the buttons say so instead of failing afterwards.
 */
export default function ProposalDialog({
  proposal,
  agentName,
  role,
  onClose,
  onDecided,
}: {
  proposal: Proposal
  agentName: string
  role: Role | undefined
  onClose: () => void
  /** Called with the proposal as the server now has it, after a decision (and when the dialog learns a newer state). */
  onDecided: (p: Proposal) => void
}) {
  const conn = useConn()
  const [p, setP] = useState<Proposal>(proposal)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState<'accept' | 'reject' | null>(null)
  const [error, setError] = useState<string>()

  // The list leaves out the Outcome, and the proposal may have moved on since the list was read: read this one again.
  useEffect(() => {
    let live = true
    api.getProposal(conn, proposal.id).then(
      (full) => {
        if (!live) return
        setP(full)
        if (full.status !== proposal.status) onDecided(full)
      },
      () => {}, // the list's copy stays on screen; a decision will say if it is out of date
    )
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- read once per proposal opened
  }, [conn, proposal.id])

  const decide = async (accept: boolean) => {
    setBusy(accept ? 'accept' : 'reject')
    setError(undefined)
    try {
      const done = accept ? await api.acceptProposal(conn, p.id, note.trim()) : await api.rejectProposal(conn, p.id, note.trim())
      setP(done)
      onDecided(done)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The decision could not be made.')
      // Most refusals mean the proposal is no longer what was on screen (decided by someone else, expired, no longer valid): show it as it is.
      api.getProposal(conn, p.id).then((full) => {
        setP(full)
        onDecided(full)
      }, () => {})
    } finally {
      setBusy(null)
    }
  }

  const lines = changeLines(p.change, p.blast)
  const effects = effectLines(p.predicted ?? {})
  const waiting = p.status === 'pending'
  const mayAccept = canAccept(role, p)
  const mayReject = canReject(role, p)
  const widensWithoutRight = waiting && p.widens && !mayAccept

  return (
    <Modal
      open
      onClose={onClose}
      title={`${lines[0] ?? 'Change'} on ${agentName}`}
      width="max-w-2xl"
      footer={
        waiting ? (
          <>
            <Button onClick={onClose}>Close</Button>
            <Button onClick={() => void decide(false)} disabled={!mayReject || busy !== null} data-testid="proposal-reject">
              {busy === 'reject' ? 'Rejecting…' : 'Reject'}
            </Button>
            <Button
              variant="primary"
              onClick={() => void decide(true)}
              disabled={!mayAccept || busy !== null}
              title={widensWithoutRight ? 'Only an administrator can accept a change that makes an agent share more.' : undefined}
              data-testid="proposal-accept"
            >
              {busy === 'accept' ? 'Applying…' : 'Accept and apply'}
            </Button>
          </>
        ) : (
          <Button onClick={onClose}>Close</Button>
        )
      }
    >
      <div className="space-y-5 text-sm">
        {error && <ErrorBanner>{error}</ErrorBanner>}

        {lines.length > 1 && (
          <ul className="list-inside list-disc text-nb-300" data-testid="proposal-lines">
            {lines.map((l) => <li key={l}>{l}</li>)}
          </ul>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <StatusPill status={p.status} />
          <ConfidencePill confidence={p.confidence} />
          <Pill className="text-xs text-nb-400">{p.widens ? 'Shares more with the server' : 'Shares no more than before'}</Pill>
          <span className="text-xs text-nb-500">
            Proposed by <span className="text-nb-300">{p.advisor}</span> {ago(p.createdAt)}
            {waiting ? ` · expires in ${timeLeft(p.expiresAt)}` : ''}
          </span>
        </div>

        {widensWithoutRight && (
          <p className="rounded-md border border-warn/40 bg-warn-soft px-3 py-2 text-warn" data-testid="proposal-needs-admin">
            This makes the agent share more with the server than it does now, so only an administrator can accept it. You can still reject it.
          </p>
        )}

        <section aria-label="Expected effect">
          <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">What the advisor expects</h3>
          {effects.length === 0 ? (
            <p className="text-nb-500">It did not predict an effect, so no comparison will be recorded.</p>
          ) : (
            <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1">
              {effects.map((e) => (
                <div key={e.metric} className="contents">
                  <dt className="text-nb-400">{e.label}</dt>
                  <dd className="tabular-nums text-nb-200">{e.text}</dd>
                </div>
              ))}
            </dl>
          )}
        </section>

        <section aria-label="Evidence">
          <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">Why</h3>
          {p.evidence.length === 0 ? (
            <p className="text-nb-500">The advisor gave no evidence.</p>
          ) : (
            <Table>
              <thead>
                <tr><Th>Fact</Th><Th>Value</Th><Th>How it is known</Th><Th>Seen</Th></tr>
              </thead>
              <tbody>
                {p.evidence.map((f, i) => (
                  <tr key={`${f.name}-${i}`} data-testid="proposal-evidence">
                    <Td className="font-medium text-nb-300">{f.name}</Td>
                    <Td className="tabular-nums">{f.value ?? '—'}</Td>
                    <Td>
                      <span title={CLASS_WORDS[f.class]?.help} className={CLASS_TONE[f.class] ?? 'text-nb-400'}>{CLASS_WORDS[f.class]?.label ?? f.class}</span>
                      {f.source ? <span className="text-nb-500"> · {f.source}</span> : null}
                    </Td>
                    <Td className="whitespace-nowrap text-nb-400">{f.observedAt ? ago(f.observedAt) : '—'}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </section>

        {!waiting && (
          <section aria-label="Decision">
            <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">Decision</h3>
            {p.decidedBy ? (
              <p className="text-nb-300">
                {p.status === 'rejected' ? 'Rejected' : 'Accepted'} by {p.decidedBy}{p.decidedAt ? `, ${stamp(p.decidedAt)}` : ''}.
              </p>
            ) : p.status === 'expired' ? (
              <p className="text-nb-400">Nobody decided before it expired.</p>
            ) : null}
            {p.note && <p className={clsx('mt-1', p.status === 'failed' ? 'text-bad' : 'text-nb-400')} data-testid="proposal-note">{p.note}</p>}
            {p.status === 'applied' && !p.outcome && (
              <p className="mt-1 text-nb-500">The change is made. Once the agent reports it in force, the server compares the agent’s own load before and after and records it here.</p>
            )}
          </section>
        )}

        {p.outcome && (
          <section aria-label="Outcome" data-testid="proposal-outcome">
            <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">What happened</h3>
            <p className="mb-2 text-nb-400">{OUTCOME_WORDS[p.outcome.result]}</p>
            {p.outcome.result === 'measured' && (
              <>
                <Table>
                  <thead>
                    <tr><Th>Measure</Th><Th>Expected</Th><Th>Before</Th><Th>After</Th><Th>Observed</Th></tr>
                  </thead>
                  <tbody>
                    {outcomeRows(p.outcome).map((r) => (
                      <tr key={r.metric} data-testid="proposal-outcome-row">
                        <Td className="font-medium text-nb-300">{r.label}</Td>
                        <Td className="tabular-nums">{r.predicted ?? '—'}</Td>
                        <Td className="tabular-nums text-nb-400">{r.before ?? '—'}</Td>
                        <Td className="tabular-nums text-nb-400">{r.after ?? '—'}</Td>
                        <Td className="tabular-nums text-nb-200">{r.observed ?? '—'}</Td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
                <p className="mt-1.5 text-xs text-nb-500">The agent’s load can also move for reasons that have nothing to do with the change. This is what was observed, not proof of cause.</p>
              </>
            )}
          </section>
        )}

        {waiting && (
          <section aria-label="Your decision">
            <label className="mb-1 block text-xs font-medium uppercase tracking-wide text-nb-500" htmlFor="proposal-note">Note (optional)</label>
            <Input
              id="proposal-note"
              value={note}
              maxLength={200}
              onChange={(e) => setNote(e.target.value)}
              placeholder="Why you accepted or rejected it. Kept with the decision."
              disabled={busy !== null}
              data-testid="proposal-note-input"
            />
            <p className="mt-1.5 text-xs text-nb-500">
              Accepting makes the change the same way you would by hand, and it appears in Who did what. The server checks it against the agent as it is now, so a change
              that no longer fits is refused.
            </p>
          </section>
        )}
      </div>
    </Modal>
  )
}

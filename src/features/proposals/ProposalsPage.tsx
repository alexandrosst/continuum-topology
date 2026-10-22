import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import AdvisorRecords from '@/features/proposals/components/AdvisorRecords'
import ProposalDialog, { ConfidencePill, StatusPill } from '@/features/proposals/components/ProposalDialog'
import { Button, EmptyState, ErrorBanner, PageHeader, Pill, Select, Table, TableSkeleton, Td, Th } from '@/components/ui/primitives'
import { api, atLeast } from '@/lib/api'
import { ago } from '@/lib/observed'
import { changeLines, effectLines, timeLeft, waitingFirst, type AdvisorRecord, type Proposal, type ProposalStatus } from '@/lib/proposals'
import { useConn, useServer } from '@/store/server'
import { useTopology } from '@/store/topology'
import { refreshWaitingProposals, useWaitingProposals, WAITING_CAP } from '@/features/proposals/store/waitingProposals'

const FILTERS: { value: ProposalStatus | 'all'; label: string }[] = [
  { value: 'pending', label: 'Waiting for a decision' },
  { value: 'all', label: 'All' },
  { value: 'applied', label: 'Applied' },
  { value: 'rejected', label: 'Rejected' },
  { value: 'failed', label: 'Could not be applied' },
  { value: 'expired', label: 'Expired' },
]

const COLS = ['', 'w-64', 'w-44', 'w-44', 'w-44']
/** The list is read again this often while the page is open, so a proposal an advisor just made shows up without a reload. */
const REFRESH_MS = 30_000

/**
 * What advisors suggest about the access of each agent. A suggestion changes nothing by itself: a person reads why, and accepts
 * or rejects it. Accepting is the same act as changing the agent's access by hand, so it is recorded the same way, and a change that
 * would give the server more access takes an administrator. The program that makes the suggestions holds an editor's API token and
 * submits through the API (see backend/docs/decision-loop.md); there is nothing to type in here.
 */
export default function ProposalsPage() {
  const status = useServer((s) => s.status)
  const role = useServer((s) => s.role)
  const conn = useConn()
  const { agents } = useTopology()
  const [filter, setFilter] = useState<ProposalStatus | 'all'>('pending')
  const [rows, setRows] = useState<Proposal[] | null>(null)
  const [error, setError] = useState<string>()
  const [advisors, setAdvisors] = useState<AdvisorRecord[]>([])
  const [openId, setOpenId] = useState<string | null>(null)
  const allowed = status === 'connected' && atLeast(role, 'editor')

  const names = useMemo(() => new Map(agents.map((a) => [a.id, a.name])), [agents])
  const nameOf = (id: string) => names.get(id) ?? id

  // Only the newest request may land: a slow answer for a filter that has since changed must not replace the rows now shown.
  const newest = useRef(0)
  const load = useCallback(async () => {
    const mine = ++newest.current
    try {
      const list = await api.listProposals(conn, { status: filter === 'all' ? undefined : filter, limit: 200 })
      if (mine !== newest.current) return
      setRows(list)
      setError(undefined)
      // This list is the freshest word on what waits, so the menu's badge takes it (a longer list than the badge counts is capped there).
      // The record is a side note: if it cannot be read the list is still right, so the last one stays.
      api.listAdvisors(conn).then((a) => mine === newest.current && setAdvisors(a), () => {})
      if (filter === 'pending') useWaitingProposals.getState().set(Math.min(list.length, WAITING_CAP))
    } catch (e) {
      if (mine !== newest.current) return
      setError(e instanceof Error ? e.message : 'The proposals could not be read.')
    }
  }, [conn, filter])
  useEffect(() => {
    if (!allowed) return
    void load()
    const t = setInterval(() => void load(), REFRESH_MS)
    return () => clearInterval(t)
  }, [allowed, load])

  if (!allowed) {
    return (
      <>
        <PageHeader title="Proposals" description="Changes an advisor suggests to what an agent shares, waiting for a person to decide." />
        <EmptyState title="Editors and administrators only" description="A proposal carries the evidence an advisor used about an agent, and deciding one changes the agent's access, so it is limited to the people who can change that. Connect to a server and sign in as an editor or administrator to see them." />
      </>
    )
  }

  const shown = rows ? waitingFirst(rows) : null
  const open = shown?.find((p) => p.id === openId)
  const waitingCount = rows?.filter((p) => p.status === 'pending').length ?? 0

  return (
    <>
      <PageHeader title="Proposals" description="Changes an advisor suggests to what an agent shares. Nothing changes until a person accepts one; an accepted change is made the same way as one you make by hand, and is recorded in Who did what. Making an agent share more takes an administrator." />
      {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="w-60">
          <Select value={filter} onChange={(e) => { setRows(null); setFilter(e.target.value as ProposalStatus | 'all') }} aria-label="Show">
            {FILTERS.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
          </Select>
        </div>
        <Button onClick={() => void load()}>Refresh</Button>
        {filter !== 'pending' && waitingCount > 0 && <Pill className="text-xs text-warn">{waitingCount} waiting</Pill>}
      </div>

      {shown === null ? (
        <TableSkeleton cols={COLS} />
      ) : shown.length === 0 ? (
        <EmptyState
          title={filter === 'pending' ? 'Nothing is waiting for a decision' : 'Nothing matches'}
          description={filter === 'pending'
            ? 'An advisor is any program that holds an editor’s API token and submits a proposal; it never changes anything itself. When one does, it appears here with its evidence.'
            : 'No proposal has this status yet.'}
        />
      ) : (
        <Table cols={COLS}>
          <thead>
            <tr><Th>Change</Th><Th>Expected effect</Th><Th>Advisor</Th><Th>When</Th><Th>Status</Th></tr>
          </thead>
          <tbody>
            {shown.map((p) => {
              const lines = changeLines(p.change, p.blast)
              const effects = effectLines(p.predicted ?? {})
              return (
                <tr key={p.id} data-testid="proposal-row" className="cursor-pointer hover:bg-nb-930/60" onClick={() => setOpenId(p.id)}>
                  <Td>
                    <button type="button" className="text-left font-medium text-nb-200 hover:underline focus-visible:underline" onClick={(e) => { e.stopPropagation(); setOpenId(p.id) }}>
                      {lines.join('; ') || 'Change'}
                    </button>
                    <div className="text-xs text-nb-500">on {nameOf(p.change.agentId || p.agentId)}{p.widens ? ' · shares more with the server' : ''}</div>
                  </Td>
                  <Td className="text-nb-400">
                    {effects.length === 0 ? '—' : effects.map((e) => <div key={e.metric} className="whitespace-nowrap">{e.label} <span className="tabular-nums text-nb-300">{e.text}</span></div>)}
                  </Td>
                  <Td>
                    <div className="text-nb-300">{p.advisor}</div>
                    <ConfidencePill confidence={p.confidence} />
                  </Td>
                  <Td className="whitespace-nowrap text-nb-400">
                    {ago(p.createdAt)}
                    {p.status === 'pending' && <div className="text-xs text-nb-500">expires in {timeLeft(p.expiresAt)}</div>}
                  </Td>
                  <Td><StatusPill status={p.status} /></Td>
                </tr>
              )
            })}
          </tbody>
        </Table>
      )}

      <AdvisorRecords records={advisors} />

      {open && (
        <ProposalDialog
          key={open.id}
          proposal={open}
          agentName={nameOf(open.change.agentId || open.agentId)}
          role={role}
          onClose={() => setOpenId(null)}
          onDecided={() => { void load(); void refreshWaitingProposals(conn) }}
        />
      )}
    </>
  )
}

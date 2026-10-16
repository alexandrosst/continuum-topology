import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import type { AdvisorRecord, Proposal } from '@/lib/proposals'
import ProposalsPage from '@/pages/ProposalsPage'
import { useWaitingProposals } from '@/store/waitingProposals'

let role = 'editor'
let status = 'connected'
const listProposals = vi.fn(async (_c: unknown, _q?: unknown): Promise<Proposal[]> => [])
const listAdvisors = vi.fn(async (_c: unknown): Promise<AdvisorRecord[]> => [])
const getProposal = vi.fn(async (_c: unknown, id: string): Promise<Proposal> => ({ ...base, id }))
const acceptProposal = vi.fn(async (_c: unknown, id: string, _note?: string): Promise<Proposal> => ({ ...base, id, status: 'applied', decidedBy: 'ann' }))
const rejectProposal = vi.fn(async (_c: unknown, id: string, _note?: string): Promise<Proposal> => ({ ...base, id, status: 'rejected', decidedBy: 'ann' }))

// One stable object: a fresh one per render would change `load` on every render and refetch forever.
const CONN = { url: '', org: 'o' }
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { status: string; role: string }) => unknown) => selector({ status, role }),
  useConn: () => CONN,
}))
const AGENTS = [{ id: 'ag-1', name: 'edge-1' }]
vi.mock('@/store/topology', () => ({ useTopology: () => ({ agents: AGENTS }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listProposals: (...a: Parameters<typeof listProposals>) => listProposals(...a),
      listAdvisors: (...a: Parameters<typeof listAdvisors>) => listAdvisors(...a),
      getProposal: (...a: Parameters<typeof getProposal>) => getProposal(...a),
      acceptProposal: (...a: Parameters<typeof acceptProposal>) => acceptProposal(...a),
      rejectProposal: (...a: Parameters<typeof rejectProposal>) => rejectProposal(...a),
    },
  }
})

const FUTURE = new Date(Date.now() + 3 * 3600_000).toISOString()
const base: Proposal = {
  id: 'pr-1', agentId: 'ag-1', advisor: 'cpu-guard-example', modelVersion: 7,
  change: { agentId: 'ag-1', pause: ['flow'] },
  evidence: [{ name: 'agent CPU (mean of 12 samples)', value: '0.61 cores', class: 'measured', source: 'heartbeat', observedAt: new Date().toISOString() }],
  confidence: 'measured', blast: { agents: 1, collectors: ['flow'] }, predicted: { agent_cpu_cores: -0.3 },
  widens: false, mode: 'advise', status: 'pending', createdAt: new Date().toISOString(), expiresAt: FUTURE,
}
const widening: Proposal = { ...base, id: 'pr-2', change: { agentId: 'ag-1', tier: 2 }, blast: { agents: 1, tierFrom: 1, tierTo: 2 }, widens: true, predicted: {}, confidence: 'declared', advisor: 'tier-bot' }

beforeEach(() => {
  role = 'editor'
  status = 'connected'
  listProposals.mockReset().mockResolvedValue([])
  listAdvisors.mockReset().mockResolvedValue([])
  getProposal.mockReset().mockImplementation(async (_c, id) => ({ ...base, id }))
  acceptProposal.mockClear()
  rejectProposal.mockClear()
})

describe('ProposalsPage', () => {
  test('people who cannot decide see why, and nothing is requested', async () => {
    role = 'viewer'
    render(<ProposalsPage />)
    expect(screen.getByText('Editors and administrators only')).toBeInTheDocument()
    expect(listProposals).not.toHaveBeenCalled()
  })

  test('not being connected to a server says the same', () => {
    status = 'idle'
    render(<ProposalsPage />)
    expect(screen.getByText('Editors and administrators only')).toBeInTheDocument()
    expect(listProposals).not.toHaveBeenCalled()
  })

  test('with nothing waiting, it says how a proposal gets here', async () => {
    render(<ProposalsPage />)
    expect(await screen.findByText('Nothing is waiting for a decision')).toBeInTheDocument()
    expect(listProposals).toHaveBeenCalledWith(CONN, { status: 'pending', limit: 200 })
  })

  test('each proposal reads as a sentence about a named agent, with its effect, advisor and confidence', async () => {
    listProposals.mockResolvedValue([base, widening])
    render(<ProposalsPage />)
    const rows = await screen.findAllByTestId('proposal-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Pause the traffic observer')).toBeInTheDocument()
    expect(within(rows[0]).getByText(/on edge-1/)).toBeInTheDocument()
    expect(within(rows[0]).getByText('−30 %')).toBeInTheDocument()
    expect(within(rows[0]).getByText('cpu-guard-example')).toBeInTheDocument()
    expect(within(rows[0]).getByText('Evidence: measured')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Change the approved access from Infrastructure to Services')).toBeInTheDocument()
    expect(within(rows[1]).getByText(/shares more with the server/)).toBeInTheDocument()
    expect(within(rows[1]).getByText('Evidence: stated')).toBeInTheDocument()
  })

  test('changing the filter asks for that status only', async () => {
    render(<ProposalsPage />)
    await screen.findByText('Nothing is waiting for a decision')
    await userEvent.click(screen.getByRole('combobox', { name: 'Show' }))
    await userEvent.click(await screen.findByRole('option', { name: 'Applied' }))
    await waitFor(() => expect(listProposals).toHaveBeenLastCalledWith(CONN, { status: 'applied', limit: 200 }))
  })

  test('an editor accepts a narrowing: the note goes with it and the result is shown', async () => {
    listProposals.mockResolvedValue([base])
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Pause the traffic observer' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('agent CPU (mean of 12 samples)')).toBeInTheDocument()
    expect(screen.getByText('0.61 cores')).toBeInTheDocument()
    await userEvent.type(screen.getByTestId('proposal-note-input'), 'busy week')
    const accept = screen.getByTestId('proposal-accept')
    expect(accept).toBeEnabled()
    await userEvent.click(accept)
    await waitFor(() => expect(acceptProposal).toHaveBeenCalledWith(CONN, 'pr-1', 'busy week'))
    expect(await within(screen.getByRole('dialog')).findByText(/Accepted by ann/)).toBeInTheDocument()
    expect(screen.queryByTestId('proposal-accept')).toBeNull()
    expect(listProposals.mock.calls.length).toBeGreaterThan(1) // the list is read again
  })

  test('each advisor’s record is shown under the list, in counts, with what a rejection does and does not mean', async () => {
    listAdvisors.mockResolvedValue([
      { advisor: 'rules-v1', proposals: 4, pending: 1, accepted: 2, failed: 0, rejected: 1, expired: 0, measured: 2, unconfirmed: 0, noData: 0, held: 1, missed: 1 },
      { advisor: 'cpu-guard', proposals: 1, pending: 1, accepted: 0, failed: 0, rejected: 0, expired: 0, measured: 0, unconfirmed: 0, noData: 0, held: 0, missed: 0 },
    ])
    render(<ProposalsPage />)
    const rows = await screen.findAllByTestId('advisor-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('rules-v1')).toBeInTheDocument()
    expect(within(rows[0]).getByText('4 (1 waiting)')).toBeInTheDocument()
    expect(within(rows[0]).getByText('2 accepted, 1 rejected')).toBeInTheDocument()
    expect(within(rows[0]).getByText('1 of 2 held')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Nothing decided yet')).toBeInTheDocument()
    expect(within(rows[1]).getByText('—')).toBeInTheDocument()
    expect(screen.getByText(/means a person disagreed, not that the advisor was wrong/)).toBeInTheDocument()
  })

  test('with no advisor yet there is no record section, and a record that cannot be read leaves the list alone', async () => {
    listProposals.mockResolvedValue([base])
    listAdvisors.mockRejectedValue(new ApiError(500, 'boom'))
    render(<ProposalsPage />)
    await screen.findAllByTestId('proposal-row')
    expect(screen.queryByTestId('advisor-records')).toBeNull()
    expect(screen.queryByText('boom')).toBeNull()
  })

  test('the menu badge follows the waiting list, and drops when one is decided', async () => {
    useWaitingProposals.getState().set(0)
    listProposals.mockResolvedValue([base, widening])
    render(<ProposalsPage />)
    await screen.findAllByTestId('proposal-row')
    await waitFor(() => expect(useWaitingProposals.getState().count).toBe(2))

    // The server no longer lists the one that is decided as waiting.
    listProposals.mockResolvedValue([widening])
    await userEvent.click(screen.getByRole('button', { name: 'Pause the traffic observer' }))
    await userEvent.click(screen.getByTestId('proposal-accept'))
    await waitFor(() => expect(useWaitingProposals.getState().count).toBe(1))
  })

  test('looking at a decided list does not change the badge', async () => {
    listProposals.mockImplementation(async (_c, q) => ((q as { status?: string }).status === 'pending' ? [base, widening, { ...base, id: 'pr-3' }] : [{ ...base, status: 'applied' }]))
    render(<ProposalsPage />)
    await waitFor(() => expect(useWaitingProposals.getState().count).toBe(3))
    await userEvent.click(await screen.findByRole('combobox', { name: 'Show' }))
    await userEvent.click(await screen.findByRole('option', { name: 'Applied' }))
    await screen.findAllByTestId('proposal-row')
    expect(useWaitingProposals.getState().count).toBe(3)
  })

  test('rejecting sends the note and changes nothing else', async () => {
    listProposals.mockResolvedValue([base])
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Pause the traffic observer' }))
    await userEvent.click(screen.getByTestId('proposal-reject'))
    await waitFor(() => expect(rejectProposal).toHaveBeenCalledWith(CONN, 'pr-1', ''))
    expect(acceptProposal).not.toHaveBeenCalled()
    expect(await within(screen.getByRole('dialog')).findByText(/Rejected by ann/)).toBeInTheDocument()
  })

  test('an editor cannot accept a widening and is told why; they can still reject it', async () => {
    listProposals.mockResolvedValue([widening])
    getProposal.mockResolvedValue(widening)
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: /Change the approved access/ }))
    expect(screen.getByTestId('proposal-needs-admin')).toBeInTheDocument()
    expect(screen.getByTestId('proposal-accept')).toBeDisabled()
    expect(screen.getByTestId('proposal-reject')).toBeEnabled()
  })

  test('an administrator can accept a widening', async () => {
    role = 'admin'
    listProposals.mockResolvedValue([widening])
    getProposal.mockResolvedValue(widening)
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: /Change the approved access/ }))
    expect(screen.queryByTestId('proposal-needs-admin')).toBeNull()
    expect(screen.getByTestId('proposal-accept')).toBeEnabled()
  })

  test('a refusal is shown, and the proposal is read again as it now stands', async () => {
    listProposals.mockResolvedValue([base])
    acceptProposal.mockRejectedValueOnce(new ApiError(409, 'this proposal has already been decided'))
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Pause the traffic observer' }))
    getProposal.mockClear()
    getProposal.mockResolvedValue({ ...base, status: 'rejected', decidedBy: 'bo' })
    await userEvent.click(screen.getByTestId('proposal-accept'))
    expect(await screen.findByText('this proposal has already been decided')).toBeInTheDocument()
    expect(await within(screen.getByRole('dialog')).findByText(/Rejected by bo/)).toBeInTheDocument()
    expect(screen.queryByTestId('proposal-accept')).toBeNull()
  })

  test('an applied proposal shows what was expected beside what was observed', async () => {
    const applied: Proposal = {
      ...base, status: 'applied', decidedBy: 'ann',
      outcome: {
        result: 'measured', predicted: { agent_cpu_cores: -0.3 }, recordedAt: new Date().toISOString(),
        observed: { before: { agent_cpu_cores: 0.5 }, after: { agent_cpu_cores: 0.35 }, change: { agent_cpu_cores: -0.3 } },
      },
    }
    listProposals.mockResolvedValue([{ ...applied, outcome: undefined }])
    getProposal.mockResolvedValue(applied)
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Pause the traffic observer' }))
    const out = await screen.findByTestId('proposal-outcome')
    const row = within(out).getByTestId('proposal-outcome-row')
    expect(within(row).getByText('0.50 cores')).toBeInTheDocument()
    expect(within(row).getByText('0.35 cores')).toBeInTheDocument()
    expect(within(row).getAllByText('−30 %')).toHaveLength(2)
    expect(within(out).getByText(/not proof of cause/)).toBeInTheDocument()
  })

  test('a failed change shows why', async () => {
    const failed: Proposal = { ...base, status: 'failed', decidedBy: 'ann', note: 'the agent is not connected' }
    listProposals.mockResolvedValue([failed])
    getProposal.mockResolvedValue(failed)
    render(<ProposalsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Pause the traffic observer' }))
    expect(await screen.findByTestId('proposal-note')).toHaveTextContent('the agent is not connected')
  })
})

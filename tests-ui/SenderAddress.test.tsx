import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SenderAddress from '@/features/pipeline/components/telemetry/SenderAddress'
import type { AddressCandidates, AddressTest, RegionalOperator } from '@/lib/types'

const candidates = vi.fn(async (..._a: unknown[]): Promise<AddressCandidates> => ({ found: false, candidates: [] }))
const startTests = vi.fn(async (..._a: unknown[]): Promise<{ tests: AddressTest[] }> => ({ tests: [] }))
const listTests = vi.fn(async (..._a: unknown[]): Promise<{ tests: AddressTest[] }> => ({ tests: [] }))
const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn }) => unknown) => {
    const state = { conn: connFn }
    return selector ? selector(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, operatorAddressCandidates: (...a: unknown[]) => candidates(...a), startAddressTests: (...a: unknown[]) => startTests(...a), addressTests: (...a: unknown[]) => listTests(...a) } }
})

const op = { id: 'site-athens', name: 'site-athens', address: 'otlp.example.com:4317' } as RegionalOperator

function Harness({ initial = '', agentId }: { initial?: string; agentId?: string }) {
  const [v, setV] = useState(initial)
  return (
    <>
      <SenderAddress operator={op} value={v} onChange={setV} testId="sa" agentId={agentId} clusterName="patras-edge" />
      <output data-testid="out">{v}</output>
    </>
  )
}

beforeEach(() => {
  startTests.mockReset()
  listTests.mockReset()
  candidates.mockReset()
  candidates.mockResolvedValue({
    found: true,
    hostClusterName: 'athens-edge',
    candidates: [
      { id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node athens-edge-1, internal address.', node: 'athens-edge-1' },
      { id: 'recorded|otlp.example.com:4317', address: 'otlp.example.com:4317', kind: 'recorded', source: 'Recorded for this operator.', recorded: true },
    ],
  })
})
afterEach(cleanup)

describe('the sender picks the address of a regional operator it dials', () => {
  it('shows the card with the exposing command when the central gateway is not reachable from other clusters', async () => {
    candidates.mockResolvedValue({
      found: true,
      candidates: [],
      expose: { service: 'fusion-central', namespace: 'ikhnos-system', value: 'fusion.central.service.type', commands: { nodePort: 'kubectl patch NODEPORT', loadBalancer: 'kubectl patch LB' } },
    })
    render(<Harness />)
    expect((await screen.findByTestId('sa-expose')).textContent).toContain('not reachable from other clusters yet')
    expect(screen.getByTestId('sa-expose-command').textContent).toContain('kubectl patch NODEPORT')
  })

  it('says why nothing is listed when the operator is not exposed, instead of showing nothing', async () => {
    candidates.mockResolvedValue({ found: true, candidates: [], hints: ['FUSION’s gateway is only reachable inside its own cluster. Expose it with fusion.central.service.type=NodePort.'] })
    render(<Harness />)
    expect((await screen.findByTestId('sa-hint')).textContent).toContain('fusion.central.service.type')
  })

  it('starts on the recorded address, lists what discovery found once, and a pick is the draft value', async () => {
    render(<Harness />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    expect(screen.getAllByTestId('sa-option')).toHaveLength(2) // the recorded one, and the one node port; the recorded address is not listed twice
    expect((screen.getByRole('radio', { name: 'otlp.example.com:4317' }) as HTMLInputElement).checked).toBe(true)
    await userEvent.click(screen.getByRole('radio', { name: '10.20.0.14:31317' }))
    expect(screen.getByTestId('out').textContent).toBe('10.20.0.14:31317')
    await userEvent.click(screen.getByRole('radio', { name: 'otlp.example.com:4317' }))
    expect(screen.getByTestId('out').textContent).toBe('')
  })

  it('takes an address of its own, and a bare host gets no port added to the draft', async () => {
    render(<Harness />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    await userEvent.type(screen.getByTestId('sa-other'), '10.9.9.9:4317')
    expect(screen.getByTestId('out').textContent).toBe('10.9.9.9:4317')
    expect((screen.getByRole('radio', { name: 'otlp.example.com:4317' }) as HTMLInputElement).checked).toBe(false)
  })

  it('still works when the lookup fails', async () => {
    candidates.mockRejectedValue(new Error('network'))
    render(<Harness initial="10.0.0.5:4317" />)
    expect((screen.getByTestId('sa-other') as HTMLInputElement).value).toBe('10.0.0.5:4317')
  })
})

const pending = (address: string): AddressTest => ({ id: 'at-1', address, state: 'pending', created: '2026-10-12T10:00:00Z' })

describe('trying an address from the cluster that will dial it', () => {
  it('offers no test without an agent to run it from', async () => {
    render(<Harness />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    expect(screen.queryByTestId('sa-test')).toBeNull()
  })

  it('starts a test for the row, shows it pending, and then what the cluster found', async () => {
    startTests.mockResolvedValue({ tests: [pending('10.20.0.14:31317')] })
    listTests.mockResolvedValue({ tests: [{ ...pending('10.20.0.14:31317'), state: 'reachable', samples: 5, failed: 0, rttP50Ms: 4.2 }] })
    render(<Harness agentId="agent-1" />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    await userEvent.click(screen.getByRole('button', { name: 'Test 10.20.0.14:31317 from this cluster' }))
    expect(startTests).toHaveBeenCalledWith(expect.anything(), 'agent-1', ['10.20.0.14:31317'])
    expect((await screen.findByTestId('sa-result')).getAttribute('data-state')).toBe('pending')
    expect(screen.getByTestId('sa-result').textContent).toContain('patras-edge')
    await waitFor(() => expect(screen.getByTestId('sa-result').getAttribute('data-state')).toBe('reachable'), { timeout: 5000 })
    expect(screen.getByTestId('sa-result').textContent).toContain('4.2 ms')
    // It does not claim the operator answered.
    expect(screen.getByTestId('sa-test-note').textContent).toContain('not that the operator is what answers')
  })

  it('says plainly when every attempt failed, and shows a refusal from the server', async () => {
    startTests.mockResolvedValueOnce({ tests: [{ ...pending('otlp.example.com:4317'), state: 'unreachable', samples: 5, failed: 5 }] })
    render(<Harness agentId="agent-1" />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    await userEvent.click(screen.getByRole('button', { name: 'Test otlp.example.com:4317 from this cluster' }))
    const r = await screen.findByTestId('sa-result')
    expect(r.getAttribute('data-state')).toBe('unreachable')
    expect(r.textContent).toContain('all 5 attempts failed')
    startTests.mockRejectedValueOnce(new Error('this agent is not connected right now'))
    await userEvent.click(screen.getByRole('button', { name: 'Test 10.20.0.14:31317 from this cluster' }))
    expect((await screen.findByRole('alert')).textContent).toContain('not connected')
  })

  it('tests an address typed in, with the receiver\'s port when none was given', async () => {
    startTests.mockResolvedValue({ tests: [pending('10.9.9.9:4317')] })
    render(<Harness agentId="agent-1" />)
    await screen.findByRole('radio', { name: '10.20.0.14:31317' })
    await userEvent.type(screen.getByTestId('sa-other'), '10.9.9.9')
    await userEvent.click(screen.getByRole('button', { name: 'Test 10.9.9.9:4317 from this cluster' }))
    expect(startTests).toHaveBeenCalledWith(expect.anything(), 'agent-1', ['10.9.9.9:4317'])
  })
})

describe('the sender picker of the central operator shows where the server runs', () => {
  it('shows the card for a server whose cluster has no agent, in place of the generic sentence', async () => {
    candidates.mockResolvedValue({
      found: true,
      candidates: [{ id: 'load-balancer|203.0.113.9:4317', address: '203.0.113.9:4317', kind: 'load-balancer', source: 'The address the load balancer gave the Service.' }],
      hints: ['The server does not know which cluster FUSION’s gateway runs in'],
      serverCluster: { state: 'noAgent', source: 'install', clusterId: 'cl-9' },
    })
    render(<MemoryRouter><Harness /></MemoryRouter>)
    expect(await screen.findByTestId('sa-server-cluster')).toHaveTextContent("The server's cluster has no agent")
    expect(screen.queryByTestId('sa-hint')).toBeNull()
  })
})

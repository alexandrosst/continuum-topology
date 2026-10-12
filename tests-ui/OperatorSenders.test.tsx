import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import OperatorSendersModal, { deliveryWord } from '@/components/operators/OperatorSendersModal'
import { ApiError } from '@/lib/api'
import type { OperatorSender, OperatorSenders, RegionalOperator } from '@/lib/types'

const senders = vi.fn(async (..._a: unknown[]): Promise<OperatorSenders> => ({ operatorState: 'online', senders: [] }))
const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn }) => unknown) => {
    const state = { conn: connFn }
    return selector ? selector(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, operatorSenders: (...a: unknown[]) => senders(...a) } }
})

const op = { id: 'site-athens', name: 'site-athens' } as RegionalOperator
const sender = (over: Partial<OperatorSender> = {}): OperatorSender => ({
  intentId: 'ti-1',
  intentName: 'patras-edge',
  agentId: 'ag-1',
  agentName: 'patras-edge',
  connected: true,
  addresses: [{ address: '10.20.0.14:31317', source: 'chosen', signals: ['metrics', 'logs'] }],
  delivery: { state: 'exporting', sent: 120, failed: 0 },
  ...over,
})

beforeEach(() => {
  senders.mockReset()
})
afterEach(cleanup)

describe('the clusters sending to an operator', () => {
  it('says so when no cluster sends here', async () => {
    senders.mockResolvedValue({ operatorState: 'online', senders: [] })
    render(<OperatorSendersModal operator={op} onClose={() => {}} />)
    expect((await screen.findByTestId('operator-senders-empty')).textContent).toContain('No cluster is set up')
  })

  it('lists each sender with the address it dials, who chose it, and how delivery goes', async () => {
    senders.mockResolvedValue({
      operatorState: 'online',
      senders: [
        sender(),
        sender({ intentId: 'ti-2', agentName: 'corinth-edge', addresses: [{ address: 'otlp.example.com:4317', source: 'recorded', signals: ['traces'] }], delivery: { state: 'waiting', sent: 0, failed: 0 } }),
      ],
    })
    render(<OperatorSendersModal operator={op} onClose={() => {}} />)
    const rows = await screen.findAllByTestId('operator-sender')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('10.20.0.14:31317')
    expect(rows[0].textContent).toContain('chosen by this cluster')
    expect(rows[0].textContent).toContain('metrics, logs')
    expect(rows[0].querySelector('[data-testid="operator-sender-delivery"]')!.textContent).toBe('Sending')
    expect(rows[1].textContent).toContain('recorded for the operator')
    expect(rows[1].querySelector('[data-testid="operator-sender-delivery"]')!.textContent).toBe('Nothing sent yet')
    expect(screen.queryByTestId('operator-sender-diagnosis')).toBeNull()
  })

  it('puts the diagnosis under a failing sender, naming which end it points at', async () => {
    senders.mockResolvedValue({
      operatorState: 'online',
      senders: [
        sender({
          delivery: { state: 'failing', sent: 0, failed: 9, lastError: 'i/o timeout' },
          diagnosis: { where: 'sender-network', title: 'patras-edge cannot reach 10.20.0.14:31317', detail: 'athens-edge is exporting to this operator at the same address, so the operator is up and answering there.' },
        }),
      ],
    })
    render(<OperatorSendersModal operator={op} onClose={() => {}} />)
    const d = await screen.findByTestId('operator-sender-diagnosis')
    expect(d.getAttribute('data-where')).toBe('sender-network')
    expect(d.textContent).toContain('The sender’s network')
    expect(d.textContent).toContain('the operator is up')
    expect(screen.getByTestId('operator-sender-delivery').textContent).toBe('Failing')
  })

  it('shows a refusal in place of a list', async () => {
    senders.mockImplementation(() => Promise.reject(new ApiError(500, 'the server is down')))
    render(<OperatorSendersModal operator={op} onClose={() => {}} />)
    expect((await screen.findByRole('alert')).textContent).toContain('the server is down')
  })
})

describe('deliveryWord', () => {
  it('never calls a sender with no counters a fault', () => {
    expect(deliveryWord(sender({ delivery: undefined }))).toEqual({ text: 'Not reported', tone: 'muted' })
    expect(deliveryWord(sender({ delivery: { state: 'silent', sent: 5, failed: 0 } })).tone).toBe('muted')
  })
})

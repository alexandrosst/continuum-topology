import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OperatorAddressModal } from '@/components/operators/OperatorAddress'
import type { AddressCandidates, RegionalOperator } from '@/lib/types'

const candidates = vi.fn(async (..._a: unknown[]): Promise<AddressCandidates> => ({ found: false, candidates: [] }))
const setAddress = vi.fn(async (..._a: unknown[]): Promise<RegionalOperator> => ({}) as RegionalOperator)

const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn }) => unknown) => {
    const state = { conn: connFn }
    return selector ? selector(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, operatorAddressCandidates: (...a: unknown[]) => candidates(...a), setOperatorAddress: (...a: unknown[]) => setAddress(...a) } }
})

const op = { id: 'site-athens', name: 'site-athens', exposure: 'nodeport' } as RegionalOperator

const open = () => render(<OperatorAddressModal operator={op} onClose={() => {}} onDone={() => {}} />)

beforeEach(() => {
  candidates.mockReset()
  setAddress.mockReset()
  setAddress.mockResolvedValue({} as RegionalOperator)
})
afterEach(cleanup)

describe('the address dialog offers what discovery found', () => {
  it('lists the addresses, and picking one fills the field without saving', async () => {
    candidates.mockResolvedValue({
      found: true,
      hostClusterName: 'athens-edge',
      candidates: [
        { id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node athens-edge-1, internal address.', node: 'athens-edge-1' },
        { id: 'load-balancer|203.0.113.9:4317', address: '203.0.113.9:4317', kind: 'load-balancer', source: 'The address the load balancer gave the Service.' },
      ],
    })
    open()
    const rows = await screen.findAllByTestId('operator-address-candidate')
    expect(rows).toHaveLength(2)
    expect(screen.getByTestId('operator-address-candidates').textContent).toContain('athens-edge')
    await userEvent.click(screen.getByRole('button', { name: 'Use 10.20.0.14:31317' }))
    expect((screen.getByTestId('operator-address-input') as HTMLInputElement).value).toBe('10.20.0.14:31317')
    expect(setAddress).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Use 10.20.0.14:31317' }).textContent).toBe('Selected')
    await userEvent.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setAddress).toHaveBeenCalledWith(expect.anything(), 'site-athens', '10.20.0.14:31317'))
  })

  it('says why the list is empty, in the two ways it can be', async () => {
    candidates.mockResolvedValue({ found: false, candidates: [] })
    const first = open()
    expect((await screen.findByTestId('operator-address-candidates-none')).textContent).toContain('No agent has reported this operator')
    first.unmount()
    candidates.mockResolvedValue({ found: true, hostClusterName: 'athens-edge', candidates: [] })
    open()
    expect((await screen.findByTestId('operator-address-candidates-none')).textContent).toContain('no address outside the cluster yet')
  })

  it('does not list the typed address again as a find, and still works when the lookup fails', async () => {
    candidates.mockResolvedValue({ found: false, candidates: [{ id: 'recorded|otlp.example.com:4317', address: 'otlp.example.com:4317', kind: 'recorded', source: 'Recorded for this operator.', recorded: true }] })
    const first = open()
    await screen.findByTestId('operator-address-candidates-none')
    expect(screen.queryByTestId('operator-address-candidate')).toBeNull()
    first.unmount()
    candidates.mockRejectedValue(new Error('network'))
    open()
    await waitFor(() => expect(candidates).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId('operator-address-candidates')).toBeNull()
    expect(screen.getByTestId('operator-address-input')).toBeTruthy()
  })
})

describe('the central operator is offered what was found for its gateway too', () => {
  it('lists the gateway\'s addresses in its own dialog', async () => {
    candidates.mockResolvedValue({ found: true, candidates: [{ id: 'load-balancer|203.0.113.50:4317', address: '203.0.113.50:4317', kind: 'load-balancer', source: 'The address the load balancer gave the operator\'s Service.' }] })
    render(<OperatorAddressModal operator={{ id: 'op-central', name: 'Central (FUSION)' } as RegionalOperator} central={{ service: 'continuum-fusion-central', namespace: 'continuum' }} onClose={() => {}} onDone={() => {}} />)
    expect(await screen.findAllByTestId('operator-address-candidate')).toHaveLength(1)
    expect(candidates).toHaveBeenCalledWith(expect.anything(), 'op-central')
    await userEvent.click(screen.getByRole('button', { name: 'Use 203.0.113.50:4317' }))
    expect((screen.getByTestId('operator-address-input') as HTMLInputElement).value).toBe('203.0.113.50:4317')
    expect(screen.getByTestId('operator-address-central')).toBeInTheDocument()
  })
})

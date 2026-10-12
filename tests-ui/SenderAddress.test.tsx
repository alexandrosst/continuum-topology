import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SenderAddress from '@/components/telemetry/SenderAddress'
import type { AddressCandidates, RegionalOperator } from '@/lib/types'

const candidates = vi.fn(async (..._a: unknown[]): Promise<AddressCandidates> => ({ found: false, candidates: [] }))
const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn }) => unknown) => {
    const state = { conn: connFn }
    return selector ? selector(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, operatorAddressCandidates: (...a: unknown[]) => candidates(...a) } }
})

const op = { id: 'site-athens', name: 'site-athens', address: 'otlp.example.com:4317' } as RegionalOperator

function Harness({ initial = '' }: { initial?: string }) {
  const [v, setV] = useState(initial)
  return (
    <>
      <SenderAddress operator={op} value={v} onChange={setV} testId="sa" />
      <output data-testid="out">{v}</output>
    </>
  )
}

beforeEach(() => {
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

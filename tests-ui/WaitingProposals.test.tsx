import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { Proposal } from '@/lib/proposals'
import { usePollWaitingProposals, useWaitingProposals, WAITING_CAP } from '@/store/waitingProposals'

let role: string | undefined = 'editor'
let status = 'connected'
const listProposals = vi.fn(async (_c: unknown, _q?: unknown): Promise<Proposal[]> => [])

const CONN = { url: '', org: 'o' }
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { status: string; role?: string }) => unknown) => selector({ status, role }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, listProposals: (...a: Parameters<typeof listProposals>) => listProposals(...a) } }
})

const some = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `pr-${i}` }) as Proposal)

beforeEach(() => {
  role = 'editor'
  status = 'connected'
  listProposals.mockReset().mockResolvedValue([])
  useWaitingProposals.getState().set(0)
})
afterEach(() => vi.useRealTimers())

describe('the count of proposals waiting for a decision', () => {
  test('an editor reads the waiting ones and the menu gets their number', async () => {
    listProposals.mockResolvedValue(some(3))
    renderHook(() => usePollWaitingProposals())
    await waitFor(() => expect(useWaitingProposals.getState().count).toBe(3))
    expect(listProposals).toHaveBeenCalledWith(CONN, { status: 'pending', limit: WAITING_CAP })
  })

  test('a viewer is not asked for the list, which is for editors, and the count stays zero', async () => {
    role = 'viewer'
    useWaitingProposals.getState().set(4) // left over from a previous sign-in
    renderHook(() => usePollWaitingProposals())
    await waitFor(() => expect(useWaitingProposals.getState().count).toBe(0))
    expect(listProposals).not.toHaveBeenCalled()
  })

  test('nothing is asked while not connected', async () => {
    status = 'disconnected'
    renderHook(() => usePollWaitingProposals())
    expect(listProposals).not.toHaveBeenCalled()
    expect(useWaitingProposals.getState().count).toBe(0)
  })

  test('it is read again every minute, and a failed read leaves the last number in place', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    listProposals.mockResolvedValueOnce(some(2))
    renderHook(() => usePollWaitingProposals())
    await vi.waitFor(() => expect(useWaitingProposals.getState().count).toBe(2))

    listProposals.mockRejectedValueOnce(new Error('network'))
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(listProposals).toHaveBeenCalledTimes(2)
    expect(useWaitingProposals.getState().count).toBe(2)

    listProposals.mockResolvedValueOnce(some(5))
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(useWaitingProposals.getState().count).toBe(5)
  })

  test('it stops reading when the component goes away', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { unmount } = renderHook(() => usePollWaitingProposals())
    await vi.waitFor(() => expect(listProposals).toHaveBeenCalledTimes(1))
    unmount()
    await act(() => vi.advanceTimersByTimeAsync(180_000))
    expect(listProposals).toHaveBeenCalledTimes(1)
  })
})

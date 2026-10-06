import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { OPERATORS_POLL_MS, useOperators } from '@/lib/useOperators'
import type { RegionalOperator } from '@/lib/types'

const listOperators = vi.fn()
const conn = () => ({ url: '', org: 'o' })
let orgId = 'o'
vi.mock('@/store/server', () => ({ useServer: (sel: (s: { conn: typeof conn; orgId: string }) => unknown) => sel({ conn, orgId }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, listOperators: () => listOperators() } }
})

const op = (id: string): RegionalOperator => ({ id, orgId: 'o', name: id, status: 'active', sourceClusterIds: [], destination: { kind: 'external', endpoint: 'x:1' }, createdAt: '', createdBy: '' })
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('useOperators', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    listOperators.mockReset()
    orgId = 'o'
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('reads again on its own, and an answer that has not changed keeps the same array, so nothing built from it starts over', async () => {
    listOperators.mockImplementation(async () => [op('a')])
    const { result } = renderHook(() => useOperators(true))
    await advance(10)
    expect(result.current.loaded).toBe(true)
    const first = result.current.operators
    await advance(OPERATORS_POLL_MS + 100)
    expect(listOperators).toHaveBeenCalledTimes(2)
    expect(result.current.operators).toBe(first)
    listOperators.mockImplementation(async () => [op('a'), op('b')])
    await advance(OPERATORS_POLL_MS)
    expect(result.current.operators).toHaveLength(2)
  })

  test('does not read at all when it is not enabled, and says it has loaded', async () => {
    const { result } = renderHook(() => useOperators(false))
    await advance(OPERATORS_POLL_MS * 2)
    expect(listOperators).not.toHaveBeenCalled()
    expect(result.current.loaded).toBe(true)
  })

  test('an answer to a read that left before a newer one is dropped', async () => {
    let releaseSlow: (v: RegionalOperator[]) => void = () => undefined
    listOperators.mockImplementationOnce(() => new Promise<RegionalOperator[]>((r) => { releaseSlow = r })).mockResolvedValue([op('new')])
    const { result } = renderHook(() => useOperators(true))
    await act(async () => { await result.current.reload() })
    expect(result.current.operators.map((o) => o.id)).toEqual(['new'])
    await act(async () => releaseSlow([op('old')]))
    expect(result.current.operators.map((o) => o.id)).toEqual(['new'])
  })

  test('a failed read says so, and the next good one clears it', async () => {
    listOperators.mockRejectedValueOnce(new Error('down')).mockResolvedValue([op('a')])
    const { result } = renderHook(() => useOperators(true))
    await advance(10)
    expect(result.current.error).toBe('Could not load the regional operators.')
    await advance(OPERATORS_POLL_MS + 100)
    expect(result.current.error).toBe('')
  })

  test('another organisation starts the list over: the last one\'s operators are not shown while the new ones are read', async () => {
    listOperators.mockImplementation(async () => [op('a')])
    const { result, rerender } = renderHook(() => useOperators(true))
    await advance(10)
    expect(result.current.operators.map((o) => o.id)).toEqual(['a'])
    let release: (v: RegionalOperator[]) => void = () => undefined
    listOperators.mockImplementation(() => new Promise<RegionalOperator[]>((r) => { release = r }))
    orgId = 'p'
    rerender()
    await advance(10)
    expect(result.current.operators).toEqual([])
    expect(result.current.loaded).toBe(false)
    await act(async () => release([op('b')]))
    expect(result.current.operators.map((o) => o.id)).toEqual(['b'])
  })
})

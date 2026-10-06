import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { FusionPanel, useFusion } from '@/components/operators/FusionPanel'
import type { FusionStatus } from '@/lib/api'
import { fusionSentence, fusionUsable } from '@/lib/fusionStatus'

const parts = (ready: number[]) =>
  (['metrics', 'logs', 'traces', 'central'] as const).map((component, i) => ({ component, label: component, desired: 1, ready: ready[i] }))

describe('fusionSentence', () => {
  test('one sentence per state, counting only the parts that are meant to run', () => {
    expect(fusionSentence(null).kind).toBe('unavailable')
    expect(fusionSentence({ available: false, state: 'off', message: 'No access.' })).toEqual({ kind: 'unavailable', text: 'No access.' })
    expect(fusionSentence({ available: true, state: 'off', components: parts([0, 0, 0, 0]).map((c) => ({ ...c, desired: 0 })) }).kind).toBe('off')
    expect(fusionSentence({ available: true, state: 'starting', components: parts([1, 1, 0, 0]) }).text).toBe('Starting - 2 of 4 parts are up.')
    expect(fusionSentence({ available: true, state: 'running', components: parts([1, 1, 1, 1]) }).kind).toBe('running')
    expect(fusionSentence({ available: true, state: 'attention', message: 'Stuck.' })).toEqual({ kind: 'attention', text: 'Stuck.' })
  })

  test('a regional operator has somewhere to send only while FUSION is starting or running', () => {
    expect(fusionUsable(null)).toBe(false)
    expect(fusionUsable({ available: false, state: 'off' })).toBe(false)
    expect(fusionUsable({ available: true, state: 'off' })).toBe(false)
    expect(fusionUsable({ available: true, state: 'attention' })).toBe(false)
    expect(fusionUsable({ available: true, state: 'starting' })).toBe(true)
    expect(fusionUsable({ available: true, state: 'running' })).toBe(true)
  })
})

const getFusion = vi.fn()
vi.mock('@/store/server', () => ({ useServer: (sel: (s: { conn: () => { url: string; org: string } }) => unknown) => sel({ conn: () => ({ url: '', org: 'o' }) }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, getFusion: () => getFusion() } }
})

function Probe() {
  const f = useFusion()
  return <FusionPanel fusion={f} />
}

describe('useFusion', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    getFusion.mockReset()
  })
  afterEach(() => vi.useRealTimers())

  test('reads the status again while FUSION is starting, and stops once it is running', async () => {
    const starting: FusionStatus = { available: true, state: 'starting', components: parts([1, 0, 0, 0]) }
    const running: FusionStatus = { available: true, state: 'running', components: parts([1, 1, 1, 1]) }
    getFusion.mockResolvedValueOnce(starting).mockResolvedValue(running)
    render(<Probe />)
    expect(await screen.findByText('Starting - 1 of 4 parts are up.')).toBeInTheDocument()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4100)
    })
    expect(await screen.findByText(/Running - the central operator/)).toBeInTheDocument()
    const calls = getFusion.mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20000)
    })
    expect(getFusion.mock.calls.length).toBe(calls)
  })
})

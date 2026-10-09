import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { FusionPanel, partState, POLL_RUNNING_MS, POLL_SETTLING_MS, useFusion } from '@/components/fusion/useFusion'
import type { FusionStatus } from '@/lib/api'
import { fusionLabel, fusionSentence, fusionUsable, type FusionKind } from '@/lib/fusionStatus'

const parts = (ready: number[]) =>
  (['metrics', 'logs', 'traces', 'central'] as const).map((component, i) => ({ component, label: component, desired: 1, ready: ready[i] }))

describe('fusionSentence', () => {
  test('one sentence per state, counting only the parts that are meant to run', () => {
    expect(fusionSentence(null).kind).toBe('checking')
    expect(fusionSentence({ available: false, state: 'off', message: 'No access.' })).toEqual({ kind: 'unavailable', text: 'No access.' })
    expect(fusionSentence({ available: true, state: 'off', components: parts([0, 0, 0, 0]).map((c) => ({ ...c, desired: 0 })) }).kind).toBe('off')
    expect(fusionSentence({ available: true, state: 'starting', components: parts([1, 1, 0, 0]) }).text).toBe('Starting - 2 of 4 parts are up.')
    expect(fusionSentence({ available: true, state: 'running', components: parts([1, 1, 1, 1]) }).kind).toBe('running')
    expect(fusionSentence({ available: true, state: 'attention', message: 'Stuck.' })).toEqual({ kind: 'attention', text: 'Stuck.' })
  })

  test('running says when data last arrived, in seconds while that is recent, or that none has yet', () => {
    const now = Date.parse('2026-10-05T12:00:00Z')
    const running: FusionStatus = { available: true, state: 'running', components: parts([1, 1, 1, 1]) }
    expect(fusionSentence({ ...running, lastDataAt: '2026-10-05T11:59:48Z' }, now).text).toBe('Running - last data 12 s ago')
    expect(fusionSentence({ ...running, lastDataAt: '2026-10-05T11:50:00Z' }, now).text).toMatch(/^Running - last data .*ago$/)
    expect(fusionSentence(running, now).text).toBe('Running - waiting for first data')
  })

  test('a Grafana that is still coming up does not hold FUSION back or count as a part', () => {
    const withGrafana = [...parts([1, 1, 1, 1]), { component: 'grafana' as const, label: 'Grafana', desired: 1, ready: 0 }]
    expect(fusionSentence({ available: true, state: 'starting', components: [...parts([1, 1, 0, 0]), withGrafana[4]] }).text).toBe('Starting - 2 of 4 parts are up.')
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

describe('fusionLabel', () => {
  test('is the one word every place uses for each state', () => {
    const words: Record<FusionKind, string> = { checking: 'Checking', unavailable: 'Unavailable', starting: 'Starting', running: 'Running', off: 'Off', attention: 'Needs attention' }
    for (const [kind, word] of Object.entries(words)) expect(fusionLabel(kind as FusionKind)).toBe(word)
  })
})

describe('partState', () => {
  const part = { component: 'logs' as const, label: 'Logs', desired: 1, ready: 0 }
  test('a part that is not ready reads Starting while FUSION is coming up, and Not ready once FUSION needs attention', () => {
    expect(partState(part, 'starting').text).toBe('Starting')
    expect(partState(part, 'attention').text).toBe('Not ready')
    expect(partState({ ...part, ready: 1 }, 'attention').text).toBe('Up')
    expect(partState({ ...part, desired: 0 }, 'running').text).toBe('Off')
  })
})

const getFusion = vi.fn()
// One stable function, as the real store's: a fresh one per render would make the hook read again on every render.
const conn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({ useServer: (sel: (s: { conn: typeof conn }) => unknown) => sel({ conn }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, getFusion: () => getFusion() } }
})

const fusionOf = (status: FusionStatus | null) => ({ status, busy: false, error: '', refresh: vi.fn(), enable: vi.fn(), disable: vi.fn() }) as never

describe('FusionPanel', () => {
  test('a part that is not ready says why, and the only spinner is the one in the header while FUSION starts', () => {
    const status: FusionStatus = {
      available: true,
      state: 'starting',
      components: [
        { component: 'metrics', label: 'Metrics', desired: 1, ready: 0, reason: 'Pulling the image' },
        { component: 'logs', label: 'Logs', desired: 1, ready: 0 },
        { component: 'traces', label: 'Traces', desired: 1, ready: 1, reason: 'stale text on a part that is up' },
        { component: 'central', label: 'Central', desired: 1, ready: 0, reason: 'Waiting for a volume' },
      ],
    }
    render(<FusionPanel fusion={fusionOf(status)} />)
    expect(screen.getByTestId('fusion-reason-metrics')).toHaveTextContent('Pulling the image')
    expect(screen.getByTestId('fusion-reason-central')).toHaveTextContent('Waiting for a volume')
    expect(screen.queryByTestId('fusion-reason-logs')).not.toBeInTheDocument()
    expect(screen.queryByTestId('fusion-reason-traces')).not.toBeInTheDocument()
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByTestId('fusion-waiting')).toBeInTheDocument()
  })

  test('needing attention words a stuck part as Not ready, not Starting', () => {
    render(<FusionPanel fusion={fusionOf({ available: true, state: 'attention', message: 'A volume cannot be bound.', components: [{ component: 'logs', label: 'Logs', desired: 1, ready: 0 }] })} />)
    expect(screen.getByTestId('fusion-part-logs')).toHaveTextContent('Not ready')
    expect(screen.queryByTestId('fusion-waiting')).not.toBeInTheDocument()
  })

  test('running names the last data, and no spinner is on screen', () => {
    render(<FusionPanel fusion={fusionOf({ available: true, state: 'running', lastDataAt: new Date(Date.now() - 12_000).toISOString(), components: parts([1, 1, 1, 1]) })} />)
    expect(screen.getByTestId('fusion-status')).toHaveTextContent(/Running - last data \d+ s ago/)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  test('shows a skeleton of the same height until the first answer', () => {
    render(<FusionPanel fusion={fusionOf(null)} />)
    expect(screen.getByRole('status', { name: 'Checking FUSION' })).toBeInTheDocument()
    expect(screen.queryByTestId('fusion-enable')).not.toBeInTheDocument()
  })
})

function Probe() {
  const f = useFusion()
  return (
    <>
      <FusionPanel fusion={f} />
      <button type="button" data-testid="reread" onClick={() => void f.refresh()} />
    </>
  )
}

describe('useFusion', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    getFusion.mockReset()
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

  test('reads again every few seconds while FUSION is starting, then slowly while it runs', async () => {
    const starting: FusionStatus = { available: true, state: 'starting', components: parts([1, 0, 0, 0]) }
    const running: FusionStatus = { available: true, state: 'running', components: parts([1, 1, 1, 1]) }
    getFusion.mockResolvedValueOnce(starting).mockResolvedValue(running)
    render(<Probe />)
    expect(await screen.findByText('Starting - 1 of 4 parts are up.')).toBeInTheDocument()
    expect(getFusion).toHaveBeenCalledTimes(1)
    await advance(POLL_SETTLING_MS + 100)
    expect(getFusion).toHaveBeenCalledTimes(2)
    expect(await screen.findByText(/Running - waiting for first data/)).toBeInTheDocument()
    // Running: not every few seconds any more...
    await advance(POLL_SETTLING_MS * 3)
    expect(getFusion).toHaveBeenCalledTimes(2)
    // ...but still every half minute, so "last data" and the parts stay honest.
    await advance(POLL_RUNNING_MS)
    expect(getFusion.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  test('does not read at all while the tab is hidden, and reads once as soon as it is visible again', async () => {
    getFusion.mockResolvedValue({ available: true, state: 'running', components: parts([1, 1, 1, 1]) } satisfies FusionStatus)
    render(<Probe />)
    await waitFor(() => expect(screen.getByTestId('fusion-status')).toHaveTextContent(/Running/))
    const setVisibility = (state: 'hidden' | 'visible') => {
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue(state)
      document.dispatchEvent(new Event('visibilitychange'))
    }
    setVisibility('hidden')
    const before = getFusion.mock.calls.length
    await advance(POLL_RUNNING_MS * 3)
    expect(getFusion.mock.calls.length).toBe(before)
    setVisibility('visible')
    await advance(10)
    expect(getFusion.mock.calls.length).toBe(before + 1)
  })

  test('an answer to a read that left before a newer one is dropped, so Off cannot come back over Running', async () => {
    let releaseSlow: (s: FusionStatus) => void = () => undefined
    getFusion
      .mockImplementationOnce(() => new Promise<FusionStatus>((resolve) => { releaseSlow = resolve }))
      .mockResolvedValue({ available: true, state: 'running', components: parts([1, 1, 1, 1]) } satisfies FusionStatus)
    render(<Probe />)
    // The first read is still out when a second one is asked for and answers.
    await act(async () => screen.getByTestId('reread').click())
    await waitFor(() => expect(screen.getByTestId('fusion-status')).toHaveTextContent(/Running/))
    await act(async () => releaseSlow({ available: true, state: 'off' }))
    expect(screen.getByTestId('fusion-status')).toHaveTextContent(/Running/)
  })
})

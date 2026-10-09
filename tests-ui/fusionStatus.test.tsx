import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { POLL_RUNNING_MS, POLL_SETTLING_MS, useFusion } from '@/components/fusion/useFusion'
import type { FusionRetention, FusionStatus } from '@/lib/api'
import { fusionHealth, fusionLabel, fusionProblems, fusionSentence, fusionUsable, lastDataText, partHealth, type FusionKind } from '@/lib/fusionStatus'

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

const NOW = Date.parse('2026-10-05T12:00:00Z')
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString()
const MIN = 60_000

describe('lastDataText', () => {
  test('says when data last arrived, or that none has', () => {
    expect(lastDataText({ available: true, state: 'running', lastDataAt: iso(12_000) }, NOW)).toBe('Last data 12 s ago')
    expect(lastDataText({ available: true, state: 'running' }, NOW)).toBe('No data yet')
  })
})

const store = (over: Partial<FusionRetention['stores'][number]> = {}) =>
  ({ component: 'metrics', label: 'Metrics', days: 15, volumeKnown: true, volumeBytes: 10 * 2 ** 30, usedBytes: 1 * 2 ** 30, canGrow: true, ...over }) as FusionRetention['stores'][number]
const retentionOf = (...stores: FusionRetention['stores']): FusionRetention => ({ available: true, stores }) as FusionRetention
const running = (over: Partial<FusionStatus> = {}): FusionStatus => ({ available: true, state: 'running', lastDataAt: iso(10_000), since: iso(60 * MIN), components: parts([1, 1, 1, 1]), ...over })

describe('fusionProblems', () => {
  test('a healthy FUSION has none', () => {
    expect(fusionProblems(running(), retentionOf(store()), NOW)).toEqual([])
    expect(fusionProblems(null, null, NOW)).toEqual([])
    expect(fusionProblems({ available: true, state: 'off' }, null, NOW)).toEqual([])
  })

  test('a part that is down says why and what to try, and offers the kubectl command for the namespace', () => {
    const status: FusionStatus = {
      available: true, state: 'attention', central: { namespace: 'ikhnos-fusion' } as never,
      components: [{ component: 'logs', label: 'Logs', desired: 1, ready: 0, reason: 'Waiting for a volume' }, ...parts([1, 1, 1, 1]).filter((c) => c.component !== 'logs')],
    }
    const [p] = fusionProblems(status, null, NOW)
    expect(p).toMatchObject({ id: 'down-logs', part: 'logs', health: 'broken', title: 'Logs is not running' })
    expect(p.detail).toContain('Waiting for a volume.')
    expect(p.detail).toContain('Logs are not being stored.')
    expect(p.detail).toContain('storage class')
    expect(p.action).toEqual({ kind: 'copy', label: 'Copy the kubectl command', text: 'kubectl -n ikhnos-fusion get pods' })
  })

  test('needing attention with no part at fault falls back to the server message and offers to look again', () => {
    const [p] = fusionProblems({ available: true, state: 'attention', message: 'Stuck.', components: parts([1, 1, 1, 1]) }, null, NOW)
    expect(p).toMatchObject({ health: 'attention', title: 'Stuck.', action: { kind: 'refresh' } })
  })

  test('a volume that is nearly full, or cannot hold the retention, offers the retention', () => {
    const full = fusionProblems(running(), retentionOf(store({ usedBytes: 9.5 * 2 ** 30, usedSource: 'volume', canGrow: false })), NOW)
    expect(full).toHaveLength(1)
    expect(full[0]).toMatchObject({ id: 'disk-metrics', health: 'attention', action: { kind: 'link', to: '/fusion/settings' } })
    expect(full[0].title).toMatch(/Metrics's volume is \d+% full/)
    expect(full[0].detail).toContain('cannot be grown')
  })

  test('no data for minutes, or none since it started, is a problem that points at the pipeline', () => {
    expect(fusionProblems(running({ lastDataAt: iso(2 * MIN) }), null, NOW)).toEqual([])
    const stale = fusionProblems(running({ lastDataAt: iso(20 * MIN) }), null, NOW)
    expect(stale[0]).toMatchObject({ id: 'no-data', health: 'attention', action: { kind: 'link', to: '/pipeline' } })
    expect(stale[0].title).toMatch(/^No new data: the last data arrived /)
    expect(fusionProblems(running({ lastDataAt: undefined, since: iso(5 * MIN) }), null, NOW)).toEqual([])
    expect(fusionProblems(running({ lastDataAt: undefined, since: iso(30 * MIN) }), null, NOW)[0].title).toBe('No data has arrived yet')
  })

  test('the central address warnings are shown, and a broken part sorts before a warning', () => {
    const status: FusionStatus = {
      ...running({ lastDataAt: iso(20 * MIN) }), state: 'attention', central: { namespace: 'n', warnings: ['The address is not reachable.'] } as never,
      components: [{ component: 'traces', label: 'Traces', desired: 1, ready: 0 }, ...parts([1, 1, 1, 1]).filter((c) => c.component !== 'traces')],
    }
    const ids = fusionProblems(status, null, NOW).map((p) => p.id)
    expect(ids[0]).toBe('down-traces')
    expect(ids).toContain('central-address')
  })
})

describe('fusionHealth and partHealth', () => {
  test('are Healthy, Needs attention, Not working or Unknown, and Starting or Off where that is not a verdict', () => {
    expect(fusionHealth(null, [])).toBe('unknown')
    expect(fusionHealth({ available: false, state: 'off' }, [])).toBe('unknown')
    expect(fusionHealth({ available: true, state: 'off' }, [])).toBe('off')
    expect(fusionHealth({ available: true, state: 'starting' }, [])).toBe('starting')
    expect(fusionHealth(running(), [])).toBe('healthy')
    const warn = { id: 'x', health: 'attention', title: '', detail: '', action: { kind: 'refresh', label: '' } } as const
    expect(fusionHealth(running(), [warn])).toBe('attention')
    expect(fusionHealth(running(), [warn, { ...warn, health: 'broken' }])).toBe('broken')
  })

  test('a part that is not ready reads Starting while FUSION comes up and Not working once FUSION needs attention', () => {
    const part = { component: 'logs' as const, label: 'Logs', desired: 1, ready: 0 }
    expect(partHealth(part, 'starting', [])).toBe('starting')
    expect(partHealth(part, 'attention', [])).toBe('broken')
    expect(partHealth({ ...part, ready: 1 }, 'running', [])).toBe('healthy')
    expect(partHealth({ ...part, desired: 0 }, 'running', [])).toBe('off')
    const own = { id: 'disk-logs', part: 'logs', health: 'attention', title: '', detail: '', action: { kind: 'refresh', label: '' } } as const
    expect(partHealth({ ...part, ready: 1 }, 'running', [own])).toBe('attention')
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

function Probe() {
  const f = useFusion()
  return (
    <>
      <p data-testid="fusion-status">{fusionSentence(f.status).text}</p>
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

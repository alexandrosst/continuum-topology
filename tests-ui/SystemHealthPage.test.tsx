import { act, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import SystemHealthPage from '@/pages/SystemHealthPage'
import type { SelfTelemetryEntity } from '@/lib/selfHealth'

const selfTelemetry = vi.fn<[], Promise<SelfTelemetryEntity[]>>()

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, selfTelemetry: (...a: unknown[]) => selfTelemetry(...(a as [])) } }
})
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { status: string }) => unknown) => {
    const state = { status: 'connected' }
    return selector ? selector(state) : state
  },
  useConn: () => ({ url: '', org: 'o' }),
}))

const iso = (secondsAgo: number) => new Date(Date.now() - secondsAgo * 1000).toISOString()

/** Advances fake time and lets every microtask/state update it triggers settle before returning - the
 *  interval tick's own `void load()` is fire-and-forget, so a plain advanceTimersByTimeAsync can resolve
 *  before React has actually committed the state update it caused. */
const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms))

/** Everything present: the common shape once a cluster's hardware/flow collector actually supports
 *  bandwidth share and host watts. */
const fullAgent: SelfTelemetryEntity = {
  id: 'agent-1',
  kind: 'agent',
  clusterName: 'edge-cluster',
  samples: [
    { t: iso(60), rssBytes: 90 << 20, goroutines: 40, flowIntervalSeconds: 30, probeIntervalSeconds: 180 },
    {
      t: iso(10),
      rssBytes: 95 << 20,
      goroutines: 42,
      cpuPct: 4.2,
      bandwidthSharePct: 1.75,
      watts: 38.4,
      flowIntervalSeconds: 30,
      probeIntervalSeconds: 180,
    },
  ],
}

const serverEntity: SelfTelemetryEntity = {
  id: 'server',
  kind: 'server',
  samples: [
    { t: iso(60), rssBytes: 150 << 20, goroutines: 80, connectedAgents: 3, flowIngestBytesPerSec: 0 },
    {
      t: iso(10),
      rssBytes: 152 << 20,
      goroutines: 81,
      cpuPct: 7.1,
      connectedAgents: 4,
      flowIngestBytesPerSec: 2048,
      modelCacheHitPct: 92.5,
      gcPauseMsPerSec: 0.8,
    },
  ],
}

/** The common real-hardware case: no RAPL, no flow collector having reported yet, but the agent is
 *  otherwise perfectly healthy and still reports its own configured intervals. */
const bareAgent: SelfTelemetryEntity = {
  id: 'agent-2',
  kind: 'agent',
  clusterName: 'bare-metal',
  samples: [
    { t: iso(60), rssBytes: 70 << 20, goroutines: 20, flowIntervalSeconds: 60, probeIntervalSeconds: 300 },
    { t: iso(10), rssBytes: 71 << 20, goroutines: 21, cpuPct: 1.1, flowIntervalSeconds: 60, probeIntervalSeconds: 300 },
  ],
}

function renderPage() {
  return render(<SystemHealthPage />)
}

describe('SystemHealthPage - full data', () => {
  beforeEach(() => selfTelemetry.mockReset())

  test('renders every entity with its real metrics, config badges, and a live indicator', async () => {
    selfTelemetry.mockResolvedValue([serverEntity, fullAgent])
    renderPage()

    expect(await screen.findByText('Continuum server')).toBeInTheDocument()
    expect(screen.getByText('edge-cluster')).toBeInTheDocument()

    // Both entities' last sample is recent, so both read as live.
    expect(screen.getAllByText('Live')).toHaveLength(2)

    // The agent's bandwidth/watts tiles render as real charts, not "unavailable" placeholders.
    expect(screen.queryByText(/not available/i)).toBeNull()
    expect(screen.getByText('Bandwidth share')).toBeInTheDocument()
    expect(screen.getByText('Host watts')).toBeInTheDocument()
    // The flow/probe interval badges show up as read-only context next to those charts.
    expect(screen.getByText('Flow report every 30s')).toBeInTheDocument()
    expect(screen.getByText('Node probe every 180s')).toBeInTheDocument()

    // The server entity never gets bandwidth/watts tiles at all - it has no cluster of its own.
    const serverCard = screen.getByText('Continuum server').closest('section') as HTMLElement
    expect(within(serverCard).queryByText('Bandwidth share')).toBeNull()
    expect(within(serverCard).queryByText('Host watts')).toBeNull()

    // The server entity gets its own four tiles instead, describing the process itself.
    expect(within(serverCard).getByText('Connected agents')).toBeInTheDocument()
    expect(within(serverCard).getByText('Flow ingest rate')).toBeInTheDocument()
    expect(within(serverCard).getByText('Model cache hit rate')).toBeInTheDocument()
    expect(within(serverCard).getByText('GC pause time')).toBeInTheDocument()

    // An agent entity never gets the server's own four tiles - they describe the process, not a cluster.
    const agentCard = screen.getByText('edge-cluster').closest('section') as HTMLElement
    expect(within(agentCard).queryByText('Connected agents')).toBeNull()
    expect(within(agentCard).queryByText('Flow ingest rate')).toBeNull()
    expect(within(agentCard).queryByText('Model cache hit rate')).toBeNull()
    expect(within(agentCard).queryByText('GC pause time')).toBeNull()
  })

  test('a metric this entity has never reported shows a calm "not available" state, not an empty chart', async () => {
    selfTelemetry.mockResolvedValue([bareAgent])
    renderPage()

    expect(await screen.findByText('bare-metal')).toBeInTheDocument()
    // Still named and charted: memory/goroutines/CPU all work fine on "bare" hardware too.
    expect(screen.getByText('RSS memory')).toBeInTheDocument()

    // Bandwidth/watts are honestly absent, each with its own specific, calm explanation.
    expect(screen.getByText(/no flow collector has reported network throughput/i)).toBeInTheDocument()
    expect(screen.getByText(/none of this cluster's nodes expose an intel rapl/i)).toBeInTheDocument()
    // The config context (interval badges) is still shown even though the reading itself is absent -
    // it's honest "this is how often it WOULD update" context, not a claim that it has one.
    expect(screen.getByText('Flow report every 60s')).toBeInTheDocument()
    expect(screen.getByText('Node probe every 300s')).toBeInTheDocument()
  })

  test('an entity with no sample recently shows Stale rather than Live', async () => {
    const stale: SelfTelemetryEntity = {
      ...bareAgent,
      id: 'agent-3',
      clusterName: 'gone-quiet',
      samples: [{ t: new Date(Date.now() - 30 * 60_000).toISOString(), rssBytes: 1 << 20, goroutines: 1 }],
    }
    selfTelemetry.mockResolvedValue([stale])
    renderPage()
    expect(await screen.findByText(/stale/i)).toBeInTheDocument()
  })

  test('renders an empty state when nothing has been sampled yet, not a blank page', async () => {
    selfTelemetry.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/nothing sampled yet/i)).toBeInTheDocument()
  })

  test('a failed poll keeps the last good render up and shows an error banner, instead of going blank', async () => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      selfTelemetry.mockResolvedValueOnce([fullAgent]).mockRejectedValueOnce(new Error('network hiccup'))
      renderPage()
      await vi.waitFor(() => expect(screen.getByText('edge-cluster')).toBeInTheDocument())

      await tick(15_000) // the next poll fires and fails
      // The previously-rendered entity stays up - a transient hiccup never blanks a working dashboard.
      expect(screen.getByText('edge-cluster')).toBeInTheDocument()
      expect(screen.getByText(/network hiccup/i)).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('SystemHealthPage - polling', () => {
  beforeEach(() => {
    selfTelemetry.mockReset()
    selfTelemetry.mockResolvedValue([serverEntity])
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  test('polls on a coarse, diagnostic interval while the tab is visible, not on every tick', async () => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    renderPage()
    await vi.waitFor(() => expect(selfTelemetry).toHaveBeenCalledTimes(1))

    await tick(5_000)
    expect(selfTelemetry).toHaveBeenCalledTimes(1) // not yet - this page polls coarser than the 5s topology poll

    await tick(10_000) // total 15s
    expect(selfTelemetry).toHaveBeenCalledTimes(2)

    await tick(15_000)
    expect(selfTelemetry).toHaveBeenCalledTimes(3)
  })

  test('skips a poll while the tab is hidden, rather than hammering the server in the background', async () => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    renderPage()
    await vi.waitFor(() => expect(selfTelemetry).toHaveBeenCalledTimes(1))

    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    await tick(30_000)
    expect(selfTelemetry).toHaveBeenCalledTimes(1) // the tab was hidden for both ticks in that window

    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    await tick(15_000)
    expect(selfTelemetry).toHaveBeenCalledTimes(2)
  })

  test('stops polling once the page unmounts', async () => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    const { unmount } = renderPage()
    await vi.waitFor(() => expect(selfTelemetry).toHaveBeenCalledTimes(1))
    unmount()
    await tick(60_000)
    expect(selfTelemetry).toHaveBeenCalledTimes(1)
  })
})

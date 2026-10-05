import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import GuidedWizard from '@/components/telemetry/GuidedWizard'
import { DEFAULT_SETTINGS, type AppSettings } from '@/lib/history'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
import { telemetrySecrets } from '@/lib/consent'
import type { RegionalOperator } from '@/lib/types'

// The Destination step's second shape: each signal type (metrics, logs, traces) to a destination of its own.
// What matters here is what the step offers per type, what it writes into the draft, and that the draft is
// not "ready" until every type that has a signal on has somewhere to go.

let settings: AppSettings
const listOperators = vi.fn(async (): Promise<RegionalOperator[]> => [])
const save = vi.fn(async () => true)

vi.mock('@/store/settings', () => ({ useSettings: () => ({ settings, loaded: true, error: undefined, save }) }))
const CONN = { url: 'https://example.test', org: 'org-1' }
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string }) => unknown) => (selector ? selector({ role: undefined }) : { role: undefined }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, listOperators: (...a: Parameters<typeof listOperators>) => listOperators(...a) } }
})

let latest: TelemetryInput = emptyTelemetry

function Wrapper({ initial = emptyTelemetry }: { initial?: TelemetryInput }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <GuidedWizard value={value} onChange={(v) => { latest = v; setValue(v) }} testIdPrefix="t" runSection={<div data-testid="t-run-section">the command</div>} />
}

const renderWizard = (initial?: TelemetryInput) => render(<MemoryRouter><Wrapper initial={initial} /></MemoryRouter>)

/** To the Destination step with one metrics signal and one logs signal on. */
async function gotoDestination(user: ReturnType<typeof userEvent.setup>, signals: string[] = ['resourceUsage', 'systemLogs']) {
  for (const s of signals) await user.click(screen.getByTestId(`t-${s}`))
  await user.click(screen.getByTestId('t-guided-continue')) // Collect -> Process
  await user.click(screen.getByTestId('t-guided-continue')) // Process -> Destination
}

async function pickInLane(user: ReturnType<typeof userEvent.setup>, lane: string, query: string, key: string) {
  await user.type(screen.getByTestId(`t-lane-${lane}-guided-destination-search`), query)
  await user.click(screen.getByTestId(`t-lane-${lane}-guided-destination-${key}`))
}

beforeEach(() => {
  settings = DEFAULT_SETTINGS
  latest = emptyTelemetry
  listOperators.mockClear()
  listOperators.mockResolvedValue([])
})

describe('GuidedWizard: one destination per signal type', () => {
  test('with a single signal type on there is nothing to split, so no choice is offered', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user, ['resourceUsage'])
    expect(screen.queryByTestId('t-guided-mode')).not.toBeInTheDocument()
  })

  test('with two signal types on, both ways are offered and one destination is where it starts', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-mode-single')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('t-guided-mode-split')).toHaveAttribute('aria-checked', 'false')
    expect(screen.queryByTestId('t-routes')).not.toBeInTheDocument()
    // The heading is the choice's own, not shown twice.
    expect(screen.getAllByText('Where should this telemetry go?')).toHaveLength(1)
  })

  test('splitting gives each signal type its own picker, filtered to what can carry it', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-mode-split'))
    expect(latest.exportSplit).toBe(true)
    expect(screen.getByTestId('t-lane-metrics')).toBeInTheDocument()
    expect(screen.getByTestId('t-lane-logs')).toBeInTheDocument()
    expect(screen.queryByTestId('t-lane-traces')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-lane-metrics-signals')).toHaveTextContent('Resource usage')
    // A logs-only backend can be where logs go, and is not even offered for metrics.
    await user.type(screen.getByTestId('t-lane-logs-guided-destination-search'), 'loki')
    expect(screen.getByTestId('t-lane-logs-guided-destination-external-preset-loki')).toBeInTheDocument()
    await user.type(screen.getByTestId('t-lane-metrics-guided-destination-search'), 'loki')
    const loki = screen.getByTestId('t-lane-metrics-guided-destination-external-preset-loki')
    expect(loki).not.toHaveAttribute('role', 'radio')
    expect(loki).toHaveTextContent('Takes logs only, not metrics.')
  })

  test('each lane is chosen on its own, Review shows where each goes, and the command can then be created', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-mode-split'))
    await pickInLane(user, 'metrics', 'mimir', 'external-preset-mimir')
    expect(screen.getByTestId('t-guided-routes-incomplete')).toHaveTextContent('logs')
    await pickInLane(user, 'logs', 'loki', 'external-preset-loki')
    expect(latest.exportLanes.metrics.exportEndpoint).not.toBe('')
    expect(latest.exportLanes.logs.exportEndpoint).not.toBe('')
    expect(latest.exportLanes.metrics.exportEndpoint).not.toBe(latest.exportLanes.logs.exportEndpoint)
    expect(screen.queryByTestId('t-guided-routes-incomplete')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    const routes = screen.getByTestId('t-review-routes')
    expect(within(routes).getByText('Metrics')).toBeInTheDocument()
    expect(within(routes).getByText('Logs')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-create-command')).toBeEnabled()
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.getByTestId('t-guided-run-summary')).toHaveTextContent('2 signals to 2 destinations, one per signal type')
  })

  test('until every signal type has a destination, nothing can be created and Review says which are missing', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-mode-split'))
    await pickInLane(user, 'metrics', 'mimir', 'external-preset-mimir')
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-create-command')).toBeDisabled()
    expect(screen.getByTestId('t-guided-no-destination')).toHaveTextContent('logs still need a destination')
  })

  test('the destination picked for everything is where every lane starts, and switching back loses nothing', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    const single = latest.exportEndpoint
    expect(single).not.toBe('')
    await user.click(screen.getByTestId('t-guided-mode-split'))
    expect(latest.exportLanes.metrics.exportEndpoint).toBe(single)
    expect(latest.exportLanes.logs.exportEndpoint).toBe(single)
    await user.click(screen.getByTestId('t-guided-mode-single'))
    expect(latest.exportSplit).toBe(false)
    expect(latest.exportEndpoint).toBe(single)
    expect(latest.exportLanes.logs.exportEndpoint).toBe(single)
    expect(screen.queryByTestId('t-routes')).not.toBeInTheDocument()
  })

  test('a credential Secret named for one lane is offered to the other, and shared when taken', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-mode-split'))
    // Name the Secret on the metrics lane only.
    const metrics = screen.getByTestId('t-lane-metrics')
    await user.click(within(metrics).getByText('Connection details'))
    await user.type(within(metrics).getByTestId('t-lane-metrics-export-auth-secret'), 'hc-token')
    expect(latest.exportLanes.metrics.exportAuthSecretName).toBe('hc-token')
    expect(latest.exportLanes.logs.exportAuthSecretName).toBe('')
    await user.click(screen.getByTestId('t-lane-logs-reuse'))
    await waitFor(() => expect(latest.exportLanes.logs.exportAuthSecretName).toBe('hc-token'))
    const secrets = telemetrySecrets(latest)
    expect(secrets).toHaveLength(1)
    expect(secrets[0].lanes).toEqual(['metrics', 'logs'])
  })

  test('an install that already sends signals to several places opens split, with each destination in place', async () => {
    const user = userEvent.setup()
    const lane = (endpoint: string) => ({ ...emptyTelemetry.exportLanes.metrics, exportEndpoint: endpoint })
    renderWizard({ ...emptyTelemetry, resourceUsage: true, systemLogs: true, exportSplit: true, exportRoutesInstalled: true, exportLanes: { metrics: lane('mimir.example:4317'), logs: lane('loki.example:3100'), traces: lane('') } })
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-mode-split')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('t-lane-metrics-guided-destination-endpoint')).toHaveValue('mimir.example:4317')
    expect(screen.getByTestId('t-lane-logs-guided-destination-endpoint')).toHaveValue('loki.example:3100')
  })
})

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import GuidedWizard from '@/components/telemetry/GuidedWizard'
import { DEFAULT_SETTINGS, type AppSettings } from '@/lib/history'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
import { telemetrySecrets } from '@/lib/consent'
import type { FusionStatus } from '@/lib/api'
import type { OperatorDestinationEntry, RegionalOperator } from '@/lib/types'

// The Destination step's second shape: each signal type (metrics, logs, traces) to a destination of its own.
// What matters here is what the step offers per type, what it writes into the draft, and that the draft is
// not "ready" until every type that has a signal on has somewhere to go.

let settings: AppSettings
let role: 'admin' | undefined
const listOperators = vi.fn(async (): Promise<RegionalOperator[]> => [])
const listOperatorDestinations = vi.fn(async (): Promise<OperatorDestinationEntry[]> => [])
// A server that does not run FUSION: no row for it, so what these tests count and pick is only the operators.
const getFusion = vi.fn(async (): Promise<FusionStatus> => ({ available: false, reason: 'not-configured', state: 'off' }))
const save = vi.fn(async () => true)

vi.mock('@/store/settings', () => ({ useSettings: () => ({ settings, loaded: true, error: undefined, save }) }))
const CONN = { url: 'https://example.test', org: 'org-1' }
// The store's conn is one stable function; a fresh one per render would make every polled list read again on every render.
const connFn = () => CONN
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string; conn: () => typeof CONN }) => unknown) => (selector ? selector({ role, conn: connFn }) : { role, conn: connFn }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listOperators: (...a: Parameters<typeof listOperators>) => listOperators(...a),
      listOperatorDestinations: (...a: Parameters<typeof listOperatorDestinations>) => listOperatorDestinations(...a),
      getFusion: (...a: Parameters<typeof getFusion>) => getFusion(...a),
    },
  }
})

let latest: TelemetryInput = emptyTelemetry

function Wrapper({ initial = emptyTelemetry }: { initial?: TelemetryInput }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <GuidedWizard value={value} onChange={(v) => { latest = v; setValue(v) }} testIdPrefix="t" clusterName="vradipus-cluster" review={{ diff: [], installed: false, kept: [] }} runSection={<div data-testid="t-run-section">the command</div>} />
}

const renderWizard = (initial?: TelemetryInput) => render(<MemoryRouter><Wrapper initial={initial} /></MemoryRouter>)

/** To the Destination step with one metrics signal and one logs signal on. */
async function gotoDestination(user: ReturnType<typeof userEvent.setup>, signals: string[] = ['resourceUsage', 'systemLogs']) {
  for (const s of signals) await user.click(screen.getByTestId(`t-${s}`))
  await user.click(screen.getByTestId('t-guided-continue')) // What to collect -> Where to send
}

/** A lane's list is short and has no search box: what is not among the first rows is behind "show more". */
async function rowInLane(user: ReturnType<typeof userEvent.setup>, lane: string, key: string) {
  const id = `t-lane-${lane}-guided-destination-${key}`
  if (!screen.queryByTestId(id)) await user.click(screen.getByTestId(`t-lane-${lane}-guided-destination-more`))
  return screen.getByTestId(id)
}

async function pickInLane(user: ReturnType<typeof userEvent.setup>, lane: string, key: string) {
  await user.click(await rowInLane(user, lane, key))
}

beforeEach(() => {
  settings = DEFAULT_SETTINGS
  role = undefined
  latest = emptyTelemetry
  listOperators.mockClear()
  listOperators.mockResolvedValue([])
})

describe('GuidedWizard: one destination per signal type', () => {
  test('with a single signal type on there is nothing to split, so no choice is offered', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user, ['resourceUsage'])
    expect(screen.queryByTestId('t-guided-split')).not.toBeInTheDocument()
  })

  test('with two signal types on, one destination is where it starts and splitting is one checkbox away', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-split')).not.toBeChecked()
    expect(screen.queryByTestId('t-routes')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-list')).toBeInTheDocument()
    expect(screen.getAllByText('Where should this telemetry go?')).toHaveLength(1)
  })

  test('splitting gives each signal type its own picker, filtered to what can carry it', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-split'))
    expect(latest.exportSplit).toBe(true)
    expect(screen.getByTestId('t-lane-metrics')).toBeInTheDocument()
    expect(screen.getByTestId('t-lane-logs')).toBeInTheDocument()
    expect(screen.queryByTestId('t-lane-traces')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-lane-metrics-signals')).toHaveTextContent('Resource usage')
    // A logs-only backend can be where logs go, and is not even offered for metrics.
    expect(await rowInLane(user, 'logs', 'external-preset-loki')).toHaveAttribute('role', 'radio')
    const loki = await rowInLane(user, 'metrics', 'external-preset-loki')
    expect(loki).not.toHaveAttribute('role', 'radio')
    expect(loki).toHaveTextContent('Does not accept metrics.')
  })

  test('each lane is chosen on its own, Review shows where each goes, and the command can then be created', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-split'))
    await pickInLane(user, 'metrics', 'external-preset-mimir')
    expect(screen.getByTestId('t-guided-why')).toHaveTextContent('Choose where logs should go.')
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    await pickInLane(user, 'logs', 'external-preset-loki')
    expect(latest.exportLanes.metrics.exportEndpoint).not.toBe('')
    expect(latest.exportLanes.logs.exportEndpoint).not.toBe('')
    expect(latest.exportLanes.metrics.exportEndpoint).not.toBe(latest.exportLanes.logs.exportEndpoint)
    expect(screen.queryByTestId('t-guided-why')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-review-changes')).toHaveTextContent(/Sends metrics to .*Mimir.* and logs to .*Loki/)
    expect(screen.getByTestId('t-guided-create-command')).toBeEnabled()
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.getByTestId('t-guided-run-summary')).toHaveTextContent('2 signals to 2 destinations, one per signal type')
  })

  test('until every signal type has a destination, Continue is off and says which are missing', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-split'))
    await pickInLane(user, 'metrics', 'external-preset-mimir')
    // Review cannot be reached with a lane still empty.
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('t-guided-why')).toHaveTextContent('Choose where logs should go.')
  })

  test('the destination picked for everything is where every lane starts, and switching back loses nothing', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    const single = latest.exportEndpoint
    expect(single).not.toBe('')
    await user.click(screen.getByTestId('t-guided-split'))
    expect(latest.exportLanes.metrics.exportEndpoint).toBe(single)
    expect(latest.exportLanes.logs.exportEndpoint).toBe(single)
    await user.click(screen.getByTestId('t-guided-split'))
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
    await user.click(screen.getByTestId('t-guided-split'))
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
    expect(screen.getByTestId('t-guided-split')).toBeChecked()
    expect(screen.getByTestId('t-lane-metrics-guided-destination-custom-endpoint')).toHaveValue('mimir.example:4317')
    expect(screen.getByTestId('t-lane-logs-guided-destination-custom-endpoint')).toHaveValue('loki.example:3100')
  })

  test('a regional operator is a destination for the signal types it takes, and splitting leaves a lane it cannot carry empty', async () => {
    const user = userEvent.setup()
    listOperators.mockResolvedValue([
      { id: 'op-m', orgId: 'org-1', name: 'Metrics operator', status: 'active', sourceClusterIds: [], acceptedModalities: ['metrics'], destination: { kind: 'external', endpoint: 'c:4317' }, createdAt: '2026-01-01T00:00:00Z', createdBy: 'a' },
    ] as RegionalOperator[])
    role = 'admin'
    renderWizard()
    await gotoDestination(user)
    // Picked for everything, it cannot carry logs, so it is not offered as the one destination...
    expect(screen.getByTestId('t-guided-destination-operator-op-m-reason')).toHaveTextContent('Does not accept logs.')
    await user.click(screen.getByTestId('t-guided-split'))
    // ...but is the lone, auto-picked destination for the metrics lane, and is shown unavailable for logs.
    await waitFor(() => expect(screen.getByTestId('t-lane-metrics-guided-destination-name')).toHaveTextContent('Metrics operator'))
    expect(latest.exportLanes.metrics.exportOperatorId).toBe('op-m')
    expect(latest.exportLanes.logs.exportOperatorId).toBe('')
  })
})


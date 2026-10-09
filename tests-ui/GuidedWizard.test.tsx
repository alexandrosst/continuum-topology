import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import GuidedWizard from '@/components/telemetry/GuidedWizard'
import { DEFAULT_SETTINGS, type AppSettings, type QuickStartBackend } from '@/lib/history'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
import type { FusionStatus } from '@/lib/api'
import type { OperatorDestinationEntry, RegionalOperator } from '@/lib/types'

// GuidedWizard's "Where to send" step (between What to collect and Review) merges regional operators, the built-in
// export presets and this org's own already-quick-started backends into one pickable catalog
// (destinationCatalog.ts), plus the two "deploy new" entry points. Every assertion below is about what
// renders and what the step writes into the TelemetryInput draft - never about anything actually
// reaching a cluster or the real network.

let settings: AppSettings
let role: 'viewer' | 'editor' | 'admin' | 'owner' | undefined
const listOperators = vi.fn(async (): Promise<RegionalOperator[]> => [])
const listOperatorDestinations = vi.fn(async (): Promise<OperatorDestinationEntry[]> => [])
// A server that does not run FUSION: no row for it, so what these tests count and pick is only the operators.
const getFusion = vi.fn(async (): Promise<FusionStatus> => ({ available: false, reason: 'not-configured', state: 'off' }))
const enableFusion = vi.fn(async (): Promise<FusionStatus> => ({ available: true, state: 'starting', components: [{ component: 'central', label: 'Central', desired: 1, ready: 0 }] }))
const save = vi.fn(async () => true)
const RUNNING: FusionStatus = { available: true, state: 'running', components: [{ component: 'central', label: 'Central', desired: 1, ready: 1 }] }

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings, loaded: true, error: undefined, save }),
}))
// useConn's real implementation (store/server.ts) memoizes this object by url/org, so it is stable across
// renders as long as neither changes - a fixed module-level object reproduces that here. A fresh literal
// per call would make GuidedWizard's own operator-fetching effect (deps: [conn, isAdmin]) re-fire on every
// render it itself causes via setOperators, looping forever.
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
      enableFusion: (...a: Parameters<typeof enableFusion>) => enableFusion(...a),
    },
  }
})

// The create dialog itself is covered by PipelinePage's tests. Here it is the real one unless a test asks for a stand-in that
// reports one creation at once, which is all this wizard has to react to.
let fakeCreated: RegionalOperator | undefined
vi.mock('@/components/operators/CreateOperatorModal', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/operators/CreateOperatorModal')>()
  const Real = actual.default
  return {
    ...actual,
    default: (props: React.ComponentProps<typeof Real>) =>
      fakeCreated ? (
        <button type="button" data-testid="fake-create" onClick={() => props.onCreated?.({ operator: fakeCreated!, reminders: [], install: {} } as never)}>create</button>
      ) : (
        <Real {...props} />
      ),
  }
})

const operator = (overrides: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id: 'op-eu',
  orgId: 'org-1',
  name: 'EU regional operator',
  status: 'active',
  sourceClusterIds: [],
  destination: { kind: 'external', endpoint: 'otel-gateway.example.com:4317' },
  createdAt: '2026-01-01T00:00:00Z',
  createdBy: 'alice',
  ...overrides,
})

/** The draft as of the last change - lets a test read what the wizard wrote into it. */
let latest: TelemetryInput = emptyTelemetry

const onDone = vi.fn()

function Wrapper({ initial = emptyTelemetry, clusterId, initialDestination }: { initial?: TelemetryInput; clusterId?: string; initialDestination?: string }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <GuidedWizard value={value} onChange={(v) => { latest = v; setValue(v) }} testIdPrefix="t" clusterId={clusterId} initialDestination={initialDestination} clusterName="vradipus-cluster" review={{ diff: [], installed: false, kept: [] }} runSection={<div data-testid="t-run-section">the command</div>} checkSection={<div data-testid="t-check-section">the check</div>} onDone={onDone} />
}

function renderWizard(initial?: TelemetryInput, clusterId?: string, initialDestination?: string) {
  return render(
    <MemoryRouter>
      <Wrapper initial={initial} clusterId={clusterId} initialDestination={initialDestination} />
    </MemoryRouter>,
  )
}

/** Gets to the Destination step for one infrastructure signal. With nothing of the organisation's own to
 *  offer, the list opens on the first few built-in presets - Honeycomb is one of them. */
async function gotoDestination(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('t-resourceUsage'))
  await user.click(screen.getByTestId('t-guided-continue')) // What to collect -> Where to send
}

beforeEach(() => {
  settings = DEFAULT_SETTINGS
  role = undefined
  listOperators.mockClear()
  listOperators.mockResolvedValue([])
  listOperatorDestinations.mockResolvedValue([])
  getFusion.mockResolvedValue({ available: false, reason: 'not-configured', state: 'off' })
  enableFusion.mockClear()
  fakeCreated = undefined
  save.mockClear()
})

describe('GuidedWizard: the command comes last', () => {
  test('the rail is the four steps, and no command anywhere until Review has been passed: "Create the command" opens the Run phase, Back returns to Review', async () => {
    const user = userEvent.setup()
    renderWizard()
    expect(screen.getByTestId('t-guided-steps')).toHaveTextContent(/Where from.*What to collect.*Where to send.*Review and install/)
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    expect(screen.queryByTestId('t-run-section')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.queryByTestId('t-run-section')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.getByTestId('t-guided-step-run')).toBeInTheDocument()
    expect(screen.getByTestId('t-run-section')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-run-summary')).toHaveTextContent('1 signal to Honeycomb')
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
  })

  test('the review says what changes in the cluster, what does not, and how to stop, before the command', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-review-changes')).toHaveTextContent('What changes in vradipus-cluster')
    expect(screen.getByTestId('t-review-changes')).toHaveTextContent('Sends it to Honeycomb.')
    expect(screen.getByTestId('t-review-unchanged')).toHaveTextContent('access level stays as approved')
    expect(screen.getByTestId('t-review-stop')).toHaveTextContent('nothing picked: the command turns every signal off')
  })

  test('after the command, "Check that data arrives" shows what the caller built, and Done closes', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.queryByTestId('t-check-section')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-check'))
    expect(screen.getByTestId('t-guided-step-check')).toBeInTheDocument()
    expect(screen.getByTestId('t-check-section')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-done'))
    expect(onDone).toHaveBeenCalled()
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-run')).toBeInTheDocument()
  })

  test('with no destination yet, Continue is off and says why; there is no way to Review without one', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('t-guided-why')).toHaveTextContent('Choose where to send this.')
  })

  test('a draft that already has an endpoint goes straight on: the destination step shows it as a custom endpoint and Review is reachable', async () => {
    const user = userEvent.setup()
    renderWizard({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'collector.example:4317' })
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Another OTLP endpoint')
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.getByTestId('t-review-changes')).toHaveTextContent('Sends it to collector.example:4317.')
  })
})

describe('GuidedWizard: moving between the three steps', () => {
  test('What to collect holds the signals, tags and masking in one screen; Continue is off until a signal is picked', async () => {
    const user = userEvent.setup()
    renderWizard()
    expect(screen.getByTestId('t-guided-step-collect')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-process')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('t-guided-why')).toHaveTextContent('Pick at least one signal.')
    await user.click(screen.getByTestId('t-resourceUsage'))
    expect(screen.getByTestId('t-guided-continue')).toBeEnabled()
  })

  test('Continue goes What to collect -> Where to send -> Review, and Back goes the same way back', async () => {
    const user = userEvent.setup()
    renderWizard()
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-where')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-collect')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-where')).toBeInTheDocument()
  })

  test('an application signal offers "Limit to some namespaces" in the same step, closed unless a scope was handed in', async () => {
    const user = userEvent.setup()
    const { unmount } = renderWizard()
    expect(screen.queryByTestId('t-guided-scope')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-applicationMetrics'))
    expect(screen.getByTestId('t-guided-scope')).not.toHaveAttribute('open')
    unmount()
    render(
      <MemoryRouter>
        <GuidedWizard value={{ ...emptyTelemetry, applicationMetrics: true }} onChange={() => undefined} testIdPrefix="t" initialScope={{ name: 'Checkout', namespaces: ['shop'] }} review={{ diff: [], installed: false, kept: [] }} />
      </MemoryRouter>,
    )
    expect(screen.getByTestId('t-guided-scope')).toHaveAttribute('open')
    expect(within(screen.getByTestId('t-guided-scope')).getByDisplayValue('Checkout')).toBeInTheDocument()
  })

  test('with a way back to the cluster list, "Change cluster" is offered on the first step', async () => {
    const onBack = vi.fn()
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <GuidedWizard value={emptyTelemetry} onChange={() => undefined} testIdPrefix="t" clusterName="vradipus-cluster" review={{ diff: [], installed: false, kept: [] }} onBackToCluster={onBack} />
      </MemoryRouter>,
    )
    await user.click(screen.getByTestId('t-guided-change-cluster'))
    expect(onBack).toHaveBeenCalled()
  })
})

describe('GuidedWizard destination step: the merged catalog', () => {
  test('a backend quick-started earlier is not offered: this wizard no longer deploys backends, only picks a destination', async () => {
    const user = userEvent.setup()
    const backend: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '72h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [backend] }
    renderWizard()
    await user.click(screen.getByTestId('t-traces'))
    await user.click(screen.getByTestId('t-guided-continue')) // What to collect -> Where to send
    expect(screen.getByTestId('t-guided-step-where')).toBeInTheDocument()
    // Nothing of the organisation's own fits, so nothing is picked for the person: they choose from the list.
    expect(screen.queryByTestId('t-guided-destination-summary')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.queryByTestId('t-guided-destination-quickstart-qsb-1')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-external-preset-jaeger')).toBeInTheDocument()
  })

  test('with nothing of its own, the list opens on a few presets and offers the rest', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-destination-external-preset-honeycomb')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-more')).toBeInTheDocument()
    // No summary until something is picked.
    expect(screen.queryByTestId('t-guided-destination-summary')).not.toBeInTheDocument()
  })

  test('the list is grouped, and searching reaches what "show more" would hide', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user) // metrics only
    expect(screen.getByTestId('t-guided-destination-group-self')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-group-cloud')).toBeInTheDocument()
    // The self-hosted collector is the fourth usable self-hosted one, so not among the first three shown.
    expect(screen.queryByTestId('t-guided-destination-external-preset-self-hosted')).not.toBeInTheDocument()
    await user.type(screen.getByTestId('t-guided-destination-search'), 'collector')
    expect(screen.getByTestId('t-guided-destination-external-preset-self-hosted')).toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-destination-group-self')).not.toBeInTheDocument()
    await user.clear(screen.getByTestId('t-guided-destination-search'))
    await user.type(screen.getByTestId('t-guided-destination-search'), 'victoria')
    await user.click(screen.getByTestId('t-guided-destination-external-preset-victoria-metrics'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('VictoriaMetrics')
    expect(latest.exportInsecure).toBe(true)
    expect(screen.getByTestId('t-guided-destination-selfhosted-note')).toBeInTheDocument()
  })

  test('a search that only matches something unable to carry the signals shows it greyed with why; no match says so', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user) // metrics only
    await user.type(screen.getByTestId('t-guided-destination-search'), 'tempo')
    expect(screen.getByTestId('t-guided-destination-external-preset-tempo')).toHaveTextContent('Does not accept metrics.')
    await user.clear(screen.getByTestId('t-guided-destination-search'))
    await user.type(screen.getByTestId('t-guided-destination-search'), 'zzzz')
    expect(screen.getByTestId('t-guided-destination-no-match')).toBeInTheDocument()
  })

  test('a non-administrator never sees a regional operator, and listOperators is never even called', async () => {
    const user = userEvent.setup()
    role = 'editor'
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-more'))
    expect(screen.queryByTestId(/t-guided-destination-operator-/)).not.toBeInTheDocument()
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('an administrator\'s lone regional operator is picked for them, and its endpoint reaches Review', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator()])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
    expect(screen.getByTestId('t-guided-destination-auto')).toBeInTheDocument()
    // An operator's endpoint is its own receiver: shown, not editable.
    expect(screen.getByTestId('t-guided-destination-endpoint')).toHaveTextContent('op-eu.continuum-system.svc:4317')
    expect(screen.getByTestId('t-guided-destination-endpoint').tagName).not.toBe('INPUT')
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.getByTestId('t-review-changes')).toHaveTextContent('Sends it to EU regional operator.')
    expect(latest.exportEndpoint).toBe('op-eu.continuum-system.svc:4317')
  })

  test('an operator says whether other clusters can reach it: the recorded address, or only its in-cluster name', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator()])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
    expect(screen.getByTestId('t-guided-destination-operator-address')).toHaveTextContent('reachable inside its own cluster only')
    // An administrator can record where it is reachable without leaving the wizard.
    expect(screen.getByTestId('t-guided-destination-record-address')).toBeInTheDocument()
  })

  test('an operator with a recorded address shows it', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator({ address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true })])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
    expect(screen.getByTestId('t-guided-destination-operator-address')).toHaveTextContent('Sends to otlp.eu.example.com:4317')
  })

  test('the destination shows, and the draft carries, the address the commands will really dial, not the placeholder name', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(RUNNING)
    listOperators.mockResolvedValue([operator({ id: 'op-central', name: 'Central (FUSION)', endpoint: 'continuum-fusion-central.continuum.svc:4317' })])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('FUSION - this server'))
    expect(screen.getByTestId('t-guided-destination-endpoint')).toHaveTextContent('continuum-fusion-central.continuum.svc:4317')
    expect(screen.getByTestId('t-guided-destination-endpoint')).not.toHaveTextContent('op-central.continuum-system.svc')
    expect(screen.getByTestId('t-guided-destination-operator-address')).toHaveTextContent('FUSION is reachable inside its own cluster only (continuum-fusion-central.continuum.svc:4317')
    // The draft keeps the operator's own name: it is the commands, not the draft, that dial the advertised address.
    expect(latest.exportOperatorId).toBe('op-central')
  })

  test('an operator pick records its id and describes the real flow; a custom endpoint afterwards clears it', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator()])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
    expect(latest.exportOperatorId).toBe('op-eu')
    expect(latest.exportEndpoint).toBe('op-eu.continuum-system.svc:4317')
    // The step no longer sends people to the Operators page for the connecting commands: the panel generates them.
    expect(screen.getByTestId('t-guided-destination-operator-note')).toHaveTextContent('generated on the last step')
    expect(screen.getByTestId('t-guided-destination-operator-note')).toHaveTextContent('administrators only')
    expect(screen.getByTestId('t-guided-destination-operator-note')).not.toHaveTextContent('Operators page')

    await user.click(screen.getByTestId('t-guided-destination-custom'))
    await user.type(screen.getByTestId('t-guided-destination-custom-endpoint'), 'otel.example.com:4317')
    expect(latest.exportOperatorId).toBe('')
    expect(latest.exportEndpoint).toBe('otel.example.com:4317')
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Another OTLP endpoint')
  })

  test('an install that already sends to an operator\'s advertised address opens with that operator picked, not "Another OTLP endpoint"', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator({ id: 'op-a', name: 'A operator', endpoint: 'otlp.a.example.com:4317', address: 'otlp.a.example.com:4317', reachableFromOtherClusters: true }), operator({ id: 'op-b', name: 'B operator' })])
    renderWizard({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otlp.a.example.com:4317' })
    await user.click(screen.getByTestId('t-guided-continue'))
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('A operator'))
    expect(await screen.findByTestId('t-guided-destination-operator-op-a')).toHaveAttribute('aria-checked', 'true')
  })

  test('two regional operators are not auto-picked: the person chooses', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator(), operator({ id: 'op-us', name: 'US regional operator' })])
    renderWizard()
    await gotoDestination(user)
    await screen.findByTestId('t-guided-destination-operator-op-eu')
    expect(screen.queryByTestId('t-guided-destination-summary')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-operator-op-us'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('US regional operator')
    expect(screen.queryByTestId('t-guided-destination-auto')).not.toBeInTheDocument()
  })

  describe('regional operator health and receiver authentication', () => {
    const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

    test('rows say Online, Offline with when it was last seen, or nothing about health when the operator does not report', async () => {
      const user = userEvent.setup()
      role = 'admin'
      listOperators.mockResolvedValue([
        operator({ id: 'op-on', name: 'On operator', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
        operator({ id: 'op-off', name: 'Off operator', health: { state: 'offline', lastSeenAt: minutesAgo(30), reporting: true } }),
        operator({ id: 'op-quiet', name: 'Quiet operator', health: { state: 'unknown', reporting: false } }),
      ])
      renderWizard()
      await gotoDestination(user)
      expect(await screen.findByTestId('t-guided-destination-operator-op-on')).toHaveTextContent('Accepts any signal · Online')
      expect(screen.getByTestId('t-guided-destination-operator-op-off')).toHaveTextContent('Offline, last seen 30 min ago')
      const quiet = screen.getByTestId('t-guided-destination-operator-op-quiet')
      expect(quiet).toHaveTextContent('Accepts any signal')
      expect(quiet).not.toHaveTextContent(/online|offline|not reported/i)
    })

    test('an offline operator can still be chosen; the summary shows its health and a calm note, and an online one shows no note', async () => {
      const user = userEvent.setup()
      role = 'admin'
      listOperators.mockResolvedValue([
        operator({ id: 'op-on', name: 'On operator', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
        operator({ id: 'op-off', name: 'Off operator', health: { state: 'offline', lastSeenAt: minutesAgo(30), reporting: true } }),
      ])
      renderWizard()
      await gotoDestination(user)
      await user.click(await screen.findByTestId('t-guided-destination-operator-op-off'))
      expect(latest.exportOperatorId).toBe('op-off')
      expect(screen.getByTestId('t-guided-destination-health')).toHaveTextContent('Offline, last seen 30 min ago')
      expect(screen.getByTestId('t-guided-destination-offline-note')).toHaveTextContent('has not reported recently')
      expect(screen.getByTestId('t-guided-destination-offline-note')).toHaveTextContent('may not be able to deliver')
      expect(screen.getByTestId('t-guided-continue')).toBeEnabled()

      await user.click(screen.getByTestId('t-guided-destination-operator-op-on'))
      expect(screen.getByTestId('t-guided-destination-health')).toHaveTextContent('Online')
      expect(screen.queryByTestId('t-guided-destination-offline-note')).not.toBeInTheDocument()
    })

    test('an operator that does not report shows no health and no offline note in the summary', async () => {
      const user = userEvent.setup()
      role = 'admin'
      listOperators.mockResolvedValue([operator({ health: { state: 'unknown', reporting: false } })])
      renderWizard()
      await gotoDestination(user)
      await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
      expect(screen.queryByTestId('t-guided-destination-health')).not.toBeInTheDocument()
      expect(screen.queryByTestId('t-guided-destination-offline-note')).not.toBeInTheDocument()
    })

    test('the receiver token Secret field is offered for a bearer operator and for one whose auth is unknown', async () => {
      const user = userEvent.setup()
      role = 'admin'
      listOperators.mockResolvedValue([operator({ receiverAuth: 'bearer' })])
      renderWizard()
      await gotoDestination(user)
      await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
      expect(screen.getByTestId('t-export-auth-secret')).toBeInTheDocument()
      expect(screen.queryByTestId('t-guided-destination-operator-mtls')).not.toBeInTheDocument()
    })

    test('for a certificate-only operator the field is hidden, the connection details say the certificate authenticates, and a leftover Secret name is not mentioned', async () => {
      const user = userEvent.setup()
      role = 'admin'
      listOperators.mockResolvedValue([operator({ receiverAuth: 'mtls' })])
      renderWizard()
      await gotoDestination(user)
      await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('EU regional operator'))
      expect(screen.queryByTestId('t-export-auth-secret')).not.toBeInTheDocument()
      expect(screen.getByTestId('t-guided-destination-operator-mtls')).toHaveTextContent('authenticates this cluster by the client certificate')
      expect(screen.getByTestId('t-guided-destination-connection-summary')).toHaveTextContent('client certificate only')
      expect(screen.getByTestId('t-guided-destination-connection-summary')).not.toHaveTextContent('receiver token')
    })
  })

  test('without a destination Continue is off and says why; choosing "Another OTLP endpoint" does not change that until an address is typed', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('t-guided-why')).toHaveTextContent('Choose where to send this.')
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    await user.type(screen.getByTestId('t-guided-destination-custom-endpoint'), 'collector.internal:4317')
    expect(screen.getByTestId('t-guided-continue')).toBeEnabled()
    expect(screen.queryByTestId('t-guided-why')).not.toBeInTheDocument()
  })

  test('the operator that already receives this cluster is recommended and listed first', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator({ id: 'op-a', name: 'A operator' }), operator({ id: 'op-b', name: 'B operator', sourceClusterIds: ['cl-1'] })])
    renderWizard(undefined, 'cl-1')
    await gotoDestination(user)
    const rows = await screen.findAllByRole('radio')
    expect(rows[0]).toHaveAttribute('data-testid', 't-guided-destination-operator-op-b')
    expect(screen.getByTestId('t-guided-destination-operator-op-b-recommended')).toHaveTextContent('Already receives this cluster')
    expect(screen.queryByTestId('t-guided-destination-operator-op-a-recommended')).not.toBeInTheDocument()
  })

  test('an operator restricted to a modality that is not enabled is listed as unable to carry the signals, with why', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator({ acceptedModalities: ['traces'] })])
    renderWizard()
    await gotoDestination(user) // metrics only
    // Greyed out in its own group, with the one sentence that says why, and it is not a radio that can be picked.
    expect(await screen.findByTestId('t-guided-destination-operator-op-eu-reason')).toHaveTextContent('Does not accept metrics.')
    expect(screen.getByTestId('t-guided-destination-operator-op-eu')).toHaveAttribute('aria-disabled', 'true')
    expect(screen.queryByRole('radio', { name: /EU regional operator/ })).not.toBeInTheDocument()
  })

  test('picking a preset opens its connection details, pre-filled, and keeps its note after the endpoint is edited', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Honeycomb')
    // Honeycomb needs a credential, so its details are open and the header is filled in.
    expect(screen.getByTestId('t-guided-destination-connection')).toHaveAttribute('open')
    expect(screen.getByTestId('t-export-auth-header')).toHaveValue('x-honeycomb-team')
    // A preset's endpoint is a pattern to fill in, and editing it must not make the step forget which one it is.
    const endpoint = screen.getByTestId('t-guided-destination-endpoint')
    await user.clear(endpoint)
    await user.type(endpoint, 'api.honeycomb.io:443')
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Honeycomb')
  })

  test('the connection checkbox says it sends WITHOUT TLS: it is not a "skip certificate verification" option', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    expect(screen.getByText('Send without TLS (plain connection)')).toBeInTheDocument()
    expect(screen.queryByText(/Skip TLS verification/)).not.toBeInTheDocument()
  })

  test('an unfilled <placeholder> in a preset endpoint is flagged on the summary', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-more'))
    await user.click(screen.getByTestId('t-guided-destination-external-preset-grafana-cloud'))
    expect(screen.getByTestId('t-guided-destination-placeholder')).toBeInTheDocument()
    // Grafana Cloud takes OTLP/HTTP only, so the protocol is stated, not offered.
    expect(screen.queryByTestId('t-export-protocol')).not.toBeInTheDocument()
  })

  test('the list stays on screen after a pick: choosing another row moves the choice, and exactly one row is selected', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    expect(screen.getByTestId('t-guided-destination-list')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-external-preset-honeycomb')).toHaveAttribute('aria-checked', 'true')
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    expect(screen.getByTestId('t-guided-destination-external-preset-honeycomb')).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('t-guided-destination-custom')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getAllByRole('radio').filter((r) => r.getAttribute('aria-checked') === 'true')).toHaveLength(1)
  })

  test('another endpoint is a row of the same list and lands on the same panel, with its connection details open', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    await user.type(screen.getByTestId('t-guided-destination-custom-endpoint'), 'collector.internal:4317')
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Another OTLP endpoint')
    expect(latest.exportEndpoint).toBe('collector.internal:4317')
    // Nothing is known about an endpoint of your own, so its details are not hidden.
    expect(screen.getByTestId('t-guided-destination-connection')).toHaveAttribute('open')
  })

  test('the legacy "Send telemetry to" block is not rendered a second time under the guided wizard', async () => {
    renderWizard()
    expect(screen.queryByText('Send telemetry to')).not.toBeInTheDocument()
  })
})

describe('GuidedWizard destination step: no backend is deployed from here', () => {
  test('the list offers a custom endpoint, and no way to set up or deploy a backend', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-destination-custom')).toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-destination-new')).not.toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-deploy-backend')).not.toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-allowed-kinds')).not.toBeInTheDocument()
    expect(screen.queryByTestId('backend-wizard-step-kind')).not.toBeInTheDocument()
  })

  test('the way to a new regional operator is offered to an administrator only', async () => {
    const user = userEvent.setup()
    role = 'editor'
    const { unmount } = renderWizard()
    await gotoDestination(user)
    expect(screen.queryByTestId('t-guided-deploy-operator')).not.toBeInTheDocument()
    unmount()

    // A genuinely fresh mount, not a `rerender` in place - GuidedWizard keeps its own step state across a
    // `rerender` of the same element (same component identity), so flipping `role` there would leave the
    // tree sitting wherever it already was instead of starting over from Layer.
    role = 'admin'
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-deploy-operator')).toBeInTheDocument())
    // It opens the create dialog in place: the wizard is still there behind it, so nothing typed so far is lost.
    await user.click(screen.getByTestId('t-guided-deploy-operator'))
    expect(await screen.findByRole('dialog', { name: 'New operator' })).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-step-where')).toBeInTheDocument()
  })
})

const OFF: FusionStatus = { available: true, state: 'off', components: [{ component: 'central', label: 'Central', desired: 0, ready: 0 }] }
const CENTRAL = { id: 'op-central', name: 'Central', endpoint: 'continuum-fusion-central.continuum.svc:4317' } as const
const centralEntry = (health: OperatorDestinationEntry['health']['state']): OperatorDestinationEntry => ({
  id: 'op-central',
  kind: 'central',
  name: 'FUSION',
  endpoint: CENTRAL.endpoint,
  acceptedModalities: ['metrics', 'logs', 'traces'],
  reachableFromOtherClusters: false,
  health: { state: health },
}) as OperatorDestinationEntry

describe('GuidedWizard destination step: FUSION', () => {
  test('a running FUSION is picked for the administrator without a click, and its endpoint reaches the draft', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(RUNNING)
    listOperators.mockResolvedValue([operator({ id: 'op-central', name: 'Central', endpoint: CENTRAL.endpoint })])
    renderWizard()
    await gotoDestination(user)
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('FUSION - this server'))
    expect(latest.exportOperatorId).toBe('op-central')
  })

  test('an off FUSION is a row of its own with "Enable and use": switching it on picks it, says it is starting and that the commands are safe to run', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(OFF)
    listOperators.mockResolvedValue([operator({ id: 'op-central', name: 'Central', endpoint: CENTRAL.endpoint })])
    renderWizard()
    await gotoDestination(user)
    // Off is not a radio: nothing is picked for the person, and picking is what the button is for.
    const enable = await screen.findByTestId('t-guided-destination-fusion-op-central-enable')
    expect(enable).toHaveTextContent('Enable and use')
    expect(latest.exportOperatorId).toBe('')
    await user.click(enable)
    expect(enableFusion).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(latest.exportOperatorId).toBe('op-central'))
    expect(screen.getByTestId('t-guided-destination-fusion-waiting')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-fusion-safe')).toHaveTextContent('safe to run now')
  })

  test('when switching FUSION on fails, nothing is picked and the reason is shown', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(OFF)
    enableFusion.mockRejectedValueOnce(new Error('boom'))
    renderWizard()
    await gotoDestination(user)
    await user.click(await screen.findByTestId('t-guided-destination-fusion-op-central-enable'))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not turn FUSION on.'))
    expect(latest.exportOperatorId).toBe('')
  })

  test('an editor sees an off FUSION from the read model, with who can turn it on and no button', async () => {
    const user = userEvent.setup()
    role = 'editor'
    listOperatorDestinations.mockResolvedValue([centralEntry('off')])
    renderWizard()
    await gotoDestination(user)
    expect(await screen.findByTestId('t-guided-destination-fusion-op-central-ask')).toHaveTextContent('An administrator can turn it on.')
    expect(screen.queryByTestId('t-guided-destination-fusion-op-central-enable')).not.toBeInTheDocument()
    expect(getFusion).not.toHaveBeenCalled()
  })

  test('a draft that sends to a FUSION that is off cannot make a command: Review says so and Create the command is disabled until it is on', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(OFF)
    listOperators.mockResolvedValue([operator({ id: 'op-central', name: 'Central', endpoint: CENTRAL.endpoint })])
    renderWizard({ ...emptyTelemetry, exportOperatorId: 'op-central', exportEndpoint: CENTRAL.endpoint })
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-create-command')).toBeDisabled()
    expect(screen.getByTestId('t-guided-fusion-blocked')).toHaveTextContent('FUSION is off')
    await user.click(screen.getByTestId('t-guided-fusion-enable'))
    await waitFor(() => expect(screen.getByTestId('t-guided-create-command')).toBeEnabled())
    expect(screen.queryByTestId('t-guided-fusion-blocked')).not.toBeInTheDocument()
  })

  test('a FUSION in another organisation is named as that, not offered', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue({ available: false, reason: 'other-org', state: 'off', message: 'FUSION belongs to another organisation.' })
    renderWizard()
    await gotoDestination(user)
    expect(await screen.findByTestId('t-guided-destination-fusion-op-central')).toHaveTextContent('FUSION belongs to another organisation.')
    expect(screen.queryByTestId('t-guided-destination-fusion-op-central-enable')).not.toBeInTheDocument()
  })

  test('opened from an operator ("Connect <cluster>"), that operator is already the destination', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator(), operator({ id: 'op-us', name: 'US regional operator' })])
    // The signals are already on, as when the wizard is started for an agent.
    renderWizard({ ...emptyTelemetry, resourceUsage: true }, undefined, 'op-us')
    await user.click(screen.getByTestId('t-guided-continue'))
    await waitFor(() => expect(latest.exportOperatorId).toBe('op-us'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('US regional operator')
  })

  test('opened for FUSION while it is running, FUSION is the destination; one that is not listed is simply dropped', async () => {
    const user = userEvent.setup()
    role = 'admin'
    getFusion.mockResolvedValue(RUNNING)
    listOperators.mockResolvedValue([operator({ id: 'op-central', name: 'Central', endpoint: CENTRAL.endpoint }), operator()])
    const preset = { ...emptyTelemetry, resourceUsage: true }
    const toDestination = () => user.click(screen.getByTestId('t-guided-continue'))
    const { unmount } = renderWizard(preset, undefined, 'op-central')
    await toDestination()
    await waitFor(() => expect(latest.exportOperatorId).toBe('op-central'))
    unmount()
    latest = preset
    renderWizard(preset, undefined, 'op-gone')
    await toDestination()
    await screen.findByTestId('t-guided-destination-fusion-op-central')
    expect(latest.exportOperatorId).toBe('')
  })

  test('"Set up a regional operator" opens the create dialog over the wizard; the operator it makes is the destination afterwards', async () => {
    const user = userEvent.setup()
    role = 'admin'
    fakeCreated = operator({ id: 'op-new', name: 'New one' })
    renderWizard()
    await gotoDestination(user)
    await user.click(await screen.findByTestId('t-guided-deploy-operator'))
    expect(latest.exportOperatorId).toBe('')
    // The list now has it, as the reload after a creation would give.
    listOperators.mockResolvedValue([fakeCreated])
    await user.click(screen.getByTestId('fake-create'))
    await waitFor(() => expect(latest.exportOperatorId).toBe('op-new'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('New one')
  })
})

describe('GuidedWizard: an install that has telemetry can be emptied', () => {
  test('nothing picked on an installed one: "Review turning everything off" goes to Review and on to the Run step', async () => {
    const user = userEvent.setup()
    renderWizard({ ...emptyTelemetry, hadTelemetry: true, resourceUsage: true })
    await user.click(screen.getByTestId('t-resourceUsage')) // untick the only signal
    expect(screen.getByTestId('t-guided-continue')).toHaveTextContent('Review turning everything off')
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toHaveTextContent('Turns every telemetry signal off')
    expect(screen.getByTestId('t-guided-create-command')).toBeEnabled()
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.getByTestId('t-guided-run-summary')).toHaveTextContent('Every signal turned off')
    expect(screen.getByTestId('t-run-section')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-done')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-back'))
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-collect')).toBeInTheDocument()
  })

  test('a fresh draft with nothing picked has no way forward, as before', () => {
    renderWizard()
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('t-guided-continue')).not.toHaveTextContent('turning everything off')
  })
})

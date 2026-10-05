import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import GuidedWizard from '@/components/telemetry/GuidedWizard'
import { DEFAULT_SETTINGS, type AppSettings, type QuickStartBackend } from '@/lib/history'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
import { quickStartSpec } from '@/lib/quickStartBackends'
import type { RegionalOperator } from '@/lib/types'

// GuidedWizard's destination step (between Scope and Review) merges regional operators, the built-in
// export presets and this org's own already-quick-started backends into one pickable catalog
// (destinationCatalog.ts), plus the two "deploy new" entry points. Every assertion below is about what
// renders and what the step writes into the TelemetryInput draft - never about anything actually
// reaching a cluster or the real network.

let settings: AppSettings
let role: 'viewer' | 'editor' | 'admin' | 'owner' | undefined
const listOperators = vi.fn(async (): Promise<RegionalOperator[]> => [])
const save = vi.fn(async () => true)

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings, loaded: true, error: undefined, save }),
}))
// useConn's real implementation (store/server.ts) memoizes this object by url/org, so it is stable across
// renders as long as neither changes - a fixed module-level object reproduces that here. A fresh literal
// per call would make GuidedWizard's own operator-fetching effect (deps: [conn, isAdmin]) re-fire on every
// render it itself causes via setOperators, looping forever.
const CONN = { url: 'https://example.test', org: 'org-1' }
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string }) => unknown) => (selector ? selector({ role }) : { role }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, listOperators: (...a: Parameters<typeof listOperators>) => listOperators(...a) } }
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

function Wrapper({ initial = emptyTelemetry, clusterId }: { initial?: TelemetryInput; clusterId?: string }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <GuidedWizard value={value} onChange={(v) => { latest = v; setValue(v) }} testIdPrefix="t" clusterId={clusterId} runSection={<div data-testid="t-run-section">the command</div>} />
}

function renderWizard(initial?: TelemetryInput, clusterId?: string) {
  return render(
    <MemoryRouter>
      <Wrapper initial={initial} clusterId={clusterId} />
    </MemoryRouter>,
  )
}

/** Walks from the landing step to the Kind screen for infrastructure/metrics, the same path
 *  TelemetryFields.test.tsx's own gotoModality helper takes. */
async function gotoKind(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('t-guided-layer-infrastructure'))
  await user.click(screen.getByTestId('t-guided-modality-metrics'))
}

/** Gets to the Destination step for one infrastructure signal. With nothing of the organisation's own to
 *  offer, the list opens on the first few built-in presets - Honeycomb is one of them. */
async function gotoDestination(user: ReturnType<typeof userEvent.setup>) {
  await gotoKind(user)
  await user.click(screen.getByTestId('t-resourceUsage'))
  await user.click(screen.getByTestId('t-guided-continue'))
}

beforeEach(() => {
  settings = DEFAULT_SETTINGS
  role = undefined
  listOperators.mockClear()
  listOperators.mockResolvedValue([])
  save.mockClear()
})

describe('GuidedWizard: the command comes last', () => {
  test('no command anywhere until Review has been passed: "Create the command" opens the Run step, Back returns to Review', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    expect(screen.queryByTestId('t-run-section')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.queryByTestId('t-run-section')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-create-command'))
    expect(screen.getByTestId('t-guided-step-run')).toBeInTheDocument()
    expect(screen.getByTestId('t-run-section')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-run-summary')).toHaveTextContent('1 signal to api.honeycomb.io:443')
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
  })

  test('with no destination yet, "Create the command" is disabled and Review offers to choose one', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-continue')) // continue without choosing one
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-create-command')).toBeDisabled()
    expect(screen.getByTestId('t-guided-no-destination')).toBeInTheDocument()
  })
})

describe('GuidedWizard destination step: reachability', () => {
  test('Continue from Kind (no scope needed) lands on Destination, and Continue from there lands on Review', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
  })

  test('Back from Destination returns to Kind; Back from Review returns to Destination', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-kind')).toBeInTheDocument()

    await user.click(screen.getByTestId('t-guided-continue')) // back to Destination
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue')) // on to Review
    expect(screen.getByTestId('t-guided-step-review')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
  })

  test('an application modality (needs scope) reaches Destination via Scope\'s own Continue, and Back from Destination returns to Scope', async () => {
    const user = userEvent.setup()
    renderWizard()
    await user.click(screen.getByTestId('t-guided-layer-application'))
    await user.click(screen.getByTestId('t-guided-modality-metrics'))
    expect(screen.getByTestId('t-guided-step-scope')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-back'))
    expect(screen.getByTestId('t-guided-step-scope')).toBeInTheDocument()
  })
})

describe('GuidedWizard destination step: the merged catalog', () => {
  test('an organisation\'s own backend leads, and the built-in presets wait behind "show more"', async () => {
    const user = userEvent.setup()
    const backend: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '72h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [backend] }
    renderWizard()
    // application/traces is a 1:1 modality match (see GuidedWizard.tsx) and is itself application-scoped,
    // so picking it lands straight on Scope; its own Continue is what reaches Destination from there.
    await user.click(screen.getByTestId('t-guided-layer-application'))
    await user.click(screen.getByTestId('t-guided-modality-traces'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
    // The one destination of the organisation's own that fits is picked for it, and the summary says so.
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Jaeger (traces)')
    expect(screen.getByTestId('t-guided-destination-auto')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-change'))
    expect(screen.getByTestId('t-guided-destination-quickstart-qsb-1')).toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-destination-external-preset-honeycomb')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-more'))
    expect(screen.getByTestId('t-guided-destination-external-preset-honeycomb')).toBeInTheDocument()
    // Jaeger (traces only) is also a compatible preset once only traces is on.
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
    expect(screen.getByTestId('t-guided-destination-external-preset-tempo')).toHaveTextContent('Takes traces only, not metrics.')
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
    expect(screen.getByTestId('t-review-pipeline')).toHaveTextContent('op-eu.continuum-system.svc:4317')
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
    expect(screen.getByTestId('t-guided-destination-operator-note')).toHaveTextContent('generated on the wizard’s last step')
    expect(screen.getByTestId('t-guided-destination-operator-note')).toHaveTextContent('administrators only')
    expect(screen.getByTestId('t-guided-destination-operator-note')).not.toHaveTextContent('Operators page')
    expect(screen.getByTestId('t-guided-destination-next')).toHaveTextContent('the server generates it')
    expect(screen.getByTestId('t-guided-destination-next')).not.toHaveTextContent('You run it in the cluster yourself')

    await user.click(screen.getByTestId('t-guided-destination-change'))
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    await user.type(screen.getByTestId('t-guided-destination-custom-endpoint'), 'otel.example.com:4317')
    await user.click(screen.getByTestId('t-guided-destination-custom-use'))
    expect(latest.exportOperatorId).toBe('')
    expect(screen.getByTestId('t-guided-destination-next')).toHaveTextContent('You run it in the cluster yourself')
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

      await user.click(screen.getByTestId('t-guided-destination-change'))
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
      expect(screen.getByTestId('t-guided-destination-connection')).toHaveTextContent('client certificate only')
      expect(screen.getByTestId('t-guided-destination-next')).not.toHaveTextContent('receiver token')
    })
  })

  test('Continue is never blocked: without a destination, Review says so and offers the way back', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.getByTestId('t-guided-continue')).toBeEnabled()
    expect(screen.getByTestId('t-guided-destination-skip-note')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-no-destination')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-choose-destination'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
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
    const toggle = await screen.findByTestId('t-guided-destination-unavailable-toggle')
    expect(screen.queryByTestId('t-guided-destination-operator-op-eu')).not.toBeInTheDocument()
    await user.click(toggle)
    expect(screen.getByTestId('t-guided-destination-operator-op-eu')).toHaveTextContent('Takes traces only')
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

  test('Change returns to the list without losing the current choice, and "Keep" goes back to it', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-destination-change'))
    expect(screen.getByTestId('t-guided-destination-list')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-keep'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Honeycomb')
  })

  test('a custom endpoint is one button away and lands on the same summary', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-custom'))
    expect(screen.getByTestId('t-guided-destination-custom-use')).toBeDisabled()
    await user.type(screen.getByTestId('t-guided-destination-custom-endpoint'), 'collector.internal:4317')
    await user.click(screen.getByTestId('t-guided-destination-custom-use'))
    expect(screen.getByTestId('t-guided-destination-name')).toHaveTextContent('Custom endpoint')
    expect(screen.getByTestId('t-guided-destination-endpoint')).toHaveValue('collector.internal:4317')
    // Details start closed for something that was never said to need a credential.
    expect(screen.getByTestId('t-guided-destination-connection')).not.toHaveAttribute('open')
  })

  test('the legacy "Send telemetry to" block is not rendered a second time under the guided wizard', async () => {
    renderWizard()
    expect(screen.queryByText('Send telemetry to')).not.toBeInTheDocument()
  })
})

describe('GuidedWizard destination step: setting up a new destination', () => {
  test('"Set up a new destination" opens the panel, and "All destinations" returns to the list', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-new'))
    expect(screen.getByTestId('t-guided-destination-new-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-destination-list')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-back-to-list'))
    expect(screen.getByTestId('t-guided-destination-list')).toBeInTheDocument()
  })

  test('"A new backend" launches the existing TelemetryBackendWizard', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoDestination(user)
    expect(screen.queryByTestId('backend-wizard-step-kind')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-destination-new'))
    await user.click(screen.getByTestId('t-guided-deploy-backend'))
    expect(screen.getByTestId('backend-wizard-step-kind')).toBeInTheDocument()
  })

  test('a regional operator and the allowed-kinds control are offered to an administrator only', async () => {
    const user = userEvent.setup()
    role = 'editor'
    const { unmount } = renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-new'))
    expect(screen.queryByTestId('t-guided-deploy-operator')).not.toBeInTheDocument()
    expect(screen.queryByTestId('t-guided-allowed-kinds')).not.toBeInTheDocument()
    unmount()

    // A genuinely fresh mount, not a `rerender` in place - GuidedWizard keeps its own step state across a
    // `rerender` of the same element (same component identity), so flipping `role` there would leave the
    // tree sitting wherever it already was instead of starting over from Layer.
    role = 'admin'
    renderWizard()
    await gotoDestination(user)
    await user.click(screen.getByTestId('t-guided-destination-new'))
    await waitFor(() => expect(screen.getByTestId('t-guided-deploy-operator')).toBeInTheDocument())
    expect(screen.getByTestId('t-guided-deploy-operator')).toHaveAttribute('href', '/operators')
    expect(screen.getByTestId('t-guided-allowed-kinds')).toBeInTheDocument()
  })

  test('after setting up a backend, the wizard lands back on the summary with it already picked', async () => {
    const user = userEvent.setup()
    role = 'admin'
    renderWizard()
    await gotoDestination(user) // metrics only, so a Prometheus backend can carry it
    await user.click(screen.getByTestId('t-guided-destination-new'))
    await user.click(screen.getByTestId('t-guided-deploy-backend'))
    await user.click(screen.getByTestId('backend-wizard-kind-prometheus'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    await user.click(screen.getByTestId('backend-wizard-save'))
    await waitFor(() => expect(save).toHaveBeenCalled())
    const saved = (save.mock.calls[0] as unknown as [unknown, AppSettings])[1].quickStartBackends[0]
    await waitFor(() => expect(screen.getByTestId('t-guided-destination-summary')).toBeInTheDocument())
    expect(screen.getByTestId('t-guided-destination-endpoint')).toHaveValue(quickStartSpec('prometheus').exportEndpoint(saved.namespace))
  })
})

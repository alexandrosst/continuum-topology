import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import GuidedWizard from '@/components/telemetry/GuidedWizard'
import { DEFAULT_SETTINGS, type AppSettings, type QuickStartBackend } from '@/lib/history'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
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

function Wrapper({ initial = emptyTelemetry }: { initial?: TelemetryInput }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <GuidedWizard value={value} onChange={setValue} testIdPrefix="t" />
}

function renderWizard(initial?: TelemetryInput) {
  return render(
    <MemoryRouter>
      <Wrapper initial={initial} />
    </MemoryRouter>,
  )
}

/** Walks from the landing step to the Kind screen for infrastructure/metrics, the same path
 *  TelemetryFields.test.tsx's own gotoModality helper takes. */
async function gotoKind(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('t-guided-layer-infrastructure'))
  await user.click(screen.getByTestId('t-guided-modality-metrics'))
}

beforeEach(() => {
  settings = DEFAULT_SETTINGS
  role = undefined
  listOperators.mockClear()
  listOperators.mockResolvedValue([])
  save.mockClear()
})

describe('GuidedWizard destination step: reachability', () => {
  test('Continue from Kind (no scope needed) lands on Destination, and Continue from there lands on Review', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-guided-step-destination')).toBeInTheDocument()
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
  test('renders a known preset and an already-quick-started backend, filtered by what is enabled', async () => {
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
    expect(screen.getByTestId('t-guided-destination-external-preset-honeycomb')).toBeInTheDocument()
    expect(screen.getByTestId('t-guided-destination-quickstart-qsb-1')).toBeInTheDocument()
    // Jaeger (traces only) is also a compatible preset once only traces is on.
    expect(screen.getByTestId('t-guided-destination-external-preset-jaeger')).toBeInTheDocument()
  })

  test('a non-administrator never sees a regional-operator card, and listOperators is never even called', async () => {
    const user = userEvent.setup()
    role = 'editor'
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.queryByTestId(/t-guided-destination-operator-/)).not.toBeInTheDocument()
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('an administrator sees a fetched regional operator as a card, and picking it fills the raw endpoint shown on Review', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator()])
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    const card = await screen.findByTestId('t-guided-destination-operator-op-eu')
    await user.click(card)
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-review-pipeline')).toHaveTextContent('op-eu.continuum-system.svc:4317')
  })

  test('an operator restricted to a modality that is not enabled is shown disabled, not hidden', async () => {
    const user = userEvent.setup()
    role = 'admin'
    listOperators.mockResolvedValue([operator({ acceptedModalities: ['traces'] })])
    renderWizard()
    await gotoKind(user) // metrics only
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    const card = await screen.findByTestId('t-guided-destination-operator-op-eu')
    expect(card).toBeDisabled()
  })

  test('picking a preset card fills the endpoint, shown on Review by its own label', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    await user.click(screen.getByTestId('t-guided-destination-external-preset-honeycomb'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.getByTestId('t-review-pipeline')).toHaveTextContent('Honeycomb')
  })
})

describe('GuidedWizard destination step: deploy entry points', () => {
  test('"Deploy a new backend" launches the existing TelemetryBackendWizard', async () => {
    const user = userEvent.setup()
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.queryByTestId('backend-wizard-step-kind')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-deploy-backend'))
    expect(screen.getByTestId('backend-wizard-step-kind')).toBeInTheDocument()
  })

  test('"Deploy a new regional operator" only appears for an administrator, and links to /operators', async () => {
    const user = userEvent.setup()
    role = 'editor'
    const { unmount } = renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(screen.queryByTestId('t-guided-deploy-operator')).not.toBeInTheDocument()
    unmount()

    // A genuinely fresh mount, not a `rerender` in place - GuidedWizard keeps its own step state across a
    // `rerender` of the same element (same component identity), so flipping `role` there would leave the
    // tree sitting wherever it already was instead of starting over from Layer.
    role = 'admin'
    renderWizard()
    await gotoKind(user)
    await user.click(screen.getByTestId('t-resourceUsage'))
    await user.click(screen.getByTestId('t-guided-continue'))
    await waitFor(() => expect(screen.getByTestId('t-guided-deploy-operator')).toBeInTheDocument())
    expect(screen.getByTestId('t-guided-deploy-operator')).toHaveAttribute('href', '/operators')
  })
})

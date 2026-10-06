import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import RegionalOperatorsPage from '@/pages/RegionalOperatorsPage'
import type { FusionStatus, OperatorHeartbeatEnabled } from '@/lib/api'
import type { Agent, Cluster, RegionalOperator } from '@/lib/types'

function renderPage() {
  return render(
    <MemoryRouter>
      <RegionalOperatorsPage />
    </MemoryRouter>,
  )
}

const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', ...over }) as Agent

// A page test: everything RegionalOperatorsPage reads from the shared stores or the API is faked here,
// the same way tests-ui/NamespacesPage.test.tsx exercises its own page in isolation.
let topologyState: { agents: Agent[]; clusters: Cluster[] }
let admin = true
let canEditFlag = true

const listOperators = vi.fn(async (): Promise<RegionalOperator[]> => [])
const createOperator = vi.fn(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }, _options?: { heartbeat?: boolean; labels?: { key: string; value: string }[] }) => ({
  operator: {
    id: 'op-1', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination,
    createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  },
  token: 'shown-once-secret',
  install: 'helm install op-1 ./continuum-regional-operator-0.1.0.tgz \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=backend.example.com:4317',
  secretCommand: 'kubectl create secret generic op-1-receiver-auth --namespace continuum-system --from-literal=token=shown-once-secret',
  reminders: [] as string[],
}))
const enableOperatorHeartbeat = vi.fn(async (_c: unknown, _id: string): Promise<OperatorHeartbeatEnabled> => ({
  operator: {} as RegionalOperator,
  rotated: false,
  heartbeatToken: 'cnh_secret',
  heartbeatSecretCommand: 'kubectl create secret generic op-1-heartbeat-auth --namespace continuum-system --from-literal=token=cnh_secret',
  heartbeatUpgradeCommand: 'helm upgrade op-1 ./chart.tgz --namespace continuum-system --reuse-values --set heartbeat.enabled=true',
  heartbeatUrl: 'https://continuum.example.com/api/v1/operator-heartbeat',
  heartbeatIntervalSeconds: 60,
}))
const parts = (desired: number, ready: number) =>
  (['metrics', 'logs', 'traces', 'central'] as const).map((component) => ({ component, label: { metrics: 'Prometheus', logs: 'Loki', traces: 'Tempo', central: 'Central operator' }[component], desired, ready }))
const central = { operatorId: 'op-central', endpoint: 'continuum-fusion-central.continuum.svc:4317', exposed: false, exists: true }
const fusionOff = (): FusionStatus => ({ available: true, state: 'off', components: parts(0, 0), central })
const fusionStarting = (): FusionStatus => ({ available: true, state: 'starting', components: parts(1, 1).map((c, i) => (i < 2 ? c : { ...c, ready: 0 })), central })
const fusionRunning = (): FusionStatus => ({ available: true, state: 'running', components: parts(1, 1), central })
let fusionStatus: FusionStatus = fusionOff()
const getFusion = vi.fn(async () => fusionStatus)
const enableFusion = vi.fn(async () => (fusionStatus = fusionStarting()))
const disableFusion = vi.fn(async () => (fusionStatus = fusionOff()))
const revokeOperator = vi.fn(async () => {})
const deleteOperator = vi.fn(async () => {})
const setOperatorAddress = vi.fn(async (_c: unknown, _id: string, _address: string): Promise<RegionalOperator> => ({} as RegionalOperator))

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: () => { url: string; org: string }; isAdmin: () => boolean; canEdit: () => boolean; state?: { agents: Agent[] } }) => unknown) => {
    const state = { conn: () => ({ url: '', org: 'o' }), isAdmin: () => admin, canEdit: () => canEditFlag, state: { agents: topologyState.agents } }
    return selector ? selector(state) : state
  },
}))
const telemetryStart = vi.fn()
vi.mock('@/components/telemetry/TelemetryFlow', () => ({
  useTelemetryFlow: () => ({ start: telemetryStart, dialogs: null, canStart: false }),
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listOperators: (...a: Parameters<typeof listOperators>) => listOperators(...a),
      createOperator: (...a: Parameters<typeof createOperator>) => createOperator(...a),
      enableOperatorHeartbeat: (...a: Parameters<typeof enableOperatorHeartbeat>) => enableOperatorHeartbeat(...a),
      getFusion: () => getFusion(),
      enableFusion: () => enableFusion(),
      disableFusion: () => disableFusion(),
      revokeOperator: (...a: Parameters<typeof revokeOperator>) => revokeOperator(...a),
      deleteOperator: (...a: Parameters<typeof deleteOperator>) => deleteOperator(...a),
      setOperatorAddress: (...a: Parameters<typeof setOperatorAddress>) => setOperatorAddress(...a),
    },
  }
})

beforeEach(() => {
  admin = true
  canEditFlag = true
  topologyState = { agents: [ag()], clusters: [cl()] }
  listOperators.mockClear()
  createOperator.mockClear()
  enableOperatorHeartbeat.mockClear()
  revokeOperator.mockClear()
  setOperatorAddress.mockReset()
  setOperatorAddress.mockResolvedValue({} as RegionalOperator)
  deleteOperator.mockClear()
  telemetryStart.mockClear()
  fusionStatus = fusionOff()
  getFusion.mockClear()
  enableFusion.mockClear()
  disableFusion.mockClear()
})

describe('RegionalOperatorsPage', () => {
  test('non-administrators see a restricted message instead of the table, and nothing is loaded', () => {
    admin = false
    renderPage()
    expect(screen.getByText('Administrators only')).toBeInTheDocument()
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('only clusters with a currently-approved agent are offered as source clusters', async () => {
    const user = userEvent.setup()
    topologyState = { agents: [ag({ status: 'pending' })], clusters: [cl()] }
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    expect(screen.getByText('No cluster has an approved agent yet - approve one on the Agents page first.')).toBeInTheDocument()
  })

  test('creating an operator is blocked until a name, a source cluster and a destination are all set', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    const submit = screen.getByTestId('operator-create')
    expect(submit).toBeDisabled()

    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    expect(submit).toBeDisabled()

    await user.click(screen.getByTestId('checkbox-c1'))
    expect(submit).toBeDisabled()

    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
    expect(submit).not.toBeDisabled()
  })

  test('labels are typed as name = value rows, sent trimmed, and half-filled rows are not sent', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    expect(screen.getByTestId('operator-labels-explain')).toHaveTextContent('cannot be changed afterwards')
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
    await user.click(screen.getByTestId('operator-label-tag-add'))
    await user.type(screen.getByTestId('operator-label-tag-key-0'), ' region ')
    await user.type(screen.getByTestId('operator-label-tag-value-0'), 'eu-south')
    await user.click(screen.getByTestId('operator-label-tag-add'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(createOperator.mock.calls[0][4]).toEqual({ heartbeat: true, labels: [{ key: 'region', value: 'eu-south' }], exposure: 'cluster' })
  })

  test('a reserved or half-filled label blocks Create and says why', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
    expect(screen.getByTestId('operator-create')).toBeEnabled()
    await user.click(screen.getByTestId('operator-label-tag-add'))
    await user.type(screen.getByTestId('operator-label-tag-key-0'), 'continuum.region')
    await user.type(screen.getByTestId('operator-label-tag-value-0'), 'x')
    expect(screen.getByTestId('operator-label-tag-problems')).toHaveTextContent('reserved')
    expect(screen.getByTestId('operator-create')).toBeDisabled()
  })

  test('submitting shows the receiver token and install command exactly once, since the server never returns the token again', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
    await user.click(screen.getByTestId('operator-create'))

    await waitFor(() => expect(createOperator).toHaveBeenCalledWith(
      { url: '', org: 'o' },
      'athens-regional',
      ['c1'],
      expect.objectContaining({ endpoint: 'backend.example.com:4317', kind: 'external' }),
      { heartbeat: true, labels: [], exposure: 'cluster' },
    ))
    expect(screen.getByText(/kubectl create secret generic op-1-receiver-auth/)).toBeInTheDocument()
    expect(screen.getByText(/helm install op-1/)).toBeInTheDocument()
  })

  test('when the server also minted a receiver TLS certificate, shows the extra secret command for it', async () => {
    createOperator.mockImplementationOnce(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }) => ({
      operator: {
        id: 'op-2', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination,
        createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
      },
      token: 'shown-once-secret',
      install: 'helm install op-2 ./continuum-regional-operator-0.1.0.tgz \
  --set receiver.tls.enabled=true',
      secretCommand: 'kubectl create secret generic op-2-receiver-auth --namespace continuum-system --from-literal=token=shown-once-secret',
      tlsSecretCommand: 'kubectl create secret generic op-2-receiver-tls --namespace continuum-system --from-literal=tls.crt="-----BEGIN CERTIFICATE-----..."',
      reminders: [] as string[],
    }))
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
    await user.click(screen.getByTestId('operator-create'))

    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(screen.getByText(/kubectl create secret generic op-2-receiver-tls/)).toBeInTheDocument()
  })

  test('while listOperators is still in flight, shows a loading skeleton instead of the misleading "No regional operators yet" empty state (task #383)', async () => {
    // mockImplementation (not -Once): the test double's useServer mock hands back a fresh `conn` function
    // identity on every render (unlike the real Zustand store, whose selector is stable), so `load`'s own
    // useCallback re-fires more than once here - every call needs to hit the same pending promise, or a
    // later call's default resolution would silently overwrite the one this test is asserting on.
    let resolve!: (ops: RegionalOperator[]) => void
    const pending = new Promise<RegionalOperator[]>((r) => { resolve = r })
    listOperators.mockImplementation(() => pending)
    renderPage()

    // The fetch hasn't settled yet: the real table (and its wrong-until-loaded "empty" reading) must not
    // render, and neither should a flash of "no operators" - only an honest loading placeholder.
    expect(screen.queryByText('No regional operators yet')).not.toBeInTheDocument()
    expect(screen.getAllByRole('status', { name: 'Loading' }).length).toBeGreaterThan(0)

    resolve([{
      id: 'op-1', orgId: 'o', name: 'athens-regional', status: 'active', sourceClusterIds: ['c1'],
      destination: { kind: 'external', endpoint: 'backend.example.com:4317' }, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
    } as RegionalOperator])

    await waitFor(() => expect(screen.getByText('athens-regional')).toBeInTheDocument())
    expect(screen.queryByText('No regional operators yet')).not.toBeInTheDocument()
    expect(screen.queryByRole('status', { name: 'Loading' })).not.toBeInTheDocument()
  })

  test('the extra-processor editor embeds with no extra processors by default', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
    expect(screen.getByTestId('operator-processor-add-filter')).toBeInTheDocument()
    await user.click(screen.getByTestId('operator-processor-add-filter'))
    expect(screen.queryByText('No extra processors.')).not.toBeInTheDocument()
  })
})

describe('RegionalOperatorsPage - FUSION and the central operator', () => {
  async function chooseCentral(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByTestId('operator-dest-central'))
  }
  const inWizard = () => within(screen.getByTestId('operator-central'))

  test('the central operator needs no endpoint, and with FUSION off the button says it will turn FUSION on', async () => {
    const user = userEvent.setup()
    renderPage()
    await chooseCentral(user)
    expect(screen.queryByPlaceholderText('otel-gateway.example.com:4317')).not.toBeInTheDocument()
    expect(await inWizard().findByText(/Off - nothing is running/)).toBeInTheDocument()
    expect(screen.getByTestId('operator-create')).toHaveTextContent('Enable FUSION and create')
    expect(screen.getByTestId('operator-create')).toBeEnabled()
  })

  test('Enable FUSION and create turns it on first, then creates an operator that sends to the central operator', async () => {
    const user = userEvent.setup()
    renderPage()
    await chooseCentral(user)
    await user.click(await screen.findByText('Enable FUSION and create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(enableFusion).toHaveBeenCalledTimes(1)
    expect(enableFusion.mock.invocationCallOrder[0]).toBeLessThan(createOperator.mock.invocationCallOrder[0])
    expect(createOperator.mock.calls[0][3]).toMatchObject({ kind: 'operator', targetOperatorId: 'op-central' })
  })

  test('with FUSION already running the button just creates, and FUSION is not touched', async () => {
    fusionStatus = fusionRunning()
    const user = userEvent.setup()
    renderPage()
    await chooseCentral(user)
    expect(await inWizard().findByText(/Running - the central operator and the three stores are up/)).toBeInTheDocument()
    expect(screen.getByTestId('operator-create')).not.toHaveTextContent('Enable FUSION')
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(enableFusion).not.toHaveBeenCalled()
  })

  test('a server that cannot switch FUSION says why, and does not let the central operator be chosen for nothing', async () => {
    fusionStatus = { available: false, reason: 'not-installed', state: 'off', message: "FUSION's workloads are not in this release." }
    const user = userEvent.setup()
    renderPage()
    await chooseCentral(user)
    expect(await inWizard().findByText(/workloads are not in this release/)).toBeInTheDocument()
    expect(screen.queryByTestId('fusion-enable')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-create')).toBeDisabled()
    expect(screen.getByTestId('operator-problems')).toHaveTextContent('FUSION cannot be switched')
  })

  test('switching back to another backend restores the endpoint field', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    await user.click(screen.getByTestId('operator-dest-central'))
    await user.click(screen.getByTestId('operator-dest-external'))
    expect(screen.queryByTestId('operator-central')).not.toBeInTheDocument()
    expect(screen.getByText('otel-gateway.example.com:4317')).toBeInTheDocument()
  })

  test('the created screen shows the client certificate Secret before the install, and warns when the central operator is not reachable from elsewhere', async () => {
    createOperator.mockImplementationOnce(async (_c, name, sourceClusterIds, destination) => ({
      operator: { id: 'op-1', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me' },
      install: 'helm install op-1 ./op.tgz --set export.otlp.endpoint=continuum-fusion-central.continuum.svc:4317',
      reminders: [] as string[],
      exportSecretCommand: 'kubectl create secret generic op-central-export-mtls --namespace continuum-system',
      exportTarget: { operatorId: 'op-central', name: 'Central (FUSION)', endpoint: 'continuum-fusion-central.continuum.svc:4317', reachableFromOtherClusters: false },
    }) as never)
    fusionStatus = fusionRunning()
    const user = userEvent.setup()
    renderPage()
    await chooseCentral(user)
    await user.click(screen.getByTestId('operator-create'))
    const block = await screen.findByTestId('operator-created-export')
    expect(block).toHaveTextContent('nothing to install for it')
    expect(screen.getByTestId('operator-export-secret')).toHaveTextContent('op-central-export-mtls')
    expect(screen.getByTestId('operator-created-export-unreachable')).toHaveTextContent('Reachable at')
    expect(block.compareDocumentPosition(screen.getByText('Then install the operator')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByText(/Install FUSION first/)).not.toBeInTheDocument()
  })

  test('the Regional tab shows FUSION with its four parts, and the switch', async () => {
    fusionStatus = fusionStarting()
    const user = userEvent.setup()
    renderPage()
    const panel = await screen.findByTestId('fusion-parts')
    expect(within(panel).getByTestId('fusion-part-metrics')).toHaveTextContent('Up')
    expect(within(panel).getByTestId('fusion-part-central')).toHaveTextContent('Starting')
    expect(screen.getByTestId('fusion-status')).toHaveTextContent('Starting - 2 of 4 parts are up.')
    expect(screen.getByTestId('fusion-exposure')).toHaveTextContent('inside this cluster only')

    // Turning it off asks first.
    await user.click(screen.getByTestId('fusion-disable'))
    expect(disableFusion).not.toHaveBeenCalled()
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(disableFusion).toHaveBeenCalled())
    expect(await screen.findByTestId('fusion-enable')).toBeInTheDocument()
  })

  test('an exposed central operator says where other clusters reach it', async () => {
    fusionStatus = { ...fusionRunning(), central: { ...central, exposed: true, endpoint: 'fusion.example.com:4317' } }
    renderPage()
    expect(await screen.findByTestId('fusion-exposure')).toHaveTextContent('reachable from other clusters at fusion.example.com:4317')
  })

  test('the central operator is listed as managed by FUSION: no revoke, no delete, and its state follows FUSION', async () => {
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([op({ id: 'op-central', name: 'Central (FUSION)', sourceClusterIds: [], destination: { kind: 'fusion', endpoint: '', fusionRelease: 'continuum-fusion', fusionNamespace: 'continuum' } })])
    renderPage()
    expect(await screen.findByTestId('operator-central-managed')).toHaveTextContent('Managed by FUSION')
    const row = screen.getByTestId('operator-Central (FUSION)')
    expect(within(row).queryByText('Revoke')).not.toBeInTheDocument()
    expect(within(row).queryByLabelText('Delete Central (FUSION)')).not.toBeInTheDocument()
    expect(within(row).getByTestId('operator-central-state')).toHaveTextContent('Running')
    listOperators.mockResolvedValue([])
  })

  test('the table names an operator that sends to the central operator by it, not by an empty endpoint', async () => {
    listOperators.mockResolvedValue([op({ name: 'athens', destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-central' } })])
    renderPage()
    expect(await screen.findByTestId('operator-destination-athens')).toHaveTextContent('Central operator (FUSION)')
    listOperators.mockResolvedValue([])
  })
})

describe('RegionalOperatorsPage - Local tab', () => {
  test('with no approved agent reporting any telemetry signal, the Local tab shows its own empty state', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operators-local'))
    expect(screen.getByText('No local operators running yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Configure telemetry/ })).toBeInTheDocument()
  })

  test('an approved agent with installed signals lists as a local operator, grouped by cluster, with a working Configure action', async () => {
    const user = userEvent.setup()
    admin = false // regional operators stay admin-only; the local tab must not depend on that
    topologyState = {
      agents: [ag({ name: 'edge-agent', diagnostics: { installedTelemetry: ['resourceUsage', 'systemLogs'], reportedAt: '2026-01-02T00:00:00Z' } } as Partial<Agent>)],
      clusters: [cl()],
    }
    renderPage()
    await user.click(screen.getByTestId('operators-local'))
    expect(screen.getByTestId('local-operator-edge-agent')).toBeInTheDocument()
    expect(screen.getByText('Resource usage')).toBeInTheDocument()
    expect(screen.getByText('System logs')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Configure' }))
    expect(telemetryStart).toHaveBeenCalledWith('a1')
  })

  test('the Configure action is hidden for a viewer who cannot edit', async () => {
    const user = userEvent.setup()
    canEditFlag = false
    topologyState = {
      agents: [ag({ name: 'edge-agent', diagnostics: { installedTelemetry: ['resourceUsage'] } } as Partial<Agent>)],
      clusters: [cl()],
    }
    renderPage()
    await user.click(screen.getByTestId('operators-local'))
    expect(screen.getByTestId('local-operator-edge-agent')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Configure' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------------------------------------
// Health reporting (the opt-in heartbeat) and certificate-only receivers.

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
const op = (over: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id: 'op-1', orgId: 'o', name: 'athens-regional', status: 'active', sourceClusterIds: ['c1'],
  destination: { kind: 'external', endpoint: 'backend.example.com:4317' }, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  receiverAuth: 'mtls', health: { state: 'unknown', reporting: false },
  ...over,
})

async function fillCreateForm(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('operator-open'))
  await user.type(screen.getByTestId('operator-name'), 'athens-regional')
  await user.click(screen.getByTestId('checkbox-c1'))
  await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
  await user.click(screen.getByRole('option', { name: 'Other…' }))
  await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
}

describe('RegionalOperatorsPage - health in the table', () => {
  test('online reads "Online", offline reads "Offline, last seen ...", and a non-reporting operator reads "Health not reported" - always in words', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'online-op', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
      op({ id: 'op-b', name: 'offline-op', health: { state: 'offline', lastSeenAt: minutesAgo(20), reporting: true } }),
      op({ id: 'op-c', name: 'quiet-op' }),
    ])
    renderPage()
    const row = async (name: string) => within(await screen.findByTestId(`operator-${name}`)).getByTestId('operator-health')
    expect(await row('online-op')).toHaveTextContent(/^Online$/)
    expect(await row('online-op')).toHaveAttribute('data-health', 'online')
    expect(await row('offline-op')).toHaveTextContent('Offline, last seen 20 min ago')
    expect(await row('offline-op')).toHaveAttribute('data-health', 'offline')
    expect(await row('quiet-op')).toHaveTextContent('Health not reported')
    expect(await row('quiet-op')).toHaveAttribute('data-health', 'unreported')
    // The Active pill is still there next to it.
    expect(within(screen.getByTestId('operator-online-op')).getByText('Active')).toBeInTheDocument()
  })

  test('an operator from an older server (no health, no receiverAuth) reads as not reported, and a revoked one shows no health at all', async () => {
    const { health: _h, receiverAuth: _r, ...legacy } = op({ name: 'legacy-op' })
    listOperators.mockResolvedValue([legacy as RegionalOperator, op({ id: 'op-r', name: 'revoked-op', status: 'revoked', health: { state: 'offline', lastSeenAt: minutesAgo(5), reporting: true } })])
    renderPage()
    expect(within(await screen.findByTestId('operator-legacy-op')).getByTestId('operator-health')).toHaveTextContent('Health not reported')
    expect(within(screen.getByTestId('operator-revoked-op')).queryByTestId('operator-health')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-health-open-revoked-op')).not.toBeInTheDocument()
  })
})

describe('RegionalOperatorsPage - enable / rotate health reporting', () => {
  test('the action says Enable for a non-reporting operator and Rotate for a reporting one', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'quiet-op' }),
      op({ id: 'op-b', name: 'live-op', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
    ])
    renderPage()
    expect(await screen.findByTestId('operator-health-open-quiet-op')).toHaveTextContent('Enable health reporting')
    expect(screen.getByTestId('operator-health-open-live-op')).toHaveTextContent('Rotate health credential')
  })

  test('nothing is minted before confirming; the confirmation says what is sent, that it is opt-in, and that rotating invalidates; commands appear only afterwards, in order', async () => {
    enableOperatorHeartbeat.mockResolvedValueOnce({
      operator: op(), rotated: true, heartbeatToken: 'cnh_new',
      heartbeatSecretCommand: 'SECRET-CMD', heartbeatUpgradeCommand: 'UPGRADE-CMD', heartbeatRestartCommand: 'RESTART-CMD',
      heartbeatUrl: 'http://continuum.local/api/v1/operator-heartbeat', heartbeatIntervalSeconds: 60,
      heartbeatWarning: 'this address is plain HTTP',
    })
    listOperators.mockResolvedValue([op({ health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-health-open-athens-regional'))

    const explain = screen.getByTestId('operator-health-explain')
    expect(explain).toHaveTextContent('availability check')
    expect(explain).toHaveTextContent('no telemetry')
    expect(explain).toHaveTextContent('opt-in')
    expect(explain).toHaveTextContent('contacting this server')
    expect(explain).toHaveTextContent('invalidates the old credential')
    expect(enableOperatorHeartbeat).not.toHaveBeenCalled()
    expect(screen.queryByTestId('operator-health-commands')).not.toBeInTheDocument()
    expect(screen.queryByText('SECRET-CMD')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('operator-health-confirm'))
    await waitFor(() => expect(enableOperatorHeartbeat).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-1'))
    const commands = await screen.findByTestId('operator-health-commands')
    expect(screen.getByTestId('operator-health-commands-once')).toHaveTextContent('shown only now')
    expect(screen.getByTestId('operator-health-commands-warning')).toHaveTextContent('plain HTTP')
    expect(screen.getByTestId('operator-health-rotated')).toBeInTheDocument()
    const text = commands.textContent ?? ''
    expect(text.indexOf('SECRET-CMD')).toBeGreaterThan(-1)
    expect(text.indexOf('SECRET-CMD')).toBeLessThan(text.indexOf('UPGRADE-CMD'))
    expect(text.indexOf('UPGRADE-CMD')).toBeLessThan(text.indexOf('RESTART-CMD'))
  })

  test('cancelling the confirmation mints nothing, and a first-time enable shows no restart step', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-health-open-athens-regional'))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(enableOperatorHeartbeat).not.toHaveBeenCalled()
    expect(screen.queryByTestId('operator-health-explain')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('operator-health-open-athens-regional'))
    await user.click(screen.getByTestId('operator-health-confirm'))
    await screen.findByTestId('operator-health-commands')
    expect(screen.getByTestId('operator-health-commands-secret')).toHaveTextContent('--from-literal=token=cnh_secret')
    expect(screen.getByTestId('operator-health-commands-upgrade')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-health-commands-restart')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-health-rotated')).not.toBeInTheDocument()
  })

  test('a refusal keeps the confirmation open with the server message and shows no commands', async () => {
    enableOperatorHeartbeat.mockRejectedValueOnce(new ApiError(403, 'admin only'))
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-health-open-athens-regional'))
    await user.click(screen.getByTestId('operator-health-confirm'))
    expect(await screen.findByText('admin only')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-health-commands')).not.toBeInTheDocument()
  })
})

describe('RegionalOperatorsPage - creating with and without health reporting', () => {
  test('the checkbox is on by default, explains itself, and the request carries heartbeat: true', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    const box = screen.getByTestId('operator-heartbeat')
    expect(box).toBeChecked()
    expect(screen.getByText("Report this operator's health to this server")).toBeInTheDocument()
    expect(screen.getByTestId('operator-heartbeat-explain')).toHaveTextContent('only thing this operator ever sends to this server')
    expect(screen.getByTestId('operator-heartbeat-explain')).toHaveTextContent('turn it on later')
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
    expect(createOperator.mock.calls[0][4]).toEqual({ heartbeat: true, labels: [], exposure: 'cluster' })
  })

  test('unchecking it sends heartbeat: false and the created screen says the operator never contacts the server', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-heartbeat'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
    expect(createOperator.mock.calls[0][4]).toEqual({ heartbeat: false, labels: [], exposure: 'cluster' })
    expect(await screen.findByTestId('operator-no-rbac-note')).toHaveTextContent('never contacts this server')
    expect(screen.queryByTestId('operator-created-heartbeat')).not.toBeInTheDocument()
  })

  test('a certificate-only operator (no token, no secretCommand) shows no receiver-token step and explains the client certificate; the heartbeat Secret comes before the install', async () => {
    createOperator.mockImplementationOnce(async (_c, name, sourceClusterIds, destination) => ({
      operator: op({ id: 'op-m', name, sourceClusterIds, destination: destination as RegionalOperator['destination'] }),
      install: 'INSTALL-CMD helm install op-m --set heartbeat.enabled=true',
      tlsSecretCommand: 'TLS-SECRET-CMD',
      reminders: [] as string[],
      heartbeatToken: 'cnh_x',
      heartbeatSecretCommand: 'HEARTBEAT-SECRET-CMD',
      heartbeatUrl: 'https://continuum.example.com/api/v1/operator-heartbeat',
      heartbeatIntervalSeconds: 60,
    }))
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))

    const mtls = await screen.findByTestId('operator-created-mtls')
    expect(mtls).toHaveTextContent('no receiver token')
    expect(mtls).toHaveTextContent('client certificate')
    expect(mtls).toHaveTextContent('issued per agent when that agent')
    expect(mtls).toHaveTextContent('own certificate authority')
    expect(screen.queryByText(/receiver token Secret first/)).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-secret-command')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-no-rbac-note')).toHaveTextContent('client-certificate requirement')
    expect(screen.getByTestId('operator-no-rbac-note')).not.toHaveTextContent('receiver token below')
    const dialog = screen.getByRole('dialog')
    const text = dialog.textContent ?? ''
    expect(text.indexOf('HEARTBEAT-SECRET-CMD')).toBeGreaterThan(-1)
    expect(text.indexOf('HEARTBEAT-SECRET-CMD')).toBeLessThan(text.indexOf('INSTALL-CMD'))
    expect(text.indexOf('TLS-SECRET-CMD')).toBeLessThan(text.indexOf('INSTALL-CMD'))
    expect(screen.getByTestId('operator-created-heartbeat-once')).toBeInTheDocument()
  })

  test('a bearer operator still shows its receiver token Secret first, then the install, then the TLS Secret, with no heartbeat block when not requested', async () => {
    createOperator.mockImplementationOnce(async (_c, name, sourceClusterIds, destination) => ({
      operator: op({ id: 'op-b', name, receiverAuth: 'bearer', sourceClusterIds, destination: destination as RegionalOperator['destination'] }),
      token: 'shown-once-secret',
      install: 'INSTALL-CMD',
      secretCommand: 'TOKEN-SECRET-CMD',
      tlsSecretCommand: 'TLS-SECRET-CMD',
      reminders: [] as string[],
    }))
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-heartbeat'))
    await user.click(screen.getByTestId('operator-create'))
    await screen.findByTestId('operator-secret-command')
    const text = screen.getByRole('dialog').textContent ?? ''
    expect(text).toContain('Create the receiver token Secret first')
    expect(text.indexOf('TOKEN-SECRET-CMD')).toBeLessThan(text.indexOf('INSTALL-CMD'))
    expect(text.indexOf('INSTALL-CMD')).toBeLessThan(text.indexOf('TLS-SECRET-CMD'))
    expect(screen.queryByTestId('operator-created-mtls')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-created-heartbeat')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-no-rbac-note')).toHaveTextContent('receiver token below')
  })
})

describe('RegionalOperatorsPage - explanatory notes', () => {
  test('the access note no longer says it never dials the server or always has a token', async () => {
    renderPage()
    const note = await screen.findByTestId('operator-access-note')
    const text = note.textContent ?? ''
    expect(text).toContain('unless you turn on health reporting')
    expect(text).toContain('client certificate alone')
    expect(text).toContain('its own certificate authority')
    expect(text).toContain('older operator the bearer token')
    expect(text).not.toContain('a receiver bearer token (minted')
  })
})

describe('RegionalOperatorsPage - where other clusters reach an operator', () => {
  const fillCreate = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'eu-hub')
    await user.click(screen.getByTestId('checkbox-c1'))
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), 'backend.example.com:4317')
  }

  test('choosing a load balancer sends it, and the created screen says how to read the address and where to record it', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreate(user)
    await user.click(screen.getByTestId('operator-exposure'))
    await user.click(screen.getByRole('option', { name: /load balancer/ }))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
    expect(createOperator.mock.calls[0][4]).toMatchObject({ exposure: 'loadbalancer' })
    const note = await screen.findByTestId('operator-created-address')
    expect(note).toHaveTextContent('Reachable at')
    // The load balancer line only, since that is what was chosen; it names the operator's own Service.
    expect(screen.getByTestId('operator-created-find-lb')).toHaveTextContent('kubectl get svc op-1 --namespace continuum-system')
    expect(screen.queryByTestId('operator-created-find-np')).not.toBeInTheDocument()
  })

  test('the default is this cluster only: nothing about addresses on the created screen', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreate(user)
    expect(screen.getByTestId('operator-exposure')).toHaveTextContent('This cluster only')
    await user.click(screen.getByTestId('operator-create'))
    await screen.findByText(/helm install op-1/)
    expect(screen.queryByTestId('operator-created-address')).not.toBeInTheDocument()
  })

  test('each row says whether other clusters can reach it', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'local-op' }),
      op({ id: 'op-b', name: 'hub-op', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true }),
      op({ id: 'op-c', name: 'gone-op', status: 'revoked' }),
    ])
    renderPage()
    expect(await screen.findByTestId('operator-address-local-op')).toHaveTextContent('no address recorded')
    expect(screen.getByTestId('operator-address-hub-op')).toHaveTextContent('reachable at otlp.eu.example.com:4317')
    expect(screen.queryByTestId('operator-address-gone-op')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-address-open-gone-op')).not.toBeInTheDocument()
  })

  test('a row says what was installed when no address is recorded yet', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'lb-op', exposure: 'loadbalancer' }),
      op({ id: 'op-b', name: 'np-op', exposure: 'nodeport' }),
      op({ id: 'op-c', name: 'own-op', exposure: 'cluster' }),
      op({ id: 'op-d', name: 'done-op', exposure: 'loadbalancer', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true }),
    ])
    renderPage()
    expect(await screen.findByTestId('operator-address-lb-op')).toHaveTextContent('exposed through a load balancer, address not recorded yet')
    expect(screen.getByTestId('operator-address-np-op')).toHaveTextContent('exposed through a node port, address not recorded yet')
    expect(screen.getByTestId('operator-address-own-op')).toHaveTextContent('this cluster only')
    expect(screen.getByTestId('operator-address-done-op')).toHaveTextContent('reachable at otlp.eu.example.com:4317')
  })

  test('the dialog shows only the lookup that fits how the operator was exposed', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'lb-op', exposure: 'loadbalancer' }),
      op({ id: 'op-c', name: 'own-op', exposure: 'cluster' }),
    ])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-address-open-lb-op'))
    expect(screen.getByTestId('operator-address-find-lb')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-address-find-np')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(await screen.findByTestId('operator-address-open-own-op'))
    expect(screen.queryByTestId('operator-address-find-lb')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-address-find-np')).not.toBeInTheDocument()
  })

  test('typing an address shows the connection check for it, with the default port', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'local-op' })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-address-open-local-op'))
    expect(screen.queryByTestId('operator-address-check')).not.toBeInTheDocument()
    await user.type(screen.getByTestId('operator-address-input'), 'otlp.eu.example.com')
    const check = screen.getByTestId('operator-address-check-command')
    expect(check).toHaveTextContent('openssl s_client -connect otlp.eu.example.com:4317 -servername op-a.continuum-system.svc')
    expect(screen.getByTestId('operator-address-check')).toHaveTextContent('DNS:op-a.continuum-system.svc')
  })

  test('the central operator has its own Reachable at: it says how to expose the gateway and reads its address from the gateway Service', async () => {
    fusionStatus = { ...fusionRunning(), central: { ...central, service: 'continuum-fusion-central', namespace: 'continuum' } }
    listOperators.mockResolvedValue([op({ id: 'op-central', name: 'Central (FUSION)', sourceClusterIds: [], destination: { kind: 'fusion', endpoint: '', fusionRelease: 'continuum-fusion', fusionNamespace: 'continuum' } })])
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByTestId('operator-address-Central (FUSION)')).toHaveTextContent('reachable inside this cluster only')
    await user.click(screen.getByTestId('operator-address-open-Central (FUSION)'))
    expect(screen.getByTestId('operator-address-central')).toHaveTextContent('fusion.central.service.type')
    expect(screen.getByTestId('operator-address-find-lb')).toHaveTextContent('kubectl get svc continuum-fusion-central --namespace continuum')
    await user.type(screen.getByTestId('operator-address-input'), 'fusion.example.com')
    await user.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setOperatorAddress).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-central', 'fusion.example.com'))
    listOperators.mockResolvedValue([])
  })

  test('the dialog records a trimmed address, shows how to find it, and reloads', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'local-op' })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-address-open-local-op'))
    expect(screen.getByTestId('operator-address-explain')).toHaveTextContent('no certificate is reissued')
    expect(screen.getByTestId('operator-address-find-lb')).toHaveTextContent('kubectl get svc op-a')
    expect(screen.getByTestId('operator-address-find-np')).toHaveTextContent('nodePort')
    await user.type(screen.getByTestId('operator-address-input'), '  203.0.113.7:4317 ')
    listOperators.mockClear()
    await user.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setOperatorAddress).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-a', '203.0.113.7:4317'))
    await waitFor(() => expect(listOperators).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByTestId('operator-address-input')).not.toBeInTheDocument())
  })

  test('a refused address stays open with the server\'s reason', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'local-op' })])
    setOperatorAddress.mockRejectedValueOnce(new ApiError(400, 'the address is a host and a port, such as otlp.example.com:4317'))
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-address-open-local-op'))
    await user.type(screen.getByTestId('operator-address-input'), 'https://nope')
    await user.click(screen.getByTestId('operator-address-save'))
    expect(await screen.findByText('the address is a host and a port, such as otlp.example.com:4317')).toBeInTheDocument()
    expect(screen.getByTestId('operator-address-input')).toBeInTheDocument()
  })

  test('an operator with an address starts the dialog on it, and clearing sends an empty address', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-b', name: 'hub-op', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-address-open-hub-op'))
    const input = screen.getByTestId('operator-address-input')
    expect(input).toHaveValue('otlp.eu.example.com:4317')
    await user.clear(input)
    await user.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setOperatorAddress).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-b', ''))
  })
})

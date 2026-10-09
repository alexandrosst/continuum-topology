import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { isReloadHeld } from '@/lib/staleBuild'
import RegionalOperatorsPage from '@/pages/RegionalOperatorsPage'
import type { CreatedOperator, FusionStatus, IssuedCertificate, OperatorHeartbeatEnabled, OperatorRemoval } from '@/lib/api'
import type { Agent, Cluster, OperatorDestinationEntry, RegionalOperator, TelemetryIntent } from '@/lib/types'

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
const listOperatorDestinations = vi.fn(async (): Promise<OperatorDestinationEntry[]> => [])
const listTelemetryIntents = vi.fn(async (): Promise<TelemetryIntent[]> => [])
const createOperator = vi.fn(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }, _options?: { heartbeat?: boolean; labels?: { key: string; value: string }[] }): Promise<CreatedOperator> => ({
  operator: {
    id: 'op-1', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination,
    createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  } as CreatedOperator['operator'],
  token: 'shown-once-secret',
  install: 'helm install op-1 ./continuum-regional-operator-0.1.0.tgz \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=backend.example.com:4317',
  secretCommand: 'kubectl create secret generic op-1-receiver-auth --namespace continuum-system --from-literal=token=shown-once-secret',
  reminders: [] as string[],
}))
const reinstallOperator = vi.fn(async (_c: unknown, _id: string): Promise<CreatedOperator> => ({
  operator: { id: 'op-1', name: 'athens-regional', status: 'active', sourceClusterIds: ['c1'], receiverAuth: 'mtls' } as CreatedOperator['operator'],
  install: 'REINSTALL-CMD',
  tlsSecretCommand: 'RENEWED-TLS-SECRET-CMD',
  reminders: [] as string[],
}))
const enableOperatorHeartbeat = vi.fn(async (_c: unknown, _id: string): Promise<OperatorHeartbeatEnabled> => ({
  operator: {} as RegionalOperator,
  rotated: false,
  heartbeatToken: 'cnh_secret',
  heartbeatSecretCommand: 'kubectl create secret generic op-1-heartbeat-auth --namespace continuum-system --from-literal=token=cnh_secret',
  heartbeatUpgradeCommand: 'helm upgrade op-1 ./chart.tgz --namespace continuum-system --reset-then-reuse-values --set heartbeat.enabled=true',
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
const revokeOperator = vi.fn(async (..._a: unknown[]): Promise<OperatorRemoval | undefined> => undefined)
const deleteOperator = vi.fn(async (..._a: unknown[]): Promise<OperatorRemoval | undefined> => undefined)
const openFusionPage = vi.fn(async (_c: unknown, page: string): Promise<{ path: string }> => ({ path: `/fusion/${page === 'grafana' ? 'grafana' : 'prometheus'}/?ikhnos_ticket=t1` }))
const listOperatorCertificates = vi.fn(async (_c: unknown, _id: string): Promise<{ certificates: IssuedCertificate[] }> => ({ certificates: [] }))
const setOperatorAddress = vi.fn(async (_c: unknown, _id: string, _address: string): Promise<RegionalOperator> => ({} as RegionalOperator))

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
// One stable conn, as the real store's: a fresh function per call would make every polled list read again on every render.
const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn; isAdmin: () => boolean; canEdit: () => boolean; state?: { agents: Agent[] } }) => unknown) => {
    const state = { conn: connFn, isAdmin: () => admin, canEdit: () => canEditFlag, state: { agents: topologyState.agents } }
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
      listOperatorDestinations: (...a: Parameters<typeof listOperatorDestinations>) => listOperatorDestinations(...a),
      listTelemetryIntents: (...a: Parameters<typeof listTelemetryIntents>) => listTelemetryIntents(...a),
      createOperator: (...a: Parameters<typeof createOperator>) => createOperator(...a),
      reinstallOperator: (...a: Parameters<typeof reinstallOperator>) => reinstallOperator(...a),
      enableOperatorHeartbeat: (...a: Parameters<typeof enableOperatorHeartbeat>) => enableOperatorHeartbeat(...a),
      getFusion: () => getFusion(),
      openFusionPage: (...a: Parameters<typeof openFusionPage>) => openFusionPage(...a),
      enableFusion: () => enableFusion(),
      disableFusion: () => disableFusion(),
      revokeOperator: (...a: Parameters<typeof revokeOperator>) => revokeOperator(...a),
      deleteOperator: (...a: Parameters<typeof deleteOperator>) => deleteOperator(...a),
      listOperatorCertificates: (...a: Parameters<typeof listOperatorCertificates>) => listOperatorCertificates(...a),
      setOperatorAddress: (...a: Parameters<typeof setOperatorAddress>) => setOperatorAddress(...a),
    },
  }
})

beforeEach(() => {
  admin = true
  canEditFlag = true
  topologyState = { agents: [ag()], clusters: [cl()] }
  listOperators.mockReset()
  listOperators.mockResolvedValue([])
  listOperatorDestinations.mockReset()
  listOperatorDestinations.mockResolvedValue([])
  listTelemetryIntents.mockReset()
  listTelemetryIntents.mockResolvedValue([])
  createOperator.mockClear()
  reinstallOperator.mockClear()
  enableOperatorHeartbeat.mockClear()
  revokeOperator.mockReset()
  revokeOperator.mockResolvedValue(undefined)
  deleteOperator.mockReset()
  deleteOperator.mockResolvedValue(undefined)
  openFusionPage.mockClear()
  listOperatorCertificates.mockReset()
  listOperatorCertificates.mockResolvedValue({ certificates: [] })
  setOperatorAddress.mockReset()
  setOperatorAddress.mockResolvedValue({} as RegionalOperator)
  telemetryStart.mockClear()
  fusionStatus = fusionOff()
  getFusion.mockClear()
  enableFusion.mockClear()
  disableFusion.mockClear()
})

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
const daysFromNow = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString()
const op = (over: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id: 'op-1', orgId: 'o', name: 'athens-regional', status: 'active', sourceClusterIds: ['c1'],
  destination: { kind: 'external', endpoint: 'backend.example.com:4317' }, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  receiverAuth: 'mtls', health: { state: 'unknown', reporting: false },
  ...over,
})
const centralOp = (over: Partial<RegionalOperator> = {}) =>
  op({ id: 'op-central', name: 'Central (FUSION)', sourceClusterIds: [], destination: { kind: 'fusion', endpoint: '', fusionRelease: 'continuum-fusion', fusionNamespace: 'continuum' }, ...over })

type User = ReturnType<typeof userEvent.setup>

/** A row's action, through its menu: the menu is the only place the actions are. */
async function rowAction(user: User, name: string, item: 'address-open' | 'health-open' | 'certs' | 'renew' | 'revoke' | 'delete') {
  await user.click(await screen.findByTestId(`operator-menu-${name}`))
  await user.click(await screen.findByTestId(`operator-${item}-${name}`))
}

/** Renewing asks first: the confirmation's own button. */
async function confirmRenew(user: User) {
  const dialog = await screen.findByRole('dialog', { name: /^Renew certificates for / })
  await user.click(within(dialog).getByRole('button', { name: 'Renew' }))
}

/** The create dialog, step by step. */
async function toDestination(user: User, name = 'athens-regional') {
  await user.click(screen.getByTestId('operator-open'))
  await user.type(screen.getByTestId('operator-name'), name)
  await user.click(screen.getByTestId('checkbox-c1'))
  await user.click(screen.getByTestId('operator-next'))
}
async function chooseBackend(user: User, endpoint = 'backend.example.com:4317') {
  await user.click(within(screen.getByTestId('operator-other-backend')).getByText('Not listed? Another backend'))
  await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
  await user.click(screen.getByRole('option', { name: 'Other…' }))
  await user.type(screen.getByPlaceholderText('otel-gateway.example.com:4317'), endpoint)
}
async function fillCreateForm(user: User) {
  await toDestination(user)
  await chooseBackend(user)
  await user.click(screen.getByTestId('operator-next'))
}

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
    await user.click(await screen.findByTestId('operator-open'))
    expect(screen.getByText('No cluster has an approved agent yet - approve one on the Agents page first.')).toBeInTheDocument()
  })

  test('the dialog is three steps - name and sources, destination, create - and each Next waits for what its step needs', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-open'))
    expect(screen.getByTestId('operator-steps')).toHaveTextContent('Name and sources')
    expect(screen.getByTestId('operator-steps')).toHaveTextContent('Destination')
    expect(screen.getByTestId('operator-steps')).toHaveTextContent('Create')
    const next = screen.getByTestId('operator-next')
    expect(next).toBeDisabled()

    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    // Source clusters are optional: a name is all this step needs.
    expect(next).toBeEnabled()
    await user.click(screen.getByTestId('checkbox-c1'))
    expect(next).toBeEnabled()
    await user.click(next)

    // Nothing is chosen yet (FUSION is off, and is never picked for anyone): Next waits for a destination.
    expect(screen.getByTestId('operator-next')).toBeDisabled()
    await chooseBackend(user)
    expect(screen.getByTestId('operator-next')).toBeEnabled()
    await user.click(screen.getByTestId('operator-next'))
    expect(screen.getByTestId('operator-step-create')).toBeInTheDocument()
    expect(screen.getByTestId('operator-summary')).toHaveTextContent('athens-regional')
    expect(screen.getByTestId('operator-summary')).toHaveTextContent('edge-1')
    expect(screen.getByTestId('operator-summary')).toHaveTextContent('backend.example.com:4317')
    expect(screen.getByTestId('operator-create')).toBeEnabled()
    // Back keeps what was typed.
    await user.click(screen.getByTestId('operator-back'))
    await user.click(screen.getByTestId('operator-back'))
    expect(screen.getByTestId('operator-name')).toHaveValue('athens-regional')
  })

  test('source clusters are optional: a name alone lets the dialog move on', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional')
    expect(screen.getByTestId('operator-next')).toBeEnabled()
  })

  test('Exposure, Heartbeat, Labels and Processors are under Advanced on the last step, not in the way before it', async () => {
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    expect(screen.queryByTestId('operator-exposure')).not.toBeInTheDocument()
    await chooseBackend(user)
    await user.click(screen.getByTestId('operator-next'))
    const advanced = screen.getByTestId('operator-advanced')
    expect(within(advanced).getByText('Advanced')).toBeInTheDocument()
    for (const id of ['operator-exposure', 'operator-heartbeat', 'operator-labels-explain', 'operator-processor-add-filter']) expect(within(advanced).getByTestId(id)).toBeInTheDocument()
    expect(advanced).not.toHaveAttribute('open')
  })

  test('labels are typed as name = value rows, sent trimmed, and half-filled rows are not sent', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    expect(screen.getByTestId('operator-labels-explain')).toHaveTextContent('cannot be changed afterwards')
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
    await fillCreateForm(user)
    expect(screen.getByTestId('operator-create')).toBeEnabled()
    await user.click(screen.getByTestId('operator-label-tag-add'))
    await user.type(screen.getByTestId('operator-label-tag-key-0'), 'continuum.region')
    await user.type(screen.getByTestId('operator-label-tag-value-0'), 'x')
    expect(screen.getByTestId('operator-label-tag-problems')).toHaveTextContent('reserved')
    expect(screen.getByTestId('operator-create')).toBeDisabled()
  })

  test('the extra-processor editor embeds with no extra processors by default', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
    await user.click(screen.getByTestId('operator-processor-add-filter'))
    expect(screen.queryByText('No extra processors.')).not.toBeInTheDocument()
  })

  test('submitting shows the commands once, as numbered steps in the order to run them, since the server never returns the token again', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
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
    const steps = within(screen.getByTestId('operator-created-steps')).getAllByRole('listitem')
    expect(steps[0]).toHaveTextContent('1Create the receiver token Secret')
    expect(steps[steps.length - 1]).toHaveTextContent(`${steps.length}Install the operator`)
  })

  test('when the server also minted a receiver TLS certificate, its Secret is a step of its own, before the install', async () => {
    createOperator.mockImplementationOnce(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }) => ({
      operator: { id: 'op-2', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me' } as CreatedOperator['operator'],
      token: 'shown-once-secret',
      install: 'INSTALL-CMD',
      secretCommand: 'kubectl create secret generic op-2-receiver-auth --namespace continuum-system --from-literal=token=shown-once-secret',
      tlsSecretCommand: 'kubectl create secret generic op-2-receiver-tls --namespace continuum-system --from-literal=tls.crt="-----BEGIN CERTIFICATE-----..."',
      reminders: [] as string[],
    }))
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    const text = (await screen.findByTestId('operator-created-steps')).textContent ?? ''
    expect(text.indexOf('op-2-receiver-auth')).toBeLessThan(text.indexOf('op-2-receiver-tls'))
    expect(text.indexOf('op-2-receiver-tls')).toBeLessThan(text.indexOf('INSTALL-CMD'))
  })

  test('while listOperators is still in flight, shows a loading skeleton instead of the misleading "No regional operators yet" empty state', async () => {
    let resolve!: (ops: RegionalOperator[]) => void
    const pending = new Promise<RegionalOperator[]>((r) => { resolve = r })
    listOperators.mockImplementation(() => pending)
    renderPage()

    // The fetch has not settled: the real table (and its wrong-until-loaded "empty" reading) must not render - only a placeholder.
    expect(screen.queryByText('No regional operators yet')).not.toBeInTheDocument()
    expect(screen.getAllByRole('status', { name: 'Loading' }).length).toBeGreaterThan(0)

    resolve([op({ name: 'athens-regional', id: 'op-1' })])

    await waitFor(() => expect(screen.getByText('athens-regional')).toBeInTheDocument())
    expect(screen.queryByText('No regional operators yet')).not.toBeInTheDocument()
    expect(screen.queryByRole('status', { name: 'Loading' })).not.toBeInTheDocument()
  })

  test('with no operators the empty state offers the same action, quietly: the header still holds the one primary', async () => {
    renderPage()
    expect(await screen.findByText('No regional operators yet')).toBeInTheDocument()
    expect(screen.getByTestId('operator-open')).toHaveTextContent('New operator')
    expect(screen.getByTestId('operator-open-empty')).toHaveTextContent('New operator')
    const primaries = screen.getAllByRole('button').filter((b) => b.className.includes('bg-accent'))
    expect(primaries).toEqual([screen.getByTestId('operator-open')])
  })

  test('with operators the header holds the one primary button, and nothing else on the page is primary', async () => {
    listOperators.mockResolvedValue([op()])
    renderPage()
    await screen.findByTestId('operator-athens-regional')
    expect(screen.getAllByTestId('operator-open')).toHaveLength(1)
    const primaries = screen.getAllByRole('button').filter((b) => b.className.includes('bg-accent'))
    expect(primaries).toHaveLength(1)
    expect(primaries[0]).toBe(screen.getByTestId('operator-open'))
  })
})

describe('RegionalOperatorsPage - the destination is picked from a list', () => {
  test('FUSION is the first entry, under "This server", ahead of the other operators', async () => {
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([op({ id: 'op-eu', name: 'eu-hub', destination: { kind: 'external', endpoint: 'x:4317' } })])
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    const groups = within(screen.getByTestId('operator-destination-list')).getAllByRole('heading', { level: 4 })
    expect(groups.map((h) => h.textContent)).toEqual(['This server', 'Your organisation'])
    expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveTextContent('FUSION - this server')
    expect(screen.getByTestId('operator-destination-operator-op-eu')).toHaveTextContent('eu-hub')
  })

  test('a running FUSION is picked for the person, and Create just creates: FUSION is not touched', async () => {
    fusionStatus = fusionRunning()
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    await waitFor(() => expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveAttribute('aria-checked', 'true'))
    expect(screen.getByTestId('operator-central')).toHaveTextContent('one door into this server')
    await user.click(screen.getByTestId('operator-next'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(enableFusion).not.toHaveBeenCalled()
    expect(createOperator.mock.calls[0][3]).toMatchObject({ kind: 'operator', targetOperatorId: 'op-central' })
  })

  test('a FUSION that is off is never picked for the person: the row offers Enable and use, and Next waits', async () => {
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    const row = await screen.findByTestId('operator-destination-fusion-op-central')
    expect(row).toHaveAttribute('data-fusion', 'off')
    expect(row).not.toHaveAttribute('aria-checked')
    expect(within(row).getByText(/Nothing receives data until it is on/)).toBeInTheDocument()
    expect(screen.getByTestId('operator-next')).toBeDisabled()
    expect(enableFusion).not.toHaveBeenCalled()
  })

  test('Enable and use switches FUSION on, then picks it as soon as it is starting; creating sends to the central operator without enabling it again', async () => {
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    await user.click(await screen.findByTestId('operator-destination-fusion-op-central-enable'))
    await waitFor(() => expect(enableFusion).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveAttribute('aria-checked', 'true'))
    expect(screen.getByTestId('operator-next')).toBeEnabled()
    await user.click(screen.getByTestId('operator-next'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(enableFusion).toHaveBeenCalledTimes(1)
    expect(createOperator.mock.calls[0][3]).toMatchObject({ kind: 'operator', targetOperatorId: 'op-central' })
  })

  test('when switching FUSION on fails, nothing is picked and the reason is shown', async () => {
    enableFusion.mockRejectedValueOnce(new ApiError(500, 'no node has room'))
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    await user.click(await screen.findByTestId('operator-destination-fusion-op-central-enable'))
    expect(await within(screen.getByTestId('operator-destination-fusion-op-central')).findByText('no node has room')).toBeInTheDocument()
    expect(screen.getByTestId('operator-next')).toBeDisabled()
  })

  test('a server that cannot switch FUSION says why, and offers no switch', async () => {
    fusionStatus = { available: false, reason: 'not-installed', state: 'off', message: "FUSION's workloads are not in this release." }
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    const row = await screen.findByTestId('operator-destination-fusion-op-central')
    expect(row).toHaveAttribute('data-fusion', 'unavailable')
    expect(row).toHaveTextContent('workloads are not in this release')
    expect(within(row).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-next')).toBeDisabled()
  })

  test('FUSION that belongs to another organisation is named as that, and cannot be picked', async () => {
    fusionStatus = { available: false, reason: 'other-org', state: 'off', message: 'FUSION belongs to another organisation on this server.' }
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    const row = await screen.findByTestId('operator-destination-fusion-op-central')
    expect(row).toHaveTextContent('belongs to another organisation')
    expect(row).not.toHaveAttribute('aria-checked')
  })

  test('a server that does not run FUSION at all has no row for it, and says what is left', async () => {
    fusionStatus = { available: false, reason: 'not-configured', state: 'off', message: 'No switch.' }
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    expect(screen.queryByTestId('operator-destination-fusion-op-central')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-destination-empty')).toHaveTextContent('Use another backend below')
  })

  test('an operator can send to another regional operator: picking it sends a destination that names it', async () => {
    fusionStatus = { available: false, reason: 'not-configured', state: 'off' }
    listOperators.mockResolvedValue([op({ id: 'op-eu', name: 'eu-hub', destination: { kind: 'external', endpoint: 'x:4317' } })])
    const user = userEvent.setup()
    renderPage()
    await toDestination(user, 'athens-edge')
    await user.click(await screen.findByTestId('operator-destination-operator-op-eu'))
    await user.click(screen.getByTestId('operator-next'))
    expect(screen.getByTestId('operator-summary')).toHaveTextContent('eu-hub')
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(createOperator.mock.calls[0][3]).toMatchObject({ kind: 'operator', targetOperatorId: 'op-eu' })
  })

  test('another backend is behind a disclosure, and typing one replaces a picked entry', async () => {
    fusionStatus = fusionRunning()
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    await waitFor(() => expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveAttribute('aria-checked', 'true'))
    expect(screen.getByTestId('operator-other-backend')).not.toHaveAttribute('open')
    await chooseBackend(user, 'other.example.com:4317')
    expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveAttribute('aria-checked', 'false')
    await user.click(screen.getByTestId('operator-next'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    expect(createOperator.mock.calls[0][3]).toMatchObject({ kind: 'external', endpoint: 'other.example.com:4317' })
  })

  test('the created screen shows the client certificate Secret before the install, says FUSION is starting, and warns when the central operator is not reachable from elsewhere', async () => {
    createOperator.mockImplementationOnce(async (_c, name, sourceClusterIds, destination) => ({
      operator: { id: 'op-1', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me' } as CreatedOperator['operator'],
      install: 'helm install op-1 ./op.tgz --set export.otlp.endpoint=continuum-fusion-central.continuum.svc:4317',
      reminders: [] as string[],
      exportSecretCommand: 'kubectl create secret generic op-central-export-mtls --namespace continuum-system',
      exportTarget: { operatorId: 'op-central', name: 'Central (FUSION)', endpoint: 'continuum-fusion-central.continuum.svc:4317', reachableFromOtherClusters: false },
    }))
    fusionStatus = fusionStarting()
    const user = userEvent.setup()
    renderPage()
    await toDestination(user)
    await waitFor(() => expect(screen.getByTestId('operator-destination-fusion-op-central')).toHaveAttribute('aria-checked', 'true'))
    await user.click(screen.getByTestId('operator-next'))
    await user.click(screen.getByTestId('operator-create'))
    const block = await screen.findByTestId('operator-created-export')
    expect(block).toHaveTextContent('nothing to install for it')
    expect(screen.getByTestId('operator-export-secret')).toHaveTextContent('op-central-export-mtls')
    expect(screen.getByTestId('operator-created-export-unreachable')).toHaveTextContent('Reachable at')
    const steps = screen.getByTestId('operator-created-steps').textContent ?? ''
    expect(steps.indexOf('op-central-export-mtls')).toBeLessThan(steps.indexOf('helm install op-1'))
    expect(screen.getByTestId('operator-created-fusion-starting')).toHaveTextContent('safe to run now')
    // A dot and words, not a second spinner.
    expect(within(screen.getByTestId('operator-created-fusion-starting')).queryByRole('status')).not.toBeInTheDocument()
  })

  test('the created screen offers Connect <cluster> for each source cluster with an agent, starting the wizard with the new operator chosen', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))
    const button = await screen.findByRole('button', { name: 'Connect edge-1' })
    await user.click(button)
    expect(telemetryStart).toHaveBeenCalledWith('a1', undefined, 'op-1')
  })

  test('the lists are read again as soon as the operator exists', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    listOperators.mockClear()
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalled())
    await waitFor(() => expect(listOperators).toHaveBeenCalled())
  })
})

describe('RegionalOperatorsPage - FUSION on the page', () => {
  test('a change of FUSION\'s state is announced by a live region that carries only the state\'s word', async () => {
    fusionStatus = fusionStarting()
    renderPage()
    const live = await screen.findByTestId('fusion-live')
    expect(live).toHaveAttribute('aria-live', 'polite')
    expect(live).toHaveTextContent('FUSION: Starting')
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

  test('enabling or disabling FUSION reads the operators again: the central operator comes and goes with it', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('fusion-enable'))
    await waitFor(() => expect(enableFusion).toHaveBeenCalled())
    listOperators.mockClear()
    listOperators.mockResolvedValue([centralOp()])
    await user.click(await screen.findByTestId('fusion-disable'))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(disableFusion).toHaveBeenCalled())
    await waitFor(() => expect(listOperators).toHaveBeenCalled())
  })

  test('the panel opens Grafana and Prometheus in a new tab once they are up, and shows them waiting before that', async () => {
    fusionStatus = { ...fusionStarting(), components: [...parts(1, 1), { component: 'grafana' as const, label: 'Grafana', desired: 1, ready: 0 }].map((c, i) => (i === 2 ? { ...c, ready: 0 } : c)) }
    const { unmount } = renderPage()
    const grafana = await screen.findByTestId('fusion-open-grafana')
    expect(grafana).toHaveAttribute('aria-disabled', 'true')
    expect(grafana).not.toHaveAttribute('href')
    expect(screen.getByTestId('fusion-waiting')).toBeInTheDocument()
    unmount()

    fusionStatus = { ...fusionRunning(), links: { prometheus: '/fusion/prometheus/', grafana: '/fusion/grafana/' } }
    renderPage()
    const g = await screen.findByTestId('fusion-open-grafana')
    expect(g).not.toHaveAttribute('aria-disabled')
    // The tab opens inside the click and is sent to the ticketed address once the server has given it.
    const tab = { location: { href: '' }, close: vi.fn(), opener: {} as unknown }
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    const user = userEvent.setup()
    await user.click(g)
    expect(openSpy).toHaveBeenCalledWith('', '_blank')
    await waitFor(() => expect(tab.location.href).toBe('/fusion/grafana/?ikhnos_ticket=t1'))
    expect(openFusionPage).toHaveBeenCalledWith(expect.anything(), 'grafana')
    expect(tab.opener).toBeNull()
    await user.click(screen.getByTestId('fusion-open-prometheus'))
    await waitFor(() => expect(tab.location.href).toBe('/fusion/prometheus/?ikhnos_ticket=t1'))
    openSpy.mockRestore()
    expect(screen.queryByTestId('fusion-waiting')).not.toBeInTheDocument()
    expect(screen.getByTestId('fusion-links-note')).toHaveTextContent('open through this server')
  })

  test('opening a page says why when the server refuses, closes the empty tab, and says when the browser blocks the tab', async () => {
    fusionStatus = { ...fusionRunning(), links: { prometheus: '/fusion/prometheus/', grafana: '/fusion/grafana/' } }
    renderPage()
    const user = userEvent.setup()
    const tab = { location: { href: '' }, close: vi.fn(), opener: {} as unknown }
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    openFusionPage.mockRejectedValueOnce(new ApiError(409, 'Grafana is not running yet'))
    await user.click(await screen.findByTestId('fusion-open-grafana'))
    expect(await screen.findByTestId('fusion-open-error')).toHaveTextContent('Grafana is not running yet')
    expect(tab.close).toHaveBeenCalled()
    expect(tab.location.href).toBe('')
    openSpy.mockReturnValue(null)
    await user.click(screen.getByTestId('fusion-open-prometheus'))
    expect(await screen.findByTestId('fusion-open-error')).toHaveTextContent('blocked the new tab')
    expect(openFusionPage).toHaveBeenCalledTimes(1)
    openSpy.mockRestore()
  })

  test('an exposed central operator says where other clusters reach it', async () => {
    fusionStatus = { ...fusionRunning(), central: { ...central, exposed: true, endpoint: 'fusion.example.com:4317' } }
    renderPage()
    expect(await screen.findByTestId('fusion-exposure')).toHaveTextContent('reachable from other clusters at fusion.example.com:4317')
  })

  test('there is one spinner on the page while FUSION starts, and none in the table rows', async () => {
    fusionStatus = fusionStarting()
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'waiting-op', health: { state: 'waiting', reporting: true } }),
      centralOp({ health: { state: 'starting', reporting: false } }),
    ])
    renderPage()
    await screen.findByTestId('operator-waiting-op')
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(within(screen.getByTestId('operator-waiting-op')).getByTestId('operator-health')).toHaveTextContent('Waiting for first heartbeat')
  })

  test('the issued certificates are listed by who holds them, with the cluster named, and the empty case says why', async () => {
    listOperators.mockResolvedValue([op()])
    const cert = (over: Partial<IssuedCertificate>): IssuedCertificate => ({
      serial: 'aa', kind: 'client', subject: 'op-1-export-c1', sender: 'c1', issuedBy: 'alex', issuedAt: '2026-01-02T10:00:00Z', notBefore: '2026-01-02T09:55:00Z', notAfter: '2027-01-02T10:00:00Z', state: 'ok', ...over,
    })
    listOperatorCertificates.mockResolvedValueOnce({ certificates: [cert({}), cert({ serial: 'bb', kind: 'receiver', sender: undefined, subject: 'op-1', state: 'expired', notAfter: '2026-11-01T00:00:00Z' })] })
    renderPage()
    const user = userEvent.setup()
    await rowAction(user, 'athens-regional', 'certs')
    const table = await screen.findByTestId('operator-certs-table')
    expect(listOperatorCertificates).toHaveBeenCalledWith(expect.anything(), 'op-1')
    expect(within(screen.getByTestId('operator-cert-aa')).getByText('edge-1')).toBeInTheDocument()
    expect(within(screen.getByTestId('operator-cert-aa')).getByText('Valid')).toBeInTheDocument()
    expect(within(screen.getByTestId('operator-cert-bb')).getByText('The operator (its receiver)')).toBeInTheDocument()
    expect(within(screen.getByTestId('operator-cert-bb')).getByText('Ended')).toBeInTheDocument()
    expect(table.textContent).not.toMatch(/PRIVATE KEY|BEGIN CERTIFICATE/)
    await user.click(screen.getByTestId('operator-certs-done'))
    await waitFor(() => expect(screen.queryByTestId('operator-certs-table')).not.toBeInTheDocument())

    await rowAction(user, 'athens-regional', 'certs')
    expect(await screen.findByTestId('operator-certs-empty')).toHaveTextContent('Nothing recorded yet')
  })

  test('the central operator is listed as managed by FUSION: one action, no revoke, no delete, and its state follows FUSION', async () => {
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([centralOp()])
    const user = userEvent.setup()
    renderPage()
    const row = await screen.findByTestId('operator-Central (FUSION)')
    expect(within(row).getByTestId('operator-central-state')).toHaveTextContent('Running')
    await user.click(within(row).getByTestId('operator-menu-Central (FUSION)'))
    expect(screen.getByTestId('operator-address-open-Central (FUSION)')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-revoke-Central (FUSION)')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-delete-Central (FUSION)')).not.toBeInTheDocument()
    expect(within(row).queryByText('Active')).not.toBeInTheDocument()
  })

  test('the table names an operator that sends to the central operator as FUSION, not by an empty endpoint', async () => {
    listOperators.mockResolvedValue([op({ name: 'athens', destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-central' } }), op({ id: 'op-2', name: 'beta', destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-athens' } }), op({ id: 'op-athens', name: 'the-hub' })])
    renderPage()
    expect(await screen.findByTestId('operator-destination-athens')).toHaveTextContent(/^FUSION$/)
    expect(screen.getByTestId('operator-destination-beta')).toHaveTextContent('the-hub')
  })
})

describe('RegionalOperatorsPage - the table', () => {
  test('the status says Online, Offline with when, or not reported - in words, and with no neutral Active pill', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'online-op', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
      op({ id: 'op-b', name: 'offline-op', health: { state: 'offline', lastSeenAt: minutesAgo(20), reporting: true } }),
      op({ id: 'op-c', name: 'quiet-op' }),
      op({ id: 'op-d', name: 'gone-op', status: 'revoked', reason: 'replaced' }),
    ])
    renderPage()
    const health = async (name: string) => within(await screen.findByTestId(`operator-${name}`)).getByTestId('operator-health')
    expect(await health('online-op')).toHaveTextContent(/^Online$/)
    expect(await health('online-op')).toHaveAttribute('data-health', 'online')
    expect(await health('offline-op')).toHaveTextContent('Offline, last seen 20 min ago')
    expect(await health('quiet-op')).toHaveTextContent('Health not reported')
    expect(screen.queryByText('Active')).not.toBeInTheDocument()
    expect(within(screen.getByTestId('operator-gone-op')).getByText('Revoked: replaced')).toBeInTheDocument()
    expect(within(screen.getByTestId('operator-gone-op')).queryByTestId('operator-health')).not.toBeInTheDocument()
  })

  test('an operator from an older server (no health, no receiverAuth) reads as not reported', async () => {
    const { health: _h, receiverAuth: _r, ...legacy } = op({ name: 'legacy-op' })
    listOperators.mockResolvedValue([legacy as RegionalOperator])
    renderPage()
    expect(within(await screen.findByTestId('operator-legacy-op')).getByTestId('operator-health')).toHaveTextContent('Health not reported')
  })

  test('Created is relative, with the exact time in a tooltip', async () => {
    listOperators.mockResolvedValue([op({ createdAt: new Date(Date.now() - 3 * 86_400_000).toISOString() })])
    renderPage()
    const row = await screen.findByTestId('operator-athens-regional')
    expect(within(row).getByText('3 days ago')).toHaveAttribute('title')
  })

  test('the row actions are in one menu whose name carries the operator, and Escape closes it', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'one' }), op({ id: 'op-b', name: 'two' })])
    const user = userEvent.setup()
    renderPage()
    const button = await screen.findByRole('button', { name: 'Actions for two' })
    expect(screen.getByRole('button', { name: 'Actions for one' })).toBeInTheDocument()
    expect(button).toHaveAttribute('aria-expanded', 'false')
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    const menu = screen.getByRole('menu', { name: 'Actions for two' })
    expect(within(menu).getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Reachable at…', 'Enable health reporting', 'Issued certificates', 'Renew certificates…', 'Revoke…', 'Delete…'])
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    // Nothing but the menu button sits in the actions cell: the row is not a row of buttons.
    expect(within(screen.getByTestId('operator-two')).getAllByRole('button').filter((b) => b.getAttribute('aria-label')?.startsWith('Actions'))).toHaveLength(1)
  })

  test('a revoked operator can only be deleted', async () => {
    listOperators.mockResolvedValue([op({ status: 'revoked' })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-menu-athens-regional'))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Delete…'])
  })

  test('the address cell: a recorded address with a copy button, "Needs an address" in amber, and this cluster only', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'local-op', addressState: 'none' }),
      op({ id: 'op-b', name: 'hub-op', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true, addressState: 'set' }),
      op({ id: 'op-c', name: 'lb-op', exposure: 'loadbalancer', addressState: 'pending' }),
      op({ id: 'op-d', name: 'gone-op', status: 'revoked' }),
    ])
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByTestId('operator-address-local-op')).toHaveTextContent('This cluster only')
    expect(screen.getByTestId('operator-address-hub-op')).toHaveTextContent('otlp.eu.example.com:4317')
    expect(within(screen.getByTestId('operator-address-hub-op')).getByRole('button', { name: 'Copy the address of hub-op' })).toBeInTheDocument()
    expect(screen.getByTestId('operator-address-hub-op')).toHaveAttribute('data-address', 'set')
    const pending = screen.getByTestId('operator-address-lb-op')
    expect(pending).toHaveAttribute('data-address', 'pending')
    expect(within(pending).getByRole('button', { name: 'Needs an address' })).toHaveClass('text-warn')
    expect(screen.queryByTestId('operator-address-gone-op')).not.toBeInTheDocument()
    // The amber text is the way to the dialog that records it.
    await user.click(within(pending).getByRole('button', { name: 'Needs an address' }))
    expect(screen.getByTestId('operator-address-input')).toBeInTheDocument()
  })

  test('an older server (no addressState) still reads: a recorded address, or what was installed', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-b', name: 'hub-op', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true }),
      op({ id: 'op-c', name: 'lb-op', exposure: 'loadbalancer' }),
      op({ id: 'op-e', name: 'own-op', exposure: 'cluster' }),
    ])
    renderPage()
    expect(await screen.findByTestId('operator-address-hub-op')).toHaveTextContent('otlp.eu.example.com:4317')
    expect(screen.getByTestId('operator-address-lb-op')).toHaveTextContent('Needs an address')
    expect(screen.getByTestId('operator-address-own-op')).toHaveTextContent('This cluster only')
  })

  test('certificates that are running out say how long, in amber, and expired ones in red, each with a way to renew them', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'fine-op', certState: 'ok', certs: { receiverNotAfter: daysFromNow(300) } }),
      op({ id: 'op-b', name: 'soon-op', certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(40), clientNotAfter: daysFromNow(12) } }),
      op({ id: 'op-c', name: 'late-op', certState: 'expired', certs: { receiverNotAfter: daysFromNow(-3) } }),
      op({ id: 'op-d', name: 'old-op' }),
    ])
    renderPage()
    expect(await screen.findByTestId('operator-cert-soon-op')).toHaveTextContent('Certificate renewal is failing')
    expect(screen.getByTestId('operator-cert-soon-op')).toHaveClass('text-warn')
    expect(screen.getByTestId('operator-cert-late-op')).toHaveTextContent('Certificate expired')
    expect(screen.getByTestId('operator-cert-late-op')).toHaveClass('text-bad')
    expect(screen.queryByTestId('operator-cert-fine-op')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-cert-old-op')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-cert-renew-soon-op')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-cert-renew-fine-op')).not.toBeInTheDocument()
  })

  test('Renew certificates asks the server to install again and shows what it returns on the same screen as a new operator', async () => {
    listOperators.mockResolvedValue([op({ certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(10) } })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-cert-renew-athens-regional'))
    await confirmRenew(user)
    await waitFor(() => expect(reinstallOperator).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-1'))
    const dialog = await screen.findByRole('dialog', { name: 'Renew certificates for athens-regional' })
    const text = within(dialog).getByTestId('operator-created-steps').textContent ?? ''
    expect(text.indexOf('RENEWED-TLS-SECRET-CMD')).toBeLessThan(text.indexOf('REINSTALL-CMD'))
    expect(within(dialog).getByTestId('operator-created-intro')).toHaveTextContent('new certificates were issued just now')
    expect(within(dialog).getByRole('button', { name: 'Connect edge-1' })).toBeInTheDocument()
  })

  test('a renewal also shows what the server says to run around the install: the CA Secret before it, the restart after it', async () => {
    reinstallOperator.mockResolvedValueOnce({
      operator: { id: 'op-1', name: 'athens-regional', status: 'active', sourceClusterIds: ['c1'], receiverAuth: 'mtls' } as CreatedOperator['operator'],
      install: 'REINSTALL-CMD',
      tlsSecretCommand: 'RENEWED-TLS-SECRET-CMD',
      heartbeatToken: 'cnh_new',
      heartbeatSecretCommand: 'HB-SECRET-CMD',
      heartbeatCaSecretCommand: 'HB-CA-SECRET-CMD',
      heartbeatUrl: 'https://continuum.example.com/api/v1/operator-heartbeat',
      heartbeatIntervalSeconds: 60,
      restartCommand: 'RESTART-CMD',
      reminders: [] as string[],
    })
    listOperators.mockResolvedValue([op({ certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(10) } })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-cert-renew-athens-regional'))
    await confirmRenew(user)
    const dialog = await screen.findByRole('dialog', { name: 'Renew certificates for athens-regional' })
    const text = within(dialog).getByTestId('operator-created-steps').textContent ?? ''
    expect(text.indexOf('HB-CA-SECRET-CMD')).toBeGreaterThan(-1)
    expect(text.indexOf('HB-CA-SECRET-CMD')).toBeLessThan(text.indexOf('REINSTALL-CMD'))
    expect(text.indexOf('RESTART-CMD')).toBeGreaterThan(text.indexOf('REINSTALL-CMD'))
  })

  test('the same action is in the menu, and a refusal is shown, with nothing displayed', async () => {
    reinstallOperator.mockRejectedValueOnce(new ApiError(409, 'this operator is revoked'))
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'renew')
    await confirmRenew(user)
    expect(await screen.findByText('this operator is revoked')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  test('Connect <cluster> in the Sources cell starts the wizard for that cluster\'s agent with the operator chosen', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Connect edge-1' }))
    expect(telemetryStart).toHaveBeenCalledWith('a1', undefined, 'op-1')
  })

  test('with several sources the cell offers one Connect menu, one entry per cluster with an agent', async () => {
    topologyState = { agents: [ag(), ag({ id: 'a2', clusterId: 'c2' }), ag({ id: 'a3', clusterId: 'c3', status: 'pending' })], clusters: [cl(), cl({ id: 'c2', name: 'edge-2' }), cl({ id: 'c3', name: 'edge-3' })] }
    listOperators.mockResolvedValue([op({ sourceClusterIds: ['c1', 'c2', 'c3'] })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Connect a cluster to athens-regional' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Connect edge-1', 'Connect edge-2'])
    await user.click(screen.getByRole('menuitem', { name: 'Connect edge-2' }))
    expect(telemetryStart).toHaveBeenCalledWith('a2', undefined, 'op-1')
  })

  test('someone who cannot edit gets no Connect', async () => {
    canEditFlag = false
    listOperators.mockResolvedValue([op()])
    renderPage()
    await screen.findByTestId('operator-athens-regional')
    expect(screen.queryByRole('button', { name: /Connect/ })).not.toBeInTheDocument()
  })

  test('a list that is read again shows what changed without a reload: a new operator, and a health that flipped', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      listOperators.mockResolvedValue([op({ id: 'op-a', name: 'first-op' })])
      renderPage()
      expect(await screen.findByTestId('operator-first-op')).toBeInTheDocument()
      listOperators.mockResolvedValue([op({ id: 'op-a', name: 'first-op', health: { state: 'online', lastSeenAt: minutesAgo(0), reporting: true } }), op({ id: 'op-b', name: 'second-op' })])
      await vi.advanceTimersByTimeAsync(13_000)
      expect(await screen.findByTestId('operator-second-op')).toBeInTheDocument()
      expect(within(screen.getByTestId('operator-first-op')).getByTestId('operator-health')).toHaveTextContent('Online')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('RegionalOperatorsPage - revoking and deleting', () => {
  test('an operator nothing depends on is revoked on one confirmation, with no acknowledgement to tick', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'revoke')
    expect(screen.queryByTestId('operator-used-by')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-remove-force')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('operator-remove-confirm'))
    await waitFor(() => expect(revokeOperator).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-1', 'revoked in the UI', false))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  test('what depends on the operator is listed, and revoking needs the acknowledgement, which is sent as force', async () => {
    listOperators.mockResolvedValue([op({ usedBy: { operators: [{ id: 'op-9', name: 'edge-hub' }], clusters: 2, intents: 1 } })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'revoke')
    const used = screen.getByTestId('operator-used-by')
    expect(used).toHaveTextContent('Operator sending to it: edge-hub')
    expect(used).toHaveTextContent('2 clusters send to it')
    expect(used).toHaveTextContent('1 telemetry request names it')
    expect(screen.getByTestId('operator-remove-confirm')).toBeDisabled()
    await user.click(screen.getByTestId('operator-remove-force'))
    expect(screen.getByTestId('operator-remove-confirm')).toBeEnabled()
    await user.click(screen.getByTestId('operator-remove-confirm'))
    await waitFor(() => expect(revokeOperator).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-1', 'revoked in the UI', true))
  })

  test('after revoking, the command that removes it from the cluster is shown, since revoking never reaches the collector', async () => {
    revokeOperator.mockResolvedValue({ uninstall: 'helm uninstall op-1 --namespace continuum-system' })
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'revoke')
    await user.click(screen.getByTestId('operator-remove-confirm'))
    expect(await screen.findByTestId('operator-uninstall-command')).toHaveTextContent('helm uninstall op-1 --namespace continuum-system')
    expect(screen.getByTestId('operator-remove-uninstall-note')).toHaveTextContent('keeps running')
    await user.click(screen.getByTestId('operator-remove-done'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  test('a 409 from the server (something started depending on it meanwhile) shows its message and then asks for the acknowledgement', async () => {
    revokeOperator.mockRejectedValueOnce(new ApiError(409, 'edge-hub still sends to this operator'))
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'revoke')
    await user.click(screen.getByTestId('operator-remove-confirm'))
    expect(await screen.findByText('edge-hub still sends to this operator')).toBeInTheDocument()
    expect(screen.getByTestId('operator-remove-confirm')).toBeDisabled()
    await user.click(screen.getByTestId('operator-remove-force'))
    await user.click(screen.getByTestId('operator-remove-confirm'))
    await waitFor(() => expect(revokeOperator).toHaveBeenLastCalledWith({ url: '', org: 'o' }, 'op-1', 'revoked in the UI', true))
  })

  test('deleting follows the same rules: the used-by list, the acknowledgement, force, and the uninstall command', async () => {
    deleteOperator.mockResolvedValue({ uninstall: 'helm uninstall op-1' })
    listOperators.mockResolvedValue([op({ usedBy: { operators: [], clusters: 1, intents: 0 } })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'delete')
    expect(screen.getByTestId('operator-used-by')).toHaveTextContent('1 cluster sends to it')
    await user.click(screen.getByTestId('operator-remove-force'))
    await user.click(screen.getByTestId('operator-remove-confirm'))
    await waitFor(() => expect(deleteOperator).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-1', true))
    expect(await screen.findByTestId('operator-uninstall-command')).toHaveTextContent('helm uninstall op-1')
  })

  test('the list is read again once it is done', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'delete')
    listOperators.mockClear()
    await user.click(screen.getByTestId('operator-remove-confirm'))
    await waitFor(() => expect(listOperators).toHaveBeenCalled())
  })
})

describe('RegionalOperatorsPage - Local tab', () => {
  const installedAgent = (over: Record<string, unknown> = {}) =>
    ag({ name: 'edge-agent', diagnostics: { installedTelemetry: ['resourceUsage', 'systemLogs'], reportedAt: '2026-01-02T00:00:00Z', ...over } } as Partial<Agent>)
  const intent = (over: Partial<TelemetryIntent> = {}): TelemetryIntent => ({
    id: 'ti-1', agentId: 'a1', name: 'to eu', status: 'active', namespaces: [], exclude: [],
    signals: [{ id: 'resourceUsage', source: 'builtin' }, { id: 'systemLogs', source: 'builtin' }],
    destination: { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' },
    createdAt: '2026-01-01T00:00:00Z', createdBy: 'me', ...over,
  })

  test('with no approved agent reporting any telemetry signal, the Local tab shows its own empty state', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(screen.getByText('No local operators running yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Configure telemetry/ })).toBeInTheDocument()
  })

  test('an approved agent with installed signals lists as a local operator, with a Configure action named for its cluster', async () => {
    const user = userEvent.setup()
    admin = false // regional operators stay admin-only; the local tab must not depend on that
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(screen.getByTestId('local-operator-edge-agent')).toBeInTheDocument()
    expect(screen.getByText('Resource usage')).toBeInTheDocument()
    expect(screen.getByText('System logs')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Configure edge-1' }))
    expect(telemetryStart).toHaveBeenCalledWith('a1')
  })

  test('the Configure action is hidden for a viewer who cannot edit', async () => {
    const user = userEvent.setup()
    canEditFlag = false
    topologyState = { agents: [installedAgent({ installedTelemetry: ['resourceUsage'] })], clusters: [cl()] }
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(screen.getByTestId('local-operator-edge-agent')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Configure/ })).not.toBeInTheDocument()
  })

  test('the destination comes from the agent\'s request: the operator\'s name with its health, from the full list for an administrator', async () => {
    listTelemetryIntents.mockResolvedValue([intent()])
    listOperators.mockResolvedValue([op({ id: 'op-eu', name: 'EU hub', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } })])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    const cell = await screen.findByTestId('local-destination-edge-agent')
    await waitFor(() => expect(cell).toHaveTextContent('EU hub'))
    expect(within(cell).getByTestId('operator-health')).toHaveTextContent('Online')
  })

  test('someone who is not an administrator gets the same from the read model', async () => {
    admin = false
    listTelemetryIntents.mockResolvedValue([intent()])
    listOperatorDestinations.mockResolvedValue([{ id: 'op-eu', name: 'EU hub', kind: 'regional', status: 'active', health: { state: 'online', lastSeenAt: minutesAgo(1) }, addressState: 'none', reachableFromOtherClusters: false, recommended: false, usedBy: 0 }])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    const cell = await screen.findByTestId('local-destination-edge-agent')
    await waitFor(() => expect(cell).toHaveTextContent('EU hub'))
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('an external destination is shown as its endpoint', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ destination: { kind: 'external', endpoint: 'otel.example.com:4317' } })])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(await screen.findByTestId('local-destination-edge-agent')).toHaveTextContent('otel.example.com:4317')
  })

  test('the data dot says whether the destination is accepting data, in words', async () => {
    topologyState = {
      agents: [
        installedAgent({ exportHealth: { podsReached: 1, podsFailed: 0, routes: [{ exporter: 'otlp', signal: 'metrics', state: 'exporting', sent: 5, failed: 0 }, { exporter: 'otlp', signal: 'logs', state: 'failing', sent: 0, failed: 3 }] } }),
        ag({ id: 'a2', clusterId: 'c2', name: 'quiet-agent', diagnostics: { installedTelemetry: ['resourceUsage'], reportedAt: '2026-01-02T00:00:00Z' } } as Partial<Agent>),
      ],
      clusters: [cl(), cl({ id: 'c2', name: 'edge-2' })],
    }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(screen.getByTestId('local-data-edge-agent')).toHaveTextContent('Failing')
    expect(screen.getByTestId('local-data-edge-agent')).toHaveAttribute('data-state', 'offline')
    expect(screen.getByTestId('local-data-quiet-agent')).toHaveTextContent('Not reported')
  })

  test('an agent asked for telemetry that has not reported it yet is a row with a hollow ring, under one waiting banner - the only spinner', async () => {
    listTelemetryIntents.mockResolvedValue([intent()])
    topologyState = { agents: [ag({ name: 'edge-agent' })], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    const row = await screen.findByTestId('local-operator-edge-agent')
    expect(within(row).getByTestId('local-data-edge-agent')).toHaveTextContent('Nothing reported running yet')
    expect(within(row).getByTestId('local-data-edge-agent')).toHaveAttribute('data-state', 'starting')
    expect(within(row).getByText('Resource usage')).toBeInTheDocument()
    expect(screen.getByTestId('local-waiting')).toHaveTextContent('Waiting for 1 cluster to report')
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })

  test('once every agent has reported, the waiting banner is gone', async () => {
    listTelemetryIntents.mockResolvedValue([intent()])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    await screen.findByTestId('local-operator-edge-agent')
    expect(screen.queryByTestId('local-waiting')).not.toBeInTheDocument()
  })

  test('a drifted chip shows when what the agent runs is not what was asked of it, and not when they agree', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ signals: [{ id: 'resourceUsage', source: 'builtin' }, { id: 'traces', source: 'builtin' }] })])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    const user = userEvent.setup()
    const { unmount } = renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(await screen.findByTestId('local-drifted-edge-agent')).toHaveTextContent('Drifted')
    unmount()

    listTelemetryIntents.mockResolvedValue([intent()])
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    await screen.findByTestId('local-operator-edge-agent')
    await waitFor(() => expect(screen.queryByTestId('local-drifted-edge-agent')).not.toBeInTheDocument())
  })

  test('a revoked request is not what was asked, and an agent with only that is not listed', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ status: 'revoked' })])
    topologyState = { agents: [ag({ name: 'edge-agent' })], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operators-local'))
    expect(await screen.findByText('No local operators running yet')).toBeInTheDocument()
  })
})

describe('RegionalOperatorsPage - health reporting', () => {
  test('the action says Enable for a non-reporting operator and Rotate for a reporting one', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'quiet-op' }),
      op({ id: 'op-b', name: 'live-op', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
    ])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-menu-quiet-op'))
    expect(screen.getByTestId('operator-health-open-quiet-op')).toHaveTextContent('Enable health reporting')
    await user.keyboard('{Escape}')
    await user.click(screen.getByTestId('operator-menu-live-op'))
    expect(screen.getByTestId('operator-health-open-live-op')).toHaveTextContent('Rotate health credential')
  })

  test('a private CA for the heartbeat address comes with its own Secret command, between the credential and the upgrade', async () => {
    enableOperatorHeartbeat.mockResolvedValueOnce({
      operator: op(), rotated: false, heartbeatToken: 'cnh_new',
      heartbeatSecretCommand: 'SECRET-CMD', heartbeatUpgradeCommand: 'UPGRADE-CMD', heartbeatCaSecretCommand: 'CA-SECRET-CMD',
      heartbeatUrl: 'https://continuum.local/api/v1/operator-heartbeat', heartbeatIntervalSeconds: 60,
    })
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'health-open')
    await user.click(screen.getByTestId('operator-health-confirm'))
    const text = (await screen.findByTestId('operator-health-commands')).textContent ?? ''
    expect(text.indexOf('SECRET-CMD')).toBeLessThan(text.indexOf('CA-SECRET-CMD'))
    expect(text.indexOf('CA-SECRET-CMD')).toBeLessThan(text.indexOf('UPGRADE-CMD'))
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
    await rowAction(user, 'athens-regional', 'health-open')

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
    await rowAction(user, 'athens-regional', 'health-open')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(enableOperatorHeartbeat).not.toHaveBeenCalled()
    expect(screen.queryByTestId('operator-health-explain')).not.toBeInTheDocument()

    await rowAction(user, 'athens-regional', 'health-open')
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
    await rowAction(user, 'athens-regional', 'health-open')
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
    expect(screen.getByTestId('operator-heartbeat')).toBeChecked()
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
    expect(mtls).toHaveTextContent('issued per agent when its commands are generated')
    expect(mtls).toHaveTextContent('own certificate authority')
    expect(screen.queryByText(/receiver token Secret/)).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-secret-command')).not.toBeInTheDocument()
    expect(screen.getByTestId('operator-no-rbac-note')).toHaveTextContent('It contacts this server only to send its health check')
    const text = screen.getByTestId('operator-created-steps').textContent ?? ''
    expect(text.indexOf('TLS-SECRET-CMD')).toBeLessThan(text.indexOf('HEARTBEAT-SECRET-CMD'))
    expect(text.indexOf('HEARTBEAT-SECRET-CMD')).toBeLessThan(text.indexOf('INSTALL-CMD'))
    // Said once, in the intro: the step itself is named for it and does not repeat it in a second warning.
    expect(screen.getByTestId('operator-created-intro')).toHaveTextContent('health credential is shown only now')
    expect(screen.queryByTestId('operator-created-heartbeat-once')).not.toBeInTheDocument()
  })

  test('a bearer operator shows its receiver token Secret first, then its TLS Secret, then the install, with no heartbeat block when not requested', async () => {
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
    const text = screen.getByTestId('operator-created-steps').textContent ?? ''
    expect(text).toContain('Create the receiver token Secret')
    expect(text.indexOf('TOKEN-SECRET-CMD')).toBeLessThan(text.indexOf('TLS-SECRET-CMD'))
    expect(text.indexOf('TLS-SECRET-CMD')).toBeLessThan(text.indexOf('INSTALL-CMD'))
    expect(screen.queryByTestId('operator-created-mtls')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-created-heartbeat')).not.toBeInTheDocument()
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
  test('choosing a load balancer sends it, and the created screen says how to read the address and where to record it', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-exposure'))
    await user.click(screen.getByRole('option', { name: /load balancer/ }))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
    expect(createOperator.mock.calls[0][4]).toMatchObject({ exposure: 'loadbalancer' })
    const note = await screen.findByTestId('operator-created-address')
    expect(note).toHaveTextContent('Reachable at')
    // The load balancer line only, since that is what was chosen; it names the operator's own Service.
    expect(screen.getByTestId('operator-created-find-lb')).toHaveTextContent('kubectl get svc op-1-regional-operator --namespace continuum-system')
    expect(screen.queryByTestId('operator-created-find-np')).not.toBeInTheDocument()
  })

  test('the default is this cluster only: nothing about addresses on the created screen', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    expect(screen.getByTestId('operator-exposure')).toHaveTextContent('This cluster only')
    await user.click(screen.getByTestId('operator-create'))
    await screen.findByText(/helm install op-1/)
    expect(screen.queryByTestId('operator-created-address')).not.toBeInTheDocument()
  })

  test('the dialog shows only the lookup that fits how the operator was exposed', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'lb-op', exposure: 'loadbalancer' }),
      op({ id: 'op-c', name: 'own-op', exposure: 'cluster' }),
    ])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'lb-op', 'address-open')
    expect(screen.getByTestId('operator-address-find-lb')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-address-find-np')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await rowAction(user, 'own-op', 'address-open')
    expect(screen.queryByTestId('operator-address-find-lb')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operator-address-find-np')).not.toBeInTheDocument()
  })

  test('an Ingress is said to need TCP/TLS passthrough only, next to the hint about it', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'lb-op', exposure: 'loadbalancer' })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'lb-op', 'address-open')
    const hint = screen.getByTestId('operator-address-find-passthrough')
    expect(hint).toHaveTextContent('TCP/TLS passthrough only')
    expect(hint.closest('p')).toHaveTextContent('Ingress')
  })

  test('typing an address shows the connection check for it, with the default port', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'local-op' })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'local-op', 'address-open')
    expect(screen.queryByTestId('operator-address-check')).not.toBeInTheDocument()
    await user.type(screen.getByTestId('operator-address-input'), 'otlp.eu.example.com')
    const check = screen.getByTestId('operator-address-check-command')
    expect(check).toHaveTextContent('openssl s_client -connect otlp.eu.example.com:4317 -servername op-a.continuum-system.svc')
    expect(screen.getByTestId('operator-address-check')).toHaveTextContent('DNS:op-a.continuum-system.svc')
  })

  test('the central operator has its own Reachable at: it says how to expose the gateway and reads its address from the gateway Service', async () => {
    fusionStatus = { ...fusionRunning(), central: { ...central, service: 'continuum-fusion-central', namespace: 'continuum' } }
    listOperators.mockResolvedValue([centralOp()])
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByTestId('operator-address-Central (FUSION)')).toHaveTextContent('This cluster only')
    await rowAction(user, 'Central (FUSION)', 'address-open')
    expect(screen.getByTestId('operator-address-central')).toHaveTextContent('fusion.central.service.type')
    expect(screen.getByTestId('operator-address-find-lb')).toHaveTextContent('kubectl get svc continuum-fusion-central --namespace continuum')
    await user.type(screen.getByTestId('operator-address-input'), 'fusion.example.com')
    await user.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setOperatorAddress).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-central', 'fusion.example.com'))
  })

  test('the dialog records a trimmed address, shows how to find it, and reloads', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'local-op' })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'local-op', 'address-open')
    expect(screen.getByTestId('operator-address-explain')).toHaveTextContent('no certificate is reissued')
    expect(screen.getByTestId('operator-address-find-lb')).toHaveTextContent('kubectl get svc op-a-regional-operator')
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
    await rowAction(user, 'local-op', 'address-open')
    await user.type(screen.getByTestId('operator-address-input'), 'https://nope')
    await user.click(screen.getByTestId('operator-address-save'))
    expect(await screen.findByText('the address is a host and a port, such as otlp.example.com:4317')).toBeInTheDocument()
    expect(screen.getByTestId('operator-address-input')).toBeInTheDocument()
  })

  test('an operator with an address starts the dialog on it, and clearing sends an empty address', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-b', name: 'hub-op', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'hub-op', 'address-open')
    const input = screen.getByTestId('operator-address-input')
    expect(input).toHaveValue('otlp.eu.example.com:4317')
    await user.clear(input)
    await user.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setOperatorAddress).toHaveBeenCalledWith({ url: '', org: 'o' }, 'op-b', ''))
  })
})

describe('operatorServiceName', () => {
  test('follows the chart: the release name plus -regional-operator, unless it already says so', async () => {
    const { operatorServiceName } = await import('@/components/operators/OperatorAddress')
    expect(operatorServiceName('op-abc')).toBe('op-abc-regional-operator')
    expect(operatorServiceName('eu-regional-operator')).toBe('eu-regional-operator')
    expect(operatorServiceName('x'.repeat(70))).toHaveLength(63)
  })
})

/** The dialog's own backdrop: the element a click outside the box lands on. */
const backdropOf = (dialog: HTMLElement) => dialog.parentElement!

describe('RegionalOperatorsPage - renewing certificates asks first', () => {
  test('nothing is renewed until it is confirmed; the question names what is replaced and that the operator must be updated', async () => {
    listOperators.mockResolvedValue([op({ receiverAuth: 'bearer', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } })])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'renew')
    const dialog = await screen.findByRole('dialog', { name: 'Renew certificates for athens-regional?' })
    expect(reinstallOperator).not.toHaveBeenCalled()
    expect(dialog).toHaveTextContent('its certificates, its receiver token and its health credential')
    expect(dialog).toHaveTextContent('must be updated with the new commands')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(reinstallOperator).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  test('a certificate-only operator that does not report health names only its certificates', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'renew')
    const dialog = await screen.findByRole('dialog', { name: /^Renew certificates/ })
    expect(dialog).toHaveTextContent('This replaces its certificates.')
    expect(dialog).not.toHaveTextContent('receiver token')
    expect(dialog).not.toHaveTextContent('health credential')
  })

  test('while one renewal runs, every other renew control is disabled and says why, instead of ignoring the click', async () => {
    let finish: (c: CreatedOperator) => void = () => undefined
    reinstallOperator.mockImplementationOnce(() => new Promise<CreatedOperator>((r) => { finish = r }))
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'one', certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(10) } }),
      op({ id: 'op-b', name: 'two', certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(10) } }),
    ])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-cert-renew-one'))
    await confirmRenew(user)
    await waitFor(() => expect(reinstallOperator).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('operator-cert-renew-two')).toBeDisabled()
    expect(screen.getByTestId('operator-cert-renew-one')).toBeDisabled()
    await user.click(screen.getByTestId('operator-menu-two'))
    const item = await screen.findByTestId('operator-renew-two')
    expect(item).toBeDisabled()
    expect(item).toHaveAttribute('title', 'Another renewal is in progress')
    finish({ operator: { id: 'op-a', name: 'one', status: 'active', sourceClusterIds: [], receiverAuth: 'mtls' } as CreatedOperator['operator'], install: 'X', reminders: [] })
    await screen.findByRole('dialog', { name: 'Renew certificates for one' })
  })
})

describe('RegionalOperatorsPage - a secret shown once cannot be lost to a stray click', () => {
  test('the created screen ignores Escape and a click outside, holds the page from reloading, and Done is the way out', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))
    const dialog = await screen.findByRole('dialog', { name: 'athens-regional created' })
    expect(isReloadHeld()).toBe(true)
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(backdropOf(dialog))
    expect(screen.getByRole('dialog', { name: 'athens-regional created' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument() // no header X either
    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(isReloadHeld()).toBe(false)
  })

  test('the renewed credentials screen is held the same way', async () => {
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'renew')
    await confirmRenew(user)
    const dialog = await screen.findByRole('dialog', { name: 'Renew certificates for athens-regional' })
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(backdropOf(dialog))
    expect(screen.getByRole('dialog', { name: 'Renew certificates for athens-regional' })).toBeInTheDocument()
  })

  test('the health credential, once minted, is held; before it, and not while it is being minted, the dialog closes as usual', async () => {
    listOperators.mockResolvedValue([op()])
    let finish: (r: OperatorHeartbeatEnabled) => void = () => undefined
    enableOperatorHeartbeat.mockImplementationOnce(() => new Promise<OperatorHeartbeatEnabled>((r) => { finish = r }))
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'health-open')
    await user.click(screen.getByTestId('operator-health-confirm'))
    // In flight: the credential is on its way to this dialog, so no way out - Escape, a click outside, Cancel.
    const asking = await screen.findByRole('dialog', { name: /Enable health reporting for/ })
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(backdropOf(asking))
    expect(screen.getByRole('dialog', { name: /Enable health reporting for/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    finish({ operator: op(), rotated: false, heartbeatToken: 'cnh_secret', heartbeatSecretCommand: 'SECRET-CMD', heartbeatUpgradeCommand: 'UPGRADE-CMD', heartbeatUrl: 'https://x/api/v1/operator-heartbeat', heartbeatIntervalSeconds: 60 })
    const shown = await screen.findByRole('dialog', { name: 'Health reporting enabled for athens-regional' })
    expect(isReloadHeld()).toBe(true)
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(backdropOf(shown))
    expect(screen.getByRole('dialog', { name: 'Health reporting enabled for athens-regional' })).toBeInTheDocument()
    await user.click(screen.getByTestId('operator-health-done'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(isReloadHeld()).toBe(false)
  })

  test('while the operator is being created there is no way out of the dialog', async () => {
    let finish: (c: CreatedOperator) => void = () => undefined
    createOperator.mockImplementationOnce(() => new Promise<CreatedOperator>((r) => { finish = r }))
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))
    const dialog = screen.getByRole('dialog', { name: 'New operator' })
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(backdropOf(dialog))
    expect(screen.getByRole('dialog', { name: 'New operator' })).toBeInTheDocument()
    expect(screen.getByTestId('operator-back')).toBeDisabled()
    finish(await createOperator.getMockImplementation()!(undefined, 'athens-regional', ['c1'], { endpoint: 'backend.example.com:4317' }))
    await screen.findByRole('dialog', { name: 'athens-regional created' })
  })
})

describe('RegionalOperatorsPage - Enter in the create form', () => {
  test('Enter in a label or a processor field creates nothing; Enter in the name field still moves to the next step', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByTestId('operator-open'))
    await user.type(screen.getByTestId('operator-name'), 'athens-regional{Enter}')
    expect(screen.getByTestId('operator-step-destination')).toBeInTheDocument()
    await chooseBackend(user)
    await user.click(screen.getByTestId('operator-next'))
    await user.click(within(screen.getByTestId('operator-advanced')).getByText('Advanced'))
    await user.click(screen.getByTestId('operator-label-tag-add'))
    await user.type(screen.getByTestId('operator-label-tag-key-0'), 'region{Enter}')
    await user.type(screen.getByTestId('operator-label-tag-value-0'), 'eu{Enter}')
    await user.click(screen.getByTestId('operator-processor-add-filter'))
    await user.keyboard('{Enter}')
    expect(createOperator).not.toHaveBeenCalled()
    expect(screen.getByTestId('operator-step-create')).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'New operator' })).toBeInTheDocument()
  })

  test('the Create button is still the way to create', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
  })
})

describe('RegionalOperatorsPage - a list that did not load, and an action that failed', () => {
  test('a first load that fails shows the error and not "No regional operators yet"', async () => {
    listOperators.mockRejectedValue(new ApiError(500, 'the server could not read operators'))
    renderPage()
    expect(await screen.findByText('the server could not read operators')).toBeInTheDocument()
    expect(screen.queryByText('No regional operators yet')).not.toBeInTheDocument()
    expect(screen.queryByTestId('operators-table')).not.toBeInTheDocument()
  })

  test('a failed action is dismissable and goes away with the next one that works', async () => {
    reinstallOperator.mockRejectedValueOnce(new ApiError(409, 'this operator is revoked'))
    listOperators.mockResolvedValue([op()])
    const user = userEvent.setup()
    renderPage()
    await rowAction(user, 'athens-regional', 'renew')
    await confirmRenew(user)
    const error = await screen.findByText('this operator is revoked')
    await user.click(within(error.closest('[role="alert"]') as HTMLElement).getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('this operator is revoked')).not.toBeInTheDocument()

    reinstallOperator.mockRejectedValueOnce(new ApiError(409, 'this operator is revoked'))
    await rowAction(user, 'athens-regional', 'renew')
    await confirmRenew(user)
    await screen.findByText('this operator is revoked')
    await rowAction(user, 'athens-regional', 'renew')
    await confirmRenew(user)
    await screen.findByRole('dialog', { name: 'Renew certificates for athens-regional' })
    expect(screen.queryByText('this operator is revoked')).not.toBeInTheDocument()
  })
})

describe('RegionalOperatorsPage - opening a FUSION page', () => {
  test('a double click mints one ticket and opens one tab', async () => {
    fusionStatus = { ...fusionRunning(), links: { prometheus: '/fusion/prometheus/', grafana: '/fusion/grafana/' } }
    let finish: (r: { path: string }) => void = () => undefined
    openFusionPage.mockImplementationOnce(() => new Promise<{ path: string }>((r) => { finish = r }))
    const tab = { location: { href: '' }, close: vi.fn(), opener: {} as unknown }
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    renderPage()
    const user = userEvent.setup()
    await user.dblClick(await screen.findByTestId('fusion-open-grafana'))
    expect(openSpy).toHaveBeenCalledTimes(1)
    expect(openFusionPage).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('fusion-open-prometheus')).toBeDisabled() // the other page waits for it too
    finish({ path: '/fusion/grafana/?ikhnos_ticket=t1' })
    await waitFor(() => expect(tab.location.href).toBe('/fusion/grafana/?ikhnos_ticket=t1'))
    await waitFor(() => expect(screen.getByTestId('fusion-open-grafana')).toBeEnabled())
    openSpy.mockRestore()
  })
})

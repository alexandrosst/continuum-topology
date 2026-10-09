import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { isReloadHeld } from '@/lib/staleBuild'
import PipelinePage from '@/pages/PipelinePage'
import type { CreatedOperator, FusionStatus, IssuedCertificate, OperatorHeartbeatEnabled, OperatorRemoval } from '@/lib/api'
import type { Agent, Cluster, OperatorDestinationEntry, RegionalOperator, TelemetryIntent } from '@/lib/types'

function renderPage() {
  return render(
    <MemoryRouter>
      <PipelinePage />
    </MemoryRouter>,
  )
}

const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', source: 'discovered', state: 'live', tier: 'cloud', orgId: 'o', ...over }) as Cluster
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', name: 'edge-agent', clusterId: 'c1', status: 'approved', ...over }) as Agent

// A page test: everything PipelinePage reads from the shared stores or the API is faked here,
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

/** A failing certificate renewal's own way in: the problem's "What to do", then the one button it offers. */
async function renewFromWhatToDo(user: User, rowId: string) {
  await user.click(await screen.findByTestId(`component-todo-${rowId}`))
  await user.click(await screen.findByTestId('what-to-do-action'))
  await confirmRenew(user)
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

describe('PipelinePage', () => {
  test('administrators only: a non-administrator sees the agents and local operators, no regional ones, and no operator is loaded', async () => {
    admin = false
    renderPage()
    expect(await screen.findByTestId('component-agent:a1')).toBeInTheDocument()
    expect(screen.getByText('Regional operators are only shown to organisation administrators.')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-open')).not.toBeInTheDocument()
    expect(screen.queryByTestId('hop-regional')).not.toBeInTheDocument()
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('while the operators are still being read there is a skeleton, not a misleading empty page', async () => {
    let resolve!: (ops: RegionalOperator[]) => void
    listOperators.mockImplementation(() => new Promise<RegionalOperator[]>((r) => { resolve = r }))
    renderPage()
    expect(screen.queryByText('Nothing is sending telemetry yet')).not.toBeInTheDocument()
    expect(screen.getAllByRole('status', { name: 'Loading' }).length).toBeGreaterThan(0)
    resolve([op()])
    expect(await screen.findByTestId('operator-athens-regional')).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: 'Loading' })).not.toBeInTheDocument()
  })

  test('with nothing at all the page says so and offers the wizard; the header still holds the one primary button', async () => {
    const user = userEvent.setup()
    topologyState = { agents: [], clusters: [] }
    renderPage()
    expect(await screen.findByText('Nothing is sending telemetry yet')).toBeInTheDocument()
    expect(screen.getByTestId('operator-open')).toHaveTextContent('New operator')
    expect(screen.getByTestId('telemetry-setup')).toHaveTextContent('Set up telemetry')
    const primaries = screen.getAllByRole('button').filter((b) => b.className.includes('bg-accent'))
    expect(primaries).toEqual([screen.getByTestId('telemetry-setup')])
    // The empty state offers the same action, quietly.
    await user.click(screen.getByTestId('telemetry-setup-empty'))
    expect(telemetryStart).toHaveBeenCalledWith()
  })

  test('a viewer who cannot edit has no Set up telemetry, and the menu item that changes what a collector sends says why it is dimmed', async () => {
    canEditFlag = false
    const user = userEvent.setup()
    topologyState = { agents: [ag({ diagnostics: { installedTelemetry: ['resourceUsage'], reportedAt: minutesAgo(1) } } as Partial<Agent>)], clusters: [cl()] }
    renderPage()
    await screen.findByTestId('component-local:a1')
    expect(screen.queryByTestId('telemetry-setup')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('component-menu-local:a1'))
    const item = screen.getByTestId('local-configure-edge-agent collector')
    expect(item).toBeDisabled()
    expect(within(item).getByText('Changing telemetry needs permission to edit')).toBeInTheDocument()
  })

  test('everything is one table: agents, local, regional and the central operator, each with its kind, and the filter chips show one kind with its count', async () => {
    const user = userEvent.setup()
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([op(), centralOp()])
    topologyState = { agents: [ag({ diagnostics: { installedTelemetry: ['resourceUsage'], reportedAt: minutesAgo(1) } } as Partial<Agent>)], clusters: [cl()] }
    renderPage()
    await screen.findByTestId('operator-athens-regional')
    for (const id of ['component-agent:a1', 'component-local:a1', 'operator-athens-regional', 'operator-Central (FUSION)']) expect(screen.getByTestId(id)).toBeInTheDocument()
    expect(screen.getByTestId('component-agent:a1')).toHaveAttribute('data-kind', 'agent')
    expect(screen.getByTestId('pipeline-filter-all')).toHaveTextContent('All 4')
    await user.click(screen.getByTestId('pipeline-filter-regional'))
    expect(screen.getByTestId('pipeline-filter-regional')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('operator-athens-regional')).toBeInTheDocument()
    expect(screen.queryByTestId('component-agent:a1')).not.toBeInTheDocument()
    // The path's own tile is the same filter.
    await user.click(screen.getByRole('button', { name: 'Discovery agents: show them in the table' }))
    expect(screen.getByTestId('component-agent:a1')).toBeInTheDocument()
    expect(screen.queryByTestId('operator-athens-regional')).not.toBeInTheDocument()
  })

  test('the data path is one connected strip: a node per hop with its count and worst state, a line between each, and FUSION last once it is on', async () => {
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([op({ health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }), centralOp({ health: { state: 'online', reporting: false, lastSeenAt: minutesAgo(1) } })])
    renderPage()
    const path = await screen.findByTestId('pipeline-path')
    await waitFor(() => expect(within(path).getByTestId('hop-fusion')).toHaveTextContent('On'))
    expect(within(path).getByTestId('hop-regional')).toHaveTextContent(/^1Regional operators\s*Healthy$/)
    expect(within(path).getByTestId('hop-central')).toHaveTextContent('Healthy')
    expect(within(path).getByTestId('hop-fusion')).toHaveTextContent('Healthy')
    expect(within(path).getAllByRole('listitem').map((i) => i.getAttribute('data-testid')).filter((t) => t?.startsWith('hop-'))).toEqual(['hop-agent', 'hop-local', 'hop-regional', 'hop-central', 'hop-fusion'])
    // A line between every two nodes, each with a freshness meter that says its value in words.
    expect(within(path).getAllByRole('meter').length).toBeGreaterThan(0)
    for (const m of within(path).getAllByRole('meter')) expect(m).toHaveAttribute('aria-valuetext', expect.stringMatching(/^Last data /))
  })

  test('with FUSION off there is no FUSION hop figure but "Off", and the central operator is not a row', async () => {
    renderPage()
    expect(await screen.findByTestId('hop-fusion')).toHaveTextContent('Off')
    expect(screen.queryByTestId('hop-central')?.textContent).toMatch(/^0Central operator/)
  })

  test('nothing is wrong: no "What needs attention"; something is wrong: one sentence per problem and a What to do that opens numbered steps with the exact command', async () => {
    const user = userEvent.setup()
    listOperators.mockResolvedValue([op({ health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } })])
    topologyState = { agents: [ag({ connected: true, lastHeartbeat: minutesAgo(0) })], clusters: [cl()] }
    const { unmount } = renderPage()
    await screen.findByTestId('operator-athens-regional')
    expect(screen.queryByTestId('pipeline-attention')).not.toBeInTheDocument()
    unmount()

    topologyState = { agents: [ag({ connected: false, lastHeartbeat: minutesAgo(130), namespace: 'continuum-system', releaseName: 'continuum-agent' })], clusters: [cl()] }
    renderPage()
    const strip = await screen.findByTestId('pipeline-attention')
    expect(strip).toHaveTextContent('edge-agent. Has not reported for 2 h 10 min')
    await user.click(within(strip).getByRole('button', { name: 'What to do about edge-agent' }))
    const dialog = screen.getByRole('dialog', { name: 'What to do about edge-agent' })
    expect(within(dialog).getByTestId('what-to-do-steps').children).toHaveLength(2)
    expect(within(dialog).getByTestId('what-to-do-command-1')).toHaveTextContent('kubectl get pods --namespace continuum-system')
    await user.click(within(dialog).getByTestId('what-to-do-action'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  test('the same sentence and the same What to do are on the row itself, whose state is a glyph, a word and a colour', async () => {
    topologyState = { agents: [ag({ connected: false, lastHeartbeat: minutesAgo(130) })], clusters: [cl()] }
    renderPage()
    const row = await screen.findByTestId('component-agent:a1')
    expect(row).toHaveAttribute('data-state', 'down')
    expect(within(row).getByText('Not working')).toBeInTheDocument()
    expect(within(row).getByTestId('component-reason-agent:a1')).toHaveTextContent('Has not reported for 2 h 10 min')
    expect(within(row).getByRole('button', { name: 'What to do about edge-agent' })).toBeInTheDocument()
  })

  test('every row has the same menu button in the same cell, named for it; Escape closes the menu', async () => {
    listOperators.mockResolvedValue([op({ id: 'op-a', name: 'one' }), centralOp()])
    topologyState = { agents: [ag({ diagnostics: { installedTelemetry: ['resourceUsage'], reportedAt: minutesAgo(1) } } as Partial<Agent>)], clusters: [cl()] }
    const user = userEvent.setup()
    renderPage()
    await screen.findByTestId('operator-one')
    for (const name of ['edge-agent', 'edge-agent collector', 'one', 'Central operator']) {
      const row = screen.getAllByRole('row').find((r) => within(r).queryByRole('button', { name: `Actions for ${name}` }))
      expect(row, name).toBeDefined()
      expect(row!.lastElementChild).toContainElement(screen.getByRole('button', { name: `Actions for ${name}` }))
    }
    const button = screen.getByRole('button', { name: 'Actions for one' })
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(within(screen.getByRole('menu', { name: 'Actions for one' })).getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Connect edge-1…', 'Reachable at…', 'Issued certificates', 'Renew certificates…', 'Enable health reporting', 'Disconnect…', 'Stop and remove…'])
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('component-menu-agent:a1'))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open in Agents'])
  })

  test('the central operator is managed by FUSION: Reachable at and Open FUSION, no disconnect, no remove, no certificates to renew', async () => {
    fusionStatus = fusionRunning()
    listOperators.mockResolvedValue([centralOp({ health: { state: 'online', reporting: false, lastSeenAt: minutesAgo(1) } })])
    const user = userEvent.setup()
    renderPage()
    const row = await screen.findByTestId('operator-Central (FUSION)')
    expect(within(row).getByText('Healthy')).toBeInTheDocument()
    await user.click(within(row).getByTestId('operator-menu-Central (FUSION)'))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Reachable at…', 'Open FUSION'])
  })

  test('a disconnected operator is Not working with its reason, is not counted, offers no What to do, and can only be removed', async () => {
    listOperators.mockResolvedValue([op({ status: 'revoked', reason: 'replaced' })])
    const user = userEvent.setup()
    renderPage()
    const row = await screen.findByTestId('operator-athens-regional')
    expect(row).toHaveAttribute('data-state', 'down')
    expect(within(row).getByTestId('component-reason-regional:op-1')).toHaveTextContent('Disconnected: replaced')
    expect(within(row).queryByRole('button', { name: /What to do/ })).not.toBeInTheDocument()
    expect(screen.queryByTestId('pipeline-attention')).not.toBeInTheDocument()
    expect(screen.getByTestId('hop-regional')).toHaveTextContent('0')
    await user.click(screen.getByTestId('operator-menu-athens-regional'))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Stop and remove…'])
  })

  test('the certificate column is a lifetime bar with its value in words; text appears only for a failing renewal or an ended certificate', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'fine-op', certState: 'ok', certs: { receiverNotAfter: daysFromNow(25) } }),
      op({ id: 'op-b', name: 'soon-op', certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(12) } }),
      op({ id: 'op-c', name: 'late-op', certState: 'expired', certs: { receiverNotAfter: daysFromNow(-3) } }),
      op({ id: 'op-d', name: 'old-op' }),
      op({ id: 'op-e', name: 'long-op', certState: 'ok', certs: { receiverNotAfter: daysFromNow(300) } }),
    ])
    renderPage()
    const cell = (name: string) => within(screen.getByTestId(`operator-${name}`)).getAllByRole('cell')[3]
    await screen.findByTestId('operator-fine-op')
    expect(cell('fine-op')).not.toHaveTextContent(/Renewing|next|failing|Expired/)
    expect(within(cell('fine-op')).getByRole('meter', { name: 'Certificate lifetime' })).toHaveAttribute('aria-valuetext', 'expires in 25 days')
    expect(within(cell('fine-op')).getByRole('meter')).toHaveAttribute('aria-valuenow', '83')
    expect(within(cell('soon-op')).getByRole('meter')).toHaveAttribute('aria-valuetext', 'expires in 12 days')
    expect(within(cell('late-op')).getByRole('meter')).toHaveAttribute('aria-valuetext', 'expired 3 days ago')
    expect(cell('long-op')).toHaveTextContent('legacy')
    expect(cell('fine-op')).not.toHaveTextContent('legacy')
    expect(cell('soon-op')).toHaveTextContent('Renewal failing')
    expect(cell('soon-op').querySelector('.text-warn')).not.toBeNull()
    expect(cell('late-op')).toHaveTextContent('Expired')
    expect(cell('late-op').querySelector('.text-bad')).not.toBeNull()
    expect(cell('old-op')).toHaveTextContent('—')
    expect(cell('old-op').querySelector('[role=meter]')).toBeNull()
    expect(screen.getByTestId('component-reason-regional:op-b')).toHaveTextContent('Certificate renewal is failing')
  })

  test('where an operator sends is a column: FUSION by that name, another operator by its name; its address has a copy button, and a missing one is something to do', async () => {
    const user = userEvent.setup()
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'athens', destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-central' }, addressState: 'set', address: 'otlp.eu.example.com:4317', reachableFromOtherClusters: true }),
      op({ id: 'op-b', name: 'beta', destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-athens' }, exposure: 'loadbalancer', addressState: 'pending' }),
      op({ id: 'op-athens', name: 'the-hub' }),
    ])
    renderPage()
    const row = await screen.findByTestId('operator-athens')
    expect(within(row).getAllByRole('cell')[4]).toHaveTextContent(/FUSION$/)
    expect(within(screen.getByTestId('operator-beta')).getAllByRole('cell')[4]).toHaveTextContent('the-hub')
    expect(within(row).getByRole('button', { name: 'Copy the address of athens' })).toBeInTheDocument()
    // A Service with no recorded address is a problem with one thing to do, and it opens the dialog that records it.
    const strip = screen.getByTestId('pipeline-attention')
    expect(strip).toHaveTextContent('beta. Clusters elsewhere cannot send to it yet')
    await user.click(within(strip).getByRole('button', { name: 'What to do about beta' }))
    await user.click(screen.getByRole('button', { name: 'Record the address' }))
    expect(screen.getByTestId('operator-address-input')).toBeInTheDocument()
  })

  test('Connect <cluster> is a menu item for each source cluster with an agent, starting the wizard with the operator chosen', async () => {
    topologyState = { agents: [ag(), ag({ id: 'a2', clusterId: 'c2', name: 'agent-2' }), ag({ id: 'a3', clusterId: 'c3', status: 'pending' })], clusters: [cl(), cl({ id: 'c2', name: 'edge-2' }), cl({ id: 'c3', name: 'edge-3' })] }
    listOperators.mockResolvedValue([op({ sourceClusterIds: ['c1', 'c2', 'c3'] })])
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByTestId('operator-menu-athens-regional'))
    expect(screen.getAllByRole('menuitem').slice(0, 2).map((i) => i.textContent)).toEqual(['Connect edge-1…', 'Connect edge-2…'])
    await user.click(screen.getByRole('menuitem', { name: 'Connect edge-2…' }))
    expect(telemetryStart).toHaveBeenCalledWith('a2', undefined, 'op-1')
  })

  test('an operator reads as Healthy only when it reports being online; one that does not report health is Unknown, in words', async () => {
    listOperators.mockResolvedValue([
      op({ id: 'op-a', name: 'online-op', health: { state: 'online', lastSeenAt: minutesAgo(1), reporting: true } }),
      op({ id: 'op-b', name: 'offline-op', health: { state: 'offline', lastSeenAt: minutesAgo(20), reporting: true } }),
      op({ id: 'op-c', name: 'quiet-op' }),
    ])
    renderPage()
    await screen.findByTestId('operator-online-op')
    expect(screen.getByTestId('operator-online-op')).toHaveAttribute('data-state', 'healthy')
    expect(screen.getByTestId('operator-offline-op')).toHaveAttribute('data-state', 'down')
    expect(screen.getByTestId('component-reason-regional:op-b')).toHaveTextContent('Offline, last heard from 20 min ago')
    expect(screen.getByTestId('operator-quiet-op')).toHaveAttribute('data-state', 'unknown')
    expect(screen.getByTestId('component-reason-regional:op-c')).toHaveTextContent('Health reporting is off')
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

})

describe('PipelinePage - the destination is picked from a list', () => {
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

describe('PipelinePage - certificates', () => {
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

})

describe('PipelinePage - the table', () => {
  test('Renew certificates asks the server to install again and shows what it returns on the same screen as a new operator', async () => {
    listOperators.mockResolvedValue([op({ certState: 'renewal-failing', certs: { receiverNotAfter: daysFromNow(10) } })])
    const user = userEvent.setup()
    renderPage()
    await renewFromWhatToDo(user, 'regional:op-1')
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
    await renewFromWhatToDo(user, 'regional:op-1')
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

  test('a list that is read again shows what changed without a reload: a new operator, and a health that flipped', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      listOperators.mockResolvedValue([op({ id: 'op-a', name: 'first-op' })])
      renderPage()
      expect(await screen.findByTestId('operator-first-op')).toBeInTheDocument()
      listOperators.mockResolvedValue([op({ id: 'op-a', name: 'first-op', health: { state: 'online', lastSeenAt: minutesAgo(0), reporting: true } }), op({ id: 'op-b', name: 'second-op' })])
      await vi.advanceTimersByTimeAsync(13_000)
      expect(await screen.findByTestId('operator-second-op')).toBeInTheDocument()
      expect(screen.getByTestId('operator-first-op')).toHaveAttribute('data-state', 'healthy')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('PipelinePage - revoking and deleting', () => {
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

describe('PipelinePage - local operators', () => {
  const installedAgent = (over: Record<string, unknown> = {}) =>
    ag({ name: 'edge-agent', diagnostics: { installedTelemetry: ['resourceUsage', 'systemLogs'], reportedAt: '2026-01-02T00:00:00Z', ...over } } as Partial<Agent>)
  const intent = (over: Partial<TelemetryIntent> = {}): TelemetryIntent => ({
    id: 'ti-1', agentId: 'a1', name: 'to eu', status: 'active', namespaces: [], exclude: [],
    signals: [{ id: 'resourceUsage', source: 'builtin' }, { id: 'systemLogs', source: 'builtin' }],
    destination: { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' },
    createdAt: '2026-01-01T00:00:00Z', createdBy: 'me', ...over,
  })


  test('a local operator is an agent with telemetry running: its collector row says where it sends, and its agent being offline makes it Unknown, with the reason', async () => {
    listTelemetryIntents.mockResolvedValue([intent()])
    listOperators.mockResolvedValue([op({ id: 'op-eu', name: 'EU hub' })])
    topologyState = { agents: [installedAgent({ exportHealth: { podsReached: 1, podsFailed: 0, routes: [{ exporter: 'otlp', signal: 'metrics', state: 'exporting', sent: 5, failed: 0, lastSentAt: minutesAgo(0) }, { exporter: 'otlp', signal: 'logs', state: 'exporting', sent: 5, failed: 0, lastSentAt: minutesAgo(0) }] } })], clusters: [cl()] }
    const { unmount } = renderPage()
    const row = await screen.findByTestId('component-local:a1')
    await waitFor(() => expect(within(row).getAllByRole('cell')[4]).toHaveTextContent('EU hub'))
    expect(row).toHaveAttribute('data-kind', 'local')
    expect(row).toHaveAttribute('data-state', 'healthy')
    expect(within(row).getAllByRole('cell')[5]).toHaveTextContent(/s ago$/)
    unmount()

    topologyState = { agents: [{ ...installedAgent(), connected: false, lastHeartbeat: minutesAgo(125) } as Agent], clusters: [cl()] }
    renderPage()
    const offline = await screen.findByTestId('component-local:a1')
    expect(offline).toHaveAttribute('data-state', 'unknown')
    expect(within(offline).getByTestId('component-reason-local:a1')).toHaveTextContent('Its agent has not reported for 2 h 5 min')
    expect(within(offline).queryByRole('button', { name: /What to do/ })).not.toBeInTheDocument()
  })

  test('a collector whose route is failing is Not working, and its What to do offers the wizard to change what it sends', async () => {
    const user = userEvent.setup()
    listTelemetryIntents.mockResolvedValue([intent({ destination: { kind: 'external', endpoint: 'otel.example.com:4317' } })])
    topologyState = { agents: [installedAgent({ exportHealth: { podsReached: 1, podsFailed: 0, routes: [{ exporter: 'otlp', signal: 'metrics', state: 'failing', sent: 0, failed: 3 }] } })], clusters: [cl()] }
    renderPage()
    const row = await screen.findByTestId('component-local:a1')
    expect(row).toHaveAttribute('data-state', 'down')
    expect(within(row).getByTestId('component-reason-local:a1')).toHaveTextContent('Sending to otel.example.com:4317 is failing.')
    await user.click(within(row).getByRole('button', { name: 'What to do about edge-agent collector' }))
    await user.click(screen.getByRole('button', { name: 'Change what it sends' }))
    expect(telemetryStart).toHaveBeenCalledWith('a1')
  })

  test('someone who is not an administrator gets the destination from the read model, and no operator is loaded', async () => {
    admin = false
    listTelemetryIntents.mockResolvedValue([intent()])
    listOperatorDestinations.mockResolvedValue([{ id: 'op-eu', name: 'EU hub', kind: 'regional', status: 'active', health: { state: 'online', lastSeenAt: minutesAgo(1) }, addressState: 'none', reachableFromOtherClusters: false, recommended: false, usedBy: 0 }])
    topologyState = { agents: [installedAgent()], clusters: [cl()] }
    renderPage()
    const row = await screen.findByTestId('component-local:a1')
    await waitFor(() => expect(within(row).getAllByRole('cell')[4]).toHaveTextContent('EU hub'))
    expect(listOperators).not.toHaveBeenCalled()
  })

  test('an agent asked for telemetry that has not reported it yet is Unknown for the first hour, and Needs attention after that', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ createdAt: minutesAgo(5) })])
    topologyState = { agents: [ag()], clusters: [cl()] }
    const { unmount } = renderPage()
    expect(await screen.findByTestId('component-local:a1')).toHaveAttribute('data-state', 'unknown')
    expect(screen.getByTestId('component-reason-local:a1')).toHaveTextContent('Asked to run telemetry. Nothing is reported running yet.')
    unmount()

    listTelemetryIntents.mockResolvedValue([intent({ createdAt: minutesAgo(180) })])
    renderPage()
    await waitFor(() => expect(screen.getByTestId('component-local:a1')).toHaveAttribute('data-state', 'attention'))
    expect(screen.getByTestId('component-reason-local:a1')).toHaveTextContent('the command may not have been run')
  })

  test('a revoked request is not what was asked, and an agent with only that has no collector row', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ status: 'revoked' })])
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderPage()
    await screen.findByTestId('component-agent:a1')
    expect(screen.queryByTestId('component-local:a1')).not.toBeInTheDocument()
  })
})

describe('PipelinePage - health reporting', () => {
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

describe('PipelinePage - creating with and without health reporting', () => {
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

describe('PipelinePage - explanatory notes', () => {
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

describe('PipelinePage - where other clusters reach an operator', () => {
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
    await screen.findByTestId('operator-Central (FUSION)')
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

/** The dialog's own backdrop: the element a click outside the box lands on. */
const backdropOf = (dialog: HTMLElement) => dialog.parentElement!

describe('PipelinePage - renewing certificates asks first', () => {
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
    await rowAction(user, 'one', 'renew')
    await confirmRenew(user)
    await waitFor(() => expect(reinstallOperator).toHaveBeenCalledTimes(1))
    await user.click(screen.getByTestId('operator-menu-two'))
    const item = await screen.findByTestId('operator-renew-two')
    expect(item).toBeDisabled()
    expect(item).toHaveAttribute('title', 'Another renewal is in progress')
    expect(within(item).getByText('Another renewal is in progress')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await user.click(screen.getByTestId('attention-todo-regional:op-b'))
    expect(screen.getByTestId('what-to-do-action')).toBeDisabled()
    await user.keyboard('{Escape}')
    finish({ operator: { id: 'op-a', name: 'one', status: 'active', sourceClusterIds: [], receiverAuth: 'mtls' } as CreatedOperator['operator'], install: 'X', reminders: [] })
    await screen.findByRole('dialog', { name: 'Renew certificates for one' })
  })
})

describe('PipelinePage - a secret shown once cannot be lost to a stray click', () => {
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

describe('PipelinePage - Enter in the create form', () => {
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

describe('PipelinePage - a list that did not load, and an action that failed', () => {
  test('a first load that fails shows the error and not "Nothing is sending telemetry yet"', async () => {
    listOperators.mockRejectedValue(new ApiError(500, 'the server could not read operators'))
    renderPage()
    expect(await screen.findByText('the server could not read operators')).toBeInTheDocument()
    expect(screen.queryByText('Nothing is sending telemetry yet')).not.toBeInTheDocument()
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

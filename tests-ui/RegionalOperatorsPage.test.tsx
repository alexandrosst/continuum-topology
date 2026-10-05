import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import RegionalOperatorsPage from '@/pages/RegionalOperatorsPage'
import type { OperatorHeartbeatEnabled } from '@/lib/api'
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
const createOperator = vi.fn(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }, _options?: { heartbeat?: boolean }) => ({
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
const revokeOperator = vi.fn(async () => {})
const deleteOperator = vi.fn(async () => {})

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
      revokeOperator: (...a: Parameters<typeof revokeOperator>) => revokeOperator(...a),
      deleteOperator: (...a: Parameters<typeof deleteOperator>) => deleteOperator(...a),
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
  deleteOperator.mockClear()
  telemetryStart.mockClear()
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
      { heartbeat: true },
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
    expect(createOperator.mock.calls[0][4]).toEqual({ heartbeat: true })
  })

  test('unchecking it sends heartbeat: false and the created screen says the operator never contacts the server', async () => {
    const user = userEvent.setup()
    renderPage()
    await fillCreateForm(user)
    await user.click(screen.getByTestId('operator-heartbeat'))
    await user.click(screen.getByTestId('operator-create'))
    await waitFor(() => expect(createOperator).toHaveBeenCalledTimes(1))
    expect(createOperator.mock.calls[0][4]).toEqual({ heartbeat: false })
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
    expect(mtls).toHaveTextContent('any certificate this server issued is accepted')
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
    expect(text).toContain('any client certificate this server')
    expect(text).toContain('older operator the bearer token')
    expect(text).not.toContain('a receiver bearer token (minted')
  })
})

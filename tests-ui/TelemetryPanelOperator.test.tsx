import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import { ApiError } from '@/lib/api'
import { DEFAULT_SETTINGS } from '@/lib/history'
import type { RegionalOperator, TelemetryIntent } from '@/lib/types'

// The regional-operator destination in the Telemetry panel: an operator's receiver wants a client
// certificate only the server can issue, so the panel shows no ready command - only an explicit "Generate"
// that creates (or updates) the agent's telemetry intent and asks the server for the commands. Every
// assertion is about what the panel sends and shows, with the API mocked; nothing here reaches a server.

let role: 'viewer' | 'editor' | 'admin' | 'owner' | undefined
const listOperators = vi.fn()
const listTelemetryIntents = vi.fn()
const createTelemetryIntent = vi.fn()
const updateTelemetryIntentScope = vi.fn()
const updateTelemetryIntentDestination = vi.fn()
const getTelemetryIntentCommand = vi.fn()

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings: DEFAULT_SETTINGS, loaded: true, error: undefined, save: vi.fn(async () => true) }),
}))
const CONN = { url: 'https://example.test', org: 'org-1' }
// The store's conn is one stable function; a fresh one per render would make every polled list read again on every render.
const connFn = () => CONN
const reloadInfo = vi.fn(async () => undefined)
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string; conn: () => typeof CONN; reloadInfo: () => Promise<void> }) => unknown) => (selector ? selector({ role, conn: connFn, reloadInfo }) : { role, conn: connFn, reloadInfo }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listOperators: (...a: unknown[]) => listOperators(...a),
      // A server without FUSION, and no read model: the destination step lists only what each test puts in listOperators.
      getFusion: async () => ({ available: false, reason: 'not-configured', state: 'off' }),
      listOperatorDestinations: async () => [],
      listTelemetryIntents: (...a: unknown[]) => listTelemetryIntents(...a),
      createTelemetryIntent: (...a: unknown[]) => createTelemetryIntent(...a),
      updateTelemetryIntentScope: (...a: unknown[]) => updateTelemetryIntentScope(...a),
      updateTelemetryIntentDestination: (...a: unknown[]) => updateTelemetryIntentDestination(...a),
      getTelemetryIntentCommand: (...a: unknown[]) => getTelemetryIntentCommand(...a),
    },
  }
})

const operator: RegionalOperator = {
  id: 'op-eu',
  orgId: 'org-1',
  name: 'EU regional operator',
  status: 'active',
  sourceClusterIds: [],
  destination: { kind: 'external', endpoint: 'otel-gateway.example.com:4317' },
  createdAt: '2026-01-01T00:00:00Z',
  createdBy: 'alice',
}
const intent = (overrides: Partial<TelemetryIntent> = {}): TelemetryIntent => ({
  id: 'ti-1',
  agentId: 'agent-1',
  name: 'Telemetry to EU regional operator',
  status: 'active',
  namespaces: [],
  exclude: [],
  signals: [{ id: 'resourceUsage', source: 'builtin' }],
  destination: { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' },
  createdAt: '2026-01-01T00:00:00Z',
  createdBy: 'alice',
  ...overrides,
})
const SECRET = 'kubectl create secret generic op-eu-export-mtls --namespace continuum-system \\\n  --from-literal=tls.crt="CERT" \\\n  --from-literal=tls.key="KEY" \\\n  --from-literal=ca.crt="CA"'
const FRAGMENT = '--set telemetry.resource.orgId=org-1 --set telemetry.resource.clusterId=cl-1 --set telemetry.resource.intentId=ti-1 --set telemetry.export.otlp.endpoint=op-eu.continuum-system.svc:4317 --set telemetry.export.otlp.tls.mtls.enabled=true --set telemetry.export.otlp.tls.mtls.secretName=op-eu-export-mtls'

beforeEach(() => {
  role = 'admin'
  for (const m of [listOperators, listTelemetryIntents, createTelemetryIntent, updateTelemetryIntentScope, updateTelemetryIntentDestination, getTelemetryIntentCommand]) m.mockReset()
  listOperators.mockResolvedValue([operator])
  listTelemetryIntents.mockResolvedValue([])
  createTelemetryIntent.mockResolvedValue(intent())
  updateTelemetryIntentScope.mockResolvedValue(intent())
  updateTelemetryIntentDestination.mockResolvedValue(intent())
  getTelemetryIntentCommand.mockResolvedValue({ installFragment: FRAGMENT, secretCommands: [SECRET] })
})

const tree = () => (
  <MemoryRouter>
    <TelemetryPanel standalone testIdPrefix="tp" agentId="agent-1" clusterId="cl-1" />
  </MemoryRouter>
)

/** Resource usage on, and the one operator of the organisation picked (the lone destination that fits is
 *  picked for the person on arrival). */
async function pickOperator(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-mode-guided'))
  await user.click(screen.getByTestId('tp-resourceUsage'))
  await user.click(screen.getByTestId('tp-guided-continue')) // Collect -> Process
  await user.click(screen.getByTestId('tp-guided-continue')) // Process -> Destination
  await screen.findByTestId('tp-guided-destination-summary')
  expect(screen.getByTestId('tp-guided-destination-name')).toHaveTextContent('EU regional operator')
}

/** On to the last step: Continue from Destination to Review, then "Create the command" - the command, or the
 *  button that generates it, is only on that step. */
async function toRun(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-create-command'))
}
async function backTo(user: ReturnType<typeof userEvent.setup>, times: number) {
  for (let i = 0; i < times; i++) await user.click(screen.getByTestId('tp-guided-back'))
}

const generateButton = () => screen.findByTestId('tp-operator-generate')
const noApiCalls = () => {
  // The one read that seeds the draft from the agent's active request is the only call allowed before the click: nothing is written or generated.
  expect(listTelemetryIntents.mock.calls.length).toBeLessThanOrEqual(1)
  expect(createTelemetryIntent).not.toHaveBeenCalled()
  expect(updateTelemetryIntentScope).not.toHaveBeenCalled()
  expect(updateTelemetryIntentDestination).not.toHaveBeenCalled()
  expect(getTelemetryIntentCommand).not.toHaveBeenCalled()
}

describe('TelemetryPanel with a regional operator destination', () => {
  test('an administrator gets a labelled action, no ready command, and nothing is sent until the click', async () => {
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    // Editing the draft after picking does not generate anything either (each generation reissues a certificate).
    await user.type(screen.getByTestId('tp-export-auth-secret'), 'tok')
    expect(screen.queryByTestId('tp-operator')).not.toBeInTheDocument()
    await toRun(user)
    const button = await generateButton()
    expect(button).toHaveTextContent('Generate commands for EU regional operator')
    expect(screen.getByTestId('tp-operator')).toHaveTextContent('fresh client certificate')
    expect(screen.getByTestId('tp-operator')).toHaveTextContent('audit log')
    expect(screen.getByTestId('tp-operator')).toHaveTextContent('shown once')
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tp-operator-command')).not.toBeInTheDocument()
    noApiCalls()
  })

  test('with no intent yet: creates one for this agent, then shows one block - secret first, then the upgrade with the server fragment', async () => {
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    const cmd = (await screen.findByTestId('tp-operator-command')).textContent ?? ''

    expect(listTelemetryIntents).toHaveBeenCalledWith(CONN, 'agent-1')
    expect(createTelemetryIntent).toHaveBeenCalledTimes(1)
    expect(createTelemetryIntent).toHaveBeenCalledWith(
      CONN,
      'agent-1',
      'Telemetry to EU regional operator',
      [],
      [],
      [{ id: 'resourceUsage', source: 'builtin' }],
      { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' },
    )
    expect(updateTelemetryIntentScope).not.toHaveBeenCalled()
    expect(getTelemetryIntentCommand).toHaveBeenCalledWith(CONN, 'ti-1')

    expect(cmd.startsWith('kubectl create secret generic op-eu-export-mtls')).toBe(true)
    expect(cmd.indexOf('kubectl create secret')).toBeLessThan(cmd.indexOf('helm upgrade'))
    expect(cmd).toContain('&&')
    expect(cmd.trimEnd().endsWith(FRAGMENT)).toBe(true)
    expect(cmd).toContain('--set telemetry.resourceUsage.metrics.enabled=true')
    // The endpoint is the server's, named once (the client's placeholder is left out). The connection is stated as gRPC and verified, so
    // an http or skip-verify an earlier destination had cannot linger; the client-certificate lines are the server fragment's alone.
    expect(cmd.split('telemetry.export.otlp.endpoint=').length - 1).toBe(1)
    expect(cmd).toContain('--set telemetry.export.otlp.endpoint=op-eu.continuum-system.svc:4317')
    expect(cmd).toContain('telemetry.export.otlp.protocol=grpc')
    expect(cmd).toContain('tls.insecure=false')
    expect(cmd).not.toContain('tls.mtls.enabled=false')
    expect(screen.queryByTestId('tp-operator-endpoint-note')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tp-operator-stale')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy the command' })).toBeEnabled()
  })

  test('an operator reached at an address the server knows: the command names that endpoint once and the note says so, without a warning', async () => {
    const real = 'otlp.eu.example.com:4317'
    getTelemetryIntentCommand.mockResolvedValue({
      installFragment: `${FRAGMENT.replace('op-eu.continuum-system.svc:4317', real)} --set telemetry.export.otlp.tls.serverName=op-eu.continuum-system.svc`,
      secretCommands: [SECRET],
    })
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    const cmd = (await screen.findByTestId('tp-operator-command')).textContent ?? ''
    expect(cmd.split('telemetry.export.otlp.endpoint=').length - 1).toBe(1)
    expect(cmd).toContain(`telemetry.export.otlp.endpoint=${real}`)
    expect(cmd).not.toContain('op-eu.continuum-system.svc:4317')
    expect(cmd).toContain('telemetry.export.otlp.tls.serverName=op-eu.continuum-system.svc')
    const note = screen.getByTestId('tp-operator-endpoint-note')
    expect(note).toHaveTextContent(`These commands send to ${real}`)
    expect(note).not.toHaveClass('text-warn')
    expect(note).not.toHaveTextContent(/not op-eu/)
  })

  test('with an intent already active for this agent: updates its scope and destination instead of creating a second', async () => {
    listTelemetryIntents.mockResolvedValue([
      intent({ id: 'ti-old', status: 'revoked' }),
      intent({ id: 'ti-9', signals: [{ id: 'traces', source: 'builtin' }], destination: { kind: 'external', endpoint: 'otel.example.com:4317' } }),
    ])
    const user = userEvent.setup()
    render(tree())
    await waitFor(() => expect(listTelemetryIntents).toHaveBeenCalled())
    await user.click(screen.getByTestId('tp-mode-guided'))
    await user.click(screen.getByTestId('tp-resourceUsage'))
    await user.click(screen.getByTestId('tp-guided-continue')) // Collect -> Process
    await user.click(screen.getByTestId('tp-guided-continue')) // Process -> Destination
    // The destination the active request already names is where the step opens, not an empty one; the operator is a change from it.
    expect(await screen.findByTestId('tp-guided-destination-endpoint')).toHaveValue('otel.example.com:4317')
    await user.click(screen.getByTestId('tp-guided-destination-change'))
    await user.click(await screen.findByTestId('tp-guided-destination-operator-op-eu'))
    await toRun(user)
    await user.click(await generateButton())
    await screen.findByTestId('tp-operator-command')

    expect(createTelemetryIntent).not.toHaveBeenCalled()
    expect(updateTelemetryIntentDestination).toHaveBeenCalledWith(CONN, 'ti-9', { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' })
    expect(updateTelemetryIntentScope).toHaveBeenCalledWith(CONN, 'ti-9', [], [], [{ id: 'resourceUsage', source: 'builtin' }])
    expect(getTelemetryIntentCommand).toHaveBeenCalledWith(CONN, 'ti-9')
  })

  test('an intent already on this operator with the same scope is left alone, only the command is fetched', async () => {
    listTelemetryIntents.mockResolvedValue([intent()])
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    await screen.findByTestId('tp-operator-command')
    expect(createTelemetryIntent).not.toHaveBeenCalled()
    expect(updateTelemetryIntentScope).not.toHaveBeenCalled()
    expect(updateTelemetryIntentDestination).not.toHaveBeenCalled()
    expect(getTelemetryIntentCommand).toHaveBeenCalledTimes(1)
  })

  test('changing the draft afterwards dims the block as out of date and disables copy; generating again makes it fresh', async () => {
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    await screen.findByTestId('tp-operator-command')
    expect(screen.getByTestId('tp-operator-fresh')).toBeInTheDocument()

    await backTo(user, 2)
    await user.type(screen.getByTestId('tp-export-auth-secret'), 'receiver-token')
    await toRun(user)
    expect(screen.getByTestId('tp-operator-stale')).toHaveTextContent('Out of date - generate again')
    expect(screen.getByTestId('tp-operator-result')).toHaveAttribute('data-stale', 'true')
    expect(screen.getByRole('button', { name: 'Copy the command' })).toBeDisabled()
    expect(screen.getByTestId('tp-operator-command')).toBeInTheDocument()
    expect(getTelemetryIntentCommand).toHaveBeenCalledTimes(1)

    await user.click(screen.getByTestId('tp-operator-generate'))
    await waitFor(() => expect(screen.queryByTestId('tp-operator-stale')).not.toBeInTheDocument())
    expect(getTelemetryIntentCommand).toHaveBeenCalledTimes(2)
    const cmd = screen.getByTestId('tp-operator-command').textContent ?? ''
    expect(cmd).toContain('kubectl create secret generic receiver-token')
    expect(cmd).toContain('auth.secretName=receiver-token')
    expect(screen.getByRole('button', { name: 'Copy the command' })).toBeEnabled()
  })

  test('a certificate-only operator: no receiver-token field or hint, and the command carries no credential Secret even if one was typed earlier', async () => {
    listOperators.mockResolvedValue([{ ...operator, receiverAuth: 'mtls' }])
    getTelemetryIntentCommand.mockResolvedValue({ installFragment: FRAGMENT, secretCommands: [SECRET], receiverAuth: 'mtls' })
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    expect(screen.queryByTestId('tp-export-auth-secret')).not.toBeInTheDocument()
    expect(await screen.findByTestId('tp-operator-mtls-note')).toHaveTextContent('client certificate alone')
    expect(screen.queryByTestId('tp-credential-hint')).not.toBeInTheDocument()
    const cmd = (await screen.findByTestId('tp-operator-command')).textContent ?? ''
    expect(cmd.startsWith('kubectl create secret generic op-eu-export-mtls')).toBe(true)
    // No credential Secret is named, and the name is stated empty so a receiver token an earlier command set stops being sent.
    expect(cmd).toMatch(/auth\.secretName= /)
    expect(cmd).not.toContain('auth.secretKey')
    expect(cmd).not.toContain('TELEMETRY_EXPORT_TOKEN')
  })

  test('someone below administrator sees a short note and no command, and the API is never called', async () => {
    const user = userEvent.setup()
    const { rerender } = render(tree())
    await pickOperator(user)
    role = 'editor' // signed in as an editor from here on
    rerender(tree())
    await toRun(user)
    expect(screen.getByTestId('tp-operator-admin-note')).toHaveTextContent('An administrator has to generate the commands')
    expect(screen.queryByTestId('tp-operator-generate')).not.toBeInTheDocument()
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tp-operator-command')).not.toBeInTheDocument()
    noApiCalls()
  })

  test.each([
    ['a modality mismatch', 'create', new ApiError(400, '"traces" is a traces signal, but the target operator only accepts [metrics]'), '"traces" is a traces signal, but the target operator only accepts [metrics]'],
    ['a conflict', 'command', new ApiError(409, 'agent "a" already has an active telemetry intent (ti-3); update it instead of creating a second one'), 'already has an active telemetry intent (ti-3)'],
    ['a refusal of the role', 'command', new ApiError(403, 'requires the admin role'), 'requires the admin role'],
  ])('%s from the server is shown verbatim, with no command', async (_name, where, err, verbatim) => {
    if (where === 'create') createTelemetryIntent.mockRejectedValue(err)
    else getTelemetryIntentCommand.mockRejectedValue(err)
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    const alert = await screen.findByTestId('tp-operator-error')
    expect(alert).toHaveTextContent(verbatim)
    expect(alert).toHaveAttribute('role', 'alert')
    expect(screen.queryByTestId('tp-operator-command')).not.toBeInTheDocument()
    expect(screen.getByTestId('tp-operator-generate')).toBeEnabled()
  })

  test('switching to a custom endpoint drops the operator flow: the ordinary command comes back', async () => {
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await user.click(screen.getByTestId('tp-guided-destination-change'))
    await user.click(screen.getByTestId('tp-guided-destination-custom'))
    await user.type(screen.getByTestId('tp-guided-destination-custom-endpoint'), 'otel.example.com:4317')
    await user.click(screen.getByTestId('tp-guided-destination-custom-use'))
    await toRun(user)
    expect(screen.queryByTestId('tp-operator')).not.toBeInTheDocument()
    expect(screen.getByTestId('helm-command').textContent).toContain('otel.example.com:4317')
    noApiCalls()
  })
})

describe('TelemetryPanel with signal types going to different destinations', () => {
  const ROUTE_FRAGMENT =
    '--set telemetry.resource.orgId=org-1 --set telemetry.resource.clusterId=cl-1 --set telemetry.resource.intentId=ti-1' +
    ' --set telemetry.export.routes.logs.endpoint=op-eu.continuum-system.svc:4317 --set telemetry.export.routes.logs.tls.mtls.enabled=true --set telemetry.export.routes.logs.tls.mtls.secretName=op-eu-export-mtls' +
    ' --set telemetry.export.routes.metrics.endpoint=op-eu.continuum-system.svc:4317 --set telemetry.export.routes.metrics.tls.mtls.enabled=true --set telemetry.export.routes.metrics.tls.mtls.secretName=op-eu-export-mtls'
  const OP_DEST = { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' }

  /** Metrics and logs on, split into one destination each: the lone operator is picked for both on arrival. */
  async function splitToRun(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByTestId('tp-mode-guided'))
    await user.click(screen.getByTestId('tp-resourceUsage'))
    await user.click(screen.getByTestId('tp-systemLogs'))
    await user.click(screen.getByTestId('tp-guided-continue')) // Collect -> Process
    await user.click(screen.getByTestId('tp-guided-continue')) // Process -> Destination
    await user.click(screen.getByTestId('tp-guided-mode-split'))
    await waitFor(() => expect(screen.getByTestId('tp-lane-metrics-guided-destination-name')).toHaveTextContent('EU regional operator'))
    await waitFor(() => expect(screen.getByTestId('tp-lane-logs-guided-destination-name')).toHaveTextContent('EU regional operator'))
  }

  beforeEach(() => {
    getTelemetryIntentCommand.mockResolvedValue({ installFragment: ROUTE_FRAGMENT, secretCommands: [SECRET], operators: { 'op-eu': 'mtls' } })
  })

  test('two signal types to one operator: one intent with a route each, one certificate, and the routes in the command', async () => {
    const user = userEvent.setup()
    render(tree())
    await splitToRun(user)
    await toRun(user)
    expect(await screen.findByTestId('tp-operator-generate')).toHaveTextContent('Generate commands for EU regional operator')
    noApiCalls()
    await user.click(screen.getByTestId('tp-operator-generate'))
    const cmd = (await screen.findByTestId('tp-operator-command')).textContent ?? ''

    expect(createTelemetryIntent).toHaveBeenCalledTimes(1)
    expect(createTelemetryIntent).toHaveBeenCalledWith(
      CONN,
      'agent-1',
      'Telemetry to EU regional operator',
      [],
      [],
      [{ id: 'resourceUsage', source: 'builtin' }, { id: 'systemLogs', source: 'builtin' }],
      OP_DEST,
      { metrics: OP_DEST, logs: OP_DEST },
    )
    // One certificate Secret, however many signal types go to the operator; then the upgrade with the server's routes last.
    expect(cmd.match(/kubectl create secret generic op-eu-export-mtls/g)).toHaveLength(1)
    expect(cmd.startsWith('kubectl create secret generic op-eu-export-mtls')).toBe(true)
    expect(cmd.trimEnd().endsWith(ROUTE_FRAGMENT)).toBe(true)
    expect(cmd).toContain('--set-string telemetry.export.routes.metrics.endpoint=op-eu.continuum-system.svc:4317')
    expect(cmd).toContain('--set-string telemetry.export.routes.logs.endpoint=op-eu.continuum-system.svc:4317')
    // The default is left alone, and an operator route is not stated off by the client half.
    expect(cmd).not.toContain('telemetry.export.otlp.endpoint')
    expect(cmd).not.toContain('routes.metrics.tls.mtls.enabled=false')
    expect(screen.getByTestId('tp-operator-fresh')).toHaveTextContent('Recorded a telemetry intent')
  })

  test('one signal type to the operator and the other to an endpoint: both are recorded, and only the operator needs a certificate', async () => {
    const user = userEvent.setup()
    render(tree())
    await splitToRun(user)
    await user.click(screen.getByTestId('tp-lane-logs-guided-destination-change'))
    await user.type(screen.getByTestId('tp-lane-logs-guided-destination-search'), 'loki')
    await user.click(screen.getByTestId('tp-lane-logs-guided-destination-external-preset-loki'))
    await toRun(user)
    await user.click(await screen.findByTestId('tp-operator-generate'))
    const cmd = (await screen.findByTestId('tp-operator-command')).textContent ?? ''
    const args = createTelemetryIntent.mock.calls[0]
    expect(args[6]).toEqual(OP_DEST)
    expect(args[7].metrics).toEqual(OP_DEST)
    expect(args[7].logs).toMatchObject({ kind: 'external' })
    expect(args[7].logs.endpoint).toContain('loki')
    // The external route is built by the page, and states mutual TLS off for itself.
    expect(cmd).toContain('--set telemetry.export.routes.logs.tls.mtls.enabled=false')
    expect(cmd.match(/kubectl create secret generic op-eu-export-mtls/g)).toHaveLength(1)
  })

  test('an intent that already has routes is told to clear them when the draft goes back to one destination', async () => {
    listTelemetryIntents.mockResolvedValue([intent({ id: 'ti-9', routes: { metrics: OP_DEST, logs: OP_DEST } })])
    getTelemetryIntentCommand.mockResolvedValue({ installFragment: FRAGMENT, secretCommands: [SECRET] })
    const user = userEvent.setup()
    render(tree())
    await pickOperator(user)
    await toRun(user)
    await user.click(await generateButton())
    await screen.findByTestId('tp-operator-command')
    expect(updateTelemetryIntentDestination).toHaveBeenCalledWith(CONN, 'ti-9', OP_DEST, {})
  })
})


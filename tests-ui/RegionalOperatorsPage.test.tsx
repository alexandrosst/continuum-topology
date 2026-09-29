import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import RegionalOperatorsPage from '@/pages/RegionalOperatorsPage'
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
const createOperator = vi.fn(async (_c: unknown, name: string, sourceClusterIds: string[], destination: { endpoint: string }) => ({
  operator: {
    id: 'op-1', orgId: 'o', name, status: 'active' as const, sourceClusterIds, destination,
    createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  },
  token: 'shown-once-secret',
  install: 'helm install op-1 ./continuum-regional-operator-0.1.0.tgz \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=backend.example.com:4317',
  secretCommand: 'kubectl create secret generic op-1-receiver-auth --namespace continuum-system --from-literal=token=shown-once-secret',
  reminders: [] as string[],
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
    ))
    expect(screen.getByText(/kubectl create secret generic op-1-receiver-auth/)).toBeInTheDocument()
    expect(screen.getByText(/helm install op-1/)).toBeInTheDocument()
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

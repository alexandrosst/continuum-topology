import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import NamespacesPage from '@/pages/NamespacesPage'
import type { Agent, Cluster, Service } from '@/lib/types'

const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const svc = (name: string, namespace: string, over: Partial<Service> = {}) =>
  ({ id: `s-${namespace}-${name}`, clusterId: 'c1', name, namespace, kind: 'Deployment', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Service
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', kubernetesVersion: 'v1.30', ...over }) as Agent

// A page test: everything NamespacesPage reads from the shared stores is faked here, so the test exercises
// the page's own rendering/filtering/column-visibility logic without needing a real server or seeded stores.
let topologyState: { clusters: Cluster[]; namespaces: never[]; services: Service[]; agents: Agent[] }

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { status: string }) => unknown) => selector({ status: 'connected' }),
}))
vi.mock('@/components/discovery/ConnectFlow', () => ({
  useConnectFlow: () => ({ start: vi.fn(), dialogs: null, canStart: true, wizardOpen: false }),
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <NamespacesPage />
    </MemoryRouter>,
  )
}

describe('NamespacesPage', () => {
  test('with nothing connected yet, explains that instead of showing an empty table', () => {
    topologyState = { clusters: [], namespaces: [], services: [], agents: [] }
    renderPage()
    expect(screen.getByText('No cluster is connected yet')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  test('lists one row per namespace, and the Services column can be hidden via the column picker', async () => {
    const user = userEvent.setup()
    topologyState = {
      clusters: [cl()],
      namespaces: [],
      services: [svc('cart', 'shop'), svc('pay', 'shop')],
      agents: [ag()],
    }
    renderPage()

    const row = screen.getByTestId('namespace-row')
    expect(within(row).getByText('shop')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Services' })).toBeInTheDocument()

    await user.click(screen.getByTestId('columns-button'))
    await user.click(screen.getByTestId('column-services'))

    expect(screen.queryByRole('columnheader', { name: 'Services' })).not.toBeInTheDocument()
    // The row itself is still there - only that one column's cell is gone.
    expect(screen.getByTestId('namespace-row')).toBeInTheDocument()
  })
})

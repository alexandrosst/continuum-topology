import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import AgentsPage from '@/pages/AgentsPage'
import type { Agent, Cluster } from '@/lib/types'

// A page test in the same style as NamespacesPage.test.tsx: everything AgentsPage reads from the shared
// stores is faked here, and ConnectFlow is mocked directly so the connect wizard's own dependencies (a real
// server connection, ServerConnect) never have to be stood up just to check the "Configure telemetry"
// button's own visibility/enabled state next to it.
const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', tier: 'edge', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', accessTier: 2, name: 'edge-1', version: '1.0.0', modules: [], fingerprint: 'x', ...over }) as Agent

let topologyState: { agents: Agent[]; clusters: Cluster[]; sites: never[] }
let serverState: { status: string; canEdit: () => boolean; state?: { agents: Agent[] } }

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: typeof serverState) => unknown) => (selector ? selector(serverState) : serverState),
}))
vi.mock('@/components/discovery/ConnectFlow', () => ({
  useConnectFlow: () => ({ start: vi.fn(), dialogs: null, canStart: false, wizardOpen: false }),
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <AgentsPage />
    </MemoryRouter>,
  )
}

describe('AgentsPage', () => {
  test('"Configure telemetry" is hidden entirely for a viewer who cannot edit', () => {
    topologyState = { agents: [ag()], clusters: [cl()], sites: [] }
    serverState = { status: 'connected', canEdit: () => false }
    renderPage()
    expect(screen.queryByRole('button', { name: /Configure telemetry/ })).not.toBeInTheDocument()
  })

  test('with no approved cluster yet, the button is disabled with an explanatory title - discovery is a prerequisite', () => {
    topologyState = { agents: [], clusters: [], sites: [] }
    serverState = { status: 'connected', canEdit: () => true }
    renderPage()
    const btn = screen.getByRole('button', { name: /Configure telemetry/ })
    expect(btn).toBeDisabled()
    expect(btn).toHaveAttribute('title', 'Connect a cluster first')
  })

  test('once a cluster is approved, an editor can open the telemetry flow', () => {
    topologyState = { agents: [ag()], clusters: [cl()], sites: [] }
    serverState = { status: 'connected', canEdit: () => true }
    renderPage()
    const btn = screen.getByRole('button', { name: /Configure telemetry/ })
    expect(btn).not.toBeDisabled()
    expect(btn).not.toHaveAttribute('title')
  })
})

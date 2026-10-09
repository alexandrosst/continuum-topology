import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import TelemetryWizard from '@/components/telemetry/TelemetryWizard'
import type { Agent, Cluster } from '@/lib/types'

// Same recipe as PipelinePage.test.tsx: the shared stores this component reads from are faked
// here, so the test exercises the wizard's own pick/configure logic without a real server or seeded stores.
const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', tier: 'edge', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', accessTier: 2, name: 'edge-1', version: '1.0.0', modules: [], fingerprint: 'x', ...over }) as Agent

let topologyState: { agents: Agent[]; clusters: Cluster[] }

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
const reloadInfo = vi.fn(async () => undefined)
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { state?: { agents: Agent[] }; info?: { install?: undefined }; role?: undefined; conn: () => null; orgId?: string; reloadInfo: () => Promise<void> }) => unknown) =>
    // conn() is null: the operator and FUSION hooks the Destination step mounts have no server to ask here,
    // so they stay idle instead of rejecting after the test has finished.
    selector ? selector({ state: { agents: [] }, info: {}, role: undefined, conn: () => null, orgId: undefined, reloadInfo }) : { state: { agents: [] }, info: {}, reloadInfo },
  useConn: () => ({ url: '', org: '' }),
}))

function renderWizard(props: { agentId?: string; initialScope?: { name: string; namespaces: string[] } } = {}) {
  const onClose = vi.fn()
  render(
    <MemoryRouter initialEntries={['/topology']}>
      <Routes>
        <Route path="/topology" element={<TelemetryWizard open onClose={onClose} {...props} />} />
        <Route path="/agents" element={<div>agents-page-marker</div>} />
      </Routes>
    </MemoryRouter>,
  )
  return { onClose }
}

describe('TelemetryWizard', () => {
  test('with no approved cluster, shows the prerequisite empty state and hands off to the connect wizard', async () => {
    const user = userEvent.setup()
    topologyState = { agents: [], clusters: [] }
    renderWizard()
    expect(screen.getByText('Connect a cluster first')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-wizard-connect'))
    expect(screen.getByText('agents-page-marker')).toBeInTheDocument()
  })

  test('with no agent named, "Where from" lists the approved clusters; Continue is off until one is picked, then the setup opens', async () => {
    const user = userEvent.setup()
    topologyState = { agents: [ag(), ag({ id: 'a2', clusterId: 'c2', name: 'edge-2' })], clusters: [cl(), cl({ id: 'c2', name: 'edge-2' })] }
    renderWizard()
    expect(screen.getByRole('dialog', { name: 'Set up telemetry' })).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-wizard-steps')).toHaveTextContent(/Where from.*What to collect.*Where to send.*Review and install/)
    expect(screen.getByTestId('telemetry-wizard-picker')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-wizard-continue')).toBeDisabled()
    expect(screen.queryByTestId('telemetry-wizard-guided-step-collect')).not.toBeInTheDocument()

    await user.click(screen.getAllByTestId('telemetry-wizard-target')[0])
    await user.click(screen.getByTestId('telemetry-wizard-continue'))
    expect(screen.queryByTestId('telemetry-wizard-picker')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-wizard-guided-step-collect')).toBeInTheDocument()
    // The cluster was chosen here, so the way back to the list is offered.
    await user.click(screen.getByTestId('telemetry-wizard-guided-change-cluster'))
    expect(screen.getByTestId('telemetry-wizard-picker')).toBeInTheDocument()
  })

  test('the only approved cluster is already picked, so Continue is on straight away', () => {
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard()
    expect(screen.getByTestId('telemetry-wizard-continue')).toBeEnabled()
  })

  test('a target and scope handed off from the topology skip "Where from", with the scope carried over and no way back to a list', () => {
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard({ agentId: 'a1', initialScope: { name: 'shop scope (from topology)', namespaces: ['shop'] } })
    expect(screen.queryByTestId('telemetry-wizard-picker')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-wizard-guided-step-collect')).toBeInTheDocument()
    expect(screen.queryByTestId('telemetry-wizard-guided-change-cluster')).not.toBeInTheDocument()
    // The scope is open where it is edited, with the name it came with.
    expect(screen.getByTestId('telemetry-wizard-guided-scope')).toHaveAttribute('open')
  })

  test('a handed-off scope for a cluster with nothing set up starts at What to collect, never at a dead-end review', () => {
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard({ agentId: 'a1', initialScope: { name: 'shop scope (from topology)', namespaces: ['shop'] } })
    expect(screen.getByTestId('telemetry-wizard-guided-step-collect')).toBeInTheDocument()
    expect(screen.queryByTestId('telemetry-wizard-guided-step-review')).not.toBeInTheDocument()
  })
})

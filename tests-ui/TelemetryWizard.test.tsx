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

  test('with no agent named, shows a picker of approved clusters and clicking one moves to the configure phase', async () => {
    const user = userEvent.setup()
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard()
    expect(screen.getByTestId('telemetry-wizard-picker')).toBeInTheDocument()
    expect(screen.queryByText('Installed now')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('telemetry-wizard-target'))
    expect(screen.queryByTestId('telemetry-wizard-picker')).not.toBeInTheDocument()
    expect(screen.getByText('Installed now')).toBeInTheDocument()
  })

  test('a target and scope handed off from the topology skip straight to the configure phase, scope carried over', () => {
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard({ agentId: 'a1', initialScope: { name: 'shop scope (from topology)', namespaces: ['shop'] } })
    expect(screen.queryByTestId('telemetry-wizard-picker')).not.toBeInTheDocument()
    expect(screen.getByText('Installed now')).toBeInTheDocument()
    // Being handed a scope starts the form in guided mode, which is where that scope actually shows up.
    expect(screen.getByTestId('telemetry-wizard-mode-guided')).toHaveAttribute('aria-checked', 'true')
  })

  test('a scope handed off for a target with nothing configured yet starts the guided wizard at Collect, not Review', () => {
    // Regression test: a fresh "Define scope from selection" hand-off used to seed the wizard straight onto
    // the 'scope' step, which - with nothing turned on yet - immediately collapsed to 'review' (a dead end
    // reading "nothing is turned on yet"), skipping Collect entirely. See GuidedWizard.tsx's
    // rawStep initializer.
    topologyState = { agents: [ag()], clusters: [cl()] }
    renderWizard({ agentId: 'a1', initialScope: { name: 'shop scope (from topology)', namespaces: ['shop'] } })
    expect(screen.getByTestId('telemetry-wizard-guided-step-collect')).toBeInTheDocument()
    expect(screen.queryByText(/nothing is turned on yet/i)).not.toBeInTheDocument()
  })
})

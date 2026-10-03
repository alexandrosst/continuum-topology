import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import Inspector from '@/components/topology/Inspector'
import type { Cluster, DiscoveryAgent } from '@/lib/types'

// A focused Inspector test: only the 'agent' selection branch is exercised here - see Inspector.test.tsx's
// own doc comment for why each of these store/hook fakes is only as deep as that branch actually reads.
let clustersFixture: Cluster[] = []
let discoveryAgentsFixture: DiscoveryAgent[] = []

vi.mock('@/store/topology', () => ({
  useTopology: () => ({
    clusters: clustersFixture,
    nodes: [],
    namespaces: [],
    services: [],
    devices: [],
    dependencies: [],
    applications: [],
    sites: [],
    siteLinks: [],
    externalEndpoints: [],
    agents: [],
    suggestions: [],
  }),
  useRawTopology: (selector: (s: { upsertExternalEndpoint: () => void }) => unknown) => selector({ upsertExternalEndpoint: vi.fn() }),
  usePaths: () => ({}),
  useClusterPairConnectivity: () => [],
  useDiscoveryAgents: () => discoveryAgentsFixture,
}))
vi.mock('@/store/history', () => ({
  useHistoryView: (selector: (s: { at: null; snapshot: null }) => unknown) => selector({ at: null, snapshot: null }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { info: undefined; status: string; url: string; orgId: string; state: undefined; conn: () => undefined }) => unknown) =>
    selector({ info: undefined, status: 'idle', url: '', orgId: 'o', state: undefined, conn: () => undefined }),
  useConn: () => ({ url: '', org: 'o' }),
}))

const cluster = (over: Partial<Cluster> = {}): Cluster => ({
  id: 'cl-1',
  name: 'edge-patras',
  tier: 'edge',
  distribution: 'k3s',
  version: 'v1.30.0',
  provider: 'On-prem',
  region: '',
  status: 'healthy',
  labels: {},
  source: 'discovered',
  orgId: 'o',
  ...over,
})

const agent = (over: Partial<DiscoveryAgent> = {}): DiscoveryAgent => ({
  id: 'ag-1',
  clusterId: 'cl-1',
  name: 'edge-patras',
  source: 'discovered',
  orgId: 'o',
  ...over,
})

function renderInspector(ag: DiscoveryAgent, clusters: Cluster[] = [cluster()]) {
  discoveryAgentsFixture = [ag]
  clustersFixture = clusters
  return render(
    <MemoryRouter>
      <Inspector selection={{ kind: 'agent', id: ag.id }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
    </MemoryRouter>,
  )
}

describe('Inspector · discovery agent', () => {
  test('a live agent names its cluster and says Live, with no self-telemetry section when none arrived yet', () => {
    renderInspector(agent())
    expect(screen.getByText('edge-patras', { selector: 'h2' })).toBeTruthy()
    expect(screen.getByText('edge-patras', { selector: 'button' })).toBeTruthy() // the cluster link
    expect(screen.getByText('Live')).toBeTruthy()
    expect(screen.queryByText('Self telemetry')).toBeNull()
  })

  test('a stale agent says so, with its own reason when the server gave one', () => {
    renderInspector(agent({ stale: true, state: 'stale', stateReason: 'stale for 2 h' }))
    expect(screen.getByText('stale for 2 h')).toBeTruthy()
  })

  test('a stale agent with no stateReason still says it is not reporting, never blank', () => {
    renderInspector(agent({ stale: true, state: 'stale' }))
    expect(screen.getByText('Not reporting')).toBeTruthy()
  })

  test('self-telemetry, when present, shows RSS and goroutines - the light snapshot, not a history', () => {
    renderInspector(agent({ self: { t: '2026-09-19T12:00:00Z', rssBytes: 256 * 1024 * 1024, goroutines: 42 } }))
    expect(screen.getByText('Self telemetry')).toBeTruthy()
    expect(screen.getByText('256 MB')).toBeTruthy()
    expect(screen.getByText('42')).toBeTruthy()
  })

  test('self-telemetry links out to the System Health page for the full history', () => {
    renderInspector(agent({ self: { t: '2026-09-19T12:00:00Z', rssBytes: 256 * 1024 * 1024, goroutines: 42 } }))
    const link = screen.getByText('View full history').closest('a')
    expect(link).toHaveAttribute('href', '/system-health')
  })

  test('an agent whose cluster is unknown still renders (no crash), falling back to the raw cluster id', () => {
    renderInspector(agent({ clusterId: 'cl-missing' }), [])
    expect(screen.getByText('cl-missing')).toBeTruthy()
  })

  test('an unknown agent id renders nothing', () => {
    discoveryAgentsFixture = []
    clustersFixture = []
    const { container } = render(
      <MemoryRouter>
        <Inspector selection={{ kind: 'agent', id: 'ag-missing' }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
      </MemoryRouter>,
    )
    expect(container).toBeEmptyDOMElement()
  })
})

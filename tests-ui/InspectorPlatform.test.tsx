import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import Inspector from '@/components/topology/Inspector'
import type { PlatformEntity, PlatformModel } from '@/lib/platformLayer'
import type { Cluster, DiscoveryAgent } from '@/lib/types'

// A focused Inspector test: only the 'platform' selection branch is exercised here - see Inspector.test.tsx's
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
  useClusterLinks: () => [],
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

const cluster: Cluster = { id: 'cl-1', name: 'edge-patras', tier: 'edge', distribution: 'k3s', version: 'v1.30.0', provider: 'On-prem', region: '', status: 'healthy', labels: {}, source: 'discovered', orgId: 'o' }

const part = (over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id: 'local:ag-1', kind: 'local', name: 'Local operator', detail: 'Metrics', status: 'healthy', sentence: 'Sending, last data just now.', sendsTo: [{ id: 'regional:op-1', name: 'Athens aggregator' }], clusterId: 'cl-1', clusterName: 'edge-patras', version: '0.19.3', lastDataAt: new Date(Date.now() - 5000).toISOString(), ...over })
const regional = part({ id: 'regional:op-1', kind: 'regional', name: 'Athens aggregator', clusterId: undefined, clusterName: undefined, sendsTo: [], lastDataAt: undefined })

function renderPlatform(selected: PlatformEntity, onSelect = vi.fn(), others: PlatformEntity[] = [regional]) {
  clustersFixture = [cluster]
  const platform: PlatformModel = { entities: [selected, ...others], edges: [] }
  render(
    <MemoryRouter>
      <Inspector selection={{ kind: 'platform', id: selected.id }} platform={platform} onSelect={onSelect} onEdit={() => {}} onClose={() => {}} />
    </MemoryRouter>,
  )
  return onSelect
}

describe('Inspector · platform part', () => {
  test('names the part, its kind and its state as glyph and word, and says what is going on in one sentence', () => {
    renderPlatform(part())
    expect(screen.getByRole('heading', { name: 'Local operator' })).toBeTruthy()
    expect(screen.getByRole('img', { name: 'Healthy' })).toBeTruthy()
    expect(screen.getByText('Sending, last data just now.')).toBeTruthy()
    expect(screen.getByText('0.19.3')).toBeTruthy()
    expect(screen.getByText('edge-patras')).toBeTruthy()
  })

  test('a part that needs attention also says what to do', () => {
    renderPlatform(part({ status: 'attention', sentence: 'Quiet: no data for 14 min.', todo: 'Check that what it collects is still running in the cluster.' }))
    expect(screen.getByRole('img', { name: 'Needs attention' })).toBeTruthy()
    expect(screen.getByText('Quiet: no data for 14 min.')).toBeTruthy()
    expect(screen.getByText('Check that what it collects is still running in the cluster.')).toBeTruthy()
  })

  test('a local operator with no data says No data yet instead of a made-up time', () => {
    renderPlatform(part({ lastDataAt: undefined, status: 'unknown', sentence: 'No data yet.' }))
    expect(screen.getAllByText('No data yet').length).toBeGreaterThan(0)
  })

  test('what it sends to is a link to that part, and Open in Pipeline leads to the Pipeline', () => {
    const onSelect = renderPlatform(part())
    fireEvent.click(screen.getByRole('button', { name: 'Athens aggregator' }))
    expect(onSelect).toHaveBeenCalledWith({ kind: 'platform', id: 'regional:op-1' })
    expect(screen.getByText('Open in Pipeline').closest('a')).toHaveAttribute('href', '/pipeline')
  })

  test('a destination outside the platform is written, not linked', () => {
    renderPlatform(part({ sendsTo: [], elsewhere: 'otel.corp.example:4317' }))
    expect(screen.getByText(/otel\.corp\.example:4317/)).toBeTruthy()
  })

  test('a Discovery agent shows its own footprint when it reported one, linking to System health for the history', () => {
    discoveryAgentsFixture = [{ id: 'da', clusterId: 'cl-1', name: 'edge-patras', source: 'discovered', orgId: 'o', self: { t: '2026-09-19T12:00:00Z', rssBytes: 256 * 1024 * 1024, goroutines: 42 } } as DiscoveryAgent]
    renderPlatform(part({ id: 'agent:ag-1', kind: 'agent', name: 'Discovery agent', sendsTo: [] }))
    expect(screen.getByText('256 MB')).toBeTruthy()
    expect(screen.getByText('View full history').closest('a')).toHaveAttribute('href', '/system-health')
    expect(screen.getByText('Open in Discovery').closest('a')).toHaveAttribute('href', '/discovery')
    discoveryAgentsFixture = []
  })

  test('a part that is no longer on the canvas renders nothing', () => {
    const { container } = render(
      <MemoryRouter>
        <Inspector selection={{ kind: 'platform', id: 'local:gone' }} platform={{ entities: [], edges: [] }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
      </MemoryRouter>,
    )
    expect(container).toBeEmptyDOMElement()
  })
})

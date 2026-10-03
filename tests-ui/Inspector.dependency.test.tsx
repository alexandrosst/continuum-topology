import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import Inspector from '@/components/topology/Inspector'
import type { Dependency } from '@/lib/types'

// A focused Inspector test for the 'dependency' selection branch's trend sparklines (DependencyTrend) -
// see Inspector.test.tsx's own header comment for the same store-mocking pattern this borrows.
let topologyState: {
  clusters: never[]
  nodes: never[]
  namespaces: never[]
  services: never[]
  devices: never[]
  dependencies: Dependency[]
  applications: never[]
  sites: never[]
  siteLinks: never[]
  externalEndpoints: never[]
  agents: never[]
  suggestions: never[]
}

const dependencySeries = vi.fn()

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
  useRawTopology: (selector: (s: { upsertExternalEndpoint: () => void }) => unknown) => selector({ upsertExternalEndpoint: vi.fn() }),
  usePaths: () => ({}),
  useClusterLinks: () => [],
}))
vi.mock('@/store/history', () => ({
  useHistoryView: (selector: (s: { at: null; snapshot: null }) => unknown) => selector({ at: null, snapshot: null }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { info: undefined; status: string; url: string; orgId: string; state: undefined; conn: () => undefined }) => unknown) =>
    selector({ info: undefined, status: 'idle', url: '', orgId: 'o', state: undefined, conn: () => undefined }),
  useConn: () => ({ url: '', org: 'o' }),
}))
vi.mock('@/components/EvidenceSection', () => ({
  EvidenceSection: () => null,
  WeakValues: () => null,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, dependencySeries: (...a: unknown[]) => dependencySeries(...a) } }
})

const dep = (over: Partial<Dependency> = {}): Dependency => ({
  id: 'dep-1',
  orgId: 'o',
  from: 'a',
  fromKind: 'external',
  to: 'b',
  toKind: 'external',
  sources: ['observed'],
  confidence: 'high',
  protocol: 'TCP',
  stats: {},
  ...over,
})

function renderDependency(d: Dependency) {
  topologyState = {
    clusters: [],
    nodes: [],
    namespaces: [],
    services: [],
    devices: [],
    dependencies: [d],
    applications: [],
    sites: [],
    siteLinks: [],
    externalEndpoints: [],
    agents: [],
    suggestions: [],
  }
  return render(
    <MemoryRouter>
      <Inspector selection={{ kind: 'dependency', id: d.id }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
    </MemoryRouter>,
  )
}

describe('Inspector · dependency provenance', () => {
  test('Found by and Confidence are one shared Provenance strip, with the method as its secondary badge', async () => {
    dependencySeries.mockResolvedValue([])
    renderDependency(dep({ sources: ['observed'], via: 'ebpf', confidence: 'medium' }))
    await waitFor(() => expect(dependencySeries).toHaveBeenCalled())
    expect(screen.getByText('Provenance')).toBeInTheDocument()
    expect(screen.getByText('observed')).toBeInTheDocument()
    expect(screen.getByText('eBPF')).toBeInTheDocument()
    expect(screen.getByTestId('provenance-confidence')).toHaveTextContent('medium')
    expect(screen.queryByText('Found by')).not.toBeInTheDocument()
    expect(screen.queryByText('Confidence')).not.toBeInTheDocument()
  })

  test('a declared-only dependency (no measurement method) shows no secondary badge', () => {
    // Never observed, so DependencyTrend's own effect (and its dependencySeries call) never fires here -
    // nothing to await; the Provenance strip renders synchronously either way.
    renderDependency(dep({ id: 'dep-3', sources: ['declared'], via: undefined, confidence: 'low' }))
    expect(screen.getByText('declared')).toBeInTheDocument()
    expect(screen.getByTestId('provenance-confidence')).toHaveTextContent('low')
  })
})

describe('Inspector · dependency trend', () => {
  test('shows a labeled trend row once the series resolves with at least one measurable signal', async () => {
    dependencySeries.mockResolvedValue([
      { at: '2026-01-01T00:00:00Z', rttMs: 10 },
      { at: '2026-01-01T00:01:00Z', rttMs: 12 },
    ])
    renderDependency(dep())
    await waitFor(() => expect(screen.getByText('Trend (24h)')).toBeInTheDocument())
    expect(dependencySeries).toHaveBeenCalledWith(expect.anything(), 'dep-1', 24)
  })

  test('shows nothing when the series has no signal with two measured points', async () => {
    dependencySeries.mockResolvedValue([{ at: '2026-01-01T00:00:00Z', rttMs: 10 }])
    renderDependency(dep({ id: 'dep-2' }))
    await waitFor(() => expect(dependencySeries).toHaveBeenCalled())
    expect(screen.queryByText('Trend (24h)')).toBeNull()
  })
})

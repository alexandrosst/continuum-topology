import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import Inspector from '@/components/topology/Inspector'
import type { ExternalEndpoint } from '@/lib/types'

// A focused Inspector test: only the 'external' selection branch is exercised here, so every store/hook
// Inspector (and usePlacementSuggestions, which it calls into) reads from is faked with just enough shape
// to render that branch without crashing - see NamespacesPage.test.tsx for the same store-mocking pattern.
let topologyState: {
  clusters: never[]
  nodes: never[]
  namespaces: never[]
  services: never[]
  devices: never[]
  dependencies: never[]
  applications: never[]
  sites: never[]
  siteLinks: never[]
  externalEndpoints: ExternalEndpoint[]
  agents: never[]
  suggestions: never[]
}

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
  useRawTopology: (selector: (s: { upsertExternalEndpoint: () => void }) => unknown) => selector({ upsertExternalEndpoint: vi.fn() }),
  usePaths: () => ({}),
}))
vi.mock('@/store/history', () => ({
  useHistoryView: (selector: (s: { at: null; snapshot: null }) => unknown) => selector({ at: null, snapshot: null }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { info: undefined; status: string; url: string; orgId: string }) => unknown) =>
    selector({ info: undefined, status: 'idle', url: '', orgId: 'o' }),
  useConn: () => ({ url: '', org: 'o' }),
}))

const ext = (over: Partial<ExternalEndpoint> = {}): ExternalEndpoint => ({
  id: 'e1',
  host: '216.239.34.178',
  kind: 'saas',
  source: 'discovered',
  orgId: 'o',
  ...over,
})

function renderInspector(endpoint: ExternalEndpoint) {
  topologyState = {
    clusters: [],
    nodes: [],
    namespaces: [],
    services: [],
    devices: [],
    dependencies: [],
    applications: [],
    sites: [],
    siteLinks: [],
    externalEndpoints: [endpoint],
    agents: [],
    suggestions: [],
  }
  return render(
    <MemoryRouter>
      <Inspector selection={{ kind: 'external', id: endpoint.id }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
    </MemoryRouter>,
  )
}

describe('Inspector · external endpoint', () => {
  test('a single-address endpoint shows no address list', () => {
    renderInspector(ext({ name: 'Google', ips: ['216.239.34.178'] }))
    expect(screen.queryByText(/^Addresses/)).not.toBeInTheDocument()
  })

  test('several addresses collapsed into one provider node list every one of them', () => {
    // A collapsed node's `host` is whichever address was seen first, which is also one of the entries in
    // `ips` - so it legitimately appears twice in the panel (once as "Address", once in the address list).
    // Scope the address-list assertions to that list's own row rather than the whole panel.
    renderInspector(
      ext({
        host: '216.239.32.10',
        name: 'Google',
        ips: ['216.239.32.10', '216.239.34.178', '216.239.38.178'],
      }),
    )
    const addressesRow = screen.getByText('Addresses (3)').parentElement!
    expect(within(addressesRow).getByText('216.239.32.10')).toBeInTheDocument()
    expect(within(addressesRow).getByText('216.239.34.178')).toBeInTheDocument()
    expect(within(addressesRow).getByText('216.239.38.178')).toBeInTheDocument()
  })

  test('an endpoint reported without ips (older data) does not crash and shows no address list', () => {
    renderInspector(ext({ name: 'Legacy' }))
    expect(screen.queryByText(/^Addresses/)).not.toBeInTheDocument()
  })
})

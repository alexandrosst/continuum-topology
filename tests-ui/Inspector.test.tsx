import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import Inspector from '@/components/topology/Inspector'
import type { Cluster, ClusterLink } from '@/lib/types'
import type { ExternalEndpoint, MachineNode } from '@/lib/types'

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

let clusterLinksState: ClusterLink[] = []
vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
  useRawTopology: (selector: (s: { upsertExternalEndpoint: () => void }) => unknown) => selector({ upsertExternalEndpoint: vi.fn() }),
  usePaths: () => ({}),
  useClusterPairConnectivity: () => [],
  useClusterLinks: () => clusterLinksState,
  useDiscoveryAgents: () => [],
}))
vi.mock('@/store/history', () => ({
  useHistoryView: (selector: (s: { at: null; snapshot: null }) => unknown) => selector({ at: null, snapshot: null }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { info: undefined; status: string; url: string; orgId: string; state: undefined; conn: () => undefined }) => unknown) =>
    selector({ info: undefined, status: 'idle', url: '', orgId: 'o', state: undefined, conn: () => undefined }),
  useConn: () => ({ url: '', org: 'o' }),
}))
// Only reached by the 'node'/'cluster'/'service' branches (see Inspector's own render, near EntityHistory) -
// the existing 'external' tests above never touched it, which is why this wasn't already mocked. Stubbed
// out entirely rather than wired up for real: what it shows isn't what these tests are about, and doing so
// for real would mean also faking useEffectiveModel's server-polling/etag machinery for no benefit here.
vi.mock('@/components/EvidenceSection', () => ({
  EvidenceSection: () => null,
  WeakValues: () => null,
}))

const ext = (over: Partial<ExternalEndpoint> = {}): ExternalEndpoint => ({
  id: 'e1',
  host: '216.239.34.178',
  kind: 'saas',
  source: 'discovered',
  orgId: 'o',
  ...over,
})

const node = (over: Partial<MachineNode> = {}): MachineNode => ({
  id: 'n1',
  name: 'edge-1',
  clusterId: 'c1',
  role: 'worker',
  kind: 'bare-metal',
  ip: '10.0.1.5',
  os: 'linux',
  cpu: 4,
  memoryGb: 8,
  status: 'ready',
  labels: {},
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

function renderNodeInspector(n: MachineNode) {
  topologyState = {
    clusters: [],
    nodes: [n],
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
  }
  return render(
    <MemoryRouter>
      <Inspector selection={{ kind: 'node', id: n.id }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} />
    </MemoryRouter>,
  )
}

describe('Inspector · node network interfaces', () => {
  test('a long interface summary chip carries the full text as a title, so it can be read even truncated', () => {
    renderNodeInspector(
      node({
        networkInterfaces: [{ name: 'eth0', kind: 'ethernet', speedMbps: 1000, mtu: 1500 }],
      }),
    )
    const full = 'eth0 (ethernet, 1000 Mbps, MTU 1500)'
    const chip = screen.getByText(full)
    expect(chip).toHaveAttribute('title', full)
  })
})

describe('Inspector · node pressure', () => {
  test('cgroup v2 PSI figures show only the resources actually read, each with its own title', () => {
    renderNodeInspector(node({ cpuPressurePct: 2.5, ioPressurePct: 13.75 }))
    const cpu = screen.getByText('CPU 2.5%')
    const io = screen.getByText('IO 13.8%')
    expect(cpu).toHaveAttribute('title')
    expect(io).toHaveAttribute('title')
    expect(screen.queryByText(/^Mem /)).not.toBeInTheDocument()
  })

  test('no "Pressure" row at all when the probe never read any of the three', () => {
    renderNodeInspector(node())
    expect(screen.queryByText('Pressure')).not.toBeInTheDocument()
  })

  test('a real, measured 0% still renders - it must not be treated the same as "not read"', () => {
    renderNodeInspector(node({ memoryPressurePct: 0 }))
    expect(screen.getByText('Mem 0.0%')).toBeInTheDocument()
  })
})

describe('Inspector · node OOM kills', () => {
  test('no row at all when the probe never read it', () => {
    renderNodeInspector(node())
    expect(screen.queryByText('OOM kills')).not.toBeInTheDocument()
  })

  test('a real, measured 0 still renders - it must not be treated the same as "not read"', () => {
    renderNodeInspector(node({ oomKillCount: 0 }))
    expect(screen.getByText('0 total')).toBeInTheDocument()
  })

  test('a non-zero count renders and is flagged', () => {
    renderNodeInspector(node({ oomKillCount: 3 }))
    const row = screen.getByText('3 total')
    expect(row).toBeInTheDocument()
    expect(row).toHaveAttribute('title')
    expect(row.className).toContain('text-bad')
  })
})

describe('Inspector · node link saturation', () => {
  test('shows the saturation percentage per interface, with a tooltip', () => {
    renderNodeInspector(node({ linkSaturation: [{ iface: 'eth0', throughputBps: 800_000_000, saturationPct: 80 }] }))
    const chip = screen.getByText('eth0 80%')
    expect(chip).toHaveAttribute('title')
  })

  test('an interface with no known rated speed shows "unknown%", not a fabricated 0%', () => {
    renderNodeInspector(node({ linkSaturation: [{ iface: 'veth1', throughputBps: 100 }] }))
    expect(screen.getByText('veth1 unknown%')).toBeInTheDocument()
  })

  test('no "Link saturation" row at all when the flow collector never reported one', () => {
    renderNodeInspector(node())
    expect(screen.queryByText('Link saturation')).not.toBeInTheDocument()
  })
})

describe('Inspector · node SNAT exhaustion', () => {
  test('no row at all when nothing has been reported', () => {
    renderNodeInspector(node())
    expect(screen.queryByText('SNAT exhaustion')).not.toBeInTheDocument()
  })

  test('a real, measured 0 shows no row either - unlike OOM kills, a confirmed healthy 0 is not worth flagging', () => {
    renderNodeInspector(node({ snatExhaustion: 0 }))
    expect(screen.queryByText('SNAT exhaustion')).not.toBeInTheDocument()
  })

  test('a non-zero count renders and is flagged', () => {
    renderNodeInspector(node({ snatExhaustion: 12 }))
    const row = screen.getByText('12 failed connects')
    expect(row).toBeInTheDocument()
    expect(row).toHaveAttribute('title')
    expect(row.className).toContain('text-bad')
  })
})

describe('Inspector · node tunnels', () => {
  test('an unconfirmed tunnel shows just its kind, no fabricated peer', () => {
    renderNodeInspector(node({ tunnels: [{ name: 'wg0', kind: 'wireguard', addresses: ['10.8.0.1/24'], routes: ['10.8.0.0/24'] }] }))
    expect(screen.getByText('wg0 (wireguard)')).toBeInTheDocument()
  })

  test('a server-confirmed tunnel names the other end it was matched to', () => {
    renderNodeInspector(node({ tunnels: [{ name: 'wg0', kind: 'wireguard', confirmed: 'edge-2' }] }))
    expect(screen.getByText('wg0 (wireguard, confirmed ↔ edge-2)')).toBeInTheDocument()
  })

  test('a node with no tunnels shows no Tunnels row at all', () => {
    renderNodeInspector(node({}))
    expect(screen.queryByText('Tunnels')).not.toBeInTheDocument()
  })

  test('mtu and administrative up/down state show up alongside kind when reported', () => {
    renderNodeInspector(node({ tunnels: [{ name: 'wg0', kind: 'wireguard', mtu: 1420, up: true }] }))
    expect(screen.getByText('wg0 (wireguard, up, MTU 1420)')).toBeInTheDocument()
  })

  test('an administratively-down tunnel says so rather than staying silent', () => {
    renderNodeInspector(node({ tunnels: [{ name: 'gre1', kind: 'gre', up: false }] }))
    expect(screen.getByText('gre1 (gre, down)')).toBeInTheDocument()
  })
})

describe('Inspector · node type confidence', () => {
  // The node page also shows a record-level Provenance strip of its own, further down in "Discovery" - these
  // tests scope every query to the "Identity" section so that unrelated strip (observation state, not the
  // Type guess) is never what a query happens to match.
  const identity = () => within(screen.getByText('Identity').parentElement!)

  test('an unprobed guess from the Kubernetes API shows the shared Provenance strip, not bespoke prose', () => {
    renderNodeInspector(node({ kind: 'vm', probed: false, evidence: { kind: { signal: 'Kubernetes API', confidence: 'low' } } }))
    const scope = identity()
    expect(scope.getByText('Provenance')).toBeInTheDocument()
    expect(scope.getByText('Kubernetes API')).toBeInTheDocument()
    const chip = scope.getByTestId('provenance-confidence')
    expect(chip).toHaveTextContent('low')
    expect(chip).toHaveAttribute('title', expect.stringContaining('guess from the Kubernetes API'))
  })

  test('a probe-based guess names the probe as the source, with its own caveat as the tooltip', () => {
    renderNodeInspector(node({ kind: 'vm', probed: true, evidence: { kind: { signal: 'chassis', confidence: 'medium' } } }))
    const scope = identity()
    expect(scope.getByText('node probe')).toBeInTheDocument()
    expect(scope.getByTestId('provenance-confidence')).toHaveAttribute('title', expect.stringContaining('no hypervisor flag'))
  })

  test('a confirmed override shows no confidence strip for Type at all', () => {
    renderNodeInspector(node({ kind: 'vm', probed: true, evidence: { kind: { signal: 'chassis', confidence: 'medium' } }, overrides: { kind: 'vm' } }))
    expect(identity().queryByTestId('provenance-confidence')).not.toBeInTheDocument()
  })
})

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

  test('an endpoint renders no bespoke "seen in traffic" line any more: it is the shared Provenance freshness', () => {
    renderInspector(ext({ name: 'Payments API', lastSeen: new Date(Date.now() - 2 * 3600_000).toISOString() }))
    expect(screen.getByText('Provenance')).toBeInTheDocument()
    expect(screen.getByTestId('provenance-freshness')).toHaveTextContent('Seen 2 h ago · found in traffic, not declared anywhere')
  })

  test('per-field identity evidence wins over the generic "Detected" origin wording, with its own confidence', () => {
    renderInspector(ext({ name: 'Stripe', evidence: { identity: { signal: 'reverse DNS to stripe.com', confidence: 'high' } } }))
    expect(screen.getByText('reverse DNS to stripe.com')).toBeInTheDocument()
    expect(screen.getByTestId('provenance-confidence')).toHaveTextContent('high')
  })

  test('a manually-named endpoint (no agent evidence) falls back to the record origin wording', () => {
    renderInspector(ext({ name: 'Internal DB', source: 'manual' }))
    expect(screen.getByText('Entered manually')).toBeInTheDocument()
    expect(screen.queryByTestId('provenance-freshness')).not.toBeInTheDocument()
  })

  test('on a narrow screen the sheet can shrink to a short peek and back, announced as expanded or not', () => {
    renderInspector(ext({ name: 'Payments API' }))
    const toggle = screen.getByTestId('inspector-peek')
    const sheet = toggle.closest('aside')!
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(sheet.className).toContain('max-h-[65vh]')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(sheet.className).toContain('max-h-[9.5rem]')
    expect(sheet.className).not.toContain('max-h-[65vh]')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
  })
})

describe('Inspector · what the canvas no longer draws', () => {
  test('a machine shows its load as meters, since its card only says when one resource is under pressure', () => {
    renderNodeInspector(node({ allocatable: { cpu: 10, memoryGb: 10 }, requested: { cpu: 4, memoryGb: 9 } }))
    const meters = screen.getByTestId('load-meters')
    expect(within(meters).getByText('40%')).toBeInTheDocument()
    expect(within(meters).getByText('90%')).toBeInTheDocument()
  })

  test('a cluster shows its load and a local operator\'s telemetry, with the control the box used to carry', () => {
    const cluster = { id: 'c1', name: 'atlas', status: 'healthy', tier: 'edge', source: 'discovered', orgId: 'o', distribution: 'k3s', version: '1.30', labels: {} } as unknown as Cluster
    topologyState = { clusters: [cluster] as never[], nodes: [node({ allocatable: { cpu: 10, memoryGb: 10 }, requested: { cpu: 5, memoryGb: 2 } })] as never[], namespaces: [], services: [], devices: [], dependencies: [], applications: [], sites: [], siteLinks: [], externalEndpoints: [], agents: [], suggestions: [] }
    const start = vi.fn()
    render(
      <MemoryRouter>
        <Inspector selection={{ kind: 'cluster', id: 'c1' }} onSelect={() => {}} onEdit={() => {}} onClose={() => {}} localOperators={new Map([['c1', { layers: ['infrastructure'], agentId: 'a1' }]])} onConfigureTelemetry={start} />
      </MemoryRouter>,
    )
    expect(within(screen.getByTestId('load-meters')).getByText('50%')).toBeInTheDocument()
    expect(screen.getByText('Running (infrastructure)')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('configure-local-telemetry'))
    expect(start).toHaveBeenCalledWith('a1')
  })
})

describe('Inspector · networks', () => {
  const cl = (id: string, name: string) => ({ id, name, status: 'healthy', tier: 'edge', source: 'discovered', orgId: 'o', distribution: 'k3s', version: '1.30', labels: {} }) as unknown as Cluster
  const open = (id: string, onSelect = vi.fn()) => {
    topologyState = { clusters: [cl('c1', 'atlas'), cl('c2', 'kestrel'), cl('c3', 'polaris')] as never[], nodes: [], namespaces: [], services: [], devices: [], dependencies: [], applications: [], sites: [], siteLinks: [], externalEndpoints: [], agents: [], suggestions: [] }
    render(<MemoryRouter><Inspector selection={{ kind: 'cluster', id }} onSelect={onSelect} onEdit={() => {}} onClose={() => {}} /></MemoryRouter>)
    return onSelect
  }
  const pair = (a: string, an: string, b: string, bn: string, via: string): ClusterLink => ({ fromCluster: a, fromName: an, toCluster: b, toName: bn, kind: 'subnet', via, redundancy: 1 })

  test('says which subnet or overlay a cluster is on and who shares it, and each name selects that cluster', () => {
    clusterLinksState = [pair('c1', 'atlas', 'c2', 'kestrel', '10.30.0.0/16'), pair('c2', 'kestrel', 'c3', 'polaris', '10.30.0.0/16')]
    const onSelect = open('c2')
    const section = screen.getByTestId('inspector-network')
    expect(within(section).getByText('Same subnet')).toBeInTheDocument()
    expect(within(section).getByText('10.30.0.0/16')).toBeInTheDocument()
    fireEvent.click(within(section).getByText('polaris'))
    expect(onSelect).toHaveBeenCalledWith({ kind: 'cluster', id: 'c3' })
    expect(within(section).getByText('atlas')).toBeInTheDocument()
  })

  test('a cluster on no network has no such section', () => {
    clusterLinksState = [pair('c1', 'atlas', 'c2', 'kestrel', '10.30.0.0/16')]
    open('c3')
    expect(screen.queryByTestId('inspector-network')).toBeNull()
  })
})

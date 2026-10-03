import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import MobilityPanel from '@/components/MobilityPanel'
import type { Cluster, MachineNode, Plan, Service } from '@/lib/types'

// Part 2's merged presentation: MobilityPanel's own feasibility sweep (real movability()/moveTargets(), run
// for real against the fixtures below) plus the plan's one globally-optimized pick, which is mocked here via
// usePlan - the two stay separate computations (see MobilityPanel.tsx's own doc comment), so only the plan
// side needs faking to control which of ServiceAdvice's old fallback states, or which match/mismatch with
// the fit list, each test exercises.
let planResult: { world: { byCluster: Map<string, { name: string }>; cap: { now: number } }; plan: Plan; policy: unknown }

const NOW = Date.parse('2026-09-21T12:00:00Z')

vi.mock('@/lib/placement/usePlacement', () => ({
  useJudgedAt: () => NOW,
  usePlan: () => planResult,
}))

const topologyState = {
  clusters: [] as Cluster[],
  nodes: [] as MachineNode[],
  services: [] as Service[],
  devices: [],
  dependencies: [],
  sites: [],
  siteLinks: [],
  externalEndpoints: [],
  agents: [],
}

vi.mock('@/store/topology', () => ({
  useTopology: () => topologyState,
}))
vi.mock('@/store/effectiveModel', () => ({
  useEffectiveModel: () => undefined,
}))

const cluster = (id: string, name: string): Cluster => ({
  orgId: 'o', source: 'discovered', id, name, tier: 'cloud', distribution: 'k3s', version: 'v1.30', provider: '', region: '', status: 'healthy', labels: {}, state: 'live',
})
const node = (id: string, clusterId: string): MachineNode => ({
  orgId: 'o', source: 'discovered', id, name: id, clusterId, role: 'worker', kind: 'vm', ip: '', os: 'linux', cpu: 16, memoryGb: 32, status: 'healthy', labels: {},
  allocatable: { cpu: 16, memoryGb: 32 }, requested: { cpu: 1, memoryGb: 1 },
})
const service = (over: Partial<Service> = {}): Service => ({
  orgId: 'o', source: 'manual', id: 'svc-1', name: 'api', namespace: 'app', clusterId: 'c-a', kind: 'Deployment', image: '', replicas: 2, nodeIds: [], status: 'healthy', labels: {}, cpuRequestM: 100, memRequestMi: 128,
  ...over,
})

function setWorld(current: string, other: string) {
  topologyState.clusters = [cluster('c-a', 'Cloud A'), cluster('c-b', 'Cloud B')]
  topologyState.nodes = [node('n-a', current), node('n-b', other)]
  topologyState.services = []
}

function renderPanel(svc: Service) {
  return render(
    <MemoryRouter>
      <MobilityPanel service={svc} onSelectCluster={() => {}} />
    </MemoryRouter>,
  )
}

describe('MobilityPanel · plan bridge', () => {
  test('a recommendation that lands on a cluster this sweep also calls a fit is highlighted on that row, not a separate section', () => {
    setWorld('c-a', 'c-b')
    planResult = {
      world: { byCluster: new Map([['c-a', { name: 'Cloud A' }], ['c-b', { name: 'Cloud B' }]]), cap: { now: NOW } },
      plan: {
        recommendations: [{
          serviceId: 'svc-1', serviceName: 'api', from: 'c-a', to: 'c-b',
          current: {} as never, target: {} as never, benefit: 30, net: 28, verdict: 'free', fit: 'fits',
          confidence: 'high', confidenceClass: 'reported', inputs: [], facts: [], wouldChange: [], fixes: [],
          reasons: ['Cloud B is much closer to what it talks to.'], caveats: [], alternatives: [],
        }],
        stay: 0, skipped: [],
      },
      policy: {},
    }
    renderPanel(service())
    // no separate "Where should this run?" section any more
    expect(screen.queryByText('Where should this run?')).not.toBeInTheDocument()
    const fitSection = screen.getByTestId('targets-fits')
    expect(within(fitSection).getByTestId('recommended-highlight')).toBeInTheDocument()
    expect(within(fitSection).getByText(/Recommended — Cloud B is much closer/)).toBeInTheDocument()
    expect(within(fitSection).getByText('saves 28.0 points')).toBeInTheDocument()
    const bridge = screen.getByTestId('plan-bridge')
    expect(bridge).toHaveTextContent('Of the clusters that fit, Cloud B is the current pick from the last optimization pass')
    expect(screen.getByTestId('plan-freshness')).toHaveTextContent('Plan run')
  })

  test('a recommendation whose target is not in the fit list falls back to a standalone bridge sentence, not a crash', () => {
    setWorld('c-a', 'c-b')
    planResult = {
      world: { byCluster: new Map([['c-a', { name: 'Cloud A' }], ['c-b', { name: 'Cloud B' }]]), cap: { now: NOW } },
      plan: {
        recommendations: [{
          serviceId: 'svc-1', serviceName: 'api', from: 'c-a', to: 'c-does-not-exist',
          current: {} as never, target: {} as never, benefit: 10, net: 8, verdict: 'free', fit: 'fits',
          confidence: 'medium', confidenceClass: 'inferred', inputs: [], facts: [], wouldChange: [], fixes: [],
          reasons: ['Stale plan run.'], caveats: [], alternatives: [],
        }],
        stay: 0, skipped: [],
      },
      policy: {},
    }
    renderPanel(service())
    expect(screen.queryByTestId('recommended-highlight')).not.toBeInTheDocument()
    expect(screen.getByTestId('plan-bridge')).toHaveTextContent('it is not in this sweep')
  })

  test('a service the plan skipped shows the skip reason in the bridge slot', () => {
    setWorld('c-a', 'c-b')
    planResult = {
      world: { byCluster: new Map(), cap: { now: NOW } },
      plan: { recommendations: [], stay: 0, skipped: [{ serviceId: 'svc-1', serviceName: 'api', why: 'Its disruption budget allows no evictions right now.' }] },
      policy: {},
    }
    renderPanel(service())
    expect(screen.queryByTestId('recommended-highlight')).not.toBeInTheDocument()
    expect(screen.getByTestId('plan-bridge')).toHaveTextContent('Its disruption budget allows no evictions right now.')
  })

  test('a kind that never moves at all says so in the bridge slot', () => {
    setWorld('c-a', 'c-b')
    planResult = { world: { byCluster: new Map(), cap: { now: NOW } }, plan: { recommendations: [], stay: 0, skipped: [] }, policy: {} }
    renderPanel(service({ kind: 'DaemonSet' }))
    expect(screen.getByTestId('plan-bridge')).toHaveTextContent('A DaemonSet does not move.')
  })

  test('nothing recommended and nothing skipped: "nothing beats where it runs now", with a link to Placement', () => {
    setWorld('c-a', 'c-b')
    planResult = { world: { byCluster: new Map(), cap: { now: NOW } }, plan: { recommendations: [], stay: 0, skipped: [] }, policy: {} }
    renderPanel(service())
    const bridge = screen.getByTestId('plan-bridge')
    expect(bridge).toHaveTextContent('Nothing beats where it runs now')
    expect(within(bridge).getByText('Placement')).toBeInTheDocument()
  })
})

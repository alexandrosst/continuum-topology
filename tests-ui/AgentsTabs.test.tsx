import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import { useSort, SortTh } from '@/components/ui/sort'
import AgentsPage from '@/features/agents/AgentsPage'
import type { Agent, Cluster, Suggestion } from '@/lib/types'

const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge-1', tier: 'edge', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', accessTier: 2, name: 'edge-1', version: '1.0.0', modules: [], fingerprint: 'x', connected: true, lastHeartbeat: new Date().toISOString(), ...over }) as Agent
const sg = (over: Partial<Suggestion> = {}) => ({ id: 's1', kind: 'create-application', status: 'open', title: 'Group checkout services', detail: 'They share a label.', createdAt: '2026-01-01T00:00:00Z', ...over }) as Suggestion

let topology: { agents: Agent[]; clusters: Cluster[]; sites: never[]; suggestions: Suggestion[]; decideSuggestion: () => void; decideDerived: () => void; applyAlternative: () => void }
vi.mock('@/store/topology', () => ({
  useTopology: Object.assign(<T,>(sel?: (s: typeof topology) => T) => (sel ? sel(topology) : topology), {}),
  useRawTopology: <T,>(sel: (s: typeof topology) => T) => sel(topology),
}))
vi.mock('@/store/server', () => ({
  useServer: (sel?: (s: unknown) => unknown) => { const s = { status: 'connected', canEdit: () => true, state: { agents: topology.agents }, info: undefined, conn: () => undefined, refresh: async () => {} }; return sel ? sel(s) : s },
  useConn: () => undefined,
}))
vi.mock('@/features/discovery/components/ConnectFlow', () => ({ useConnectFlow: () => ({ start: vi.fn(), dialogs: null, canStart: true, wizardOpen: false }) }))
vi.mock('@/lib/usePlacement', () => ({ usePlacementSuggestions: () => ({ suggestions: [] }) }))
vi.mock('@/components/Observations', () => ({ ObservedClusters: () => <div data-testid="observed" />, GoneRecords: () => <div data-testid="gone" /> }))
vi.mock('@/components/GettingStarted', () => ({ default: () => null, useGettingStarted: () => undefined }))

function Where() {
  const l = useLocation()
  return <output data-testid="where">{l.pathname + l.search}</output>
}
function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/agents" element={<><AgentsPage /><Where /></>} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('Agents: Discovery is a tab', () => {
  test('the Suggestions tab carries the count and shows the inbox; Agents is the default', () => {
    topology = { agents: [ag()], clusters: [cl()], sites: [], suggestions: [sg()], decideSuggestion: vi.fn(), decideDerived: vi.fn(), applyAlternative: vi.fn() }
    renderAt('/agents')
    expect(screen.getByTestId('tab-agents')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('tab-suggestions')).toHaveTextContent('1')
    fireEvent.click(screen.getByTestId('tab-suggestions'))
    expect(screen.getByTestId('where')).toHaveTextContent('/agents?tab=suggestions')
    expect(screen.getByText('Group checkout services')).toBeInTheDocument()
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'agents-tab-suggestions')
  })

  test('a link with ?tab=seen opens what the agents have seen and lost', () => {
    topology = { agents: [], clusters: [], sites: [], suggestions: [], decideSuggestion: vi.fn(), decideDerived: vi.fn(), applyAlternative: vi.fn() }
    renderAt('/agents?tab=seen')
    expect(screen.getByTestId('observed')).toBeInTheDocument()
    expect(screen.getByTestId('gone')).toBeInTheDocument()
  })

  test('the agents table sorts by a column, and a third click goes back to the order it came in', () => {
    topology = { agents: [ag({ id: 'a1', name: 'beta' }), ag({ id: 'a2', name: 'alpha' }), ag({ id: 'a3', name: 'gamma' })], clusters: [cl()], sites: [], suggestions: [], decideSuggestion: vi.fn(), decideDerived: vi.fn(), applyAlternative: vi.fn() }
    renderAt('/agents')
    const names = () => screen.getAllByTestId('agent-row').map((r) => within(r).getAllByRole('cell')[0].textContent?.replace(/v1.*$/, ''))
    expect(names()).toEqual(['beta', 'alpha', 'gamma'])
    const head = screen.getByRole('columnheader', { name: /^Agent/ })
    fireEvent.click(within(head).getByRole('button'))
    expect(head).toHaveAttribute('aria-sort', 'ascending')
    expect(names()).toEqual(['alpha', 'beta', 'gamma'])
    fireEvent.click(within(head).getByRole('button'))
    expect(head).toHaveAttribute('aria-sort', 'descending')
    expect(names()).toEqual(['gamma', 'beta', 'alpha'])
    fireEvent.click(within(head).getByRole('button'))
    expect(head).toHaveAttribute('aria-sort', 'none')
    expect(names()).toEqual(['beta', 'alpha', 'gamma'])
  })
})

describe('useSort', () => {
  function Table({ rows }: { rows: { n: string; v?: number }[] }) {
    const by = { n: (r: { n: string }) => r.n, v: (r: { v?: number }) => r.v }
    const { sorted, sort, toggle } = useSort(rows, by)
    return (
      <table>
        <thead><tr><SortTh id="n" sort={sort} onSort={toggle}>N</SortTh><SortTh id="v" sort={sort} onSort={toggle}>V</SortTh></tr></thead>
        <tbody>{sorted.map((r) => <tr key={r.n}><td>{r.n}</td></tr>)}</tbody>
      </table>
    )
  }
  test('a missing value goes last whichever way it sorts, and numbers sort as numbers', () => {
    render(<Table rows={[{ n: 'a' }, { n: 'b', v: 10 }, { n: 'c', v: 9 }]} />)
    const order = () => screen.getAllByRole('row').slice(1).map((r) => r.textContent)
    fireEvent.click(screen.getByRole('button', { name: 'V' }))
    expect(order()).toEqual(['c', 'b', 'a'])
    fireEvent.click(screen.getByRole('button', { name: 'V' }))
    expect(order()).toEqual(['b', 'c', 'a'])
  })
})

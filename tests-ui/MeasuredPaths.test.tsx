import { render, screen, within } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import MeasuredPaths from '@/components/MeasuredPaths'
import type { Path } from '@/lib/types'

// Only the table is exercised: the connection is reported as not connected, which leaves out the form for
// naming targets and with it everything that needs a server.
let pathsState: Path[] = []

vi.mock('@/store/topology', () => ({
  usePaths: () => pathsState,
  useTopology: () => ({ agents: [], clusters: [] }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { status: string; role: string }) => unknown) => selector({ status: 'idle', role: 'viewer' }),
  useConn: () => ({ url: '', org: 'o' }),
}))
vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings: { probeTargets: [] }, save: vi.fn(), error: undefined }),
}))

const path = (id: string, extra: Partial<Path> = {}): Path => ({
  id, fromCluster: 'c1', fromName: 'edge-1', host: `10.0.0.${id}`, port: 443, source: 'observed',
  rttMinMs: 8, rttP50Ms: 12, rttP95Ms: 19, lossPct: 0, samples: 6, at: '2026-10-01T00:00:00Z', ...extra,
})

describe('MeasuredPaths', () => {
  test('a path where every attempt failed says so, instead of showing 0 ms', () => {
    pathsState = [
      path('1', { rttMinMs: 0, rttP50Ms: 0, rttP95Ms: 0, lossPct: 100 }),
      path('2'),
    ]
    render(<MeasuredPaths />)
    const rows = screen.getAllByTestId('path-row')
    expect(rows).toHaveLength(2)
    // The live path comes first, the dead one last.
    expect(within(rows[0]).getByText('12 ms')).toBeInTheDocument()
    expect(within(rows[1]).getByTestId('path-no-roundtrip')).toHaveTextContent('no connection completed')
    expect(within(rows[1]).getByText('100 %')).toBeInTheDocument()
    expect(within(rows[1]).queryByText(/0\.0 ms/)).toBeNull()
    expect(within(rows[1]).queryByText(/^0 ms$/)).toBeNull()
  })

  test('a path nothing has been tried on yet says it is not measured', () => {
    pathsState = [path('3', { rttMinMs: 0, rttP50Ms: 0, rttP95Ms: 0, samples: 0 })]
    render(<MeasuredPaths />)
    expect(screen.getByTestId('path-no-roundtrip')).toHaveTextContent('not measured yet')
  })
})

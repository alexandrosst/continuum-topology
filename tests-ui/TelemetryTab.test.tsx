import { fireEvent, render, screen, within } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import TelemetryTab from '@/components/topology/TelemetryTab'
import type { PlatformEntity, PlatformModel } from '@/lib/platformLayer'

let model: PlatformModel | undefined
vi.mock('@/components/topology/usePlatformLayer', () => ({ usePlatformLayer: () => model }))

const part = (id: string, kind: PlatformEntity['kind'], over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id, kind, name: kind, detail: '', status: 'healthy', sentence: '', sendsTo: [], ...over })
const hop = (from: string, to: string, status: PlatformEntity['status'] = 'healthy', age?: string) => ({ id: `${from}>${to}`, from, to, status, age })

const twoLanes = (): PlatformModel => ({
  entities: [
    part('agent:a', 'agent', { name: 'Discovery agent', clusterId: 'a', clusterName: 'apple', agentId: 'a' }),
    part('local:a', 'local', { name: 'Local operator', clusterId: 'a', clusterName: 'apple', agentId: 'a' }),
    part('agent:b', 'agent', { name: 'Discovery agent', clusterId: 'b', clusterName: 'banana', agentId: 'b' }),
    part('local:b', 'local', { name: 'Local operator', clusterId: 'b', clusterName: 'banana', agentId: 'b', status: 'attention' }),
    part('regional:eu', 'regional', { name: 'eu' }),
  ],
  edges: [hop('agent:a', 'local:a', 'healthy', '20 s'), hop('agent:b', 'local:b', 'healthy', '20 s'), hop('local:a', 'regional:eu', 'healthy', '2 s'), hop('local:b', 'regional:eu', 'attention', '14 min')],
})

function renderTab(over: Partial<React.ComponentProps<typeof TelemetryTab>> = {}) {
  const props = { clusters: [], agents: [], problemsOnly: false, selection: null, onSelect: vi.fn(), onShowAll: vi.fn(), onConnect: vi.fn(), onClose: vi.fn(), ...over }
  render(<MemoryRouter><TelemetryTab {...props} /></MemoryRouter>)
  return props
}

beforeEach(() => {
  model = twoLanes()
})

describe('TelemetryTab', () => {
  test('draws one lane per cluster, the stages as column headers, and the shared operator once', () => {
    renderTab()
    expect(screen.getAllByTestId('lane').map((l) => l.textContent)).toEqual(['apple', 'banana'])
    expect(screen.getByText('Regional operator')).toBeInTheDocument()
    expect(screen.getAllByTestId('platform-node').filter((n) => n.getAttribute('data-platform') === 'regional')).toHaveLength(1)
  })

  test('a healthy hop flows and says nothing; one that stalled is still and says how long ago data last arrived', () => {
    renderTab()
    const hops = screen.getAllByTestId('hop')
    expect(hops.filter((h) => h.getAttribute('data-flowing') === 'true')).toHaveLength(3)
    expect(hops.filter((h) => h.getAttribute('data-status') === 'attention' && h.getAttribute('data-flowing') === 'false')).toHaveLength(1)
    expect(hops.filter((h) => h.getAttribute('data-flowing') === 'true').every((h) => h.querySelector('path')?.getAttribute('class')?.includes('flow-dash'))).toBe(true)
    expect(hops.find((h) => h.getAttribute('data-status') === 'attention')!.querySelector('path')?.getAttribute('class')).not.toContain('flow-dash')
    expect(screen.getAllByTestId('hop-age').map((a) => a.textContent)).toEqual(['14 min'])
  })

  test('the age of a healthy hop shows while its sender is focused, and goes again', () => {
    renderTab()
    const local = screen.getByRole('button', { name: /^Local operator, apple/ })
    fireEvent.focus(local)
    expect(screen.getAllByTestId('hop-age').map((a) => a.textContent)).toEqual(['2 s', '14 min'])
    fireEvent.blur(local)
    expect(screen.getAllByTestId('hop-age').map((a) => a.textContent)).toEqual(['14 min'])
  })

  test('every part is a button named with its state and its last data, and a click selects it as a platform part', () => {
    const { onSelect } = renderTab()
    const node = screen.getByRole('button', { name: 'Local operator, banana: Needs attention, last data 14 min' })
    fireEvent.click(node)
    expect(onSelect).toHaveBeenCalledWith({ kind: 'platform', id: 'local:b' })
  })

  test('the selected part is marked pressed', () => {
    renderTab({ selection: { kind: 'platform', id: 'regional:eu' } })
    expect(within(screen.getByTestId('telemetry-lanes')).getByRole('button', { pressed: true }).getAttribute('data-platform')).toBe('regional')
  })

  test('no cluster with an agent: points to connecting one', () => {
    model = { entities: [], edges: [] }
    const { onConnect } = renderTab()
    fireEvent.click(screen.getByTestId('telemetry-connect'))
    expect(onConnect).toHaveBeenCalledOnce()
    expect(screen.queryByTestId('telemetry-lanes')).toBeNull()
  })

  test('problems only with nothing wrong says so and offers every cluster again', () => {
    model = { entities: twoLanes().entities.map((e) => ({ ...e, status: 'healthy' as const })), edges: [] }
    const { onShowAll } = renderTab({ problemsOnly: true })
    expect(screen.getByText('No problems')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('show-all-lanes'))
    expect(onShowAll).toHaveBeenCalledOnce()
  })

  test('problems only leaves out the healthy lane', () => {
    renderTab({ problemsOnly: true })
    expect(screen.getAllByTestId('lane').map((l) => l.textContent)).toEqual(['banana'])
  })

  test('until what the person may read has arrived there are no lanes to draw', () => {
    model = undefined
    renderTab()
    expect(screen.queryByTestId('telemetry-lanes')).toBeNull()
    expect(screen.queryByText('No cluster sends telemetry yet')).toBeNull()
  })
})

describe('flow animation', () => {
  const css = readFileSync('src/index.css', 'utf8')

  test('is CSS only, slow, and switched off for people who ask for less motion', () => {
    expect(css).toMatch(/\.flow-dash\s*\{[^}]*animation:\s*flow-dash 1\.6s linear infinite/)
    const reduced = css.split('@media (prefers-reduced-motion: reduce)').slice(1).join('\n')
    expect(reduced).toMatch(/\.flow-dash[^{]*\{\s*animation:\s*none;?\s*\}/)
  })
})

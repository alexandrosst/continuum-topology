import { render } from '@testing-library/react'
import { ReactFlowProvider } from '@xyflow/react'
import { describe, expect, test } from 'vitest'
import { Card, GroupBox } from '@/components/topology/nodes'
import { DetailContext, type Detail } from '@/lib/detail'
import type { CardData, GroupData } from '@/lib/graph'

const card = (over: Partial<CardData> = {}): CardData => ({
  kind: 'service', entityId: 's1', title: 'metrics-api', subtitle: 'analytics · Deployment', meta: '×3', status: 'healthy', tier: 'cloud', clusterName: 'c1', ...over,
}) as CardData
const group = (over: Partial<GroupData> = {}): GroupData => ({
  entityId: 'c1', title: 'sunrise-cluster', subtitle: 'GKE · Frankfurt', stats: '3 services', status: 'healthy', tier: 'cloud', ...over,
}) as GroupData

function renderCard(data: CardData, detail: Detail, selected = false) {
  const props = { id: 'c:s1', data, selected, type: 'card', dragging: false, zIndex: 0, isConnectable: false, positionAbsoluteX: 0, positionAbsoluteY: 0 } as never
  return render(<ReactFlowProvider><DetailContext.Provider value={detail}><Card {...props} /></DetailContext.Provider></ReactFlowProvider>)
}
function renderGroup(data: GroupData, detail: Detail, selected = false) {
  const props = { id: 'g:c1', data, selected, type: 'boundary', dragging: false, zIndex: 0, isConnectable: false, positionAbsoluteX: 0, positionAbsoluteY: 0 } as never
  return render(<ReactFlowProvider><DetailContext.Provider value={detail}><GroupBox {...props} /></DetailContext.Provider></ReactFlowProvider>)
}

/** What a hover would reveal: the parts of the card that are not drawn at rest. */
const waiting = (c: HTMLElement) => [...c.querySelectorAll('.hidden')].map((e) => e.textContent ?? '')

describe('Calm card', () => {
  const pods = { total: 3, ready: 3, warn: 0, bad: 0, nodes: 1, rail: [{ id: 'a', label: 'a', state: 'ready', recent: false, restarts: 0, age: '3d', title: 'a' }], overflow: 0, groups: [] } as unknown as CardData['pods']

  test('a card that is fine is its name and its status: the kind, namespace and replicas are the Inspector\'s', () => {
    const { container, getByTitle } = renderCard(card(), 'calm')
    expect(container.textContent).toContain('metrics-api')
    expect(getByTitle('healthy')).toBeInTheDocument()
    expect(container.querySelector('[data-alert]')).toBeNull()
    expect(container.textContent).not.toContain('analytics')
    expect(container.textContent).not.toContain('×3')
  })

  test('the pods of a service wait for hover, focus, selection and a close zoom', () => {
    const { container } = renderCard(card({ pods }), 'calm')
    expect(waiting(container).length).toBe(1)
    expect(waiting(container)[0]).toContain('3/3')
  })

  test('a card with a problem says what is wrong in one line, in the state colour, and says so for tests and assistive technology', () => {
    const { container, getByTestId } = renderCard(card({ alert: 'bad', status: 'offline', note: '1 crash-looping · 1 not ready', pods }), 'calm')
    expect(container.querySelector('[data-alert="bad"]')).not.toBeNull()
    expect(getByTestId('card-note')).toHaveTextContent('1 crash-looping · 1 not ready')
    // the rail is still the hover's: the line is the tiny signal
    expect(waiting(container).length).toBe(1)
  })

  test('selection draws the pods', () => {
    const { container } = renderCard(card({ pods }), 'calm', true)
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('3/3')
  })

  test('a machine card that lists its services is something the person asked to see, so it stays up', () => {
    const { container } = renderCard(card({ kind: 'machine', chips: [{ id: 'a', name: 'api' }] }), 'calm')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('api')
  })

  test('a device group keeps its count, which is what identifies it', () => {
    const { container } = renderCard(card({ kind: 'device', title: 'Quay cameras', meta: '×36' }), 'calm')
    expect(container.textContent).toContain('×36')
  })

  test('Full draws the card as it always was: nothing waits for a hover', () => {
    const { container } = renderCard(card({ notReady: '1/3 ready' }), 'full')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('analytics · Deployment')
    expect(container.textContent).toContain('1/3 ready')
    expect(container.querySelector('.group\\/card')).toBeNull()
  })
})

describe('Calm cluster box', () => {
  test('the header is the name, the status and where it is; the tier is a glyph with its name, not a text chip', () => {
    const { container, getByRole } = renderGroup(group({ place: 'Frankfurt, Germany' }), 'calm')
    expect(container.textContent).toContain('sunrise-cluster')
    expect(container.textContent).toContain('Frankfurt, Germany')
    expect(getByRole('img', { name: 'Cloud tier' })).toBeInTheDocument()
    expect(container.textContent).not.toContain('Cloud')
    // the distribution, the count and the load bars are the Inspector's
    expect(container.textContent).not.toContain('GKE')
    expect(container.textContent).not.toContain('3 services')
    expect(waiting(container)).toEqual([])
  })

  test('a cluster in trouble says so in one line and tints its border; the bars are not drawn', () => {
    const load = { cpuPct: 92, memPct: 40, podPct: 10, nodes: 2, ready: 2, unready: 0, services: 3, unreadyServices: 0 }
    const { container, getByTestId } = renderGroup(group({ alert: 'bad', note: 'CPU 92% requested', load } as Partial<GroupData>), 'calm')
    expect(getByTestId('box-note')).toHaveTextContent('CPU 92% requested')
    expect(container.querySelector('[data-testid="cluster-load"]')).toBeNull()
  })

  test('the local-telemetry control is the Inspector\'s in Calm and stays on the box in Full', () => {
    const localTelemetry = { layers: ['infrastructure'] }
    expect(renderGroup(group({ localTelemetry }), 'calm').container.querySelector('[data-testid="local-telemetry-badge"]')).toBeNull()
    expect(renderGroup(group({ localTelemetry }), 'full').container.querySelector('[data-testid="local-telemetry-badge"]')).not.toBeNull()
  })

  test('Full keeps every line on show', () => {
    const { container } = renderGroup(group(), 'full')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('GKE · Frankfurt')
    expect(container.textContent).toContain('Cloud')
  })
})

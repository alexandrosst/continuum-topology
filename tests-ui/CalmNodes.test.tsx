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
  test('a card that is fine draws its name and status, and keeps the line under it, its badges and its pods for hover', () => {
    const { container, getByTitle } = renderCard(card({ notReady: undefined }), 'calm')
    expect(container.textContent).toContain('metrics-api')
    expect(getByTitle('healthy')).toBeInTheDocument()
    expect(container.querySelector('[data-alert]')).toBeNull()
    expect(waiting(container).join(' ')).toContain('analytics · Deployment')
  })

  test('a card with a problem keeps everything on show, in the state colour, and says so for tests and assistive technology', () => {
    const { container } = renderCard(card({ alert: 'bad', status: 'offline' }), 'calm')
    expect(container.querySelector('[data-alert="bad"]')).not.toBeNull()
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('analytics · Deployment')
  })

  test('selection keeps the card up as a problem does', () => {
    const { container } = renderCard(card(), 'calm', true)
    expect(waiting(container)).toEqual([])
  })

  test('a machine card that lists its services is something the person asked to see, so it stays up', () => {
    const { container } = renderCard(card({ kind: 'machine', chips: [{ id: 'a', name: 'api' }] }), 'calm')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('api')
  })

  test('Full draws the card as it always was: nothing waits for a hover', () => {
    const { container } = renderCard(card(), 'full')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('analytics · Deployment')
    expect(container.querySelector('.group\\/card')).toBeNull()
  })
})

describe('Calm cluster box', () => {
  test('the header is the name, the status and the tier; the lines under it wait for hover', () => {
    const { container } = renderGroup(group(), 'calm')
    expect(container.textContent).toContain('sunrise-cluster')
    expect(container.textContent).toContain('Cloud')
    const quiet = waiting(container).join(' ')
    expect(quiet).toContain('GKE · Frankfurt')
    expect(quiet).toContain('3 services')
  })

  test('a cluster under pressure keeps its bars up and its border in the state colour', () => {
    const load = { cpuPct: 92, memPct: 40, podPct: 10, nodes: 2, ready: 2, unready: 0, services: 3, unreadyServices: 0 }
    const { container } = renderGroup(group({ alert: 'bad', load } as Partial<GroupData>), 'calm')
    expect(container.querySelector('[data-testid]')).not.toBeNull()
    expect(waiting(container).join(' ')).not.toContain('CPU')
  })

  test('Full keeps every line on show', () => {
    const { container } = renderGroup(group(), 'full')
    expect(waiting(container)).toEqual([])
    expect(container.textContent).toContain('GKE · Frankfurt')
  })
})

import { ReactFlowProvider } from '@xyflow/react'
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { Card } from '@/features/topology/components/nodes'
import type { CardData } from '@/features/topology/lib/graph'

const card = (over: Partial<CardData>): CardData => ({
  kind: 'service', entityId: 's', title: 'api', subtitle: 'core · Deployment', meta: '×4', status: 'healthy', tier: 'cloud', clusterName: 'c', ...over,
})
const show = (data: CardData) =>
  render(
    <ReactFlowProvider>
      <Card {...({ data, selected: false } as never)} />
    </ReactFlowProvider>,
  )

describe('a service card\'s replicas', () => {
  test('are one cell each, amber for those not ready', () => {
    const { container } = show(card({ replicas: { total: 4, ready: 3 } }))
    expect(screen.getByTitle('3 of 4 replicas ready')).toBeTruthy()
    expect(container.querySelectorAll('.bg-ok\\/80')).toHaveLength(3)
    expect(container.querySelectorAll('.bg-warn')).toHaveLength(1)
    // The count stays available to screen readers.
    expect(screen.getByText('×4').className).toContain('sr-only')
  })

  test('fall back to the plain count past eight', () => {
    const { container } = show(card({ meta: '×12', replicas: { total: 12, ready: 12 } }))
    expect(screen.getByText('×12')).toBeTruthy()
    expect(container.querySelectorAll('.bg-ok\\/80')).toHaveLength(0)
  })

  test('an unhealthy card carries a severity class, a healthy one does not', () => {
    const { container, rerender } = show(card({ status: 'offline' }))
    expect(container.querySelector('.topo-sev-bad')).not.toBeNull()
    rerender(<ReactFlowProvider><Card {...({ data: card({ status: 'degraded' }), selected: false } as never)} /></ReactFlowProvider>)
    expect(container.querySelector('.topo-sev-warn')).not.toBeNull()
    rerender(<ReactFlowProvider><Card {...({ data: card({}), selected: false } as never)} /></ReactFlowProvider>)
    expect(container.querySelector('.topo-sev-bad, .topo-sev-warn')).toBeNull()
  })
})

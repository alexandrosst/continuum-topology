import { ReactFlow, ReactFlowProvider } from '@xyflow/react'
import { render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { PlatformNode, StatusGlyph } from '@/components/topology/PlatformNode'
import { PLATFORM_STATUS_WORD, type PlatformStatus } from '@/lib/platformLayer'
import { platformNode } from '@/lib/platformLayerGraph'
import type { PlatformEntity } from '@/lib/platformLayer'

class NoObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal('ResizeObserver', NoObserver)

const entity = (over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id: 'local:a', kind: 'local', name: 'Local operator', detail: 'Metrics', status: 'healthy', sentence: '', sendsTo: [], ...over })

function renderNode(e: PlatformEntity) {
  const n = platformNode(e, 'edge', { x: 0, y: 0 })
  render(
    <ReactFlowProvider>
      <div style={{ width: 600, height: 300 }}>
        <ReactFlow nodes={[n]} edges={[]} nodeTypes={{ platform: PlatformNode }} fitView />
      </div>
    </ReactFlowProvider>,
  )
}

describe('PlatformNode', () => {
  test('is a node in the same language as the cards: title, the line under it, and its state as a glyph with a name', () => {
    renderNode(entity())
    const node = screen.getByTestId('platform-node')
    expect(node).toHaveAttribute('data-platform', 'local')
    expect(node.textContent).toContain('Local operator')
    expect(node.textContent).toContain('Metrics')
    expect(node.querySelector('svg[data-status="healthy"]')?.getAttribute('aria-label')).toBe('Healthy')
  })

  test('anything but Healthy is written under the name as well as drawn', () => {
    renderNode(entity({ status: 'attention' }))
    expect(screen.getByTestId('platform-node').textContent).toContain('Needs attention')
    expect(screen.getByTestId('platform-node').querySelector('svg[data-status="attention"]')?.getAttribute('aria-label')).toBe('Needs attention')
  })
})

describe('StatusGlyph', () => {
  test('each of the four states has its own shape and its word as the name', () => {
    const states: PlatformStatus[] = ['healthy', 'attention', 'down', 'unknown']
    const { container } = render(<>{states.map((s) => <StatusGlyph key={s} status={s} />)}</>)
    const names = Array.from(container.querySelectorAll('svg')).map((el) => el.getAttribute('aria-label'))
    expect(names).toEqual(states.map((s) => PLATFORM_STATUS_WORD[s]))
    expect(new Set(Array.from(container.querySelectorAll('svg')).map((el) => el.getAttribute('class')?.match(/lucide-[a-z-]+/)?.[0])).size).toBe(4)
  })
})

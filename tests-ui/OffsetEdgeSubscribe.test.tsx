import * as flow from '@xyflow/react'
import { render } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { OffsetEdge, SquareRoutesContext } from '@/features/topology/components/OffsetEdge'

// A spy over the real store hook: the point is which edges subscribe to the store at all.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return { ...actual, useStore: vi.fn(actual.useStore), useViewport: vi.fn(actual.useViewport) }
})

const props = { id: 'e1', source: 'a', target: 'b', sourceX: 0, sourceY: 0, targetX: 100, targetY: 0 } as never

describe('OffsetEdge subscriptions', () => {
  test('a routed (squared) line reads only the context: no store subscription, so a pan or a drag does not touch it', () => {
    vi.mocked(flow.useStore).mockClear()
    vi.mocked(flow.useViewport).mockClear()
    const { container } = render(
      <flow.ReactFlowProvider>
        <SquareRoutesContext.Provider value={new Map([['e1', { path: 'M0 0L10 0', labelX: 5, labelY: 0 }]])}>
          <svg><OffsetEdge {...props} /></svg>
        </SquareRoutesContext.Provider>
      </flow.ReactFlowProvider>,
    )
    expect(container.querySelector('path')?.getAttribute('d')).toBe('M0 0L10 0')
    expect(flow.useStore).not.toHaveBeenCalled()
    expect(flow.useViewport).not.toHaveBeenCalled()
  })

  test('a line SquareRoutes has not laid out yet falls back to its own routing from the store, and never reads the pan', () => {
    vi.mocked(flow.useStore).mockClear()
    vi.mocked(flow.useViewport).mockClear()
    const { container } = render(
      <flow.ReactFlowProvider>
        <SquareRoutesContext.Provider value={new Map()}>
          <svg><OffsetEdge {...props} /></svg>
        </SquareRoutesContext.Provider>
      </flow.ReactFlowProvider>,
    )
    expect(container.querySelector('path')?.getAttribute('d')).toBeTruthy()
    expect(flow.useStore).toHaveBeenCalled()
    expect(flow.useViewport).not.toHaveBeenCalled()
  })
})

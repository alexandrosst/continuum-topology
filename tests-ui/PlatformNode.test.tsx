import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { PlatformNode, StatusGlyph } from '@/components/topology/PlatformNode'
import { PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformStatus } from '@/lib/platformLayer'

const entity = (over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id: 'local:a', kind: 'local', name: 'Local operator', detail: 'Metrics', status: 'healthy', sentence: '', sendsTo: [], ...over })

function renderNode(e: PlatformEntity, onClick = () => {}) {
  render(<PlatformNode entity={e} label={`${e.name}: label`} onClick={onClick} />)
  return screen.getByTestId('platform-node')
}

describe('PlatformNode', () => {
  test('is a node in the same language as the cards: title, the line under it, and its state as a glyph with a name', () => {
    const node = renderNode(entity())
    expect(node).toHaveAttribute('data-platform', 'local')
    expect(node.textContent).toContain('Local operator')
    expect(node.textContent).toContain('Metrics')
    expect(node.querySelector('svg[data-status="healthy"]')?.getAttribute('aria-label')).toBe('Healthy')
  })

  test('anything but Healthy is written under the name as well as drawn', () => {
    const node = renderNode(entity({ status: 'attention' }))
    expect(node.textContent).toContain('Needs attention')
    expect(node.querySelector('svg[data-status="attention"]')?.getAttribute('aria-label')).toBe('Needs attention')
  })

  test('a part that is not turned on says so, quietly, instead of Unknown', () => {
    const node = renderNode(entity({ kind: 'fusion', name: 'FUSION', detail: 'Not turned on', status: 'unknown', off: true }))
    expect(node.textContent).toContain('Not turned on')
    expect(node.textContent).not.toContain('Unknown')
    expect(node.className).toContain('border-dashed')
  })

  test('is a button with the name it is given, and a click selects it', () => {
    const onClick = vi.fn()
    renderNode(entity(), onClick)
    const button = screen.getByRole('button', { name: 'Local operator: label' })
    fireEvent.click(button)
    expect(onClick).toHaveBeenCalledOnce()
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

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { PlatformNode, StatusGlyph } from '@/components/topology/PlatformNode'
import { PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformStatus } from '@/lib/platformLayer'

const entity = (over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id: 'local:a', kind: 'local', name: 'Local operator', detail: 'Metrics', status: 'healthy', sentence: 'Sending, last data 2 s ago.', collecting: ['metrics', 'logs'], sendsTo: [{ id: 'regional:eu', name: 'eu-west' }], clusterName: 'apple', ...over })

function renderNode(e: PlatformEntity, props: Partial<React.ComponentProps<typeof PlatformNode>> = {}) {
  render(<PlatformNode entity={e} label={`${e.name}: label`} onClick={() => {}} {...props} />)
  return screen.getByTestId('platform-node')
}

describe('PlatformNode', () => {
  test('in a column it leads with what is specific to it, not with its role: an agent its version, a local operator what it collects as glyphs', () => {
    const agent = renderNode(entity({ kind: 'agent', name: 'Discovery agent', detail: 'v0.19.3', collecting: undefined }))
    expect(agent.textContent).toBe('v0.19.3')
    expect(agent.textContent).not.toContain('Discovery agent')
    expect(agent.querySelector('[role="img"][data-status="healthy"]')?.getAttribute('aria-label')).toBe('Healthy')
  })

  test('what a local operator collects is two glyphs, never a sentence that has to be cut short', () => {
    const node = renderNode(entity())
    expect(node.textContent).toBe('')
    expect(node.querySelectorAll('[data-testid="signals"] svg')).toHaveLength(2)
    expect(node.getAttribute('aria-label')).toBe('Local operator: label')
  })

  test('a shared operator is its name, with how many send to it under it', () => {
    expect(renderNode(entity({ id: 'regional:eu', kind: 'regional', name: 'eu-west', detail: 'Regional operator', sendsTo: [] }), { senders: 3 }).textContent).toBe('eu-west3 clusters')
  })

  test('anything but Healthy takes the line under the name, in words, and tints the box; Healthy is only the glyph', () => {
    const node = renderNode(entity({ status: 'attention' }))
    expect(node.textContent).toBe('Needs attention')
    expect(node.className).toContain('border-warn')
    expect(node.querySelector('[role="img"][data-status="attention"]')?.getAttribute('aria-label')).toBe('Needs attention')
  })

  test('in a list there is no column header, so the role leads, and where it sends to follows', () => {
    const node = renderNode(entity(), { withRole: true })
    expect(node.textContent).toBe('Local operatorMetrics, Logs → eu-west')
  })

  test('a part that is not turned on says so, quietly, instead of Unknown, and has no state glyph', () => {
    const node = renderNode(entity({ kind: 'fusion', name: 'FUSION', detail: 'Not turned on', status: 'unknown', off: true, collecting: undefined, sendsTo: [] }))
    expect(node.textContent).toBe('FUSIONOff')
    expect(node.textContent).not.toContain('Unknown')
    expect(node.className).toContain('border-dashed')
    expect(node.querySelector('svg[data-status]')).toBeNull()
  })

  test('is a button with the name it is given, and a click selects it', () => {
    const onClick = vi.fn()
    renderNode(entity(), { onClick })
    fireEvent.click(screen.getByRole('button', { name: 'Local operator: label' }))
    expect(onClick).toHaveBeenCalledOnce()
  })

  test('its tooltip says what it is, where it runs and what its state means, for the pointer and the keyboard, and is not read out twice', () => {
    renderNode(entity({ status: 'attention', sentence: 'Quiet: no data for 14 min.', todo: 'Check what it collects.' }), { tip: true })
    const tip = screen.getByText('Quiet: no data for 14 min.').parentElement!
    expect(tip.textContent).toContain('Local operator · apple')
    expect(tip.textContent).toContain('Check what it collects.')
    expect(tip.getAttribute('aria-hidden')).toBe('true')
    expect(tip.className).toContain('group-hover/node:opacity-100')
    expect(tip.className).toContain('group-has-[:focus-visible]/node:opacity-100')
  })
})

describe('StatusGlyph', () => {
  test('each of the four states has its own shape and its word as the name', () => {
    const states: PlatformStatus[] = ['healthy', 'attention', 'down', 'unknown']
    const { container } = render(<>{states.map((s) => <StatusGlyph key={s} status={s} />)}</>)
    const marks = Array.from(container.querySelectorAll('[role="img"]'))
    expect(marks.map((el) => el.getAttribute('aria-label'))).toEqual(states.map((s) => PLATFORM_STATUS_WORD[s]))
    // The same marks as the canvas draws (StatusMark), each with its own shape.
    expect(marks.map((el) => el.getAttribute('data-status'))).toEqual(states)
    expect(new Set(marks.map((el) => el.innerHTML)).size).toBe(4)
  })
})

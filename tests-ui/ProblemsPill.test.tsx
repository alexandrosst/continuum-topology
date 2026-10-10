import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { CanvasBar, ProblemsPill } from '@/components/topology/ProblemsPill'
import { nextProblem, problemsOf, type Problem } from '@/lib/problems'
import type { TopoNode } from '@/lib/graph'

const node = (id: string, x: number, y: number, alert?: 'warn' | 'bad', parentId?: string) => ({ id, parentId, position: { x, y }, data: { alert } }) as unknown as TopoNode

describe('problemsOf', () => {
  test('is exactly the nodes that carry an alert, worst first, then top to bottom and left to right, in absolute positions', () => {
    const nodes = [node('g', 0, 500), node('a', 300, 10, 'warn', 'g'), node('b', 10, 10, 'warn', 'g'), node('top', 900, 0, 'warn'), node('boom', 0, 900, 'bad'), node('fine', 0, 0)]
    expect(problemsOf(nodes).map((p) => p.id)).toEqual(['boom', 'top', 'b', 'a'])
  })
  test('counts the innermost place: a box whose cards show the trouble is a frame, not a second problem, unless it is worse than they are', () => {
    const nodes = [node('box', 0, 0, 'warn'), node('card', 10, 10, 'warn', 'box'), node('lone', 500, 0, 'bad'), node('inner', 10, 10, 'warn', 'lone'), node('same', 900, 0, 'bad'), node('hurt', 10, 10, 'bad', 'same')]
    expect(problemsOf(nodes).map((p) => p.id)).toEqual(['lone', 'hurt', 'card', 'inner'])
  })
  test('is empty when nothing is wrong', () => {
    expect(problemsOf([node('a', 0, 0), node('b', 1, 1)])).toEqual([])
  })
})

describe('nextProblem', () => {
  const ps: Problem[] = [{ id: 'a', alert: 'bad' }, { id: 'b', alert: 'warn' }, { id: 'c', alert: 'warn' }]
  test('goes forward and wraps; the first when nothing in the list is selected', () => {
    expect(nextProblem(ps, null)?.id).toBe('a')
    expect(nextProblem(ps, 'zzz')?.id).toBe('a')
    expect(nextProblem(ps, 'a')?.id).toBe('b')
    expect(nextProblem(ps, 'c')?.id).toBe('a')
  })
  test('goes back and wraps; the last when nothing in the list is selected', () => {
    expect(nextProblem(ps, null, true)?.id).toBe('c')
    expect(nextProblem(ps, 'a', true)?.id).toBe('c')
    expect(nextProblem(ps, 'c', true)?.id).toBe('b')
  })
  test('is nothing when there is nothing', () => {
    expect(nextProblem([], 'a')).toBeUndefined()
  })
})

describe('ProblemsPill', () => {
  const ps: Problem[] = [{ id: 'a', alert: 'bad' }, { id: 'b', alert: 'warn' }, { id: 'c', alert: 'warn' }]

  test('draws nothing when nothing needs attention', () => {
    const { container } = render(<ProblemsPill problems={[]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('says how many, with an accessible name, and singular for one', () => {
    const { rerender } = render(<ProblemsPill problems={ps} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByRole('button', { name: /^3 places need attention/ })).toBeTruthy()
    rerender(<ProblemsPill problems={[ps[0]]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByRole('button', { name: /^1 place needs attention/ })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Previous problem' })).toBeNull()
  })

  test('says what it counts: the noun the problems share, else places', () => {
    const svc = (id: string, alert: Problem['alert']): Problem => ({ id, alert, noun: 'service' })
    const { rerender } = render(<ProblemsPill problems={[svc('a', 'bad'), svc('b', 'warn'), svc('c', 'warn')]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByRole('button', { name: /^3 services need attention/ })).toBeTruthy()
    rerender(<ProblemsPill problems={[{ id: 'a', alert: 'bad', noun: 'cluster' }]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByRole('button', { name: /^1 cluster needs attention/ }).getAttribute('title')).toContain('Counts the clusters in this layer')
    rerender(<ProblemsPill problems={[svc('a', 'bad'), { id: 'b', alert: 'warn', noun: 'cluster' }]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByRole('button', { name: /^2 places need attention/ })).toBeTruthy()
  })

  test('the label shows the problem, the chevrons walk to the next and previous, and the position shows once one is selected', () => {
    const onGo = vi.fn()
    const onShow = vi.fn()
    render(<ProblemsPill problems={ps} selectedId="b" onGo={onGo} onShow={onShow} />)
    expect(screen.getByText('2 of 3 places need attention')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /need attention/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Next problem' }))
    fireEvent.click(screen.getByRole('button', { name: 'Previous problem' }))
    expect(onShow).toHaveBeenCalledTimes(1)
    expect(onGo.mock.calls).toEqual([[false], [true]])
  })

  test('is tinted by the worst of it: red when anything is broken, amber when it only needs a look', () => {
    const { rerender } = render(<ProblemsPill problems={ps} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByTestId('problems-pill')).toHaveAttribute('data-worst', 'bad')
    rerender(<ProblemsPill problems={ps.slice(1)} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByTestId('problems-pill')).toHaveAttribute('data-worst', 'warn')
  })
})

describe('CanvasBar', () => {
  test('answers "is anything wrong?" before anything else: the pill when something is, "No problems" with a Healthy glyph when not', () => {
    const { rerender } = render(<CanvasBar problems={[]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    const bar = screen.getByTestId('canvas-status')
    expect(bar).toHaveTextContent('No problems')
    expect(bar.querySelector('[role="img"][data-status="healthy"]')).not.toBeNull()
    rerender(<CanvasBar problems={[{ id: 'a', alert: 'bad', noun: 'service' }, { id: 'b', alert: 'warn', noun: 'service' }]} selectedId={null} onGo={() => {}} onShow={() => {}} />)
    expect(screen.getByTestId('problems-pill')).toHaveTextContent('2 services need attention')
    expect(screen.getByTestId('canvas-status')).toHaveTextContent('Shift N')
  })
})

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { ProblemsPill } from '@/components/topology/ProblemsPill'
import { nextProblem, problemsOf, type Problem } from '@/lib/problems'
import type { TopoNode } from '@/lib/graph'

const node = (id: string, x: number, y: number, alert?: 'warn' | 'bad', parentId?: string) => ({ id, parentId, position: { x, y }, data: { alert } }) as unknown as TopoNode

describe('problemsOf', () => {
  test('is exactly the nodes that carry an alert, worst first, then top to bottom and left to right, in absolute positions', () => {
    const nodes = [node('g', 0, 500), node('a', 300, 10, 'warn', 'g'), node('b', 10, 10, 'warn', 'g'), node('top', 900, 0, 'warn'), node('boom', 0, 900, 'bad'), node('fine', 0, 0)]
    expect(problemsOf(nodes).map((p) => p.id)).toEqual(['boom', 'top', 'b', 'a'])
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
    const { container } = render(<ProblemsPill problems={[]} selectedId={null} onGo={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('says how many, with an accessible name, and singular for one', () => {
    const { rerender } = render(<ProblemsPill problems={ps} selectedId={null} onGo={() => {}} />)
    expect(screen.getByRole('button', { name: /^3 need attention/ })).toBeTruthy()
    rerender(<ProblemsPill problems={[ps[0]]} selectedId={null} onGo={() => {}} />)
    expect(screen.getByRole('button', { name: /^1 needs attention/ })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Previous problem' })).toBeNull()
  })

  test('a press goes forward, Shift or the chevron goes back, and the position shows once one is selected', () => {
    const onGo = vi.fn()
    render(<ProblemsPill problems={ps} selectedId="b" onGo={onGo} />)
    expect(screen.getByText('2 of 3 need attention')).toBeTruthy()
    const main = screen.getByRole('button', { name: /need attention/ })
    fireEvent.click(main)
    fireEvent.click(main, { shiftKey: true })
    fireEvent.click(screen.getByRole('button', { name: 'Previous problem' }))
    expect(onGo.mock.calls).toEqual([[false], [true], [true]])
  })
})

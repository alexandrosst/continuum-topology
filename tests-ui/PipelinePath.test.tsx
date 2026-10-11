import { render, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test } from 'vitest'
import PipelinePath from '@/components/operators/PipelinePath'
import type { Hop } from '@/lib/operatorsView'

const NOW = Date.parse('2026-10-09T12:00:00Z')
const fresh = new Date(NOW - 20_000).toISOString()
const hop = (key: Hop['key'], over: Partial<Hop> = {}): Hop => ({ key, label: key, total: 2, healthy: 2, state: 'healthy', worst: 0, lastData: fresh, ...over })
// One line per state: agent -> local flows, local -> regional is stalled (amber), regional -> central is broken (red), central -> FUSION is unknown (dashed).
const hops = [hop('agent'), hop('local', { state: 'attention', worst: 1 }), hop('regional', { state: 'down', worst: 1 }), hop('central', { state: 'unknown' }), hop('fusion', { lastData: undefined })]
const STATES = { agent: 'flowing', local: 'stalled', regional: 'broken', central: 'unknown' } as const

function renderPath() {
  render(<MemoryRouter><PipelinePath hops={hops} now={NOW} selected="" onSelect={() => {}} /></MemoryRouter>)
}
/** The numbers of a path's "M x y L x y ...", in order. */
const nums = (d: string | null) => (d ?? '').match(/-?\d+(\.\d+)?/g)!.map(Number)

describe('the arrow between two nodes', () => {
  const svgs = (key: string) => [...document.querySelectorAll<SVGSVGElement>(`[data-testid=link-${key}] > svg`)]

  test('is one drawing per direction, with the shaft and the head sharing a stroke, and no separate icon for the head', () => {
    renderPath()
    for (const key of Object.keys(STATES)) {
      const li = document.querySelector(`[data-testid=link-${key}]`)!
      expect(li.querySelector('.lucide')).toBeNull()
      const [down, across] = svgs(key)
      expect(svgs(key)).toHaveLength(2)
      for (const svg of [down, across]) {
        expect(svg).toHaveAttribute('aria-hidden', 'true')
        expect(svg).toHaveAttribute('focusable', 'false')
        const g = svg.querySelector('g')!
        expect(svg.querySelectorAll('g')).toHaveLength(1)
        expect(g).toHaveAttribute('stroke', 'currentColor')
        expect(g).toHaveAttribute('stroke-width', '2')
        expect(g).toHaveAttribute('fill', 'none')
        // Nothing in it sets a colour or a weight of its own, so the shaft and the head cannot differ.
        for (const p of svg.querySelectorAll('path')) for (const a of ['stroke', 'stroke-width', 'fill']) expect(p).not.toHaveAttribute(a)
        expect(g.querySelectorAll('path')).toHaveLength(['flowing', 'unknown'].includes(STATES[key]) ? 3 : 2) // dashed ones add the solid dash at the head
      }
    }
  })

  test('across on a wide screen and down on a narrow one, as before: each drawing is shown by its own breakpoint', () => {
    renderPath()
    const [down, across] = svgs('agent')
    expect(down).toHaveClass('xl:hidden')
    expect(down).not.toHaveClass('hidden')
    expect(across).toHaveClass('hidden', 'xl:block')
    expect(down).toHaveAttribute('preserveAspectRatio', 'xMidYMax slice')
    expect(across).toHaveAttribute('preserveAspectRatio', 'xMaxYMid slice')
  })

  test('the head joins the end of the shaft, and its tip is the same small gap short of the next node on every hop and in both directions', () => {
    renderPath()
    const tips = new Set<number>()
    for (const key of Object.keys(STATES)) {
      for (const [i, svg] of svgs(key).entries()) {
        const paths = [...svg.querySelectorAll('path')].map((p) => nums(p.getAttribute('d')))
        const [shaft, head] = [paths[0], paths[paths.length - 1]]
        const across = i === 1 // [vertical, horizontal]
        const [along, mid] = across ? [(n: number[], k: number) => n[2 * k], (n: number[], k: number) => n[2 * k + 1]] : [(n: number[], k: number) => n[2 * k + 1], (n: number[], k: number) => n[2 * k]]
        // Shaft: start -> end. Head: wing, corner, wing; the corner is where the shaft ends, on the middle of the drawing.
        expect([along(shaft, 1), mid(shaft, 1)]).toEqual([along(head, 1), mid(head, 1)])
        expect(mid(shaft, 0)).toBe(mid(shaft, 1))
        expect(mid(head, 0) + mid(head, 2)).toBe(2 * mid(head, 1))
        expect(along(head, 0)).toBe(along(head, 2))
        expect(along(head, 0)).toBeLessThan(along(head, 1))
        const len = Number(svg.getAttribute('viewBox')!.split(' ')[across ? 2 : 3])
        tips.add(len - along(head, 1))
      }
    }
    expect([...tips]).toEqual([5]) // the corner is 5 short of the end of the box, 4 of it the gap and 1 the round join's reach
  })

  test('healthy and moving: dashes that travel (the shared flow-dash class, which reduced motion stills); every other state does not move', () => {
    renderPath()
    for (const [key, state] of Object.entries(STATES)) {
      expect(document.querySelector(`[data-testid=link-${key}]`)).toHaveAttribute('data-link', state)
      for (const svg of svgs(key)) expect(!!svg.querySelector('.flow-dash')).toBe(state === 'flowing')
    }
  })

  test('dashed ones (moving and unknown) are laid out so the last dash ends exactly at the head; amber and red are one unbroken line', () => {
    renderPath()
    for (const [key, state] of Object.entries(STATES)) {
      for (const [i, svg] of svgs(key).entries()) {
        const shaft = svg.querySelector('path')!
        const n = nums(shaft.getAttribute('d'))
        const [start, end] = i === 1 ? [n[0], n[2]] : [n[1], n[3]]
        if (state === 'stalled' || state === 'broken') {
          expect(start).toBe(0)
          expect(shaft).not.toHaveAttribute('stroke-dasharray')
          continue
        }
        // 5 on, 4 off, from the start of the line: the end falls where a dash ends (5 into a period of 9).
        if (state === 'unknown') expect(shaft).toHaveAttribute('stroke-dasharray', '5 4')
        expect((end - start) % 9).toBe(5)
      }
    }
  })

  test('a dashed line ends in one solid dash fixed to the head, so the moving dashes run into it; the others have none', () => {
    renderPath()
    for (const [key, state] of Object.entries(STATES)) {
      for (const [i, svg] of svgs(key).entries()) {
        const paths = [...svg.querySelectorAll('path')]
        const dashed = state === 'flowing' || state === 'unknown'
        expect(paths).toHaveLength(dashed ? 3 : 2)
        if (!dashed) continue
        const stem = nums(paths[1].getAttribute('d'))
        const head = nums(paths[2].getAttribute('d'))
        const [from, to, corner] = i === 1 ? [stem[0], stem[2], head[2]] : [stem[1], stem[3], head[3]]
        expect(to - from).toBe(5)
        expect(to).toBe(corner)
        expect(paths[1]).not.toHaveAttribute('stroke-dasharray')
        expect(paths[1].classList.contains('flow-dash')).toBe(false)
      }
    }
  })

  test('the freshness bar and the age stay under the line, and a line with nothing measured on both sides still draws', () => {
    renderPath()
    const li = document.querySelector('[data-testid=link-agent]') as HTMLElement
    expect(within(li).getByRole('meter')).toHaveAttribute('aria-valuetext', 'Last data 20 s ago')
    expect(li).toHaveTextContent('20 s ago')
    expect(document.querySelectorAll('[data-testid=link-central] > svg')).toHaveLength(2)
  })
})

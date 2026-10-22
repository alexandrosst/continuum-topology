import { describe, expect, test } from 'vitest'
import { straightenJog, MAX_JOG } from '@/features/topology/components/straighten'

const box = { x: 0, y: 0, w: 100, h: 50 }
const below = { x: 0, y: 200, w: 100, h: 50 }
const jog = (dx: number) => [{ x: 50, y: 50 }, { x: 50, y: 125 }, { x: 50 + dx, y: 125 }, { x: 50 + dx, y: 200 }]

describe('straightenJog', () => {
  test('a small sideways jog becomes one straight run, moving the end that is not fanned', () => {
    const pts = jog(8)
    expect(straightenJog(pts, { source: box, target: below, sourceOffset: 8, targetOffset: 0 })).toBe(true)
    expect(pts[0].x).toBe(50)
    expect(pts[3].x).toBe(50)
    const mine = jog(8)
    expect(straightenJog(mine, { source: box, target: below, sourceOffset: 0, targetOffset: 8 })).toBe(true)
    expect(mine[0].x).toBe(58)
  })

  test('a jog wider than MAX_JOG is a real detour and stays', () => {
    const pts = jog(MAX_JOG + 6)
    expect(straightenJog(pts, { source: box, target: below, sourceOffset: 0, targetOffset: 0 })).toBe(false)
    expect(pts[3].x).toBe(50 + MAX_JOG + 6)
  })

  test('an end is not moved past the corner of its box; the other end moves instead', () => {
    const narrow = { x: 40, y: 200, w: 30, h: 50 }
    const pts = jog(-3)
    pts[0].x = pts[1].x = 36 + 14 // source at x 50 -> arrives at 47; target spans 40..70, inset 12 -> 52..58
    expect(straightenJog(pts, { source: box, target: narrow, sourceOffset: 0, targetOffset: 0 })).toBe(true)
    expect(pts[0].x).toBe(pts[3].x)
  })

  test('routes with other shapes are left alone', () => {
    const l = [{ x: 0, y: 0 }, { x: 0, y: 10 }, { x: 40, y: 10 }]
    expect(straightenJog(l, { source: box, target: below, sourceOffset: 0, targetOffset: 0 })).toBe(false)
  })

  test('horizontal legs work the same way', () => {
    const left = { x: 0, y: 0, w: 50, h: 100 }
    const right = { x: 200, y: 0, w: 50, h: 100 }
    const pts = [{ x: 50, y: 40 }, { x: 125, y: 40 }, { x: 125, y: 46 }, { x: 200, y: 46 }]
    expect(straightenJog(pts, { source: left, target: right, sourceOffset: 0, targetOffset: 0 })).toBe(true)
    expect(pts[3].y).toBe(40)
  })
})

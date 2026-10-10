import { describe, expect, test } from 'vitest'
import { laneRoute, type Pt, type Rect } from '@/lib/lanes'

/** Every run of the route is straight up-down or side-to-side. */
const orthogonal = (r: Pt[]) => r.every((p, i) => i === 0 || p.x === r[i - 1].x || p.y === r[i - 1].y)
/** Whether any run goes through the inside of `b`. */
const crosses = (r: Pt[], b: Rect) =>
  r.some((p, i) => {
    if (i === 0) return false
    const q = r[i - 1]
    return Math.max(p.x, q.x) > b.x && Math.min(p.x, q.x) < b.x + b.w && Math.max(p.y, q.y) > b.y && Math.min(p.y, q.y) < b.y + b.h
  })

const A: Rect = { x: 0, y: 0, w: 300, h: 100 }

describe('laneRoute: lines that stay in the gutters', () => {
  test('boxes in two rows are joined bottom to top through the gap between the rows', () => {
    const B: Rect = { x: 400, y: 250, w: 300, h: 100 }
    const r = laneRoute(A, B, [])!
    expect(orthogonal(r)).toBe(true)
    expect(r[0]).toEqual({ x: 150, y: 100 })
    expect(r[r.length - 1]).toEqual({ x: 550, y: 250 })
    // The sideways run is in the middle of the gap between the rows, clear of both boxes.
    expect(r.some((p) => p.y === 175)).toBe(true)
    expect(crosses(r, A) || crosses(r, B)).toBe(false)
  })

  test('boxes right above one another are joined by one straight line', () => {
    const B: Rect = { x: 0, y: 250, w: 300, h: 100 }
    expect(laneRoute(A, B, [])).toEqual([{ x: 150, y: 100 }, { x: 150, y: 250 }])
  })

  test('side by side boxes whose ends differ by a few pixels are joined by one straight line, not a Z with a jog', () => {
    const B: Rect = { x: 400, y: 4, w: 300, h: 100 }
    const r = laneRoute(A, B, [], 0, 3)!
    expect(r).toHaveLength(2)
    expect(r[0].y).toBe(r[1].y)
  })

  test('a box in the way is gone round through the gutters, never across', () => {
    const middle: Rect = { x: -20, y: 150, w: 340, h: 100 }
    const B: Rect = { x: 0, y: 400, w: 300, h: 100 }
    const r = laneRoute(A, B, [middle])!
    expect(orthogonal(r)).toBe(true)
    expect(crosses(r, middle)).toBe(false)
    expect(crosses(r, A) || crosses(r, B)).toBe(false)
  })

  test('boxes in one row leave and enter by the sides that face each other, round a neighbour between them', () => {
    const between: Rect = { x: 400, y: 0, w: 300, h: 100 }
    const B: Rect = { x: 800, y: 0, w: 300, h: 100 }
    const r = laneRoute(A, B, [between])!
    expect(orthogonal(r)).toBe(true)
    expect(r[0].x).toBe(300)
    expect(r[r.length - 1].x).toBe(800)
    expect(crosses(r, between)).toBe(false)
  })

  test('lines that share a side attach apart, and never past the box\'s own edge', () => {
    const B: Rect = { x: 400, y: 250, w: 300, h: 100 }
    expect(laneRoute(A, B, [], -40)![0].x).toBe(110)
    expect(laneRoute(A, B, [], 5000)![0].x).toBe(280)
  })

  test('the same boxes give the same route, and boxes that overlap give none', () => {
    const B: Rect = { x: 400, y: 250, w: 300, h: 100 }
    expect(laneRoute(A, B, [])).toEqual(laneRoute(A, B, []))
    expect(laneRoute(A, { x: 100, y: 50, w: 300, h: 100 }, [])).toBeNull()
  })

  test('every turn is a real corner: no point sits in the middle of a straight run', () => {
    const B: Rect = { x: 400, y: 250, w: 300, h: 100 }
    const r = laneRoute(A, B, [{ x: 800, y: 0, w: 100, h: 400 }])!
    for (let i = 1; i < r.length - 1; i++) expect((r[i - 1].x === r[i].x && r[i].x === r[i + 1].x) || (r[i - 1].y === r[i].y && r[i].y === r[i + 1].y)).toBe(false)
  })
})

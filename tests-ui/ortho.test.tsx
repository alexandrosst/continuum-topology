import { describe, expect, test } from 'vitest'
import { ORTHO_MARGIN, ORTHO_STUB, orthoRoute, simpleOrthoRoute } from '@/features/topology/components/ortho'

const down = { x: 0, y: 1 }
const up = { x: 0, y: -1 }
const right = { x: 1, y: 0 }
const left = { x: -1, y: 0 }

const horizontalOrVertical = (pts: { x: number; y: number }[]) => pts.slice(1).every((p, i) => Math.abs(p.x - pts[i].x) < 1e-6 || Math.abs(p.y - pts[i].y) < 1e-6)
const crosses = (pts: { x: number; y: number }[], r: { x: number; y: number; w: number; h: number }) =>
  pts.slice(1).some((b, i) => {
    const a = pts[i]
    const x0 = Math.min(a.x, b.x)
    const x1 = Math.max(a.x, b.x)
    const y0 = Math.min(a.y, b.y)
    const y1 = Math.max(a.y, b.y)
    return x1 > r.x + 0.01 && x0 < r.x + r.w - 0.01 && y1 > r.y + 0.01 && y0 < r.y + r.h - 0.01
  })

describe('orthoRoute', () => {
  test('a clear way is a straight line when the ends are level', () => {
    const r = orthoRoute({ x: 0, y: 0 }, down, { x: 0, y: 300 }, up, [])!
    expect(r).toEqual([{ x: 0, y: 0 }, { x: 0, y: 300 }])
  })

  test('uses only horizontal and vertical runs, leaves along the source normal and arrives along the target normal', () => {
    const r = orthoRoute({ x: 0, y: 0 }, down, { x: 200, y: 300 }, up, [])!
    expect(horizontalOrVertical(r)).toBe(true)
    expect(r[1].x).toBe(0) // the first run goes straight down
    expect(r[1].y).toBeGreaterThanOrEqual(ORTHO_STUB - 1e-6)
    const last = r[r.length - 1]
    const before = r[r.length - 2]
    expect(before.x).toBe(last.x) // the last run is vertical into the target
    expect(before.y).toBeLessThan(last.y)
  })

  test('a jog between ends that are not level sits about halfway', () => {
    const r = orthoRoute({ x: 0, y: 0 }, down, { x: 200, y: 400 }, up, [])!
    const horizontal = r.slice(1).map((p, i) => [r[i], p]).find(([a, b]) => a.y === b.y && a.x !== b.x)!
    expect(horizontal[0].y).toBeGreaterThan(150)
    expect(horizontal[0].y).toBeLessThan(250)
  })

  test('goes round a box in the way, keeping its margin', () => {
    const box = { x: -50, y: 100, w: 100, h: 100 }
    const r = orthoRoute({ x: 0, y: 0 }, down, { x: 0, y: 300 }, up, [box])!
    expect(r).not.toBeNull()
    expect(crosses(r, box)).toBe(false)
    expect(horizontalOrVertical(r)).toBe(true)
    // ...and passes it at no less than the margin.
    for (const p of r) {
      const inside = p.x > box.x - ORTHO_MARGIN + 0.01 && p.x < box.x + box.w + ORTHO_MARGIN - 0.01 && p.y > box.y - ORTHO_MARGIN + 0.01 && p.y < box.y + box.h + ORTHO_MARGIN - 0.01
      expect(inside).toBe(false)
    }
  })

  test('takes the gap between two boxes when there is one', () => {
    const boxes = [{ x: -200, y: 100, w: 170, h: 100 }, { x: 30, y: 100, w: 170, h: 100 }]
    const r = orthoRoute({ x: 0, y: 0 }, down, { x: 0, y: 300 }, up, boxes)!
    expect(r).toEqual([{ x: 0, y: 0 }, { x: 0, y: 300 }])
  })

  test('prefers fewer bends to a slightly shorter route', () => {
    const r = orthoRoute({ x: 0, y: 0 }, right, { x: 400, y: 10 }, left, [])!
    expect(r.length).toBeLessThanOrEqual(4)
  })

  test('null when an end is boxed in, or when there are too many boxes', () => {
    expect(orthoRoute({ x: 0, y: 0 }, down, { x: 0, y: 300 }, up, [{ x: -100, y: 5, w: 200, h: 100 }])).toBeNull()
    const many = Array.from({ length: 41 }, (_, i) => ({ x: i * 300, y: 500, w: 10, h: 10 }))
    expect(orthoRoute({ x: 0, y: 0 }, down, { x: 0, y: 300 }, up, many)).toBeNull()
  })

  test('a diagonal route needs no search: simpleOrthoRoute gives one centred jog', () => {
    const r = simpleOrthoRoute({ x: 0, y: 0 }, down, { x: 200, y: 400 }, up)
    expect(horizontalOrVertical(r)).toBe(true)
    expect(r[0]).toEqual({ x: 0, y: 0 })
    expect(r[r.length - 1]).toEqual({ x: 200, y: 400 })
  })
})

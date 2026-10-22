import { describe, expect, test } from 'vitest'
import { DETOUR_MARGIN, DETOUR_MAX_OBSTACLES, detourRoute, polylineMidpoint, segmentCrosses, type Pt, type Rect } from '@/features/topology/components/detour'

const box: Rect = { x: 100, y: 100, w: 100, h: 100 }
const right = { x: 1, y: 0 }
const left = { x: -1, y: 0 }

describe('segmentCrosses', () => {
  test('a segment through the middle of a box crosses it', () => {
    expect(segmentCrosses({ x: 0, y: 150 }, { x: 300, y: 150 }, box)).toBe(true)
  })
  test('a segment that stops short, or passes beside, does not', () => {
    expect(segmentCrosses({ x: 0, y: 150 }, { x: 90, y: 150 }, box)).toBe(false)
    expect(segmentCrosses({ x: 0, y: 50 }, { x: 300, y: 50 }, box)).toBe(false)
  })
  test('running along an edge, or touching a corner, is not crossing', () => {
    expect(segmentCrosses({ x: 0, y: 100 }, { x: 300, y: 100 }, box)).toBe(false)
    expect(segmentCrosses({ x: 50, y: 150 }, { x: 150, y: 50 }, box)).toBe(false)
  })
  test('a diagonal that clips a corner does cross', () => {
    expect(segmentCrosses({ x: 80, y: 120 }, { x: 120, y: 80 }, box)).toBe(false) // touches the corner only
    expect(segmentCrosses({ x: 80, y: 130 }, { x: 130, y: 80 }, box)).toBe(true) // cuts across it
  })
})

describe('detourRoute', () => {
  const grown = (r: Rect): Rect => ({ x: r.x - DETOUR_MARGIN, y: r.y - DETOUR_MARGIN, w: r.w + 2 * DETOUR_MARGIN, h: r.h + 2 * DETOUR_MARGIN })
  const clearOf = (pts: Pt[], rects: Rect[]) => pts.slice(1).every((p, i) => rects.every((r) => !segmentCrosses(pts[i], p, grown(r))))

  test('nothing in the way: no detour is needed, so none is offered', () => {
    expect(detourRoute({ x: 0, y: 150 }, right, { x: 400, y: 150 }, left, [])).toBeNull()
  })

  test('a box between the two ends is gone around, from p1 to p2, never through it', () => {
    const p1 = { x: 0, y: 150 }
    const p2 = { x: 300, y: 150 }
    const route = detourRoute(p1, right, p2, left, [box])!
    expect(route[0]).toEqual(p1)
    expect(route[route.length - 1]).toEqual(p2)
    expect(route.length).toBeGreaterThan(3)
    expect(clearOf(route, [box])).toBe(true)
  })

  test('it leaves and arrives square to the boxes it joins: the first and last legs run along the normals', () => {
    const route = detourRoute({ x: 0, y: 150 }, right, { x: 300, y: 150 }, left, [box])!
    expect(route[1].y).toBe(150)
    expect(route[1].x).toBeGreaterThan(0)
    expect(route[route.length - 2].y).toBe(150)
    expect(route[route.length - 2].x).toBeLessThan(300)
  })

  test('several boxes in a row are passed through the channel between them or around the whole row, never across one', () => {
    const row: Rect[] = [box, { x: 260, y: 100, w: 100, h: 100 }, { x: 420, y: 100, w: 100, h: 100 }]
    const route = detourRoute({ x: 0, y: 150 }, right, { x: 600, y: 150 }, left, row)!
    expect(clearOf(route, row)).toBe(true)
  })

  test('an end boxed in by another box has no route', () => {
    expect(detourRoute({ x: 100, y: 150 }, left, { x: 400, y: 150 }, left, [{ x: 70, y: 100, w: 20, h: 100 }])).toBeNull()
  })

  test('too many boxes to search: no route, so the caller keeps its own best effort', () => {
    const many = Array.from({ length: DETOUR_MAX_OBSTACLES + 1 }, (_, i) => ({ x: 100 + i * 5, y: 5000 + i, w: 2, h: 2 }))
    expect(detourRoute({ x: 0, y: 150 }, right, { x: 300, y: 150 }, left, many)).toBeNull()
  })

  test('of two routes of about the same length it takes the one with fewer turns', () => {
    const route = detourRoute({ x: 0, y: 150 }, right, { x: 300, y: 150 }, left, [box])!
    // out, round the nearer side of the box, and in: p1, stub, two corners, stub, p2
    expect(route).toHaveLength(6)
  })
})

describe('polylineMidpoint', () => {
  test('is halfway along the length, not halfway between the ends', () => {
    expect(polylineMidpoint([{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 100, y: 100 }])).toEqual({ x: 100, y: 0 })
    expect(polylineMidpoint([{ x: 0, y: 0 }, { x: 100, y: 0 }])).toEqual({ x: 50, y: 0 })
  })
})

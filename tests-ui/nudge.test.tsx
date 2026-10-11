import { describe, expect, test } from 'vitest'
import { LANE_GAP, nudgeRoutes, type Route } from '@/components/topology/nudge'

// A line that leaves downward, runs along y, and comes back down: a Z with a long horizontal run.
const z = (id: string, y: number, x0 = 0, x1 = 300): Route => ({
  id,
  pts: [{ x: x0, y: 0 }, { x: x0, y }, { x: x1, y }, { x: x1, y: 400 }],
})
const runY = (r: Route) => r.pts[1].y

describe('nudgeRoutes', () => {
  test('two lines on top of each other are spread into separate lanes around where they were', () => {
    const a = z('a', 200)
    const b = z('b', 200)
    nudgeRoutes([a, b], [])
    expect(Math.abs(runY(a) - runY(b))).toBeCloseTo(LANE_GAP, 5)
    expect((runY(a) + runY(b)) / 2).toBeCloseTo(200, 5)
  })

  test('lines already well apart are left alone', () => {
    const a = z('a', 150)
    const b = z('b', 250)
    nudgeRoutes([a, b], [])
    expect(runY(a)).toBe(150)
    expect(runY(b)).toBe(250)
  })

  test('lines that are close but share no length are not a group', () => {
    const a = z('a', 200, 0, 100)
    const b = z('b', 203, 200, 300)
    nudgeRoutes([a, b], [])
    expect(runY(a)).toBe(200)
    expect(runY(b)).toBe(203)
  })

  test('three lines get three evenly spaced lanes', () => {
    const rs = [z('a', 200), z('b', 202), z('c', 199)]
    nudgeRoutes(rs, [])
    const ys = rs.map(runY).sort((p, q) => p - q)
    expect(ys[1] - ys[0]).toBeCloseTo(LANE_GAP, 5)
    expect(ys[2] - ys[1]).toBeCloseTo(LANE_GAP, 5)
  })

  test('the legs at the boxes never move: only the bends do, and the line stays connected and square', () => {
    const a = z('a', 200)
    const b = z('b', 200)
    nudgeRoutes([a, b], [])
    for (const r of [a, b]) {
      expect(r.pts[0]).toEqual({ x: r.pts[0].x, y: 0 })
      expect(r.pts[3].y).toBe(400)
      expect(r.pts[0].x).toBe(r.pts[1].x)
      expect(r.pts[2].x).toBe(r.pts[3].x)
      expect(r.pts[1].y).toBe(r.pts[2].y)
    }
  })

  test('a lane never enters a box: a tight channel squeezes the lanes instead', () => {
    const a = z('a', 200)
    const b = z('b', 200)
    const above = { x: 0, y: 100, w: 300, h: 90 } // bottom at 190
    const below = { x: 0, y: 214, w: 300, h: 50 } // top at 214
    nudgeRoutes([a, b], [above, below])
    for (const r of [a, b]) {
      expect(runY(r)).toBeGreaterThan(190)
      expect(runY(r)).toBeLessThan(214)
    }
    expect(runY(a)).not.toBe(runY(b))
  })

  test('with no room at all the lines are left as routed', () => {
    const a = z('a', 200)
    const b = z('b', 200)
    nudgeRoutes([a, b], [{ x: 0, y: 100, w: 300, h: 99 }, { x: 0, y: 202, w: 300, h: 50 }])
    expect(runY(a)).toBe(200)
    expect(runY(b)).toBe(200)
  })

  test('the order of lanes is stable, whatever order the lines come in', () => {
    const one = [z('a', 200), z('b', 200)]
    const two = [z('b', 200), z('a', 200)]
    nudgeRoutes(one, [])
    nudgeRoutes(two, [])
    expect(runY(one[0])).toBe(runY(two[1]))
    expect(runY(one[1])).toBe(runY(two[0]))
  })

  test('vertical runs are spread the same way', () => {
    const v = (id: string): Route => ({ id, pts: [{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 100, y: 300 }, { x: 400, y: 300 }] })
    const a = v('a')
    const b = v('b')
    nudgeRoutes([a, b], [])
    expect(Math.abs(a.pts[1].x - b.pts[1].x)).toBeCloseTo(LANE_GAP, 5)
  })

  test('two lines that run opposite ways between the same boxes are ordered so they do not cross', () => {
    // Both leave the lower box (y 470) at the left and meet the upper one (y 336) at the right, the second drawn from the upper end.
    const a: Route = { id: 'a', pts: [{ x: 618, y: 470 }, { x: 618, y: 403 }, { x: 835, y: 403 }, { x: 835, y: 336 }] }
    const o: Route = { id: 'o', pts: [{ x: 865, y: 336 }, { x: 865, y: 403 }, { x: 632, y: 403 }, { x: 632, y: 470 }] }
    nudgeRoutes([a, o], [])
    // `a` has the lower ends at both sides, so its run takes the lane nearer the upper box; crossing would need the reverse.
    expect(a.pts[1].y).toBeLessThan(o.pts[1].y)
    // And so a's lower vertical (x 618, rising to its run) never meets o's run, nor o's vertical a's run.
    const crossesO = a.pts[1].y >= o.pts[1].y && a.pts[1].y <= 470
    expect(crossesO).toBe(false)
  })
})

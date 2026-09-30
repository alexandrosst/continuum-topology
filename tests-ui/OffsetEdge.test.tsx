import { describe, expect, test } from 'vitest'
import { curvedPath, intersection, pullBackEnds } from '@/components/topology/OffsetEdge'

// A pure-math test, no rendering needed - kept in tests-ui/ (not tests/) purely because that's where the
// vitest config's include glob looks; nothing here touches the DOM.
describe('intersection (OffsetEdge\'s floating-edge anchor math)', () => {
  const box = { x: 100, y: 100, w: 200, h: 100 } // center (200, 150)

  test('a point straight to the right exits at the right-side midpoint', () => {
    const p = intersection(box, { x: 1000, y: 150 })
    expect(p.x).toBeCloseTo(300, 5) // box.x + box.w
    expect(p.y).toBeCloseTo(150, 5) // box's own vertical center
  })

  test('a point straight below exits at the bottom-side midpoint', () => {
    const p = intersection(box, { x: 200, y: 1000 })
    expect(p.x).toBeCloseTo(200, 5)
    expect(p.y).toBeCloseTo(200, 5) // box.y + box.h
  })

  test('a point straight left exits at the left-side midpoint, straight up at the top-side midpoint', () => {
    const left = intersection(box, { x: -1000, y: 150 })
    expect(left).toEqual({ x: 100, y: 150 })
    const up = intersection(box, { x: 200, y: -1000 })
    expect(up).toEqual({ x: 200, y: 100 })
  })

  test('exits on a genuinely continuous point around the perimeter, not one of 4 fixed side midpoints', () => {
    // A shallow diagonal (mostly rightward, barely downward) used to bucket to the same fixed 'right'
    // midpoint pickSides would have chosen for a much steeper line too - that's the "steep/awkward" bug.
    // The exit point here must still sit on the right edge (x = 300) but at a y noticeably off-center,
    // proportional to how steep the approach actually is.
    const shallow = intersection(box, { x: 1000, y: 200 })
    const steeper = intersection(box, { x: 1000, y: 400 })
    expect(shallow.x).toBeCloseTo(300, 5)
    expect(steeper.x).toBeCloseTo(300, 5)
    expect(shallow.y).toBeGreaterThan(150) // below center, since the target is below-right
    expect(steeper.y).toBeGreaterThan(shallow.y) // a steeper approach exits further down the same side
    expect(steeper.y).toBeLessThan(200) // but never past the box's own bottom edge
  })

  test('every exit point lands exactly on the box\'s own boundary, for a spread of directions', () => {
    const angles = [10, 40, 80, 100, 145, 190, 240, 300, 350]
    for (const deg of angles) {
      const rad = (deg * Math.PI) / 180
      const toward = { x: 200 + Math.cos(rad) * 5000, y: 150 + Math.sin(rad) * 5000 }
      const p = intersection(box, toward)
      const onVerticalEdge = Math.abs(p.x - 100) < 1e-6 || Math.abs(p.x - 300) < 1e-6
      const onHorizontalEdge = Math.abs(p.y - 100) < 1e-6 || Math.abs(p.y - 200) < 1e-6
      expect(onVerticalEdge || onHorizontalEdge, `angle ${deg}: (${p.x}, ${p.y}) should sit on one of the box's 4 edges`).toBe(true)
      expect(p.x).toBeGreaterThanOrEqual(100 - 1e-6)
      expect(p.x).toBeLessThanOrEqual(300 + 1e-6)
      expect(p.y).toBeGreaterThanOrEqual(100 - 1e-6)
      expect(p.y).toBeLessThanOrEqual(200 + 1e-6)
    }
  })
})

describe('curvedPath (OffsetEdge\'s gentle-bow path math)', () => {
  test('starts and ends exactly at the given anchor points, whatever the bow', () => {
    const { path } = curvedPath(0, 0, 300, 0, 0, 1)
    expect(path.startsWith('M0,0 ')).toBe(true)
    expect(path.endsWith(' 300,0')).toBe(true)
  })

  test('bows perpendicular to the line, toward the given normal, and away from the straight midpoint', () => {
    const straightMidX = 150
    const straightMidY = 0
    const { path } = curvedPath(0, 0, 300, 0, 0, 1) // nx=0, ny=1: bow straight "down" in SVG's y-down space
    const control = path.match(/Q([\d.-]+),([\d.-]+)/)
    expect(control).not.toBeNull()
    const [, cx, cy] = control!
    expect(Number(cx)).toBeCloseTo(straightMidX, 5)
    expect(Number(cy)).toBeGreaterThan(straightMidY) // pulled toward +y, not left sitting on the straight line
  })

  test('bow is proportional to length but capped, so a very long edge stays a subtle arc', () => {
    const short = curvedPath(0, 0, 100, 0, 0, 1)
    const long = curvedPath(0, 0, 100_000, 0, 0, 1)
    const bowOf = (p: { path: string }) => {
      const m = p.path.match(/Q[\d.-]+,([\d.-]+)/)
      return Number(m![1])
    }
    expect(bowOf(short)).toBeCloseTo(100 * 0.12, 5) // under the cap: exactly proportional
    expect(bowOf(long)).toBeCloseTo(36, 5) // over the cap: clamped, not thousands of pixels
  })

  test('label sits on the actual curve (quadratic midpoint), not the straight-line midpoint, for a bowed edge', () => {
    const { labelX, labelY } = curvedPath(0, 0, 300, 0, 0, 1)
    expect(labelX).toBeCloseTo(150, 5) // symmetric case: still centered on x
    expect(labelY).toBeGreaterThan(0) // but pulled off the straight line's y=0 toward the bow
  })

  test('a straight-through bow (zero normal) collapses back to the straight-line midpoint', () => {
    const { labelX, labelY } = curvedPath(0, 0, 300, 0, 0, 0)
    expect(labelX).toBeCloseTo(150, 5)
    expect(labelY).toBeCloseTo(0, 5)
  })
})


describe('pullBackEnds (keeps only an arrowhead\'s own tip touching a box, per the UI/UX pass)', () => {
  test('pulls the source end back toward the target, and leaves the target end untouched when it has a marker', () => {
    const { sx, sy, tx, ty } = pullBackEnds(0, 0, 300, 0, 5, true)
    expect(sx).toBeCloseTo(5, 5) // moved 5px toward the target
    expect(sy).toBeCloseTo(0, 5)
    expect(tx).toBe(300) // untouched: a real markerEnd's own tip belongs exactly here
    expect(ty).toBe(0)
  })

  test('pulls the target end back too when it has no marker (an aggregated edge, with nothing pointed to place there)', () => {
    const { tx, ty } = pullBackEnds(0, 0, 300, 0, 5, false)
    expect(tx).toBeCloseTo(295, 5) // moved 5px back toward the source
    expect(ty).toBeCloseTo(0, 5)
  })

  test('pulls back along whatever direction the segment actually runs, not just axis-aligned', () => {
    // A 3-4-5 triangle: length 10, so a gap of 2 is exactly 20% of the way along each axis.
    const { sx, sy } = pullBackEnds(0, 0, 6, 8, 2, true)
    expect(sx).toBeCloseTo(1.2, 5)
    expect(sy).toBeCloseTo(1.6, 5)
  })

  test('never produces a degenerate (zero-length) segment: source and target still land on the same point for a zero-length input', () => {
    const { sx, sy, tx, ty } = pullBackEnds(50, 50, 50, 50, 5, false)
    expect(sx).toBe(50)
    expect(sy).toBe(50)
    expect(tx).toBe(50)
    expect(ty).toBe(50)
  })
})

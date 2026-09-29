import { describe, expect, test } from 'vitest'
import { intersection } from '@/components/topology/OffsetEdge'

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

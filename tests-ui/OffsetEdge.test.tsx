import { describe, expect, test } from 'vitest'
import {
  avoidHeaderSide,
  cachedOrtho,
  collectObstacles,
  curvedPath,
  ARRIVAL_DEPTH,
  insetFromCorners,
  intersection,
  outwardNormal,
  OBSTACLE_MARGIN,
  orthoCacheCapFor,
  pickClearSide,
  pullBackEnds,
  setOrthoCacheCap,
  SIDE_INSET,
  sideIsClear,
  sideMidpoint,
  slideOnSide,
} from '@/components/topology/OffsetEdge'
import { HEADER, NS_HEADER } from '@/lib/graph'

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

describe('outwardNormal (which side of a box a boundary point sits on, as a unit normal)', () => {
  const box = { x: 100, y: 100, w: 200, h: 100 } // spans x:100-300, y:100-200

  test('points left/right for a point on the box\'s own left/right edge', () => {
    expect(outwardNormal(box, { x: 100, y: 150 })).toEqual({ x: -1, y: 0 })
    expect(outwardNormal(box, { x: 300, y: 150 })).toEqual({ x: 1, y: 0 })
  })

  test('points up/down for a point on the box\'s own top/bottom edge', () => {
    expect(outwardNormal(box, { x: 200, y: 100 })).toEqual({ x: 0, y: -1 })
    expect(outwardNormal(box, { x: 200, y: 200 })).toEqual({ x: 0, y: 1 })
  })
})

describe('sideMidpoint (the touch point pickClearSide falls back to on each of a box\'s 4 sides)', () => {
  const box = { x: 100, y: 100, w: 200, h: 80 } // spans x:100-300, y:100-180

  test('returns the exact midpoint of each cardinal side', () => {
    expect(sideMidpoint(box, 'top')).toEqual({ x: 200, y: 100 })
    expect(sideMidpoint(box, 'bottom')).toEqual({ x: 200, y: 180 })
    expect(sideMidpoint(box, 'left')).toEqual({ x: 100, y: 140 })
    expect(sideMidpoint(box, 'right')).toEqual({ x: 300, y: 140 })
  })
})

describe('sideIsClear (whether a box\'s approach corridor on one side is free of every obstacle)', () => {
  const box = { x: 100, y: 100, w: 200, h: 80 } // spans x:100-300, y:100-180

  test('a side with nothing nearby is clear', () => {
    expect(sideIsClear(box, 'top', [], OBSTACLE_MARGIN)).toBe(true)
    expect(sideIsClear(box, 'top', [{ x: 100, y: 5000, w: 200, h: 40 }], OBSTACLE_MARGIN)).toBe(true)
  })

  test('an obstacle sitting directly in the corridor above the box blocks the top side only', () => {
    const obstacles = [{ x: 100, y: 100 - OBSTACLE_MARGIN / 2, w: 200, h: 40 }]
    expect(sideIsClear(box, 'top', obstacles, OBSTACLE_MARGIN)).toBe(false)
    expect(sideIsClear(box, 'bottom', obstacles, OBSTACLE_MARGIN)).toBe(true)
    expect(sideIsClear(box, 'left', obstacles, OBSTACLE_MARGIN)).toBe(true)
    expect(sideIsClear(box, 'right', obstacles, OBSTACLE_MARGIN)).toBe(true)
  })

  test('an obstacle just past the given depth does not block the side', () => {
    const obstacles = [{ x: 100, y: 100 - OBSTACLE_MARGIN - 10, w: 200, h: 5 }]
    expect(sideIsClear(box, 'top', obstacles, OBSTACLE_MARGIN)).toBe(true)
  })
})

describe('pickClearSide (falls back to a different entry side when the natural one\'s corridor is blocked)', () => {
  const box = { x: 100, y: 100, w: 200, h: 80 } // spans x:100-300, y:100-180
  const naturalTop = { point: sideMidpoint(box, 'top'), normal: { x: 0, y: -1 } }

  test('with no obstacles, returns the natural point/normal untouched', () => {
    expect(pickClearSide(box, naturalTop, [])).toEqual(naturalTop)
  })

  test('with the natural side clear, still returns it untouched even when other obstacles exist elsewhere', () => {
    const farObstacle = { x: 5000, y: 5000, w: 10, h: 10 }
    expect(pickClearSide(box, naturalTop, [farObstacle])).toEqual(naturalTop)
  })

  test('when the natural (top) side is blocked, falls back to a perpendicular side (left or right) before the opposite one', () => {
    const blockingTop = { x: 100, y: 100 - ARRIVAL_DEPTH / 2, w: 200, h: 40 }
    const result = pickClearSide(box, naturalTop, [blockingTop])
    expect(result).not.toEqual(naturalTop)
    // Should land on left or right, not bottom (the opposite side is only tried after both perpendiculars).
    const isLeft = result.point.x === box.x && result.normal.x === -1
    const isRight = result.point.x === box.x + box.w && result.normal.x === 1
    expect(isLeft || isRight).toBe(true)
  })

  test('when top, left, and right are all blocked, falls back to the opposite (bottom) side', () => {
    const blockTop = { x: 100, y: 100 - ARRIVAL_DEPTH / 2, w: 200, h: 40 }
    const blockLeft = { x: 100 - ARRIVAL_DEPTH / 2, y: 100, w: 40, h: 80 }
    const blockRight = { x: 300 - 40 + ARRIVAL_DEPTH / 2, y: 100, w: 40, h: 80 }
    const result = pickClearSide(box, naturalTop, [blockTop, blockLeft, blockRight])
    expect(result.point).toEqual(sideMidpoint(box, 'bottom'))
    expect(result.normal).toEqual({ x: 0, y: 1 })
  })

  test('given the other end, a fallback side is slid to be level with it, not pinned to the middle', () => {
    const blockingTop = { x: 100, y: 100 - ARRIVAL_DEPTH / 2, w: 200, h: 40 }
    const blockingLeft = { x: 100 - ARRIVAL_DEPTH / 2, y: 100, w: 40, h: 80 }
    const r = pickClearSide(box, naturalTop, [blockingTop, blockingLeft], { x: 5000, y: 120 })
    expect(r.normal).toEqual({ x: 1, y: 0 })
    expect(r.point).toEqual({ x: 300, y: 120 })
  })

  test('when every side is blocked, falls back to the natural point rather than picking an obstructed one', () => {
    const depth = ARRIVAL_DEPTH
    const blockAll = [
      { x: box.x, y: box.y - depth / 2, w: box.w, h: 20 },
      { x: box.x, y: box.y + box.h + depth / 2 - 10, w: box.w, h: 20 },
      { x: box.x - depth / 2, y: box.y, w: 20, h: box.h },
      { x: box.x + box.w + depth / 2 - 10, y: box.y, w: 20, h: box.h },
    ]
    expect(pickClearSide(box, naturalTop, blockAll)).toEqual(naturalTop)
  })
})

describe('avoidHeaderSide (keeps a line off a cluster header\'s text by leaving a first-row card through a side)', () => {
  const parent = { x: 0, y: 0, w: 400, h: 300 }
  const firstRow = { x: 20, y: HEADER, w: 100, h: 50 } // sits right under the header
  const secondRow = { x: 20, y: HEADER + 90, w: 100, h: 50 }
  const top = { point: { x: 70, y: HEADER }, normal: { x: 0, y: -1 } }

  test('a top exit from a first-row card moves to the side facing the other end', () => {
    const r = avoidHeaderSide(firstRow, parent, top, { x: 900, y: 0 }, [])
    expect(r.normal).toEqual({ x: 1, y: 0 })
    expect(r.point).toEqual({ x: 120, y: HEADER + SIDE_INSET }) // level with the other end (here far above), kept off the corner
    expect(avoidHeaderSide(firstRow, parent, top, { x: -500, y: 0 }, []).normal).toEqual({ x: -1, y: 0 })
  })

  test('falls to the other side when the near one is taken by a neighbouring card, and keeps the top when both are', () => {
    const neighbour = { x: 125, y: HEADER, w: 100, h: 50 }
    expect(avoidHeaderSide(firstRow, parent, top, { x: 900, y: 0 }, [neighbour]).normal).toEqual({ x: -1, y: 0 })
    const other = { x: -60, y: HEADER, w: 55, h: 50 }
    expect(avoidHeaderSide(firstRow, parent, top, { x: 900, y: 0 }, [neighbour, other])).toEqual(top)
  })

  test('only a top exit from a first-row card is touched', () => {
    expect(avoidHeaderSide(secondRow, parent, { point: { x: 70, y: HEADER + 90 }, normal: { x: 0, y: -1 } }, { x: 900, y: 0 }, [])).toEqual({ point: { x: 70, y: HEADER + 90 }, normal: { x: 0, y: -1 } })
    const side = { point: { x: 120, y: HEADER + 25 }, normal: { x: 1, y: 0 } }
    expect(avoidHeaderSide(firstRow, parent, side, { x: 900, y: 0 }, [])).toEqual(side)
    const bottom = { point: { x: 70, y: HEADER + 50 }, normal: { x: 0, y: 1 } }
    expect(avoidHeaderSide(firstRow, parent, bottom, { x: 900, y: 0 }, [])).toEqual(bottom)
  })

  test('a card with no cluster around it (a device or an external box) is left alone', () => {
    expect(avoidHeaderSide(firstRow, undefined, top, { x: 900, y: 0 }, [])).toEqual(top)
  })
})

describe('curvedPath (a line that leaves and arrives square to its boxes)', () => {
  const nums = (path: string) => path.match(/-?\d+(?:\.\d+)?/g)!.map(Number)
  const right = { x: 1, y: 0 }
  const left = { x: -1, y: 0 }
  const up = { x: 0, y: -1 }

  test('starts and ends exactly at the anchors, with a short straight leg out of each and a cubic between them', () => {
    const { path } = curvedPath(0, 0, 300, 100, right, left)
    expect(path).toMatch(/^M0,0 L[\d.]+,0 C/)
    expect(path).toMatch(/ L300,100$/)
    expect(path.match(/ C/g)).toHaveLength(1)
  })

  test('leaves straight out along the source normal and arrives straight in along the target normal, whatever the offset', () => {
    for (const [x2, y2] of [[300, 100], [300, -250], [120, 40]]) {
      const [, , l1x, l1y, , , , , s2x, s2y] = nums(curvedPath(0, 0, x2, y2, right, left).path)
      expect(l1y).toBe(0) // first leg runs along +x only
      expect(l1x).toBeGreaterThan(0)
      expect(s2y).toBe(y2) // last leg runs along x only: the arrowhead comes in square
      expect(s2x).toBeLessThan(x2)
    }
  })

  test('the curve between the legs also leaves each end along its normal (the control points sit on the normals)', () => {
    const n = nums(curvedPath(0, 0, 300, 100, right, left).path) // M x y L x y C c1x c1y c2x c2y x y L x y
    const [, , s1x, s1y, c1x, c1y, c2x, c2y, s2x, s2y] = n
    expect(c1y).toBe(s1y)
    expect(c1x).toBeGreaterThan(s1x)
    expect(c2y).toBe(s2y)
    expect(c2x).toBeLessThan(s2x)
  })

  test('the legs are shortened on a very short edge, so they never run past the other end', () => {
    const [, , l1x] = nums(curvedPath(0, 0, 20, 0, right, left).path)
    expect(l1x).toBeLessThan(10)
    expect(l1x).toBeGreaterThan(0)
  })

  test('the label sits on the curve, halfway', () => {
    const { labelX, labelY } = curvedPath(0, 0, 300, 100, right, left)
    expect(labelX).toBeCloseTo(150, 5)
    expect(labelY).toBeCloseTo(50, 5)
  })

  test('works for ends facing the same way (an exit and an arrival on the same side)', () => {
    const { path } = curvedPath(0, 0, 200, 0, up, up)
    expect(path).toMatch(/^M0,0 L0,-[\d.]+ C/)
    expect(path).toMatch(/ L200,0$/)
  })

  test('a box that is nowhere near leaves the curve exactly as it was', () => {
    const plain = curvedPath(0, 0, 300, 100, right, left)
    expect(curvedPath(0, 0, 300, 100, right, left, [{ x: 10_000, y: 10_000, w: 10, h: 10 }])).toEqual(plain)
    expect(curvedPath(0, 0, 300, 100, right, left, [])).toEqual(plain)
  })

  test('a box across the way is gone around, still leaving and arriving square, and the label is on the route', () => {
    const wall = { x: 120, y: -400, w: 60, h: 800 }
    const { path, labelX, labelY } = curvedPath(0, 0, 300, 0, right, left, [wall])
    expect(path).not.toMatch(/ C/) // a routed detour, not the plain curve
    const [, , a, b, , , c, d] = nums(path) // M0,0 L a,b ...: the first leg stays on y=0
    expect([b]).toEqual([0])
    expect(a).toBeGreaterThan(0)
    expect(path).toMatch(/ L300,0$/)
    const inside = labelX > wall.x && labelX < wall.x + wall.w && labelY > wall.y && labelY < wall.y + wall.h
    expect(inside).toBe(false)
    expect([c, d]).toBeDefined()
  })

  test('with no way round (an end boxed in) it still returns a well-formed path between the same anchors', () => {
    const boxedIn = { x: 5, y: -50, w: 10, h: 100 } // right across the first leg, nowhere to turn
    const { path } = curvedPath(0, 0, 300, 100, right, left, [boxedIn])
    expect(path.startsWith('M0,0 ')).toBe(true)
    expect(path.endsWith(' L300,100')).toBe(true)
  })
})

describe('pullBackEnds (keeps only an arrowhead\'s own tip touching a box)', () => {
  test('moves the source end straight out along its normal, and leaves the target end where a marker belongs', () => {
    const r = pullBackEnds({ x: 100, y: 50 }, { x: 1, y: 0 }, { x: 400, y: 80 }, { x: -1, y: 0 }, 5, true)
    expect(r).toEqual({ sx: 105, sy: 50, tx: 400, ty: 80 })
  })

  test('moves the target end back off its box too when it has no marker', () => {
    const r = pullBackEnds({ x: 100, y: 50 }, { x: 1, y: 0 }, { x: 400, y: 80 }, { x: -1, y: 0 }, 5, false)
    expect(r.tx).toBe(395)
    expect(r.ty).toBe(80)
  })

  test('the gap is square-on whatever the angle between the two ends', () => {
    const r = pullBackEnds({ x: 0, y: 0 }, { x: 0, y: -1 }, { x: 300, y: 300 }, { x: 0, y: 1 }, 4, false)
    expect(r).toEqual({ sx: 0, sy: -4, tx: 300, ty: 304 })
  })
})

describe('slideOnSide / insetFromCorners (an end keeps sliding along its side, off the corners)', () => {
  const box = { x: 100, y: 100, w: 200, h: 80 }

  test('slides along top and bottom with the other end\'s x, and along left and right with its y', () => {
    expect(slideOnSide(box, 'top', { x: 180, y: -500 })).toEqual({ x: 180, y: 100 })
    expect(slideOnSide(box, 'bottom', { x: 250, y: 900 })).toEqual({ x: 250, y: 180 })
    expect(slideOnSide(box, 'left', { x: -500, y: 140 })).toEqual({ x: 100, y: 140 })
    expect(slideOnSide(box, 'right', { x: 900, y: 120 })).toEqual({ x: 300, y: 120 })
  })

  test('stops short of the corners by the inset', () => {
    expect(slideOnSide(box, 'top', { x: -9000, y: 0 })).toEqual({ x: 100 + SIDE_INSET, y: 100 })
    expect(slideOnSide(box, 'top', { x: 9000, y: 0 })).toEqual({ x: 300 - SIDE_INSET, y: 100 })
    expect(slideOnSide(box, 'right', { x: 0, y: -9000 })).toEqual({ x: 300, y: 100 + SIDE_INSET })
  })

  test('a point already on a side is moved along it off a corner, and left alone otherwise', () => {
    expect(insetFromCorners(box, { x: 101, y: 100 }, { x: 0, y: -1 })).toEqual({ x: 100 + SIDE_INSET, y: 100 })
    expect(insetFromCorners(box, { x: 300, y: 179 }, { x: 1, y: 0 })).toEqual({ x: 300, y: 180 - SIDE_INSET })
    expect(insetFromCorners(box, { x: 200, y: 100 }, { x: 0, y: -1 })).toEqual({ x: 200, y: 100 })
  })
})

describe('collectObstacles (what a line must keep out of)', () => {
  const node = (type: string, x: number, y: number, w: number, h: number, parentId?: string) => ({ type, parentId, measured: { width: w, height: h }, internals: { positionAbsolute: { x, y } } })
  const lookup = new Map([
    ['g1', node('boundary', 0, 0, 400, 300)], // holds the source
    ['src', node('card', 20, 120, 100, 50, 'g1')],
    ['g2', node('boundary', 600, 0, 400, 300)], // holds the target
    ['dst', node('card', 620, 120, 100, 50, 'g2')],
    ['g3', node('boundary', 0, 400, 400, 300)], // someone else's cluster
    ['mid', node('card', 20, 500, 100, 50, 'g3')],
    ['ns', node('namespace', 10, 440, 200, 120, 'g3')],
  ]) as never
  const everything = { minX: -1e6, maxX: 1e6, minY: -1e6, maxY: 1e6 }

  test('skips the two ends and the boxes that hold them; another cluster counts as a whole wall by default', () => {
    const o = collectObstacles(lookup, 'src', 'dst', everything)
    expect(o).toContainEqual({ x: 0, y: 400, w: 400, h: 300 })
    expect(o).toContainEqual({ x: 20, y: 500, w: 100, h: 50 })
    expect(o).toHaveLength(3) // g3, the namespace and the card; not src, dst, g1, g2
  })

  test('squared: a cluster or namespace is only its header (the text), and its whole box is returned apart, as soft', () => {
    const o = collectObstacles(lookup, 'src', 'dst', everything, true, true)
    expect(o).toContainEqual({ x: 0, y: 400, w: 400, h: HEADER })
    expect(o).toContainEqual({ x: 10, y: 440, w: 200, h: NS_HEADER })
    expect(o).toContainEqual({ x: 20, y: 500, w: 100, h: 50 }) // a card is always solid
    expect(o.filter((b) => b.soft)).toEqual([{ x: 0, y: 400, w: 400, h: 300, soft: true }, { x: 10, y: 440, w: 200, h: 120, soft: true }])
  })

  test('only boxes near the two ends are looked at', () => {
    const o = collectObstacles(lookup, 'src', 'dst', { minX: 0, maxX: 700, minY: 0, maxY: 300 })
    expect(o).toEqual([])
  })
})

describe('cachedOrtho', () => {
  // A hit hands back the very array it stored, so identity tells a hit from a fresh search.
  const route = (i: number) => cachedOrtho({ x: i * 10, y: 0 }, { x: 1, y: 0 }, { x: i * 10 + 400, y: 100 }, { x: -1, y: 0 }, [])

  test('a line that is read again stays cached while other lines churn past the cap', () => {
    setOrthoCacheCap(5)
    const kept = route(1000)
    for (let i = 0; i < 50; i++) {
      route(i) // a line near the dragged card: a new key every frame
      expect(route(1000)).toBe(kept) // an untouched line, read every frame
    }
  })

  test('the cap follows the edge count, so a pass over 500 lines is all hits the next time', () => {
    expect(orthoCacheCapFor(10)).toBe(400)
    expect(orthoCacheCapFor(500)).toBeGreaterThanOrEqual(500)
    setOrthoCacheCap(orthoCacheCapFor(500))
    const first = Array.from({ length: 500 }, (_, i) => route(2000 + i))
    first.forEach((r, i) => expect(route(2000 + i)).toBe(r))
  })
})

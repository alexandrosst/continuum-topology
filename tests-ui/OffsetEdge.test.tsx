import { describe, expect, test } from 'vitest'
import {
  avoidHeaderSide,
  collectObstacles,
  curvedPath,
  elbowPath,
  ARRIVAL_DEPTH,
  insetFromCorners,
  intersection,
  outwardNormal,
  OBSTACLE_MARGIN,
  pickClearSide,
  pullBackEnds,
  SIDE_INSET,
  sideIsClear,
  sideMidpoint,
  slideOnSide,
} from '@/components/topology/OffsetEdge'
import { HEADER } from '@/lib/graph'

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

describe('elbowPath (the opt-in squared-but-soft alternative to curvedPath)', () => {
  const outUp = { x: 0, y: -1 }
  const outDown = { x: 0, y: 1 }
  const outLeft = { x: -1, y: 0 }
  const outRight = { x: 1, y: 0 }

  test('starts and ends exactly at the given anchor points', () => {
    const { path } = elbowPath(0, 0, 300, 200, outDown, outUp)
    expect(path.startsWith('M0,0 ')).toBe(true)
    expect(path.endsWith('L300,200')).toBe(true) // the final point is never touched by corner-rounding
  })

  test('both normals vertical: routes with a vertical-jog-vertical shape regardless of which axis the raw distance favors', () => {
    // dx (300) far exceeds dy (20) here - the old dx-vs-dy heuristic would have picked a horizontal-jog
    // route and approached the target sideways, even though targetNormal (top) means it should be entered
    // from directly above. This is the exact shape of the reported bug: an edge to a target sitting in
    // roughly the same row, but whose own natural entry side is still its top edge.
    const { path } = elbowPath(0, 0, 300, 20, outDown, outUp)
    const nums = path.match(/-?[\d.]+/g)!.map(Number)
    const pts: [number, number][] = []
    for (let i = 0; i < nums.length; i += 2) pts.push([nums[i], nums[i + 1]])
    // The very first and very last points of the path are still the true anchors.
    expect(pts[0]).toEqual([0, 0])
    expect(pts.at(-1)).toEqual([300, 20])
    // The final approach into the target is vertical (same x as the target for the point just before it),
    // not horizontal - i.e. it arrives from above, matching outUp, rather than skimming in from the side.
    const penultimate = pts.at(-2)!
    expect(penultimate[0]).toBeCloseTo(300, 5)
  })

  test('both normals horizontal: routes with a horizontal-jog-horizontal shape regardless of which axis the raw distance favors', () => {
    // Mirror of the above: dy (300) far exceeds dx (20), but both normals are horizontal, so the route
    // must still approach the target sideways (same y as the target just before the end), not from above.
    const { path } = elbowPath(0, 0, 20, 300, outRight, outLeft)
    const nums = path.match(/-?[\d.]+/g)!.map(Number)
    const pts: [number, number][] = []
    for (let i = 0; i < nums.length; i += 2) pts.push([nums[i], nums[i + 1]])
    expect(pts[0]).toEqual([0, 0])
    expect(pts.at(-1)).toEqual([20, 300])
    const penultimate = pts.at(-2)!
    expect(penultimate[1]).toBeCloseTo(300, 5)
  })

  test('mixed normals (vertical source, horizontal target): a single-bend L route, no redundant jog', () => {
    const { path } = elbowPath(0, 0, 300, 200, outDown, outLeft)
    // Exactly one interior corner (one Q command), not the two a jog route would have - a single bend is
    // all a mixed-axis route needs, since each leg already runs along a different anchor's own normal.
    expect((path.match(/Q/g) ?? []).length).toBe(1)
    // The corner sits directly below the source (same x) and level with the target (same y): the first
    // leg is vertical (per sourceNormal=down), the last leg horizontal (per targetNormal=left). A Q
    // command's own first coordinate pair is the exact, un-rounded corner point.
    const [, cx, cy] = path.match(/Q(-?[\d.]+),(-?[\d.]+)/)!
    expect(Number(cx)).toBeCloseTo(0, 5)
    expect(Number(cy)).toBeCloseTo(200, 5)
    expect(path.endsWith('L300,200')).toBe(true)
  })

  test('mixed normals (horizontal source, vertical target): the mirrored single-bend L route', () => {
    const { path } = elbowPath(0, 0, 300, 200, outRight, outUp)
    expect((path.match(/Q/g) ?? []).length).toBe(1)
    const [, cx, cy] = path.match(/Q(-?[\d.]+),(-?[\d.]+)/)!
    expect(Number(cx)).toBeCloseTo(300, 5)
    expect(Number(cy)).toBeCloseTo(0, 5)
    expect(path.endsWith('L300,200')).toBe(true)
  })

  test('label sits on the middle leg for a 4-point jog route, clear of either anchor', () => {
    const { labelX, labelY } = elbowPath(0, 0, 300, 200, outDown, outUp)
    expect(labelY).toBeCloseTo(100, 5) // the jog's own y, between 0 and 200
    expect(labelX).toBeGreaterThan(0)
    expect(labelX).toBeLessThan(300)
  })

  test('label sits on the longer leg for a 3-point single-bend route', () => {
    // Source-to-corner leg (0,0)->(0,200): length 200. Corner-to-target leg (0,200)->(300,200): length 300.
    // The longer one wins, so the label should be the midpoint of the second leg, not the first.
    const { labelX, labelY } = elbowPath(0, 0, 300, 200, outDown, outLeft)
    expect(labelX).toBeCloseTo(150, 5)
    expect(labelY).toBeCloseTo(200, 5)
  })
  describe('with boxes in the way', () => {
    // Every vertex of the route, read back out of the SVG path (corners are rounded, so only the straight parts matter: the
    // first and last of each leg are enough to see which boxes the legs run through).
    const legs = (path: string) => {
      const nums = path.match(/-?[\d.]+/g)!.map(Number)
      const pts: { x: number; y: number }[] = []
      for (let i = 0; i < nums.length; i += 2) pts.push({ x: nums[i], y: nums[i + 1] })
      return pts
    }
    const crosses = (pts: { x: number; y: number }[], o: { x: number; y: number; w: number; h: number }) => {
      // Straight legs only: the rounded corners add `Q` control points that sit on the vertices, so consecutive points are the legs.
      for (let i = 1; i < pts.length; i++) {
        const a = pts[i - 1]
        const b = pts[i]
        if (Math.min(a.x, b.x) < o.x + o.w && Math.max(a.x, b.x) > o.x && Math.min(a.y, b.y) < o.y + o.h && Math.max(a.y, b.y) > o.y) return true
      }
      return false
    }

    test('a box straight down the vertical leg is gone around, through a gap beside it', () => {
      const wall = { x: -60, y: 80, w: 120, h: 80 } // sits across x=0, between the two rows
      const plain = elbowPath(0, 0, 0, 300, outDown, outUp)
      expect(crosses(legs(plain.path), wall)).toBe(true) // what the unaware route did
      const { path, labelX } = elbowPath(0, 0, 0, 300, outDown, outUp, [wall])
      expect(crosses(legs(path), wall)).toBe(false)
      expect(path.startsWith('M0,0 ')).toBe(true)
      expect(path.endsWith('L0,300')).toBe(true) // still ends exactly on the anchor
      expect(Math.abs(labelX)).toBeGreaterThan(60) // the label rides the channel, not the box
    })

    test('the horizontal case is the same, with the axes swapped', () => {
      const wall = { x: 80, y: -60, w: 80, h: 120 }
      const { path } = elbowPath(0, 0, 300, 0, outRight, outLeft, [wall])
      expect(crosses(legs(path), wall)).toBe(false)
      expect(path.endsWith('L300,0')).toBe(true)
    })

    test('a route nothing is in the way of is left exactly as it was', () => {
      const away = { x: 500, y: 0, w: 50, h: 50 }
      expect(elbowPath(0, 0, 300, 200, outDown, outUp, [away])).toEqual(elbowPath(0, 0, 300, 200, outDown, outUp))
    })
  })
})


describe('collectObstacles (which nodeLookup entries count as a routing obstacle)', () => {
  // A minimal fake InternalNode - just the fields collectObstacles itself reads.
  const node = (type: string, parentId: string | undefined, x: number, y: number, w = 100, h = 60) => ({
    type,
    parentId,
    measured: { width: w, height: h },
    internals: { positionAbsolute: { x, y } },
  })
  const bounds = { minX: -1000, maxX: 1000, minY: -1000, maxY: 1000 }

  test('a group/namespace box that contains the source or target is excluded, not counted against itself', () => {
    const nodeLookup = new Map(
      Object.entries({
        'grp-a': node('boundary', undefined, 0, 0, 400, 400),
        'ns-a': node('namespace', 'grp-a', 10, 10, 200, 200),
        src: node('card', 'ns-a', 20, 20),
        'grp-b': node('boundary', undefined, 600, 0, 400, 400),
        tgt: node('card', 'grp-b', 620, 20),
      }),
    )
    const obstacles = collectObstacles(nodeLookup, 'src', 'tgt', bounds)
    expect(obstacles).toEqual([])
  })

  test('a sibling group/namespace box neither end is inside counts as a real obstacle', () => {
    const nodeLookup = new Map(
      Object.entries({
        'grp-a': node('boundary', undefined, 0, 0, 200, 200),
        src: node('card', 'grp-a', 20, 20),
        'grp-mid': node('boundary', undefined, 250, 0, 200, 200), // between src and tgt, neither's ancestor
        'grp-b': node('boundary', undefined, 500, 0, 200, 200),
        tgt: node('card', 'grp-b', 520, 20),
      }),
    )
    const obstacles = collectObstacles(nodeLookup, 'src', 'tgt', bounds)
    expect(obstacles).toContainEqual({ x: 250, y: 0, w: 200, h: 200 })
    // Neither end's own container (nor the ends themselves) is in the result.
    expect(obstacles).toHaveLength(1)
  })

  test('a node type that is none of card/boundary/namespace is never an obstacle (e.g. a cluster-link group box)', () => {
    const nodeLookup = new Map(
      Object.entries({
        src: node('card', undefined, 0, 0),
        tgt: node('card', undefined, 500, 0),
        other: node('regionalOperator', undefined, 250, 0),
      }),
    )
    expect(collectObstacles(nodeLookup, 'src', 'tgt', bounds)).toEqual([])
  })

  test('a candidate outside the given bounds is skipped even though it would otherwise qualify', () => {
    const nodeLookup = new Map(
      Object.entries({
        src: node('card', undefined, 0, 0),
        tgt: node('card', undefined, 500, 0),
        far: node('card', undefined, 5000, 5000),
      }),
    )
    expect(collectObstacles(nodeLookup, 'src', 'tgt', bounds)).toEqual([])
  })

  test('an unmeasured node (width/height not yet known) is skipped rather than producing a zero-size obstacle', () => {
    const nodeLookup = new Map(
      Object.entries({
        src: node('card', undefined, 0, 0),
        tgt: node('card', undefined, 500, 0),
        unmeasured: { type: 'card', parentId: undefined, measured: {}, internals: { positionAbsolute: { x: 250, y: 0 } } },
      }),
    )
    expect(collectObstacles(nodeLookup, 'src', 'tgt', bounds)).toEqual([])
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

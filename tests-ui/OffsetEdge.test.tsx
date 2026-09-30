import { describe, expect, test } from 'vitest'
import {
  clampTowardNormal,
  curvedPath,
  elbowPath,
  intersection,
  outwardNormal,
  OBSTACLE_MARGIN,
  pickClearSide,
  pullBackEnds,
  sideIsClear,
  sideMidpoint,
} from '@/components/topology/OffsetEdge'

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

describe('clampTowardNormal (keeps a direction within a cone of another, per the UI/UX pass)', () => {
  const maxAngle = (30 * Math.PI) / 180

  test('a direction already inside the cone is returned unchanged', () => {
    // 10 degrees off (1,0) - well inside a 30 degree cone.
    const dir = { x: Math.cos((10 * Math.PI) / 180), y: Math.sin((10 * Math.PI) / 180) }
    expect(clampTowardNormal(dir, { x: 1, y: 0 }, maxAngle)).toEqual(dir)
  })

  test('exactly at the cone\'s own edge is left unchanged too (the check is inclusive)', () => {
    const dir = { x: Math.cos(maxAngle), y: Math.sin(maxAngle) }
    expect(clampTowardNormal(dir, { x: 1, y: 0 }, maxAngle)).toEqual(dir)
  })

  test('a direction outside the cone is rotated back to exactly the cone\'s edge, on the same side it was on', () => {
    // 80 degrees off (1,0), on the +y side - clamped to exactly 30 degrees off (1,0), same (+y) side.
    const dir = { x: Math.cos((80 * Math.PI) / 180), y: Math.sin((80 * Math.PI) / 180) }
    const result = clampTowardNormal(dir, { x: 1, y: 0 }, maxAngle)
    expect(result.x).toBeCloseTo(Math.cos(maxAngle), 5)
    expect(result.y).toBeCloseTo(Math.sin(maxAngle), 5) // positive: stayed on dir's own side, not flipped
  })

  test('clamps toward the other side too, symmetrically', () => {
    const dir = { x: Math.cos((-80 * Math.PI) / 180), y: Math.sin((-80 * Math.PI) / 180) }
    const result = clampTowardNormal(dir, { x: 1, y: 0 }, maxAngle)
    expect(result.x).toBeCloseTo(Math.cos(maxAngle), 5)
    expect(result.y).toBeCloseTo(-Math.sin(maxAngle), 5)
  })

  test('a direction pointing the opposite way (180 degrees off) still lands exactly on the cone, not somewhere undefined', () => {
    const result = clampTowardNormal({ x: -1, y: 0 }, { x: 1, y: 0 }, maxAngle)
    expect(Math.hypot(result.x, result.y)).toBeCloseTo(1, 5) // still a unit vector
    const angle = Math.acos(result.x * 1 + result.y * 0)
    expect(angle).toBeCloseTo(maxAngle, 5)
  })
})

describe('curvedPath (OffsetEdge\'s asymmetric cubic-bow path math)', () => {
  const outUp = { x: 0, y: -1 }
  const outRight = { x: 1, y: 0 }
  // The target box's own outward normal for every test below except the shallow-angle ones: these all run
  // a line from (0,0) to (300,0), i.e. a target approached head-on from the left, so the box's outward
  // normal at that entry point faces left - comfortably far from every natural tangent these tests exercise,
  // so passing it never triggers the MAX_ARROWHEAD_APPROACH_ANGLE clamp and these keep testing exactly the
  // un-clamped math they did before curvedPath grew a target-angle clamp at all.
  const outLeft = { x: -1, y: 0 }

  test('starts and ends exactly at the given anchor points, and is a genuine cubic (two control points)', () => {
    const { path } = curvedPath(0, 0, 300, 0, 0, 1, outUp, outLeft)
    expect(path.startsWith('M0,0 ')).toBe(true)
    expect(path.endsWith(' 300,0')).toBe(true)
    expect(path).toMatch(/^M[\d.,-]+ C[\d.,-]+ [\d.,-]+ [\d.,-]+$/)
  })

  test('the initial tangent leaves straight out along sourceNormal, not toward the target', () => {
    // sourceNormal points straight "up" - away from the target, which is off to the right - so the
    // near-source control point should sit directly above the source anchor, not pulled sideways at all.
    const { path } = curvedPath(0, 0, 300, 0, 0, 1, outUp, outLeft)
    const [, c1x, c1y] = path.match(/C([\d.-]+),([\d.-]+)/)!
    expect(Number(c1x)).toBeCloseTo(0, 5) // no sideways pull toward the target
    expect(Number(c1y)).toBeLessThan(0) // straight up, per outUp
  })

  test('the target-end control point is unaffected by sourceNormal - still the old gentle bow toward nx/ny', () => {
    const up = curvedPath(0, 0, 300, 0, 0, 1, outUp, outLeft)
    const right = curvedPath(0, 0, 300, 0, 0, 1, outRight, outLeft)
    const secondControl = (p: { path: string }) => p.path.match(/C[\d.-]+,[\d.-]+ ([\d.-]+),([\d.-]+)/)!.slice(1).map(Number)
    expect(secondControl(up)).toEqual(secondControl(right)) // same regardless of the source's own exit direction
  })

  test('bow (the target-side control offset) is proportional to length but capped, so a very long edge stays a subtle arc', () => {
    const short = curvedPath(0, 0, 100, 0, 0, 1, outUp, outLeft)
    const long = curvedPath(0, 0, 100_000, 0, 0, 1, outUp, outLeft)
    const bowOf = (p: { path: string }) => Number(p.path.match(/C[\d.-]+,[\d.-]+ [\d.-]+,([\d.-]+)/)![1])
    expect(bowOf(short)).toBeCloseTo(100 * 0.12, 5) // under the cap: exactly proportional
    expect(bowOf(long)).toBeCloseTo(36, 5) // over the cap: clamped, not thousands of pixels
  })

  test('the source-side exit stub is proportional to length but capped, independent of the bow', () => {
    const short = curvedPath(0, 0, 100, 0, 0, 1, outUp, outLeft)
    const long = curvedPath(0, 0, 100_000, 0, 0, 1, outUp, outLeft)
    const exitOf = (p: { path: string }) => -Number(p.path.match(/C([\d.-]+),([\d.-]+)/)![2]) // outUp: c1y = y1 - exit
    expect(exitOf(short)).toBeCloseTo(100 * 0.35, 5) // under the cap: exactly proportional
    expect(exitOf(long)).toBeCloseTo(40, 5) // over the cap: clamped
  })

  test('label sits on the actual cubic curve at t=0.5, not the straight-line midpoint', () => {
    const { path, labelX, labelY } = curvedPath(0, 0, 300, 0, 0, 1, outRight, outLeft)
    const [, c1x, c1y, mx, my] = path.match(/C([\d.-]+),([\d.-]+) ([\d.-]+),([\d.-]+)/)!.map(Number)
    // Cubic Bezier at t=0.5: (P0 + 3*C1 + 3*C2 + P2) / 8
    expect(labelX).toBeCloseTo((0 + 3 * c1x + 3 * mx + 300) / 8, 5)
    expect(labelY).toBeCloseTo((0 + 3 * c1y + 3 * my + 0) / 8, 5)
    expect(labelY).toBeGreaterThan(0) // pulled toward the bow (+y), not sitting on the straight line's y=0
  })

  test('when both the bow and the source\'s own exit direction lie exactly along the line, the whole curve sits on that line', () => {
    // outRight is parallel to the source->target direction itself here, and nx=ny=0 means no perpendicular
    // bow either - so every point of the cubic, including the label, has y=0, even though (being an
    // asymmetric cubic, not a symmetric one) the label isn't exactly at the line's geometric midpoint.
    const { labelX, labelY } = curvedPath(0, 0, 300, 0, 0, 0, outRight, outLeft)
    expect(labelY).toBeCloseTo(0, 5)
    expect(labelX).toBeCloseTo(108.75, 5)
  })

  test('a target-end tangent that would otherwise graze near-tangential to the target box is clamped, so the arrowhead can\'t dip inside it', () => {
    // The target box's outward normal points straight up here (approached from underneath), but nx/ny bows
    // the curve hard off to the side (perpendicular to the source->target line, which itself runs straight
    // right) - so the *natural*, unclamped tangent would arrive almost sideways-on to the box, well past
    // MAX_ARROWHEAD_APPROACH_ANGLE off the box's own inward direction (straight down).
    const targetNormal = { x: 0, y: 1 } // box's own outward normal: straight down in SVG's y-down space
    const inward = { x: 0, y: -1 }
    const { path } = curvedPath(0, 0, 300, 0, 0, 1, outUp, targetNormal)
    const [x2, y2] = [300, 0]
    const [, , , mx, my] = path.match(/C([\d.-]+),([\d.-]+) ([\d.-]+),([\d.-]+)/)!.map(Number)
    // The tip itself never moves - only the curve's approach to it does.
    expect(path.endsWith(` ${x2},${y2}`)).toBe(true)
    const tdx = x2 - mx
    const tdy = y2 - my
    const tlen = Math.hypot(tdx, tdy)
    const angle = Math.acos((tdx / tlen) * inward.x + (tdy / tlen) * inward.y)
    const MAX_ARROWHEAD_APPROACH_ANGLE = (38 * Math.PI) / 180
    expect(angle).toBeLessThanOrEqual(MAX_ARROWHEAD_APPROACH_ANGLE + 1e-9) // clamped, not left grazing
    expect(angle).toBeCloseTo(MAX_ARROWHEAD_APPROACH_ANGLE, 5) // and clamped exactly to the limit, not overcorrected
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

describe('curvedPath obstacle avoidance (task #382: keeps an arrowhead\'s curve from sweeping into an unrelated card)', () => {
  const outUp = { x: 0, y: -1 }
  const outDown = { x: 0, y: 1 }

  test('with no obstacles in the way, the curve is identical to the no-obstacle-argument call', () => {
    const withoutArg = curvedPath(0, 0, 300, 0, 0, 1, outUp, outUp)
    const withEmpty = curvedPath(0, 0, 300, 0, 0, 1, outUp, outUp, [])
    const farAway = curvedPath(0, 0, 300, 0, 0, 1, outUp, outUp, [{ x: 10_000, y: 10_000, w: 10, h: 10 }])
    expect(withEmpty.path).toBe(withoutArg.path)
    expect(farAway.path).toBe(withoutArg.path)
  })

  test('an obstacle squarely in the default curve\'s path pushes the bow wide enough to clear it', () => {
    // A box sitting right on the straight-line midpoint between source and target, where the default
    // (unescalated) bow would otherwise sweep through it.
    const obstacle = { x: 130, y: -20, w: 40, h: 40 }
    const { path } = curvedPath(0, 0, 300, 0, 0, 1, outUp, outDown, [obstacle])
    const [, , , mx, my] = path.match(/C([\d.-]+),([\d.-]+) ([\d.-]+),([\d.-]+)/)!.map(Number)
    // The escalation search should have moved the bow's middle control point away from the obstacle's
    // vertical span (y: -20 to 20) rather than leaving it at the tiny default bow (which sits inside it).
    expect(Math.abs(my)).toBeGreaterThan(20)
  })

  test('an obstacle far off to the side never perturbs the curve at all', () => {
    const clear = curvedPath(0, 0, 300, 0, 0, 1, outUp, outDown)
    const withSideObstacle = curvedPath(0, 0, 300, 0, 0, 1, outUp, outDown, [{ x: 130, y: 5000, w: 40, h: 40 }])
    expect(withSideObstacle.path).toBe(clear.path)
  })

  test('when every escalation candidate still collides, it settles on the one with the fewest hits rather than the raw default', () => {
    // A obstacle wide enough to straddle the entire route corridor - no sideways bow can fully dodge it,
    // matching the genuinely-unavoidable "obstacle wider than the route" case documented in OffsetEdge.
    const wideObstacle = { x: -50, y: -15, w: 400, h: 30 }
    const defaultBow = curvedPath(0, 0, 300, 0, 0, 1, outUp, outDown)
    const withObstacle = curvedPath(0, 0, 300, 0, 0, 1, outUp, outDown, [wideObstacle])
    // It's still a valid, well-formed curve anchored at the same two points - escalation degrades to
    // "least bad" rather than throwing or leaving the path malformed.
    expect(withObstacle.path.startsWith('M0,0 ')).toBe(true)
    expect(withObstacle.path.endsWith(' 300,0')).toBe(true)
    // And it actually tried something different from the plain default, even though it couldn't fully clear it.
    expect(withObstacle.path).not.toBe(defaultBow.path)
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
    const blockingTop = { x: 100, y: 100 - OBSTACLE_MARGIN / 2, w: 200, h: 40 }
    const result = pickClearSide(box, naturalTop, [blockingTop])
    expect(result).not.toEqual(naturalTop)
    // Should land on left or right, not bottom (the opposite side is only tried after both perpendiculars).
    const isLeft = result.point.x === box.x && result.normal.x === -1
    const isRight = result.point.x === box.x + box.w && result.normal.x === 1
    expect(isLeft || isRight).toBe(true)
  })

  test('when top, left, and right are all blocked, falls back to the opposite (bottom) side', () => {
    const blockTop = { x: 100, y: 100 - OBSTACLE_MARGIN / 2, w: 200, h: 40 }
    const blockLeft = { x: 100 - OBSTACLE_MARGIN / 2, y: 100, w: 40, h: 80 }
    const blockRight = { x: 300 - 40 + OBSTACLE_MARGIN / 2, y: 100, w: 40, h: 80 }
    const result = pickClearSide(box, naturalTop, [blockTop, blockLeft, blockRight])
    expect(result.point).toEqual(sideMidpoint(box, 'bottom'))
    expect(result.normal).toEqual({ x: 0, y: 1 })
  })

  test('when every side is blocked, falls back to the natural point rather than picking an obstructed one', () => {
    const depth = OBSTACLE_MARGIN
    const blockAll = [
      { x: box.x, y: box.y - depth / 2, w: box.w, h: 20 },
      { x: box.x, y: box.y + box.h + depth / 2 - 10, w: box.w, h: 20 },
      { x: box.x - depth / 2, y: box.y, w: 20, h: box.h },
      { x: box.x + box.w + depth / 2 - 10, y: box.y, w: 20, h: box.h },
    ]
    expect(pickClearSide(box, naturalTop, blockAll)).toEqual(naturalTop)
  })
})

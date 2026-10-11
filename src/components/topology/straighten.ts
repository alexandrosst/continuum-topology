/**
 * Taking the kink out of a nearly straight line.
 *
 * Where a box has several lines on one side they are fanned along it, so one end of a line can sit a few pixels off the
 * other end's straight line to it. Routed as is, that is a short jog (out, a few px sideways, on) that reads as a mistake. When
 * the whole route is just such a jog, the end that is not fanned (or the one fanned less) moves along its own side to meet the
 * other, and the line becomes one straight run.
 */
import type { Pt, Rect } from './nudge'

/** A jog up to this wide (px) is straightened; a wider one is a real detour. */
export const MAX_JOG = 12
/** How far in from a corner an end may be moved to. */
const INSET = 12

export type Ends = { source: Rect; target: Rect; sourceOffset: number; targetOffset: number }

/** Replaces `pts` (a route of four points: out, across, in) by its straight form when it is a small jog; returns whether it did. */
export function straightenJog(pts: Pt[], ends: Ends): boolean {
  if (pts.length !== 4) return false
  const [a, b, c, d] = pts
  // Vertical legs joined by a short horizontal run, or the other way round.
  const vertical = Math.abs(a.x - b.x) < 1e-6 && Math.abs(c.x - d.x) < 1e-6 && Math.abs(b.y - c.y) < 1e-6
  const horizontal = Math.abs(a.y - b.y) < 1e-6 && Math.abs(c.y - d.y) < 1e-6 && Math.abs(b.x - c.x) < 1e-6
  if (!vertical && !horizontal) return false
  const delta = vertical ? d.x - a.x : d.y - a.y
  if (Math.abs(delta) < 1e-6 || Math.abs(delta) > MAX_JOG) return false

  const fits = (box: Rect, v: number) => (vertical ? v >= box.x + INSET && v <= box.x + box.w - INSET : v >= box.y + INSET && v <= box.y + box.h - INSET)
  const moveTarget = fits(ends.target, vertical ? a.x : a.y)
  const moveSource = fits(ends.source, vertical ? d.x : d.y)
  // The less fanned end moves; a tie moves the target.
  const preferTarget = Math.abs(ends.targetOffset) <= Math.abs(ends.sourceOffset)
  if (moveTarget && (preferTarget || !moveSource)) {
    if (vertical) {
      c.x = a.x
      d.x = a.x
    } else {
      c.y = a.y
      d.y = a.y
    }
    return true
  }
  if (moveSource) {
    if (vertical) {
      a.x = d.x
      b.x = d.x
    } else {
      a.y = d.y
      b.y = d.y
    }
    return true
  }
  return false
}

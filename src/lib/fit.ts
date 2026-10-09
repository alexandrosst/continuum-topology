/** Below this canvas width (px) the page is a phone: a whole-graph fit is too small to read there. */
export const NARROW_VIEW = 640

/** The zoom a fit should use. Fitting the whole graph into a narrow window leaves it tiny, so there the widest box
 *  sets the zoom instead (its width fills the view, capped at 1); the rest of the graph is a pan away. A fit that is
 *  already larger, or any window that is not narrow, keeps its own zoom. */
export function narrowFitZoom(viewWidth: number, wholeZoom: number, widestBox: number, sideMargin = 0.04): number {
  if (viewWidth >= NARROW_VIEW || widestBox <= 0) return wholeZoom
  return Math.max(wholeZoom, Math.min(1, (viewWidth * (1 - 2 * sideMargin)) / widestBox))
}

/**
 * The toolbar is a container (`@container/bar`), so what it shows follows the room it has, not the window: with the Inspector open it is narrower than the
 * screen says. Wide, a button carries its word; narrow, it is its icon alone (its name stays for assistive technology and the tooltip), and the bar stays
 * one row instead of wrapping and moving the canvas under it. A finger gets 44px either way.
 */
export const BAR_BUTTON = 'px-2.5 @min-[800px]/bar:px-4 pointer-coarse:min-h-11 pointer-coarse:min-w-11'
/** A button's word: drawn only where the bar is wide. */
export const BAR_WORD = 'hidden @min-[800px]/bar:inline'

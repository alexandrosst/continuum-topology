import { useLayoutEffect } from 'react'
import { holdReload } from '@/lib/staleBuild'

/**
 * Keeps the page from reloading itself for a new version (see staleBuild.ts) while the calling component is mounted and `active`:
 * for a dialog that shows a secret once, where a reload would lose it before it was copied. The hold is taken in the commit that puts the
 * dialog on the screen (a layout effect), not after the browser has painted it: there is no moment in which the secret is shown and a
 * reload is still allowed.
 */
export function useHoldReload(active = true): void {
  useLayoutEffect(() => (active ? holdReload() : undefined), [active])
}

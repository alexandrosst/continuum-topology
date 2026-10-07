import { useEffect } from 'react'
import { holdReload } from '@/lib/staleBuild'

/**
 * Keeps the page from reloading itself for a new version (see staleBuild.ts) while the calling component is mounted and `active`:
 * for a dialog that shows a secret once, where a reload would lose it before it was copied.
 */
export function useHoldReload(active = true): void {
  useEffect(() => (active ? holdReload() : undefined), [active])
}

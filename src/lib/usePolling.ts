import { useEffect, useRef } from 'react'

/**
 * Calls `tick` every `ms` while the tab is visible - and not at all while it is hidden, which is where a page left open in a
 * background tab would otherwise keep asking the server forever. Coming back to the tab ticks once straight away (what it shows
 * may be minutes old) and restarts the clock. `ms = null` switches it off. `tick` is read through a ref, so a new function each
 * render does not restart the timer.
 */
export function useVisiblePolling(tick: () => void, ms: number | null): void {
  const latest = useRef(tick)
  useEffect(() => {
    latest.current = tick
  })
  useEffect(() => {
    if (ms === null) return
    let timer: ReturnType<typeof setInterval> | undefined
    const stop = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
    }
    const start = () => {
      stop()
      timer = setInterval(() => latest.current(), ms)
    }
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') return stop()
      latest.current()
      start()
    }
    if (document.visibilityState !== 'hidden') start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [ms])
}

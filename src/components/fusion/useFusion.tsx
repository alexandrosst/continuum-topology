import { useCallback, useEffect, useRef, useState } from 'react'
import { LiveDot, type LiveKind } from '@/components/ui/primitives'
import { api, ApiError, type FusionStatus } from '@/lib/api'
import { type FusionKind, fusionSentence } from '@/lib/fusionStatus'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** How often the status is read again while FUSION is coming up or needs a look: images are being pulled and volumes bound,
 *  and a person is usually watching. */
export const POLL_SETTLING_MS = 4000
/** And while it simply runs: only to keep "last data 12 s ago" and the parts honest, never while the tab is hidden. */
export const POLL_RUNNING_MS = 30_000

/**
 * The bundled FUSION's state and its switch, as a hook both the page's card and the new-operator wizard share.
 * `enable` and `disable` resolve to the new status (or throw its message); the status is read again every few
 * seconds while FUSION is starting, and only then. `onChanged` is called once a switch has resolved.
 */
export function useFusion(enabled = true, onChanged?: () => void) {
  const conn = useServer((s) => s.conn)
  // The organisation in use (see usePolledList): another one's FUSION must not stay on screen after a switch.
  const org = useServer((s) => s.orgId)
  // Told once a switch has resolved, so a list that depends on FUSION being on (the central operator appears with it) is read again.
  const changed = useRef(onChanged)
  useEffect(() => {
    changed.current = onChanged
  })
  const [status, setStatus] = useState<FusionStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const alive = useRef(true)
  // Only the newest read (or switch) may write its answer: a slow poll that left before Enable was clicked must not land after
  // it and put "Off" back on screen.
  const newest = useRef(0)
  // Set on mount as well as cleared on unmount: React's StrictMode runs the cleanup once between two mounts, and a flag only ever
  // cleared would leave every later state update silently dropped.
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const shownOrg = useRef(org)
  useEffect(() => {
    if (shownOrg.current === org) return
    shownOrg.current = org
    setStatus(null)
    setError('')
  }, [org])

  const refresh = useCallback(async () => {
    const c = conn()
    if (!c || !enabled) return
    const mine = ++newest.current
    try {
      const s = await api.getFusion(c)
      if (alive.current && mine === newest.current) {
        setStatus(s)
        setError('')
      }
    } catch (e) {
      if (alive.current && mine === newest.current) setError(e instanceof ApiError ? e.message : 'Could not read the state of FUSION.')
    }
  // org is not read in here (conn() reads it at call time); it is a dependency so that switching organisation
  // makes a new callback, and with it a fresh read, instead of leaving the old organisation's data on screen.
  }, [conn, enabled, org])
  useEffect(() => {
    void refresh()
  }, [refresh])

  // Quickly while FUSION is coming up (or a part is still on its way - Grafana starts after the stores and can be slow to pull) or
  // needs a look; slowly while it runs; not at all while it is off. A first read that failed is tried again slowly.
  const kind = fusionSentence(status).kind
  const settling = kind === 'starting' || kind === 'attention' || (kind === 'running' && (status?.components ?? []).some((c) => c.desired > 0 && c.ready < c.desired))
  const pollMs = !enabled ? null : settling ? POLL_SETTLING_MS : kind === 'running' || (status === null && error !== '') ? POLL_RUNNING_MS : null
  useVisiblePolling(() => void refresh(), pollMs)

  const act = useCallback(async (f: (c: NonNullable<ReturnType<typeof conn>>) => Promise<FusionStatus>, fallback: string) => {
    const c = conn()
    if (!c) return null
    setBusy(true)
    setError('')
    ++newest.current
    try {
      const s = await f(c)
      // The switch's own answer is the freshest there is: reads that left before it returned are older than it.
      ++newest.current
      if (alive.current) setStatus(s)
      changed.current?.()
      return s
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : fallback
      if (alive.current) setError(msg)
      throw new Error(msg)
    } finally {
      if (alive.current) setBusy(false)
    }
  }, [conn])

  return {
    status,
    busy,
    error,
    refresh,
    enable: () => act((c) => api.enableFusion(c), 'Could not turn FUSION on.'),
    disable: () => act((c) => api.disableFusion(c), 'Could not turn FUSION off.'),
  }
}

/** FUSION's status as a dot, in the same vocabulary as an operator's health (LiveDot): green and pulsing while it runs, a hollow
 *  fading ring while it starts, amber when it needs attention, hollow grey when it is off, being checked or cannot be switched. */
export function FusionDot({ kind, className }: { kind: FusionKind; className?: string }) {
  const live: LiveKind = kind === 'running' ? 'online' : kind === 'starting' ? 'starting' : kind === 'attention' ? 'late' : 'idle'
  return <LiveDot kind={live} className={className} />
}

/** A clock that moves on its own, so "last data 12 s ago" keeps counting between two reads of the status. */
export function useNow(everyMs: number, active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(t)
  }, [everyMs, active])
  return now
}

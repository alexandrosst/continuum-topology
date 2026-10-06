import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, type Conn } from '@/lib/api'
import type { OperatorDestinationEntry, RegionalOperator, TelemetryIntent } from '@/lib/types'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** How often the operator lists are read again. Health changes within a minute or two, so a slower poll would show a stale dot,
 *  and a faster one asks the server for nothing new. */
export const OPERATORS_POLL_MS = 12_000

interface Loaded<T> {
  items: T[]
  /** True once the first answer (or the first failure) has come back - until then an empty list means "not known yet". */
  loaded: boolean
  error: string
  reload: () => Promise<void>
}

/** One list that stays current: read once, then every OPERATORS_POLL_MS while the tab is visible. Only the newest request may
 *  write its answer, so a slow one that left before a reload cannot land after it and bring back what it replaced. */
function usePolledList<T>(read: (c: Conn) => Promise<T[]>, enabled: boolean, failure: string): Loaded<T> {
  const conn = useServer((s) => s.conn)
  // `conn` is one stable function that reads the organisation in use when it is called, so a switch of organisation is invisible to it:
  // the organisation itself is what makes the list start over, instead of showing the last one's operators until the next poll.
  const org = useServer((s) => s.orgId)
  const [items, setItems] = useState<T[]>([])
  const [loaded, setLoaded] = useState(!enabled)
  const [error, setError] = useState('')
  const newest = useRef(0)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  const readRef = useRef(read)
  useEffect(() => {
    readRef.current = read
  })
  const shownOrg = useRef(org)
  useEffect(() => {
    if (shownOrg.current === org) return
    shownOrg.current = org
    setItems([])
    setError('')
    setLoaded(!enabled)
  }, [org, enabled])

  const reload = useCallback(async () => {
    const c = conn()
    if (!c || !enabled) return
    const mine = ++newest.current
    try {
      const next = await readRef.current(c)
      if (!alive.current || mine !== newest.current) return
      // The same answer keeps the same array: a poll that found nothing new must not make everything built from the list start over
      // (the topology canvas lays itself out again whenever its operators change identity).
      setItems((prev) => (JSON.stringify(prev) === JSON.stringify(next) ? prev : next))
      setError('')
    } catch (e) {
      if (!alive.current || mine !== newest.current) return
      setError(e instanceof ApiError ? e.message : failure)
    } finally {
      if (alive.current && mine === newest.current) setLoaded(true)
    }
  // org is not read in here (conn() reads it at call time); it is a dependency so that switching organisation
  // makes a new callback, and with it a fresh read, instead of leaving the old organisation's data on screen.
  }, [conn, enabled, failure, org])
  useEffect(() => {
    void reload()
  }, [reload])
  useVisiblePolling(() => void reload(), enabled ? OPERATORS_POLL_MS : null)
  return { items, loaded, error, reload }
}

/** The organisation's regional operators (administrators only: GET /operators is). Polled, so a health dot that changes
 *  while the page is open changes on it. */
export function useOperators(enabled = true): Omit<Loaded<RegionalOperator>, 'items'> & { operators: RegionalOperator[] } {
  const { items, ...rest } = usePolledList((c) => api.listOperators(c), enabled, 'Could not load the regional operators.')
  return { operators: items, ...rest }
}

/** What the destination picker offers, readable by editors as well: active operators and the central one, no secrets. */
export function useOperatorDestinations(enabled = true): Omit<Loaded<OperatorDestinationEntry>, 'items'> & { destinations: OperatorDestinationEntry[] } {
  const { items, ...rest } = usePolledList((c) => api.listOperatorDestinations(c), enabled, 'Could not load the destinations.')
  return { destinations: items, ...rest }
}

/** The telemetry intents of every agent (what each was asked to collect and where to send it), kept current the same way. */
export function useTelemetryIntents(enabled = true): Omit<Loaded<TelemetryIntent>, 'items'> & { intents: TelemetryIntent[] } {
  const { items, ...rest } = usePolledList((c) => api.listTelemetryIntents(c), enabled, 'Could not load the telemetry requests.')
  return { intents: items, ...rest }
}

import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError } from './api'
import type { AddressTest } from './types'
import { useServer } from '@/store/server'

/** How often the agent is asked what it found, and for how long: its first round starts a few seconds after it is handed the address. */
const POLL_MS = 2000
const POLL_ROUNDS = 30

/** An address tried from the cluster an agent runs in. `timedOut` is set when no answer came in the time allowed (the agent may be offline). */
export type AddressTestView = AddressTest & { timedOut?: boolean }

/**
 * Tests addresses from the cluster of `agentId`: starts them, then reads what the agent found until each has an answer.
 * Results are kept by address. Without an agent (no cluster chosen yet) nothing can be tested and `available` is false.
 */
export function useAddressTests(agentId: string | undefined) {
  const conn = useServer((s) => s.conn)
  const [byAddress, setByAddress] = useState<Record<string, AddressTestView>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const rounds = useRef(0)
  const live = useRef(true)
  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
    }
  }, [])
  // Another cluster is another set of answers.
  useEffect(() => {
    setByAddress({})
    setError('')
  }, [agentId])

  const merge = useCallback((tests: AddressTest[]) => {
    setByAddress((cur) => {
      const next = { ...cur }
      for (const t of tests) {
        // A newer test of the same address replaces an older one; an older one never replaces a newer.
        if (!next[t.address] || next[t.address].created <= t.created) next[t.address] = t
      }
      return next
    })
  }, [])

  const start = useCallback(
    async (addresses: string[]) => {
      const c = conn()
      if (!c || !agentId || busy || addresses.length === 0) return
      setBusy(true)
      setError('')
      try {
        const r = await api.startAddressTests(c, agentId, addresses)
        if (!live.current) return
        rounds.current = 0
        merge(r.tests)
      } catch (e) {
        if (live.current) setError(e instanceof ApiError || e instanceof Error ? e.message : 'The test could not be started.')
      } finally {
        if (live.current) setBusy(false)
      }
    },
    [agentId, busy, conn, merge],
  )

  const pending = Object.values(byAddress).some((t) => t.state === 'pending' && !t.timedOut)
  useEffect(() => {
    if (!pending || !agentId) return
    const id = setInterval(async () => {
      const c = conn()
      if (!c) return
      rounds.current += 1
      try {
        const r = await api.addressTests(c, agentId)
        if (live.current) merge(r.tests)
      } catch {
        // The next poll tries again; running out of rounds says no answer came.
      }
      if (rounds.current >= POLL_ROUNDS && live.current) {
        setByAddress((cur) => Object.fromEntries(Object.entries(cur).map(([k, t]) => [k, t.state === 'pending' ? { ...t, timedOut: true } : t])))
      }
    }, POLL_MS)
    return () => clearInterval(id)
  }, [pending, agentId, conn, merge])

  return { available: !!agentId, tests: byAddress, start, busy, error }
}

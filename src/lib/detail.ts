// How much the topology canvas says at rest. "Calm" (the default) draws what identifies a thing and what is wrong
// with it, and keeps the rest for hover, selection and zoom; "Full" is the canvas as it was before the choice
// existed. A person's choice lives in the URL like every other view option (?detail=full) and in saved views.
import { createContext } from 'react'
import type { PodsView } from './pods'
import type { Status } from './types'

export type Detail = 'calm' | 'full'

export const parseDetail = (v: string | null | undefined): Detail => (v === 'full' ? 'full' : 'calm')

/** Where a person's own Detail choice is kept in this browser (a convenience, never state that matters). */
export const DETAIL_KEY = 'continuum:topology-detail'

/** What the canvas draws: the URL when it says (a shared link, a saved view), else what this person chose last time, else Calm. */
export const resolveDetail = (url: string | null, stored: string | null): Detail => (url === 'calm' || url === 'full' ? url : parseDetail(stored))

/** Which presentation the canvas is drawing, for the nodes and edges React Flow renders far from the page. */
export const DetailContext = createContext<Detail>('calm')

/** How loud a problem is: `warn` is "needs a look" (amber), `bad` is "broken" (red) - the app's own two state colours. */
export type Alert = 'warn' | 'bad'

/** Share of a resource already promised from which a machine or cluster counts as under pressure (the same 70/90 the far-zoom badge uses). */
export const PRESSURE_WARN = 70
export const PRESSURE_BAD = 90

export const alertOfStatus = (s: Status): Alert | undefined => (s === 'offline' ? 'bad' : s === 'degraded' ? 'warn' : undefined)

export const alertOfLoad = (peakPct: number | undefined): Alert | undefined =>
  peakPct === undefined ? undefined : peakPct >= PRESSURE_BAD ? 'bad' : peakPct >= PRESSURE_WARN ? 'warn' : undefined

/** Pods that are not all ready are a warning; a crash loop is worse. A scaling pod (`recent`) is news, not a problem. */
export function alertOfPods(pods: PodsView | undefined): Alert | undefined {
  if (!pods) return undefined
  if (pods.bad > 0) return 'bad'
  return pods.ready < pods.total || pods.warn > 0 ? 'warn' : undefined
}

export const worstAlert = (...xs: (Alert | undefined)[]): Alert | undefined => (xs.includes('bad') ? 'bad' : xs.includes('warn') ? 'warn' : undefined)

import { ageOf } from '@/lib/history'
import { TONE_CLASS } from '@/lib/provenance'
import type { Agent } from '@/lib/types'

export const when = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '—')

export const STATUS_STYLE: Record<Agent['status'], string> = {
  approved: TONE_CLASS.ok,
  pending: TONE_CLASS.warn,
  revoked: TONE_CLASS.bad,
  rejected: TONE_CLASS.bad,
  expired: TONE_CLASS.muted,
}

export const MODULE_STYLE = { ok: 'text-emerald-300', skipped: 'text-nb-500', error: 'text-red-300' } as const

/** One line about the traffic observer: which method runs on how many nodes, or how to turn it on. */
export function ObserverLine({ agent: a }: { agent: Agent }) {
  const o = a.observer
  if (!o) {
    if (a.status !== 'approved' || a.accessTier < 2) return null
    return <div className="mt-1 text-xs text-nb-500" data-testid="observer-hint">No traffic observer reporting. Enable it in the install options to see which services talk to which.</div>
  }
  const ebpf = o.collectors.filter((c) => c.method === 'ebpf').length
  const ct = o.collectors.filter((c) => c.method === 'conntrack').length
  const parts = [ebpf > 0 && `eBPF on ${ebpf} node${ebpf === 1 ? '' : 's'}`, ct > 0 && `conntrack on ${ct} node${ct === 1 ? '' : 's'}`].filter(Boolean).join(', ')
  const noBytes = o.collectors.length > 0 && o.collectors.every((c) => !c.bytesKnown)
  return (
    <div className="mt-1 text-xs text-nb-500" data-testid="observer-line">
      <span className="text-nb-300">Traffic observer:</span> {parts || 'no collectors'}
      {noBytes && ' · connection counts only (byte accounting is off on the nodes)'}
      {o.lost > 0 && ` · ${o.lost} observation${o.lost === 1 ? '' : 's'} dropped`}
      {' · last report '}{when(o.lastReport)}
    </div>
  )
}

/** Whether the server's picture of this cluster matched the cluster's own at the last full check, and whether it is timing paths. */
export function CheckLine({ agent: a }: { agent: Agent }) {
  if (a.status !== 'approved') return null
  const c = a.consistency
  return (
    <div className="mt-1 text-xs text-nb-500" data-testid="check-line">
      <span className="text-nb-300">Consistency check:</span>{' '}
      {c ? (
        <>
          {c.differences === 0 ? <span className="text-emerald-300">matched</span> : <span className="text-amber-300">{c.differences} thing{c.differences === 1 ? '' : 's'} had been missed and {c.differences === 1 ? 'was' : 'were'} corrected</span>}
          {' · '}{ageOf(c.lastCheck)} · {c.checks} check{c.checks === 1 ? '' : 's'} so far
        </>
      ) : (
        'not yet run for this connection'
      )}
      {a.accessTier >= 2 && (
        <>
          {' · '}
          <span className="text-nb-300">Path measurements:</span> {(a.measuring ?? 0) > 0 ? `timing ${a.measuring} address${a.measuring === 1 ? '' : 'es'}` : 'off, or nothing to time'}
        </>
      )}
    </div>
  )
}


/** Which namespaces this agent reports: all of them, or the ones it was told to. Counts only; a left-out name never reaches the server. */
export function ScopeLine({ agent: a }: { agent: Agent }) {
  if (a.status !== 'approved') return null
  const sc = a.scope
  // Hand-made and sample agents never reported a scope; a real one that sends none is not narrowing anything.
  if (!sc && !a.kubernetesVersion) return null
  return (
    <div className="mt-1 text-xs text-nb-500" data-testid="scope-line">
      <span className="text-nb-300">Namespaces:</span>{' '}
      {!sc ? (
        'all of them'
      ) : sc.inScope >= sc.namespaces && !sc.description ? (
        `all ${sc.namespaces}`
      ) : (
        <>
          <span className="text-amber-300">{sc.inScope} of {sc.namespaces}</span>
          {sc.description ? ` · ${sc.description}` : ''}
          {' · the rest never leave the cluster'}
        </>
      )}
    </div>
  )
}

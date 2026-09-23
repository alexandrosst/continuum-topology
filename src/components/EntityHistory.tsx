import { History, UserRound } from 'lucide-react'
import { useEffect, useState } from 'react'
import { SkeletonLines } from '@/components/ui/primitives'
import { api, type Timeline } from '@/lib/api'
import { kindLabel } from '@/lib/history'
import { useConn, useServer } from '@/store/server'

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const show = (v: unknown) => {
  if (v === undefined || v === null || v === '') return '—'
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  return s.length > 60 ? `${s.slice(0, 57)}…` : s
}

/** The kinds of record the graph keeps a history for. */
export const HISTORY_KINDS = new Set(['cluster', 'node', 'service', 'external'])

/**
 * One record's life, from the graph: every state it was in, what changed between them, the events about it,
 * and what people did to it. Loaded only when asked for, and only offered when the server keeps a graph.
 */
export default function EntityHistory({ kind, id }: { kind: string; id: string }) {
  const conn = useConn()
  const status = useServer((s) => s.status)
  const [enabled, setEnabled] = useState(false)
  const [tl, setTl] = useState<Timeline | null>(null)
  const [state, setState] = useState<'idle' | 'loading' | 'none' | 'error'>('idle')

  useEffect(() => {
    let live = true
    if (status === 'connected') api.storage(conn).then((s) => live && setEnabled(s.backend === 'neo4j' && !!s.ready)).catch(() => live && setEnabled(false))
    return () => {
      live = false
    }
  }, [conn, status])
  if (!enabled || !HISTORY_KINDS.has(kind)) return null

  const load = async () => {
    setState('loading')
    try {
      setTl(await api.timeline(conn, kind, id))
      setState('idle')
    } catch (e) {
      setState((e as { status?: number }).status === 404 ? 'none' : 'error')
    }
  }

  return (
    <div className="border-t border-nb-850 px-5 py-4" data-testid="entity-history">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">History</div>
      {!tl && state === 'idle' && (
        <button onClick={() => void load()} className="flex items-center gap-2 text-sm text-accent hover:underline" data-testid="entity-history-load">
          <History size={14} aria-hidden /> Show how this changed over time
        </button>
      )}
      {state === 'loading' && <SkeletonLines lines={3} className="max-w-sm" />}
      {state === 'none' && <p className="text-sm text-nb-500">Nothing has been recorded for this yet. It appears after the next recording.</p>}
      {state === 'error' && <p className="text-sm text-red-300" role="alert">The history could not be read.</p>}
      {tl && (
        <div className="space-y-4">
          <ol className="space-y-3" data-testid="entity-versions">
            {tl.versions.map((v, i) => (
              <li key={v.from} className="border-l border-nb-800 pl-3">
                <div className="text-xs text-nb-500">
                  {when(v.from)} {v.to ? `→ ${when(v.to)}` : <span className="text-emerald-300">→ now</span>}
                </div>
                <div className="text-sm text-nb-300">
                  {i === tl.versions.length - 1 ? 'First recorded' : 'Changed'}
                  {v.status ? <span className="ml-2 text-xs text-nb-500">{v.status}</span> : null}
                </div>
                {v.changes.length > 0 && (
                  <ul className="mt-1 space-y-0.5 text-xs text-nb-400">
                    {v.changes.map((c) => (
                      <li key={c.field} className="break-words">
                        <span className="text-nb-300">{c.field}</span>: {show(c.from)} <span className="text-nb-600">→</span> {show(c.to)}
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ol>
          {tl.events.length > 0 && (
            <div>
              <div className="mb-1 text-xs text-nb-500">What Continuum noticed</div>
              <ul className="space-y-1 text-xs text-nb-400">
                {tl.events.map((e) => (
                  <li key={e.id}>
                    <span className="text-nb-500">{when(e.at)}</span> · {kindLabel(e.kind)}{e.detail ? `: ${e.detail}` : ''}
                    {e.cause && <span className="italic text-nb-500"> (likely {e.cause})</span>}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {tl.audit.length > 0 && (
            <div>
              <div className="mb-1 text-xs text-nb-500">What people did</div>
              <ul className="space-y-1 text-xs text-nb-400" data-testid="entity-audit">
                {tl.audit.map((a) => (
                  <li key={a.id} className="flex items-baseline gap-1.5">
                    <UserRound size={11} className="shrink-0 translate-y-0.5 text-nb-500" aria-hidden />
                    <span><span className="text-nb-300">{a.actor}</span> {a.action}{a.detail ? ` — ${a.detail}` : ''} <span className="text-nb-500">{when(a.at)}</span></span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

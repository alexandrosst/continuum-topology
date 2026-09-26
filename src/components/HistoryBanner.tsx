import { ChevronLeft, ChevronRight, History } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Button } from '@/components/ui/primitives'
import { ageOf } from '@/lib/history'
import { useHistoryView } from '@/store/history'
import { useConn } from '@/store/server'

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/**
 * Shown on every page while a recorded moment is displayed instead of now. It says what is and is not from
 * that moment, lets you step between recordings, and is the way back. Edits are refused while it is up.
 */
export default function HistoryBanner() {
  const { at, points, loading, error, refusedAt, view, live, loadPoints } = useHistoryView()
  const conn = useConn()
  const [flash, setFlash] = useState(false)
  useEffect(() => {
    if (at && points.length === 0) void loadPoints(conn)
  }, [at, points.length, loadPoints, conn])
  useEffect(() => {
    if (!refusedAt) return
    setFlash(true)
    const id = setTimeout(() => setFlash(false), 3500)
    return () => clearTimeout(id)
  }, [refusedAt])
  const i = useMemo(() => (at ? points.findIndex((p) => p.at === at) : -1), [at, points])
  if (!at) return null
  const step = (d: -1 | 1) => {
    const n = points[i + d]
    if (n) void view(conn, n.at)
  }
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-warn/30 bg-warn/10 px-4 py-2.5 text-sm text-nb-300 sm:px-6" data-testid="history-banner">
      <History size={16} className="shrink-0 text-warn" aria-hidden />
      <span className="min-w-[55%] flex-1 basis-64">
        <strong className="font-medium text-nb-300">The estate as it was on {when(at)}</strong> <span className="text-nb-400">({ageOf(at)})</span>. Read-only: clusters, nodes, services and traffic are from that moment;
        names, sites, applications and policy you set are as they are now.
        {flash && <span className="ml-2 font-medium text-warn" data-testid="history-refused">Nothing was changed: return to now to edit.</span>}
        {error && <span className="ml-2 text-bad">{error}</span>}
      </span>
      <span className="flex items-center gap-1.5">
        <Button size="sm" onClick={() => step(-1)} disabled={loading || i <= 0} aria-label="Previous recording" title="Previous recording">
          <ChevronLeft size={14} />
        </Button>
        <Button size="sm" onClick={() => step(1)} disabled={loading || i < 0 || i >= points.length - 1} aria-label="Next recording" title="Next recording">
          <ChevronRight size={14} />
        </Button>
        <Button size="sm" variant="primary" onClick={live} data-testid="history-return">Return to now</Button>
      </span>
    </div>
  )
}

import { useCallback, useEffect, useState } from 'react'
import EntityHealthCard from '@/components/health/EntityHealthCard'
import { EmptyState, ErrorBanner, PageHeader, SkeletonBlock, SkeletonLines } from '@/components/ui/primitives'
import { api, type Conn } from '@/lib/api'
import { entityLabel, type SelfTelemetryEntity } from '@/lib/selfHealth'
import { useConn, useServer } from '@/store/server'

// Diagnostic, not primary, data - a touch coarser than HistoryPage's own 15s poll (itself already coarser
// than the main topology poll's 2-5s - see Layout.tsx's useServerPolling) felt like the right convention
// to match here rather than inventing a third cadence from nothing.
const POLL_MS = 15_000

/**
 * What running Continuum itself costs, broken out per entity (the server process, and every connected
 * agent/cluster) - see backend/internal/server/admin_telemetry.go's own doc comment for exactly what the
 * API behind this returns and why. Read-only and diagnostic: nothing here is editable, and polling stays
 * coarse on purpose (see POLL_MS above).
 */
export default function SystemHealthPage() {
  const status = useServer((s) => s.status)
  const conn = useConn()
  if (status !== 'connected') {
    return (
      <>
        <PageHeader title="System Health" description="What running Continuum itself costs, in your own clusters." />
        <EmptyState
          title="System Health needs a server"
          description="Connect to a server to see what its own process, and every connected agent's, is actually costing - memory, goroutines, CPU, and (when the hardware exposes them) network and power share."
        />
      </>
    )
  }
  return <Connected conn={conn} />
}

function entityOrder(a: SelfTelemetryEntity, b: SelfTelemetryEntity): number {
  if (a.kind !== b.kind) return a.kind === 'server' ? -1 : 1
  return entityLabel(a).localeCompare(entityLabel(b))
}

function Connected({ conn }: { conn: Conn }) {
  const [entities, setEntities] = useState<SelfTelemetryEntity[] | null>(null)
  const [error, setError] = useState<string>()

  const load = useCallback(async () => {
    try {
      const rows = await api.selfTelemetry(conn)
      setEntities(rows)
      setError(undefined)
    } catch (e) {
      // The copy already held stays visible; the next poll tries again - the same convention
      // store/server.ts's own refresh() uses for a transient hiccup, rather than yanking a working
      // dashboard blank over one missed poll.
      setError(e instanceof Error ? e.message : 'Self-telemetry could not be read.')
    }
  }, [conn])

  useEffect(() => {
    void load()
    const id = setInterval(() => document.visibilityState === 'visible' && void load(), POLL_MS)
    return () => clearInterval(id)
  }, [load])

  return (
    <>
      <PageHeader
        title="System Health"
        description="What running Continuum itself costs, in your own clusters: each agent's and the server's own memory, goroutines and CPU, plus network and power share where the hardware exposes them. Nothing here is synthetic - a metric this hardware can't report stays visibly unavailable rather than showing a guess."
      />
      {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}
      {entities === null ? (
        <div className="flex flex-col gap-6" aria-busy>
          {[0, 1].map((i) => (
            <div key={i} className="rounded-2xl border border-nb-850 bg-nb-920 p-5">
              <SkeletonLines lines={1} className="mb-4 w-48" />
              <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {[0, 1, 2].map((j) => (
                  <SkeletonBlock key={j} className="h-40 w-full rounded-xl" />
                ))}
              </div>
            </div>
          ))}
        </div>
      ) : entities.length === 0 ? (
        <EmptyState
          title="Nothing sampled yet"
          description="This appears once the server or a connected agent reports at least one self-telemetry reading - normally within its first sample cycle."
        />
      ) : (
        <div className="flex flex-col gap-6">
          {[...entities].sort(entityOrder).map((e) => (
            <EntityHealthCard key={e.id} entity={e} />
          ))}
        </div>
      )}
    </>
  )
}

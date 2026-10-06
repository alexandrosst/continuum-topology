import { lazy, Suspense, useCallback, useState, type ReactNode } from 'react'
import { WizardLoading } from '@/components/discovery/ConnectFlow'
import { useServer } from '@/store/server'

// The wizard is large and most visits never open it.
const TelemetryWizard = lazy(() => import('@/components/telemetry/TelemetryWizard'))

/**
 * "Configure telemetry" is one flow with several entry points - the Agents page header button (no target
 * chosen yet), the topology canvas's "Define scope from selection" quick action (a target and scope already
 * known), "Connect <cluster>" on an operator (a target and the operator to send to already known: `initialDestination` is that
 * operator's id), and the inline "Change telemetry" disclosure on an already-expanded agent row, which stays a
 * separate, simpler path (see TelemetryPanel). Call `start()` from a button; render `dialogs` once on the
 * page that owns this hook. Mirrors useConnectFlow's shape in ConnectFlow.tsx, but gated on editor-level
 * access (`canEdit`), the same bar TelemetryPanel/ConsentPanel already use - not useConnectFlow's own
 * admin-level `canStart`, since configuring telemetry is an editor-level action today, not an
 * administrative one.
 *
 * Deliberately no `?connect=1`-style URL auto-open: nothing here needs a bookmarkable telemetry-wizard URL.
 * The wizard's own empty-state handles "no cluster connected yet" by navigating to `/agents?connect=1`
 * itself, rather than this hook owning a second `useConnectFlow` instance - see TelemetryWizard.tsx.
 */
export function useTelemetryFlow(): {
  start: (agentId?: string, initialScope?: { name: string; namespaces: string[] }, initialDestination?: string) => void
  dialogs: ReactNode
  canStart: boolean
} {
  const server = useServer()
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState<{ agentId?: string; initialScope?: { name: string; namespaces: string[] }; initialDestination?: string }>({})
  const canStart = server.status === 'connected' && server.canEdit()

  const start = useCallback((agentId?: string, initialScope?: { name: string; namespaces: string[] }, initialDestination?: string) => {
    setPending({ agentId, initialScope, initialDestination })
    setOpen(true)
  }, [])

  const close = useCallback(() => setOpen(false), [])

  const dialogs = open && (
    <Suspense fallback={<WizardLoading />}>
      <TelemetryWizard open={open} onClose={close} agentId={pending.agentId} initialScope={pending.initialScope} initialDestination={pending.initialDestination} />
    </Suspense>
  )

  return { start, dialogs, canStart }
}

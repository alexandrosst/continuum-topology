import { ChevronLeft, Radio } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import { Button, EmptyState, ICON_SM, Modal, TierBadge } from '@/components/ui/primitives'
import { ACCESS_TIERS } from '@/lib/types'
import { extrasOf } from '@/lib/consent'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

const tierLabel = (t: number) => ACCESS_TIERS.find((x) => x.value === t)?.label ?? `Tier ${t}`

/**
 * The decoupled telemetry entry point: "Configure telemetry" on the Agents page (no target chosen yet),
 * and the topology canvas's "Define scope from selection" (a target and scope already known). Two phases:
 * `pick` a cluster when none was named, then `configure` its telemetry through the same TelemetryPanel form
 * the inline "Change telemetry" disclosure already uses (see AgentInsight.tsx), rendered `standalone` here
 * since this modal's whole purpose is that form.
 *
 * Discovery is a prerequisite, not a bundled step: this wizard never creates or approves an agent itself.
 * With no approved agent yet, the picker becomes an empty state pointing at `/agents?connect=1`, the same
 * handoff ConnectClusterWizard's own "View in topology" button already uses in reverse - not a second
 * `useConnectFlow` instance, which could otherwise race this one to open ConnectClusterWizard twice.
 */
export default function TelemetryWizard({
  open,
  onClose,
  agentId,
  initialScope,
}: {
  open: boolean
  onClose: () => void
  /** A target already known (from the topology canvas) - skips straight to the `configure` phase. */
  agentId?: string
  /** A scope draft handed off from the topology's "Define scope from selection" quick action. */
  initialScope?: { name: string; namespaces: string[] }
}) {
  const navigate = useNavigate()
  const { agents, clusters } = useTopology()
  const rawAgents = useServer((s) => s.state?.agents)
  const install = useServer((s) => s.info?.install)
  const approved = agents.filter((a) => a.status === 'approved')

  // Which agent this wizard is configuring, once resolved. Seeded from `agentId` each time the wizard
  // opens (mirrors ConnectClusterWizard's own open-keyed seeding effects) - a fresh open should always
  // reflect whatever the caller asked for, not whatever was picked last time this instance was open.
  const [pickedId, setPickedId] = useState(agentId)
  useEffect(() => {
    if (open) setPickedId(agentId)
  }, [open, agentId])

  const target = approved.find((a) => a.id === pickedId)
  const targetCluster = clusters.find((c) => c.id === target?.clusterId)
  // The picker shows whenever no target is currently resolved - whether that's because the wizard opened
  // with no agentId at all, or because "Change cluster" (below) reset the picked id back to undefined. It
  // does NOT re-derive from `agentId` alone, or picking a cluster from the list would never leave the
  // picker: `pickedId` is the only thing that decides this once the wizard is open.
  const showPicker = !target

  const goConnect = () => {
    onClose()
    navigate('/agents?connect=1')
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      width="max-w-xl"
      title="Configure telemetry"
      description="Send metrics, logs or traces from an already-connected cluster to an observability backend you run. This never changes what the discovery agent itself may see."
      footer={
        showPicker ? (
          <Button onClick={onClose}>Cancel</Button>
        ) : (
          // Secondary, not primary: this only closes the dialog - nothing here is "submitted" to a server,
          // the actual output of this form is the generated helm command a person copies from TelemetryPanel
          // below, so a bold "Done" button competing with the guided wizard's own "Continue" would overstate
          // what clicking it actually does, and could read as the form's real call to action when it isn't.
          <Button onClick={onClose} data-testid="telemetry-wizard-done">Done</Button>
        )
      }
    >
      {showPicker ? (
        approved.length === 0 ? (
          <EmptyState
            title="Connect a cluster first"
            description="Telemetry is configured per cluster, and there is no approved cluster yet. Discovery comes first - connect one, then come back here."
            action={<Button variant="primary" onClick={goConnect} data-testid="telemetry-wizard-connect"><Radio size={ICON_SM} /> Connect a cluster</Button>}
          />
        ) : (
          <div>
            <p className="mb-2 text-xs text-nb-500">Pick which cluster's telemetry to configure.</p>
            <div className="max-h-80 space-y-1.5 overflow-y-auto" data-testid="telemetry-wizard-picker">
              {approved.map((a) => {
                const c = clusters.find((cl) => cl.id === a.clusterId)
                return (
                  <button
                    key={a.id}
                    type="button"
                    className="block w-full rounded-lg border border-nb-850 bg-nb-925 px-4 py-3 text-left hover:bg-nb-930"
                    onClick={() => setPickedId(a.id)}
                    data-testid="telemetry-wizard-target"
                  >
                    <div className="flex items-center justify-between gap-3">
                      <span className="text-sm font-medium text-nb-300">{a.name}</span>
                      <span className="text-xs text-nb-500">{tierLabel(a.accessTier)}</span>
                    </div>
                    {c && (
                      <div className="mt-1 flex items-center gap-2 text-xs text-nb-500">
                        <TierBadge tier={c.tier} /> {c.name}
                      </div>
                    )}
                  </button>
                )
              })}
            </div>
          </div>
        )
      ) : (
        <div>
          {!agentId && approved.length > 1 && (
            <button type="button" className="mb-3 inline-flex items-center gap-1 text-xs text-nb-500 hover:text-nb-300" onClick={() => setPickedId(undefined)} data-testid="telemetry-wizard-back">
              <ChevronLeft size={ICON_SM} /> Change cluster
            </button>
          )}
          <div className="mb-3 text-sm text-nb-300">
            <span className="font-medium">{target?.name}</span>
            {targetCluster && <span className="text-nb-500"> · {targetCluster.name}</span>}
          </div>
          <TelemetryPanel
            diagnostics={extrasOf(rawAgents, target?.id ?? '').diagnostics}
            install={install}
            initialScope={initialScope}
            standalone
            testIdPrefix="telemetry-wizard"
          />
        </div>
      )}
    </Modal>
  )
}

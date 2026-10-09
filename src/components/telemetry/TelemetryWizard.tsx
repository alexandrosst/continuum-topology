import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import { Button, Modal, WizardSteps } from '@/components/ui/primitives'
import { extrasOf } from '@/lib/consent'
import { SETUP_STEPS } from '@/lib/telemetrySetup'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'
import ClusterStep from './ClusterStep'

/**
 * "Set up telemetry": the guided setup in a dialog, for the Agents page header button (no cluster chosen yet), the topology canvas's
 * "Define scope from selection" (a cluster and scope already known) and "Connect <cluster>" on an operator (a cluster and the destination
 * known). Two parts of the same four-step rail: "Where from" picks the cluster when none was named, then the other three steps are the
 * TelemetryPanel's guided setup, the same component the inline panel on an agent row uses.
 *
 * Discovery is a prerequisite, not a bundled step: this never creates or approves an agent itself. With no approved agent yet, "Where from"
 * is an empty state pointing at `/agents?connect=1`, the same handoff ConnectClusterWizard's own "View in topology" button already uses in
 * reverse - not a second `useConnectFlow` instance, which could otherwise race this one to open ConnectClusterWizard twice.
 */
export default function TelemetryWizard({
  open,
  onClose,
  agentId,
  initialScope,
  initialDestination,
}: {
  open: boolean
  onClose: () => void
  /** A cluster's agent already known (from the topology canvas) - skips "Where from". */
  agentId?: string
  /** A scope draft handed off from the topology's "Define scope from selection" quick action. */
  initialScope?: { name: string; namespaces: string[] }
  /** The id of the operator (or FUSION) to send to, already chosen by whoever opened this - see GuidedWizard. */
  initialDestination?: string
}) {
  const navigate = useNavigate()
  const { agents, clusters } = useTopology()
  const rawAgents = useServer((s) => s.state?.agents)
  const install = useServer((s) => s.info?.install)
  const approved = agents.filter((a) => a.status === 'approved')

  // The cluster highlighted in "Where from", and whether it has been confirmed with Continue. Seeded each time the dialog opens (mirrors
  // ConnectClusterWizard's own open-keyed seeding): a fresh open reflects what the caller asked for, not what was picked last time. With
  // exactly one cluster there is nothing to choose, so it starts picked.
  const [pickedId, setPickedId] = useState<string | undefined>(agentId)
  const [confirmed, setConfirmed] = useState(!!agentId)
  useEffect(() => {
    if (!open) return
    setPickedId(agentId ?? (approved.length === 1 ? approved[0].id : undefined))
    setConfirmed(!!agentId)
    // Only when the dialog opens or is pointed at another agent: a list that changes under an open dialog must not reset a pick in progress.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, agentId])

  const target = confirmed ? approved.find((a) => a.id === pickedId) : undefined
  // "Where from" shows whenever no target is confirmed - whether the dialog opened with no agentId at all, or "Change cluster" went back to it.
  const showPicker = !target

  const goConnect = () => {
    onClose()
    navigate('/agents?connect=1')
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      width="max-w-2xl"
      title="Set up telemetry"
      description="Send metrics, logs or traces from a connected cluster to a place you choose. This never changes what the discovery agent itself may see."
      footer={
        showPicker ? (
          <>
            <Button onClick={onClose}>Cancel</Button>
            {approved.length > 0 && (
              <Button variant="primary" onClick={() => setConfirmed(true)} disabled={!pickedId} data-testid="telemetry-wizard-continue">Continue</Button>
            )}
          </>
        ) : undefined
      }
    >
      {showPicker ? (
        <div className="space-y-4">
          <WizardSteps steps={SETUP_STEPS} currentIndex={0} testId="telemetry-wizard-steps" />
          <ClusterStep agents={approved} clusters={clusters} rawAgents={rawAgents} selected={pickedId} onSelect={setPickedId} onConnect={goConnect} testIdPrefix="telemetry-wizard" />
        </div>
      ) : (
        <TelemetryPanel
          diagnostics={extrasOf(rawAgents, target.id).diagnostics}
          install={install}
          agentId={target.id}
          clusterId={target.clusterId}
          target={{ namespace: target.namespace, release: target.releaseName }}
          initialScope={initialScope}
          initialDestination={initialDestination}
          onBackToCluster={agentId ? undefined : () => setConfirmed(false)}
          onDone={onClose}
          standalone
          testIdPrefix="telemetry-wizard"
        />
      )}
    </Modal>
  )
}

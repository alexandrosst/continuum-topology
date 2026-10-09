import { Plug } from 'lucide-react'
import type { ReactNode } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { FusionDot } from '@/components/fusion/useFusion'
import { HeartbeatCommands } from '@/components/operators/HeartbeatCommands'
import { FindTheAddress } from '@/components/operators/OperatorAddress'
import { Button, ICON_SM, Modal, RunStep } from '@/components/ui/primitives'
import type { CreatedOperator, OperatorExposure } from '@/lib/api'
import { CENTRAL_OPERATOR_ID, type FusionKind } from '@/lib/fusionStatus'
import { useHoldReload } from '@/lib/useHoldReload'
import { receiverAuthOf } from '@/lib/operatorHealth'
import { buildOperatorInstallCommand } from '@/lib/operatorInstall'
import type { ProcessorEntry } from '@/lib/processorCatalog'
import { useTopology } from '@/store/topology'

/**
 * What to run for an operator, in the order to run it, shown once: the server keeps only a hash of any receiver token and of the
 * health credential, so this is the only chance to copy them - same "shown once, gone forever" convention as TeamPage's InviteCreated.
 * Every Secret is a step of its own, ahead of the install that refers to it; a certificate-gated operator (`token` absent) has no
 * receiver token at all. The same screen shows what "Renew certificates" returns (`mode: 'reinstalled'`): the same commands, minted again.
 */
export default function OperatorCreated({
  created,
  extraProcessors = [],
  exposure = 'cluster',
  mode = 'created',
  fusionKind,
  onConnect,
  onClose,
}: {
  created: CreatedOperator
  extraProcessors?: ProcessorEntry[]
  exposure?: OperatorExposure
  mode?: 'created' | 'reinstalled'
  /** FUSION's state, when this operator sends to it: a starting FUSION is said to be nothing to wait for. */
  fusionKind?: FusionKind
  /** Starts the telemetry wizard for one source cluster's agent, with this operator already chosen. */
  onConnect?: (agentId: string) => void
  onClose: () => void
}) {
  const { agents, clusters } = useTopology()
  // Shown once: the server keeps only a hash. A backdrop click or Escape must not throw it away, and neither may the page reloading itself for a
  // new version underneath it; the footer's Done is the one way out, and it asks nothing.
  useHoldReload()
  const install = buildOperatorInstallCommand(created.install, extraProcessors)
  const hasToken = !!created.token && !!created.secretCommand
  const mtls = !hasToken && receiverAuthOf(created.operator) === 'mtls'
  const heartbeat = !!created.heartbeatToken && !!created.heartbeatSecretCommand
  const shownOnce = [hasToken && 'the receiver token', heartbeat && 'the health credential'].filter(Boolean).join(' and ')
  const renewed = mode === 'reinstalled'

  // One entry per step, built in the order they are run; the numbers follow from the list, so a step that does not apply leaves no gap.
  const steps: { key: string; title: string; body: ReactNode }[] = []
  if (hasToken) {
    steps.push({ key: 'token', title: 'Create the receiver token Secret', body: <CopyCommand text={created.secretCommand!} testId="operator-secret-command" label="Copy the receiver token Secret command" /> })
  }
  if (created.tlsSecretCommand) {
    steps.push({
      key: 'tls',
      title: 'Create the receiver’s TLS certificate Secret',
      body: (
        <>
          {mtls ? 'It is what the receiver checks agents’ certificates against.' : 'mTLS on top of the token above; the install command already turns it on.'}
          <CopyCommand text={created.tlsSecretCommand} testId="operator-tls-secret-command" label="Copy the receiver TLS Secret command" />
        </>
      ),
    })
  }
  if (heartbeat) {
    steps.push({
      key: 'heartbeat',
      title: 'Create the health credential Secret',
      body: (
        <>
          <HeartbeatCommands
            testId="operator-created-heartbeat"
            embedded
            secretCommand={created.heartbeatSecretCommand!}
            warning={created.heartbeatWarning}
            url={created.heartbeatUrl ?? ''}
            intervalSeconds={created.heartbeatIntervalSeconds ?? 60}
          />
          <span className="mt-1 block">The install command below already turns health reporting on.</span>
        </>
      ),
    })
  }
  if (created.heartbeatCaSecretCommand) {
    steps.push({
      key: 'heartbeat-ca',
      title: 'Create the Secret that lets it trust this server',
      body: (
        <>
          The health check is sent over HTTPS to an address with a private certificate authority; this is the one it checks it against.
          <CopyCommand text={created.heartbeatCaSecretCommand} testId="operator-heartbeat-ca-command" label="Copy the certificate authority Secret command" multiline />
        </>
      ),
    })
  }
  if (created.exportSecretCommand && created.exportTarget) {
    steps.push({
      key: 'export',
      title: `Create the client certificate Secret for ${created.exportTarget.name}`,
      body: (
        <>
          It is what {created.exportTarget.name} checks, and it was issued just now for this operator.
          <CopyCommand text={created.exportSecretCommand} testId="operator-export-secret" label="Copy the client certificate Secret command" />
        </>
      ),
    })
  }
  steps.push({ key: 'install', title: renewed ? 'Install it again' : 'Install the operator', body: <CopyCommand text={install} testId="operator-install-command" label="Copy the install command" /> })
  if (created.restartCommand) {
    steps.push({
      key: 'restart',
      title: 'Restart the operator',
      body: (
        <>
          Its certificates are read again on their own; a new receiver token or health credential is only read when it starts.
          <CopyCommand text={created.restartCommand} testId="operator-restart-command" label="Copy the restart command" />
        </>
      ),
    })
  }
  if (exposure !== 'cluster') {
    steps.push({
      key: 'address',
      title: 'Tell Ikhnos where other clusters reach it',
      body: (
        <div data-testid="operator-created-address">
          Once it is running, use the “Reachable at” action on this operator’s row. After that, every command that points at it uses that address.
          <div className="mt-2"><FindTheAddress id={created.operator.id} exposure={exposure} testId="operator-created-find" /></div>
        </div>
      ),
    })
  }

  // Source clusters that have an approved agent can be connected from here; the others are named so nothing is silently left out.
  const sources = created.operator.sourceClusterIds.map((id) => ({
    id,
    name: clusters.find((c) => c.id === id)?.name ?? id,
    agent: agents.find((a) => a.status === 'approved' && a.clusterId === id),
  }))

  return (
    <Modal open onClose={onClose} dismissible={false} title={renewed ? `Renew certificates for ${created.operator.name}` : `${created.operator.name} created`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <p className="text-sm text-nb-400" data-testid="operator-created-intro">
        {renewed ? 'Run these again where the operator lives: new certificates were issued just now, and the old ones stop being the current ones once its release restarts. ' : 'Run these where the operator itself should live, in this order. '}
        {shownOnce
          ? `${shownOnce[0].toUpperCase()}${shownOnce.slice(1)} ${hasToken && heartbeat ? 'are' : 'is'} shown only now - if lost, ${hasToken ? 'revoke this operator and create another' : 'rotate the health credential from the Operators page'}.`
          : 'There is no secret to keep from this screen.'}
      </p>
      {created.exportTarget?.operatorId === CENTRAL_OPERATOR_ID && fusionKind === 'starting' && (
        <p className="mt-2 inline-flex items-center gap-1.5 text-xs text-nb-400" data-testid="operator-created-fusion-starting">
          <FusionDot kind="starting" /> FUSION is starting. These steps are safe to run now; the first data can take a few minutes to show.
        </p>
      )}
      {mtls && (
        <p className="mt-2 text-xs leading-relaxed text-nb-400" data-testid="operator-created-mtls">
          There is no receiver token for this operator. Its receiver accepts agents that present a client certificate
          from this operator&apos;s own certificate authority; one is issued per agent when its commands are generated
          (from the agent&apos;s Telemetry panel). A certificate issued for any other operator, or by the organisation&apos;s own CA, is refused.
        </p>
      )}
      {created.exportTarget && (
        <div className="mt-2" data-testid="operator-created-export">
          <p className="text-xs leading-relaxed text-nb-400">
            This operator sends to <span className="text-nb-200">{created.exportTarget.name}</span>
            {created.exportTarget.operatorId === CENTRAL_OPERATOR_ID ? ', which saves metrics, logs and traces in FUSION. FUSION is part of this server, so there is nothing to install for it' : ''}.
          </p>
          {!created.exportTarget.reachableFromOtherClusters && (
            <p role="alert" className="mt-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid="operator-created-export-unreachable">
              {created.exportTarget.name} is reachable inside its own cluster only. Install this operator there, or record an address for it (<span className="font-semibold">Reachable at</span> on its row) before one in another cluster can send to it.
            </p>
          )}
        </div>
      )}

      <ol className="mt-4 space-y-4" data-testid="operator-created-steps">
        {steps.map((s, i) => <RunStep key={s.key} n={i + 1} title={s.title} testId={`operator-created-step-${s.key}`}>{s.body}</RunStep>)}
        {created.reminders.length > 0 && (
          <RunStep n={steps.length + 1} title="Point each source cluster at it" testId="operator-created-step-sources">
            Informational only - nothing here runs on your behalf. Each source cluster needs its own copy of the client certificate Secret, then its agent&apos;s export endpoint pointed here.
            <span className="mt-1 block space-y-1.5">
              {created.reminders.map((r) => <CopyCommand key={r} text={r} label="Copy the reminder command" />)}
            </span>
          </RunStep>
        )}
      </ol>

      {sources.length > 0 && onConnect && (
        <div className="mt-4 border-t border-nb-850 pt-3" data-testid="operator-created-connect">
          <div className="mb-2 text-xs text-nb-500">Or let the wizard write each cluster’s command, with this operator already chosen:</div>
          <div className="flex flex-wrap gap-2">
            {sources.map((s) =>
              s.agent ? (
                <Button key={s.id} size="sm" onClick={() => onConnect(s.agent!.id)} data-testid={`operator-connect-${s.id}`}>
                  <Plug size={ICON_SM} aria-hidden /> Connect {s.name}
                </Button>
              ) : (
                <span key={s.id} className="rounded-md border border-dashed border-nb-850 px-2 py-1 text-xs text-nb-500" data-testid={`operator-connect-none-${s.id}`}>{s.name}: no connected agent</span>
              ),
            )}
          </div>
        </div>
      )}

      <p className="mt-4 rounded-md border border-nb-850 bg-nb-930 px-3 py-2 text-xs leading-relaxed text-nb-400" data-testid="operator-no-rbac-note">
        No Kubernetes RBAC was applied, and none was needed: this chart requests no ServiceAccount token at all
        (<code className="font-mono">automountServiceAccountToken: false</code>, no ClusterRole, no Role, no binding).
        {' '}{heartbeat
          ? 'It contacts this server only to send its health check, nothing else.'
          : 'It never contacts this server: health reporting is off, and can be turned on later from the Operators page.'}
      </p>
    </Modal>
  )
}

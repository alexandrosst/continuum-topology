import { Antenna, ChevronRight, Globe2, HeartPulse, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { ConfirmModal } from '@/components/forms'
import { FusionDot, FusionPanel, useFusion } from '@/components/operators/FusionPanel'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { TagRows } from '@/components/telemetry/ProcessStep'
import ProcessorEditor from '@/components/telemetry/ProcessorEditor'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { Button, CheckboxList, ChipList, ComboField, EmptyState, ErrorBanner, Field, ICON_MD, ICON_SM, Input, Modal, PageHeader, Pill, Table, TableSkeleton, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator, type OperatorHeartbeatEnabled } from '@/lib/api'
import { extrasOf, TELEMETRY_SIGNALS } from '@/lib/consent'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '@/lib/exportPresets'
import { CENTRAL_OPERATOR_ID, fusionSentence } from '@/lib/fusionStatus'
import { cleanTags, tagProblems, type TagEntry } from '@/lib/install'
import { isReportingHealth, receiverAuthOf } from '@/lib/operatorHealth'
import { buildOperatorInstallCommand, operatorProcessorProblems } from '@/lib/operatorInstall'
import type { ProcessorEntry } from '@/lib/processorCatalog'
import type { OperatorDestination, RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : 'never')
const problem = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback)

function ErrorLine({ text }: { text: string }) {
  return text ? <ErrorBanner className="mb-4">{text}</ErrorBanner> : null
}

type Category = 'local' | 'regional'

const emptyDestination: OperatorDestination = {
  kind: 'external',
  endpoint: '',
  insecure: false,
  authHeaderName: '',
  authSecretName: '',
  authSecretKey: '',
}

/** What is wrong with the destination draft, in words a person can act on - deliberately not reusing
 *  install.ts's telemetryProblems (that one covers a whole TelemetryInput, this is just a destination). */
function destinationProblems(d: OperatorDestination): string[] {
  if (d.kind === 'operator') return d.targetOperatorId ? [] : ['Choose the operator to send to']
  const out: string[] = []
  if (!d.endpoint.trim()) out.push('An export endpoint is required')
  return out
}

/** Sending to the server's own central operator, the one door into the bundled FUSION. */
const centralDestination: OperatorDestination = { kind: 'operator', endpoint: '', targetOperatorId: CENTRAL_OPERATOR_ID }
const isCentral = (d: OperatorDestination) => d.kind === 'operator' && d.targetOperatorId === CENTRAL_OPERATOR_ID

/** Where a regional operator exports to, in one short line for the table: the endpoint, the central operator by
 *  name, or (the central operator's own, which is how it is described) its three stores. */
function destinationLabel(d: OperatorDestination): string {
  if (isCentral(d)) return 'Central operator (FUSION)'
  if (d.kind === 'operator') return `Operator ${d.targetOperatorId ?? ''}`.trim()
  if (d.kind === 'fusion') return 'Prometheus, Loki and Tempo'
  return d.endpoint
}

/** Name + source clusters + destination + extra processors, the whole create form's shape in one place so
 *  the create modal and (later, if this page grows an edit form) an edit modal can share it unmodified. */
interface Draft {
  name: string
  sourceClusterIds: string[]
  destination: OperatorDestination
  extraProcessors: ProcessorEntry[]
  /** Name = value tags this operator stamps on everything it forwards (a region, an environment). Fixed at
   *  creation: they are part of the operator's own install. */
  labels: TagEntry[]
  /** Opt in to the heartbeat that lets this server say online/offline. On by default - it is the point of
   *  asking - but always stated next to the box, and sent explicitly either way. */
  heartbeat: boolean
}
const emptyDraft: Draft = { name: '', sourceClusterIds: [], destination: emptyDestination, extraProcessors: [], labels: [], heartbeat: true }

/** What health reporting sends, in one sentence both the create form, the confirmation and the created
 *  screen can lean on: not a telemetry payload, only an availability check, and only when opted in. */
const HEARTBEAT_WHAT = "an availability check: the collector's own health result, once a minute, sent to this server's health endpoint. It carries no telemetry - nothing you relay, no logs, no traces, no cluster data."

/** The commands that put a heartbeat credential to use, in the order to run them: the Secret first (the
 *  chart reads it), then the helm upgrade that turns the heartbeat on, then - after a rotation - the restart
 *  that makes the collector pick the new value up. Shown once: the credential is never retrievable again. */
function HeartbeatCommands({ secretCommand, upgradeCommand, restartCommand, warning, url, intervalSeconds, rotated, testId }: { secretCommand: string; upgradeCommand?: string; restartCommand?: string; warning?: string; url: string; intervalSeconds: number; rotated?: boolean; testId: string }) {
  return (
    <div className="space-y-3" data-testid={testId}>
      <p className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid={`${testId}-once`}>
        The health credential is shown only now - the server keeps only a hash of it. If it is lost, rotate it to get a new one.
      </p>
      {warning && <p role="alert" className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid={`${testId}-warning`}>{warning}</p>}
      <p className="text-xs text-nb-500">
        The operator will report to <code className="font-mono text-nb-400">{url}</code> every {intervalSeconds} seconds.
      </p>
      <div>
        <div className="mb-1 text-xs text-nb-500">{rotated ? 'Replace the health credential Secret' : 'Create the health credential Secret first'}</div>
        <CopyCommand text={secretCommand} testId={`${testId}-secret`} />
      </div>
      {upgradeCommand && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Then turn health reporting on for the running release</div>
          <CopyCommand text={upgradeCommand} testId={`${testId}-upgrade`} />
        </div>
      )}
      {restartCommand && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Then restart the collector so it presents the new credential (until then it logs a 401 on every attempt)</div>
          <CopyCommand text={restartCommand} testId={`${testId}-restart`} />
        </div>
      )}
    </div>
  )
}

/** The commands for a new operator, shown once: the server keeps only a hash of any receiver token and of the
 *  health credential, so this is the only chance to copy them - same "shown once, gone forever" convention as
 *  TeamPage's InviteCreated. A certificate-gated operator (`token` absent) has no receiver token at all. */
function OperatorCreated({ created, extraProcessors, onClose }: { created: CreatedOperator; extraProcessors: ProcessorEntry[]; onClose: () => void }) {
  const install = buildOperatorInstallCommand(created.install, extraProcessors)
  const hasToken = !!created.token && !!created.secretCommand
  const mtls = !hasToken && receiverAuthOf(created.operator) === 'mtls'
  const heartbeat = !!created.heartbeatToken && !!created.heartbeatSecretCommand
  const shownOnce = [hasToken && 'the receiver token', heartbeat && 'the health credential'].filter(Boolean).join(' and ')
  return (
    <Modal open onClose={onClose} title={`${created.operator.name} created`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <p className="text-sm text-nb-400" data-testid="operator-created-intro">
        Run these where the operator itself should live.{' '}
        {shownOnce
          ? `The install command already refers to what you create first; ${shownOnce} ${hasToken && heartbeat ? 'are' : 'is'} shown only now - if lost, ${hasToken ? 'revoke this operator and create another' : 'rotate the health credential from the Operators page'}.`
          : 'There is no secret to keep from this screen.'}
      </p>
      <p className="mt-2 rounded-md border border-nb-850 bg-nb-930 px-3 py-2 text-xs leading-relaxed text-nb-400" data-testid="operator-no-rbac-note">
        No Kubernetes RBAC was applied, and none was needed: this chart requests no ServiceAccount token at all
        (<code className="font-mono">automountServiceAccountToken: false</code>, no ClusterRole, no Role, no binding).
        {' '}{hasToken ? 'The receiver token below' : 'The client-certificate requirement on its receiver'}, the destination you chose
        {heartbeat ? ', and the health reporting you turned on' : ''} are the complete list of what this operator was granted.
        {heartbeat
          ? ' It contacts this server only to send that health check, nothing else.'
          : ' It never contacts this server: health reporting is off, and can be turned on later from the Operators page.'}
      </p>
      {mtls && (
        <p className="mt-2 text-xs leading-relaxed text-nb-400" data-testid="operator-created-mtls">
          There is no receiver token for this operator. Its receiver accepts agents that present a client certificate
          from this operator&apos;s own certificate authority, and a certificate is issued per agent when that agent&apos;s
          commands are generated (from the agent&apos;s Telemetry panel). A certificate issued for any other operator, or by
          the organisation&apos;s own CA, is refused by this receiver.
        </p>
      )}
      {hasToken && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">Create the receiver token Secret first</div>
          <CopyCommand text={created.secretCommand!} testId="operator-secret-command" />
        </div>
      )}
      {mtls && created.tlsSecretCommand && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">Create the receiver&apos;s TLS certificate Secret first - it is what the receiver checks agents&apos; certificates against</div>
          <CopyCommand text={created.tlsSecretCommand} />
        </div>
      )}
      {heartbeat && (
        <div className="mt-3">
          <HeartbeatCommands
            testId="operator-created-heartbeat"
            secretCommand={created.heartbeatSecretCommand!}
            warning={created.heartbeatWarning}
            url={created.heartbeatUrl ?? ''}
            intervalSeconds={created.heartbeatIntervalSeconds ?? 60}
          />
          <p className="mt-1 text-xs text-nb-500">The install command below already turns health reporting on.</p>
        </div>
      )}
      {created.exportTarget && (
        <div className="mt-3" data-testid="operator-created-export">
          <p className="text-xs leading-relaxed text-nb-400">
            This operator sends to <span className="text-nb-200">{created.exportTarget.name}</span>
            {created.exportTarget.operatorId === CENTRAL_OPERATOR_ID ? ', which saves metrics, logs and traces in FUSION. FUSION is part of this server, so there is nothing to install for it' : ''}.
          </p>
          {!created.exportTarget.reachableFromOtherClusters && (
            <p role="alert" className="mt-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid="operator-created-export-unreachable">
              The central operator is reachable inside this server&apos;s cluster only. Install this operator there, or set <code className="font-mono">fusionControl.centralAddress</code> on the server install and expose the central operator, before one in another cluster can send to it.
            </p>
          )}
          {created.exportSecretCommand && (
            <div className="mt-2">
              <div className="mb-1 text-xs text-nb-500">Create the client certificate Secret first. It is what {created.exportTarget.name} checks, and it was issued just now for this operator</div>
              <CopyCommand text={created.exportSecretCommand} testId="operator-export-secret" />
            </div>
          )}
        </div>
      )}
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">Then install the operator</div>
        <CopyCommand text={install} />
      </div>
      {!mtls && created.tlsSecretCommand && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">
            And create the receiver&apos;s TLS certificate Secret (mTLS, on top of the token above - the install command already turns it on)
          </div>
          <CopyCommand text={created.tlsSecretCommand} />
        </div>
      )}
      {created.reminders.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">
            Informational only - nothing below runs on your behalf. Each source cluster needs its own copy of the client certificate Secret, then its agent&apos;s export endpoint pointed here:
          </div>
          <div className="space-y-1.5">
            {created.reminders.map((r) => <CopyCommand key={r} text={r} />)}
          </div>
        </div>
      )}
    </Modal>
  )
}

/** Confirms, then mints, an operator's health credential - and only then shows it. Minting is the whole
 *  point of the confirmation: it creates a credential, replaces any existing one, and makes the operator
 *  start calling this server, so nothing is requested until the person has read what that means. */
function HealthModal({ operator, onClose, onDone }: { operator: RegionalOperator; onClose: () => void; onDone: () => void }) {
  const conn = useServer((s) => s.conn)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<OperatorHeartbeatEnabled | null>(null)
  const rotating = isReportingHealth(operator)
  const verb = rotating ? 'Rotate health credential' : 'Enable health reporting'
  const confirm = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      setResult(await api.enableOperatorHeartbeat(c, operator.id))
      onDone()
    } catch (e) {
      setError(problem(e, 'Could not enable health reporting.'))
    } finally {
      setBusy(false)
    }
  }
  if (result) {
    return (
      <Modal open onClose={onClose} title={result.rotated ? `Health credential rotated for ${operator.name}` : `Health reporting enabled for ${operator.name}`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-health-done">Done</Button>}>
        {result.rotated && (
          <p className="mb-3 text-xs leading-relaxed text-nb-400" data-testid="operator-health-rotated">
            The previous credential stopped working at once. The operator shows as offline until its Secret is replaced and the collector restarted.
          </p>
        )}
        <HeartbeatCommands
          testId="operator-health-commands"
          secretCommand={result.heartbeatSecretCommand}
          upgradeCommand={result.heartbeatUpgradeCommand}
          restartCommand={result.heartbeatRestartCommand}
          warning={result.heartbeatWarning}
          url={result.heartbeatUrl}
          intervalSeconds={result.heartbeatIntervalSeconds}
          rotated={result.rotated}
        />
      </Modal>
    )
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`${verb} for ${operator.name}?`}
      width="max-w-lg"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={() => void confirm()} disabled={busy} data-testid="operator-health-confirm">{busy ? 'Working…' : verb}</Button></>}
    >
      <div className="space-y-2 text-sm text-nb-400" data-testid="operator-health-explain">
        <p>This mints a credential for the operator&apos;s heartbeat and shows it once. What the operator then sends is {HEARTBEAT_WHAT}</p>
        <p>The operator will start contacting this server - the only thing it sends here. It is opt-in: nothing changes until you run the commands that follow, and you can switch it off later with heartbeat.enabled=false on the release.</p>
        <p>{rotating ? 'Rotating invalidates the old credential at once: until you replace its Secret and restart the collector, the operator will show as offline.' : 'If this operator already has a health credential, this replaces it and the old one stops working at once.'}</p>
      </div>
      {error && <ErrorBanner className="mt-3">{error}</ErrorBanner>}
    </Modal>
  )
}

/** One category tab, styled like AgentsPage's own List/Map toggle - same segmented-button convention. */
function CategoryTab({ id, label, icon: Icon, active, onClick, testId }: { id: Category; label: string; icon: typeof Antenna; active: boolean; onClick: () => void; testId: string }) {
  return (
    <button
      key={id}
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`flex items-center gap-1.5 px-3 py-1.5 text-sm ${active ? 'bg-nb-940 text-nb-300' : 'text-nb-400 hover:text-nb-300'}`}
      data-testid={testId}
    >
      <Icon size={ICON_SM} aria-hidden /> {label}
    </button>
  )
}

/** Fleet management for both tiers of operator: local (an already-approved agent's own OTel collectors,
 *  turned on per-signal via telemetry intent - no separate record of its own, just a view over Agent
 *  diagnostics) and regional (standalone aggregation points, a real backend entity - see RegionalOperator
 *  in lib/types.ts). They share this one page as two categories rather than two nav entries, because a
 *  person reasoning about "what's collecting telemetry in my fleet" wants both answered in one place; the
 *  route/nav slot is unchanged (`/operators`, still one "Operators" entry). Local operators have no
 *  approval flow or heartbeat of their own the way regional operators or discovery agents do - they're a
 *  property of an agent that's already been through that flow elsewhere (Agents/Discovery pages). */
export default function RegionalOperatorsPage() {
  const [sp, setSp] = useSearchParams()
  const conn = useServer((s) => s.conn)
  const isAdmin = useServer((s) => s.isAdmin)
  const canEdit = useServer((s) => s.canEdit)
  const rawAgents = useServer((s) => s.state?.agents)
  const { agents, clusters } = useTopology()
  const [operators, setOperators] = useState<RegionalOperator[]>([])
  const [error, setError] = useState('')
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [created, setCreated] = useState<CreatedOperator | null>(null)
  const [createdProcessors, setCreatedProcessors] = useState<ProcessorEntry[]>([])
  const [revoking, setRevoking] = useState<RegionalOperator | null>(null)
  const [deleting, setDeleting] = useState<RegionalOperator | null>(null)
  const [healthFor, setHealthFor] = useState<RegionalOperator | null>(null)
  const admin = isAdmin()
  const canConsent = conn() != null && canEdit()
  const telemetry = useTelemetryFlow()
  const fusion = useFusion(admin)

  const [operatorsLoaded, setOperatorsLoaded] = useState(false)
  const load = useCallback(async () => {
    const c = conn()
    if (!c || !admin) return
    try {
      setOperators(await api.listOperators(c))
      setError('')
    } catch (e) {
      setError(problem(e, 'Could not load the regional operators.'))
    } finally {
      setOperatorsLoaded(true)
    }
  }, [conn, admin])
  useEffect(() => {
    void load()
  }, [load])

  // Every currently-approved agent that has at least one telemetry signal actually running, per its own
  // self-reported `installedTelemetry` (see consent.ts) - not a separate entity, a view over agents that
  // already exist. Mirrors how AgentInsight.tsx's inline "Change telemetry" panel reads the same field.
  const localRows = useMemo(
    () =>
      agents
        .filter((a) => a.status === 'approved')
        .map((a) => {
          const extras = extrasOf(rawAgents, a.id)
          const installed = extras.diagnostics?.installedTelemetry ?? []
          return { agent: a, cluster: clusters.find((c) => c.id === a.clusterId), installed, reportedAt: extras.diagnostics?.reportedAt }
        })
        .filter((r) => r.installed.length > 0),
    [agents, clusters, rawAgents],
  )

  // Whichever category actually has something in it wins by default (regional if both do, or neither -
  // today's behaviour, unchanged for anyone who only ever used regional operators); the URL is the source
  // of truth once a person has picked one, so switching tabs is bookmarkable/shareable like AgentsPage's
  // own List/Map toggle. `operators` starts empty and only becomes accurate once `load()`'s fetch resolves,
  // so deciding this live off `operators.length` on every render used to mean the page could open on Local
  // (nothing regional yet, by construction) and then silently swap the whole screen to Regional the instant
  // the fetch came back, for anyone who actually has both - a jarring flash of the wrong tab, not a real
  // choice. Deciding it once, in a ref, the first time the fetch has actually settled (loading or not) means
  // it can only ever change once, right when there is finally something to base it on, never again after.
  // "Adjusting state when a prop changes" (React's own documented pattern for this, not an effect - an
  // effect would need an extra commit-then-rerun round trip for something that only ever needs to happen
  // once, right when `operatorsLoaded` itself flips, which this can just as well catch inline during render).
  const [autoCategory, setAutoCategory] = useState<Category | null>(null)
  const [autoCategoryLoadSeen, setAutoCategoryLoadSeen] = useState(false)
  if (operatorsLoaded !== autoCategoryLoadSeen) {
    setAutoCategoryLoadSeen(operatorsLoaded)
    if (operatorsLoaded && autoCategory === null) setAutoCategory(localRows.length > 0 && operators.length === 0 ? 'local' : 'regional')
  }
  const requestedCategory = sp.get('cat')
  const category: Category = requestedCategory === 'local' || requestedCategory === 'regional' ? requestedCategory : (autoCategory ?? 'regional')
  const setCategory = (c: Category) => setSp((p) => { const n = new URLSearchParams(p); n.set('cat', c); return n }, { replace: true })

  const act = async (f: () => Promise<void>, fallback: string) => {
    try {
      setError('')
      await f()
      await load()
    } catch (e) {
      setError(problem(e, fallback))
    }
  }

  // Only clusters a currently-approved agent actually reports, and by cluster id - the same set the
  // server itself checks a source cluster id against (see Core.validSourceClusters). Picking from this
  // list is a convenience; the server re-validates regardless.
  const clusterOptions = clusters
    .filter((cl) => agents.some((a) => a.status === 'approved' && a.clusterId === cl.id))
    .map((cl) => ({ value: cl.id, label: cl.name, hint: cl.region || undefined }))

  const centralDest = isCentral(draft.destination)
  const fusionOff = centralDest && fusion.status?.state === 'off'
  const destinationNote = centralDest ? undefined : unsupportedDestinationNote(draft.destination.endpoint)
  const problems = [
    centralDest && fusion.status && !fusion.status.available ? [`FUSION cannot be switched from this server. ${fusion.status.message ?? ''}`.trim()] : [],
    draft.name.trim().length < 2 ? ['A name of at least two characters is required'] : [],
    draft.sourceClusterIds.length === 0 ? ['Pick at least one source cluster'] : [],
    destinationProblems(draft.destination),
    operatorProcessorProblems(draft.extraProcessors),
    tagProblems(draft.labels),
  ].flat()

  const create = () =>
    act(async () => {
      const c = conn()
      if (!c) return
      // Choosing the central operator while FUSION is off means "and turn it on": it is what that operator saves into.
      if (fusionOff) await fusion.enable()
      const r = await api.createOperator(c, draft.name.trim(), draft.sourceClusterIds, draft.destination, { heartbeat: draft.heartbeat, labels: cleanTags(draft.labels) })
      setCreating(false)
      setCreatedProcessors(draft.extraProcessors)
      setDraft(emptyDraft)
      setCreated(r)
    }, 'Could not create the regional operator.')

  const revoke = (op: RegionalOperator) =>
    act(async () => {
      const c = conn()
      if (c) await api.revokeOperator(c, op.id, 'revoked in the UI')
    }, 'Could not revoke the operator.')

  const remove = (op: RegionalOperator) =>
    act(async () => {
      const c = conn()
      if (c) await api.deleteOperator(c, op.id)
    }, 'Could not delete the operator.')

  return (
    <>
      <PageHeader
        title="Operators"
        description={
          category === 'local'
            ? "Per-cluster OpenTelemetry collectors, driven by which signals each cluster's own agent has turned on."
            : 'Each aggregates telemetry already exported by a set of approved clusters and re-exports it to one destination - another observability backend, or (soon) another regional operator above it.'
        }
        actions={
          category === 'regional' && admin ? (
            <Button variant="primary" onClick={() => { setDraft(emptyDraft); setCreating(true) }} data-testid="operator-open"><Plus size={ICON_SM} /> New operator</Button>
          ) : undefined
        }
      />

      <div className="mb-4 flex w-fit overflow-hidden rounded-md border border-nb-850" role="tablist" aria-label="Operator category">
        <CategoryTab id="local" label="Local" icon={Antenna} active={category === 'local'} onClick={() => setCategory('local')} testId="operators-local" />
        <CategoryTab id="regional" label="Regional" icon={Globe2} active={category === 'regional'} onClick={() => setCategory('regional')} testId="operators-regional" />
      </div>

      {category === 'regional' && admin && (
        <details className="group mb-4 rounded-lg border border-nb-850 bg-nb-925" data-testid="operator-access-note">
          <summary className="flex cursor-pointer select-none items-center gap-1.5 px-4 py-3 text-sm font-medium text-nb-300 marker:content-none">
            <ChevronRight size={ICON_SM} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
            What a regional operator needs, and what you grant it
          </summary>
          <div className="space-y-2 border-t border-nb-850 px-4 py-3 text-xs leading-relaxed text-nb-400">
            <p>
              <span className="font-medium text-nb-200">No Kubernetes API access of any kind.</span> This chart renders no
              ClusterRole, Role or RoleBinding, and its ServiceAccount is created with{' '}
              <code className="font-mono">automountServiceAccountToken: false</code> - it cannot present a token to the API
              server even if it tried. It never dials the Ikhnos server either, unless you turn on health reporting
              for it - and then the only thing it sends is {HEARTBEAT_WHAT} It never watches this cluster&apos;s own
              objects the way a discovery agent&apos;s RBAC lets it (see the chart&apos;s own README for the full architecture).
            </p>
            <p>
              What you actually configure, in full: <span className="text-nb-200">how its receiver authenticates agents</span>{' '}
              - for an operator created now, the client certificate alone, with no receiver token at all (each operator has its own
              certificate authority, so a certificate issued for another operator is refused), and for an
              older operator the bearer token it was created with, minted once and kept only as a hash;{' '}
              <span className="text-nb-200">the destination</span> it re-exports aggregated telemetry to; and, optionally,{' '}
              <span className="text-nb-200">health reporting</span>, which is what lets this page say online or offline.
              Nothing else is asked for or needed.
            </p>
          </div>
        </details>
      )}

      {category === 'local' ? (
        localRows.length === 0 ? (
          <EmptyState
            title="No local operators running yet"
            description="A local operator is just an already-connected cluster's agent with at least one telemetry signal turned on. Configure one to see it here."
            action={canConsent ? <Button variant="primary" onClick={() => telemetry.start()}><Antenna size={ICON_SM} /> Configure telemetry</Button> : undefined}
          />
        ) : (
          <Table data-testid="local-operators-table">
            <thead>
              <tr><Th>Cluster</Th><Th>Agent</Th><Th>Signals</Th><Th>Last reported</Th><Th /></tr>
            </thead>
            <tbody>
              {localRows.map(({ agent, cluster, installed, reportedAt }) => (
                <tr key={agent.id} className="group hover:bg-nb-930/60" data-testid={`local-operator-${agent.name}`}>
                  <Td className="text-nb-300">{cluster?.name ?? agent.name}</Td>
                  <Td className="text-nb-500">{agent.name}</Td>
                  <Td><ChipList items={TELEMETRY_SIGNALS.filter((s) => installed.includes(s.id)).map((s) => s.label)} max={3} /></Td>
                  <Td className="text-nb-500">{when(reportedAt)}</Td>
                  <Td className="text-right">
                    {canConsent && (
                      <Button size="sm" onClick={() => telemetry.start(agent.id)}>Configure</Button>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )
      ) : !admin ? (
        <EmptyState title="Administrators only" description="Only organisation administrators can see and manage regional operators." />
      ) : (
        <>
          <ErrorLine text={error} />
          <div className="mb-4"><FusionPanel fusion={fusion} /></div>
          {!operatorsLoaded ? (
            // Without this, the fetch that op.listOperators fires on every mount (task #383: this data is
            // live and org-scoped, so unlike every other tab here it can't just read the already-synced
            // global store) left a brief but real window where `operators` was still its initial `[]` -
            // reading, wrongly, as "you have none" rather than "still loading" for anyone who actually has
            // some. A skeleton says the honest thing for however long that round trip takes, same as
            // ActivityPage/HistoryPage's own fetches already do.
            <TableSkeleton cols={['', '', '', '', '', '']} />
          ) : operators.length === 0 ? (
            <EmptyState title="No regional operators yet" description="Create one to aggregate telemetry from a set of clusters before it leaves your infrastructure." />
          ) : (
          <Table>
            <thead>
              <tr><Th>Name</Th><Th>Status</Th><Th>Sources</Th><Th>Destination</Th><Th>Created</Th><Th /></tr>
            </thead>
            <tbody>
              {operators.map((op) => (
                <tr key={op.id} className="group hover:bg-nb-930/60" data-testid={`operator-${op.name}`}>
                  <Td className="text-nb-300">
                    {op.name}
                    {(op.labels?.length ?? 0) > 0 && (
                      <div className="mt-1"><ChipList items={op.labels!.map((l) => `${l.key}=${l.value}`)} max={3} /></div>
                    )}
                  </Td>
                  <Td>
                    <Pill>{op.status === 'active' ? 'Active' : `Revoked${op.reason ? `: ${op.reason}` : ''}`}</Pill>
                    {op.status === 'active' && op.id === CENTRAL_OPERATOR_ID ? (
                      <div className="mt-1 inline-flex items-center gap-1.5 text-xs text-nb-400" data-testid="operator-central-state">
                        <FusionDot kind={fusionSentence(fusion.status).kind} /> {fusionSentence(fusion.status).kind === 'running' ? 'Running' : fusionSentence(fusion.status).kind === 'off' ? 'Off' : fusionSentence(fusion.status).kind === 'starting' ? 'Starting' : 'Needs attention'}
                      </div>
                    ) : (
                      op.status === 'active' && <div className="mt-1"><OperatorHealth operator={op} /></div>
                    )}
                  </Td>
                  <Td className="text-nb-500">{op.sourceClusterIds.length} cluster{op.sourceClusterIds.length === 1 ? '' : 's'}</Td>
                  <Td className="text-nb-500"><span className="font-mono text-xs" data-testid={`operator-destination-${op.name}`}>{destinationLabel(op.destination)}</span></Td>
                  <Td className="text-nb-500">{when(op.createdAt)}</Td>
                  <Td className="text-right">
                    {op.id === CENTRAL_OPERATOR_ID ? (
                      <span className="text-xs text-nb-500" data-testid="operator-central-managed">Managed by FUSION</span>
                    ) : (
                    <>
                    {op.status === 'active' && (
                      <>
                        <Button size="sm" onClick={() => setHealthFor(op)} data-testid={`operator-health-open-${op.name}`}>
                          <HeartPulse size={ICON_SM} aria-hidden /> {isReportingHealth(op) ? 'Rotate health credential' : 'Enable health reporting'}
                        </Button>{' '}
                        <Button size="sm" variant="danger" onClick={() => setRevoking(op)}>Revoke</Button>
                      </>
                    )}
                    <Button size="sm" variant="danger" aria-label={`Delete ${op.name}`} onClick={() => setDeleting(op)}><Trash2 size={ICON_MD} /></Button>
                    </>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
          )}
        </>
      )}

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New regional operator"
        description="Mechanism only: this declares the operator, its scope and its destination, and gives you an install command. It does not touch any cluster's own export settings for you."
        width="max-w-2xl"
        footer={<><Button onClick={() => setCreating(false)}>Cancel</Button><Button variant="primary" onClick={create} disabled={problems.length > 0 || fusion.busy} data-testid="operator-create">{fusionOff ? 'Enable FUSION and create' : 'Create operator'}</Button></>}
      >
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (problems.length === 0) void create() }}>
          <Field label="Name"><Input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} maxLength={80} data-testid="operator-name" /></Field>

          <p className="rounded-md border border-nb-850 bg-nb-930 px-3 py-2 text-xs leading-relaxed text-nb-400" data-testid="operator-create-no-rbac-note">
            This needs no Kubernetes API access at all - no RBAC is applied and no ServiceAccount token is even requested.
            The destination below, how its receiver authenticates agents (normally a client certificate issued by this
            server), and the health reporting below if you leave it on, are the complete list of what this operator is granted.
          </p>

          <Field label="Source clusters" hint="Clusters whose already-exported telemetry this operator aggregates. Only clusters with a currently-approved agent are listed.">
            <CheckboxList
              options={clusterOptions}
              value={draft.sourceClusterIds}
              onChange={(v) => setDraft({ ...draft, sourceClusterIds: v })}
              emptyLabel="No cluster has an approved agent yet - approve one on the Agents page first."
            />
          </Field>

          <div className="border-t border-nb-850 pt-3">
            <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">Where it saves what it receives</div>
            <div className="flex w-fit overflow-hidden rounded-md border border-nb-850" role="radiogroup" aria-label="Destination type">
              {([
                ['external', 'Another backend', 'operator-dest-external'],
                ['central', 'Central operator (FUSION)', 'operator-dest-central'],
              ] as const).map(([kind, label, testId]) => (
                <button
                  key={kind}
                  type="button"
                  role="radio"
                  aria-checked={(centralDest ? 'central' : 'external') === kind}
                  onClick={() => setDraft({ ...draft, destination: kind === 'central' ? centralDestination : emptyDestination })}
                  data-testid={testId}
                  className={`px-3 py-1.5 text-sm ${(centralDest ? 'central' : 'external') === kind ? 'bg-nb-850 text-nb-100' : 'text-nb-400 hover:bg-nb-900'}`}
                >
                  {label}
                </button>
              ))}
            </div>
            <p className="mt-1.5 text-xs leading-relaxed text-nb-500">
              An OTLP endpoint you already run (Honeycomb, Grafana Cloud, your own gateway) - or the central operator, which saves into this server&apos;s own FUSION stores.
            </p>
          </div>

          {centralDest && (
            <div className="space-y-3" data-testid="operator-central">
              <FusionPanel fusion={fusion} compact />
              <p className="text-xs leading-relaxed text-nb-500">
                The central operator is the one door into FUSION: Prometheus for metrics, Loki for logs, Tempo for traces, each on its own volume, and never exposed themselves.
                This operator sends to it over mutual TLS; the client certificate is issued when you create the operator, and shown with its install command.
                {fusionOff && ' Creating it turns FUSION on first - it starts four pods next to this server, which the server already carries but keeps switched off.'}
              </p>
            </div>
          )}

          {!centralDest && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Send aggregated telemetry to" hint="Pick a known backend to fill in its endpoint pattern and credential header, or type your own.">
                <ComboField
                  value={draft.destination.endpoint}
                  onChange={(v) => {
                    const preset = EXPORT_PRESETS.find((p) => p.endpointPattern === v)
                    setDraft({
                      ...draft,
                      destination: {
                        ...draft.destination,
                        endpoint: v,
                        authHeaderName: preset && preset.headerName ? preset.headerName : draft.destination.authHeaderName,
                      },
                    })
                  }}
                  placeholder="otel-gateway.example.com:4317"
                  options={EXPORT_PRESETS.map((p) => ({ value: p.endpointPattern, label: p.label }))}
                />
              </Field>
              {destinationNote && <p role="alert" className="text-xs text-warn sm:col-span-2">{destinationNote}</p>}
              <label className="flex cursor-pointer items-center gap-2 text-sm sm:col-span-2">
                <input
                  type="checkbox"
                  className="size-4 accent-[var(--color-accent)]"
                  checked={!!draft.destination.insecure}
                  onChange={(e) => setDraft({ ...draft, destination: { ...draft.destination, insecure: e.target.checked } })}
                  data-testid="operator-export-insecure"
                />
                <span className="text-nb-300">Skip TLS verification for this endpoint</span>
              </label>
              <Field label="Credential header" hint="Which header the destination expects its credential in.">
                <Input
                  value={draft.destination.authHeaderName ?? ''}
                  onChange={(e) => setDraft({ ...draft, destination: { ...draft.destination, authHeaderName: e.target.value } })}
                  placeholder="Authorization"
                />
              </Field>
              <Field label="Secret holding it" hint="A Secret you create in the release namespace, outside this chart - never the credential value itself.">
                <Input
                  value={draft.destination.authSecretName ?? ''}
                  onChange={(e) => setDraft({ ...draft, destination: { ...draft.destination, authSecretName: e.target.value } })}
                  placeholder="telemetry-export-token"
                />
              </Field>
            </div>
          )}

          <div className="border-t border-nb-850 pt-3">
            <label className="flex cursor-pointer items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-0.5 size-4 accent-[var(--color-accent)]"
                checked={draft.heartbeat}
                onChange={(e) => setDraft({ ...draft, heartbeat: e.target.checked })}
                data-testid="operator-heartbeat"
              />
              <span>
                <span className="text-nb-300">Report this operator&apos;s health to this server</span>
                <span className="mt-0.5 block text-xs leading-relaxed text-nb-500" data-testid="operator-heartbeat-explain">
                  Sends {HEARTBEAT_WHAT} It is the only thing this operator ever sends to this server, so this page can show online or offline. You can also turn it on later.
                </span>
              </span>
            </label>
          </div>

          <div className="border-t border-nb-850 pt-3">
            <div className="mb-1 text-xs font-medium uppercase tracking-wide text-nb-500">Labels</div>
            <p className="mb-2 text-xs leading-relaxed text-nb-500" data-testid="operator-labels-explain">
              Added to every metric, log and trace this operator forwards, next to its own id and name, so that data can be told apart downstream (a region, an environment). They cannot be changed afterwards: they live in the operator&apos;s install.
            </p>
            <TagRows tags={draft.labels} onChange={(labels) => setDraft({ ...draft, labels })} testIdPrefix="operator-label" noun="label" />
          </div>

          <div className="border-t border-nb-850 pt-3">
            <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">Extra processors</div>
            <ProcessorEditor entries={draft.extraProcessors} onChange={(extraProcessors) => setDraft({ ...draft, extraProcessors })} testIdPrefix="operator" />
          </div>

          {problems.length > 0 && (
            <p role="alert" className="text-xs text-bad" data-testid="operator-problems">{problems.join('. ')}.</p>
          )}
        </form>
      </Modal>

      {created && (
        <OperatorCreated created={created} extraProcessors={createdProcessors} onClose={() => setCreated(null)} />
      )}

      {healthFor && (
        <HealthModal operator={healthFor} onClose={() => setHealthFor(null)} onDone={() => void load()} />
      )}

      {revoking && (
        <ConfirmModal
          title={`Revoke ${revoking.name}?`}
          message="This only marks it revoked here - there is no channel back to the deployed collector, so its receiver keeps accepting what it accepted before (a bearer token until you delete the Kubernetes Secret holding it, a client certificate until you uninstall the release). Its health reports are refused from now on. The record stays for the audit trail; delete it separately if you want it gone entirely."
          confirmLabel="Revoke"
          onConfirm={() => void revoke(revoking)}
          onClose={() => setRevoking(null)}
        />
      )}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Removes the record for good. If it is still active, revoke it first (or the source clusters keep exporting to a receiver that no longer exists)."
          onConfirm={() => void remove(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}

      {telemetry.dialogs}
    </>
  )
}

import clsx from 'clsx'
import { Antenna, ChevronRight, Globe2, Plug, Plus } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ConfirmModal } from '@/components/forms'
import CreateOperatorModal from '@/components/operators/CreateOperatorModal'
import { FusionDot, FusionPanel, useFusion } from '@/components/operators/FusionPanel'
import { HEARTBEAT_WHAT, HeartbeatCommands } from '@/components/operators/HeartbeatCommands'
import LocalOperatorsTab from '@/components/operators/LocalOperatorsTab'
import { OperatorAddressModal } from '@/components/operators/OperatorAddress'
import OperatorCertificatesModal from '@/components/operators/OperatorCertificatesModal'
import OperatorCreated from '@/components/operators/OperatorCreated'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import RemoveOperatorModal from '@/components/operators/RemoveOperatorModal'
import RowMenu, { type RowMenuItem } from '@/components/operators/RowMenu'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { Button, ChipList, CopyIconButton, EmptyState, ErrorBanner, ICON_SM, LiveDot, Modal, PageHeader, PulseDot, Table, TableSkeleton, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator, type OperatorHeartbeatEnabled } from '@/lib/api'
import { CENTRAL_OPERATOR_ID, fusionLabel, fusionSentence } from '@/lib/fusionStatus'
import { ageOf } from '@/lib/history'
import { isReportingHealth, receiverAuthOf } from '@/lib/operatorHealth'
import { addressCell, certChip, joinLocal } from '@/lib/operatorsView'
import type { OperatorDestination, RegionalOperator } from '@/lib/types'
import { operatorFromDestination } from '@/lib/destinationCatalog'
import { useHoldReload } from '@/lib/useHoldReload'
import { useOperatorDestinations, useOperators, useTelemetryIntents } from '@/lib/useOperators'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

const problem = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback)

/** What "Renew certificates" replaces, said before it is done: the server issues new certificates and, for an operator that has them, a new
 *  receiver token and health credential, and none of it reaches the running operator by itself. */
function renewWarning(op: RegionalOperator): string {
  const replaced = ['its certificates']
  if (receiverAuthOf(op) === 'bearer') replaced.push('its receiver token')
  if (isReportingHealth(op)) replaced.push('its health credential (the heartbeat secret)')
  const list = replaced.length > 1 ? `${replaced.slice(0, -1).join(', ')} and ${replaced[replaced.length - 1]}` : replaced[0]
  return `This replaces ${list}. Nothing reaches the running operator by itself: the current ones stop being the current ones once its release restarts, so ${op.name} must be updated with the new commands that follow, run where it is installed${isReportingHealth(op) ? ' (until then it shows as offline)' : ''}. They are shown once.`
}

type Category = 'local' | 'regional'

/** Fixed widths that add up to what the page has beside the sidebar at 1280 px, so the row's own menu is on screen without scrolling the
 *  table sideways; narrower windows scroll the table, and the actions column stays in view (sticky). */
const OP_COLS = ['w-44', 'w-36', 'w-40', 'w-36', 'w-44', 'w-24', 'w-12']

/** Where a regional operator exports to, in one short line for the table: another operator by name (FUSION's door is just "FUSION"),
 *  an endpoint, or - the central operator's own - its three stores. */
function destinationLabel(d: OperatorDestination, operators: RegionalOperator[]): string {
  if (d.kind === 'operator') return d.targetOperatorId === CENTRAL_OPERATOR_ID ? 'FUSION' : operators.find((o) => o.id === d.targetOperatorId)?.name ?? `Operator ${d.targetOperatorId ?? ''}`.trim()
  if (d.kind === 'fusion') return 'Prometheus, Loki and Tempo'
  return d.endpoint
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
  // The credential is shown once, in the result below: hold the page's own reload while it is on screen (see the Modal's `dismissible`).
  useHoldReload(result !== null)
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
      <Modal open onClose={onClose} dismissible={false} title={result.rotated ? `Health credential rotated for ${operator.name}` : `Health reporting enabled for ${operator.name}`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-health-done">Done</Button>}>
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
          caSecretCommand={result.heartbeatCaSecretCommand}
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
      // Once the request is out the credential exists and is on its way to this dialog: closing now would lose it.
      dismissible={!busy}
      title={`${verb} for ${operator.name}?`}
      width="max-w-lg"
      footer={<><Button onClick={onClose} disabled={busy}>Cancel</Button><Button variant="primary" onClick={() => void confirm()} disabled={busy} data-testid="operator-health-confirm">{busy ? 'Working…' : verb}</Button></>}
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
function CategoryTab({ label, icon: Icon, active, onClick, testId }: { label: string; icon: typeof Antenna; active: boolean; onClick: () => void; testId: string }) {
  return (
    <button
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

/** Fleet management for both tiers of operator: local (an already-approved agent's own OTel collectors, turned on per signal via a
 *  telemetry request - no separate record of its own, a view over what each agent reports and was asked) and regional (standalone
 *  aggregation points, a real backend entity). They share this page as two categories because a person reasoning about "what is
 *  collecting telemetry in my fleet" wants both answered in one place. */
export default function RegionalOperatorsPage() {
  const [sp, setSp] = useSearchParams()
  const conn = useServer((s) => s.conn)
  const isAdmin = useServer((s) => s.isAdmin)
  const canEdit = useServer((s) => s.canEdit)
  const rawAgents = useServer((s) => s.state?.agents)
  const { agents, clusters } = useTopology()
  const admin = isAdmin()
  const canConsent = conn() != null && canEdit()
  const telemetry = useTelemetryFlow()

  const { operators, loaded: operatorsLoaded, error: loadError, reload } = useOperators(admin)
  const { destinations } = useOperatorDestinations(!admin && canConsent)
  const { intents } = useTelemetryIntents(canConsent)
  const fusion = useFusion(admin, () => void reload())
  // The central operator appears (or goes) with FUSION: read the list again the moment that flips, however it came about.
  const centralExists = fusion.status?.central?.exists
  const seenCentral = useRef(centralExists)
  useEffect(() => {
    if (seenCentral.current === centralExists) return
    seenCentral.current = centralExists
    void reload()
  }, [centralExists, reload])

  const [actionError, setActionError] = useState('')
  const [creating, setCreating] = useState(false)
  const [renewed, setRenewed] = useState<{ result: CreatedOperator; operator: RegionalOperator } | null>(null)
  const [renewing, setRenewing] = useState<string | null>(null)
  // Renewing issues new credentials: it is asked about first, with what it replaces, and never started from a stray click.
  const [confirmRenew, setConfirmRenew] = useState<RegionalOperator | null>(null)
  const [removing, setRemoving] = useState<{ operator: RegionalOperator; mode: 'revoke' | 'delete' } | null>(null)
  const [healthFor, setHealthFor] = useState<RegionalOperator | null>(null)
  const [addressFor, setAddressFor] = useState<RegionalOperator | null>(null)
  const [certsFor, setCertsFor] = useState<RegionalOperator | null>(null)

  // Whichever category has something in it wins by default (regional if both do, or neither); the URL is the source of truth once a
  // person has picked one, so a tab is bookmarkable. Decided once, the first time the operators have settled, so the page cannot open
  // on Local and then swap itself to Regional when the list arrives. ("Adjusting state when a prop changes", during render.)
  const localRows = useMemo(() => joinLocal({ agents, clusters, rawAgents, intents }), [agents, clusters, rawAgents, intents])
  const [autoCategory, setAutoCategory] = useState<Category | null>(null)
  const [autoCategoryLoadSeen, setAutoCategoryLoadSeen] = useState(false)
  if (operatorsLoaded !== autoCategoryLoadSeen) {
    setAutoCategoryLoadSeen(operatorsLoaded)
    if (operatorsLoaded && autoCategory === null) setAutoCategory(localRows.length > 0 && operators.length === 0 ? 'local' : 'regional')
  }
  const requestedCategory = sp.get('cat')
  const category: Category = requestedCategory === 'local' || requestedCategory === 'regional' ? requestedCategory : (autoCategory ?? 'regional')
  const setCategory = (c: Category) => setSp((p) => { const n = new URLSearchParams(p); n.set('cat', c); return n }, { replace: true })

  // What the Local tab can name a destination by: the full list for an administrator, the read model for anyone else.
  const knownOperators = useMemo(() => (admin ? operators : destinations.map(operatorFromDestination)), [admin, operators, destinations])

  const renew = async (op: RegionalOperator) => {
    const c = conn()
    if (!c || renewing) return
    setRenewing(op.id)
    setActionError('')
    try {
      setRenewed({ result: await api.reinstallOperator(c, op.id), operator: op })
      actionDone()
    } catch (e) {
      setActionError(problem(e, 'Could not renew the certificates.'))
    } finally {
      setRenewing(null)
    }
  }

  const sourcesOf = (op: RegionalOperator) =>
    op.sourceClusterIds.map((id) => ({ id, name: clusters.find((c) => c.id === id)?.name ?? id, agent: agents.find((a) => a.status === 'approved' && a.clusterId === id) })).filter((s) => !!s.agent)
  const connect = (agentId: string, operatorId: string) => telemetry.start(agentId, undefined, operatorId)

  const fusionKind = fusionSentence(fusion.status).kind
  // `loaded` is true once a read has finished, answered or failed: with nothing to show and an error, the list is unknown, not empty.
  const empty = admin && operatorsLoaded && operators.length === 0 && !loadError
  // Something done to an operator went through: whatever the last failed action said is no longer the state of the page.
  const actionDone = () => {
    setActionError('')
    void reload()
  }

  const rowItems = (op: RegionalOperator): RowMenuItem[] => {
    if (op.status !== 'active') return [{ key: 'delete', label: 'Delete…', danger: true, onSelect: () => setRemoving({ operator: op, mode: 'delete' }), testId: `operator-delete-${op.name}` }]
    const address: RowMenuItem = { key: 'address', label: 'Reachable at…', onSelect: () => setAddressFor(op), testId: `operator-address-open-${op.name}` }
    if (op.id === CENTRAL_OPERATOR_ID) return [address]
    return [
      address,
      { key: 'health', label: isReportingHealth(op) ? 'Rotate health credential' : 'Enable health reporting', onSelect: () => setHealthFor(op), testId: `operator-health-open-${op.name}` },
      { key: 'certs', label: 'Issued certificates', onSelect: () => setCertsFor(op), testId: `operator-certs-${op.name}` },
      // Dimmed while any renewal runs (not only this row's): one at a time, and the reason is visible rather than a click that does nothing.
      { key: 'renew', label: 'Renew certificates…', onSelect: () => setConfirmRenew(op), testId: `operator-renew-${op.name}`, disabled: renewing !== null, title: renewing !== null ? 'Another renewal is in progress' : undefined },
      { key: 'revoke', label: 'Revoke…', danger: true, onSelect: () => setRemoving({ operator: op, mode: 'revoke' }), testId: `operator-revoke-${op.name}` },
      { key: 'delete', label: 'Delete…', danger: true, onSelect: () => setRemoving({ operator: op, mode: 'delete' }), testId: `operator-delete-${op.name}` },
    ]
  }

  return (
    <>
      <PageHeader
        title="Operators"
        description={
          category === 'local'
            ? "Per-cluster OpenTelemetry collectors, driven by which signals each cluster's own agent has turned on."
            : 'Each gathers telemetry that clusters already export and passes it on to one destination: FUSION, another operator or another backend.'
        }
        // Always there on this tab, so it is where the eye already is whether or not there are operators yet: the one primary button.
        actions={category === 'regional' && admin ? <Button variant="primary" onClick={() => setCreating(true)} data-testid="operator-open"><Plus size={ICON_SM} /> New operator</Button> : undefined}
      />

      <div className="mb-4 flex w-fit overflow-hidden rounded-md border border-nb-850" role="tablist" aria-label="Operator category">
        <CategoryTab label="Local" icon={Antenna} active={category === 'local'} onClick={() => setCategory('local')} testId="operators-local" />
        <CategoryTab label="Regional" icon={Globe2} active={category === 'regional'} onClick={() => setCategory('regional')} testId="operators-regional" />
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
        <LocalOperatorsTab rows={localRows} operators={knownOperators} canConfigure={canConsent} onConfigure={(id) => telemetry.start(id)} />
      ) : !admin ? (
        <EmptyState title="Administrators only" description="Only organisation administrators can see and manage regional operators." />
      ) : (
        <>
          {loadError && <ErrorBanner className="mb-4">{loadError}</ErrorBanner>}
          {actionError && <ErrorBanner className="mb-4" onDismiss={() => setActionError('')}>{actionError}</ErrorBanner>}
          <div className="mb-4"><FusionPanel fusion={fusion} /></div>
          {!operatorsLoaded ? (
            // The list is live and org-scoped, so it cannot be read from the already-synced store: until it answers, "none" would be a lie.
            <TableSkeleton cols={OP_COLS} />
          ) : operators.length === 0 && loadError ? null : empty ? (
            <EmptyState title="No regional operators yet" description="Create one to gather telemetry from a set of clusters before it leaves your infrastructure." action={<Button onClick={() => setCreating(true)} data-testid="operator-open-empty"><Plus size={ICON_SM} /> New operator</Button>} />
          ) : (
            <Table cols={OP_COLS} data-testid="operators-table">
              <thead>
                <tr><Th>Name</Th><Th>Status</Th><Th>Sources</Th><Th>Destination</Th><Th>Address</Th><Th>Created</Th><Th actionsLabel="Actions" className="sticky right-0 bg-nb-925" /></tr>
              </thead>
              <tbody>
                {operators.map((op) => {
                  const active = op.status === 'active'
                  const central = op.id === CENTRAL_OPERATOR_ID
                  const chip = active ? certChip(op) : undefined
                  const addr = active ? addressCell(op) : undefined
                  const sources = sourcesOf(op)
                  return (
                    <tr key={op.id} className="group hover:bg-nb-930/60" data-testid={`operator-${op.name}`}>
                      <Td valign="top" className="text-nb-300">
                        <div className="truncate" title={op.name}>{op.name}</div>
                        {chip && (
                          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
                            <span className={clsx('inline-flex items-center rounded-md border px-2 py-0.5 text-xs', chip.tone === 'bad' ? 'border-bad/30 bg-bad/10 text-bad' : 'border-warn/30 bg-warn/10 text-warn')} data-testid={`operator-cert-${op.name}`}>{chip.text}</span>
                            {!central && <button type="button" className="text-xs text-accent hover:underline" onClick={() => setConfirmRenew(op)} disabled={renewing !== null} title={renewing !== null ? 'Another renewal is in progress' : undefined} data-testid={`operator-cert-renew-${op.name}`}>Renew certificates</button>}
                          </div>
                        )}
                        {(op.labels?.length ?? 0) > 0 && <div className="mt-1"><ChipList items={op.labels!.map((l) => `${l.key}=${l.value}`)} max={3} /></div>}
                      </Td>
                      <Td valign="top">
                        {!active ? (
                          <span className="text-xs text-nb-500">Revoked{op.reason ? `: ${op.reason}` : ''}</span>
                        ) : central ? (
                          <span className="inline-flex items-center gap-1.5 text-xs text-nb-400" data-testid="operator-central-state">
                            <FusionDot kind={fusionKind} /> {fusionLabel(fusionKind)}
                          </span>
                        ) : (
                          <OperatorHealth operator={op} />
                        )}
                      </Td>
                      <Td valign="top" className="text-nb-500">
                        <div>{op.sourceClusterIds.length} cluster{op.sourceClusterIds.length === 1 ? '' : 's'}</div>
                        {active && !central && canConsent && sources.length === 1 && (
                          <Button size="sm" variant="ghost" className="-ml-2.5 mt-1 max-w-full" onClick={() => connect(sources[0].agent!.id, op.id)} data-testid={`operator-connect-${op.name}`}>
                            <Plug size={ICON_SM} aria-hidden /> <span className="truncate">Connect {sources[0].name}</span>
                          </Button>
                        )}
                        {active && !central && canConsent && sources.length > 1 && (
                          <div className="-ml-2.5 mt-1">
                            <RowMenu
                              ariaLabel={`Connect a cluster to ${op.name}`}
                              testId={`operator-connect-${op.name}`}
                              items={sources.map((s) => ({ key: s.id, label: `Connect ${s.name}`, onSelect: () => connect(s.agent!.id, op.id), testId: `operator-connect-${op.name}-${s.id}` }))}
                            >
                              <Plug size={ICON_SM} aria-hidden /> Connect a cluster
                            </RowMenu>
                          </div>
                        )}
                      </Td>
                      <Td valign="top" className="text-nb-500"><span className="block truncate font-mono text-xs" title={destinationLabel(op.destination, operators)} data-testid={`operator-destination-${op.name}`}>{destinationLabel(op.destination, operators)}</span></Td>
                      <Td valign="top">
                        {addr && (
                          <div className="flex items-center gap-1.5 text-xs" data-testid={`operator-address-${op.name}`} data-address={addr.kind}>
                            {addr.kind === 'set' ? <PulseDot color="bg-ok" size="size-1.5" /> : <LiveDot kind={addr.kind === 'pending' ? 'late' : 'idle'} />}
                            {addr.kind === 'set' ? (
                              <>
                                <span className="min-w-0 truncate font-mono text-nb-400" title={addr.address}>{addr.address}</span>
                                <CopyIconButton text={addr.address} title={`Copy the address of ${op.name}`} />
                              </>
                            ) : addr.kind === 'pending' ? (
                              <button type="button" className="text-warn hover:underline" onClick={() => setAddressFor(op)}>{addr.text}</button>
                            ) : (
                              <span className="text-nb-500">{addr.text}</span>
                            )}
                          </div>
                        )}
                      </Td>
                      <Td valign="top" className="whitespace-nowrap text-nb-500"><span title={op.createdAt ? new Date(op.createdAt).toLocaleString() : undefined}>{op.createdAt ? ageOf(op.createdAt) : ''}</span></Td>
                      <Td valign="top" className="sticky right-0 bg-nb-925 text-right group-hover:bg-nb-930">
                        <RowMenu ariaLabel={`Actions for ${op.name}`} items={rowItems(op)} testId={`operator-menu-${op.name}`} />
                      </Td>
                    </tr>
                  )
                })}
              </tbody>
            </Table>
          )}
        </>
      )}

      {creating && (
        <CreateOperatorModal
          operators={operators}
          fusion={fusion}
          onCreated={() => { void reload(); void fusion.refresh() }}
          onConnect={connect}
          onClose={() => setCreating(false)}
        />
      )}

      {confirmRenew && (
        <ConfirmModal
          title={`Renew certificates for ${confirmRenew.name}?`}
          message={renewWarning(confirmRenew)}
          confirmLabel="Renew"
          onConfirm={() => void renew(confirmRenew)}
          onClose={() => setConfirmRenew(null)}
        />
      )}

      {renewed && (
        <OperatorCreated
          created={renewed.result}
          mode="reinstalled"
          exposure={renewed.operator.exposure ?? 'cluster'}
          fusionKind={fusionKind}
          onConnect={canConsent ? (id) => connect(id, renewed.operator.id) : undefined}
          onClose={() => setRenewed(null)}
        />
      )}

      {certsFor && <OperatorCertificatesModal operator={certsFor} clusterName={(id) => clusters.find((c) => c.id === id)?.name ?? ''} onClose={() => setCertsFor(null)} />}
      {addressFor && (
        <OperatorAddressModal operator={addressFor} central={addressFor.id === CENTRAL_OPERATOR_ID ? { service: fusion.status?.central?.service ?? '', namespace: fusion.status?.central?.namespace ?? '' } : undefined} onClose={() => setAddressFor(null)} onDone={() => { actionDone(); void fusion.refresh() }} />
      )}
      {healthFor && <HealthModal operator={healthFor} onClose={() => setHealthFor(null)} onDone={actionDone} />}
      {removing && <RemoveOperatorModal operator={removing.operator} mode={removing.mode} onClose={() => setRemoving(null)} onDone={actionDone} />}

      {telemetry.dialogs}
    </>
  )
}

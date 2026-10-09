import clsx from 'clsx'
import { ChevronRight, Plus, Radio } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { ConfirmModal } from '@/components/forms'
import ComponentsTable from '@/components/operators/ComponentsTable'
import CreateOperatorModal from '@/components/operators/CreateOperatorModal'
import { useFusion } from '@/components/operators/FusionPanel'
import HealthModal from '@/components/operators/HealthModal'
import { HEARTBEAT_WHAT } from '@/components/operators/HeartbeatCommands'
import { OperatorAddressModal } from '@/components/operators/OperatorAddress'
import OperatorCertificatesModal from '@/components/operators/OperatorCertificatesModal'
import OperatorCreated from '@/components/operators/OperatorCreated'
import PipelinePath from '@/components/operators/PipelinePath'
import RemoveOperatorModal from '@/components/operators/RemoveOperatorModal'
import type { RowMenuItem } from '@/components/operators/RowMenu'
import StateChip from '@/components/operators/StateChip'
import WhatToDoModal from '@/components/operators/WhatToDoModal'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { Button, EmptyState, ErrorBanner, ICON_SM, PageHeader, TableSkeleton } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator } from '@/lib/api'
import { operatorFromDestination } from '@/lib/destinationCatalog'
import { CENTRAL_OPERATOR_ID, fusionSentence } from '@/lib/fusionStatus'
import { isReportingHealth, receiverAuthOf } from '@/lib/operatorHealth'
import { buildComponents, buildHops, KIND_PLURAL, KINDS, needAttention, type ComponentRow, type Kind, type Todo } from '@/lib/operatorsView'
import type { RegionalOperator } from '@/lib/types'
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

/** How many problems the strip lists before it says "and N more": the table below has all of them. */
const STRIP_MAX = 3

/**
 * The telemetry pipeline: every hop from a cluster's discovery agent to FUSION, and every component on it in one table. Local operators
 * are not records of their own (an agent's collectors, turned on per signal by a telemetry request), regional operators are, and the
 * central operator is FUSION's door: all of them share the columns, the four state words and the menu.
 */
export default function PipelinePage() {
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const conn = useServer((s) => s.conn)
  const isAdmin = useServer((s) => s.isAdmin)
  const canEdit = useServer((s) => s.canEdit)
  const rawAgents = useServer((s) => s.state?.agents)
  const { agents, clusters } = useTopology()
  const admin = isAdmin()
  const canConsent = conn() != null && canEdit()
  const telemetry = useTelemetryFlow()

  const { operators, loaded, error: loadError, reload } = useOperators(admin)
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

  // "Last data" is a column to watch move, so the page's clock runs between the polls.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(t)
  }, [])

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
  const [todoFor, setTodoFor] = useState<ComponentRow | null>(null)

  // What a destination can be named by: the full list for an administrator, the read model for anyone else.
  const known = useMemo(() => (admin ? operators : destinations.map(operatorFromDestination)), [admin, operators, destinations])
  const rows = useMemo(() => buildComponents({ agents, clusters, rawAgents, intents, operators, known, now }), [agents, clusters, rawAgents, intents, operators, known, now])
  const fusionKind = fusionSentence(fusion.status).kind
  const hops = useMemo(
    () => buildHops(rows, { kind: fusionKind, lastDataAt: fusion.status?.lastDataAt }).filter((h) => admin || (h.key !== 'regional' && h.key !== 'central')),
    [rows, fusionKind, fusion.status?.lastDataAt, admin],
  )
  const attention = needAttention(rows)

  const asked = sp.get('kind')
  const filter: Kind | 'all' = KINDS.find((k) => k === asked) ?? 'all'
  const setFilter = (k: Kind | 'all') => setSp((p) => { const n = new URLSearchParams(p); if (k === 'all') n.delete('kind'); else n.set('kind', k); return n }, { replace: true })
  const counts = { all: rows.length, ...Object.fromEntries(KINDS.map((k) => [k, rows.filter((r) => r.kind === k).length])) } as Record<Kind | 'all', number>
  const shown = filter === 'all' ? rows : rows.filter((r) => r.kind === filter)

  // `loaded` is true once a read has finished, answered or failed: with nothing to show and an error, the list is unknown, not empty.
  const ready = !admin || loaded
  const actionDone = () => {
    setActionError('')
    void reload()
  }

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

  const connect = (agentId: string, operatorId: string) => telemetry.start(agentId, undefined, operatorId)
  const sourcesOf = (op: RegionalOperator) =>
    op.sourceClusterIds.map((id) => ({ id, name: clusters.find((c) => c.id === id)?.name ?? id, agent: agents.find((a) => a.status === 'approved' && a.clusterId === id) })).filter((s) => !!s.agent)

  /** The same menu in the same place on every row, in the same order: change what it sends, details, certificates, then the two that end it.
   *  What does not apply to a kind is left out; what applies and cannot be used right now stays, dimmed, with the reason under it. */
  const menuFor = (r: ComponentRow): RowMenuItem[] => {
    const op = r.operator
    if (!op) {
      if (r.kind === 'agent') return [{ key: 'agents', label: 'Open in Agents', onSelect: () => navigate('/agents'), testId: `agent-open-${r.name}` }]
      return [{ key: 'configure', label: 'Change what it sends…', onSelect: () => telemetry.start(r.agentId), disabled: !canConsent, title: canConsent ? undefined : 'Changing telemetry needs permission to edit', testId: `local-configure-${r.name}` }]
    }
    const remove: RowMenuItem = { key: 'delete', label: 'Stop and remove…', danger: true, onSelect: () => setRemoving({ operator: op, mode: 'delete' }), testId: `operator-delete-${op.name}` }
    if (r.ended) return [remove]
    const address: RowMenuItem = { key: 'address', label: 'Reachable at…', onSelect: () => setAddressFor(op), testId: `operator-address-open-${op.name}` }
    if (r.kind === 'central') return [address, { key: 'fusion', label: 'Open FUSION', onSelect: () => navigate('/fusion'), testId: 'operator-open-fusion' }]
    return [
      ...sourcesOf(op).map((s) => ({ key: `connect-${s.id}`, label: `Connect ${s.name}…`, onSelect: () => connect(s.agent!.id, op.id), disabled: !canConsent, title: canConsent ? undefined : 'Connecting a cluster needs permission to edit', testId: `operator-connect-${op.name}-${s.id}` })),
      address,
      { key: 'certs', label: 'Issued certificates', onSelect: () => setCertsFor(op), testId: `operator-certs-${op.name}` },
      // Dimmed while any renewal runs (not only this row's): one at a time, and the reason is visible rather than a click that does nothing.
      { key: 'renew', label: 'Renew certificates…', onSelect: () => setConfirmRenew(op), disabled: renewing !== null, title: renewing !== null ? 'Another renewal is in progress' : undefined, testId: `operator-renew-${op.name}` },
      { key: 'health', label: isReportingHealth(op) ? 'Rotate health credential' : 'Enable health reporting', onSelect: () => setHealthFor(op), testId: `operator-health-open-${op.name}` },
      { key: 'revoke', label: 'Disconnect…', danger: true, onSelect: () => setRemoving({ operator: op, mode: 'revoke' }), testId: `operator-revoke-${op.name}` },
      remove,
    ]
  }

  const doTodo = (kind: NonNullable<Todo['action']>['kind']) => {
    const row = todoFor
    setTodoFor(null)
    if (!row) return
    if (kind === 'configure') telemetry.start(row.agentId)
    else if (kind === 'address' && row.operator) setAddressFor(row.operator)
    else if (kind === 'renew' && row.operator) setConfirmRenew(row.operator)
    else navigate(kind === 'fusion' ? '/fusion' : '/agents')
  }

  const chips = (['all', ...KINDS] as const).filter((k) => k === 'all' || counts[k])
  const empty = ready && rows.length === 0 && !loadError

  return (
    <>
      <PageHeader
        title="Pipeline"
        description="How telemetry gets from your clusters to FUSION, and whether each step is working."
        actions={
          <>
            {admin && <Button onClick={() => setCreating(true)} data-testid="operator-open"><Plus size={ICON_SM} /> New operator</Button>}
            {canConsent && <Button variant="primary" onClick={() => telemetry.start()} data-testid="telemetry-setup"><Radio size={ICON_SM} /> Set up telemetry</Button>}
          </>
        }
      />

      {loadError && <ErrorBanner className="mb-4">{loadError}</ErrorBanner>}
      {actionError && <ErrorBanner className="mb-4" onDismiss={() => setActionError('')}>{actionError}</ErrorBanner>}

      {!ready ? (
        // The list is live and org-scoped, so it cannot be read from the already-synced store: until it answers, "none" would be a lie.
        <TableSkeleton colCount={7} />
      ) : empty ? (
        <EmptyState
          title="Nothing is sending telemetry yet"
          description="Set up telemetry on a connected cluster to see the path its data takes, from the discovery agent to FUSION, and whether each step is working."
          action={canConsent ? <Button onClick={() => telemetry.start()} data-testid="telemetry-setup-empty"><Radio size={ICON_SM} /> Set up telemetry</Button> : undefined}
        />
      ) : (
        <>
          {attention.length > 0 && (
            <section aria-labelledby="attention-title" className="mb-6 space-y-3" data-testid="pipeline-attention">
              <h2 id="attention-title" className="text-sm font-medium text-nb-300">What needs attention</h2>
              <ul className="divide-y divide-nb-850 rounded-xl border border-nb-850 bg-nb-925">
                {attention.slice(0, STRIP_MAX).map((r) => (
                  <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
                    <StateChip state={r.verdict.state} />
                    <p className="min-w-0 flex-1 basis-60 text-sm text-nb-400"><span className="font-medium text-nb-300">{r.name}.</span> {r.verdict.reason}</p>
                    <Button size="sm" onClick={() => setTodoFor(r)} aria-label={`What to do about ${r.name}`} data-testid={`attention-todo-${r.id}`}>What to do</Button>
                  </li>
                ))}
              </ul>
              {attention.length > STRIP_MAX && <p className="text-xs text-nb-500">and {attention.length - STRIP_MAX} more, marked in the table below.</p>}
            </section>
          )}

          <PipelinePath hops={hops} now={now} selected={filter} onSelect={(k) => setFilter(filter === k ? 'all' : k)} />

          <div className="mb-4 flex flex-wrap items-center gap-1.5" role="group" aria-label="Show components">
            {chips.map((k) => (
              <button
                key={k}
                onClick={() => setFilter(k)}
                aria-pressed={filter === k}
                className={clsx('rounded-full border px-3 py-1 text-xs transition-colors', filter === k ? 'border-accent/40 bg-accent-soft text-accent' : 'border-nb-850 text-nb-400 hover:border-nb-800 hover:text-nb-300')}
                data-testid={`pipeline-filter-${k}`}
              >
                {k === 'all' ? 'All' : KIND_PLURAL[k]} <span className="text-nb-500">{counts[k]}</span>
              </button>
            ))}
          </div>

          <ComponentsTable rows={shown} now={now} menuFor={menuFor} onWhatToDo={setTodoFor} />
          {!admin && <p className="mt-3 text-xs text-nb-500">Regional operators are only shown to organisation administrators.</p>}

          {admin && (
            <details className="group mt-6 rounded-xl border border-nb-850 bg-nb-925" data-testid="operator-access-note">
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
        </>
      )}

      {todoFor?.verdict.todo && <WhatToDoModal row={todoFor} todo={todoFor.verdict.todo} onClose={() => setTodoFor(null)} onAction={doTodo} busy={renewing !== null && todoFor.verdict.todo.action?.kind === 'renew' ? 'Another renewal is in progress' : undefined} />}

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

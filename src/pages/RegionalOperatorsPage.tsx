import { Antenna, Globe2, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { ConfirmModal } from '@/components/forms'
import ProcessorEditor from '@/components/telemetry/ProcessorEditor'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { Button, ChipList, CheckboxList, ComboField, EmptyState, ErrorBanner, Field, Input, Modal, PageHeader, Pill, Table, TableSkeleton, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator } from '@/lib/api'
import { extrasOf, TELEMETRY_SIGNALS } from '@/lib/consent'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '@/lib/exportPresets'
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
  const out: string[] = []
  if (!d.endpoint.trim()) out.push('An export endpoint is required')
  return out
}

/** Name + source clusters + destination + extra processors, the whole create form's shape in one place so
 *  the create modal and (later, if this page grows an edit form) an edit modal can share it unmodified. */
interface Draft {
  name: string
  sourceClusterIds: string[]
  destination: OperatorDestination
  extraProcessors: ProcessorEntry[]
}
const emptyDraft: Draft = { name: '', sourceClusterIds: [], destination: emptyDestination, extraProcessors: [] }

/** The receiver token and install command, shown once: the server keeps only a hash of the token, so this
 *  is the only chance to copy it - same "shown once, gone forever" convention as TeamPage's InviteCreated. */
function OperatorCreated({ created, extraProcessors, onClose }: { created: CreatedOperator; extraProcessors: ProcessorEntry[]; onClose: () => void }) {
  const install = buildOperatorInstallCommand(created.install, extraProcessors)
  return (
    <Modal open onClose={onClose} title={`${created.operator.name} created`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <p className="text-sm text-nb-400">
        Run this where the operator itself should live. It carries the receiver token below already; the token
        is shown only now - if it is lost, revoke this operator and create another.
      </p>
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">Create the receiver token Secret first</div>
        <CopyCommand text={created.secretCommand} />
      </div>
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">Then install the operator</div>
        <CopyCommand text={install} />
      </div>
      {created.tlsSecretCommand && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">
            And create the receiver's TLS certificate Secret (mTLS, on top of the token above - the install command already turns it on)
          </div>
          <CopyCommand text={created.tlsSecretCommand} />
        </div>
      )}
      {created.reminders.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">
            Informational only - nothing below runs on your behalf. Each source cluster needs its own copy of the client certificate Secret, then its agent's export endpoint pointed here:
          </div>
          <div className="space-y-1.5">
            {created.reminders.map((r) => <CopyCommand key={r} text={r} />)}
          </div>
        </div>
      )}
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
      <Icon size={14} aria-hidden /> {label}
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
  const admin = isAdmin()
  const canConsent = conn() != null && canEdit()
  const telemetry = useTelemetryFlow()

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

  const destinationNote = unsupportedDestinationNote(draft.destination.endpoint)
  const problems = [
    draft.name.trim().length < 2 ? ['A name of at least two characters is required'] : [],
    draft.sourceClusterIds.length === 0 ? ['Pick at least one source cluster'] : [],
    destinationProblems(draft.destination),
    operatorProcessorProblems(draft.extraProcessors),
  ].flat()

  const create = () =>
    act(async () => {
      const c = conn()
      if (!c) return
      const r = await api.createOperator(c, draft.name.trim(), draft.sourceClusterIds, draft.destination)
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
            <Button variant="primary" onClick={() => { setDraft(emptyDraft); setCreating(true) }} data-testid="operator-open"><Plus size={16} /> New operator</Button>
          ) : undefined
        }
      />

      <div className="mb-4 flex w-fit overflow-hidden rounded-md border border-nb-850" role="tablist" aria-label="Operator category">
        <CategoryTab id="local" label="Local" icon={Antenna} active={category === 'local'} onClick={() => setCategory('local')} testId="operators-local" />
        <CategoryTab id="regional" label="Regional" icon={Globe2} active={category === 'regional'} onClick={() => setCategory('regional')} testId="operators-regional" />
      </div>

      {category === 'local' ? (
        localRows.length === 0 ? (
          <EmptyState
            title="No local operators running yet"
            description="A local operator is just an already-connected cluster's agent with at least one telemetry signal turned on. Configure one to see it here."
            action={canConsent ? <Button variant="primary" onClick={() => telemetry.start()}><Antenna size={16} /> Configure telemetry</Button> : undefined}
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
                  <Td className="text-nb-300">{op.name}</Td>
                  <Td><Pill>{op.status === 'active' ? 'Active' : `Revoked${op.reason ? `: ${op.reason}` : ''}`}</Pill></Td>
                  <Td className="text-nb-500">{op.sourceClusterIds.length} cluster{op.sourceClusterIds.length === 1 ? '' : 's'}</Td>
                  <Td className="text-nb-500"><span className="font-mono text-xs">{op.destination.endpoint}</span></Td>
                  <Td className="text-nb-500">{when(op.createdAt)}</Td>
                  <Td className="text-right">
                    {op.status === 'active' && (
                      <Button size="sm" variant="danger" onClick={() => setRevoking(op)}>Revoke</Button>
                    )}
                    <Button size="sm" variant="danger" onClick={() => setDeleting(op)}><Trash2 size={13} /></Button>
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
        footer={<><Button onClick={() => setCreating(false)}>Cancel</Button><Button variant="primary" onClick={create} disabled={problems.length > 0} data-testid="operator-create">Create operator</Button></>}
      >
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (problems.length === 0) void create() }}>
          <Field label="Name"><Input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} maxLength={80} data-testid="operator-name" /></Field>

          <Field label="Source clusters" hint="Clusters whose already-exported telemetry this operator aggregates. Only clusters with a currently-approved agent are listed.">
            <CheckboxList
              options={clusterOptions}
              value={draft.sourceClusterIds}
              onChange={(v) => setDraft({ ...draft, sourceClusterIds: v })}
              emptyLabel="No cluster has an approved agent yet - approve one on the Agents page first."
            />
          </Field>

          <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
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

      {revoking && (
        <ConfirmModal
          title={`Revoke ${revoking.name}?`}
          message="Its receiver token stops accepting telemetry at once. The record stays for the audit trail; delete it separately if you want it gone entirely."
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

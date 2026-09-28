import { Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { ConfirmModal } from '@/components/forms'
import ProcessorEditor from '@/components/telemetry/ProcessorEditor'
import { Button, CheckboxList, ComboField, EmptyState, ErrorBanner, Field, Input, Modal, PageHeader, Pill, Table, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator } from '@/lib/api'
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
      {created.reminders.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-nb-500">
            Informational only - nothing below runs on your behalf. Each source cluster's own agent needs its export endpoint pointed here separately:
          </div>
          <div className="space-y-1.5">
            {created.reminders.map((r) => <CopyCommand key={r} text={r} />)}
          </div>
        </div>
      )}
    </Modal>
  )
}

/** Fleet management for regional operators: standalone aggregation points that receive OTLP from a set of
 *  approved agents' clusters and re-export it further up. Not folded into AgentsPage - an operator has no
 *  live connection, heartbeat, or approval flow the way an Agent does (see lib/types.ts's own doc comment
 *  on RegionalOperator), so a dedicated page keeps both models honest instead of a leaky shared branch. */
export default function RegionalOperatorsPage() {
  const conn = useServer((s) => s.conn)
  const isAdmin = useServer((s) => s.isAdmin)
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

  const load = useCallback(async () => {
    const c = conn()
    if (!c || !admin) return
    try {
      setOperators(await api.listOperators(c))
      setError('')
    } catch (e) {
      setError(problem(e, 'Could not load the regional operators.'))
    }
  }, [conn, admin])
  useEffect(() => {
    void load()
  }, [load])

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

  if (!admin) {
    return (
      <>
        <PageHeader title="Regional operators" description="Standalone aggregation points that receive telemetry from a set of clusters and re-export it further up." />
        <EmptyState title="Administrators only" description="Only organisation administrators can see and manage regional operators." />
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="Regional operators"
        description="Each aggregates telemetry already exported by a set of approved clusters and re-exports it to one destination - another observability backend, or (soon) another regional operator above it."
        actions={<Button variant="primary" onClick={() => { setDraft(emptyDraft); setCreating(true) }} data-testid="operator-open"><Plus size={16} /> New operator</Button>}
      />
      <ErrorLine text={error} />

      {operators.length === 0 ? (
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
    </>
  )
}

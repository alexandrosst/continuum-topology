import { Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Button, Field, Input, LabelsEditor, Modal, Select } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { uid, useTopology } from '@/store/topology'
import { useServer } from '@/store/server'
import { DEFAULT_ORG, type Dependency, type Service, type ServiceKind } from '@/lib/types'
import { StatusSelect, FormFooter, DiscoveredNote } from './shared'

/* ---------- Service (+ outgoing dependencies) ---------- */
export function ServiceForm({ initial, onClose, defaultClusterId }: { initial: Service | null; onClose: () => void; defaultClusterId?: string }) {
  const { clusters, nodes, services, dependencies, applications, saveService, resetOverrides, upsertDependency, deleteDependency } = useTopology()
  const by = useServer((s) => s.user?.username)
  const [f, setF] = useState<Service>(
    initial ?? {
      id: uid('w'),
      orgId: DEFAULT_ORG,
      name: '',
      namespace: 'default',
      clusterId: defaultClusterId ?? clusters[0]?.id ?? '',
      kind: 'Deployment',
      image: '',
      replicas: 1,
      nodeIds: [],
      status: 'unknown',
      labels: {},
      source: 'manual',
    },
  )
  const [labels, setLabels] = useState(f.labels)
  // This form edits service → service calls; calls to devices or external endpoints are left untouched.
  const isEditable = (d: Dependency) => d.from === f.id && d.fromKind === 'service' && d.toKind === 'service'
  const [deps, setDeps] = useState<Dependency[]>(() => dependencies.filter(isEditable))
  const set = <K extends keyof Service>(k: K, v: Service[K]) => setF((p) => ({ ...p, [k]: v }))

  const clusterNodes = useMemo(() => nodes.filter((n) => n.clusterId === f.clusterId), [nodes, f.clusterId])
  const targets = services.filter((w) => w.id !== f.id)
  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '?'

  const save = () => {
    saveService({ ...f, name: f.name.trim(), labels }, by)
    const kept = new Set(deps.map((d) => d.id))
    dependencies.filter((d) => isEditable(d) && !kept.has(d.id)).forEach((d) => deleteDependency(d.id))
    deps.filter((d) => d.to).forEach((d) => upsertDependency({ ...d, from: f.id }))
    onClose()
  }

  return (
    <Modal
      open
      onClose={onClose}
      width="max-w-2xl"
      title={initial ? 'Edit service' : 'Add service'}
      description="A microservice / application running in a cluster, plus the services it calls."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim() || !f.clusterId}
          onCancel={onClose}
          onSave={save}
          onReset={
            initial && hasOverrides(initial)
              ? () => {
                  resetOverrides('service', initial.id)
                  onClose()
                }
              : undefined
          }
        />
      }
    >
      <DiscoveredNote entity={initial} />
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name">
          <Input autoFocus value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. inference-edge" />
        </Field>
        <Field label="Cluster">
          <Select
            value={f.clusterId}
            onChange={(e) => setF((p) => ({ ...p, clusterId: e.target.value, nodeIds: [] }))}
          >
            {clusters.length === 0 && <option value="">No clusters yet</option>}
            {clusters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Namespace">
          <Input value={f.namespace} onChange={(e) => set('namespace', e.target.value)} />
        </Field>
        <Field label="Kind">
          <Select value={f.kind} onChange={(e) => set('kind', e.target.value as ServiceKind)}>
            {(['Deployment', 'StatefulSet', 'DaemonSet', 'Job'] as const).map((k) => (
              <option key={k}>{k}</option>
            ))}
          </Select>
        </Field>
        <Field label="Application" hint="Groups related services. Manage under Applications." className="col-span-2">
          <Select value={f.applicationId ?? ''} onChange={(e) => set('applicationId', e.target.value || undefined)}>
            <option value="">No application</option>
            {applications.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Image" className="col-span-2">
          <Input value={f.image} onChange={(e) => set('image', e.target.value)} placeholder="ghcr.io/org/app:1.0" />
        </Field>
        <Field label="Replicas">
          <Input type="number" min={0} value={f.replicas} onChange={(e) => set('replicas', Number(e.target.value))} />
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Data sensitivity" hint="Used by placement policy: confidential services should not leave trusted sites." className="col-span-2">
          <Select value={f.sensitivity ?? ''} onChange={(e) => set('sensitivity', (e.target.value || undefined) as Service['sensitivity'])}>
            <option value="">Not set</option>
            <option value="public">Public</option>
            <option value="internal">Internal</option>
            <option value="confidential">Confidential</option>
          </Select>
        </Field>

        <div className="col-span-2">
          <span className="mb-1.5 block text-sm font-medium text-nb-300">Runs on nodes</span>
          {clusterNodes.length === 0 ? (
            <p className="text-xs text-nb-500">This cluster has no nodes yet. Add nodes to place the service.</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {clusterNodes.map((n) => {
                const on = f.nodeIds.includes(n.id)
                return (
                  <button
                    key={n.id}
                    type="button"
                    onClick={() => set('nodeIds', on ? f.nodeIds.filter((x) => x !== n.id) : [...f.nodeIds, n.id])}
                    className={
                      'rounded-md border px-2.5 py-1 text-xs transition-colors ' +
                      (on ? 'border-accent/50 bg-accent-soft text-accent' : 'border-nb-800 bg-nb-925 text-nb-400 hover:bg-nb-940')
                    }
                  >
                    {n.name}
                  </button>
                )
              })}
            </div>
          )}
        </div>

        <Field label="Labels" className="col-span-2">
          <LabelsEditor value={labels} onChange={setLabels} keyPlaceholder="tier" valuePlaceholder="backend" />
        </Field>

        <div className="col-span-2 rounded-lg border border-nb-850 bg-nb-930 p-4">
          <div className="mb-3 flex items-center justify-between">
            <div>
              <div className="text-sm font-medium text-nb-300">Calls (outgoing dependencies)</div>
              <div className="text-xs text-nb-500">Drawn as edges in the Application view.</div>
            </div>
            <Button
              size="sm"
              disabled={targets.length === 0}
              onClick={() => setDeps((d) => [...d, { id: uid('d'), orgId: DEFAULT_ORG, from: f.id, fromKind: 'service', to: targets[0].id, toKind: 'service', protocol: 'HTTP', sources: ['manual'], confidence: 'high' }])}
            >
              <Plus size={14} /> Add
            </Button>
          </div>
          {deps.length === 0 && <p className="text-xs text-nb-500">No dependencies.</p>}
          <div className="space-y-2">
            {deps.map((d, i) => {
              const patch = (p: Partial<Dependency>) => setDeps((all) => all.map((x, j) => (j === i ? { ...x, ...p } : x)))
              return (
                <div key={d.id} className="grid grid-cols-[1fr_110px_90px_36px] gap-2">
                  <Select value={d.to} onChange={(e) => patch({ to: e.target.value })}>
                    {targets.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} · {clusterName(t.clusterId)}
                      </option>
                    ))}
                  </Select>
                  <Input value={d.protocol} onChange={(e) => patch({ protocol: e.target.value })} placeholder="Protocol" />
                  <Input
                    type="number"
                    value={d.port ?? ''}
                    onChange={(e) => patch({ port: e.target.value ? Number(e.target.value) : undefined })}
                    placeholder="Port"
                  />
                  <Button variant="ghost" className="h-9 px-0" aria-label="Remove dependency" onClick={() => setDeps((all) => all.filter((_, j) => j !== i))}>
                    <Trash2 size={14} />
                  </Button>
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </Modal>
  )
}

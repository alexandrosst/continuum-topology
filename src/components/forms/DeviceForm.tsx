import { Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { Button, ComboField, Field, Input, LabelsEditor, Modal, Select } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { DEVICE_PROTOCOL_OPTIONS } from '@/lib/present'
import { uid, useTopology } from '@/store/topology'
import { useServer } from '@/store/server'
import { DEFAULT_ORG, DEVICE_KINDS, type Dependency, type Device, type DeviceKind } from '@/lib/types'
import { StatusSelect, ConnectivitySelect, FormFooter, DiscoveredNote } from './shared'

/* ---------- Device (+ what it talks to) ---------- */
export function DeviceForm({ initial, onClose }: { initial: Device | null; onClose: () => void }) {
  const { clusters, nodes, services, sites, applications, dependencies, saveDevice, resetOverrides, upsertDependency, deleteDependency } = useTopology()
  const by = useServer((s) => s.user?.username)
  const [f, setF] = useState<Device>(
    initial ?? {
      id: uid('dev'),
      orgId: DEFAULT_ORG,
      name: '',
      kind: 'sensor',
      count: 1,
      protocol: 'MQTT',
      connectivity: 'ethernet',
      status: 'unknown',
      labels: {},
      source: 'manual',
    },
  )
  const [labels, setLabels] = useState(f.labels)
  const isEditable = (d: Dependency) => d.from === f.id && d.fromKind === 'device' && d.toKind === 'service'
  const [deps, setDeps] = useState<Dependency[]>(() => dependencies.filter(isEditable))
  const set = <K extends keyof Device>(k: K, v: Device[K]) => setF((p) => ({ ...p, [k]: v }))
  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '?'

  const save = () => {
    saveDevice({ ...f, name: f.name.trim(), count: Math.max(1, Math.round(f.count) || 1), labels }, by)
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
      title={initial ? 'Edit device' : 'Add device'}
      description="Sensors, cameras, PLCs and other things that belong to an application but do not run on Kubernetes. Use one record for a fleet of identical units."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim()}
          onCancel={onClose}
          onSave={save}
          onReset={
            initial && hasOverrides(initial)
              ? () => {
                  resetOverrides('device', initial.id)
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
          <Input autoFocus value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. Temperature sensors" />
        </Field>
        <Field label="Kind">
          <Select value={f.kind} onChange={(e) => set('kind', e.target.value as DeviceKind)}>
            {DEVICE_KINDS.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Units" hint="How many identical devices this record stands for.">
          <Input type="number" min={1} value={f.count} onChange={(e) => set('count', Number(e.target.value))} />
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Application">
          <Select value={f.applicationId ?? ''} onChange={(e) => set('applicationId', e.target.value || undefined)}>
            <option value="">No application</option>
            {applications.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Site" hint="Where the devices physically are.">
          <Select value={f.siteId ?? ''} onChange={(e) => set('siteId', e.target.value || undefined)}>
            <option value="">No site</option>
            {sites.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Protocol">
          <ComboField value={f.protocol} onChange={(v) => set('protocol', v)} options={DEVICE_PROTOCOL_OPTIONS} placeholder="MQTT, OPC UA, Modbus, RTSP…" />
        </Field>
        <Field label="Connectivity">
          <ConnectivitySelect value={f.connectivity} onChange={(v) => set('connectivity', v ?? 'unknown')} />
        </Field>
        <Field label="Attached to node" hint="Only for devices wired to a machine (USB / CSI camera, serial PLC)." className="col-span-2">
          <Select value={f.gatewayNodeId ?? ''} onChange={(e) => set('gatewayNodeId', e.target.value || undefined)}>
            <option value="">Not attached to a node</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name} · {clusterName(n.clusterId)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Hardware model">
          <Input value={f.hardwareModel ?? ''} onChange={(e) => set('hardwareModel', e.target.value || undefined)} />
        </Field>
        <Field label="Firmware">
          <Input value={f.firmware ?? ''} onChange={(e) => set('firmware', e.target.value || undefined)} />
        </Field>
        <Field label="Labels" className="col-span-2">
          <LabelsEditor value={labels} onChange={setLabels} keyPlaceholder="line" valuePlaceholder="3" />
        </Field>

        <div className="col-span-2 rounded-lg border border-nb-850 bg-nb-930 p-4">
          <div className="mb-3 flex items-center justify-between">
            <div>
              <div className="text-sm font-medium text-nb-300">Sends data to</div>
              <div className="text-xs text-nb-500">Usually a broker or ingest service. Drawn as edges in the Application view.</div>
            </div>
            <Button
              size="sm"
              disabled={services.length === 0}
              onClick={() => setDeps((d) => [...d, { id: uid('d'), orgId: DEFAULT_ORG, from: f.id, fromKind: 'device', to: services[0].id, toKind: 'service', protocol: f.protocol || 'MQTT', sources: ['manual'], confidence: 'high' }])}
            >
              <Plus size={14} /> Add
            </Button>
          </div>
          {deps.length === 0 && <p className="text-xs text-nb-500">Not connected to any service.</p>}
          <div className="space-y-2">
            {deps.map((d, i) => {
              const patch = (p: Partial<Dependency>) => setDeps((all) => all.map((x, j) => (j === i ? { ...x, ...p } : x)))
              return (
                <div key={d.id} className="grid grid-cols-[1fr_110px_90px_36px] gap-2">
                  <Select value={d.to} onChange={(e) => patch({ to: e.target.value })}>
                    {services.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} · {clusterName(t.clusterId)}
                      </option>
                    ))}
                  </Select>
                  <Input value={d.protocol} onChange={(e) => patch({ protocol: e.target.value })} placeholder="Protocol" />
                  <Input type="number" value={d.port ?? ''} onChange={(e) => patch({ port: e.target.value ? Number(e.target.value) : undefined })} placeholder="Port" />
                  <Button variant="ghost" className="h-9 px-0" aria-label="Remove connection" onClick={() => setDeps((all) => all.filter((_, j) => j !== i))}>
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

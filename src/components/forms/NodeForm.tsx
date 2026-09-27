import { useState } from 'react'
import { ComboField, EvidenceChip, Field, Input, LabelsEditor, Modal, Select } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { OS_OPTIONS } from '@/lib/present'
import { useWeakValue } from '@/store/rowEvidence'
import { uid, useTopology } from '@/store/topology'
import { useServer } from '@/store/server'
import { DEFAULT_ORG, type MachineNode } from '@/lib/types'
import { StatusSelect, ConnectivitySelect, FormFooter, DiscoveredNote } from './shared'

/* ---------- Node ---------- */
export function NodeForm({ initial, onClose, defaultClusterId }: { initial: MachineNode | null; onClose: () => void; defaultClusterId?: string }) {
  const clusters = useTopology((s) => s.clusters)
  const save = useTopology((s) => s.saveNode)
  const resetOverrides = useTopology((s) => s.resetOverrides)
  const by = useServer((s) => s.user?.username)
  const [f, setF] = useState<MachineNode>(
    initial ?? {
      id: uid('n'),
      orgId: DEFAULT_ORG,
      name: '',
      clusterId: defaultClusterId ?? clusters[0]?.id ?? '',
      role: 'worker',
      kind: 'vm',
      ip: '',
      os: 'Ubuntu 22.04',
      cpu: 4,
      memoryGb: 16,
      status: 'unknown',
      labels: {},
      source: 'manual',
    },
  )
  const [labels, setLabels] = useState(f.labels)
  const set = <K extends keyof MachineNode>(k: K, v: MachineNode[K]) => setF((p) => ({ ...p, [k]: v }))
  // Same as ClusterForm above, including the one-click confirm for a guess.
  const weak = useWeakValue('node')
  const confirmField = useTopology((s) => s.confirmField)
  const chip = (attr: string, field?: string) => {
    const w = weak(f as never, attr, field)
    if (!w) return undefined
    return (
      <span className="inline-flex items-center gap-1.5">
        <EvidenceChip level={w.level} why={w.why} />
        {w.level === 'guess' && initial && by && (
          <button
            onClick={() => confirmField('node', initial.id, field ?? attr, by)}
            className="text-[11px] text-accent hover:underline"
            title="Keep this value as it is; it will not be overwritten by rediscovery."
          >
            confirm
          </button>
        )}
      </span>
    )
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit node' : 'Add node'}
      description="A machine (VM, bare-metal server or edge device) that is part of a cluster."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim() || !f.clusterId}
          onCancel={onClose}
          onSave={() => {
            save({ ...f, name: f.name.trim(), labels }, by)
            onClose()
          }}
          onReset={
            initial && hasOverrides(initial)
              ? () => {
                  resetOverrides('node', initial.id)
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
          <Input autoFocus value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. worker-1" />
        </Field>
        <Field label="Cluster">
          <Select value={f.clusterId} onChange={(e) => set('clusterId', e.target.value)}>
            {clusters.length === 0 && <option value="">No clusters yet</option>}
            {clusters.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Role">
          <Select value={f.role} onChange={(e) => set('role', e.target.value as MachineNode['role'])}>
            <option value="worker">Worker</option>
            <option value="control-plane">Control plane</option>
          </Select>
        </Field>
        <Field label="Machine type" adornment={chip('kind')}>
          <Select value={f.kind} onChange={(e) => set('kind', e.target.value as MachineNode['kind'])}>
            <option value="vm">VM</option>
            <option value="bare-metal">Bare metal</option>
            <option value="edge-device">Edge device</option>
          </Select>
        </Field>
        <Field label="IP address">
          <Input value={f.ip} onChange={(e) => set('ip', e.target.value)} placeholder="10.0.0.10" />
        </Field>
        <Field label="OS">
          <ComboField value={f.os} onChange={(v) => set('os', v)} options={OS_OPTIONS} placeholder="Ubuntu 22.04" />
        </Field>
        <Field label="CPU cores">
          <Input type="number" min={1} value={f.cpu} onChange={(e) => set('cpu', Number(e.target.value))} />
        </Field>
        <Field label="Memory (GB)">
          <Input type="number" min={1} value={f.memoryGb} onChange={(e) => set('memoryGb', Number(e.target.value))} />
        </Field>
        <Field label="Architecture" hint="Decides which images can run here." adornment={chip('arch')}>
          <Select value={f.arch ?? ''} onChange={(e) => set('arch', e.target.value || undefined)}>
            <option value="">Unknown</option>
            {['amd64', 'arm64', 'arm', 'riscv64'].map((a) => (
              <option key={a}>{a}</option>
            ))}
          </Select>
        </Field>
        <Field label="Hardware model" adornment={chip('hardwareModel')}>
          <Input value={f.hardwareModel ?? ''} onChange={(e) => set('hardwareModel', e.target.value || undefined)} placeholder="Raspberry Pi 5, Jetson Orin…" />
        </Field>
        <Field label="Uplink" className="col-span-2" adornment={chip('connectivity')}>
          <ConnectivitySelect value={f.connectivity} onChange={(v) => set('connectivity', v)} />
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Labels" className="col-span-2">
          <LabelsEditor value={labels} onChange={setLabels} keyPlaceholder="accelerator" valuePlaceholder="jetson-orin" />
        </Field>
      </div>
    </Modal>
  )
}

import { countries } from 'country-flag-icons'
import { Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { GroupingPicker } from '@/components/GroupingPicker'
import { Button, Field, Input, Modal, Select } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { countryName } from '@/lib/present'
import { countryAt, findCities, nearestCity, siteLocationIssue, type City } from '@/lib/places'
import { usePlaceIndex } from '@/lib/places-data'
import { groupingAlternativesFor } from '@/lib/suggestions'
import { uid, useTopology } from '@/store/topology'
import { useServer } from '@/store/server'
import {
  CONNECTIVITY,
  DEFAULT_ORG,
  DEVICE_KINDS,
  SITE_KINDS,
  TIERS,
  type Application,
  type Connectivity,
  type Site,
  type Cluster,
  type Dependency,
  type Device,
  type DeviceKind,
  type MachineNode,
  type Status,
  type Service,
  type ServiceKind,
  type TrustZone,
} from '@/lib/types'

/* ---------- helpers ---------- */
export const parseLabels = (s: string): Record<string, string> =>
  Object.fromEntries(
    s
      .split(',')
      .map((p) => p.trim())
      .filter(Boolean)
      .map((p) => {
        const i = p.indexOf('=')
        return i === -1 ? [p, ''] : [p.slice(0, i).trim(), p.slice(i + 1).trim()]
      }),
  )
export const formatLabels = (l: Record<string, string>) =>
  Object.entries(l)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ')

const STATUSES: Status[] = ['healthy', 'degraded', 'offline', 'unknown']

function StatusSelect({ value, onChange }: { value: Status; onChange: (s: Status) => void }) {
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value as Status)}>
      {STATUSES.map((s) => (
        <option key={s} value={s}>
          {s[0].toUpperCase() + s.slice(1)}
        </option>
      ))}
    </Select>
  )
}

function TrustSelect({ value, onChange }: { value?: TrustZone; onChange: (v: TrustZone | undefined) => void }) {
  return (
    <Select value={value ?? ''} onChange={(e) => onChange((e.target.value || undefined) as TrustZone | undefined)}>
      <option value="">Not set</option>
      <option value="public">Public</option>
      <option value="private">Private</option>
      <option value="restricted">Restricted</option>
    </Select>
  )
}

function ConnectivitySelect({ value, onChange }: { value?: Connectivity; onChange: (v: Connectivity | undefined) => void }) {
  return (
    <Select value={value ?? ''} onChange={(e) => onChange((e.target.value || undefined) as Connectivity | undefined)}>
      <option value="">Not set</option>
      {CONNECTIVITY.map((c) => (
        <option key={c.value} value={c.value}>
          {c.label}
        </option>
      ))}
    </Select>
  )
}

function FormFooter({
  onCancel,
  onSave,
  disabled,
  isNew,
  onReset,
}: {
  onCancel: () => void
  onSave: () => void
  disabled: boolean
  isNew: boolean
  /** Shown only when a discovered entity carries human overrides. */
  onReset?: () => void
}) {
  return (
    <>
      {onReset && (
        <Button variant="ghost" className="mr-auto" onClick={onReset}>
          Reset to detected values
        </Button>
      )}
      <Button onClick={onCancel}>Cancel</Button>
      <Button variant="primary" onClick={onSave} disabled={disabled}>
        {isNew ? 'Add' : 'Save changes'}
      </Button>
    </>
  )
}

/** Explains the two-layer values on entities that came from discovery. */
function DiscoveredNote({ entity }: { entity: { source: string; overrides?: Record<string, unknown> } | null }) {
  if (!entity || entity.source !== 'discovered') return null
  const n = Object.keys(entity.overrides ?? {}).length
  return (
    <p className="mb-4 rounded-lg border border-nb-850 bg-nb-930 px-3.5 py-2.5 text-xs text-nb-400">
      Detected by discovery. Changes you make here are kept as overrides and survive rediscovery
      {n ? ` (${n} field${n === 1 ? '' : 's'} currently overridden).` : '.'}
    </p>
  )
}

/* ---------- Cluster ---------- */
export function ClusterForm({ initial, onClose }: { initial: Cluster | null; onClose: () => void }) {
  const save = useTopology((s) => s.saveCluster)
  const resetOverrides = useTopology((s) => s.resetOverrides)
  const sites = useTopology((s) => s.sites)
  const by = useServer((s) => s.user?.username)
  const [f, setF] = useState<Cluster>(
    initial ?? {
      id: uid('cl'),
      orgId: DEFAULT_ORG,
      name: '',
      tier: 'cloud',
      distribution: 'kubeadm',
      version: '',
      provider: '',
      region: '',
      status: 'unknown',
      labels: {},
      source: 'manual',
    },
  )
  const [labels, setLabels] = useState(formatLabels(f.labels))
  const set = <K extends keyof Cluster>(k: K, v: Cluster[K]) => setF((p) => ({ ...p, [k]: v }))

  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit cluster' : 'Add cluster'}
      description="A Kubernetes cluster and where it sits on the cloud–edge continuum."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim()}
          onCancel={onClose}
          onSave={() => {
            save({ ...f, name: f.name.trim(), labels: parseLabels(labels) }, by)
            onClose()
          }}
          onReset={
            initial && hasOverrides(initial)
              ? () => {
                  resetOverrides('cluster', initial.id)
                  onClose()
                }
              : undefined
          }
        />
      }
    >
      <DiscoveredNote entity={initial} />
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name" className="col-span-2">
          <Input autoFocus value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. edge-patras" />
        </Field>
        <Field label="Tier" hint="Drives vertical placement in the topology.">
          <Select value={f.tier} onChange={(e) => set('tier', e.target.value as Cluster['tier'])}>
            {TIERS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Distribution">
          <Input value={f.distribution} onChange={(e) => set('distribution', e.target.value)} placeholder="EKS, k3s, kubeadm…" />
        </Field>
        <Field label="Version">
          <Input value={f.version} onChange={(e) => set('version', e.target.value)} placeholder="v1.30.2" />
        </Field>
        <Field label="Provider">
          <Input value={f.provider} onChange={(e) => set('provider', e.target.value)} placeholder="AWS, On-prem…" />
        </Field>
        <Field label="Region label" hint="A cloud region code or a city name. If the cluster is not on a site yet, it is used to suggest where it is.">
          <Input value={f.region} onChange={(e) => set('region', e.target.value)} placeholder="eu-central-1" />
        </Field>
        <Field label="Site" hint="Where it sits on the map. Manage sites under Sites." className="col-span-2">
          <Select value={f.siteId ?? ''} onChange={(e) => set('siteId', e.target.value || undefined)}>
            <option value="">No site</option>
            {sites.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="CNI">
          <Input value={f.cni ?? ''} onChange={(e) => set('cni', e.target.value || undefined)} placeholder="calico, flannel, cilium…" />
        </Field>
        <Field label="Trust zone" hint="Policy input for placement.">
          <TrustSelect value={f.trustZone} onChange={(v) => set('trustZone', v)} />
        </Field>
        <Field label="Data residency" hint="Jurisdiction data must stay in, e.g. EU." className="col-span-2">
          <Input value={f.dataResidency ?? ''} onChange={(e) => set('dataResidency', e.target.value || undefined)} placeholder="EU" />
        </Field>
        <Field label="Labels" hint="Comma separated key=value pairs." className="col-span-2">
          <Input value={labels} onChange={(e) => setLabels(e.target.value)} placeholder="env=prod, team=ml" />
        </Field>
      </div>
    </Modal>
  )
}

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
  const [labels, setLabels] = useState(formatLabels(f.labels))
  const set = <K extends keyof MachineNode>(k: K, v: MachineNode[K]) => setF((p) => ({ ...p, [k]: v }))

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
            save({ ...f, name: f.name.trim(), labels: parseLabels(labels) }, by)
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
        <Field label="Machine type">
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
          <Input value={f.os} onChange={(e) => set('os', e.target.value)} />
        </Field>
        <Field label="CPU cores">
          <Input type="number" min={1} value={f.cpu} onChange={(e) => set('cpu', Number(e.target.value))} />
        </Field>
        <Field label="Memory (GB)">
          <Input type="number" min={1} value={f.memoryGb} onChange={(e) => set('memoryGb', Number(e.target.value))} />
        </Field>
        <Field label="Architecture" hint="Decides which images can run here.">
          <Select value={f.arch ?? ''} onChange={(e) => set('arch', e.target.value || undefined)}>
            <option value="">Unknown</option>
            {['amd64', 'arm64', 'arm', 'riscv64'].map((a) => (
              <option key={a}>{a}</option>
            ))}
          </Select>
        </Field>
        <Field label="Hardware model">
          <Input value={f.hardwareModel ?? ''} onChange={(e) => set('hardwareModel', e.target.value || undefined)} placeholder="Raspberry Pi 5, Jetson Orin…" />
        </Field>
        <Field label="Uplink" className="col-span-2">
          <ConnectivitySelect value={f.connectivity} onChange={(v) => set('connectivity', v)} />
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Labels" hint="key=value, comma separated.">
          <Input value={labels} onChange={(e) => setLabels(e.target.value)} placeholder="accelerator=jetson-orin" />
        </Field>
      </div>
    </Modal>
  )
}

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
  const [labels, setLabels] = useState(formatLabels(f.labels))
  // This form edits service → service calls; calls to devices or external endpoints are left untouched.
  const isEditable = (d: Dependency) => d.from === f.id && d.fromKind === 'service' && d.toKind === 'service'
  const [deps, setDeps] = useState<Dependency[]>(() => dependencies.filter(isEditable))
  const set = <K extends keyof Service>(k: K, v: Service[K]) => setF((p) => ({ ...p, [k]: v }))

  const clusterNodes = useMemo(() => nodes.filter((n) => n.clusterId === f.clusterId), [nodes, f.clusterId])
  const targets = services.filter((w) => w.id !== f.id)
  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '?'

  const save = () => {
    saveService({ ...f, name: f.name.trim(), labels: parseLabels(labels) }, by)
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

        <Field label="Labels" hint="key=value, comma separated." className="col-span-2">
          <Input value={labels} onChange={(e) => setLabels(e.target.value)} placeholder="tier=backend" />
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
  const [labels, setLabels] = useState(formatLabels(f.labels))
  const isEditable = (d: Dependency) => d.from === f.id && d.fromKind === 'device' && d.toKind === 'service'
  const [deps, setDeps] = useState<Dependency[]>(() => dependencies.filter(isEditable))
  const set = <K extends keyof Device>(k: K, v: Device[K]) => setF((p) => ({ ...p, [k]: v }))
  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '?'

  const save = () => {
    saveDevice({ ...f, name: f.name.trim(), count: Math.max(1, Math.round(f.count) || 1), labels: parseLabels(labels) }, by)
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
          <Input value={f.protocol} onChange={(e) => set('protocol', e.target.value)} placeholder="MQTT, OPC UA, Modbus, RTSP…" />
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
        <Field label="Labels" hint="key=value, comma separated." className="col-span-2">
          <Input value={labels} onChange={(e) => setLabels(e.target.value)} placeholder="line=3" />
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

/* ---------- Application ---------- */
export function ApplicationForm({ initial, onClose }: { initial: Application | null; onClose: () => void }) {
  const upsert = useTopology((s) => s.upsertApplication)
  const regroup = useTopology((s) => s.regroupApplication)
  const suggestions = useTopology((s) => s.suggestions)
  const [f, setF] = useState<Application>(
    initial ?? { id: uid('app'), orgId: DEFAULT_ORG, name: '', description: '', origin: 'explicit', confidence: 'high', source: 'manual' },
  )
  const alternatives = initial ? groupingAlternativesFor(initial, suggestions) : []
  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit application' : 'Add application'}
      description="A group of services and devices that together deliver something. Assign them from the Services and Devices pages."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim()}
          onCancel={onClose}
          onSave={() => {
            upsert({ ...f, name: f.name.trim() })
            onClose()
          }}
        />
      }
    >
      <div className="grid gap-4">
        <Field label="Name">
          <Input autoFocus value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. Sensor ingestion" />
          <GroupingPicker
            label="Group by a different label instead"
            alternatives={alternatives}
            onPick={(alt) => {
              regroup(initial!.id, alt)
              onClose()
            }}
          />
        </Field>
        <Field label="Description">
          <Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
        </Field>
      </div>
    </Modal>
  )
}

/* ---------- Site ---------- */
const COUNTRY_OPTIONS = (countries as string[]).map((code) => ({ code, name: countryName(code) })).sort((a, b) => a.name.localeCompare(b.name))

export function SiteForm({ initial, onClose }: { initial: Site | null; onClose: () => void }) {
  const upsert = useTopology((s) => s.upsertSite)
  const [f, setF] = useState<Site>(initial ?? { id: uid('site'), orgId: DEFAULT_ORG, name: '', kind: 'data-center', lat: 0, lng: 0, country: '' })
  const [lat, setLat] = useState(String(f.lat))
  const [lng, setLng] = useState(String(f.lng))
  const latN = Number(lat)
  const lngN = Number(lng)
  const valid = lat.trim() !== '' && lng.trim() !== '' && latN >= -90 && latN <= 90 && lngN >= -180 && lngN <= 180
  // The place tables load when the form opens: a city search fills everything in, and the coordinates are
  // checked against the country so a typo cannot quietly put a site in the wrong place.
  const places = usePlaceIndex()
  const [search, setSearch] = useState('')
  const hits = useMemo(() => (places ? findCities(places, search, 6) : []), [places, search])
  const here = useMemo(() => {
    if (!places || !valid) return undefined
    const near = nearestCity(places, latN, lngN, 60)
    const cc = countryAt(places, latN, lngN) ?? near?.city.cc
    return { near, cc, issue: siteLocationIssue(places, { lat: latN, lng: lngN, country: f.country }) }
  }, [places, valid, latN, lngN, f.country])
  const pick = (c: City) => {
    setF({ ...f, city: c.name, country: c.cc, name: f.name.trim() ? f.name : c.name })
    setLat(String(c.lat))
    setLng(String(c.lng))
    setSearch('')
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit site' : 'Add site'}
      description="A physical place. Clusters are attached to a site; the map view will draw sites as dots."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim() || !valid}
          onCancel={onClose}
          onSave={() => {
            upsert({ ...f, name: f.name.trim(), city: f.city?.trim() || undefined, lat: latN, lng: lngN, country: f.country.trim().toUpperCase() })
            onClose()
          }}
        />
      }
    >
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name" className="col-span-2">
          <Input autoFocus value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. Patras edge site" />
        </Field>
        <Field label="Find a city" hint="Fills in city, country and coordinates from a table of ~34,000 cities." className="col-span-2">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={places ? 'Type a city name…' : 'Loading the city table…'} disabled={!places} aria-label="Find a city" />
          {hits.length > 0 && (
            <ul className="mt-1 overflow-hidden rounded-md border border-nb-800 bg-nb-930" role="listbox" aria-label="Matching cities">
              {hits.map((c) => (
                <li key={`${c.name}-${c.cc}-${c.lat}-${c.lng}`}>
                  <button type="button" role="option" aria-selected={false} className="flex w-full items-center justify-between gap-3 px-3 py-1.5 text-left text-sm text-nb-300 hover:bg-nb-850 hover:text-white" onClick={() => pick(c)}>
                    <span>{c.name}, <span className="text-nb-500">{countryName(c.cc)}</span></span>
                    <span className="text-xs text-nb-600">{c.pop >= 1000 ? `${Math.round(c.pop / 1000).toLocaleString()}k people` : ''}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Field>
        <Field label="Kind">
          <Select value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value as Site['kind'] })}>
            {SITE_KINDS.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="City">
          <Input value={f.city ?? ''} onChange={(e) => setF({ ...f, city: e.target.value || undefined })} placeholder="e.g. Patras" />
        </Field>
        <Field label="Country">
          <Select value={f.country} onChange={(e) => setF({ ...f, country: e.target.value })}>
            <option value="">Not set</option>
            {f.country && !COUNTRY_OPTIONS.some((c) => c.code === f.country) && <option value={f.country}>{f.country}</option>}
            {COUNTRY_OPTIONS.map((c) => (
              <option key={c.code} value={c.code}>{c.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Latitude" hint="-90 to 90">
          <Input inputMode="decimal" value={lat} onChange={(e) => setLat(e.target.value)} />
        </Field>
        <Field label="Longitude" hint="-180 to 180">
          <Input inputMode="decimal" value={lng} onChange={(e) => setLng(e.target.value)} />
        </Field>
        <Field label="Data residency" hint="Jurisdiction, e.g. EU.">
          <Input value={f.dataResidency ?? ''} onChange={(e) => setF({ ...f, dataResidency: e.target.value || undefined })} />
        </Field>
        <Field label="Trust zone">
          <TrustSelect value={f.trustZone} onChange={(v) => setF({ ...f, trustZone: v })} />
        </Field>
        {!valid && <p className="col-span-2 text-xs text-red-300">Enter a latitude between -90 and 90 and a longitude between -180 and 180.</p>}
        {here?.issue && (
          <p className="col-span-2 flex flex-wrap items-center gap-2 rounded-md border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-xs text-amber-200" role="alert">
            {here.issue}
            {here.cc && <button type="button" className="rounded border border-amber-400/40 px-1.5 py-0.5 hover:bg-amber-400/10" onClick={() => setF({ ...f, country: here.cc! })}>Set country to {countryName(here.cc)}</button>}
          </p>
        )}
        {here && !here.issue && (!f.country || !f.city) && (here.cc || here.near) && (
          <p className="col-span-2 flex flex-wrap items-center gap-2 text-xs text-nb-400">
            These coordinates are {here.near ? `near ${here.near.city.name}, ` : 'in '}{countryName(here.near?.city.cc ?? here.cc)}.
            <button type="button" className="rounded border border-accent/40 px-1.5 py-0.5 text-accent hover:bg-accent/10" onClick={() => setF({ ...f, country: f.country || (here.cc ?? ''), city: f.city || here.near?.city.name })}>Fill in the blanks</button>
          </p>
        )}
      </div>
    </Modal>
  )
}

/* ---------- Confirm ---------- */
export function ConfirmModal({ title, message, onConfirm, onClose }: { title: string; message: string; onConfirm: () => void; onClose: () => void }) {
  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      width="max-w-md"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            onClick={() => {
              onConfirm()
              onClose()
            }}
          >
            Delete
          </Button>
        </>
      }
    >
      <p className="text-sm text-nb-400">{message}</p>
    </Modal>
  )
}

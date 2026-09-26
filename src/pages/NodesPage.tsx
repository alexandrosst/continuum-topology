import { AlertTriangle, Cpu, HardDrive, Plug, Plus, Server } from 'lucide-react'
import { useState } from 'react'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { ConfirmModal, NodeForm } from '@/components/forms'
import { WithIcon } from '@/components/ui/brand'
import { GoneRecords } from '@/components/Observations'
import { Button, DeclaredMark, EmptyState, EvidenceChip, IpAddress, Meter, ObservationChip, PageHeader, Select, SourceBadge, StatusDot, Table, Td, Th } from '@/components/ui/primitives'
import { observation } from '@/lib/provenance'
import { useWeakValue } from '@/store/rowEvidence'
import { useTopology } from '@/store/topology'
import { hasOverrides } from '@/lib/effective'
import { accelSummary, ageLabel, formatCpu, formatMemory, podsLabel, podsPercent, requestedPercent } from '@/lib/present'
import type { MachineNode } from '@/lib/types'
import { matches, RowActions, SearchBox } from './shared'

const KIND_LABEL = { vm: 'VM', 'bare-metal': 'Bare metal', 'edge-device': 'Edge device' } as const
const KIND_ICON = { vm: Server, 'bare-metal': HardDrive, 'edge-device': Cpu } as const

export default function NodesPage() {
  const { clusters, nodes, services, deleteNode } = useTopology()
  const weak = useWeakValue('node')
  const connect = useConnectFlow()
  const [q, setQ] = useState('')
  const [clusterFilter, setClusterFilter] = useState('')
  const [editing, setEditing] = useState<MachineNode | null | 'new'>(null)
  const [deleting, setDeleting] = useState<MachineNode | null>(null)

  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '—'
  const rows = nodes.filter((n) => (!clusterFilter || n.clusterId === clusterFilter) && matches(q, n.name, n.ip, n.os, clusterName(n.clusterId)))

  return (
    <>
      <PageHeader
        title="Nodes"
        description="The machines — VMs, bare-metal servers and edge devices — that make up your clusters."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')} disabled={clusters.length === 0}>
            <Plus size={16} /> Add node
          </Button>
        }
      />
      <div className="mb-4 flex gap-3">
        <SearchBox value={q} onChange={setQ} placeholder="Search nodes by name, IP, OS…" />
        <Select className="w-56" value={clusterFilter} onChange={(e) => setClusterFilter(e.target.value)}>
          <option value="">All clusters</option>
          {clusters.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </Select>
      </div>

      {nodes.length === 0 ? (
        <EmptyState
          title="No node is known yet"
          description={clusters.length ? 'No machine is on record for these clusters. An agent reads nodes at the Infrastructure access level or above; or describe them by hand.' : 'No cluster is connected or declared yet, so there are no machines to list. Connect a cluster and its agent reports its nodes, or add a cluster by hand first.'}
          action={!clusters.length && connect.canStart ? <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button> : undefined}
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Cluster</Th>
              <Th>Type · arch</Th>
              <Th>IP</Th>
              <Th>CPU</Th>
              <Th>Memory</Th>
              <Th>Services</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((n) => {
              const obs = observation(n)
              const chip = (attr: string, field?: string) => {
                const w = weak(n as never, attr, field)
                return w ? <EvidenceChip level={w.level} why={w.why} /> : null
              }
              return (
              <tr key={n.id} className="group hover:bg-nb-930/60">
                <Td>
                  <div className="flex items-center gap-2 whitespace-nowrap font-medium text-nb-300">
                    <span className="shrink-0">
                      <StatusDot status={n.status} notCurrent={obs && obs.kind !== 'live' ? (obs.reason ?? obs.label) : undefined} />
                    </span>
                    <span>{n.name}</span>
                    <SourceBadge source={n.source} overridden={hasOverrides(n)} />
                    <ObservationChip quiet info={obs} />
                    {n.source === 'manual' && <DeclaredMark />}
                    {n.status !== 'healthy' && <span className="text-xs font-normal capitalize text-nb-400">{obs && obs.kind !== 'live' ? `was ${n.status}` : n.status}</span>}
                    {n.conditions && n.conditions.length > 0 && (
                      <span className="inline-flex text-warn" title={`Active conditions: ${n.conditions.join(', ')}`}>
                        <AlertTriangle size={14} aria-label={n.conditions.join(', ')} />
                      </span>
                    )}
                  </div>
                  {(n.hardwareModel || n.os || n.accelerators?.length || n.createdAt) && (
                    <div className="flex items-center gap-1.5 whitespace-nowrap text-xs text-nb-500">
                      <span>{n.hardwareModel || n.os}</span>
                      {n.createdAt && <span title={`Joined the cluster ${new Date(n.createdAt).toLocaleDateString()}`}>· {ageLabel(n.createdAt)}</span>}
                      {n.accelerators && n.accelerators.length > 0 && (
                        <span title={accelSummary(n.accelerators)} className="rounded border border-violet-400/30 bg-violet-400/10 px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-violet-300">GPU</span>
                      )}
                    </div>
                  )}
                </Td>
                <Td className="whitespace-nowrap">
                  <div>{clusterName(n.clusterId)}</div>
                  <div className={n.role === 'control-plane' ? 'text-xs text-accent' : 'text-xs text-nb-500'}>{n.role === 'control-plane' ? 'Control plane' : 'Worker'}</div>
                </Td>
                <Td>
                  {(() => { const K = KIND_ICON[n.kind]; return <WithIcon icon={<K size={15} className="text-nb-500" />}>{KIND_LABEL[n.kind]}</WithIcon> })()}
                  <div className="flex items-center gap-1.5 pl-[23px] text-xs text-nb-500">
                    {n.arch ? <span>{n.arch}</span> : chip('arch')}
                    {/* Bare metal/edge rows now carry an explicit "No hypervisor detected" too (see hostprobe.go) - worth a full line in the Inspector, but repeating it on every row here would just be noise next to a kind icon that already says "Bare metal". Only the interesting case (it IS a VM) earns space in this dense list. */}
                    {n.kind === 'vm' && n.virtualization && <span>· {n.virtualization}</span>}
                    {chip('kind')}
                  </div>
                </Td>
                <Td><IpAddress ip={n.ip} /></Td>
                {(() => {
                  const pct = requestedPercent(n.requested, n.allocatable)
                  return (
                    <>
                      <Td><Meter value={formatCpu(n.cpu)} unit="vCPU" pct={pct.cpu} title={pct.cpu === undefined ? undefined : `${pct.cpu}% of allocatable CPU is requested by pods`} /></Td>
                      <Td>
                        <Meter value={formatMemory(n.memoryGb).split(' ')[0]} unit={formatMemory(n.memoryGb).split(' ')[1]} pct={pct.memory} title={pct.memory === undefined ? undefined : `${pct.memory}% of allocatable memory is requested by pods`} />
                      </Td>
                    </>
                  )
                })()}
                <Td className="whitespace-nowrap">
                  <div>{(() => { const c = services.filter((w) => w.nodeIds.includes(n.id)).length; return <>{c} <span className="text-nb-500">{c === 1 ? 'service' : 'services'}</span></> })()}</div>
                  {n.podCount !== undefined && (
                    <div
                      className={(podsPercent(n.podCount, n.podCapacity) ?? 0) >= 90 ? 'text-xs text-bad' : 'text-xs text-nb-500'}
                      title={n.podCapacity ? `${n.podCount} pods placed of the ${n.podCapacity} the kubelet will run` : `${n.podCount} pods placed`}
                    >
                      {podsLabel(n.podCount, n.podCapacity)} pods
                    </div>
                  )}
                </Td>
                <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(n)} onDelete={() => setDeleting(n)} /></Td>
              </tr>
              )
            })}
            {rows.length === 0 && (
              <tr><Td colSpan={8} className="py-8 text-center text-nb-500">No nodes match your filters.</Td></tr>
            )}
          </tbody>
        </Table>
      )}
      <GoneRecords kinds={['node']} className="mt-8" />

      {editing && <NodeForm initial={editing === 'new' ? null : editing} defaultClusterId={clusterFilter || undefined} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Services scheduled on this node will lose that placement."
          onConfirm={() => deleteNode(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

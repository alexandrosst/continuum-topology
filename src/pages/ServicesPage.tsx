import { Plug, Plus, X } from 'lucide-react'
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { ConfirmModal, ServiceForm } from '@/components/forms'
import { MobilityChip, useMoveModel } from '@/components/MobilityPanel'
import { GoneRecords } from '@/components/Observations'
import { Button, ChipList, DeclaredMark, EmptyState, EvidenceChip, ObservationChip, PageHeader, Pill, Select, SourceBadge, StatusDot, Table, Td, Th } from '@/components/ui/primitives'
import { observation } from '@/lib/provenance'
import { useWeakValue } from '@/store/rowEvidence'
import { useTopology } from '@/store/topology'
import { hasOverrides } from '@/lib/effective'
import type { Service } from '@/lib/types'
import { matches, RowActions, SearchBox, ServiceTraits } from './shared'

export default function ServicesPage() {
  const { clusters, nodes, services, dependencies, applications, deleteService } = useTopology()
  const { forService } = useMoveModel()
  const weak = useWeakValue('service')
  const connect = useConnectFlow()
  const [q, setQ] = useState('')
  // The cluster and namespace filters live in the address (?cluster=…&namespace=…), so the Namespaces page can link here and a filtered view can be shared.
  const [sp, setSp] = useSearchParams()
  const wantCluster = sp.get('cluster') ?? ''
  const clusterFilter = clusters.some((c) => c.id === wantCluster) ? wantCluster : ''
  const nsFilter = sp.get('namespace') ?? ''
  const setParam = (k: string, v: string) => setSp((p) => { const n = new URLSearchParams(p); if (v) n.set(k, v); else n.delete(k); return n }, { replace: true })
  const [editing, setEditing] = useState<Service | null | 'new'>(null)
  const [deleting, setDeleting] = useState<Service | null>(null)

  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '—'
  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name ?? id
  const rows = services.filter(
    (w) => (!clusterFilter || w.clusterId === clusterFilter) && (!nsFilter || w.namespace === nsFilter) && matches(q, w.name, w.namespace, w.image, clusterName(w.clusterId), applications.find((a) => a.id === w.applicationId)?.name),
  )

  return (
    <>
      <PageHeader
        title="Services"
        description="Microservices and applications, where they run, and which other services they call."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')} disabled={clusters.length === 0}>
            <Plus size={16} /> Add service
          </Button>
        }
      />
      <div className="mb-4 flex gap-3">
        <SearchBox value={q} onChange={setQ} placeholder="Search services by name, namespace, image…" />
        <Select className="w-56" value={clusterFilter} onChange={(e) => setParam('cluster', e.target.value)} aria-label="Cluster">
          <option value="">All clusters</option>
          {clusters.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </Select>
        {nsFilter && (
          <button onClick={() => setParam('namespace', '')} className="inline-flex h-9 items-center gap-2 rounded-md border border-accent/40 bg-accent-soft px-3 text-sm text-accent hover:bg-accent/20" data-testid="namespace-filter" aria-label={`Namespace ${nsFilter}: clear this filter`}>
            Namespace {nsFilter} <X size={14} aria-hidden />
          </button>
        )}
      </div>

      {services.length === 0 ? (
        <EmptyState
          title="No service is known yet"
          action={!clusters.length && connect.canStart ? <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button> : undefined}
          description={clusters.length ? 'No service is on record for these clusters. Connect an agent that reads at the Services access level, or add one by hand.' : 'No cluster is connected or declared yet. Connect a cluster and its agent reports its services, or add a cluster by hand first.'} />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Cluster</Th>
              <Th>Application</Th>
              <Th>Kind</Th>
              <Th>Runs on</Th>
              <Th>Calls</Th>
              <Th>Mobility</Th>
              <Th>Status</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((w) => (
              <tr key={w.id} className="group hover:bg-nb-930/60">
                <Td>
                  <div className="font-medium text-white">{w.name}<SourceBadge source={w.source} overridden={hasOverrides(w)} /> <ObservationChip quiet info={observation(w)} />{w.source === 'manual' && <> <DeclaredMark /></>}</div>
                  <div className="font-mono text-xs text-nb-500">{w.image || '—'} · ×{w.replicas}</div>
                  <ServiceTraits w={w} nodeName={nodeName} />
                </Td>
                <Td className="whitespace-nowrap">
                  {clusterName(w.clusterId)}
                  <div className="text-xs text-nb-500" title="Namespace">{w.namespace}</div>
                </Td>
                <Td className="whitespace-nowrap text-nb-400">
                  {applications.find((a) => a.id === w.applicationId)?.name ?? '—'}
                  {(() => { const g = w.applicationId ? weak(w as never, 'applicationId') : undefined; return g ? <> <EvidenceChip level={g.level} why={g.why} /></> : null })()}
                </Td>
                <Td><Pill>{w.kind}</Pill></Td>
                <Td><ChipList items={w.nodeIds.map(nodeName)} /></Td>
                <Td>{dependencies.filter((d) => d.from === w.id).length}</Td>
                <Td><MobilityChip service={w} forService={forService} /></Td>
                <Td>{(() => { const o = observation(w); return <StatusDot status={w.status} withLabel notCurrent={o && o.kind !== 'live' ? (o.reason ?? o.label) : undefined} /> })()}</Td>
                <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(w)} onDelete={() => setDeleting(w)} /></Td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><Td className="py-8 text-center text-nb-500">No services match your filters.</Td></tr>
            )}
          </tbody>
        </Table>
      )}
      <GoneRecords kinds={['service', 'namespace']} className="mt-8" />

      {connect.dialogs}
      {editing && <ServiceForm initial={editing === 'new' ? null : editing} defaultClusterId={clusterFilter || undefined} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Dependencies to and from this service are removed as well."
          onConfirm={() => deleteService(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

import { Plug, Plus } from 'lucide-react'
import { useState } from 'react'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { ClusterForm, ConfirmModal } from '@/components/forms'
import PlacementHint from '@/components/PlacementHint'
import { DistroIcon, Place, ProviderIcon, WithIcon } from '@/components/ui/brand'
import { Button, CompletenessBadge, DeclaredMark, EmptyState, EvidenceChip, ObservationChip, PageHeader, SourceBadge, StatusDot, Table, Td, Th, TierBadge } from '@/components/ui/primitives'
import { observation } from '@/lib/provenance'
import { ageLabel, shortVersion } from '@/lib/present'
import { completeness } from '@/lib/completeness'
import { hasOverrides } from '@/lib/effective'
import { useAutoPlaceClusters, usePlacementSuggestions } from '@/lib/usePlacement'
import { useWeakValue } from '@/store/rowEvidence'
import { useTopology } from '@/store/topology'
import type { Cluster } from '@/lib/types'
import { matches, RowActions, SearchBox } from './shared'

export default function ClustersPage() {
  const { clusters, nodes, services, dependencies, sites, deleteCluster } = useTopology()
  // This page shows and accepts placement hints, so it's a fair place to also resolve them silently.
  useAutoPlaceClusters()
  const placement = usePlacementSuggestions().byCluster
  const weak = useWeakValue('cluster')
  const connect = useConnectFlow()
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<Cluster | null | 'new'>(null)
  const [deleting, setDeleting] = useState<Cluster | null>(null)

  const rows = clusters.filter((c) => matches(q, c.name, c.distribution, c.provider, c.region, c.tier, sites.find((x) => x.id === c.siteId)?.name))

  return (
    <>
      <PageHeader
        title="Clusters"
        description="Kubernetes clusters across the cloud–edge continuum. Nodes and services belong to a cluster."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Plus size={16} /> Add cluster
          </Button>
        }
      />
      <div className="mb-4">
        <SearchBox value={q} onChange={setQ} placeholder="Search clusters…" />
      </div>

      {clusters.length === 0 ? (
        <EmptyState
          title="No cluster is connected or declared yet"
          description="Connect a cluster and its agent reports the cluster, its nodes and its services, or describe one by hand if nothing can run inside it."
          action={
            <div className="flex flex-wrap justify-center gap-2">
              {connect.canStart && <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button>}
              <Button onClick={() => setEditing('new')}><Plus size={16} /> Add manually</Button>
            </div>
          }
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Tier</Th>
              <Th>Distribution / provider</Th>
              <Th>Location</Th>
              <Th>Nodes</Th>
              <Th>Services</Th>
              <Th>Discovered</Th>
              <Th>Status</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => {
              const obs = observation(c)
              const chip = (attr: string, field?: string) => {
                const w = weak(c as never, attr, field)
                return w ? <EvidenceChip level={w.level} why={w.why} /> : null
              }
              return (
              <tr key={c.id} className="group hover:bg-nb-930/60">
                <Td className="whitespace-nowrap font-medium text-nb-300">
                  <div>{c.name}</div>
                  {c.createdAt && <div className="text-xs font-normal text-nb-500" title={`Created ${new Date(c.createdAt).toLocaleDateString()}`}>{ageLabel(c.createdAt)} old</div>}
                  <SourceBadge stacked source={c.source} overridden={hasOverrides(c)} />
                  <div className="mt-1 flex items-center gap-2">
                    <ObservationChip quiet info={obs} />
                    {c.source === 'manual' && <DeclaredMark />}
                  </div>
                </Td>
                <Td>
                  <TierBadge tier={c.tier} />
                  {chip('tier') && <div className="mt-1">{chip('tier')}</div>}
                </Td>
                <Td>
                  <WithIcon icon={<DistroIcon distribution={c.distribution} />}>
                    <span title={c.version}>{c.distribution || '—'} <span className="text-nb-500">{shortVersion(c.version)}</span></span>
                  </WithIcon>
                  {chip('distribution') && <span className="ml-1.5">{chip('distribution')}</span>}
                  {c.provider && (
                    <div className="mt-0.5 flex items-center gap-1.5 text-xs text-nb-500">
                      <WithIcon icon={<ProviderIcon provider={c.provider} />}>{c.provider}</WithIcon>
                      {chip('provider')}
                    </div>
                  )}
                </Td>
                <Td>
                  {(() => {
                    const site = sites.find((x) => x.id === c.siteId)
                    return (
                      <div>
                        <Place site={site} fallback={c.region} />
                        {site && <div className="whitespace-nowrap pl-[26px] text-xs text-nb-500">{site.name}</div>}
                        {!site && chip('region') && <div className="mt-0.5">{chip('region')}</div>}
                        {!site && <PlacementHint compact suggestion={placement.get(c.id)} egressIp={c.egressIp} />}
                      </div>
                    )
                  })()}
                </Td>
                <Td>{nodes.filter((n) => n.clusterId === c.id).length}</Td>
                <Td>{services.filter((w) => w.clusterId === c.id).length}</Td>
                <Td><CompletenessBadge compact c={completeness(c, nodes, services, dependencies)} /></Td>
                <Td><StatusDot status={c.status} withLabel notCurrent={obs && obs.kind !== 'live' ? (obs.reason ?? obs.label) : undefined} /></Td>
                <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(c)} onDelete={() => setDeleting(c)} /></Td>
              </tr>
              )
            })}
            {rows.length === 0 && (
              <tr>
                <Td colSpan={9} className="py-8 text-center text-nb-500" >No clusters match “{q}”.</Td>
              </tr>
            )}
          </tbody>
        </Table>
      )}

      {editing && <ClusterForm initial={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="This also removes the cluster's nodes and services, and any dependencies that involve them."
          onConfirm={() => deleteCluster(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

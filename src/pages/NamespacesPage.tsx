import { ArrowRight, Plug } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { GoneRecords } from '@/components/Observations'
import { Button, EmptyState, ObservationChip, PageHeader, Select, Table, Td, Th } from '@/components/ui/primitives'
import { buildNamespaceRows, excludedWords, rowMatches, scopeLabel, type NamespaceRow } from '@/lib/namespaces'
import { TONE_CLASS } from '@/lib/provenance'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'
import { SearchBox } from './shared'

/** Where a row leads: the services of that namespace. (The topology has no per-namespace focus, the Services page does.) */
const servicesOf = (r: Pick<NamespaceRow, 'clusterId' | 'name'>) => `/services?cluster=${encodeURIComponent(r.clusterId)}&namespace=${encodeURIComponent(r.name)}`

export default function NamespacesPage() {
  const { clusters, namespaces, services, agents } = useTopology()
  const connect = useConnectFlow()
  const connected = useServer((s) => s.status === 'connected')
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [clusterFilter, setClusterFilter] = useState('')

  const all = useMemo(() => buildNamespaceRows({ clusters, namespaces, services, agents }), [clusters, namespaces, services, agents])
  const rows = all.filter((r) => (!clusterFilter || r.clusterId === clusterFilter) && rowMatches(r, q))
  const count = all.filter((r) => r.kind === 'namespace').length
  const left = all.reduce((n, r) => n + (r.kind === 'excluded' ? r.count : 0), 0)
  const live = clusters.filter((c) => !c.deletedAt)

  return (
    <>
      <PageHeader
        title="Namespaces"
        description="The namespaces of every cluster, what runs in them, how far each can be trusted right now, and whether an agent was told to look at it."
      />
      <div className="mb-4 flex flex-wrap gap-3">
        <SearchBox value={q} onChange={setQ} placeholder="Search namespaces or clusters…" />
        <Select className="w-56" value={clusterFilter} onChange={(e) => setClusterFilter(e.target.value)} aria-label="Cluster">
          <option value="">All clusters</option>
          {live.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </Select>
      </div>

      {count === 0 && left === 0 ? (
        <EmptyState
          title={live.length === 0 ? 'No cluster is connected yet' : 'No namespaces are known yet'}
          description={
            live.length === 0
              ? 'Namespaces are read from a cluster by its agent, so there is nothing to list until one is connected.'
              : 'The clusters here have no namespaces or workloads on record. An agent below the Services access level does not read them.'
          }
          action={live.length === 0 && (!connected || connect.canStart) ? <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button> : undefined}
        />
      ) : (
        <>
          <Table cols={['w-52', 'w-44', 'w-44', 'w-24', 'w-52', 'w-32', 'w-32']}>
            <thead>
              <tr>
                <Th>Namespace</Th>
                <Th>Cluster</Th>
                <Th>Workloads</Th>
                <Th>Exposed</Th>
                <Th>Service mesh</Th>
                <Th>Agent scope</Th>
                <Th>State</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) =>
                r.kind === 'excluded' ? (
                  <tr key={r.key} className="bg-nb-930/40" data-testid="namespace-excluded" data-cluster={r.clusterName}>
                    <Td className="text-nb-400">
                      <span className="font-medium">{r.count} more</span>
                    </Td>
                    <Td className="truncate text-nb-400" title={r.clusterName}>{r.clusterName}</Td>
                    <Td colSpan={5} className="text-xs text-nb-500" title={r.description ? `How the scope was set: ${r.description}` : undefined}>
                      {excludedWords(r.count)}
                    </Td>
                  </tr>
                ) : (
                  <tr key={r.key} className="group cursor-pointer hover:bg-nb-930/60" onClick={() => navigate(servicesOf(r))} data-testid="namespace-row" data-namespace={r.name} data-cluster={r.clusterName}>
                    <Td className="font-medium text-white">
                      <Link to={servicesOf(r)} onClick={(e) => e.stopPropagation()} className="rounded hover:underline" title="Show this namespace’s services">{r.name}</Link>
                    </Td>
                    <Td className="truncate" title={r.clusterName}>{r.clusterName}</Td>
                    <Td className="whitespace-nowrap">
                      {r.workloads}
                      {r.kinds && <div className="whitespace-normal text-xs text-nb-500">{r.kinds}</div>}
                    </Td>
                    <Td>{r.exposed > 0 ? r.exposed : <span className="text-nb-600">none</span>}</Td>
                    <Td>
                      {r.mesh ? (
                        <span title={r.mesh.detail} className={`inline-flex max-w-full items-center truncate whitespace-nowrap rounded border px-1.5 py-px text-[11px] font-medium leading-4 ${TONE_CLASS[r.mesh.tone]}`} data-testid="namespace-mesh">
                          {r.mesh.label}
                        </span>
                      ) : (
                        <span className="text-nb-600">none</span>
                      )}
                      {r.mesh && r.mesh.total > 0 && <div className="mt-0.5 text-xs text-nb-500">{r.mesh.covered} of {r.mesh.total} in mesh</div>}
                    </Td>
                    <Td className="whitespace-nowrap" data-testid="namespace-scope">
                      <span className={r.scope === 'in' ? 'text-nb-300' : 'text-nb-500'}>{scopeLabel(r)}</span>
                    </Td>
                    <Td>
                      {r.observation ? <ObservationChip info={r.observation} /> : <span className="text-xs text-nb-500" title="Typed by a person; no agent observes it, so it has nothing to be stale about.">declared</span>}
                    </Td>
                  </tr>
                ),
              )}
              {rows.length === 0 && (
                <tr><Td colSpan={7} className="py-8 text-center text-nb-500">No namespaces match your filters.</Td></tr>
              )}
            </tbody>
          </Table>
          {left > 0 && (
            <p className="mt-3 max-w-3xl text-xs text-nb-500" data-testid="namespaces-scope-note">
              Some agents were told to read only part of their cluster. The rest is dropped inside the cluster, so this server never receives the names, only how many were left out. The cluster’s owner widens it with <code className="font-mono text-nb-400">helm upgrade</code>; an administrator can narrow it further under Agents.
              {' '}<Link to="/agents" className="inline-flex items-center gap-1 text-accent hover:underline">Agents <ArrowRight size={12} aria-hidden /></Link>
            </p>
          )}
        </>
      )}

      <GoneRecords kinds={['namespace']} className="mt-8" />
      {connect.dialogs}
    </>
  )
}

import { Plus } from 'lucide-react'
import { useState } from 'react'
import { ApplicationForm, ConfirmModal } from '@/components/forms'
import { Button, EmptyState, PageHeader, Pill, SourceBadge, Table, Td, Th } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { originLabel } from '@/lib/present'
import type { Application } from '@/lib/types'
import { useTopology } from '@/store/topology'
import { matches, RowActions, SearchBox } from './shared'

export default function ApplicationsPage() {
  const { applications, services, devices, clusters, deleteApplication } = useTopology()
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<Application | null | 'new'>(null)
  const [deleting, setDeleting] = useState<Application | null>(null)

  const rows = applications.filter((a) => matches(q, a.name, a.description))

  return (
    <>
      <PageHeader
        title="Applications"
        description="Services and devices grouped into the things your users care about. Groupings are declared by you or accepted from a suggestion; no agent observes them."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Plus size={16} /> Add application
          </Button>
        }
      />
      <div className="mb-4">
        <SearchBox value={q} onChange={setQ} placeholder="Search applications…" />
      </div>

      {applications.length === 0 ? (
        <EmptyState
          title="No applications yet"
          description="No grouping exists yet. Discovery suggests one once agents report services, or create an application and assign services to it from the Services page."
          action={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={16} /> Add application</Button>}
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Services</Th>
              <Th>Devices</Th>
              <Th>Clusters</Th>
              <Th>Grouped by</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => {
              const mine = services.filter((s) => s.applicationId === a.id)
              const cIds = [...new Set(mine.map((s) => s.clusterId))]
              return (
                <tr key={a.id} className="group hover:bg-nb-930/60">
                  <Td>
                    <div className="font-medium text-white">{a.name}<SourceBadge source={a.source} overridden={hasOverrides(a)} /></div>
                    {a.description && <div className="text-xs text-nb-500">{a.description}</div>}
                  </Td>
                  <Td>{mine.length}</Td>
                  <Td>{devices.filter((d) => d.applicationId === a.id).reduce((s, d) => s + d.count, 0) || '—'}</Td>
                  <Td className="text-nb-400">{cIds.map((id) => clusters.find((c) => c.id === id)?.name).filter(Boolean).join(', ') || '—'}</Td>
                  <Td><Pill title={a.origin}>{originLabel(a.origin)}</Pill></Td>
                  <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(a)} onDelete={() => setDeleting(a)} /></Td>
                </tr>
              )
            })}
            {rows.length === 0 && (
              <tr><Td colSpan={6} className="py-8 text-center text-nb-500">No applications match “{q}”.</Td></tr>
            )}
          </tbody>
        </Table>
      )}

      {editing && <ApplicationForm initial={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Its services are kept; they just stop belonging to an application."
          onConfirm={() => deleteApplication(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

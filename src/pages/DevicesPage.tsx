import { Plus } from 'lucide-react'
import { useState } from 'react'
import ColumnPicker from '@/components/ColumnPicker'
import { ConfirmModal, DeviceForm } from '@/components/forms'
import { DEVICE_ICON } from '@/components/topology/nodes'
import { Button, EmptyState, ICON_SM, PageHeader, Pill, Select, SourceBadge, StatusDot, Table, Td, Th } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { useColumnVisibility, type ColumnDef } from '@/lib/columns'
import { CONNECTIVITY, DEVICE_KINDS, type Device } from '@/lib/types'
import { useTopology } from '@/store/topology'
import { matches, RowActions, SearchBox } from './shared'

const DEVICE_COLUMNS: ColumnDef[] = [
  { key: 'kind', label: 'Kind' },
  { key: 'units', label: 'Units' },
  { key: 'application', label: 'Application' },
  { key: 'site', label: 'Site' },
  { key: 'protocol', label: 'Protocol · link' },
  { key: 'sendsTo', label: 'Sends to' },
]

export default function DevicesPage() {
  const { devices, applications, sites, nodes, dependencies, deleteDevice } = useTopology()
  const [q, setQ] = useState('')
  const [siteFilter, setSiteFilter] = useState('')
  const { isVisible, toggle } = useColumnVisibility('devices')
  const [editing, setEditing] = useState<Device | null | 'new'>(null)
  const [deleting, setDeleting] = useState<Device | null>(null)

  const appName = (id?: string) => applications.find((a) => a.id === id)?.name
  const siteName = (id?: string) => sites.find((s) => s.id === id)?.name
  const kindLabel = (k: string) => DEVICE_KINDS.find((x) => x.value === k)?.label ?? k
  const rows = devices.filter(
    (d) => (!siteFilter || (siteFilter === 'none' ? !d.siteId : d.siteId === siteFilter)) && matches(q, d.name, d.protocol, kindLabel(d.kind), appName(d.applicationId), siteName(d.siteId)),
  )
  const units = devices.reduce((s, d) => s + d.count, 0)

  return (
    <>
      <PageHeader
        title="Devices"
        description={`Sensors, cameras, PLCs and other things that belong to an application but do not run on Kubernetes. Nothing observes them, so each shows what was declared. ${devices.length ? `${devices.length} groups, ${units} units.` : ''}`}
        actions={
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Plus size={ICON_SM} /> Add device
          </Button>
        }
      />
      <div className="mb-4 flex gap-3">
        <SearchBox value={q} onChange={setQ} placeholder="Search devices by name, protocol, site…" />
        <Select className="w-56" value={siteFilter} onChange={(e) => setSiteFilter(e.target.value)}>
          <option value="">All sites</option>
          <option value="none">No site</option>
          {sites.map((s) => (
            <option key={s.id} value={s.id}>{s.name}</option>
          ))}
        </Select>
        <ColumnPicker columns={DEVICE_COLUMNS} isVisible={isVisible} onToggle={toggle} />
      </div>

      {devices.length === 0 ? (
        <EmptyState
          title="No devices yet"
          description="Add a device group, for example the temperature sensors at a site. Devices show up in the Application view next to the services they talk to."
          action={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={ICON_SM} /> Add device</Button>}
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              {isVisible('kind') && <Th>Kind</Th>}
              {isVisible('units') && <Th>Units</Th>}
              {isVisible('application') && <Th>Application</Th>}
              {isVisible('site') && <Th>Site</Th>}
              {isVisible('protocol') && <Th>Protocol · link</Th>}
              {isVisible('sendsTo') && <Th>Sends to</Th>}
              <Th>Status</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((d) => {
              const Icon = DEVICE_ICON[d.kind]
              const out = dependencies.filter((x) => x.from === d.id).length
              const gw = nodes.find((n) => n.id === d.gatewayNodeId)
              return (
                <tr key={d.id} className="group hover:bg-nb-930/60">
                  <Td className="font-medium text-nb-300">
                    <span className="inline-flex items-center gap-2"><Icon size={ICON_SM} className="text-nb-500" />{d.name}</span>
                    <SourceBadge source={d.source} overridden={hasOverrides(d)} />
                    {gw && <div className="whitespace-nowrap pl-6 text-xs font-normal text-nb-500">attached to {gw.name}</div>}
                  </Td>
                  {isVisible('kind') && <Td><Pill>{kindLabel(d.kind)}</Pill></Td>}
                  {isVisible('units') && <Td>{d.count}</Td>}
                  {isVisible('application') && <Td>{appName(d.applicationId) ?? <span className="text-nb-600">—</span>}</Td>}
                  {isVisible('site') && <Td className="whitespace-nowrap text-nb-400">{siteName(d.siteId) ?? '—'}</Td>}
                  {isVisible('protocol') && <Td className="whitespace-nowrap text-nb-400">{[d.protocol, CONNECTIVITY.find((c) => c.value === d.connectivity)?.label].filter(Boolean).join(' · ')}</Td>}
                  {isVisible('sendsTo') && <Td>{out}</Td>}
                  <Td><StatusDot status={d.status} withLabel /></Td>
                  <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(d)} onDelete={() => setDeleting(d)} /></Td>
                </tr>
              )
            })}
            {rows.length === 0 && (
              <tr><Td colSpan={3 + DEVICE_COLUMNS.filter((c) => isVisible(c.key)).length} className="py-8 text-center text-nb-500">No devices match your filters.</Td></tr>
            )}
          </tbody>
        </Table>
      )}

      {editing && <DeviceForm initial={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Connections from and to this device are removed too."
          onConfirm={() => deleteDevice(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

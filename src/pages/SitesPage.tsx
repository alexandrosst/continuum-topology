import { AlertTriangle, Plus } from 'lucide-react'
import { useMemo, useState } from 'react'
import { ConfirmModal, SiteForm } from '@/components/forms'
import MeasuredPaths from '@/components/MeasuredPaths'
import { Place } from '@/components/ui/brand'
import { Button, EmptyState, PageHeader, Pill, Table, Td, Th } from '@/components/ui/primitives'
import { siteLocationIssue } from '@/lib/places'
import { usePlaceIndex } from '@/lib/places-data'
import { countryName } from '@/lib/present'
import { SITE_KINDS, type Site } from '@/lib/types'
import { useTopology } from '@/store/topology'
import { matches, RowActions, SearchBox } from './shared'

export default function SitesPage() {
  const { sites, clusters, deleteSite } = useTopology()
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<Site | null | 'new'>(null)
  const [deleting, setDeleting] = useState<Site | null>(null)

  // Coordinates and country are checked against a table of country outlines; a mismatch is flagged, not fixed.
  const places = usePlaceIndex(sites.length > 0)
  const issues = useMemo(() => new Map(sites.flatMap((s) => { const i = places && siteLocationIssue(places, s); return i ? [[s.id, i] as const] : [] })), [places, sites])
  const rows = sites.filter((s) => matches(q, s.name, s.city, s.country, countryName(s.country)))
  const kindLabel = (k: Site['kind']) => SITE_KINDS.find((x) => x.value === k)?.label ?? k

  return (
    <>
      <PageHeader
        title="Sites"
        description="Physical places where clusters live, declared by you or accepted from a suggestion. Coordinates place them on the map; the country is checked against them."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Plus size={16} /> Add site
          </Button>
        }
      />
      <div className="mb-4">
        <SearchBox value={q} onChange={setQ} placeholder="Search sites…" />
      </div>

      {sites.length === 0 ? (
        <EmptyState
          title="No sites yet"
          description="Add a site, then attach clusters to it from the cluster form."
          action={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={16} /> Add site</Button>}
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Kind</Th>
              <Th>Location</Th>
              <Th>Clusters</Th>
              <Th className="sticky right-0 bg-nb-925" />
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.id} className="group hover:bg-nb-930/60">
                <Td className="font-medium text-white">{s.name}</Td>
                <Td><Pill>{kindLabel(s.kind)}</Pill></Td>
                <Td>
                  <Place site={s} />
                  <div className="whitespace-nowrap pl-[26px] font-mono text-xs text-nb-500">{s.lat.toFixed(2)}, {s.lng.toFixed(2)}</div>
                  {issues.has(s.id) && (
                    <div className="mt-0.5 flex items-center gap-1 pl-[26px] text-xs text-amber-300" title={issues.get(s.id)}>
                      <AlertTriangle size={12} aria-hidden /> Country and coordinates disagree
                    </div>
                  )}
                </Td>
                <Td className="text-nb-400">{clusters.filter((c) => c.siteId === s.id).map((c) => c.name).join(', ') || '—'}</Td>
                <Td className="sticky right-0 bg-nb-925 group-hover:bg-nb-930"><RowActions onEdit={() => setEditing(s)} onDelete={() => setDeleting(s)} /></Td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><Td colSpan={5} className="py-8 text-center text-nb-500">No sites match “{q}”.</Td></tr>
            )}
          </tbody>
        </Table>
      )}

      <MeasuredPaths />

      {editing && <SiteForm initial={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmModal
          title={`Delete ${deleting.name}?`}
          message="Clusters at this site are kept but will have no location."
          onConfirm={() => deleteSite(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

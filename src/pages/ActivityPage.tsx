import { ScrollText } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Button, EmptyState, ErrorBanner, Input, PageHeader, Select, Table, Td, Th } from '@/components/ui/primitives'
import { api, atLeast, type AuditRow, type WorkspaceRevision } from '@/lib/api'
import { useConn, useServer } from '@/store/server'

const WINDOWS = [
  { hours: 24, label: 'Last 24 hours' },
  { hours: 24 * 7, label: 'Last 7 days' },
  { hours: 24 * 30, label: 'Last 30 days' },
  { hours: 0, label: 'Everything kept' },
]

/** Audit actions read as sentences: "agent-approved" → "agent approved". */
const words = (a: string) => a.replace(/[-_.]/g, ' ')
const stamp = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

/**
 * Accountability: who did what in this organisation. Every sign-in-backed action that changes something
 * (approving or revoking an agent, changing settings, inviting or removing a person, saving the shared
 * workspace) is written to an audit trail with the person's name. Administrators only.
 */
/** Both tables share these widths so their columns line up: time, person, action, target, the rest. */
const COLS = ['w-56', 'w-40', 'w-48', 'w-72', '']

export default function ActivityPage() {
  const status = useServer((s) => s.status)
  const role = useServer((s) => s.role)
  const conn = useConn()
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const [hours, setHours] = useState(24 * 7)
  const [rows, setRows] = useState<AuditRow[] | null>(null)
  const [source, setSource] = useState<'graph' | 'local'>('local')
  const [revs, setRevs] = useState<WorkspaceRevision[]>([])
  const [error, setError] = useState<string>()
  const allowed = status === 'connected' && atLeast(role, 'admin')

  const load = useCallback(async () => {
    try {
      const since = hours ? new Date(Date.now() - hours * 3600_000).toISOString() : undefined
      const [a, r] = await Promise.all([api.audit(conn, { actor: actor.trim(), action: action.trim(), since, limit: 300 }), api.workspaceRevisions(conn).catch(() => [])])
      setRows(a.rows)
      setSource(a.source)
      setRevs(r)
      setError(undefined)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The activity could not be read.')
    }
  }, [conn, actor, action, hours])
  useEffect(() => {
    if (!allowed) return
    const t = setTimeout(() => void load(), 250) // typing in the filters should not fire a request per key
    return () => clearTimeout(t)
  }, [allowed, load])

  if (!allowed) {
    return (
      <>
        <PageHeader title="Who did what" description="The record of actions people took in this organisation." />
        <EmptyState title="Administrators only" description="The activity trail names people and what they changed, so it is limited to administrators and owners of the organisation. Connect to a server and sign in as one to see it." />
      </>
    )
  }
  return (
    <>
      <PageHeader title="Who did what" description="Every change people made to this organisation: agents approved or revoked, settings changed, people invited or removed, the shared workspace saved. Nothing here can be edited." />
      {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="w-44"><Input value={actor} onChange={(e) => setActor(e.target.value)} placeholder="Person" aria-label="Person" data-testid="audit-actor" /></div>
        <div className="w-44"><Input value={action} onChange={(e) => setAction(e.target.value)} placeholder="Action, e.g. agent" aria-label="Action" data-testid="audit-action" /></div>
        <div className="w-44">
          <Select value={String(hours)} onChange={(e) => setHours(Number(e.target.value))} aria-label="Time window">
            {WINDOWS.map((w) => <option key={w.hours} value={w.hours}>{w.label}</option>)}
          </Select>
        </div>
        <Button onClick={() => void load()}>Refresh</Button>
        <span className="ml-auto text-xs text-nb-500">{source === 'graph' ? 'Searchable, kept in Neo4j' : 'Latest 500, kept on this server'}</span>
      </div>
      {rows === null ? (
        <p className="text-sm text-nb-500" role="status">Loading…</p>
      ) : rows.length === 0 ? (
        <EmptyState title="Nothing matches" description="No recorded action fits these filters in this window." />
      ) : (
        <Table cols={COLS}>
          <thead>
            <tr><Th>When</Th><Th>Who</Th><Th>Did what</Th><Th>To</Th><Th>Detail</Th></tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} data-testid="audit-row">
                <Td className="whitespace-nowrap text-nb-400">{stamp(r.at)}</Td>
                <Td className="font-medium text-white">{r.actor}</Td>
                <Td>{words(r.action)}</Td>
                <Td className="text-nb-400">{r.targetKind ? `${r.targetKind}${r.targetId ? ` ${r.targetId}` : ''}` : '—'}</Td>
                <Td className="max-w-md break-words text-nb-400">{r.detail || ''}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      <section className="mt-10" aria-label="Saved versions of the shared workspace">
        <h2 className="mb-3 flex items-center gap-2 text-sm font-medium text-white"><ScrollText size={14} className="text-nb-500" aria-hidden /> Saves of the shared workspace</h2>
        {revs.length === 0 ? (
          <p className="text-sm text-nb-500">{source === 'graph' ? 'No saves have been kept yet.' : 'Past saves of the workspace are kept when the server is connected to Neo4j.'}</p>
        ) : (
          <Table cols={COLS}>
            <thead><tr><Th>Saved</Th><Th>By</Th><Th>Version</Th><Th>Size</Th><Th /></tr></thead>
            <tbody>
              {revs.slice(0, 20).map((r) => (
                <tr key={r.rev}>
                  <Td className="whitespace-nowrap text-nb-400">{stamp(r.at)}</Td>
                  <Td className="font-medium text-white">{r.by}</Td>
                  <Td className="tabular-nums">{r.rev}</Td>
                  <Td className="tabular-nums text-nb-400">{(r.bytes / 1024).toFixed(1)} KB</Td>
                  <Td />
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </section>
    </>
  )
}

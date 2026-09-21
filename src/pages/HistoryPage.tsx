import clsx from 'clsx'
import { CheckCircle2, CircleAlert, Clock, History as HistoryIcon, Radio, Save, TriangleAlert } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, EmptyState, ErrorBanner, Field, Input, PageHeader, Pill, Select, Table, Td, Th } from '@/components/ui/primitives'
import { api, atLeast, type Conn, type StorageInfo } from '@/lib/api'
import { ageOf, EVENT_KINDS, EVENT_RETENTION_MAX, EVENT_RETENTION_MIN, kindLabel, parseEventRetention, pointAt, type AppSettings, type ChangeEvent, type HistoryIndex, type TrafficRate } from '@/lib/history'
import { ago, bytesPerSec } from '@/lib/observed'
import { useHistoryView } from '@/store/history'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { useTopology } from '@/store/topology'

const WINDOWS = [
  { hours: 1, label: 'Last hour' },
  { hours: 6, label: 'Last 6 hours' },
  { hours: 24, label: 'Last 24 hours' },
  { hours: 24 * 7, label: 'Last 7 days' },
  { hours: 24 * 30, label: 'Last 30 days' },
]

const SEVERITY: Record<ChangeEvent['severity'], string> = { info: 'bg-nb-500', notice: 'bg-sky-400', warning: 'bg-amber-400' }
const stamp = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

export default function HistoryPage() {
  const status = useServer((s) => s.status)
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const conn = useConn()
  if (status !== 'connected') {
    return (
      <>
        <PageHeader title="History" description="What changed, when, and what it looked like." />
        <EmptyState
          title="History is recorded by a Continuum server"
          description="Connect to a server and it records the estate as it changes: every scaling, migration, node joining or leaving and image change, with a snapshot you can scrub back through. This browser alone cannot see what happened while it was closed."
        />
      </>
    )
  }
  return <Connected conn={conn} admin={!!admin} />
}

function Connected({ conn, admin }: { conn: Conn; admin: boolean }) {
  const [hours, setHours] = useState(24)
  const [kind, setKind] = useState('')
  const [clusterId, setClusterId] = useState('')
  const [index, setIndex] = useState<HistoryIndex | null>(null)
  const [events, setEvents] = useState<ChangeEvent[] | null>(null)
  const [error, setError] = useState<string>()
  const { clusters } = useTopology()
  const [tick, setTick] = useState(0)

  const load = useCallback(async () => {
    const since = new Date(Date.now() - hours * 3600_000).toISOString()
    try {
      const [i, e] = await Promise.all([api.history(conn, since), api.events(conn, { since, kind, cluster: clusterId, limit: 400 })])
      setIndex(i)
      setEvents(e)
      setError(undefined)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'The history could not be read.')
    }
  }, [conn, hours, kind, clusterId])
  useEffect(() => {
    void load()
    const id = setInterval(() => document.visibilityState === 'visible' && setTick((t) => t + 1), 15000)
    return () => clearInterval(id)
  }, [load])
  useEffect(() => {
    if (tick > 0) void load()
  }, [tick, load])

  return (
    <>
      <PageHeader
        title="History"
        description="Every change Continuum noticed, with its likely cause, and a recording of the estate you can go back to."
        actions={admin ? <RecordNow onDone={() => setTick((t) => t + 1)} conn={conn} /> : undefined}
      />
      {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}
      <div className="mb-6 grid gap-4 xl:grid-cols-[1fr_22rem]">
        <Timeline hours={hours} setHours={setHours} index={index} events={events ?? []} />
        <Consistency />
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <h2 className="mr-auto text-sm font-medium text-white">Changes</h2>
        <div className="w-44">
          <Select value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Kind of change">
            <option value="">All kinds of change</option>
            {EVENT_KINDS.map((k) => (
              <option key={k.value} value={k.value}>{k.label}</option>
            ))}
          </Select>
        </div>
        <div className="w-44">
          <Select value={clusterId} onChange={(e) => setClusterId(e.target.value)} aria-label="Cluster">
            <option value="">All clusters</option>
            {clusters.filter((c) => !c.deletedAt).map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </Select>
        </div>
      </div>
      <Events events={events} hours={hours} />
      <div className="mt-8 grid gap-6 xl:grid-cols-2">
        <Traffic conn={conn} />
        <RecordingSettings admin={admin} conn={conn} />
      </div>
      <StorageCard conn={conn} admin={admin} tick={tick} />
    </>
  )
}

function RecordNow({ conn, onDone }: { conn: Conn; onDone: () => void }) {
  const [busy, setBusy] = useState(false)
  return (
    <Button
      disabled={busy}
      onClick={async () => {
        setBusy(true)
        try {
          await api.recordNow(conn)
          onDone()
        } finally {
          setBusy(false)
        }
      }}
    >
      <Clock size={15} /> Record now
    </Button>
  )
}

/* ---------- the timeline: pick a moment, see the estate then ---------- */

function Timeline({ hours, setHours, index, events }: { hours: number; setHours: (h: number) => void; index: HistoryIndex | null; events: ChangeEvent[] }) {
  const conn = useConn()
  const navigate = useNavigate()
  const view = useHistoryView((s) => s.view)
  const loading = useHistoryView((s) => s.loading)
  const shown = useHistoryView((s) => s.at)
  const [frac, setFrac] = useState(1)
  const end = Date.now()
  const start = end - hours * 3600_000
  const points = index?.points ?? []
  const inWindow = points.filter((p) => new Date(p.at).getTime() >= start)
  const chosen = new Date(start + frac * (end - start)).toISOString()
  const point = pointAt(inWindow.length ? inWindow : points, chosen)
  const pos = (iso: string) => Math.min(100, Math.max(0, ((new Date(iso).getTime() - start) / (end - start)) * 100))
  const oldest = points[0]?.at
  const viewError = useHistoryView((s) => s.error)
  const [exact, setExact] = useState('')
  const open = async (at: string) => {
    const got = await view(conn, at)
    if (got) navigate('/topology')
  }

  return (
    <section className="rounded-xl border border-nb-850 bg-nb-925 p-5" aria-label="Timeline">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <h2 className="mr-auto flex items-center gap-2 text-sm font-medium text-white">
          <HistoryIcon size={15} className="text-accent" aria-hidden /> Go back to a moment
        </h2>
        <div className="w-40">
          <Select value={String(hours)} onChange={(e) => { setHours(Number(e.target.value)); setFrac(1) }} aria-label="Time window">
            {WINDOWS.map((w) => (
              <option key={w.hours} value={w.hours}>{w.label}</option>
            ))}
          </Select>
        </div>
      </div>
      {points.length === 0 ? (
        <p className="py-6 text-sm text-nb-500">Nothing has been recorded yet. The first recording is made as soon as a cluster is connected and reporting.</p>
      ) : (
        <>
          <div className="relative h-14" data-testid="timeline">
            <div className="absolute inset-x-0 top-7 h-1 rounded bg-nb-850" />
            {inWindow.map((p) => (
              <span key={p.at} className="absolute top-[26px] h-2 w-px bg-nb-600" style={{ left: `${pos(p.at)}%` }} aria-hidden />
            ))}
            {events.filter((e) => new Date(e.at).getTime() >= start).map((e) => (
              <span key={e.id} className={clsx('absolute top-3 size-2 -translate-x-1/2 rounded-full', SEVERITY[e.severity])} style={{ left: `${pos(e.at)}%` }} title={`${kindLabel(e.kind)}: ${e.name}`} aria-hidden />
            ))}
            <input
              type="range"
              min={0}
              max={1000}
              value={Math.round(frac * 1000)}
              onChange={(e) => setFrac(Number(e.target.value) / 1000)}
              aria-label="Moment to view"
              className="absolute inset-x-0 top-5 h-4 w-full cursor-pointer appearance-none bg-transparent accent-[var(--color-accent)]"
            />
            <div className="absolute inset-x-0 bottom-0 flex justify-between text-[11px] text-nb-500">
              <span>{new Date(start).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })}</span>
              <span>now</span>
            </div>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <div className="min-w-0 flex-1 text-sm text-nb-400">
              {point ? (
                <>
                  Nearest recording: <strong className="font-medium text-white">{stamp(point.at)}</strong> <span className="text-nb-500">({ageOf(point.at)})</span>
                  {shown === point.at && <span className="ml-2 text-amber-300">showing now</span>}
                </>
              ) : (
                'Pick a moment on the line.'
              )}
              <div className="mt-0.5 text-xs text-nb-500">
                {points.length} recordings kept{oldest ? `, back to ${new Date(oldest).toLocaleDateString()}` : ''}; one is made every {index?.snapshotMinutes} min and right after any change. Older ones are thinned out after a day.
              </div>
            </div>
            <Button variant="primary" disabled={!point || loading} onClick={() => point && open(point.at)} data-testid="view-moment">
              View the estate then
            </Button>
          </div>
          <form
            className="mt-4 flex flex-wrap items-end gap-2 border-t border-nb-850 pt-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (exact) void open(new Date(exact).toISOString())
            }}
          >
            <label className="text-xs text-nb-500">
              Or type an exact moment
              <Input
                type="datetime-local"
                value={exact}
                step={1}
                onChange={(e) => setExact(e.target.value)}
                className="mt-1 w-56"
                data-testid="exact-moment"
              />
            </label>
            <Button type="submit" disabled={!exact || loading} data-testid="view-exact">View the estate then</Button>
            <span className="min-w-0 flex-1 basis-48 text-xs text-nb-500">You get the newest recording at or before that time; the banner says exactly which.</span>
          </form>
          {viewError && <p className="mt-2 text-sm text-red-300" role="alert">{viewError}</p>}
        </>
      )}
    </section>
  )
}

/* ---------- does the server's picture match the clusters' own? ---------- */

function Consistency() {
  // no `?? []` inside the selector: a fresh array on every read makes the store report a change every time
  const allAgents = useServer((s) => s.state?.agents)
  const agents = useMemo(() => allAgents ?? [], [allAgents])
  const minutes = useSettings((s) => s.settings.consistencyMinutes)
  const live = agents.filter((a) => a.status === 'approved')
  return (
    <section className="rounded-xl border border-nb-850 bg-nb-925 p-5" aria-label="Consistency checks">
      <h2 className="mb-1 flex items-center gap-2 text-sm font-medium text-white">
        <CheckCircle2 size={15} className="text-nb-400" aria-hidden /> Is this picture complete?
      </h2>
      <p className="mb-3 text-xs leading-5 text-nb-500">
        Changes arrive as they happen. Every {minutes} min each agent also re-sends everything it sees, and the server compares that with its own picture. A difference means a change was missed; it is corrected and recorded here, never hidden.
      </p>
      {live.length === 0 && <p className="text-sm text-nb-500">No cluster is connected yet, so there is no picture to check.</p>}
      <ul className="space-y-2">
        {live.map((a) => {
          const c = a.consistency
          return (
            <li key={a.id} className="text-sm" data-testid="consistency-row">
              <div className="flex items-center gap-2">
                {!c ? <Clock size={14} className="text-nb-500" aria-hidden /> : c.differences === 0 ? <CheckCircle2 size={14} className="text-emerald-300" aria-hidden /> : <TriangleAlert size={14} className="text-amber-300" aria-hidden />}
                <span className="font-medium text-nb-300">{a.name}</span>
                <span className="ml-auto text-xs text-nb-500">{c ? `checked ${ago(c.lastCheck)}` : 'not checked yet'}</span>
              </div>
              {c && c.differences > 0 && <div className="ml-6 mt-0.5 text-xs text-amber-300/90">Found {c.differences} difference{c.differences === 1 ? '' : 's'}: {c.summary}. Corrected.</div>}
              {c && c.differences === 0 && <div className="ml-6 text-xs text-nb-500">Matched the cluster ({c.checks} check{c.checks === 1 ? '' : 's'} so far).</div>}
            </li>
          )
        })}
      </ul>
    </section>
  )
}

/* ---------- the changes ---------- */

function Events({ events, hours }: { events: ChangeEvent[] | null; hours: number }) {
  const navigate = useNavigate()
  const conn = useConn()
  const view = useHistoryView((s) => s.view)
  if (events === null) return <p className="text-sm text-nb-500" role="status">Loading…</p>
  if (events.length === 0) {
    return <EmptyState title="Nothing changed in this window" description={`No changes were recorded in the last ${hours < 48 ? `${hours} hour${hours === 1 ? '' : 's'}` : `${Math.round(hours / 24)} days`} for this selection.`} />
  }
  return (
    <Table>
      <thead>
        <tr>
          <Th>When</Th>
          <Th>Change</Th>
          <Th>What</Th>
          <Th>Detail</Th>
          <Th />
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.id} className="hover:bg-nb-930/60" data-testid="event-row" data-kind={e.kind}>
            <Td className="whitespace-nowrap text-nb-400" >
              <div>{new Date(e.at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</div>
              <div className="text-xs text-nb-500">{new Date(e.at).toLocaleDateString()}</div>
            </Td>
            <Td>
              <span className="inline-flex items-center gap-2 whitespace-nowrap">
                <span className={clsx('size-2 shrink-0 rounded-full', SEVERITY[e.severity])} aria-label={e.severity} />
                {kindLabel(e.kind)}
              </span>
            </Td>
            <Td>
              <div className="font-medium text-white">{e.name || '—'}</div>
              {e.clusterName && e.clusterName !== e.name && <div className="text-xs text-nb-500">{e.clusterName}</div>}
            </Td>
            <Td className="max-w-xl text-nb-400">
              {e.detail}
              {e.cause && <div className="mt-0.5 text-xs italic text-nb-500">Likely cause: {e.cause}</div>}
            </Td>
            <Td>
              <Button size="sm" onClick={async () => (await view(conn, e.at)) && navigate('/topology')} title="Show the estate at the moment of this change">
                View then
              </Button>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  )
}

/* ---------- traffic over time ---------- */

/** A link that carries a trickle is not the same as one that carries nothing. */
const rate = (n: number) => (n > 0 && n < 1 ? '<1 B/s' : bytesPerSec(n))

function Traffic({ conn }: { conn: Conn }) {
  const [rates, setRates] = useState<TrafficRate[] | null>(null)
  const [snapshots, setSnapshots] = useState(0)
  const { dependencies, services, devices, externalEndpoints } = useTopology()
  useEffect(() => {
    let live = true
    api.traffic(conn, 24).then((r) => {
      if (live) {
        setRates(r.rates)
        setSnapshots(r.snapshots)
      }
    }).catch(() => live && setRates([]))
    return () => {
      live = false
    }
  }, [conn])
  const names = useMemo(() => new Map<string, string>([...services.map((s) => [s.id, s.name] as const), ...devices.map((d) => [d.id, d.name] as const), ...externalEndpoints.map((e) => [e.id, e.name ?? e.host] as const)]), [services, devices, externalEndpoints])
  const byId = useMemo(() => new Map(dependencies.map((d) => [d.id, d])), [dependencies])
  const rows = (rates ?? []).filter((r) => r.avgBytesPerSec > 0).sort((a, b) => b.avgBytesPerSec - a.avgBytesPerSec).slice(0, 8)
  return (
    <section aria-label="Busiest links">
      <h2 className="mb-2 text-sm font-medium text-white">Busiest links, last 24 hours</h2>
      {rates === null ? (
        <p className="text-sm text-nb-500" role="status">Loading…</p>
      ) : rows.length === 0 ? (
        <p className="rounded-xl border border-dashed border-nb-850 px-4 py-6 text-sm text-nb-500">
          No traffic figures over this period. They come from the traffic observer, from at least two recordings; see Discovery to switch it on.
        </p>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Link</Th>
              <Th>Average</Th>
              <Th>Peak</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const d = byId.get(r.id)
              return (
                <tr key={r.id}>
                  <Td>{d ? `${names.get(d.from) ?? d.from} → ${names.get(d.to) ?? d.to}` : r.id}{d?.port ? <span className="ml-2 text-xs text-nb-500">:{d.port}</span> : null}</Td>
                  <Td className="tabular-nums">{rate(r.avgBytesPerSec)}</Td>
                  <Td className="tabular-nums text-nb-400">{rate(r.peakBytesPerSec)}</Td>
                </tr>
              )
            })}
          </tbody>
        </Table>
      )}
      <p className="mt-2 text-xs text-nb-500">Worked out from {snapshots} recordings; the average covers the time between them, so a link that was busy for a minute in an hour shows a low average and a higher peak.</p>
    </section>
  )
}

/* ---------- what is recorded and how often (administrators) ---------- */

const FIELDS: { key: keyof Pick<AppSettings, 'snapshotMinutes' | 'retentionDays' | 'maxHistoryMb' | 'tombstoneRetentionDays' | 'consistencyMinutes' | 'staleAfterBeats' | 'flowStaleHours'>; label: string; unit: string; min: number; max: number; hint: string }[] = [
  { key: 'snapshotMinutes', label: 'Recording interval', unit: 'minutes', min: 1, max: 1440, hint: 'How often the estate is recorded. It is also recorded right after a change, and only when something differs (or once an hour).' },
  { key: 'retentionDays', label: 'Keep history for', unit: 'days', min: 1, max: 365, hint: 'Every recording is kept for a day, then one per hour for a week, then one per day.' },
  { key: 'maxHistoryMb', label: 'History size limit', unit: 'MB', min: 16, max: 8192, hint: 'When the recordings grow past this, the oldest go first. The newest is always kept.' },
  { key: 'tombstoneRetentionDays', label: 'Keep removed records visible for', unit: 'days', min: 1, max: 90, hint: "Records that disappear from an agent's report are still shown as gone for this long, then removed." },
  { key: 'consistencyMinutes', label: 'Consistency check every', unit: 'minutes', min: 1, max: 240, hint: 'How often each agent re-sends its whole picture to be compared with the server’s.' },
  { key: 'staleAfterBeats', label: 'Mark stale after', unit: 'missed heartbeats', min: 2, max: 20, hint: 'An agent beats every 30 s. Its records are shown as stale once this many are missed.' },
  { key: 'flowStaleHours', label: 'Link is quiet after', unit: 'hours', min: 1, max: 720, hint: 'How long an observed link may go unseen before it is shown as quiet.' },
]

function RecordingSettings({ admin, conn }: { admin: boolean; conn: Conn }) {
  const { settings, save, error, loaded } = useSettings()
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [eventOn, setEventOn] = useState(false)
  const [eventDraft, setEventDraft] = useState('')
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    setDraft(Object.fromEntries(FIELDS.map((f) => [f.key, String(settings[f.key])])))
    setEventOn(settings.eventRetentionDays > 0)
    setEventDraft(settings.eventRetentionDays > 0 ? String(settings.eventRetentionDays) : '')
  }, [settings])
  const bad = FIELDS.filter((f) => {
    const n = Number(draft[f.key])
    return !Number.isInteger(n) || n < f.min || n > f.max
  })
  const eventParsed = parseEventRetention(eventOn, eventDraft)
  const dirty = FIELDS.some((f) => String(settings[f.key]) !== draft[f.key]) || settings.eventRetentionDays !== eventParsed.days
  return (
    <section aria-label="Recording settings">
      <h2 className="mb-2 text-sm font-medium text-white">Recording and checks</h2>
      <div className="rounded-xl border border-nb-850 bg-nb-925 p-5">
        <div className="grid gap-4 sm:grid-cols-2">
          {FIELDS.map((f) => {
            const invalid = bad.includes(f)
            return (
              <Field key={f.key} label={f.label} hint={invalid ? `Between ${f.min} and ${f.max}.` : f.hint}>
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    inputMode="numeric"
                    min={f.min}
                    max={f.max}
                    value={draft[f.key] ?? ''}
                    disabled={!admin}
                    aria-invalid={invalid}
                    onChange={(e) => { setSaved(false); setDraft((d) => ({ ...d, [f.key]: e.target.value })) }}
                    className={clsx('w-28', invalid && 'border-red-400/60')}
                    data-testid={`setting-${f.key}`}
                  />
                  <span className="whitespace-nowrap text-xs text-nb-500">{f.unit}</span>
                </div>
              </Field>
            )
          })}
          <Field
            label="Delete old events"
            hint={
              eventOn && !eventParsed.ok
                ? `Between ${EVENT_RETENTION_MIN} and ${EVENT_RETENTION_MAX} days.`
                : 'Only the event/drift log below, never the audit trail: actions people took are kept forever regardless of this setting.'
            }
          >
            <div className="flex items-center gap-2">
              <label className="flex items-center gap-1.5 text-xs text-nb-400">
                <input
                  type="checkbox"
                  className="accent-[var(--color-accent)]"
                  checked={eventOn}
                  disabled={!admin}
                  onChange={(e) => { setSaved(false); setEventOn(e.target.checked) }}
                  data-testid="setting-eventRetentionEnabled"
                />
                after
              </label>
              <Input
                type="number"
                inputMode="numeric"
                min={EVENT_RETENTION_MIN}
                max={EVENT_RETENTION_MAX}
                value={eventDraft}
                disabled={!admin || !eventOn}
                placeholder={eventOn ? undefined : 'kept forever'}
                aria-invalid={eventOn && !eventParsed.ok}
                onChange={(e) => { setSaved(false); setEventDraft(e.target.value) }}
                className={clsx('w-28', eventOn && !eventParsed.ok && 'border-red-400/60')}
                data-testid="setting-eventRetentionDays"
              />
              <span className="whitespace-nowrap text-xs text-nb-500">days</span>
            </div>
          </Field>
        </div>
        {error && <p className="mt-3 text-sm text-red-300" role="alert"><CircleAlert size={13} className="mr-1 inline" aria-hidden />{error}</p>}
        <div className="mt-4 flex items-center gap-3">
          {admin ? (
            <>
              <Button
                variant="primary"
                disabled={!dirty || bad.length > 0 || (eventOn && !eventParsed.ok) || !loaded}
                onClick={async () => setSaved(await save(conn, { ...settings, ...Object.fromEntries(FIELDS.map((f) => [f.key, Number(draft[f.key])])), eventRetentionDays: eventParsed.days }))}
                data-testid="save-settings"
              >
                <Save size={15} /> Save
              </Button>
              {saved && <span className="text-sm text-emerald-300" role="status">Saved. Agents were told.</span>}
            </>
          ) : (
            <p className="text-xs text-nb-500">Only administrators can change these.</p>
          )}
          <Pill><Radio size={11} className="mr-1" aria-hidden /> applies to every connected agent within a minute</Pill>
        </div>
        <p className="mt-3 text-xs text-nb-500">Measurements between places are set up on the <Link to="/sites" className="text-accent hover:underline">Sites</Link> page.</p>
      </div>
    </section>
  )
}

/* ---------- where the memory is kept ---------- */

function StorageCard({ conn, admin, tick }: { conn: Conn; admin: boolean; tick: number }) {
  const [info, setInfo] = useState<StorageInfo | null>(null)
  useEffect(() => {
    let live = true
    api.storage(conn).then((i) => live && setInfo(i)).catch(() => live && setInfo(null))
    return () => {
      live = false
    }
  }, [conn, tick])
  if (!info) return null
  const graph = info.backend === 'neo4j'
  const st = info.stats
  return (
    <section className="mt-8" aria-label="Where history is kept" data-testid="storage-card">
      <h2 className="mb-2 text-sm font-medium text-white">Where history is kept</h2>
      <div className="rounded-xl border border-nb-850 bg-nb-925 p-5 text-sm">
        {!graph ? (
          <p className="text-nb-400">
            History lives in the server’s own database file. It can be scrubbed and searched by time, but questions across time (“what ran on this node last Tuesday”, a record’s full life story) need the graph database.
            An administrator can connect one when starting the server (<code className="text-nb-300">--neo4j-url</code>).
          </p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <span className={clsx('inline-flex items-center gap-2 font-medium', info.connected ? 'text-emerald-300' : 'text-amber-300')} data-testid="storage-state">
                <span className={clsx('size-2 rounded-full', info.connected ? 'bg-emerald-400' : 'bg-amber-400')} aria-hidden />
                {info.connected ? 'Neo4j connected' : info.ready ? 'Neo4j not reachable' : 'Neo4j starting'}
              </span>
              {info.buffering && <Pill>catching up: some recordings are waiting to be moved across</Pill>}
            </div>
            <p className="mt-2 text-xs leading-5 text-nb-500">
              Every recording, change, workspace save and action people took is kept as a graph you can ask about any moment. If the database is away, Continuum keeps recording locally and moves everything across when it is back, so nothing is lost.
            </p>
            {admin && info.error && !info.connected && <p className="mt-2 text-xs text-amber-300/90" role="status">{info.error}</p>}
            {admin && st && (
              <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 text-xs sm:grid-cols-5">
                {([['Recordings', st.snapshots], ['Record versions', st.versions], ['Records', st.entities], ['Events', st.events], ['Actions', st.audit]] as const).map(([l, v]) => (
                  <div key={l}>
                    <dt className="text-nb-500">{l}</dt>
                    <dd className="text-base font-medium tabular-nums text-white">{v}</dd>
                  </div>
                ))}
              </dl>
            )}
            {admin && st?.oldest && <p className="mt-2 text-xs text-nb-500">Back to {new Date(st.oldest).toLocaleString()}.</p>}
          </>
        )}
      </div>
    </section>
  )
}

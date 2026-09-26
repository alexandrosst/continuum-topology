import clsx from 'clsx'
import { CircleAlert, Gauge, Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Button, Field, Input, Pill, Select, Table, Td, Th } from '@/components/ui/primitives'
import type { ProbeTarget } from '@/lib/history'
import { ago } from '@/lib/observed'
import { atLeast } from '@/lib/api'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { usePaths, useTopology } from '@/store/topology'

const ms = (n: number) => (n < 10 ? n.toFixed(1) : Math.round(n).toString())

/**
 * Round-trip times and loss between clusters and the places they talk to, measured by the agents (TCP connect
 * timing, opt-in, no probes beyond an address the cluster already uses or an administrator named).
 */
export default function MeasuredPaths() {
  const paths = usePaths()
  const { agents, clusters } = useTopology()
  const status = useServer((s) => s.status)
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const measuring = agents.filter((a) => (a.measuring ?? 0) > 0)
  const ordered = useMemo(() => [...paths].sort((a, b) => a.fromName.localeCompare(b.fromName) || b.rttP50Ms - a.rttP50Ms), [paths])

  return (
    <section className="mt-10" aria-label="Measured paths" data-testid="measured-paths">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <h2 className="flex items-center gap-2 text-lg font-medium text-nb-300">
          <Gauge size={17} className="text-accent" aria-hidden /> Measured paths
        </h2>
        {status === 'connected' && (
          <Pill>
            {measuring.length === 0 ? 'no agent is measuring' : `${measuring.length} agent${measuring.length === 1 ? '' : 's'} measuring`}
          </Pill>
        )}
      </div>
      <p className="mb-4 max-w-3xl text-sm text-nb-500">
        How long a connection takes to open from a cluster to the places it talks to, and how often it fails. The placement advice uses these instead of guesses where they exist. Measuring is off until you turn it on for an agent (in the connect wizard, or with the agent’s measurement setting); it only ever opens a connection to addresses the cluster already uses or ones you name here.
      </p>

      {ordered.length === 0 ? (
        <div className="rounded-xl border border-dashed border-nb-850 px-5 py-8 text-center text-sm text-nb-400" data-testid="no-paths">
          Nothing has been measured yet.{' '}
          {status !== 'connected' ? 'Paths are measured by agents reporting to a Continuum server.' : measuring.length === 0 ? 'Enable measurements on an agent to start.' : 'Results appear within a couple of minutes.'}
        </div>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>From</Th>
              <Th>To</Th>
              <Th>Round trip (median)</Th>
              <Th>Best · 95th</Th>
              <Th>Failed</Th>
              <Th>Samples</Th>
              <Th>Measured</Th>
            </tr>
          </thead>
          <tbody>
            {ordered.map((p) => (
              <tr key={p.id} className={clsx(p.stale && 'opacity-60')} data-testid="path-row">
                <Td className="font-medium text-nb-300">{p.fromName}</Td>
                <Td>
                  <div>{p.toName ?? p.label ?? p.host}</div>
                  <div className="font-mono text-xs text-nb-500">{p.host}:{p.port} · {p.source === 'manual' ? 'you asked' : 'seen in traffic'}</div>
                </Td>
                <Td className="tabular-nums text-nb-300">{ms(p.rttP50Ms)} ms</Td>
                <Td className="tabular-nums text-nb-400">{ms(p.rttMinMs)} · {ms(p.rttP95Ms)} ms</Td>
                <Td className={clsx('tabular-nums', p.lossPct > 0 ? 'text-warn' : 'text-nb-400')}>{p.lossPct > 0 ? `${p.lossPct.toFixed(0)} %` : 'none'}</Td>
                <Td className="tabular-nums text-nb-400">{p.samples}</Td>
                <Td className="whitespace-nowrap text-nb-400">{p.stale ? <span className="text-warn">stale · </span> : null}{ago(p.at)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {status === 'connected' && <Targets admin={!!admin} clusterOptions={agents.filter((a) => a.clusterId).map((a) => a.clusterId as string)} clusters={clusters} />}
    </section>
  )
}

function Targets({ admin, clusterOptions, clusters }: { admin: boolean; clusterOptions: string[]; clusters: { id: string; name: string }[] }) {
  const conn = useConn()
  const { settings, save, error } = useSettings()
  const [cluster, setCluster] = useState('')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('443')
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const options = clusters.filter((c) => clusterOptions.includes(c.id))
  const name = (id: string) => clusters.find((c) => c.id === id)?.name ?? id
  const p = Number(port)
  const valid = cluster !== '' && host.trim() !== '' && Number.isInteger(p) && p >= 1 && p <= 65535

  const write = async (targets: ProbeTarget[]) => {
    setBusy(true)
    try {
      return await save(conn, { ...settings, probeTargets: targets })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mt-6 rounded-xl border border-nb-850 bg-nb-925 p-5">
      <h3 className="mb-1 text-sm font-medium text-nb-300">Addresses you asked to be measured</h3>
      <p className="mb-3 text-xs text-nb-500">For places no cluster talks to yet, such as a site you plan to move something to. Loopback, link-local and metadata addresses are always refused.</p>
      {settings.probeTargets.length === 0 ? (
        <p className="text-sm text-nb-400">None.</p>
      ) : (
        <ul className="divide-y divide-nb-850/60 text-sm" data-testid="probe-targets">
          {settings.probeTargets.map((t) => (
            <li key={t.id} className="flex items-center gap-3 py-2">
              <span className="text-nb-300">{name(t.clusterId)}</span>
              <span className="text-nb-600">→</span>
              <span className="font-mono text-xs text-nb-300">{t.host}:{t.port}</span>
              {t.label && <span className="text-nb-500">{t.label}</span>}
              {admin && (
                <Button variant="ghost" size="sm" className="ml-auto" disabled={busy} onClick={() => write(settings.probeTargets.filter((x) => x.id !== t.id))} aria-label={`Stop measuring ${t.host}`}>
                  <Trash2 size={14} />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {error && <p className="mt-3 text-sm text-bad" role="alert"><CircleAlert size={13} className="mr-1 inline" aria-hidden />{error}</p>}
      {admin ? (
        <div className="mt-4 grid gap-3 border-t border-nb-850 pt-4 md:grid-cols-[1fr_1.4fr_6rem_1fr_auto] md:items-end">
          <Field label="Measured from">
            <Select value={cluster} onChange={(e) => setCluster(e.target.value)} placeholder="Choose a cluster" aria-label="Measured from">
              {options.map((c) => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </Select>
          </Field>
          <Field label="Address">
            <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder="10.20.0.5 or host.example.org" data-testid="probe-host" />
          </Field>
          <Field label="Port">
            <Input value={port} inputMode="numeric" onChange={(e) => setPort(e.target.value)} data-testid="probe-port" />
          </Field>
          <Field label="Label (optional)">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} maxLength={80} placeholder="Frankfurt edge" />
          </Field>
          <Button
            variant="primary"
            disabled={!valid || busy}
            data-testid="add-probe"
            onClick={async () => {
              const ok = await write([...settings.probeTargets, { id: '', clusterId: cluster, host: host.trim(), port: p, label: label.trim() }])
              if (ok) {
                setHost('')
                setLabel('')
              }
            }}
          >
            <Plus size={15} /> Measure
          </Button>
        </div>
      ) : (
        <p className="mt-3 text-xs text-nb-500">Only administrators can add or remove these.</p>
      )}
    </div>
  )
}

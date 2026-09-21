import clsx from 'clsx'
import { CircleAlert, Play, Save } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button, Field, Input, Pill } from '@/components/ui/primitives'
import { api, atLeast, type Conn } from '@/lib/api'
import { BUILTIN_DECIDERS, buildDecisionInput, compare, externalDecider, runDecider, type Decider, type DeciderResult } from '@/lib/placement/deciders'
import type { Policy } from '@/lib/placement/types'
import type { World } from '@/lib/placement/world'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { Card, pts } from './shared'

/**
 * Deciders side by side. Each one proposes; the same checks vet every proposal, and one scoring function judges
 * what is left, so "which is better" is arithmetic on the same picture, not each decider grading itself.
 */
export default function Deciders({ world, policy }: { world: World; policy: Policy }) {
  const status = useServer((s) => s.status)
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const conn = useConn()
  const settings = useSettings((s) => s.settings)
  const external = status === 'connected' && settings.deciderConfigured
  const [results, setResults] = useState<DeciderResult[] | null>(null)
  const [running, setRunning] = useState(false)
  const [showInput, setShowInput] = useState(false)

  const deciders: Decider[] = useMemo(
    () => (external ? [...BUILTIN_DECIDERS, externalDecider(settings.deciderName || 'External decider', (i) => api.decide(conn, i))] : BUILTIN_DECIDERS),
    [external, settings.deciderName, conn],
  )
  // the picture or the policy changed, so an earlier comparison no longer describes it
  useEffect(() => setResults(null), [world, policy, external])

  const run = async () => {
    setRunning(true)
    try {
      setResults(await Promise.all(deciders.map((d) => runDecider(d, world, policy))))
    } finally {
      setRunning(false)
    }
  }
  const comparison = useMemo(() => (results ? compare(world, results) : null), [world, results])
  const cname = (id: string) => world.byCluster.get(id)?.name ?? id
  const input = useMemo(() => (showInput ? JSON.stringify(buildDecisionInput(world, policy), null, 2) : ''), [showInput, world, policy])

  return (
    <div className="space-y-4" data-testid="deciders">
      <Card
        title="Who proposes the moves"
        aside={
          <Button variant="primary" onClick={run} disabled={running} data-testid="run-deciders">
            <Play size={15} /> {running ? 'Running…' : 'Run and compare'}
          </Button>
        }
      >
        <ul className="space-y-3 text-sm">
          {deciders.map((d) => (
            <li key={d.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="font-medium text-white">{d.name}</span>
              <Pill>{d.kind === 'builtin' ? 'built in' : 'external'}</Pill>
              <span className="basis-full text-nb-400">{d.description}</span>
            </li>
          ))}
        </ul>
        <p className="mt-3 text-xs text-nb-500">
          Every proposal is checked against the same hard constraints (policy, labels, capacity) before it is kept, whoever made it. Nothing is applied: a decider recommends, a person decides.
        </p>
      </Card>

      {results && comparison && (
        <>
          <Card title="Comparison" aside={<span className="text-xs text-nb-500">Same scoring for every decider. Lower cost is better.</span>}>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm" data-testid="decider-totals">
                <thead className="text-xs text-nb-500">
                  <tr>
                    <th className="py-2 pr-4 font-medium">Decider</th>
                    <th className="py-2 pr-4 font-medium">Moves kept</th>
                    <th className="py-2 pr-4 font-medium">Change in cost</th>
                    <th className="py-2 pr-4 font-medium">Refused</th>
                    <th className="py-2 pr-4 font-medium">Took</th>
                  </tr>
                </thead>
                <tbody>
                  {comparison.totals.map((t) => {
                    const r = results.find((x) => x.deciderId === t.deciderId)
                    return (
                      <tr key={t.deciderId} className="border-t border-nb-850/60 text-nb-300">
                        <td className="py-2 pr-4 text-white">{t.name}</td>
                        {t.ok ? (
                          <>
                            <td className="py-2 pr-4 tabular-nums">{t.moves}</td>
                            <td className={clsx('py-2 pr-4 tabular-nums', t.costChange < 0 ? 'text-emerald-300' : t.costChange > 0 ? 'text-amber-300' : 'text-nb-500')}>
                              {t.moves === 0 ? 'no change' : `${t.costChange > 0 ? '+' : ''}${pts(t.costChange)} points`}
                            </td>
                            <td className="py-2 pr-4 tabular-nums">{r?.rejected.length ?? 0}</td>
                            <td className="py-2 pr-4 tabular-nums text-nb-500">{r?.tookMs} ms</td>
                          </>
                        ) : (
                          <td colSpan={4} className="py-2 pr-4 text-red-300"><CircleAlert size={13} className="mr-1 inline" aria-hidden />{t.error}</td>
                        )}
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
            {results.some((r) => (r.outcome?.warnings.length ?? 0) > 0) && (
              <ul className="mt-3 space-y-1 rounded-lg border border-amber-400/20 bg-amber-400/5 px-3 py-2 text-xs text-amber-200/90" data-testid="decider-warnings">
                {results.flatMap((r) => (r.outcome?.warnings ?? []).map((w) => <li key={`${r.deciderId}-${w}`}><span className="text-white">{r.name}:</span> {w}</li>))}
              </ul>
            )}
          </Card>

          <Card title="Service by service" aside={<span className="text-xs text-nb-500">Where deciders disagree comes first.</span>}>
            {comparison.rows.length === 0 ? (
              <p className="text-sm text-nb-400">No decider proposed a move. Either the estate is already well placed under this policy, or there is not yet enough traffic and path evidence to say otherwise.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="text-xs text-nb-500">
                    <tr>
                      <th className="py-2 pr-4 font-medium">Service</th>
                      <th className="py-2 pr-4 font-medium">Runs in</th>
                      {results.map((r) => (
                        <th key={r.deciderId} className="py-2 pr-4 font-medium">{r.name}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {comparison.rows.map((row) => (
                      <tr key={row.serviceId} className={clsx('border-t border-nb-850/60 text-nb-300', !row.agree && 'bg-amber-400/5')}>
                        <td className="py-2 pr-4 text-white">
                          {row.serviceName}
                          {!row.agree && <span className="ml-2 text-xs text-amber-300">disagree</span>}
                        </td>
                        <td className="py-2 pr-4 text-nb-400">{cname(row.from)}</td>
                        {results.map((r) => {
                          const p = row.picks.get(r.deciderId)
                          return (
                            <td key={r.deciderId} className="py-2 pr-4">
                              {p ? (
                                <Link className="text-accent hover:underline" to={`/placement?tab=whatif&service=${encodeURIComponent(row.serviceId)}&to=${encodeURIComponent(p.to)}`}>
                                  {cname(p.to)} <span className="text-xs text-nb-500">({pts(p.benefit)})</span>
                                </Link>
                              ) : (
                                <span className="text-nb-600">stays</span>
                              )}
                            </td>
                          )
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>

          {results.some((r) => r.rejected.length > 0) && (
            <Card title="Proposals that were refused">
              <ul className="space-y-2 text-sm text-nb-300" data-testid="rejected">
                {results.flatMap((r) =>
                  r.rejected.map((x, i) => (
                    <li key={`${r.deciderId}-${x.serviceId}-${i}`}>
                      <span className="text-white">{r.name}</span> proposed <span className="text-white">{world.byService.get(x.serviceId)?.name ?? x.serviceId}</span> → {cname(x.to)}: <span className="text-amber-200/90">{x.why}</span>
                    </li>
                  )),
                )}
              </ul>
            </Card>
          )}
        </>
      )}

      <Card
        title="What an external decider is sent"
        aside={
          <button className="text-xs text-accent hover:underline" onClick={() => setShowInput((s) => !s)} aria-expanded={showInput}>
            {showInput ? 'Hide' : 'Preview'}
          </button>
        }
      >
        <p className="text-sm text-nb-400">
          Only what this dashboard already shows: clusters and places, round trips, services with their sizes and constraints, and the traffic between them (including the external addresses services talk to). No secrets and no environment values. The server sends it, so the decider never sees a browser.
        </p>
        {showInput && (
          <pre className="mt-3 max-h-96 overflow-auto rounded-lg border border-nb-850 bg-nb-950 p-3 text-xs text-nb-300" data-testid="decision-input">{input}</pre>
        )}
      </Card>

      <ExternalConfig admin={admin} connected={status === 'connected'} conn={conn} />
    </div>
  )
}

function ExternalConfig({ admin, connected, conn }: { admin: boolean; connected: boolean; conn: Conn }) {
  const { settings, save, error } = useSettings()
  const [name, setName] = useState('')
  const [addr, setAddr] = useState('')
  const [timeout, setTimeoutSec] = useState('10')
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    setName(settings.deciderName)
    setAddr(settings.deciderUrl)
    setTimeoutSec(String(settings.deciderTimeoutSec))
  }, [settings])
  const t = Number(timeout)
  const badTimeout = !Number.isInteger(t) || t < 1 || t > 25
  const badUrl = addr !== '' && !/^https?:\/\/[^\s/]+/i.test(addr)
  const dirty = name !== settings.deciderName || addr !== settings.deciderUrl || String(settings.deciderTimeoutSec) !== timeout

  return (
    <Card title="Plug in your own decider">
      {!connected ? (
        <p className="text-sm text-nb-400">An external decider is reached through a Continuum server, which holds its address. Connect to a server to configure one.</p>
      ) : !admin ? (
        <p className="text-sm text-nb-400">
          {settings.deciderConfigured ? `“${settings.deciderName || 'An external decider'}” is configured and is included in the comparison.` : 'No external decider is configured.'} Only administrators can change this.
        </p>
      ) : (
        <>
          <p className="mb-4 max-w-2xl text-sm text-nb-400">
            Any HTTP service that accepts a JSON POST of the estate and answers with recommendations: a scheduler, an optimiser, a learned policy, a script. The server calls it, refuses redirects and internal addresses it should not reach, and every answer is checked like the built-in ones.
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" hint="Shown in comparisons.">
              <Input value={name} onChange={(e) => { setSaved(false); setName(e.target.value) }} placeholder="My scheduler" maxLength={60} data-testid="decider-name" />
            </Field>
            <Field label="Timeout" hint={badTimeout ? 'Between 1 and 25 seconds.' : 'Seconds the server waits for an answer.'}>
              <Input type="number" min={1} max={25} value={timeout} onChange={(e) => { setSaved(false); setTimeoutSec(e.target.value) }} className={clsx('w-28', badTimeout && 'border-red-400/60')} data-testid="decider-timeout" />
            </Field>
            <Field label="Address" hint={badUrl ? 'Start with http:// or https://.' : 'Leave empty to switch it off.'} className="sm:col-span-2">
              <Input value={addr} onChange={(e) => { setSaved(false); setAddr(e.target.value) }} placeholder="https://decider.example.org/decide" className={clsx(badUrl && 'border-red-400/60')} data-testid="decider-url" />
            </Field>
          </div>
          {error && <p className="mt-3 text-sm text-red-300" role="alert"><CircleAlert size={13} className="mr-1 inline" aria-hidden />{error}</p>}
          <div className="mt-4 flex items-center gap-3">
            <Button
              variant="primary"
              disabled={!dirty || badTimeout || badUrl}
              onClick={async () => setSaved(await save(conn, { ...settings, deciderName: name.trim(), deciderUrl: addr.trim(), deciderTimeoutSec: t }))}
              data-testid="save-decider"
            >
              <Save size={15} /> Save
            </Button>
            {saved && <span className="text-sm text-emerald-300" role="status">Saved.</span>}
          </div>
        </>
      )}
    </Card>
  )
}

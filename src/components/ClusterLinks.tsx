import clsx from 'clsx'
import { ArrowLeftRight, Check as CheckIcon, CircleAlert, Pencil, Plus } from 'lucide-react'
import { useMemo, useState } from 'react'
import { ConfirmModal } from '@/components/forms/ConfirmModal'
import { Button, ErrorBanner, Field, ICON_MD, ICON_SM, Input, Modal, Pill, Select, Table, Td, Th } from '@/components/ui/primitives'
import { atLeast } from '@/lib/api'
import {
  PRESETS, clusterChecks, costLine, hasPair, newPair, presetOf, removePair, replacePair, runnerOf, throughputFor, throughputProblems, throughputSummary,
  type Check, type PresetId,
} from '@/lib/clusterLinks'
import type { LinkPair } from '@/lib/history'
import type { Agent } from '@/lib/types'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { useTopology } from '@/store/topology'

/**
 * The pairs of clusters whose link is measured, and what is measured for each. Real traffic is watched for every listed pair; probing and
 * throughput tests send traffic of their own, so they are choices (see the "Choices per pair" section of the cluster-links architecture page).
 */
export default function ClusterLinks() {
  const { agents, clusters } = useTopology()
  const status = useServer((s) => s.status)
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const { settings } = useSettings()
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<LinkPair | null>(null)
  const name = useMemo(() => {
    const m = new Map(clusters.map((c) => [c.id, c.name]))
    return (id: string) => m.get(id) ?? id
  }, [clusters])
  if (status !== 'connected') return null
  const pairs = [...settings.links].sort((x, y) => name(x.a).localeCompare(name(y.a)) || name(x.b).localeCompare(name(y.b)))

  return (
    <section className="mt-10" aria-label="Cluster links" data-testid="cluster-links">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <h2 className="flex items-center gap-2 text-lg font-medium text-nb-300">
          <ArrowLeftRight size={ICON_SM} className="text-accent" aria-hidden /> Cluster links
        </h2>
        <Pill>{pairs.length} pair{pairs.length === 1 ? '' : 's'}</Pill>
        {admin && (
          <Button variant="primary" className="ml-auto" onClick={() => setAdding(true)} data-testid="add-pair">
            <Plus size={ICON_MD} aria-hidden /> Add pair
          </Button>
        )}
      </div>
      <p className="mb-4 max-w-3xl text-sm text-nb-500">
        How well two of your clusters can talk to each other, in each direction: delay, jitter, loss, the largest packet that fits, and how much the link can carry. Watching real traffic is always on for a pair. Active probing and throughput tests are choices, because they send traffic of their own.
      </p>

      {pairs.length === 0 ? (
        <div className="rounded-xl border border-dashed border-nb-850 px-5 py-8 text-center text-sm text-nb-400" data-testid="no-pairs">
          No cluster pair is being measured yet.{admin ? ' Add one to see delay, loss and capacity between two clusters.' : ' An administrator can add one.'}
        </div>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Pair</Th>
              <Th>Real traffic</Th>
              <Th>Probing</Th>
              <Th>Throughput tests</Th>
              {admin && <Th />}
            </tr>
          </thead>
          <tbody>
            {pairs.map((p) => {
              const t = p.throughput ? throughputSummary(p.throughput) : null
              return (
                <tr key={`${p.a}|${p.b}`} data-testid="pair-row">
                  <Td valign="top">
                    <div className="font-medium text-nb-300">
                      {name(p.a)} <span className="px-1 text-nb-600" aria-label="and">⇄</span> {name(p.b)}
                    </div>
                    <div className="mt-0.5 text-xs text-nb-500">{t ? `tests run from ${name(runnerOf(p))}` : p.noProbe ? 'real traffic only' : 'no throughput tests'}</div>
                  </Td>
                  <Td valign="top"><State on label="watching" /></Td>
                  <Td valign="top"><State on={!p.noProbe} label={p.noProbe ? 'off' : 'on'} /></Td>
                  <Td valign="top">
                    {t ? (
                      <>
                        <State on label={t.when} />
                        <div className="mt-0.5 text-xs text-nb-500">{t.budget}</div>
                      </>
                    ) : (
                      <State on={false} label="off" />
                    )}
                  </Td>
                  {admin && (
                    <Td valign="top" className="w-14">
                      <Button variant="ghost" size="sm" onClick={() => setEditing(p)} aria-label={`Edit ${name(p.a)} and ${name(p.b)}`}>
                        <Pencil size={ICON_MD} />
                      </Button>
                    </Td>
                  )}
                </tr>
              )
            })}
          </tbody>
        </Table>
      )}

      <p className="mt-3 max-w-3xl text-xs text-nb-500">
        Results go to your telemetry backend with the other signals of the clusters involved. Throughput tests are scheduled so they never overlap, wait while the link is busy, and stop for the day at their budget.
      </p>

      {adding && <AddPair clusters={clusters} agents={agents} name={name} onClose={() => setAdding(false)} />}
      {editing && <EditPair key={`${editing.a}|${editing.b}`} pair={editing} agents={agents} name={name} onClose={() => setEditing(null)} />}
    </section>
  )
}

function State({ on, label }: { on: boolean; label: string }) {
  return (
    <span className={clsx('inline-flex items-center gap-1.5 text-sm', on ? 'text-nb-300' : 'text-nb-500')}>
      <span className={clsx('size-[7px] rounded-full', on ? 'bg-ok' : 'border-[1.5px] border-nb-600')} aria-hidden />
      {label}
    </span>
  )
}

function Checks({ checks }: { checks: Check[] }) {
  return (
    <ul className="space-y-2 text-sm" data-testid="link-checks">
      {checks.map((c, i) => (
        <li key={i} className="flex items-start gap-2 text-nb-300">
          <span className={clsx('mt-1.5 size-[7px] shrink-0 rounded-full', c.ok ? 'bg-ok' : 'bg-warn')} aria-hidden />
          <span>
            {c.text}
            {c.fix && <span className="mt-0.5 block text-xs text-nb-500">{c.fix}</span>}
          </span>
        </li>
      ))}
    </ul>
  )
}

/** One setting that can be on or off, with what it does underneath. */
function Choice({ label, text, on, onChange, fixed }: { label: string; text: string; on: boolean; onChange?: (v: boolean) => void; fixed?: boolean }) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex-1">
        <div className="text-sm font-medium text-nb-300">{label}</div>
        <p className="mt-1 text-xs leading-relaxed text-nb-500">{text}</p>
      </div>
      <button
        role="switch"
        aria-checked={on}
        aria-label={label}
        disabled={fixed}
        onClick={() => onChange?.(!on)}
        className={clsx('relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors disabled:opacity-60', on ? 'bg-ok' : 'bg-nb-700')}
      >
        <span className={clsx('absolute top-0.5 size-4 rounded-full transition-all', on ? 'left-[18px] bg-nb-900' : 'left-0.5 bg-nb-300')} />
      </button>
    </div>
  )
}

function useSaveLinks(onDone: () => void) {
  const conn = useConn()
  const { settings, save } = useSettings()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = async (next: LinkPair[]) => {
    setBusy(true)
    setError('')
    const ok = await save(conn, { ...settings, links: next })
    setBusy(false)
    if (ok) onDone()
    else setError(useSettings.getState().error ?? 'The change could not be saved.')
  }
  return { links: settings.links, busy, error, run }
}

function EditPair({ pair, agents, name, onClose }: { pair: LinkPair; agents: Agent[]; name: (id: string) => string; onClose: () => void }) {
  const { links, busy, error, run } = useSaveLinks(onClose)
  const [probe, setProbe] = useState(!pair.noProbe)
  const [tpOn, setTpOn] = useState(!!pair.throughput)
  const start = pair.throughput ?? throughputFor('standard')
  const [preset, setPreset] = useState<PresetId | 'custom'>(presetOf(pair.throughput) ?? 'standard')
  const [hours, setHours] = useState(String((start.intervalMinutes ?? PRESETS.standard.intervalMinutes) / 60))
  const [budget, setBudget] = useState(String(start.budgetMB ?? PRESETS.standard.budgetMB))
  const [confirmRemove, setConfirmRemove] = useState(false)

  const pick = (id: PresetId | 'custom') => {
    setPreset(id)
    if (id !== 'custom') {
      setHours(String(PRESETS[id].intervalMinutes / 60))
      setBudget(String(PRESETS[id].budgetMB))
    }
  }
  const problems = tpOn ? throughputProblems(hours, budget) : []
  const throughput = tpOn && problems.length === 0 ? { intervalMinutes: Math.round(Number(hours) * 60), budgetMB: Number(budget) } : undefined
  const checks = [...clusterChecks(pair.a, name(pair.a), agents), ...clusterChecks(pair.b, name(pair.b), agents)]

  const submit = () => {
    const next: LinkPair = { a: pair.a, b: pair.b, ...(probe ? {} : { noProbe: true }), ...(throughput ? { throughput } : {}) }
    void run(replacePair(links, next))
  }

  return (
    <>
      <Modal
        open
        side
        width="max-w-md"
        onClose={onClose}
        title={`${name(pair.a)} ⇄ ${name(pair.b)}`}
        description="Cluster link"
        footer={
          <>
            <Button variant="danger" className="mr-auto" disabled={busy} onClick={() => setConfirmRemove(true)}>Remove pair</Button>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" disabled={busy || problems.length > 0} onClick={submit} data-testid="save-pair">Save</Button>
          </>
        }
      >
        <div className="space-y-5">
          <Choice label="Real traffic" text="Delay, loss and retransmits seen in the connections the clusters already have. Always on, costs nothing." on fixed />
          <hr className="border-nb-850" />
          <Choice label="Probing" text="Small packets, both ways. Gives delay, jitter and loss per direction, the path’s largest packet size and when its route changes. A few kilobytes a minute." on={probe} onChange={setProbe} />
          <hr className="border-nb-850" />
          <Choice label="Throughput tests" text="Fills the link for a few seconds to see how much it can carry, how many packets it drops when pushed, and how far it can go before it does." on={tpOn} onChange={setTpOn} />

          {tpOn && (
            <div className="space-y-4" data-testid="throughput-options">
              <div>
                <div className="mb-1.5 text-sm font-medium text-nb-300">How much</div>
                <div className="flex gap-2" role="group" aria-label="How much">
                  {([...(Object.keys(PRESETS) as PresetId[]), 'custom'] as const).map((id) => (
                    <button
                      key={id}
                      aria-pressed={preset === id}
                      onClick={() => pick(id)}
                      className={clsx('h-9 flex-1 rounded-lg border text-sm font-medium', preset === id ? 'border-accent bg-accent-soft text-nb-300' : 'border-nb-850 text-nb-400 hover:bg-nb-940')}
                    >
                      {id === 'custom' ? 'Custom' : PRESETS[id].label}
                    </button>
                  ))}
                </div>
                <p className="mt-1.5 text-xs text-nb-500">Light is every day with 50 MB, Standard every 6 hours with 500 MB, Heavy every hour with 5 GB.</p>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Every (hours)">
                  <Input inputMode="decimal" value={hours} disabled={preset !== 'custom'} onChange={(e) => setHours(e.target.value)} data-testid="tp-hours" />
                </Field>
                <Field label="Daily budget (MB)">
                  <Input inputMode="numeric" value={budget} disabled={preset !== 'custom'} onChange={(e) => setBudget(e.target.value)} data-testid="tp-budget" />
                </Field>
              </div>
              {problems.length > 0 ? (
                <ErrorBanner>{problems.join('. ')}.</ErrorBanner>
              ) : (
                throughput && <p className="rounded-lg border border-nb-850 bg-nb-900 px-3.5 py-3 text-sm text-nb-400" data-testid="tp-cost">{costLine(throughput)}</p>
              )}
              <p className="text-xs text-nb-500">Tests run from {name(runnerOf(pair))} (its id sorts first); {name(runnerOf(pair) === pair.a ? pair.b : pair.a)} only answers. Nothing to choose.</p>
            </div>
          )}

          <hr className="border-nb-850" />
          <div>
            <div className="mb-2 text-sm font-medium text-nb-300">What this needs</div>
            <Checks checks={checks} />
          </div>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </div>
      </Modal>
      {confirmRemove && (
        <ConfirmModal
          title={`Stop measuring ${name(pair.a)} ⇄ ${name(pair.b)}?`}
          message="The pair is removed from the list; probing and throughput tests for it stop. Results already sent to your backend stay there."
          confirmLabel="Remove pair"
          onConfirm={() => {
            setConfirmRemove(false)
            void run(removePair(links, pair.a, pair.b))
          }}
          onClose={() => setConfirmRemove(false)}
        />
      )}
    </>
  )
}

function AddPair({ clusters, agents, name, onClose }: { clusters: { id: string; name: string }[]; agents: Agent[]; name: (id: string) => string; onClose: () => void }) {
  const { links, busy, error, run } = useSaveLinks(onClose)
  const [x, setX] = useState('')
  const [y, setY] = useState('')
  const [probe, setProbe] = useState(true)
  const withAgent = new Set(agents.filter((a) => a.status === 'approved' && a.clusterId).map((a) => a.clusterId as string))
  const options = clusters.filter((c) => withAgent.has(c.id))
  const duplicate = x !== '' && y !== '' && x !== y && hasPair(links, x, y)
  const problem = x !== '' && x === y ? 'Choose two different clusters.' : duplicate ? 'This pair is already listed. Edit it from the table.' : ''
  const ready = x !== '' && y !== '' && !problem
  const checks = ready ? [...clusterChecks(x, name(x), agents), ...clusterChecks(y, name(y), agents)] : []

  return (
    <Modal
      open
      onClose={onClose}
      title="Add a cluster pair"
      description="One entry covers both directions."
      width="max-w-lg"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!ready || busy} onClick={() => void run([...links, { ...newPair(x, y), ...(probe ? {} : { noProbe: true }) }])} data-testid="confirm-add-pair">
            Add pair
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        {options.length < 2 ? (
          <p className="text-sm text-nb-400" data-testid="not-enough-clusters">A pair needs two clusters that each have an approved agent. Connect another cluster first.</p>
        ) : (
          <>
            <div className="grid grid-cols-[1fr_auto_1fr] items-end gap-2">
              <Field label="First cluster">
                <Select value={x} onChange={(e) => setX(e.target.value)} placeholder="Choose a cluster" aria-label="First cluster">
                  {options.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </Select>
              </Field>
              <span className="pb-2 text-nb-600" aria-hidden>⇄</span>
              <Field label="Second cluster">
                <Select value={y} onChange={(e) => setY(e.target.value)} placeholder="Choose a cluster" aria-label="Second cluster">
                  {options.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </Select>
              </Field>
            </div>
            {problem && <p className="flex items-center gap-1.5 text-sm text-warn" role="alert"><CircleAlert size={ICON_SM} aria-hidden />{problem}</p>}

            <div className="space-y-4">
              <Choice label="Real traffic" text="Always on for a listed pair." on fixed />
              <Choice label="Probing" text="Delay, jitter and loss per direction, packet size and route. Tiny traffic." on={probe} onChange={setProbe} />
              <p className="flex items-start gap-2 text-xs text-nb-500"><CheckIcon size={ICON_SM} className="mt-px shrink-0" aria-hidden />Throughput tests start off. Turn them on from the pair’s settings, with a budget you choose, so nothing heavy runs by surprise.</p>
            </div>

            {checks.length > 0 && (
              <div className="rounded-lg border border-nb-850 bg-nb-900 px-3.5 py-3">
                <div className="mb-2 text-xs font-medium text-nb-400">Checked for this pair</div>
                <Checks checks={checks} />
              </div>
            )}
          </>
        )}
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </div>
    </Modal>
  )
}

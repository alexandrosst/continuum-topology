import clsx from 'clsx'
import { ArrowLeftRight, Check as CheckIcon, ChevronRight, CircleAlert, CircleDashed, HelpCircle, Pencil, Plus } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { ConfirmModal } from '@/components/forms/ConfirmModal'
import { Button, ErrorBanner, Field, ICON_MD, ICON_SM, Input, Modal, Pill, Select, SkeletonLines, Table, Td, Th } from '@/components/ui/primitives'
import { api, atLeast } from '@/lib/api'
import {
  LINK_REQUIREMENT, PRESETS, STEP_MARK, VIA_HINT, VIA_OPTIONS, clusterChecks, costLine, firstLines, hasPair, latestView, newPair, pairKey, presetOf, removePair, replacePair, runnerOf, runsFromWords, setupSummary, throughputFieldProblems, throughputFor, throughputSummary, uncoveredChecks, uncoveredNotes,
  type Check, type LinkPairLatest, type LinksLatest, type PairSetup, type PresetId, type SetupState, type SetupStep, type Via,
} from '@/lib/clusterLinks'
import type { LinkPair } from '@/lib/history'
import type { Agent } from '@/lib/types'
import type { Conn } from '@/lib/api'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { useTopology } from '@/store/topology'

/** How often the latest figures are read again while the screen is open (the server shares one read of FUSION for a few seconds anyway). */
const LATEST_EVERY_MS = 60_000

/**
 * The latest figures of the listed pairs, read when the screen opens, whenever the list of pairs changes, and once a minute while the page
 * is visible. `failed` is a read that could not be made (not the server saying FUSION cannot be had, which `latest.reason` says).
 */
function useLatest(conn: Conn, enabled: boolean, pairsKey: string) {
  const [latest, setLatest] = useState<LinksLatest | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!enabled) return
    let stopped = false
    const read = async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      try {
        const d = await api.linksLatest(conn)
        if (stopped) return
        setLatest(d)
        setFailed(false)
      } catch {
        if (!stopped) setFailed(true)
      }
    }
    void read()
    const timer = setInterval(() => void read(), LATEST_EVERY_MS)
    return () => {
      stopped = true
      clearInterval(timer)
    }
    // pairsKey stands for the list of pairs: a pair added or removed is read for at once
  }, [conn.url, conn.org, enabled, pairsKey]) // eslint-disable-line react-hooks/exhaustive-deps
  return { latest, failed }
}

/**
 * The pairs of clusters whose link is measured, and what is measured for each. Real traffic is watched for every listed pair; probing and
 * throughput tests send traffic of their own, so they are choices (see the "Choices per pair" section of the cluster-links architecture page).
 */
export default function ClusterLinks() {
  const { agents, clusters } = useTopology()
  const status = useServer((s) => s.status)
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const conn = useConn()
  const { settings, loaded, error: loadError } = useSettings()
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<LinkPair | null>(null)
  const connected = status === 'connected'
  const pairsKey = settings.links.map((l) => pairKey(l.a, l.b)).join(',')
  const { latest, failed } = useLatest(conn, connected && loaded && settings.links.length > 0, pairsKey)
  const latestByPair = useMemo(() => new Map((latest?.pairs ?? []).map((l) => [pairKey(l.a, l.b), l])), [latest])
  const setupByPair = useMemo(() => new Map((latest?.setup ?? []).map((x) => [pairKey(x.a, x.b), x])), [latest])
  const name = useMemo(() => {
    const m = new Map(clusters.map((c) => [c.id, c.name]))
    return (id: string) => m.get(id) ?? id
  }, [clusters])
  if (status !== 'connected') return null
  // A pair needs two clusters that each have an approved agent, so with fewer there is nothing to add yet.
  const withAgent = new Set(agents.filter((a) => a.status === 'approved' && a.clusterId).map((a) => a.clusterId as string))
  const canAdd = clusters.filter((c) => withAgent.has(c.id)).length >= 2
  const pairs = [...settings.links].sort((x, y) => name(x.a).localeCompare(name(y.a)) || name(x.b).localeCompare(name(y.b)))

  return (
    <section className="mt-10" aria-label="Cluster links" data-testid="cluster-links">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <h2 className="flex items-center gap-2 text-lg font-medium text-nb-300">
          <ArrowLeftRight size={ICON_SM} className="text-accent" aria-hidden /> Cluster links
        </h2>
        {loaded && <Pill>{pairs.length} pair{pairs.length === 1 ? '' : 's'}</Pill>}
        {admin && loaded && (
          <span className="ml-auto flex items-center gap-3">
            {!canAdd && <span id="add-pair-why" className="text-xs text-nb-500" data-testid="add-pair-why">Needs two clusters with an approved agent</span>}
            <Button variant="primary" disabled={!canAdd} aria-describedby={canAdd ? undefined : 'add-pair-why'} onClick={() => setAdding(true)} data-testid="add-pair">
              <Plus size={ICON_MD} aria-hidden /> Add pair
            </Button>
          </span>
        )}
      </div>
      <p className="mb-2 max-w-3xl text-sm text-nb-500">
        How well two of your clusters can talk to each other, in each direction: delay, jitter, loss, the largest packet that fits, and how much the link can carry. Watching real traffic is always on for a pair. Active probing and throughput tests are choices, because they send traffic of their own.
      </p>
      <p className="mb-4 max-w-3xl text-sm text-nb-500" data-testid="links-requirement">{LINK_REQUIREMENT}</p>

      {!loaded ? (
        loadError ? (
          <div className="flex flex-wrap items-center gap-3" data-testid="links-load-error">
            <ErrorBanner className="flex-1">{loadError}</ErrorBanner>
            <Button onClick={() => void useSettings.getState().load(conn)}>Retry</Button>
          </div>
        ) : (
          <SkeletonLines lines={3} />
        )
      ) : pairs.length === 0 ? (
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
              <Th>Latest result</Th>
              {admin && <Th />}
            </tr>
          </thead>
          <tbody>
            {pairs.map((p) => {
              const t = p.throughput ? throughputSummary(p.throughput) : null
              const setup = setupByPair.get(pairKey(p.a, p.b))
              const notes = uncoveredNotes((latest?.notes ?? []).filter((n) => n.cluster === p.a || n.cluster === p.b), setup)
              const failing = uncoveredChecks([...clusterChecks(p.a, name(p.a), agents), ...clusterChecks(p.b, name(p.b), agents)].filter((c) => !c.ok), setup)
              // What is still to do: the server's unmet steps first, then what this page can tell from the agents it lists.
              const unmet = [...(setup ? setupSummary(setup.steps).todo.map((x) => x.text) : []), ...failing.map((c) => c.text)]
              const lines = firstLines(unmet)
              return (
                <tr key={`${p.a}|${p.b}`} data-testid="pair-row">
                  <Td valign="top">
                    <div className="font-medium text-nb-300">
                      {name(p.a)} <span className="px-1 text-nb-600" aria-label="and">⇄</span> {name(p.b)}
                    </div>
                    <div className="mt-0.5 text-xs text-nb-500">{runsFromWords(p, name(runnerOf(p)))}</div>
                    {lines.shown.map((text, i) => (
                      <div key={i} className="mt-1 flex items-start gap-1.5 text-xs text-warn" data-testid="pair-warning">
                        <CircleAlert size={ICON_SM} className="mt-px shrink-0" aria-hidden />
                        <span><span className="sr-only">To do: </span>{text}</span>
                      </div>
                    ))}
                    {lines.more > 0 && <div className="mt-1 pl-5 text-xs text-nb-500" data-testid="pair-warning-more">and {lines.more} more, listed under Setup</div>}
                    {notes.map((n) => (
                      <div key={`${n.cluster}|${n.peer ?? ''}|${n.code}`} className="mt-1 flex items-start gap-1.5 text-xs text-warn" data-testid="pair-note">
                        <CircleAlert size={ICON_SM} className="mt-px shrink-0" aria-hidden />
                        <span>{name(n.cluster)}: {n.text}</span>
                      </div>
                    ))}
                    {setup && <SetupDisclosure setup={setup} name={name} />}
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
                  <Td valign="top">
                    <Latest pair={p} found={latestByPair.get(pairKey(p.a, p.b))} latest={latest} failed={failed} name={name} />
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

      {pairs.length > 0 && <LatestNotice latest={latest} failed={failed} />}
      <p className="mt-3 max-w-3xl text-xs text-nb-500">
        Results go to your telemetry backend with the other signals of the clusters involved. Throughput tests are scheduled so they never overlap, wait while the link is busy, and stop for the day at their budget.
      </p>

      {adding && <AddPair clusters={clusters} agents={agents} name={name} onClose={() => setAdding(false)} />}
      {editing && <EditPair key={`${editing.a}|${editing.b}`} pair={editing} agents={agents} name={name} onClose={() => setEditing(null)} />}
    </section>
  )
}

const STEP_ICON: Record<SetupState, typeof CheckIcon> = { ok: CheckIcon, todo: CircleAlert, unknown: HelpCircle, optional: CircleDashed }
const STEP_TONE: Record<SetupState, string> = { ok: 'text-ok', todo: 'text-warn', unknown: 'text-nb-500', optional: 'text-nb-500' }

/**
 * What a pair needs before its figures reach FUSION, step by step, as the server worked it out. Collapsed to a line that says how it
 * stands; the list marks each step with a shape and a word for screen readers, so colour is never the only difference. An unknown step
 * is an agent too old to say, which is not a fault.
 */
function SetupDisclosure({ setup, name }: { setup: PairSetup; name: (id: string) => string }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  const sum = setupSummary(setup.steps)
  if (setup.steps.length === 0) return null
  return (
    <div className="mt-1.5" data-testid="pair-setup">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center gap-1 rounded text-xs text-nb-500 hover:text-nb-300 focus-visible:outline-2 focus-visible:outline-accent"
        data-testid="pair-setup-toggle"
      >
        <ChevronRight size={ICON_SM} className={clsx('shrink-0 transition-transform', open && 'rotate-90')} aria-hidden />
        <span>Setup</span>
        <span className={clsx(sum.todo.length > 0 && 'text-warn')} data-testid="pair-setup-summary">· {sum.label}</span>
      </button>
      {open && (
        <ul id={id} className="mt-1.5 space-y-1.5 border-l border-nb-850 pl-3 text-xs" data-testid="pair-setup-steps" aria-label={`Setup of ${name(setup.a)} and ${name(setup.b)}`}>
          {setup.steps.map((s: SetupStep, i) => {
            const Icon = STEP_ICON[s.state]
            return (
              <li key={`${s.code}|${s.cluster ?? ''}|${i}`} className="flex items-start gap-1.5" data-testid="setup-step" data-state={s.state}>
                <Icon size={ICON_SM} className={clsx('mt-px shrink-0', STEP_TONE[s.state])} aria-hidden />
                <span className={s.state === 'ok' || s.state === 'optional' || s.state === 'unknown' ? 'text-nb-500' : 'text-nb-300'}>
                  <span className="sr-only">{STEP_MARK[s.state].word}: </span>
                  {s.text}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** The latest figures of one pair, or why there are none. */
function Latest({ pair, found, latest, failed, name }: { pair: LinkPair; found?: LinkPairLatest; latest: LinksLatest | null; failed: boolean; name: (id: string) => string }) {
  const view = latestView(found)
  if (!view) {
    const text = !latest && !failed ? 'Reading…' : pair.noProbe ? 'No probing: see the traffic in FUSION' : latest?.available ? 'No data yet' : '—'
    return <span className="text-sm text-nb-500" data-testid="latest-empty">{text}</span>
  }
  const arrows = `→ is from ${name(pair.a)} to ${name(pair.b)}; ← is the other way`
  return (
    <div className="tabular-nums" data-testid="latest" title={arrows}>
      {view.loss && <div className={clsx('text-sm', view.loss.tone === 'bad' ? 'text-bad' : view.loss.tone === 'warn' ? 'text-warn' : 'text-nb-300')}>{view.loss.text}</div>}
      {view.details.length > 0 && <div className="mt-0.5 text-xs text-nb-500">{view.details.join(' · ')}</div>}
      {view.when && <div className={clsx('mt-0.5 text-xs', view.when.stale ? 'text-warn' : 'text-nb-500')}>{view.when.stale ? `${view.when.text} (old)` : view.when.text}</div>}
      <span className="sr-only">{arrows}</span>
    </div>
  )
}

/** One line under the table when the figures cannot be had at all. */
function LatestNotice({ latest, failed }: { latest: LinksLatest | null; failed: boolean }) {
  let text = ''
  if (latest?.reason === 'no-fusion') text = 'The latest results are read from FUSION, which this server does not have, so they are not shown here.'
  else if (latest?.reason === 'unavailable' || (failed && !latest)) text = 'The latest results could not be read from FUSION just now. They are tried again every minute.'
  else if (latest?.available && latest.pairs.every((p) => latestView(p) === null)) {
    text = 'No figures have arrived yet. They come from the clusters\' link metrics, which are sent to FUSION once a cluster\'s agent has Cluster links turned on in its telemetry settings.'
  }
  if (!text) return null
  return <p className="mt-3 max-w-3xl text-xs text-nb-500" data-testid="latest-notice">{text}</p>
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
  const textId = useId()
  return (
    <div className="flex items-start gap-3">
      <div className="flex-1">
        <div className="text-sm font-medium text-nb-300">{label}</div>
        <p id={textId} className="mt-1 text-xs leading-relaxed text-nb-500">{text}</p>
      </div>
      <button
        role="switch"
        aria-checked={on}
        aria-label={label}
        aria-describedby={textId}
        disabled={fixed}
        onClick={() => onChange?.(!on)}
        className={clsx('relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors disabled:opacity-60', on ? 'bg-ok' : 'bg-nb-700')}
      >
        <span className={clsx('absolute top-0.5 size-4 rounded-full transition-all', on ? 'left-[18px] bg-nb-900' : 'left-0.5 bg-nb-300')} />
      </button>
    </div>
  )
}

/** Which kind of the other cluster's addresses the active measurements go through. */
function ViaSelect({ value, onChange }: { value: Via | ''; onChange: (v: Via | '') => void }) {
  return (
    <Field label="Measure through" hint={VIA_HINT}>
      <Select value={value} onChange={(e) => onChange(e.target.value as Via | '')} aria-label="Measure through" data-testid="via-select">
        {VIA_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </Select>
    </Field>
  )
}

function useSaveLinks(onDone: () => void) {
  const conn = useConn()
  const { settings, save } = useSettings()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // `build` gets the list as the store holds it when the save starts, not as this render saw it, and nothing is sent before the settings
  // have loaded: the server takes the body as the whole document, so a save made from the defaults would reset every other setting.
  const run = async (build: (links: LinkPair[]) => LinkPair[]) => {
    const live = useSettings.getState()
    if (!live.loaded) {
      setError('Settings have not loaded yet.')
      return
    }
    setBusy(true)
    setError('')
    const ok = await save(conn, { ...live.settings, links: build(live.settings.links) })
    setBusy(false)
    if (ok) onDone()
    else setError(useSettings.getState().error ?? 'The change could not be saved.')
  }
  return { links: settings.links, busy, error, run }
}

function EditPair({ pair, agents, name, onClose }: { pair: LinkPair; agents: Agent[]; name: (id: string) => string; onClose: () => void }) {
  const { busy, error, run } = useSaveLinks(onClose)
  const [probe, setProbe] = useState(!pair.noProbe)
  const [via, setVia] = useState<Via | ''>(pair.via ?? '')
  const [tpOn, setTpOn] = useState(!!pair.throughput)
  const start = pair.throughput ?? throughputFor('standard')
  const [preset, setPreset] = useState<PresetId | 'custom'>(presetOf(pair.throughput) ?? 'standard')
  const [hours, setHours] = useState(String((start.intervalMinutes ?? PRESETS.standard.intervalMinutes) / 60))
  const [budget, setBudget] = useState(String(start.budgetMB ?? PRESETS.standard.budgetMB))
  const [confirmRemove, setConfirmRemove] = useState(false)
  // A pair set through the API may leave parts of throughput unset ("the cluster's own"); the form shows Standard's numbers for those, so
  // they are only written back once the person has touched the throughput controls.
  const [tpTouched, setTpTouched] = useState(false)

  const pick = (id: PresetId | 'custom') => {
    setTpTouched(true)
    setPreset(id)
    if (id !== 'custom') {
      setHours(String(PRESETS[id].intervalMinutes / 60))
      setBudget(String(PRESETS[id].budgetMB))
    }
  }
  const fieldProblems = tpOn ? throughputFieldProblems(hours, budget) : {}
  const hasProblems = !!(fieldProblems.hours || fieldProblems.budget)
  const throughput = tpOn && !hasProblems ? { intervalMinutes: Math.round(Number(hours) * 60), budgetMB: Number(budget) } : undefined
  const checks = [...clusterChecks(pair.a, name(pair.a), agents), ...clusterChecks(pair.b, name(pair.b), agents)]

  const submit = () => {
    const kept = tpOn && !tpTouched && pair.throughput ? pair.throughput : throughput
    const next: LinkPair = { a: pair.a, b: pair.b, ...(probe ? {} : { noProbe: true }), ...(via ? { via } : {}), ...(kept ? { throughput: kept } : {}) }
    void run((cur) => replacePair(cur, next))
  }

  return (
    <>
      <Modal
        open
        side
        width="max-w-md"
        onClose={onClose}
        dismissible={!busy}
        title={`${name(pair.a)} ⇄ ${name(pair.b)}`}
        description="Cluster link"
        footer={
          <>
            <Button variant="danger" className="mr-auto" disabled={busy} onClick={() => setConfirmRemove(true)}>Remove pair</Button>
            <Button disabled={busy} onClick={onClose}>Cancel</Button>
            <Button variant="primary" disabled={busy || hasProblems} onClick={submit} data-testid="save-pair">Save</Button>
          </>
        }
      >
        <div className="space-y-5">
          <Choice label="Real traffic" text="Delay, loss and retransmits seen in the connections the clusters already have. Always on, costs nothing." on fixed />
          <hr className="border-nb-850" />
          <Choice label="Probing" text={`Small packets, both ways, sent from ${name(runnerOf(pair))} (its id sorts first) while the other cluster only answers. Gives delay, jitter and loss per direction, the path’s largest packet size and when its route changes. A few kilobytes a minute.`} on={probe} onChange={setProbe} />
          <hr className="border-nb-850" />
          <Choice label="Throughput tests" text="Fills the link for a few seconds to see how much it can carry, how many packets it drops when pushed, and how far it can go before it does." on={tpOn} onChange={(v) => { setTpTouched(true); setTpOn(v) }} />

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
                  <Input inputMode="decimal" value={hours} disabled={preset !== 'custom'} onChange={(e) => { setTpTouched(true); setHours(e.target.value) }} aria-invalid={!!fieldProblems.hours} aria-describedby={fieldProblems.hours ? 'tp-hours-err' : undefined} data-testid="tp-hours" />
                </Field>
                <Field label="Daily budget (MB)">
                  <Input inputMode="numeric" value={budget} disabled={preset !== 'custom'} onChange={(e) => { setTpTouched(true); setBudget(e.target.value) }} aria-invalid={!!fieldProblems.budget} aria-describedby={fieldProblems.budget ? 'tp-budget-err' : undefined} data-testid="tp-budget" />
                </Field>
              </div>
              {hasProblems ? (
                <>
                  {fieldProblems.hours && <ErrorBanner id="tp-hours-err">{fieldProblems.hours}.</ErrorBanner>}
                  {fieldProblems.budget && <ErrorBanner id="tp-budget-err">{fieldProblems.budget}.</ErrorBanner>}
                </>
              ) : (
                throughput && <p className="rounded-lg border border-nb-850 bg-nb-900 px-3.5 py-3 text-sm text-nb-400" data-testid="tp-cost">{costLine(throughput)}</p>
              )}
              <p className="text-xs text-nb-500">Tests run from {name(runnerOf(pair))} (its id sorts first); {name(runnerOf(pair) === pair.a ? pair.b : pair.a)} only answers. Nothing to choose.</p>
            </div>
          )}

          <hr className="border-nb-850" />
          <ViaSelect value={via} onChange={setVia} />

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
            void run((cur) => removePair(cur, pair.a, pair.b))
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
  const [via, setVia] = useState<Via | ''>('')
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
      dismissible={!busy}
      title="Add a cluster pair"
      description="One entry covers both directions."
      width="max-w-lg"
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!ready || busy} onClick={() => void run((cur) => [...cur, { ...newPair(x, y), ...(probe ? {} : { noProbe: true }), ...(via ? { via } : {}) }])} data-testid="confirm-add-pair">
            Add pair
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <p className="text-sm text-nb-400" data-testid="add-pair-requirement">{LINK_REQUIREMENT}</p>
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
              <ViaSelect value={via} onChange={setVia} />
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

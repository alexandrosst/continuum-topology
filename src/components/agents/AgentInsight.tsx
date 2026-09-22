import clsx from 'clsx'
import { AlertCircle, AlertTriangle, Check, Copy, Info, ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Button, Field, Input } from '@/components/ui/primitives'
import TierLevels from '@/components/TierLevels'
import { api, ApiError, type ServerInfo } from '@/lib/api'
import {
  COLLECTORS,
  collectorState,
  consentChange,
  effectiveNote,
  healthSummary,
  helmUpgradeCommand,
  inForce,
  parseExclusions,
  scopeWords,
  sortProblems,
  tierName,
  uptimeWords,
  type AgentConsent,
  type AgentDiagnostics,
  type AgentProblem,
  type HealthSummary,
  type Severity,
} from '@/lib/consent'
import type { Agent, AccessTier } from '@/lib/types'
import { useServer } from '@/store/server'

const SEVERITY_STYLE: Record<Severity, { chip: string; icon: typeof Info; label: string }> = {
  error: { chip: 'border-red-400/30 bg-red-400/10 text-red-300', icon: AlertCircle, label: 'Error' },
  warn: { chip: 'border-amber-400/30 bg-amber-400/10 text-amber-300', icon: AlertTriangle, label: 'Warning' },
  info: { chip: 'border-nb-700 bg-nb-930 text-nb-400', icon: Info, label: 'Notice' },
}

const LEVEL_STYLE: Record<HealthSummary['level'], string> = {
  unknown: 'border-nb-700 bg-nb-930 text-nb-400',
  healthy: 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300',
  warn: SEVERITY_STYLE.warn.chip,
  error: SEVERITY_STYLE.error.chip,
}

/** The agent's health in a few words, for a table row: "Healthy", "2 problems". */
export function HealthChip({ diagnostics, connected }: { diagnostics?: AgentDiagnostics; connected?: boolean }) {
  const h = healthSummary(diagnostics, { now: Date.now(), connected })
  return (
    <span className={clsx('inline-flex items-center gap-1 rounded-full border px-2 py-px text-[11px] font-medium', LEVEL_STYLE[h.level])} data-testid="health-chip" data-level={h.level} title={h.level === 'unknown' ? (diagnostics ? 'The agent’s last self-report is old, or the agent is not connected: what it said then may no longer be true.' : 'The agent has not sent a self-report yet (or this is an older agent).') : 'From the agent’s own account of itself'}>
      {h.level === 'healthy' ? <ShieldCheck size={11} aria-hidden /> : h.level === 'unknown' ? null : <AlertTriangle size={11} aria-hidden />}
      {h.line}
    </span>
  )
}

/** What the agent says is wrong, worst first, each with what to do about it. */
export function Problems({ problems }: { problems: AgentProblem[] }) {
  if (problems.length === 0) return null
  return (
    <ul className="space-y-2" data-testid="agent-problems">
      {sortProblems(problems).map((p, i) => {
        const s = SEVERITY_STYLE[p.severity]
        const I = s.icon
        return (
          <li key={`${p.code}-${i}`} className="rounded-md border border-nb-850 bg-nb-950/60 px-3 py-2" data-testid="agent-problem" data-code={p.code}>
            <div className="flex flex-wrap items-center gap-2">
              <span className={clsx('inline-flex items-center gap-1 rounded-full border px-2 py-px text-[11px] font-medium', s.chip)}><I size={11} aria-hidden /> {s.label}</span>
              <code className="font-mono text-xs text-nb-300">{p.code}</code>
              {p.since && <span className="text-xs text-nb-600">since {new Date(p.since).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}</span>}
            </div>
            <p className="mt-1 break-words text-xs leading-relaxed text-nb-300">{p.message}</p>
          </li>
        )
      })}
    </ul>
  )
}

const TONE: Record<string, string> = { ok: 'text-emerald-300', warn: 'text-amber-300', paused: 'text-sky-300', off: 'text-nb-500' }

/** "What this agent can see": the ceiling the install sets, what was approved, what it collects now, and how much of the cluster is in view. */
export function CanSee({ agent, diagnostics: d }: { agent: Agent; diagnostics: AgentDiagnostics }) {
  const note = effectiveNote(d)
  const approved = (agent.status === 'approved' ? agent.accessTier : d.approvedTier) as AccessTier
  const failing = d.informers.filter((i) => !i.synced || i.lastError)
  return (
    <div data-testid="can-see">
      <div className="mb-1.5 text-sm font-medium text-nb-300">What it can see</div>
      <TierLevels
        tiers={[0, 1, 2] as AccessTier[]}
        value={d.effectiveTier as AccessTier}
        max={d.installedTier as AccessTier}
        markAt={approved}
        markLabel={`Approved up to ${tierName(approved).toLowerCase()}`}
        size="sm"
        data-testid="can-see-tier"
      />
      {note && <p className="mt-1 text-xs text-amber-300" data-testid="tier-note">{note}</p>}
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-nb-500">Namespaces</dt>
        <dd className="text-nb-300" data-testid="scope-words">{scopeWords(d)}</dd>
        <dt className="text-nb-500">Watches</dt>
        <dd className="text-nb-300">
          {d.informers.length === 0 ? 'none yet' : `${d.informers.filter((i) => i.synced).length} of ${d.informers.length} read`}
          {failing.map((i) => (
            <span key={i.name} className="block break-words text-xs text-amber-300">{i.name}: {i.lastError || 'not read yet'}</span>
          ))}
        </dd>
        <dt className="text-nb-500">Agent</dt>
        <dd className="text-nb-300">v{d.agentVersion}{d.arch ? ` · ${d.os ?? ''}/${d.arch}` : ''} · up {uptimeWords(d.uptimeSeconds)}</dd>
      </dl>
      <div className="mt-3 text-xs font-medium uppercase tracking-wide text-nb-500">Optional collectors</div>
      <ul className="mt-1 space-y-1 text-sm" data-testid="collectors">
        {COLLECTORS.map((c) => {
          const s = collectorState(d.collectors.find((x) => x.name === c.id))
          return (
            <li key={c.id} className="flex flex-wrap items-baseline gap-x-2" data-testid={`collector-${c.id}`} data-tone={s.tone}>
              <span className="text-nb-300">{c.label}</span>
              <span className={clsx('text-xs', TONE[s.tone])}>{s.label}</span>
            </li>
          )
        })}
      </ul>
      {d.partial && <p className="mt-2 text-xs text-nb-600">This is what the agent said when it connected; it has not finished looking at the cluster yet.</p>}
    </div>
  )
}

/** A one-line command with a copy button. Shared wherever the app hands someone an exact command to run: the
 *  widen hint here, the harden and teardown commands on the Agents page. */
export function CopyCommand({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <div className="mt-1 flex items-start gap-2 rounded border border-nb-850 bg-nb-950 px-2 py-1.5">
      <code className="min-w-0 flex-1 break-words font-mono text-[11px] text-nb-300" data-testid="helm-command">{text}</code>
      <button
        type="button"
        className="shrink-0 rounded p-1 text-nb-500 hover:bg-nb-930 hover:text-nb-300"
        aria-label="Copy the command"
        onClick={() => {
          void navigator.clipboard?.writeText(text).then(() => {
            setDone(true)
            window.setTimeout(() => setDone(false), 1500)
          })
        }}
      >
        {done ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
      </button>
    </div>
  )
}

/**
 * The consent panel. Everything on it can only REDUCE what this agent shares, and the agent enforces that for itself: what is above the
 * ceiling its install sets is shown, disabled, with the command the cluster's owner runs to raise it.
 */
export function ConsentPanel({ agent, diagnostics: d, consent }: { agent: Agent; diagnostics?: AgentDiagnostics; consent?: AgentConsent }) {
  const server = useServer()
  const info: ServerInfo | undefined = server.info
  const installed = (agent.installedTier ?? d?.installedTier ?? agent.accessTier) as AccessTier
  const implemented = Math.min(info?.implementedTier ?? 2, 2) as AccessTier
  const tierLadder = useMemo(() => Array.from({ length: implemented + 1 }, (_, i) => i as AccessTier), [implemented])
  const stored = consent ?? { pausedCollectors: [], excludedNamespaces: [] }

  const [tier, setTier] = useState<AccessTier>(agent.accessTier)
  const [paused, setPaused] = useState<string[]>(stored.pausedCollectors)
  const [excl, setExcl] = useState(stored.excludedNamespaces.join(', '))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [helm, setHelm] = useState('')
  const [saved, setSaved] = useState(false)
  const [asked, setAsked] = useState<AccessTier | null>(null) // an option above the ceiling that was clicked

  // Follow the server while nothing has been edited (another editor may have changed it, or the agent confirmed).
  const storedKey = `${agent.accessTier}|${stored.pausedCollectors.join(',')}|${stored.excludedNamespaces.join(',')}`
  const [seen, setSeen] = useState(storedKey)
  const parsed = useMemo(() => parseExclusions(excl), [excl])
  const draft = { tier, paused, excluded: parsed.names }
  const change = consentChange(draft, agent.accessTier, stored)
  const dirty = change.tier || change.overrides
  if (storedKey !== seen) {
    // Adjusting state while rendering, the documented way to follow a prop: only when nothing has been edited.
    setSeen(storedKey)
    if (!dirty) {
      setTier(agent.accessTier)
      setPaused(stored.pausedCollectors)
      setExcl(stored.excludedNamespaces.join(', '))
    }
  }

  const toShow = asked ?? (installed < implemented ? ((installed + 1) as AccessTier) : undefined)
  const valid = parsed.problems.length === 0
  const confirmed = inForce(consent, agent.accessTier, d)

  const save = async () => {
    const c = server.conn()
    if (!c || busy || !valid || !dirty) return
    setBusy(true)
    setError('')
    setHelm('')
    setSaved(false)
    try {
      if (change.tier) await api.setAgentTier(c, agent.id, tier)
      if (change.overrides) await api.setAgentConsent(c, agent.id, { pausedCollectors: paused, excludedNamespaces: parsed.names })
      setSaved(true)
      await server.refresh()
    } catch (e) {
      if (e instanceof ApiError) {
        setError(e.message.split('\n')[0])
        const h = e.body?.helm
        if (typeof h === 'string') setHelm(h)
      } else setError('Could not save.')
      await server.refresh().catch(() => undefined) // the first of two changes may have been applied
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="consent-panel">
      <p className="mb-3 text-xs leading-relaxed text-nb-500">
        This can only <span className="text-nb-300">reduce</span> what this agent shares. What it may read at most is set by whoever owns the cluster, when it was installed (its access tier, permissions and scope), and this server cannot exceed it: the agent checks that itself and ignores anything that would widen it.
      </p>

      <div>
        <div className="mb-1.5 text-sm font-medium text-nb-300" id="tier-label">Access this agent is approved for</div>
        <TierLevels
          tiers={tierLadder}
          value={tier}
          max={installed}
          onSelect={(t) => {
            if (t > installed) setAsked(t)
            else {
              setTier(t)
              setSaved(false)
            }
          }}
          size="sm"
          data-testid="tier-option"
          aria-labelledby="tier-label"
        />
      </div>
      {toShow !== undefined && (
        <div className="mt-2 text-xs text-nb-500" data-testid="widen-hint">
          This install allows up to <span className="text-nb-300">{tierName(installed).toLowerCase()}</span>. To allow <span className="text-nb-300">{tierName(toShow).toLowerCase()}</span>, the cluster's owner runs this in that cluster, and then you can select it here:
          <CopyCommand text={helmUpgradeCommand(info?.install, toShow)} />
        </div>
      )}
      {agent.hardenHelm && (
        <div className="mt-2 rounded-md border border-amber-500/20 bg-amber-500/5 px-3 py-2 text-xs leading-relaxed text-nb-500" data-testid="harden-hint">
          Approved access here is narrower than what this install allows in the cluster. The agent already stopped reading and reporting at the wider tier, but that is a software-level change only: its <span className="text-nb-300">Kubernetes RBAC still grants the wider tier</span> until the cluster's owner runs this there too:
          <CopyCommand text={agent.hardenHelm} />
        </div>
      )}

      <div className="mt-4">
        <div className="mb-1.5 text-sm font-medium text-nb-300">Pause optional collectors</div>
        <ul className="space-y-2">
          {COLLECTORS.map((c) => {
            const live = d?.collectors.find((x) => x.name === c.id)
            const installedHere = d ? !!live?.configured : true
            const on = paused.includes(c.id)
            return (
              <li key={c.id}>
                <label className={clsx('flex items-start gap-2.5 text-sm', installedHere ? 'cursor-pointer' : 'cursor-not-allowed opacity-60')}>
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4 accent-[var(--color-accent)]"
                    checked={on}
                    disabled={!installedHere && !on}
                    onChange={(e) => {
                      setPaused(e.target.checked ? [...paused, c.id] : paused.filter((x) => x !== c.id))
                      setSaved(false)
                    }}
                    data-testid={`pause-${c.id}`}
                  />
                  <span>
                    <span className="text-nb-300">Pause {c.label.toLowerCase()}</span>
                    <span className="block text-xs text-nb-500">{c.what} {installedHere ? 'Pausing stops it in the cluster and forgets what it held.' : 'Not installed in this cluster.'}</span>
                  </span>
                </label>
              </li>
            )
          })}
        </ul>
      </div>

      <div className="mt-4">
        <Field label="Leave more namespaces out" hint="Names separated by commas or spaces. The agent drops them before anything is sent; they can only be added to what the install already leaves out. System namespaces cannot be left out.">
          <Input value={excl} onChange={(e) => { setExcl(e.target.value); setSaved(false) }} placeholder="e.g. payments, batch" aria-invalid={!valid} data-testid="exclude-input" />
        </Field>
        {!valid && (
          <ul className="mt-1 text-xs text-red-300" role="alert" data-testid="exclude-problems">
            {parsed.problems.map((p) => <li key={p}>{p}</li>)}
          </ul>
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button variant="primary" size="sm" disabled={!dirty || !valid || busy} onClick={() => void save()} data-testid="consent-save">{busy ? 'Saving…' : 'Save'}</Button>
        {dirty && !busy && <button type="button" className="text-xs text-nb-500 hover:text-nb-300" onClick={() => { setTier(agent.accessTier); setPaused(stored.pausedCollectors); setExcl(stored.excludedNamespaces.join(', ')); setError(''); setHelm('') }}>Discard changes</button>}
        {saved && !dirty && !error && (
          <span className={clsx('text-xs', confirmed ? 'text-emerald-300' : 'text-nb-400')} role="status" data-testid="consent-status">
            {confirmed ? 'Saved, and the agent confirms it is in force.' : 'Saved. The agent applies this within seconds; the state on the left updates when it does.'}
          </span>
        )}
      </div>
      {error && (
        <div role="alert" className="mt-2 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300" data-testid="consent-error">
          {error}
          {helm && <CopyCommand text={helm} />}
        </div>
      )}
    </div>
  )
}

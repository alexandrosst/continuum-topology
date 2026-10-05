import clsx from 'clsx'
import { AlertCircle, AlertTriangle, Check, ChevronRight, Copy, Info, Loader2, ShieldCheck } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Button, Field, ICON_MD, ICON_SM, TagsInput } from '@/components/ui/primitives'
import TelemetryFields from '@/components/telemetry/TelemetryFields'
import TierLevels from '@/components/TierLevels'
import { api, ApiError, atLeast, type ServerInfo } from '@/lib/api'
import {
  COLLECTORS,
  collectorState,
  consentChange,
  discoveryStatus,
  effectiveNote,
  measurementsRunning,
  healthSummary,
  helmUpgradeCommand,
  inForce,
  parseExclusions,
  scopeWords,
  sortProblems,
  telemetryUpgradeCommand,
  telemetrySecretCommand,
  TELEMETRY_CREDENTIAL_VAR,
  seedTelemetryFromInstalled,
  TELEMETRY_SIGNALS,
  tierName,
  uptimeWords,
  type AgentConsent,
  type AgentDiagnostics,
  type AgentProblem,
  type HealthSummary,
  type InstallInfo,
  type Severity,
} from '@/lib/consent'
import { telemetryActive, telemetryProblems, type TelemetryInput } from '@/lib/install'
import { receiverAuthOf } from '@/lib/operatorHealth'
import { exportOperatorId, explainIntentError, fragmentEndpoint, generateOperatorCommands, operatorCommandBlock, operatorCommandDraft } from '@/lib/operatorIntent'
import { TONE_CLASS } from '@/lib/provenance'
import type { Agent, AccessTier, ReceiverAuth } from '@/lib/types'
import { useConn, useServer } from '@/store/server'

const SEVERITY_STYLE: Record<Severity, { chip: string; icon: typeof Info; label: string }> = {
  error: { chip: TONE_CLASS.bad, icon: AlertCircle, label: 'Error' },
  warn: { chip: TONE_CLASS.warn, icon: AlertTriangle, label: 'Warning' },
  info: { chip: TONE_CLASS.muted, icon: Info, label: 'Notice' },
}

const LEVEL_STYLE: Record<HealthSummary['level'], string> = {
  unknown: TONE_CLASS.muted,
  healthy: TONE_CLASS.ok,
  warn: SEVERITY_STYLE.warn.chip,
  error: SEVERITY_STYLE.error.chip,
}

/** The agent's health in a few words, for a table row: "Healthy", "2 problems". */
export function HealthChip({ diagnostics, connected }: { diagnostics?: AgentDiagnostics; connected?: boolean }) {
  const h = healthSummary(diagnostics, { now: Date.now(), connected })
  return (
    <span className={clsx('inline-flex items-center gap-1 rounded-full border px-2 py-px text-[11px] font-medium', LEVEL_STYLE[h.level])} data-testid="health-chip" data-level={h.level} title={h.level === 'unknown' ? (diagnostics ? 'The agent’s last self-report is old, or the agent is not connected: what it said then may no longer be true.' : 'The agent has not sent a self-report yet (or this is an older agent).') : 'From the agent’s own account of itself'}>
      {h.level === 'healthy' ? <ShieldCheck size={ICON_SM} aria-hidden /> : h.level === 'unknown' ? null : <AlertTriangle size={ICON_SM} aria-hidden />}
      {h.line}
    </span>
  )
}

/**
 * Whether the agent has finished its first full look at the cluster - every watch it runs synced at least
 * once. This is a one-time milestone, not how healthy the agent is (that's HealthChip) and not a live count
 * (those keep changing once discovery is done): it disappears once there is nothing left to report.
 */
export function DiscoveryChip({ diagnostics }: { diagnostics?: AgentDiagnostics }) {
  const s = discoveryStatus(diagnostics)
  if (s.state === 'unknown') return null
  if (s.state === 'done') {
    return (
      <span
        className="inline-flex items-center gap-1 rounded-full border border-ok/30 bg-ok/10 px-2 py-px text-[11px] font-medium text-ok"
        data-testid="discovery-chip"
        data-state="done"
        title={`Every watch this agent runs (${s.total}) has completed its first full read of the cluster.`}
      >
        <Check size={ICON_SM} aria-hidden /> Fully discovered
      </span>
    )
  }
  return (
    <span
      className="inline-flex items-center gap-1 rounded-full border border-nb-700 bg-nb-930 px-2 py-px text-[11px] font-medium text-nb-400"
      data-testid="discovery-chip"
      data-state="discovering"
      title="Still doing its first full read of the cluster (a one-time pass, separate from how busy it is once caught up)."
    >
      <Loader2 size={ICON_SM} className="animate-spin" aria-hidden /> Discovering {s.synced}/{s.total}
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
              <span className={clsx('inline-flex items-center gap-1 rounded-full border px-2 py-px text-[11px] font-medium', s.chip)}><I size={ICON_SM} aria-hidden /> {s.label}</span>
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

const TONE: Record<string, string> = { ok: 'text-ok', warn: 'text-warn', paused: 'text-info', off: 'text-nb-500' }

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
      {note && <p className="mt-1 text-xs text-warn" data-testid="tier-note">{note}</p>}
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-nb-500">Namespaces</dt>
        <dd className="text-nb-300" data-testid="scope-words">{scopeWords(d)}</dd>
        <dt className="text-nb-500">Watches</dt>
        <dd className="text-nb-300">
          {d.informers.length === 0 ? 'none yet' : `${d.informers.filter((i) => i.synced).length} of ${d.informers.length} read`}
          {failing.map((i) => (
            <span key={i.name} className="block break-words text-xs text-warn">{i.name}: {i.lastError || 'not read yet'}</span>
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
export function CopyCommand({ text, stale = false, multiline = false, testId = 'helm-command' }: { text: string; /** Out of date: shown dimmed and not copyable, rather than hand out a command that is wrong now. */ stale?: boolean; /** Keep the command's own line breaks on screen (a certificate's PEM would otherwise run together). */ multiline?: boolean; testId?: string }) {
  const [done, setDone] = useState(false)
  return (
    <div className={clsx('mt-1 flex items-start gap-2 rounded border border-nb-850 bg-nb-950 px-2 py-1.5', stale && 'opacity-50')} data-stale={stale || undefined}>
      <code className={clsx('min-w-0 flex-1 break-words font-mono text-[11px] text-nb-300', multiline && 'whitespace-pre-wrap')} data-testid={testId}>{text}</code>
      <button
        type="button"
        className="shrink-0 rounded p-1 text-nb-500 hover:bg-nb-930 hover:text-nb-300 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-nb-500"
        aria-label="Copy the command"
        disabled={stale}
        onClick={() => {
          void navigator.clipboard?.writeText(text).then(() => {
            setDone(true)
            window.setTimeout(() => setDone(false), 1500)
          })
        }}
      >
        {done ? <Check size={ICON_MD} aria-hidden /> : <Copy size={ICON_MD} aria-hidden />}
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
  const stored = consent ?? { pausedCollectors: [], excludedNamespaces: [], confirmed: true, unknownNamespaces: [] }

  const [tier, setTier] = useState<AccessTier>(agent.accessTier)
  const [paused, setPaused] = useState<string[]>(stored.pausedCollectors)
  const [excl, setExcl] = useState<string[]>(stored.excludedNamespaces)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [helm, setHelm] = useState('')
  const [saved, setSaved] = useState(false)
  const [asked, setAsked] = useState<AccessTier | null>(null) // an option above the ceiling that was clicked

  // Follow the server while nothing has been edited (another editor may have changed it, or the agent confirmed).
  const storedKey = `${agent.accessTier}|${stored.pausedCollectors.join(',')}|${stored.excludedNamespaces.join(',')}`
  const [seen, setSeen] = useState(storedKey)
  const parsed = useMemo(() => parseExclusions(excl.join(', ')), [excl])
  const draft = { tier, paused, excluded: parsed.names }
  const change = consentChange(draft, agent.accessTier, stored)
  const dirty = change.tier || change.overrides
  if (storedKey !== seen) {
    // Adjusting state while rendering, the documented way to follow a prop: only when nothing has been edited.
    setSeen(storedKey)
    if (!dirty) {
      setTier(agent.accessTier)
      setPaused(stored.pausedCollectors)
      setExcl(stored.excludedNamespaces)
    }
  }

  const toShow = asked ?? (installed < implemented ? ((installed + 1) as AccessTier) : undefined)
  const valid = parsed.problems.length === 0
  // The server's own confirmed/setAt take precedence once present (they read correctly on a fresh page
  // load, before any diagnostics have come in this session); inForce is the fallback for an older server
  // that has not started sending them yet.
  const confirmed = stored.confirmed ?? inForce(consent, agent.accessTier, d)
  const narrowed = stored.pausedCollectors.length > 0 || stored.excludedNamespaces.length > 0
  const waitingWords = !confirmed && narrowed && stored.setAt ? `${uptimeWords((Date.now() - Date.parse(stored.setAt)) / 1000)} ago` : undefined

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
        This is the agent's <span className="text-nb-300">observability intent</span> — the scope of what it reads and reports. It can only <span className="text-nb-300">reduce</span> what this agent shares. What it may read at most is set by whoever owns the cluster, when it was installed (its access tier, permissions and scope), and this server cannot exceed it: the agent checks that itself and ignores anything that would widen it.
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
        <div className="mt-2 rounded-md border border-warn/20 bg-warn/5 px-3 py-2 text-xs leading-relaxed text-nb-500" data-testid="harden-hint">
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
        <Field label="Leave more namespaces out" hint="The agent drops them before anything is sent; they can only be added to what the install already leaves out. System namespaces cannot be left out.">
          <TagsInput value={excl} onChange={(v) => { setExcl(v); setSaved(false) }} placeholder="e.g. payments, batch" aria-invalid={!valid} aria-describedby={valid ? undefined : 'exclude-problems'} data-testid="exclude-input" />
        </Field>
        {!valid && (
          <ul id="exclude-problems" className="mt-1 text-xs text-bad" role="alert" data-testid="exclude-problems">
            {parsed.problems.map((p) => <li key={p}>{p}</li>)}
          </ul>
        )}
        {valid && !dirty && stored.unknownNamespaces && stored.unknownNamespaces.length > 0 && (
          <ul className="mt-1 text-xs text-warn" data-testid="exclude-unknown">
            {stored.unknownNamespaces.map((n) => (
              <li key={n}>“{n}” has not been reported by this agent yet — check the spelling, or it may not have rolled out.</li>
            ))}
          </ul>
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button variant="primary" size="sm" disabled={!dirty || !valid || busy} onClick={() => void save()} data-testid="consent-save">{busy ? 'Saving…' : 'Save'}</Button>
        {dirty && !busy && <button type="button" className="text-xs text-nb-500 hover:text-nb-300" onClick={() => { setTier(agent.accessTier); setPaused(stored.pausedCollectors); setExcl(stored.excludedNamespaces); setError(''); setHelm('') }}>Discard changes</button>}
        {!dirty && !error && (saved || (narrowed && !confirmed)) && (
          <span className={clsx('text-xs', confirmed ? 'text-ok' : 'text-nb-400')} role="status" data-testid="consent-status">
            {confirmed
              ? saved
                ? 'Saved, and the agent confirms it is in force.'
                : 'The agent confirms this is in force.'
              : saved
                ? 'Saved. The agent applies this within seconds; the state on the left updates when it does.'
                : `Waiting on the agent to confirm this${waitingWords ? ` — asked ${waitingWords}` : ''}.`}
          </span>
        )}
      </div>
      {error && (
        <div role="alert" className="mt-2 rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-xs text-bad" data-testid="consent-error">
          {error}
          {helm && <CopyCommand text={helm} />}
        </div>
      )}
    </div>
  )
}

/** One numbered step of the "run this" screen: the number in a ring, a title, then whatever the step needs. */
function RunStep({ n, title, children, testId }: { n: number; title: string; children?: ReactNode; testId?: string }) {
  return (
    <li className="flex gap-3" data-testid={testId}>
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full border border-nb-800 bg-nb-930 text-xs font-medium text-nb-300" aria-hidden>{n}</span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-nb-200">{title}</div>
        <div className="text-xs text-nb-500">{children}</div>
      </div>
    </li>
  )
}

/**
 * What telemetry this agent's chart install actually has enabled right now (self-reported by the agent,
 * the same pattern `installedTier` above already uses), plus a "change telemetry" form that builds the
 * `helm upgrade --reuse-values` command for it. Like ConsentPanel's widen-hint, this only ever writes a
 * command - nothing here is pushed live, because telemetry is Helm-values-only in this chart, exactly like
 * the access-tier ceiling. There is no save button: the cluster's owner runs the command themselves.
 */
export function TelemetryPanel({
  diagnostics: d,
  install,
  agentId,
  clusterId,
  initialScope,
  standalone = false,
  testIdPrefix = 'telemetry-panel',
}: {
  diagnostics?: AgentDiagnostics
  install?: InstallInfo
  /** The agent and cluster this telemetry is being configured for, when the caller knows them - the
   *  destination step uses the cluster to say which regional operator already receives it. */
  agentId?: string
  clusterId?: string
  /** A scope draft handed off from the topology's "Define scope from selection" quick action, or from the
   * standalone telemetry wizard's own picker - pre-fills TelemetryFields' guided wizard and, in inline mode,
   * starts this panel's own disclosure open, so the person doesn't also have to notice and expand it by hand. */
  initialScope?: { name: string; namespaces: string[] }
  /** Skip the "Change telemetry" disclosure chrome and render the form section directly, always open. A
   * modal whose entire purpose is configuring telemetry (the standalone TelemetryWizard) shouldn't hide its
   * own form behind a second disclosure - the inline usage on an already-expanded agent row keeps it. */
  standalone?: boolean
  /** Forwarded to the outer container, the inline disclosure (inline mode only), and TelemetryFields' own
   * testIdPrefix. Needed because an expanded Agents-page row and the standalone wizard can both be mounted
   * for the same agent at once - without a distinguishing prefix they'd share every nested testid. */
  testIdPrefix?: string
}) {
  const installed = d?.installedTelemetry ?? []
  const [open, setOpen] = useState(() => !!initialScope)
  // Seeded from what the agent actually reports running, not a blank form: withTelemetry states every
  // signal explicitly on every call, so a blank draft would silently turn off everything the operator
  // didn't happen to re-check the moment they ran the generated command for an unrelated change. The
  // agent's installedTelemetryConfig (destination, redaction/resourcedetection/traces-sampling, energy/
  // accelerators source) seeds those same fields too, when the agent is new enough to report it - see
  // seedTelemetryFromInstalled's own doc comment for exactly which fields that covers.
  const [draft, setDraft] = useState<TelemetryInput>(() => seedTelemetryFromInstalled(installed, d?.installedTelemetryConfig))
  const measurementsOn = measurementsRunning(d)
  // Without measurementsOn, withTelemetry's own telemetryProblems check (see its doc comment) can never see
  // the one validation rule that depends on it - a network-latency signal with measurements off - so the
  // generated command used to build as if that problem didn't exist, silently disagreeing with the
  // role="alert" warning TelemetryFields (right above, given the same measurementsOn) already shows for
  // exactly that case.
  const conn = useConn()
  const command = useMemo(() => {
    // Who this belongs to is stamped on everything it emits, whatever the destination: the signed-in
    // organisation and this agent's cluster, added here rather than asked for in the wizard.
    const stamped = { ...draft, resourceOrgId: conn.org ?? '', resourceClusterId: clusterId ?? '' }
    const upgrade = telemetryUpgradeCommand(install, stamped, measurementsOn)
    // One block to paste, not two: the Secret the credential flags name is created first, and a failure
    // there (credential variable unset) stops everything before anything is upgraded.
    const secret = telemetrySecretCommand(stamped, measurementsOn)
    return secret ? `${secret} && \\\n${upgrade}` : upgrade
  }, [install, draft, measurementsOn, conn.org, clusterId])

  // A regional operator destination: its receiver wants a client certificate only the server can issue, so
  // there is no ready command - only an explicit "Generate" that asks the server for one (and issues a new
  // certificate every time it runs, which is why it never fires on render or on an edit).
  const isAdmin = useServer((s) => atLeast(s.role, 'admin'))
  const operatorId = exportOperatorId(draft)
  const [operatorName, setOperatorName] = useState('')
  const [operatorAuth, setOperatorAuth] = useState<ReceiverAuth | undefined>(undefined)
  useEffect(() => {
    if (!operatorId || !isAdmin) return
    let cancelled = false
    void api
      .listOperators(conn)
      .then((ops) => {
        if (cancelled) return
        const op = ops.find((o) => o.id === operatorId)
        setOperatorName(op?.name ?? '')
        setOperatorAuth(op ? receiverAuthOf(op) : undefined)
      })
      .catch(() => undefined) // only the label; the id stands in for the name
    return () => {
      cancelled = true
    }
    // conn's identifying fields, not the object: see GuidedWizard's own operator fetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.url, conn.org, operatorId, isAdmin])
  const operatorLabel = operatorName || operatorId
  // A receiver gated by the client certificate alone has no token to present: a Secret named in the draft is
  // ignored for it (the command builder drops it too, see operatorCommandDraft).
  const operatorMtls = !!operatorId && operatorAuth === 'mtls'
  const needsCredential = !operatorMtls && telemetrySecretCommand(draft, measurementsOn) !== undefined
  const operatorProblems = telemetryActive(draft) ? telemetryProblems(operatorMtls ? operatorCommandDraft(draft, 'mtls') : draft, measurementsOn) : []
  // Everything the generated block depends on. It is stale the moment any of it differs from what it was
  // generated from, and then shows dimmed and uncopyable rather than a command that is wrong now.
  const snapshot = useMemo(() => JSON.stringify({ draft, install, measurementsOn, agentId, operatorId }), [draft, install, measurementsOn, agentId, operatorId])
  const [generated, setGenerated] = useState<{ snapshot: string; text: string; action: 'created' | 'updated'; endpointNote?: string } | null>(null)
  const [generating, setGenerating] = useState(false)
  const [genError, setGenError] = useState<{ snapshot: string; message: string } | null>(null)
  const generate = async () => {
    if (generating || !agentId || !operatorId || operatorProblems.length > 0) return
    const asked = snapshot
    setGenerating(true)
    setGenError(null)
    try {
      const { result, action } = await generateOperatorCommands(conn, { agentId, operatorId, name: `Telemetry to ${operatorLabel}`, draft })
      const theirs = fragmentEndpoint(result.installFragment)
      setGenerated({
        snapshot: asked,
        text: operatorCommandBlock({ install, draft, measurementsOn, result }),
        action,
        endpointNote: theirs && theirs !== draft.exportEndpoint.trim() ? `The server sets the export endpoint to ${theirs}, not ${draft.exportEndpoint.trim()}; the server's value comes last, so it is the one that applies.` : undefined,
      })
    } catch (e) {
      setGenError({ snapshot: asked, message: explainIntentError(e) })
    } finally {
      setGenerating(false)
    }
  }
  const p = testIdPrefix
  const staleGenerated = generated !== null && generated.snapshot !== snapshot
  const operatorSection = (
    <div className="space-y-3" data-testid={`${p}-operator`}>
      {!isAdmin ? (
        <p className="text-xs text-nb-500" data-testid={`${p}-operator-admin-note`}>
          An administrator has to generate the commands for an operator destination: they include a client certificate for this cluster, which only an administrator can issue. No command is shown here.
        </p>
      ) : (
        <ol className="space-y-4">
          <RunStep n={1} title={`Generate the commands for ${operatorLabel}`}>
            <p>
              <span className="text-nb-300">{operatorLabel}</span> only accepts clients holding a certificate from your organisation&apos;s CA, so its commands are generated by the server, not built on this page. Generating issues a <span className="text-nb-300">fresh client certificate for this cluster</span> and is recorded in the audit log. The commands include a Secret holding that certificate and its private key; like the operator&apos;s own install output, it is shown once, here, so copy it when you generate it.
            </p>
            {operatorMtls && (
              <p className="mt-1" data-testid={`${p}-operator-mtls-note`}>
                This operator&apos;s receiver authenticates this cluster by that client certificate alone: no receiver token is needed or asked for.
              </p>
            )}
            {needsCredential && (
              <p className="mt-1" data-testid={`${p}-credential-hint`}>
                The receiver token Secret you named is created from <code className="font-mono text-nb-400">{TELEMETRY_CREDENTIAL_VAR}</code>: set it in that shell to <code className="font-mono text-nb-400">Bearer</code> followed by the operator&apos;s token first - it is read from there, so it never passes through this page.
              </p>
            )}
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button variant="primary" size="sm" disabled={generating || !agentId || operatorProblems.length > 0} onClick={() => void generate()} data-testid={`${p}-operator-generate`}>
                {generating ? <Loader2 size={ICON_SM} className="animate-spin" aria-hidden /> : null}
                {generating ? 'Generating…' : generated ? `Generate again for ${operatorLabel}` : `Generate commands for ${operatorLabel}`}
              </Button>
              {!agentId && <span data-testid={`${p}-operator-needs-agent`}>Open this from an agent to generate its commands.</span>}
              {agentId && operatorProblems.length > 0 && <span data-testid={`${p}-operator-problem`}>{operatorProblems[0]}</span>}
            </div>
            {genError && genError.snapshot === snapshot && (
              <div role="alert" className="mt-2 rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-bad" data-testid={`${p}-operator-error`}>
                {genError.message}
              </div>
            )}
          </RunStep>
          <RunStep n={2} title="Run what it generated, in that cluster">
            {generated ? (
              <div data-testid={`${p}-operator-result`} data-stale={staleGenerated || undefined}>
                {staleGenerated ? (
                  <p className="text-warn" role="status" data-testid={`${p}-operator-stale`}>Out of date - generate again. The settings changed after this was generated, so it is no longer copyable.</p>
                ) : (
                  <p role="status" data-testid={`${p}-operator-fresh`}>
                    {generated.action === 'created' ? 'Recorded a telemetry intent for this agent.' : 'Updated this agent’s telemetry intent.'} The cluster&apos;s owner runs this in that cluster. It is safe to run more than once: the Secrets are replaced, not duplicated.
                  </p>
                )}
                {generated.endpointNote && <p className="mt-1 text-warn" data-testid={`${p}-operator-endpoint-note`}>{generated.endpointNote}</p>}
                <CopyCommand text={generated.text} stale={staleGenerated} multiline testId={`${p}-operator-command`} />
              </div>
            ) : (
              <p>Nothing to run until step 1 has been done.</p>
            )}
          </RunStep>
        </ol>
      )}
    </div>
  )

  // Everything an external (non-operator) destination needs, as the numbered steps the wizard's last screen shows.
  const commandSection = (
    <ol className="space-y-4" data-testid={`${p}-run-steps`}>
      {needsCredential && (
        <RunStep n={1} title="Put the destination’s credential in your shell">
          <p data-testid={`${p}-credential-hint`}>
            Set <code className="font-mono text-nb-400">{TELEMETRY_CREDENTIAL_VAR}</code> to the destination&apos;s credential in the shell you run the command in - it is read from there, so it never passes through this page. The command stores it as the Secret you named, then upgrades the release.
          </p>
        </RunStep>
      )}
      <RunStep n={needsCredential ? 2 : 1} title="Run the command in that cluster">
        <p>The cluster&apos;s owner runs this. It changes only the telemetry settings and keeps everything else in the release as it is.</p>
        <CopyCommand text={command} />
      </RunStep>
    </ol>
  )

  const form = (
    <TelemetryFields
      value={draft}
      onChange={setDraft}
      testIdPrefix={testIdPrefix}
      initialScope={initialScope}
      measurementsOn={measurementsOn}
      agentId={agentId}
      clusterId={clusterId}
      runSection={telemetryActive(draft) ? <div className="mt-3" data-testid={`${p}-run`}>{operatorId ? operatorSection : commandSection}</div> : undefined}
    />
  )

  return (
    <div data-testid={testIdPrefix}>
      <div className="mb-1.5 text-sm font-medium text-nb-300">Installed now</div>
      {installed.length === 0 ? (
        <p className="text-xs text-nb-500">{d ? 'No telemetry signal is enabled in this install.' : 'Not reported yet.'}</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5" data-testid="telemetry-installed">
          {installed.map((id) => {
            const s = TELEMETRY_SIGNALS.find((x) => x.id === id)
            return (
              <li key={id} title={s?.what} className="rounded border border-nb-800 bg-nb-930 px-1.5 py-0.5 text-xs text-nb-300">
                {s?.label ?? id}
              </li>
            )
          })}
        </ul>
      )}

      {standalone ? (
        <div className="mt-3 rounded-lg border border-nb-850 bg-nb-925 p-3">{form}</div>
      ) : (
        <details className="group mt-3" open={open} onToggle={(e) => setOpen(e.currentTarget.open)} data-testid={`${testIdPrefix}-change`}>
          <summary className="flex cursor-pointer select-none items-center gap-1 text-xs font-medium text-nb-400 hover:text-nb-300 marker:content-none">
            <ChevronRight size={ICON_SM} className="transition-transform group-open:rotate-90" aria-hidden />
            Change telemetry
          </summary>
          <div className="mt-3 rounded-lg border border-nb-850 bg-nb-925 p-3">{form}</div>
        </details>
      )}
    </div>
  )
}

import clsx from 'clsx'
import { ExternalLink, Layers } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { ConfirmModal } from '@/components/forms'
import { FusionAccess } from '@/components/operators/FusionAccess'
import { FusionRetentionCard } from '@/components/operators/FusionRetention'
import { buttonClass } from '@/components/ui/buttonClass'
import { Button, ErrorBanner, ICON_SM, LiveDot, type LiveKind, SkeletonBlock, Waiting } from '@/components/ui/primitives'
import { api, ApiError, type FusionComponent, type FusionStatus } from '@/lib/api'
import { type FusionKind, fusionLabel, fusionSentence } from '@/lib/fusionStatus'
import { TONE_CLASS, type Tone } from '@/lib/provenance'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** How often the status is read again while FUSION is coming up or needs a look: images are being pulled and volumes bound,
 *  and a person is usually watching. */
export const POLL_SETTLING_MS = 4000
/** And while it simply runs: only to keep "last data 12 s ago" and the parts honest, never while the tab is hidden. */
export const POLL_RUNNING_MS = 30_000

/**
 * The bundled FUSION's state and its switch, as a hook both the page's card and the new-operator wizard share.
 * `enable` and `disable` resolve to the new status (or throw its message); the status is read again every few
 * seconds while FUSION is starting, and only then. `onChanged` is called once a switch has resolved.
 */
export function useFusion(enabled = true, onChanged?: () => void) {
  const conn = useServer((s) => s.conn)
  // The organisation in use (see usePolledList): another one's FUSION must not stay on screen after a switch.
  const org = useServer((s) => s.orgId)
  // Told once a switch has resolved, so a list that depends on FUSION being on (the central operator appears with it) is read again.
  const changed = useRef(onChanged)
  useEffect(() => {
    changed.current = onChanged
  })
  const [status, setStatus] = useState<FusionStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const alive = useRef(true)
  // Only the newest read (or switch) may write its answer: a slow poll that left before Enable was clicked must not land after
  // it and put "Off" back on screen.
  const newest = useRef(0)
  // Set on mount as well as cleared on unmount: React's StrictMode runs the cleanup once between two mounts, and a flag only ever
  // cleared would leave every later state update silently dropped.
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const shownOrg = useRef(org)
  useEffect(() => {
    if (shownOrg.current === org) return
    shownOrg.current = org
    setStatus(null)
    setError('')
  }, [org])

  const refresh = useCallback(async () => {
    const c = conn()
    if (!c || !enabled) return
    const mine = ++newest.current
    try {
      const s = await api.getFusion(c)
      if (alive.current && mine === newest.current) {
        setStatus(s)
        setError('')
      }
    } catch (e) {
      if (alive.current && mine === newest.current) setError(e instanceof ApiError ? e.message : 'Could not read the state of FUSION.')
    }
  // org is not read in here (conn() reads it at call time); it is a dependency so that switching organisation
  // makes a new callback, and with it a fresh read, instead of leaving the old organisation's data on screen.
  }, [conn, enabled, org])
  useEffect(() => {
    void refresh()
  }, [refresh])

  // Quickly while FUSION is coming up (or a part is still on its way - Grafana starts after the stores and can be slow to pull) or
  // needs a look; slowly while it runs; not at all while it is off. A first read that failed is tried again slowly.
  const kind = fusionSentence(status).kind
  const settling = kind === 'starting' || kind === 'attention' || (kind === 'running' && (status?.components ?? []).some((c) => c.desired > 0 && c.ready < c.desired))
  const pollMs = !enabled ? null : settling ? POLL_SETTLING_MS : kind === 'running' || (status === null && error !== '') ? POLL_RUNNING_MS : null
  useVisiblePolling(() => void refresh(), pollMs)

  const act = useCallback(async (f: (c: NonNullable<ReturnType<typeof conn>>) => Promise<FusionStatus>, fallback: string) => {
    const c = conn()
    if (!c) return null
    setBusy(true)
    setError('')
    ++newest.current
    try {
      const s = await f(c)
      // The switch's own answer is the freshest there is: reads that left before it returned are older than it.
      ++newest.current
      if (alive.current) setStatus(s)
      changed.current?.()
      return s
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : fallback
      if (alive.current) setError(msg)
      throw new Error(msg)
    } finally {
      if (alive.current) setBusy(false)
    }
  }, [conn])

  return {
    status,
    busy,
    error,
    refresh,
    enable: () => act((c) => api.enableFusion(c), 'Could not turn FUSION on.'),
    disable: () => act((c) => api.disableFusion(c), 'Could not turn FUSION off.'),
  }
}

type PartState = { tone: Tone; text: string }
/** One part's state in a word. `overall` is FUSION's own verdict: a part that is not ready while FUSION as a whole needs attention
 *  has stopped coming up, and "Starting" would keep promising what is not happening. */
export function partState(c: FusionComponent, overall: FusionKind): PartState {
  if (c.desired === 0) return { tone: 'muted', text: 'Off' }
  if (c.ready >= c.desired) return { tone: 'ok', text: 'Up' }
  if (overall === 'attention') return { tone: 'warn', text: 'Not ready' }
  return { tone: 'warn', text: 'Starting' }
}

const STORE_NOTE: Record<FusionComponent['component'], string> = {
  central: 'the one door in',
  metrics: 'metrics',
  logs: 'logs',
  traces: 'traces',
  grafana: 'dashboards, already connected to the three stores',
}

/** FUSION's status as a dot, in the same vocabulary as an operator's health (LiveDot): green and pulsing while it runs, a hollow
 *  fading ring while it starts, amber when it needs attention, hollow grey when it is off, being checked or cannot be switched. */
export function FusionDot({ kind, className }: { kind: FusionKind; className?: string }) {
  const live: LiveKind = kind === 'running' ? 'online' : kind === 'starting' ? 'starting' : kind === 'attention' ? 'late' : 'idle'
  return <LiveDot kind={live} className={className} />
}

/** A clock that moves on its own, so "last data 12 s ago" keeps counting between two reads of the status. */
function useNow(everyMs: number, active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(t)
  }, [everyMs, active])
  return now
}

/** The state line, the parts and the switch. The card has the height it will have from the first paint (a skeleton until the first
 *  answer), so the table under it does not jump when the answer comes. */
export function FusionPanel({ fusion }: { fusion: ReturnType<typeof useFusion> }) {
  const { status, busy, error } = fusion
  const [confirmOff, setConfirmOff] = useState(false)
  const now = useNow(10_000, status?.state === 'running')
  const sentence = fusionSentence(status, now)
  const canSwitch = !!status?.available
  const on = status?.state && status.state !== 'off'
  const conn = useServer((st) => st.conn)
  const org = useServer((st) => st.orgId)
  const links = status?.links
  const [openError, setOpenError] = useState('')
  // A page is being opened: a second click in the meantime would mint a second ticket and open a second tab. The ref is the guard (it changes
  // at once, where state changes on the next render, after a quick double click has already run twice); the state only dims the buttons.
  const openingRef = useRef(false)
  const [opening, setOpening] = useState(false)
  /** Opens one of FUSION's pages in a new tab. The tab is opened inside the click, which is what lets a pop-up blocker allow it; the
   *  address arrives with the server's answer (a link into a new tab carries no session cookie, so the server gives it a ticket). */
  const openPage = async (page: 'grafana' | 'prometheus') => {
    const c = conn()
    if (!c || openingRef.current) return
    setOpenError('')
    const tab = window.open('', '_blank')
    if (!tab) {
      setOpenError('Your browser blocked the new tab. Allow pop-ups for this address and try again.')
      return
    }
    tab.opener = null
    openingRef.current = true
    setOpening(true)
    try {
      const r = await api.openFusionPage(c, page)
      tab.location.href = `${c.url.replace(/\/$/, '')}${r.path}`
    } catch (e) {
      tab.close()
      setOpenError(e instanceof ApiError ? e.message : 'Could not open the page.')
    } finally {
      openingRef.current = false
      setOpening(false)
    }
  }
  const starting = sentence.kind === 'starting'
  return (
    <div className="min-h-[3.75rem] rounded-lg border border-nb-850 bg-nb-925 p-4" data-testid="fusion-panel" data-fusion={sentence.kind}>
      {status === null && !error ? (
        <div className="space-y-3" role="status" aria-label="Checking FUSION">
          <SkeletonBlock className="h-4 w-48" />
          <SkeletonBlock className="h-24 w-full" />
        </div>
      ) : (
      <>
      {/* Only the state's word, so that a change (Starting, then Running) is announced and "last data 12 s ago" counting up is not. */}
      <span className="sr-only" aria-live="polite" aria-atomic="true" data-testid="fusion-live">FUSION: {fusionLabel(sentence.kind)}</span>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="flex items-center gap-1.5 text-sm font-medium text-nb-200">
          <Layers size={ICON_SM} className="text-nb-500" aria-hidden /> FUSION
        </span>
        <span className={clsx('inline-flex flex-wrap items-center gap-x-2 gap-y-1 text-xs', sentence.kind === 'unavailable' || sentence.kind === 'off' || sentence.kind === 'checking' ? 'text-nb-500' : 'text-nb-400')} data-testid="fusion-status">
          {starting ? (
            // The one spinner of the page: the parts and every row elsewhere show a dot, which is still.
            <Waiting testId="fusion-waiting"><span>{sentence.text}</span></Waiting>
          ) : (
            <>
              <FusionDot kind={sentence.kind} />
              <span>{sentence.text}</span>
            </>
          )}
          {starting && <span className="text-nb-500">Usually under two minutes - you can leave this page; collectors keep buffering and catch up.</span>}
        </span>
        {canSwitch && (
          <span className="ml-auto flex flex-wrap items-center gap-2">
            {on && (
              <>
                <OpenLink ready={!!links?.grafana} busy={opening} onOpen={() => void openPage('grafana')} testId="fusion-open-grafana">Open Grafana</OpenLink>
                <OpenLink ready={!!links?.prometheus} busy={opening} onOpen={() => void openPage('prometheus')} testId="fusion-open-prometheus">Open Prometheus</OpenLink>
              </>
            )}
            {on ? (
              <Button size="sm" onClick={() => setConfirmOff(true)} disabled={busy} data-testid="fusion-disable">Turn off</Button>
            ) : (
              <Button size="sm" onClick={() => void fusion.enable().catch(() => undefined)} disabled={busy} data-testid="fusion-enable">
                {busy ? 'Starting…' : 'Enable FUSION'}
              </Button>
            )}
          </span>
        )}
      </div>

      {canSwitch && (status?.components?.length ?? 0) > 0 && (
        <ul className="mt-3 divide-y divide-nb-850 rounded-md border border-nb-850 text-xs" data-testid="fusion-parts">
          {status!.components!.map((c) => {
            const p = partState(c, sentence.kind)
            return (
              <li key={c.component} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 px-3 py-1.5">
                <span className="flex-1 text-nb-300 sm:w-32 sm:flex-none">{c.label}</span>
                <span className="order-3 w-full min-w-0 text-nb-500 sm:order-none sm:w-auto sm:flex-1">
                  {STORE_NOTE[c.component]}
                  {c.reason && c.ready < c.desired && <span className="ml-2 text-nb-400" data-testid={`fusion-reason-${c.component}`}>{c.reason}</span>}
                </span>
                <span className={clsx('inline-flex items-center rounded border px-1.5 py-px text-[11px] font-medium leading-4', TONE_CLASS[p.tone])} data-testid={`fusion-part-${c.component}`}>{p.text}</span>
              </li>
            )
          })}
        </ul>
      )}

      {canSwitch && links && (
        <p className="mt-3 text-xs leading-relaxed text-nb-500" data-testid="fusion-links-note">
          Grafana and Prometheus open through this server, so they need your sign-in and nothing extra is exposed. Loki and Tempo have no page of their own;
          Grafana is already connected to them.
        </p>
      )}

      {canSwitch && status?.central && (
        <p className="mt-3 text-xs leading-relaxed text-nb-500" data-testid="fusion-exposure">
          {status.central.exposed ? (
            <>The central operator is reachable from other clusters at <code className="font-mono text-nb-400">{status.central.endpoint}</code>. Anything that sends needs a client certificate from it; the three stores are never exposed.</>
          ) : (
            <>The central operator is reachable inside this cluster only (<code className="font-mono text-nb-400">{status.central.endpoint}</code>), so a regional operator in another cluster cannot send to it yet. To allow that, expose the central operator and record where it is reachable: <span className="text-nb-400">Reachable at</span> on its row in the list below.</>
          )}
        </p>
      )}
      {error && <ErrorBanner className="mt-3">{error}</ErrorBanner>}
      {openError && <ErrorBanner className="mt-3" data-testid="fusion-open-error">{openError}</ErrorBanner>}

      {status?.data && <FusionRetentionCard key={org} state={status.state} />}
      {status?.data && <FusionAccess />}
      </>
      )}

      {confirmOff && (
        <ConfirmModal
          title="Turn FUSION off?"
          message="The central operator and the three stores stop. What they saved stays on their volumes and comes back when FUSION is turned on again. Regional operators sending to it keep what they cannot deliver queued for a while, then drop it, until it is back."
          confirmLabel="Turn off"
          onConfirm={() => void fusion.disable().catch(() => undefined)}
          onClose={() => setConfirmOff(false)}
        />
      )}
    </div>
  )
}

/** A page FUSION serves, opened in a new tab. Before it is up (the part is still starting) it is shown, disabled, so the person
 *  knows it is coming rather than wondering where it is. */
function OpenLink({ ready, busy = false, onOpen, children, testId }: { ready: boolean; /** A page is being opened: dimmed, so a second click cannot start a second tab. */ busy?: boolean; onOpen: () => void; children: string; testId: string }) {
  const cls = buttonClass('secondary', 'sm')
  if (!ready) {
    return (
      <span className={clsx(cls, 'cursor-not-allowed opacity-45')} aria-disabled="true" title="Available when it has started" data-testid={testId}>
        <ExternalLink size={ICON_SM} aria-hidden /> {children}
      </span>
    )
  }
  return (
    <button type="button" className={cls} onClick={onOpen} disabled={busy} data-testid={testId}>
      <ExternalLink size={ICON_SM} aria-hidden /> {children}
    </button>
  )
}

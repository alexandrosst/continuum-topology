import clsx from 'clsx'
import { Layers } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { ConfirmModal } from '@/components/forms'
import { Button, ErrorBanner, ICON_SM, PulseDot } from '@/components/ui/primitives'
import { api, ApiError, type FusionComponent, type FusionStatus } from '@/lib/api'
import { fusionSentence } from '@/lib/fusionStatus'
import { TONE_CLASS, type Tone } from '@/lib/provenance'
import { useServer } from '@/store/server'

/** How often the status is read again while FUSION is starting: images are being pulled and volumes bound. */
const POLL_MS = 4000

/**
 * The bundled FUSION's state and its switch, as a hook both the page's card and the new-operator wizard share.
 * `enable` and `disable` resolve to the new status (or throw its message); the status is read again every few
 * seconds while FUSION is starting, and only then.
 */
export function useFusion(enabled = true) {
  const conn = useServer((s) => s.conn)
  const [status, setStatus] = useState<FusionStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const alive = useRef(true)
  useEffect(() => () => { alive.current = false }, [])

  const refresh = useCallback(async () => {
    const c = conn()
    if (!c || !enabled) return
    try {
      const s = await api.getFusion(c)
      if (alive.current) setStatus(s)
    } catch (e) {
      if (alive.current) setError(e instanceof ApiError ? e.message : 'Could not read the state of FUSION.')
    }
  }, [conn, enabled])
  useEffect(() => {
    void refresh()
  }, [refresh])
  useEffect(() => {
    if (status?.state !== 'starting') return
    const t = setInterval(() => void refresh(), POLL_MS)
    return () => clearInterval(t)
  }, [status?.state, refresh])

  const act = useCallback(async (f: (c: NonNullable<ReturnType<typeof conn>>) => Promise<FusionStatus>, fallback: string) => {
    const c = conn()
    if (!c) return null
    setBusy(true)
    setError('')
    try {
      const s = await f(c)
      if (alive.current) setStatus(s)
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
function partState(c: FusionComponent): PartState {
  if (c.desired === 0) return { tone: 'muted', text: 'Off' }
  if (c.ready >= c.desired) return { tone: 'ok', text: 'Up' }
  return { tone: 'warn', text: 'Starting' }
}

const STORE_NOTE: Record<FusionComponent['component'], string> = {
  central: 'the one door in',
  metrics: 'metrics',
  logs: 'logs',
  traces: 'traces',
}

/** FUSION's status as a dot and one sentence, like an operator's health: blue and pulsing while it runs, hollow
 *  blue while it starts, hollow orange when it needs attention, hollow grey when it is off or cannot be switched. */
export function FusionDot({ kind, className }: { kind: ReturnType<typeof fusionSentence>['kind']; className?: string }) {
  if (kind === 'running') return <PulseDot color="bg-info" size="size-1.5" className={className} />
  const border = kind === 'attention' ? 'border-warn' : kind === 'starting' ? 'border-info' : 'border-nb-500'
  return <span className={clsx('inline-block size-1.5 shrink-0 rounded-full border', border, kind === 'starting' && 'animate-pulse', className)} aria-hidden />
}

/** The state line, the four parts and the switch. `compact` drops the frame and the parts, for inside a form. */
export function FusionPanel({ fusion, compact, enableLabel = 'Enable FUSION' }: { fusion: ReturnType<typeof useFusion>; compact?: boolean; enableLabel?: string }) {
  const { status, busy, error } = fusion
  const [confirmOff, setConfirmOff] = useState(false)
  const sentence = fusionSentence(status)
  const canSwitch = !!status?.available
  const on = status?.state && status.state !== 'off'
  return (
    <div className={clsx(!compact && 'rounded-lg border border-nb-850 bg-nb-925 p-4')} data-testid="fusion-panel" data-fusion={sentence.kind}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="flex items-center gap-1.5 text-sm font-medium text-nb-200">
          <Layers size={ICON_SM} className="text-nb-500" aria-hidden /> FUSION
        </span>
        <span className={clsx('inline-flex items-center gap-1.5 text-xs', sentence.kind === 'unavailable' || sentence.kind === 'off' ? 'text-nb-500' : 'text-nb-400')} data-testid="fusion-status">
          <FusionDot kind={sentence.kind} />
          <span>{sentence.text}</span>
        </span>
        {canSwitch && (
          <span className="ml-auto">
            {on ? (
              <Button size="sm" onClick={() => setConfirmOff(true)} disabled={busy} data-testid="fusion-disable">Turn off</Button>
            ) : (
              <Button size="sm" variant="primary" onClick={() => void fusion.enable().catch(() => undefined)} disabled={busy} data-testid="fusion-enable">
                {busy ? 'Starting…' : enableLabel}
              </Button>
            )}
          </span>
        )}
      </div>

      {!compact && canSwitch && (status?.components?.length ?? 0) > 0 && (
        <ul className="mt-3 divide-y divide-nb-850 rounded-md border border-nb-850 text-xs" data-testid="fusion-parts">
          {status!.components!.map((c) => {
            const p = partState(c)
            return (
              <li key={c.component} className="flex items-center gap-3 px-3 py-1.5">
                <span className="w-32 text-nb-300">{c.label}</span>
                <span className="flex-1 text-nb-500">{STORE_NOTE[c.component]}</span>
                <span className={clsx('inline-flex items-center rounded border px-1.5 py-px text-[11px] font-medium leading-4', TONE_CLASS[p.tone])} data-testid={`fusion-part-${c.component}`}>{p.text}</span>
              </li>
            )
          })}
        </ul>
      )}

      {canSwitch && status?.central && (
        <p className="mt-3 text-xs leading-relaxed text-nb-500" data-testid="fusion-exposure">
          {status.central.exposed ? (
            <>The central operator is reachable from other clusters at <code className="font-mono text-nb-400">{status.central.endpoint}</code>. Anything that sends needs a client certificate from it; the three stores are never exposed.</>
          ) : (
            <>The central operator is reachable inside this cluster only (<code className="font-mono text-nb-400">{status.central.endpoint}</code>), so a regional operator in another cluster cannot send to it yet. Set <code className="font-mono text-nb-400">fusionControl.centralAddress</code> on the server install, and expose the central operator, to allow that.</>
          )}
        </p>
      )}
      {error && <ErrorBanner className="mt-3">{error}</ErrorBanner>}

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

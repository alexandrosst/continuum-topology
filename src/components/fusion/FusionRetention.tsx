import clsx from 'clsx'
import { HardDrive } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { buttonClass } from '@/components/ui/buttonClass'
import { Button, ErrorBanner, Field, ICON_SM, Input, Modal } from '@/components/ui/primitives'
import { api, ApiError, type FusionRetention, type FusionRetentionStore } from '@/lib/api'
import {
  daysText, daysThatFit, formatBytes, formChanges, formError, fullness, fullnessAdvice, initialForm, restartedBy, retentionVerdict, type RetentionFormState,
  usageText, volumeGiB,
} from '@/lib/fusionRetention'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** Reasons the control is not worth a line of its own: another organisation's FUSION, or no FUSION to set. */
const QUIET = new Set(['other-org', 'not-configured', 'not-installed', 'no-access', 'unavailable'])

const POLL_GROWING_MS = 5000
const POLL_STEADY_MS = 60_000

/**
 * How long FUSION keeps what it saved, and how big the volumes holding it are, with a way to change both. A longer retention may need a bigger
 * volume, so the two are set together: the card shows what each store takes now and how fast it grows, and says whether the retention asked for
 * fits. Growing a volume asks the cluster to expand it (which needs a storage class that allows expansion); the server does that first and
 * changes the retention only if it worked. `state` is FUSION's own state, which makes the card read again when it changes.
 */
export function FusionRetentionCard({ state }: { state: string }) {
  const conn = useServer((s) => s.conn)
  const [doc, setDoc] = useState<FusionRetention | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const alive = useRef(true)
  const newest = useRef(0)
  // Set back to true on every mount (StrictMode mounts twice; see FusionAccess).
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const load = useCallback(async () => {
    const c = conn()
    if (!c) return
    const mine = ++newest.current
    try {
      const d = await api.getFusionRetention(c)
      if (alive.current && mine === newest.current) {
        setDoc(d)
        setError('')
      }
    } catch (e) {
      if (alive.current && mine === newest.current) setError(e instanceof ApiError ? e.message : 'Could not read the retention.')
    }
  }, [conn])
  useEffect(() => {
    void load()
  }, [load, state])

  const growing = !!doc?.stores.some((s) => s.resizing)
  useVisiblePolling(() => void load(), doc?.available ? (growing ? POLL_GROWING_MS : POLL_STEADY_MS) : null)

  if (!doc) {
    return error ? <ErrorBanner className="mt-4">{error}</ErrorBanner> : null
  }
  if (!doc.available) {
    if (QUIET.has(doc.reason ?? '')) return null
    return (
      <section className="mt-4 border-t border-nb-850 pt-4" data-testid="fusion-retention" data-available="false">
        <Heading />
        <p className="mt-2 text-xs leading-relaxed text-nb-500" data-testid="fusion-retention-unavailable">{doc.message}</p>
      </section>
    )
  }
  return (
    <section className="mt-4 border-t border-nb-850 pt-4" data-testid="fusion-retention" data-available="true">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Heading />
        <span className="ml-auto">
          <Button size="sm" onClick={() => setEditing(true)} data-testid="fusion-retention-change">Change retention</Button>
        </span>
      </div>
      <p className="mt-2 text-xs leading-relaxed text-nb-500">
        How long each store keeps what it saved. A longer retention takes more room, so the volume can be grown with it; the card says whether the
        retention you choose fits.
      </p>
      <ul className="mt-3 divide-y divide-nb-850 rounded-md border border-nb-850 text-xs" data-testid="fusion-retention-list">
        {doc.stores.map((s) => <StoreRow key={s.component} s={s} />)}
      </ul>
      {doc.warnings?.map((w) => <ErrorBanner key={w} className="mt-2">{w}</ErrorBanner>)}
      {error && <ErrorBanner className="mt-2">{error}</ErrorBanner>}
      {editing && (
        <RetentionModal
          doc={doc}
          onClose={() => setEditing(false)}
          onSaved={(d) => {
            newest.current++
            setDoc(d)
            setEditing(false)
          }}
        />
      )}
    </section>
  )
}

function Heading() {
  return (
    <span className="flex items-center gap-1.5 text-sm font-medium text-nb-200">
      <HardDrive size={ICON_SM} className="text-nb-500" aria-hidden /> Retention
    </span>
  )
}

function StoreRow({ s }: { s: FusionRetentionStore }) {
  const sizeShort = s.sizeLimitDays !== undefined && s.sizeLimitDays < s.days
  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-3 py-2" data-testid={`fusion-retention-${s.component}`}>
      <span className="w-20 shrink-0 text-nb-300">{s.label}</span>
      <span className="text-nb-200" data-testid={`fusion-retention-days-${s.component}`}>
        keeps {daysText(s.days)}
        {!s.exactDays && <span className="ml-1 text-nb-500">({s.value})</span>}
      </span>
      <span className="min-w-0 flex-1 text-nb-500">
        {s.volumeKnown ? (
          <>
            {formatBytes(s.volumeBytes)} volume{s.storageClass ? ` (${s.storageClass})` : ''}, {usageText(s)}
          </>
        ) : (
          'volume not readable'
        )}
        {s.resizing && (
          <span className="ml-2 text-warn" data-testid={`fusion-retention-growing-${s.component}`}>
            growing to {formatBytes(s.volumeBytes)}{s.resizeNote ? ` - ${s.resizeNote}` : ''}
          </span>
        )}
      </span>
      {fullness(s) && (
        <span className={clsx('w-full', fullness(s)!.level === 'critical' ? 'text-bad' : 'text-warn')} data-testid={`fusion-retention-full-${s.component}`}>
          {fullnessAdvice(s)}
        </span>
      )}
      {s.volumeKnown && s.canGrow === false && (
        <span className="w-full text-nb-500" data-testid={`fusion-retention-fixed-${s.component}`}>
          This volume cannot be grown ({s.growNote ?? 'the cluster refuses'}), so only the days can be changed.
        </span>
      )}
      {s.sizeNotEnforced && (
        <span className="w-full text-nb-500" data-testid={`fusion-retention-nominal-${s.component}`}>
          This storage does not enforce the volume&apos;s size: {formatBytes(s.volumeBytes)} is what was asked for, and the node&apos;s disk is the real limit.
        </span>
      )}
      {sizeShort && (
        <span className="w-full text-warn" data-testid={`fusion-retention-sizecap-${s.component}`}>
          Its size limit keeps only about {daysText(Math.max(1, Math.floor(s.sizeLimitDays!)))} at today&apos;s growth, fewer than the {daysText(s.days)} set. Grow the volume to keep more.
        </span>
      )}
    </li>
  )
}

function RetentionModal({ doc, onClose, onSaved }: { doc: FusionRetention; onClose: () => void; onSaved: (d: FusionRetention) => void }) {
  const conn = useServer((s) => s.conn)
  const [form, setForm] = useState<RetentionFormState>(() => initialForm(doc.stores))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const changes = useMemo(() => formChanges(doc.stores, form), [doc.stores, form])
  const problem = doc.stores.map((s) => formError(s, form[s.component])).find((e) => e !== '') ?? ''
  const nothing = Object.keys(changes).length === 0
  const restarts = doc.running ? restartedBy(doc.stores, changes) : []

  const set = (c: FusionRetentionStore['component'], field: 'days' | 'gib', v: string) =>
    setForm((f) => ({ ...f, [c]: { ...f[c], [field]: v } }))

  const save = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      onSaved(await api.setFusionRetention(c, changes))
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not change the retention.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!busy}
      title="Change retention"
      description="How many days each store keeps, and the size of the volume it keeps them on."
      width="max-w-2xl"
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>Cancel</Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || nothing || problem !== ''} data-testid="fusion-retention-save">
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        {doc.stores.map((s) => {
          const f = form[s.component]
          const err = formError(s, f)
          const days = Number(f.days)
          const gib = s.volumeKnown ? Number(f.gib) : 0
          const verdict = !err && s.volumeKnown ? retentionVerdict(s, days, gib) : null
          const suggestion = s.canGrow === false ? undefined : verdict?.suggestGiB
          const fit = s.canGrow === false && verdict?.tone === 'warn' ? daysThatFit(s, gib) : null
          return (
            <fieldset key={s.component} className="space-y-2" data-testid={`fusion-retention-form-${s.component}`}>
              <legend className="text-sm font-medium text-nb-200">{s.label}</legend>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Keep for (days)" hint={`Between ${s.minDays} and ${s.maxDays}.`}>
                  <Input
                    type="number" inputMode="numeric" min={s.minDays} max={s.maxDays} value={f.days}
                    onChange={(e) => set(s.component, 'days', e.target.value)} data-testid={`fusion-retention-input-days-${s.component}`}
                  />
                </Field>
                {s.volumeKnown ? (
                  <Field label="Volume (GiB)" hint={`Now ${formatBytes(s.volumeBytes)}${s.storageClass ? ` on ${s.storageClass}` : ''}. ${s.canGrow === false ? 'This volume cannot be grown.' : 'It can grow, not shrink.'}`}>
                    <Input
                      type="number" inputMode="numeric" min={volumeGiB(s)} value={f.gib}
                      onChange={(e) => set(s.component, 'gib', e.target.value)} disabled={s.resizing || s.canGrow === false} data-testid={`fusion-retention-input-gib-${s.component}`}
                    />
                  </Field>
                ) : (
                  <p className="self-end text-xs text-nb-500">This store&apos;s volume could not be read, so its size cannot be changed here.</p>
                )}
              </div>
              {err && <p className="text-xs text-bad" role="alert" data-testid={`fusion-retention-error-${s.component}`}>{err}</p>}
              {verdict && (
                <p
                  className={clsx('text-xs leading-relaxed', verdict.tone === 'ok' ? 'text-ok' : verdict.tone === 'warn' ? 'text-warn' : 'text-nb-500')}
                  data-testid={`fusion-retention-verdict-${s.component}`}
                >
                  {verdict.text}
                  {fit !== null && fit < days && (
                    <button
                      type="button" className={clsx(buttonClass('secondary', 'sm'), 'ml-2 align-middle')}
                      onClick={() => set(s.component, 'days', String(fit))} data-testid={`fusion-retention-fit-${s.component}`}
                    >
                      Keep {daysText(fit)}
                    </button>
                  )}
                  {suggestion !== undefined && suggestion > gib && (
                    <button
                      type="button" className={clsx(buttonClass('secondary', 'sm'), 'ml-2 align-middle')}
                      onClick={() => set(s.component, 'gib', String(suggestion))} data-testid={`fusion-retention-suggest-${s.component}`}
                    >
                      Grow to {suggestion} GiB
                    </button>
                  )}
                </p>
              )}
              {s.sizeNotEnforced && <p className="text-xs text-nb-500">This storage does not enforce the volume&apos;s size, so the estimate above is against the size asked for; the node&apos;s disk is the real limit.</p>}
              {s.resizing && <p className="text-xs text-warn">The volume is still being grown from an earlier change. It can be grown again when that has finished.</p>}
            </fieldset>
          )
        })}
        {restarts.length > 0 && (
          <p className="text-xs leading-relaxed text-nb-500" data-testid="fusion-retention-restart-note">
            Saving restarts {restarts.join(' and ')} for a few seconds so it reads the new setting. Collectors keep what they cannot deliver meanwhile and catch up.
          </p>
        )}
        <p className="text-xs leading-relaxed text-nb-500">
          A bigger volume is asked of the cluster first and needs a storage class that allows expansion; if it does not, nothing is changed and the cluster&apos;s
          reason is shown here. Shortening a retention frees room only as the stores clean up old data, which they do in the background.
        </p>
        {error && <ErrorBanner data-testid="fusion-retention-save-error">{error}</ErrorBanner>}
      </div>
    </Modal>
  )
}

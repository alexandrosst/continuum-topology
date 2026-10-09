import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ConfirmModal } from '@/components/forms'
import { FusionSection, ROWS } from '@/components/fusion/FusionSection'
import { DiskBar, FillsIn, KeptBar } from '@/components/fusion/StoreBars'
import { buttonClass } from '@/components/ui/buttonClass'
import { Button, ErrorBanner, Field, Input, Modal } from '@/components/ui/primitives'
import { api, ApiError, type FusionRetention, type FusionRetentionStore } from '@/lib/api'
import {
  daysText, daysThatFit, formatBytes, formChanges, formError, fullness, fullnessAdvice, initialForm, restartedBy, retentionVerdict, type RetentionFormState, shortened,
  sizeCapped, volumeGiB,
} from '@/lib/fusionRetention'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** Reasons the control is not worth a line of its own: another organisation's FUSION, or no FUSION to set. */
const QUIET = new Set(['other-org', 'not-configured', 'not-installed', 'no-access', 'unavailable'])

const POLL_GROWING_MS = 5000
const POLL_STEADY_MS = 60_000

/**
 * How long FUSION keeps what it saved, and how big the volumes holding it are, with a way to change both. A longer retention may need a bigger
 * volume, so the two are set together: the block shows what each store takes now and how fast it grows, and says whether the retention asked for
 * fits. Growing a volume asks the cluster to expand it (which needs a storage class that allows expansion); the server does that first and
 * changes the retention only if it worked. `state` is FUSION's own state, which makes it read again when it changes.
 */
export function useFusionRetention(state: string, enabled = true) {
  const conn = useServer((s) => s.conn)
  const [doc, setDoc] = useState<FusionRetention | null>(null)
  const [error, setError] = useState('')
  const alive = useRef(true)
  const newest = useRef(0)
  // Set back to true on every mount (StrictMode mounts twice; see FusionAccess).
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const load = useCallback(async () => {
    const c = conn()
    if (!c || !enabled) return
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
  }, [conn, enabled])
  useEffect(() => {
    void load()
  }, [load, state])

  const growing = !!doc?.stores.some((s) => s.resizing)
  useVisiblePolling(() => void load(), doc?.available ? (growing ? POLL_GROWING_MS : POLL_STEADY_MS) : null)
  /** The server's answer to a change is the freshest there is: reads that left before it are older. */
  const accept = (d: FusionRetention) => {
    newest.current++
    setDoc(d)
  }
  return { doc, error, accept }
}

const WHAT = 'How long each store keeps what it saved, and the volume it keeps it on. A longer retention takes more room, so the volume can be grown with it.'

export function FusionRetentionCard({ retention: { doc, error, accept } }: { retention: ReturnType<typeof useFusionRetention> }) {
  const [editing, setEditing] = useState(false)

  if (!doc) {
    return error ? <ErrorBanner className="mb-8">{error}</ErrorBanner> : null
  }
  if (!doc.available) {
    if (QUIET.has(doc.reason ?? '')) return null
    return (
      <FusionSection title="Retention" description={WHAT} testId="fusion-retention">
        <p className="text-sm text-nb-500" data-testid="fusion-retention-unavailable">{doc.message}</p>
      </FusionSection>
    )
  }
  return (
    <FusionSection title="Retention" description={WHAT} testId="fusion-retention" actions={<Button size="sm" onClick={() => setEditing(true)} data-testid="fusion-retention-change">Change retention</Button>}>
      <ul className={ROWS} data-testid="fusion-retention-list">
        {doc.stores.map((s) => <StoreRow key={s.component} s={s} />)}
      </ul>
      {doc.warnings?.map((w) => <ErrorBanner key={w} className="mt-2">{w}</ErrorBanner>)}
      {error && <ErrorBanner className="mt-2">{error}</ErrorBanner>}
      {editing && <RetentionModal doc={doc} onClose={() => setEditing(false)} onSaved={(d) => { accept(d); setEditing(false) }} />}
    </FusionSection>
  )
}

function StoreRow({ s }: { s: FusionRetentionStore }) {
  return (
    <li className="flex flex-wrap items-center gap-x-5 gap-y-1.5 px-4 py-3" data-testid={`fusion-retention-${s.component}`}>
      <span className="w-24 shrink-0 text-nb-300">{s.label}</span>
      <span className="w-28 text-nb-200" data-testid={`fusion-retention-days-${s.component}`}>
        keeps {daysText(s.days)}
        {!s.exactDays && <span className="ml-1 text-nb-500">({s.value})</span>}
      </span>
      <KeptBar s={s} />
      {s.volumeKnown ? <DiskBar s={s} /> : <span className="text-xs text-nb-500">volume not readable</span>}
      <FillsIn s={s} />
      {s.resizing && (
        <span className="text-xs text-warn" data-testid={`fusion-retention-growing-${s.component}`}>
          growing to {formatBytes(s.volumeBytes)}{s.resizeNote ? ` - ${s.resizeNote}` : ''}
        </span>
      )}
      {fullness(s) && (
        <span className={clsx('w-full text-xs', fullness(s)!.level === 'critical' ? 'text-bad' : 'text-warn')} data-testid={`fusion-retention-full-${s.component}`}>
          {fullnessAdvice(s)}
        </span>
      )}
      {s.volumeKnown && s.canGrow === false && (
        <span className="w-full text-xs text-nb-500" data-testid={`fusion-retention-fixed-${s.component}`}>
          This volume cannot be grown ({s.growNote ?? 'the cluster refuses'}), so only the days can be changed.
        </span>
      )}
      {s.sizeNotEnforced && (
        <span className="w-full text-xs text-nb-500" data-testid={`fusion-retention-nominal-${s.component}`}>
          This storage does not enforce the volume&apos;s size: {formatBytes(s.volumeBytes)} is what was asked for, and the node&apos;s disk is the real limit.
        </span>
      )}
      {sizeCapped(s) && (
        <span className="w-full text-xs text-warn" data-testid={`fusion-retention-sizecap-${s.component}`}>
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
  // Shortening a retention deletes what is older than the new setting, for good: said, and asked about, before it is done.
  const cut = shortened(doc.stores, changes)
  const [confirming, setConfirming] = useState(false)

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
    <>
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
          <Button variant="primary" onClick={() => (cut.length > 0 ? setConfirming(true) : void save())} disabled={busy || nothing || problem !== ''} data-testid="fusion-retention-save">
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
    {confirming && (
      <ConfirmModal
        title="Delete older data?"
        message={cut.map((c) => `${c.label} will keep ${daysText(c.to)} instead of ${daysText(c.from)}: what is older than ${daysText(c.to)} is deleted as the store cleans up, in the background, and cannot be brought back.`).join(' ')}
        confirmLabel="Keep fewer days"
        onConfirm={() => void save()}
        onClose={() => setConfirming(false)}
      />
    )}
    </>
  )
}

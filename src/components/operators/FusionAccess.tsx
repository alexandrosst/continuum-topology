import { BookOpen, KeyRound, Plus } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { buttonClass } from '@/components/ui/buttonClass'
import { ConfirmModal } from '@/components/forms'
import { Button, CheckboxList, CopyValue, ErrorBanner, Field, ICON_SM, Input, Modal, Select, TagsInput } from '@/components/ui/primitives'
import { shArg, shQuote } from '@/lib/install'
import { useHoldReload } from '@/lib/useHoldReload'
import { api, ApiError, type CreatedFusionAccessToken, type FusionAccessToken, type FusionSignal } from '@/lib/api'
import { ago } from '@/lib/observed'
import { useServer } from '@/store/server'

const SIGNALS: { value: FusionSignal; label: string; hint: string }[] = [
  { value: 'metrics', label: 'Metrics', hint: 'Prometheus - series over time' },
  { value: 'logs', label: 'Logs', hint: 'Loki - log lines, with their trace and span ids' },
  { value: 'traces', label: 'Traces', hint: 'Tempo - spans, and the trace joined to its logs and metrics' },
]

/** Where the data API's reference page is: the same server, so the same address the rest of the app talks to. It needs no sign-in (it only describes the API). */
export function fusionDocsUrl(base: string): string {
  return `${base.replace(/\/+$/, '')}/api/v1/fusion/docs`
}

const EXPIRY_DAYS = [30, 90, 180, 365]

/** The curl line that tries a freshly minted token. The header keeps its double quotes while the token is plain (the usual case); anything
 *  else in it is single-quoted, so what the server hands out can never end the line early. */
export function fusionStatusCurl(token: string, base: string): string {
  const header = `Authorization: Bearer ${token}`
  return `curl -H ${/^[A-Za-z0-9_.~+/=: -]+$/.test(header) ? `"${header}"` : shQuote(header)} ${shArg(`${base}/api/v1/fusion/status`)}`
}

/** "expires in 12 days", "expires today", "expired", from the token's own expiry time. */
export function expiryText(iso: string, now = Date.now()): string {
  const ms = new Date(iso).getTime() - now
  if (ms <= 0) return 'expired'
  const days = Math.ceil(ms / 86_400_000)
  return days <= 1 ? 'expires today' : `expires in ${days} days`
}

const chip = 'inline-flex items-center rounded border border-nb-800 px-1.5 py-px text-[11px] leading-4 text-nb-400'

function scopeChips(t: FusionAccessToken) {
  const signals = t.signals.length === SIGNALS.length ? 'all signals' : t.signals.join(' + ')
  return [signals, t.namespaces.length ? `namespaces: ${t.namespaces.join(', ')}` : 'all namespaces', ...(t.clusters.length ? [`clusters: ${t.clusters.join(', ')}`] : [])]
}

/**
 * Who else may read what FUSION saved: the access tokens the shared data API accepts. A token is read-only, limited to
 * the signal types and namespaces it is given, and expires; its secret is shown once, when it is made. Shown on the
 * FUSION card of the server's main organisation, whatever state the switch is in (the API says plainly when the stores
 * are off).
 */
export function FusionAccess() {
  const conn = useServer((s) => s.conn)
  const [tokens, setTokens] = useState<FusionAccessToken[] | null>(null)
  const [error, setError] = useState('')
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<FusionAccessToken | null>(null)
  const alive = useRef(true)
  // Set back to true on every mount, not only cleared on unmount: React's StrictMode (main.tsx) mounts, unmounts and mounts again, and a flag only
  // ever cleared would stay false for the real mount, so the token list would never be filled in.
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const load = useCallback(async () => {
    const c = conn()
    if (!c) return
    try {
      const list = await api.listFusionTokens(c)
      if (alive.current) {
        setTokens(list)
        setError('')
      }
    } catch (e) {
      if (alive.current) setError(e instanceof ApiError ? e.message : 'Could not load the access tokens.')
    }
  }, [conn])
  useEffect(() => {
    void load()
  }, [load])

  const revoke = async (t: FusionAccessToken) => {
    const c = conn()
    if (!c) return
    try {
      await api.revokeFusionToken(c, t.id)
      await load()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not revoke the token.')
    }
  }

  return (
    <section className="mt-4 border-t border-nb-850 pt-4" data-testid="fusion-access">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="flex items-center gap-1.5 text-sm font-medium text-nb-200">
          <KeyRound size={ICON_SM} className="text-nb-500" aria-hidden /> Data access
        </span>
        <span className="ml-auto flex flex-wrap items-center gap-2">
          <a className={buttonClass('secondary', 'sm')} href={fusionDocsUrl(conn()?.url || window.location.origin)} target="_blank" rel="noreferrer" data-testid="fusion-api-docs">
            <BookOpen size={ICON_SM} aria-hidden /> API reference
          </a>
          <Button size="sm" onClick={() => setCreating(true)} data-testid="fusion-token-new">
            <Plus size={ICON_SM} aria-hidden /> New access token
          </Button>
        </span>
      </div>
      <p className="mt-2 text-xs leading-relaxed text-nb-500">
        Another system reads what FUSION saved - metrics, logs and traces, separately or joined around a trace - with a token. A token is read-only, limited to
        the signals and namespaces you give it, and expires. The stores themselves are never exposed; every read goes through this server. The API reference
        lists every call and lets you try it.
      </p>
      {error && <ErrorBanner className="mt-2">{error}</ErrorBanner>}
      {tokens && tokens.length === 0 && !error && <p className="mt-3 text-xs text-nb-500" data-testid="fusion-token-empty">No access tokens yet.</p>}
      {tokens && tokens.length > 0 && (
        <ul className="mt-3 divide-y divide-nb-850 rounded-md border border-nb-850" data-testid="fusion-token-list">
          {tokens.map((t) => (
            <li key={t.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2" data-testid={`fusion-token-${t.id}`}>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-nb-200">{t.name}</div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {scopeChips(t).map((c) => <span key={c} className={chip}>{c}</span>)}
                </div>
                <div className="mt-1 text-[11px] text-nb-500">
                  {expiryText(t.expiresAt)}, {t.lastUsedAt ? `last used ${ago(t.lastUsedAt)}` : 'never used'}
                </div>
              </div>
              <Button size="sm" variant="danger" onClick={() => setRevoking(t)} aria-label={`Revoke ${t.name}`}>Revoke</Button>
            </li>
          ))}
        </ul>
      )}
      {creating && <NewTokenModal onClose={() => setCreating(false)} onCreated={() => void load()} />}
      {revoking && (
        <ConfirmModal
          title={`Revoke ${revoking.name}?`}
          message="Whatever uses this token stops being able to read FUSION at once. This cannot be undone; make a new token to give it access again."
          confirmLabel="Revoke"
          onConfirm={() => void revoke(revoking)}
          onClose={() => setRevoking(null)}
        />
      )}
    </section>
  )
}

function NewTokenModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const conn = useServer((s) => s.conn)
  const [name, setName] = useState('')
  const [signals, setSignals] = useState<FusionSignal[]>(SIGNALS.map((s) => s.value))
  const [namespaces, setNamespaces] = useState<string[]>([])
  const [clusters, setClusters] = useState<string[]>([])
  const [days, setDays] = useState('90')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState<CreatedFusionAccessToken | null>(null)
  // The token is shown once: while it is on screen nothing may reload the page under it (see the created view's Modal).
  useHoldReload(created !== null)

  const create = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      setCreated(await api.createFusionToken(c, { name: name.trim(), signals, namespaces, clusters, expiresInDays: Number(days) }))
      onCreated()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not create the token.')
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    const base = conn()?.url || window.location.origin
    return (
      <Modal open onClose={onClose} dismissible={false} title="Access token created" width="max-w-lg" footer={<Button variant="primary" onClick={onClose} data-testid="fusion-token-done">Done</Button>}>
        <div className="space-y-3">
          <div className="space-y-2 rounded-md border border-accent/30 bg-accent-soft p-3" data-testid="fusion-token-secret">
            <p className="text-xs text-nb-300">
              Copy <strong className="text-nb-200">{created.details.name}</strong> now - for your own safety, it won&apos;t be shown again.
            </p>
            <CopyValue value={created.token} testId="fusion-token-value" />
          </div>
          <div>
            <p className="text-xs text-nb-500">Try it:</p>
            <CopyCommand text={fusionStatusCurl(created.token, base)} testId="fusion-token-curl" />
          </div>
          <p className="text-xs leading-relaxed text-nb-500">
            {expiryText(created.details.expiresAt)}. Read a trace with its logs and metrics joined:{' '}
            <code className="font-mono text-nb-400">/api/v1/fusion/traces/&lt;trace-id&gt;?fused=true</code>.{' '}
            <a className="text-accent underline" href={fusionDocsUrl(base)} target="_blank" rel="noreferrer" data-testid="fusion-token-docs">Every call is in the API reference</a>.
          </p>
        </div>
      </Modal>
    )
  }

  return (
    <Modal
      open
      onClose={onClose}
      // Once the request is out the token exists and is on its way to this dialog: closing now would lose it.
      dismissible={!busy}
      title="New access token"
      description="A read-only credential for another system. It sees only what you allow here."
      width="max-w-lg"
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>Cancel</Button>
          <Button variant="primary" onClick={() => void create()} disabled={busy || name.trim() === '' || signals.length === 0} data-testid="fusion-token-create">
            {busy ? 'Creating…' : 'Create token'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Name" hint="The system or script it is for, so you can tell tokens apart later.">
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="Decision engine" maxLength={64} data-testid="fusion-token-name" />
        </Field>
        <Field label="Can read">
          <CheckboxList options={SIGNALS} value={signals} onChange={(v) => setSignals(SIGNALS.map((s) => s.value).filter((s) => v.includes(s)))} />
        </Field>
        <Field label="Namespaces" hint="Only telemetry from these namespaces is visible to it. Empty: every namespace. Telemetry with no namespace (node metrics) is then hidden.">
          <TagsInput value={namespaces} onChange={setNamespaces} placeholder="shop, payments" data-testid="fusion-token-namespaces" />
        </Field>
        <Field label="Clusters" hint="Optionally limit it to these cluster ids too. Empty: every cluster.">
          <TagsInput value={clusters} onChange={setClusters} placeholder="cl-1a2b3c4d5e" data-testid="fusion-token-clusters" />
        </Field>
        <Field label="Expires">
          <Select value={days} onChange={(e) => setDays(e.target.value)} data-testid="fusion-token-days">
            {EXPIRY_DAYS.map((d) => <option key={d} value={String(d)}>{`After ${d} days`}</option>)}
          </Select>
        </Field>
        {(namespaces.length > 0 || clusters.length > 0) && (
          <p className="text-xs leading-relaxed text-nb-500">
            A token with a namespace or cluster limit can use the structured filters only. Raw PromQL, LogQL and TraceQL need a token with no such limit, because a limit cannot be enforced on a query written by hand.
          </p>
        )}
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </div>
    </Modal>
  )
}

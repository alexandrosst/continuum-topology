import clsx from 'clsx'
import { ChevronDown, ExternalLink, Lock, Plus, Rocket, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { Button, CopyButton, Field, ICON_MD, ICON_SM, Input, Modal, Select } from '@/components/ui/primitives'
import { api, atLeast, ApiError, type GatewayTokenStatus, type MintedGatewayToken } from '@/lib/api'
import { effectiveAllowedBackendKinds, KNOWN_BACKEND_KINDS, type AppSettings, type QuickStartBackend, type QuickStartKind } from '@/lib/history'
import type { ProcessorEntry } from '@/lib/processorCatalog'
import { QUICK_START_BACKENDS, quickStartSpec } from '@/lib/quickStartBackends'
import { gatewayManifest, gatewayPortForward, hasGatewayManifest } from '@/lib/quickStartGateway'
import type { Modality } from '@/lib/consent'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import TelemetryBackendWizard from './TelemetryBackendWizard'

const KIND_LABEL: Record<QuickStartKind, string> = { jaeger: 'Jaeger', zipkin: 'Zipkin', prometheus: 'Prometheus', loki: 'Loki', custom: 'Custom' }

/**
 * "Don't have a backend yet?" - sits right under the telemetry destination field (see TelemetryFields.tsx)
 * and offers to quick-start one, for whichever of Jaeger/Zipkin/Prometheus/Loki matches a modality that's actually
 * turned on. This never deploys or dials anything itself: picking one produces a `helm install` command
 * to run by hand, the same "mechanism only" shape as every other install command in this app
 * (ConnectClusterWizard, regional operators). Once a person says it's installed, it's remembered in
 * Settings (see QuickStartBackend) so this renders a compact summary instead of the setup form next time,
 * with "Open <tool>" once a reachable URL is known and "Use as destination" to fill the field above.
 *
 * Which of the three catalog kinds (plus "custom", a backend with no catalog entry of its own - see
 * CustomBackends) an organisation may add at all is a per-org allow-list (settings.allowedBackendKinds,
 * see effectiveAllowedBackendKinds) an administrator manages right here via AllowedKindsControl.
 */
export default function QuickStartBackends({ enabledModalities, onUseAsDestination, currentDestination, extraProcessors = [] }: {
  enabledModalities: Set<Modality>
  onUseAsDestination: (exportEndpoint: string, exportProtocol: 'grpc' | 'http') => void
  /** The destination (if any) already set on the telemetry form this sits under - used only by the guided
   *  wizard's own compatibility step (see TelemetryBackendWizard), to flag that using a quick-start backend
   *  as the destination would replace it, and whether doing so also switches the protocol. Optional so
   *  existing callers/tests that only care about the flat per-kind disclosures need not pass it. */
  currentDestination?: { endpoint: string; protocol: 'grpc' | 'http' }
  /** Extra OTel processors already configured on the telemetry form - same reasoning: only the guided
   *  wizard's compatibility step reads these, to flag one shaped for a signal this backend won't carry. */
  extraProcessors?: ProcessorEntry[]
}) {
  const conn = useConn()
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const { settings, save, error } = useSettings()
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState<QuickStartKind | null>(null)
  const [addingCustom, setAddingCustom] = useState(false)
  const [wizardOpen, setWizardOpen] = useState(false)
  // Per-backend gateway token state (Part B/C): whether one has been minted (never the secret - see
  // api.gatewayTokenStatus), and the plaintext from the most recent mint, shown exactly once.
  const [gatewayStatus, setGatewayStatus] = useState<Record<string, GatewayTokenStatus>>({})
  const [minting, setMinting] = useState<string | null>(null)
  const [mintError, setMintError] = useState<string | null>(null)
  const [minted, setMinted] = useState<{ backend: QuickStartBackend; token: MintedGatewayToken } | null>(null)

  // Prefetches each gated-eligible saved backend's gateway token status (never the secret) once, so
  // "Generate access token" can read "regenerate" and the gated note on "Open <tool>" can show up without
  // a person first opening anything. Keyed on the ids themselves (joined), not the array, so a re-render
  // that leaves the same set of backends in place does not refetch. Placed before either early return
  // below so this effect is always called in the same order, whatever `enabledModalities` turns out to be.
  const gatewayEligibleIds = (settings?.quickStartBackends ?? []).filter((b) => hasGatewayManifest(b.kind)).map((b) => b.id)
  const gatewayEligibleKey = gatewayEligibleIds.join(',')
  useEffect(() => {
    if (!gatewayEligibleKey) return
    let cancelled = false
    for (const id of gatewayEligibleKey.split(',')) {
      void api.gatewayTokenStatus(conn, id).then((st) => {
        if (!cancelled) setGatewayStatus((prev) => ({ ...prev, [id]: st }))
      }).catch(() => {
        // Not fatal - "Generate access token" still works, it just starts from "no status known" instead
        // of "known inactive". Nothing to show a person for a background prefetch failing quietly.
      })
    }
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn, gatewayEligibleKey])

  // With no telemetry signal on at all there is no destination field this sits under in the first
  // place, so there is nothing to quick-start and nowhere sensible to show the allow-list control either
  // - same as every modality simply having no matching backend, which is what this used to reduce to
  // before "custom"/the allow-list existed. Checked before touching `settings` at all, deliberately: a
  // caller may render this before settings has finished loading.
  if (enabledModalities.size === 0) return null

  const allowed = effectiveAllowedBackendKinds(settings.allowedBackendKinds)
  const relevant = QUICK_START_BACKENDS.filter((s) => enabledModalities.has(s.modality) && allowed.includes(s.kind))
  const customBackends = settings.quickStartBackends.filter((b) => b.kind === 'custom')
  const customRelevant = customBackends.filter((b) => enabledModalities.has(b.modality))
  const customAllowed = allowed.includes('custom')

  // A non-administrator with nothing relevant to see (no matching built-in backend, no matching custom
  // one) gets nothing - same as before this allow-list existed. An administrator always sees at least the
  // allow-list control, even with nothing else to show, since this is the only place to manage it.
  if (!admin && relevant.length === 0 && customRelevant.length === 0) return null

  const writeSettings = async (patch: Partial<AppSettings>) => {
    setBusy(true)
    try {
      return await save(conn, { ...settings, ...patch })
    } finally {
      setBusy(false)
    }
  }
  const write = (backends: QuickStartBackend[]) => writeSettings({ quickStartBackends: backends })

  // Mints a fresh gateway token for one backend (Part B) and holds the plaintext in `minted` for the
  // reveal-once dialog below - never written to Settings, never requested again after this response.
  const generateToken = async (backend: QuickStartBackend) => {
    setMinting(backend.id)
    setMintError(null)
    try {
      const token = await api.mintGatewayToken(conn, backend.id)
      setGatewayStatus((prev) => ({ ...prev, [backend.id]: { active: true, expired: false, createdAt: token.createdAt, expiresAt: token.expiresAt } }))
      setMinted({ backend, token })
    } catch (e) {
      setMintError(e instanceof ApiError ? e.message : 'Could not generate an access token.')
    } finally {
      setMinting(null)
    }
  }

  return (
    <div className="sm:col-span-2 rounded-lg border border-dashed border-nb-850 bg-nb-930/60 p-3">
      {error && <p className="mb-2 text-xs text-bad" role="alert">{error}</p>}
      {mintError && <p className="mb-2 text-xs text-bad" role="alert">{mintError}</p>}
      {admin && <AllowedKindsControl allowed={allowed} busy={busy} onChange={(kinds) => writeSettings({ allowedBackendKinds: kinds })} />}
      <div className="mb-2 flex items-center justify-between gap-2">
        <p className="text-xs text-nb-500">Pick a kind below, or walk through it step by step - sensible defaults, then a check for anything already configured that won't play well with it.</p>
        <Button type="button" size="sm" variant="ghost" onClick={() => setWizardOpen(true)} data-testid="quickstart-guided-setup">
          <Rocket size={ICON_SM} /> Guided setup
        </Button>
      </div>
      <div className="flex flex-col gap-2">
        {relevant.map((spec) => {
          const saved = settings.quickStartBackends.find((b) => b.kind === spec.kind)
          if (saved) {
            return (
              <SavedBackend
                key={spec.kind}
                backend={saved}
                admin={admin}
                busy={busy}
                canUse={enabledModalities.size === 1}
                gatewayStatus={gatewayStatus[saved.id]}
                minting={minting === saved.id}
                onUse={() => onUseAsDestination(spec.exportEndpoint(saved.namespace), spec.exportProtocol)}
                onSaveUrl={(url) => write(settings.quickStartBackends.map((b) => (b.id === saved.id ? { ...b, toolUrl: url } : b)))}
                onSaveRetention={(retention) => write(settings.quickStartBackends.map((b) => (b.id === saved.id ? { ...b, retention } : b)))}
                onRemove={() => write(settings.quickStartBackends.filter((b) => b.id !== saved.id))}
                onGenerateToken={() => void generateToken(saved)}
              />
            )
          }
          return (
            <SetupBackend
              key={spec.kind}
              kind={spec.kind}
              admin={admin}
              busy={busy}
              expanded={open === spec.kind}
              onToggle={() => setOpen(open === spec.kind ? null : spec.kind)}
              onSave={(namespace, retention) => {
                const rec: QuickStartBackend = { id: '', kind: spec.kind, modality: spec.modality, namespace, retention, label: spec.label }
                void write([...settings.quickStartBackends, rec]).then((ok) => {
                  if (ok) setOpen(null)
                })
              }}
            />
          )
        })}
        {customAllowed && (
          <CustomBackends
            backends={admin ? customBackends : customRelevant}
            admin={admin}
            busy={busy}
            enabledModalities={enabledModalities}
            adding={addingCustom}
            onToggleAdd={() => setAddingCustom((v) => !v)}
            onAdd={(rec) => {
              void write([...settings.quickStartBackends, rec]).then((ok) => {
                if (ok) setAddingCustom(false)
              })
            }}
            onSaveUrl={(id, url) => write(settings.quickStartBackends.map((b) => (b.id === id ? { ...b, toolUrl: url } : b)))}
            onRemove={(id) => write(settings.quickStartBackends.filter((b) => b.id !== id))}
          />
        )}
      </div>
      {minted && <GatewayTokenCreated minted={minted} onClose={() => setMinted(null)} />}
      <TelemetryBackendWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        allowedKinds={allowed}
        enabledModalities={enabledModalities}
        existingBackends={settings.quickStartBackends}
        currentEndpoint={currentDestination?.endpoint ?? ''}
        currentProtocol={currentDestination?.protocol ?? 'grpc'}
        extraProcessors={extraProcessors}
        admin={admin}
        busy={busy}
        error={error}
        onSave={(rec) => {
          void write([...settings.quickStartBackends, rec]).then((ok) => {
            if (ok) setWizardOpen(false)
          })
        }}
      />
    </div>
  )
}

/** Which quick-start backend kinds this organisation allows (settings.allowedBackendKinds) - admin-only,
 * the same role gate every other write in this component already applies inline. Toggling flips the
 * *effective* set (defaults included), so unchecking one of the defaults persists the rest explicitly
 * rather than an empty "nothing allowed" list, and checking "custom" for the first time keeps the three
 * built-ins enabled alongside it. */
export function AllowedKindsControl({ allowed, busy, onChange }: { allowed: QuickStartKind[]; busy: boolean; onChange: (kinds: QuickStartKind[]) => void }) {
  const toggle = (kind: QuickStartKind) => onChange(allowed.includes(kind) ? allowed.filter((k) => k !== kind) : [...allowed, kind])
  return (
    <div className="mb-3 flex flex-wrap items-center gap-3 border-b border-nb-850 pb-3 text-xs">
      <span className="font-medium uppercase tracking-wide text-nb-500">Backend kinds enabled for this org</span>
      {KNOWN_BACKEND_KINDS.map((kind) => (
        <label key={kind} className="flex items-center gap-1.5 text-nb-300">
          <input
            type="checkbox"
            className="size-3.5 accent-[var(--color-accent)]"
            checked={allowed.includes(kind)}
            disabled={busy}
            onChange={() => toggle(kind)}
            data-testid={`allowed-kind-${kind}`}
          />
          {KIND_LABEL[kind]}
        </label>
      ))}
    </div>
  )
}

function SavedBackend({ backend, admin, busy, canUse, gatewayStatus, minting, onUse, onSaveUrl, onSaveRetention, onRemove, onGenerateToken }: {
  backend: QuickStartBackend
  admin: boolean
  busy: boolean
  /** False once a modality this backend cannot carry is also turned on (see QuickStartBackends' own
   *  enabledModalities check) - "Use as destination" is disabled rather than silently misconfiguring the
   *  one exportEndpoint every signal shares, the same guard TelemetryFields' own preset picker applies. */
  canUse: boolean
  /** Whether a Part B gateway token exists for this backend, and whether it has lapsed - undefined while
   *  the status prefetch (see QuickStartBackends' own effect) hasn't resolved yet, which reads the same as
   *  "none known" and just hides the gated note until it has. */
  gatewayStatus?: GatewayTokenStatus
  minting: boolean
  onUse: () => void
  onSaveUrl: (url: string) => void
  onSaveRetention: (retention: string) => void
  onRemove: () => void
  onGenerateToken: () => void
}) {
  const spec = quickStartSpec(backend.kind)
  const [url, setUrl] = useState(backend.toolUrl ?? '')
  const [editingRetention, setEditingRetention] = useState(false)
  const [retention, setRetention] = useState(backend.retention)
  const gated = hasGatewayManifest(backend.kind)
  const active = gated && gatewayStatus?.active && !gatewayStatus.expired
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="font-medium text-nb-300">{backend.label}</span>
      <span className="font-mono text-xs text-nb-500">{backend.namespace} ·</span>
      {editingRetention ? (
        <form
          className="flex items-center gap-1"
          onSubmit={(e) => {
            e.preventDefault()
            if (retention.trim()) {
              onSaveRetention(retention.trim())
              setEditingRetention(false)
            }
          }}
        >
          <Input value={retention} onChange={(e) => setRetention(e.target.value)} className="h-7 w-20 text-xs" aria-label={`${backend.label} retention`} />
          <Button type="submit" size="sm" disabled={busy || !retention.trim()}>Save</Button>
        </form>
      ) : admin ? (
        <button type="button" className="font-mono text-xs text-nb-500 underline-offset-2 hover:text-nb-300 hover:underline" onClick={() => setEditingRetention(true)} title="Change retention">
          {backend.retention}
        </button>
      ) : (
        <span className="font-mono text-xs text-nb-500">{backend.retention}</span>
      )}
      <div className="ml-auto flex items-center gap-2">
        {backend.toolUrl ? (
          <span className="inline-flex items-center gap-1">
            <a href={backend.toolUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-accent hover:underline">
              <ExternalLink size={ICON_SM} aria-hidden /> Open {spec.label.split(' ')[0]}
            </a>
            {active && (
              <span
                className="inline-flex cursor-help text-nb-500"
                title="Gated by the gateway below - a plain browser tab gets 401 here now. Send Authorization: Bearer <token> (curl, httpie, a header-injecting extension) - the token itself was only ever shown once, when generated."
                aria-label="Gated"
              >
                <Lock size={ICON_SM} aria-hidden />
              </span>
            )}
          </span>
        ) : admin ? (
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault()
              if (url.trim()) onSaveUrl(url.trim())
            }}
          >
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://localhost:16686" className="h-7 w-44 text-xs" aria-label={`${backend.label} tool URL`} />
            <Button type="submit" size="sm" disabled={busy || !url.trim()}>Save URL</Button>
          </form>
        ) : null}
        {admin && gated && (
          <Button type="button" size="sm" variant="ghost" onClick={onGenerateToken} disabled={busy || minting} data-testid={`quickstart-gateway-token-${backend.kind}`}>
            <Lock size={ICON_SM} /> {active ? 'Regenerate access token' : 'Generate access token'}
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={onUse}
          disabled={busy || !canUse}
          title={canUse ? undefined : `${spec.label} only carries ${spec.modality} - turn off the other signals above first.`}
        >
          Use as destination
        </Button>
        {admin && (
          <Button type="button" size="sm" variant="ghost" onClick={onRemove} disabled={busy} aria-label={`Forget ${backend.label}`}>
            <Trash2 size={ICON_SM} />
          </Button>
        )}
      </div>
    </div>
  )
}

/**
 * The gateway token's plaintext, shown exactly once - same "shown only now, gone forever" convention as
 * RegionalOperatorsPage's own OperatorCreated dialog for the receiver token. Alongside it, the Part C
 * manifest that checks that exact token, as a second `kubectl apply` command block next to the backend's
 * own `helm install` one, and the gateway's own port-forward line in place of the raw tool's.
 */
function GatewayTokenCreated({ minted, onClose }: { minted: { backend: QuickStartBackend; token: MintedGatewayToken }; onClose: () => void }) {
  const { backend, token } = minted
  const manifest = gatewayManifest(backend, token.token)
  return (
    <Modal open onClose={onClose} title={`${backend.label} access token created`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <p className="text-sm text-nb-400">
        This token is shown only now - the server keeps only its hash. It expires {new Date(token.expiresAt).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}; generate a new one any time (it replaces this one once applied below).
      </p>
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">The token itself</div>
        <CopyCommand text={token.token} />
      </div>
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">Apply the gateway (gates {backend.label} behind the token above - checked entirely by this, never by Continuum)</div>
        <CopyCommand text={manifest} />
      </div>
      <div className="mt-3">
        <div className="mb-1 text-xs text-nb-500">Then, to reach it through the gateway</div>
        <CopyCommand text={gatewayPortForward(backend)} />
      </div>
      <p className="mt-3 text-xs text-nb-500">
        A plain browser tab cannot send the Authorization header this needs - use curl, httpie, or a header-injecting extension, or front the gateway Service with your own ingress/header-injecting proxy.
      </p>
    </Modal>
  )
}

function SetupBackend({ kind, admin, busy, expanded, onToggle, onSave }: {
  kind: QuickStartKind
  admin: boolean
  busy: boolean
  expanded: boolean
  onToggle: () => void
  onSave: (namespace: string, retention: string) => void
}) {
  const spec = quickStartSpec(kind)
  const [namespace, setNamespace] = useState(spec.defaultNamespace)
  const [retention, setRetention] = useState(spec.defaultRetention)

  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 text-left text-sm text-nb-300 hover:text-nb-200"
        data-testid={`quickstart-toggle-${kind}`}
      >
        <Rocket size={ICON_SM} className="text-accent" aria-hidden />
        <span>Don&apos;t have a backend for {spec.modality} yet? Quick-start {spec.label}.</span>
        <ChevronDown size={ICON_SM} className={clsx('ml-auto text-nb-500 transition-transform', expanded && 'rotate-180')} aria-hidden />
      </button>
      {expanded && (
        <div className="mt-3 flex flex-col gap-3 border-t border-nb-850 pt-3">
          <p className="text-xs text-nb-500">
            Installs the upstream {spec.label.replace(/ \(.*\)/, '')} chart in your own cluster - a one-line command to run yourself, same as every other install command here. Nothing is deployed for you. This gets something reachable fast for a single cluster; for a real fleet, point every agent at a real, externally reachable backend instead.
          </p>
          {!admin ? (
            <p className="text-xs text-nb-500">Only administrators can set this up.</p>
          ) : (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Namespace">
                  <Input value={namespace} onChange={(e) => setNamespace(e.target.value)} data-testid={`quickstart-namespace-${kind}`} />
                </Field>
                <Field label="Retention" hint={spec.retentionHint}>
                  <Input value={retention} onChange={(e) => setRetention(e.target.value)} data-testid={`quickstart-retention-${kind}`} />
                </Field>
              </div>
              <div className="rounded-md border border-nb-850 bg-nb-950 p-3">
                <div className="mb-1 flex items-center justify-between">
                  <span className="text-xs font-medium uppercase tracking-wide text-nb-500">Install command</span>
                  <CopyButton text={spec.command(namespace || spec.defaultNamespace, retention || spec.defaultRetention)} />
                </div>
                <pre className="overflow-x-auto whitespace-pre font-mono text-xs text-nb-300">{spec.command(namespace || spec.defaultNamespace, retention || spec.defaultRetention)}</pre>
              </div>
              <div className="rounded-md border border-nb-850 bg-nb-950 p-3">
                <div className="mb-1 flex items-center justify-between">
                  <span className="text-xs font-medium uppercase tracking-wide text-nb-500">Then, to reach {spec.openHint.toLowerCase()}</span>
                  <CopyButton text={spec.portForward(namespace || spec.defaultNamespace)} />
                </div>
                <pre className="overflow-x-auto whitespace-pre font-mono text-xs text-nb-300">{spec.portForward(namespace || spec.defaultNamespace)}</pre>
              </div>
              <Button
                type="button"
                variant="primary"
                size="sm"
                disabled={busy || !namespace.trim() || !retention.trim()}
                onClick={() => onSave(namespace.trim(), retention.trim())}
                data-testid={`quickstart-save-${kind}`}
              >
                <Plus size={ICON_MD} /> I&apos;ve installed it
              </Button>
            </>
          )}
        </div>
      )}
    </div>
  )
}

/**
 * The "custom" kind: a backend with no catalog entry of its own (see quickStartBackends.ts's fixed
 * jaeger/prometheus/loki list) - just a person-supplied display name and tool URL, the same "typed
 * catalog plus an open escape hatch" shape processorCatalog.ts uses for extra OTel processors. Unlike the
 * fixed kinds above, there can be more than one (there is no catalog slot limiting it to one per kind),
 * so this renders a list plus an "Add" form instead of one fixed row per kind.
 */
function CustomBackends({ backends, admin, busy, enabledModalities, adding, onToggleAdd, onAdd, onSaveUrl, onRemove }: {
  backends: QuickStartBackend[]
  admin: boolean
  busy: boolean
  enabledModalities: Set<Modality>
  adding: boolean
  onToggleAdd: () => void
  onAdd: (backend: QuickStartBackend) => void
  onSaveUrl: (id: string, url: string) => void
  onRemove: (id: string) => void
}) {
  const modalityOptions = [...enabledModalities]
  const [label, setLabel] = useState('')
  const [modality, setModality] = useState<Modality>(modalityOptions[0] ?? 'traces')
  const [namespace, setNamespace] = useState('observability')
  const [retention, setRetention] = useState('n/a')
  const [toolUrl, setToolUrl] = useState('')

  return (
    <div className="flex flex-col gap-2">
      {backends.map((b) => (
        <CustomBackendRow key={b.id} backend={b} admin={admin} busy={busy} onSaveUrl={(url) => onSaveUrl(b.id, url)} onRemove={() => onRemove(b.id)} />
      ))}
      {admin && (
        <div>
          <button
            type="button"
            onClick={onToggleAdd}
            className="flex w-full items-center gap-2 text-left text-sm text-nb-300 hover:text-nb-200"
            data-testid="quickstart-toggle-custom"
          >
            <Rocket size={ICON_SM} className="text-accent" aria-hidden />
            <span>Have a backend of your own? Register it as a custom destination.</span>
            <ChevronDown size={ICON_SM} className={clsx('ml-auto text-nb-500 transition-transform', adding && 'rotate-180')} aria-hidden />
          </button>
          {adding && (
            <div className="mt-3 flex flex-col gap-3 border-t border-nb-850 pt-3">
              <p className="text-xs text-nb-500">
                Not from this app&apos;s own catalog, so nothing is installed or checked for you here - just a name and a URL to remember it by, the same &quot;mechanism only&quot; rule as everything else on this page.
              </p>
              {modalityOptions.length === 0 ? (
                <p className="text-xs text-nb-500">Turn on a telemetry signal above first.</p>
              ) : (
                <>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label="Display name">
                      <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Elastic APM" data-testid="quickstart-custom-label" />
                    </Field>
                    <Field label="Signal">
                      <Select value={modality} onChange={(e) => setModality(e.target.value as Modality)} data-testid="quickstart-custom-modality">
                        {modalityOptions.map((m) => (
                          <option key={m} value={m}>{m}</option>
                        ))}
                      </Select>
                    </Field>
                    <Field label="Namespace">
                      <Input value={namespace} onChange={(e) => setNamespace(e.target.value)} data-testid="quickstart-custom-namespace" />
                    </Field>
                    <Field label="Retention / notes" hint="Free text, echoed nowhere - just a reminder to yourself.">
                      <Input value={retention} onChange={(e) => setRetention(e.target.value)} data-testid="quickstart-custom-retention" />
                    </Field>
                    <Field label="Tool URL" className="sm:col-span-2">
                      <Input value={toolUrl} onChange={(e) => setToolUrl(e.target.value)} placeholder="https://apm.example.com" data-testid="quickstart-custom-url" />
                    </Field>
                  </div>
                  <Button
                    type="button"
                    variant="primary"
                    size="sm"
                    disabled={busy || !label.trim() || !namespace.trim() || !retention.trim() || !toolUrl.trim()}
                    onClick={() =>
                      onAdd({ id: '', kind: 'custom', modality, namespace: namespace.trim(), retention: retention.trim(), toolUrl: toolUrl.trim(), label: label.trim() })
                    }
                    data-testid="quickstart-custom-save"
                  >
                    <Plus size={ICON_MD} /> Add
                  </Button>
                </>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function CustomBackendRow({ backend, admin, busy, onSaveUrl, onRemove }: {
  backend: QuickStartBackend
  admin: boolean
  busy: boolean
  onSaveUrl: (url: string) => void
  onRemove: () => void
}) {
  const [url, setUrl] = useState(backend.toolUrl ?? '')
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="font-medium text-nb-300">{backend.label}</span>
      <span className="font-mono text-xs text-nb-500">custom · {backend.modality} · {backend.namespace}</span>
      <div className="ml-auto flex items-center gap-2">
        {backend.toolUrl ? (
          <a href={backend.toolUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-accent hover:underline">
            <ExternalLink size={ICON_SM} aria-hidden /> Open {backend.label}
          </a>
        ) : admin ? (
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault()
              if (url.trim()) onSaveUrl(url.trim())
            }}
          >
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://apm.example.com" className="h-7 w-44 text-xs" aria-label={`${backend.label} tool URL`} />
            <Button type="submit" size="sm" disabled={busy || !url.trim()}>Save URL</Button>
          </form>
        ) : null}
        {admin && (
          <Button type="button" size="sm" variant="ghost" onClick={onRemove} disabled={busy} aria-label={`Forget ${backend.label}`}>
            <Trash2 size={ICON_SM} />
          </Button>
        )}
      </div>
    </div>
  )
}

import clsx from 'clsx'
import { ChevronDown, ExternalLink, Plus, Rocket, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { Button, CopyButton, Field, ICON_MD, ICON_SM, Input } from '@/components/ui/primitives'
import { atLeast } from '@/lib/api'
import type { QuickStartBackend, QuickStartKind } from '@/lib/history'
import { QUICK_START_BACKENDS, quickStartSpec } from '@/lib/quickStartBackends'
import type { Modality } from '@/lib/consent'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'

/**
 * "Don't have a backend yet?" - sits right under the telemetry destination field (see TelemetryFields.tsx)
 * and offers to quick-start one, for whichever of Jaeger/Prometheus/Loki matches a modality that's actually
 * turned on. This never deploys or dials anything itself: picking one produces a `helm install` command
 * to run by hand, the same "mechanism only" shape as every other install command in this app
 * (ConnectClusterWizard, regional operators). Once a person says it's installed, it's remembered in
 * Settings (see QuickStartBackend) so this renders a compact summary instead of the setup form next time,
 * with "Open <tool>" once a reachable URL is known and "Use as destination" to fill the field above.
 */
export default function QuickStartBackends({ enabledModalities, onUseAsDestination }: {
  enabledModalities: Set<Modality>
  onUseAsDestination: (exportEndpoint: string, exportProtocol: 'grpc' | 'http') => void
}) {
  const conn = useConn()
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const { settings, save, error } = useSettings()
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState<QuickStartKind | null>(null)

  const relevant = QUICK_START_BACKENDS.filter((s) => enabledModalities.has(s.modality))
  if (relevant.length === 0) return null

  const write = async (backends: QuickStartBackend[]) => {
    setBusy(true)
    try {
      return await save(conn, { ...settings, quickStartBackends: backends })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="sm:col-span-2 rounded-lg border border-dashed border-nb-850 bg-nb-930/60 p-3">
      {error && <p className="mb-2 text-xs text-bad" role="alert">{error}</p>}
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
                onUse={() => onUseAsDestination(spec.exportEndpoint(saved.namespace), spec.exportProtocol)}
                onSaveUrl={(url) => write(settings.quickStartBackends.map((b) => (b.id === saved.id ? { ...b, toolUrl: url } : b)))}
                onSaveRetention={(retention) => write(settings.quickStartBackends.map((b) => (b.id === saved.id ? { ...b, retention } : b)))}
                onRemove={() => write(settings.quickStartBackends.filter((b) => b.id !== saved.id))}
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
      </div>
    </div>
  )
}

function SavedBackend({ backend, admin, busy, canUse, onUse, onSaveUrl, onSaveRetention, onRemove }: {
  backend: QuickStartBackend
  admin: boolean
  busy: boolean
  /** False once a modality this backend cannot carry is also turned on (see QuickStartBackends' own
   *  enabledModalities check) - "Use as destination" is disabled rather than silently misconfiguring the
   *  one exportEndpoint every signal shares, the same guard TelemetryFields' own preset picker applies. */
  canUse: boolean
  onUse: () => void
  onSaveUrl: (url: string) => void
  onSaveRetention: (retention: string) => void
  onRemove: () => void
}) {
  const spec = quickStartSpec(backend.kind)
  const [url, setUrl] = useState(backend.toolUrl ?? '')
  const [editingRetention, setEditingRetention] = useState(false)
  const [retention, setRetention] = useState(backend.retention)
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
          <a href={backend.toolUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-accent hover:underline">
            <ExternalLink size={ICON_SM} aria-hidden /> Open {spec.label.split(' ')[0]}
          </a>
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

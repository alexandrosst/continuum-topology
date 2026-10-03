import clsx from 'clsx'
import { Check, ChevronLeft, Info, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { Button, CopyButton, Field, ICON_MD, ICON_SM, Input, Modal, Select, WizardSteps } from '@/components/ui/primitives'
import type { Modality } from '@/lib/consent'
import { KNOWN_BACKEND_KINDS, type QuickStartBackend, type QuickStartKind } from '@/lib/history'
import { processorTarget, type ProcessorEntry } from '@/lib/processorCatalog'
import { quickStartSpec, type QuickStartSpec } from '@/lib/quickStartBackends'

const KIND_LABEL: Record<QuickStartKind, string> = { jaeger: 'Jaeger', prometheus: 'Prometheus', loki: 'Loki', custom: 'Custom' }
const KIND_BLURB: Record<QuickStartKind, string> = {
  jaeger: 'Traces only. OTLP/gRPC.',
  prometheus: 'Metrics only. OTLP/HTTP.',
  loki: 'Logs only. OTLP/HTTP.',
  custom: 'Any backend with no catalog entry here - register a name and URL, nothing is installed for you.',
}

type Step = 'kind' | 'details' | 'review'

/** Which modality a configured extra processor actually touches, or null when it can't be told from here
 * - either because it carries a `raw` override (opaque JSON, not worth half-guessing at) or because its
 * kind has no notion of a signal at all. Mirrors processorTarget()'s own tailSampling-is-traces-only rule
 * (see processorCatalog.ts) plus filter/transform's own `config.signal`, so this agrees with what the
 * generated collector config would actually do rather than re-deriving it differently. */
function processorModality(e: ProcessorEntry): Modality | null {
  if (e.raw.trim()) return null
  if (processorTarget(e) === 'extraTracesProcessorNames') return 'traces'
  const signal = (e.config as { signal?: 'metric' | 'log' | 'trace' }).signal
  if (signal === 'metric') return 'metrics'
  if (signal === 'log') return 'logs'
  if (signal === 'trace') return 'traces'
  return null
}

interface CompatCheck {
  tone: 'warn' | 'info' | 'ok'
  text: string
}

/** Everything worth telling a person about *before* they copy an install command, not after - a second
 * backend of a kind already set up, a destination this picks replacing one already configured, a
 * modality nothing upstream is even turned on for yet, or an extra processor that was shaped for a
 * different signal than this backend will ever receive. Each is informational, not blocking: none of
 * this stops a real backend from working, it's just the kind of mismatch that otherwise only turns up
 * once nothing shows up in the tool. */
function compatibilityChecks({
  kind,
  modality,
  namespace,
  enabledModalities,
  currentEndpoint,
  currentProtocol,
  extraProcessors,
}: {
  kind: QuickStartKind
  modality: Modality
  namespace: string
  enabledModalities: Set<Modality>
  currentEndpoint: string
  currentProtocol: 'grpc' | 'http'
  extraProcessors: ProcessorEntry[]
}): CompatCheck[] {
  const out: CompatCheck[] = []

  if (!enabledModalities.has(modality)) {
    out.push({ tone: 'warn', text: `No ${modality} signal is turned on in this telemetry configuration yet - this backend won't receive anything until one is.` })
  }

  const dest = currentEndpoint.trim()
  if (dest) {
    out.push({ tone: 'info', text: `A destination is already set (${dest}) - using this backend as the destination below will replace it.` })
    const spec = kind !== 'custom' ? quickStartSpec(kind) : undefined
    if (spec && spec.exportProtocol !== currentProtocol) {
      out.push({ tone: 'info', text: `The current destination uses OTLP/${currentProtocol === 'grpc' ? 'gRPC' : 'HTTP'}; this backend expects OTLP/${spec.exportProtocol === 'grpc' ? 'gRPC' : 'HTTP'} - "Use as destination" switches the protocol automatically.` })
    }
  }

  for (const e of extraProcessors) {
    const pm = processorModality(e)
    if (pm && pm !== modality) {
      out.push({ tone: 'warn', text: `The "${e.name || e.id}" processor only applies to ${pm} - it will have no effect on what this backend receives.` })
    }
  }

  if (out.length === 0) out.push({ tone: 'ok', text: `Nothing already configured conflicts with a ${KIND_LABEL[kind]} backend in "${namespace}".` })
  return out
}

const CHECK_ICON: Record<CompatCheck['tone'], typeof Info> = { warn: TriangleAlert, info: Info, ok: Check }
const CHECK_CLASS: Record<CompatCheck['tone'], string> = { warn: 'text-warn', info: 'text-nb-400', ok: 'text-ok' }

function PickCard({ label, hint, selected, disabled, onClick, testId }: { label: string; hint: string; selected: boolean; disabled?: boolean; onClick: () => void; testId: string }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onClick}
      data-testid={testId}
      className={clsx(
        'relative flex flex-col items-start gap-1 rounded-xl border p-3 text-left transition-colors',
        disabled
          ? 'cursor-not-allowed border-nb-850 bg-nb-930/40 opacity-50'
          : selected
            ? 'border-accent bg-accent-soft ring-1 ring-accent/40'
            : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
      )}
    >
      {selected && (
        <span className="absolute right-3 top-3 flex size-5 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
          <Check size={ICON_MD} strokeWidth={3} />
        </span>
      )}
      <span className="pr-6 text-sm font-medium text-nb-200">{label}</span>
      <span className="text-xs text-nb-500">{hint}</span>
    </button>
  )
}

function BackLink({ onClick, testId }: { onClick: () => void; testId: string }) {
  return (
    <Button variant="ghost" size="sm" onClick={onClick} data-testid={testId}>
      <ChevronLeft size={ICON_SM} /> Back
    </Button>
  )
}

/**
 * A guided path to a working quick-start backend: pick a kind (from this org's own allow-list - see
 * AllowedKindsControl in QuickStartBackends.tsx, the only caller), set its namespace/retention (or, for
 * "custom", its name/modality/URL), see what - if anything - looks like it won't play well with what's
 * already configured, then get the install command. This never deploys or dials anything itself, same as
 * every other step of quick-start; it only decides what to show before handing over a command, instead of
 * generating one blindly the moment a kind is picked (see QuickStartBackends.tsx's own flat SetupBackend
 * disclosures, which this sits next to rather than replaces - a person who already knows what they want
 * can still use those directly).
 */
export default function TelemetryBackendWizard({
  open,
  onClose,
  allowedKinds,
  enabledModalities,
  existingBackends,
  currentEndpoint,
  currentProtocol,
  extraProcessors,
  admin,
  busy,
  error,
  onSave,
}: {
  open: boolean
  onClose: () => void
  allowedKinds: QuickStartKind[]
  /** Which modalities are turned on elsewhere in the telemetry form right now - used for the "nothing will
   *  reach this backend yet" compatibility check, not to filter which kinds are offered. */
  enabledModalities: Set<Modality>
  existingBackends: QuickStartBackend[]
  currentEndpoint: string
  currentProtocol: 'grpc' | 'http'
  extraProcessors: ProcessorEntry[]
  admin: boolean
  busy: boolean
  error?: string | null
  onSave: (backend: QuickStartBackend) => void
}) {
  const [kind, setKind] = useState<QuickStartKind | undefined>()
  const [step, setStep] = useState<Step>('kind')
  const [namespace, setNamespace] = useState('')
  const [retention, setRetention] = useState('')
  const [label, setLabel] = useState('')
  const [customModality, setCustomModality] = useState<Modality>('traces')
  const [toolUrl, setToolUrl] = useState('')

  const reset = () => {
    setKind(undefined)
    setStep('kind')
    setNamespace('')
    setRetention('')
    setLabel('')
    setToolUrl('')
  }
  const close = () => {
    reset()
    onClose()
  }

  const spec: QuickStartSpec | undefined = kind && kind !== 'custom' ? quickStartSpec(kind) : undefined
  const modality: Modality = kind === 'custom' ? customModality : spec?.modality ?? 'traces'
  const duplicate = kind && kind !== 'custom' ? existingBackends.find((b) => b.kind === kind) : undefined

  const pickKind = (k: QuickStartKind) => {
    setKind(k)
    const s = k !== 'custom' ? quickStartSpec(k) : undefined
    setNamespace(s?.defaultNamespace ?? 'observability')
    setRetention(s?.defaultRetention ?? '')
    setLabel(s ? s.label : '')
    setStep('details')
  }

  const detailsValid = kind === 'custom' ? label.trim() && namespace.trim() && toolUrl.trim() : namespace.trim() && retention.trim()

  const save = () => {
    const rec: QuickStartBackend =
      kind === 'custom'
        ? { id: '', kind: 'custom', modality: customModality, namespace: namespace.trim(), retention: retention.trim() || 'n/a', toolUrl: toolUrl.trim(), label: label.trim() }
        : { id: '', kind: kind!, modality, namespace: namespace.trim(), retention: retention.trim(), label: spec!.label }
    onSave(rec)
  }

  const steps = ['Kind', 'Details', 'Review']
  const currentIndex = steps.indexOf(step === 'kind' ? 'Kind' : step === 'details' ? 'Details' : 'Review')

  return (
    <Modal open={open} onClose={close} width="max-w-2xl" title="Guided backend setup" description="Pick a backend, set sensible defaults for an evaluation deployment, and see what's worth knowing about before you run the install command.">
      <WizardSteps steps={steps} currentIndex={currentIndex} testId="backend-wizard-steps" />

      {step === 'kind' && (
        <div data-testid="backend-wizard-step-kind">
          <p className="mb-2.5 text-xs text-nb-500">Which backend kind? Only what this organisation allows is shown.</p>
          <div className="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Backend kind">
            {KNOWN_BACKEND_KINDS.filter((k) => allowedKinds.includes(k)).map((k) => (
              <PickCard key={k} label={KIND_LABEL[k]} hint={KIND_BLURB[k]} selected={kind === k} onClick={() => pickKind(k)} testId={`backend-wizard-kind-${k}`} />
            ))}
          </div>
          {allowedKinds.length === 0 && <p className="mt-2 text-xs text-nb-500">No backend kind is enabled for this organisation yet - an administrator can turn one on above.</p>}
        </div>
      )}

      {step === 'details' && kind && (
        <div className="space-y-3" data-testid="backend-wizard-step-details">
          {!admin ? (
            <p className="text-xs text-nb-500">Only administrators can set this up.</p>
          ) : kind === 'custom' ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Display name">
                <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Elastic APM" data-testid="backend-wizard-custom-label" />
              </Field>
              <Field label="Signal">
                <Select value={customModality} onChange={(e) => setCustomModality(e.target.value as Modality)} data-testid="backend-wizard-custom-modality">
                  <option value="traces">traces</option>
                  <option value="metrics">metrics</option>
                  <option value="logs">logs</option>
                </Select>
              </Field>
              <Field label="Namespace">
                <Input value={namespace} onChange={(e) => setNamespace(e.target.value)} data-testid="backend-wizard-custom-namespace" />
              </Field>
              <Field label="Retention / notes" hint="Free text, echoed nowhere - just a reminder to yourself.">
                <Input value={retention} onChange={(e) => setRetention(e.target.value)} placeholder="n/a" data-testid="backend-wizard-custom-retention" />
              </Field>
              <Field label="Tool URL" className="sm:col-span-2">
                <Input value={toolUrl} onChange={(e) => setToolUrl(e.target.value)} placeholder="https://apm.example.com" data-testid="backend-wizard-custom-url" />
              </Field>
            </div>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Namespace">
                <Input value={namespace} onChange={(e) => setNamespace(e.target.value)} data-testid="backend-wizard-namespace" />
              </Field>
              <Field label="Retention" hint={spec?.retentionHint}>
                <Input value={retention} onChange={(e) => setRetention(e.target.value)} data-testid="backend-wizard-retention" />
              </Field>
              <p className="text-xs text-nb-600 sm:col-span-2">
                {spec?.defaultRetention} is sized for trying this out, not for keeping data around - short of the long-lived defaults {KIND_LABEL[kind]} otherwise assumes. Lengthen it once this is a real destination, not a quick-start.
              </p>
            </div>
          )}
          <div className="flex items-center gap-2 pt-1">
            <BackLink onClick={() => setStep('kind')} testId="backend-wizard-back-details" />
            {admin && (
              <Button variant="primary" className="ml-auto" disabled={!detailsValid} onClick={() => setStep('review')} data-testid="backend-wizard-continue">
                Continue
              </Button>
            )}
          </div>
        </div>
      )}

      {step === 'review' && kind && (
        <div className="space-y-3" data-testid="backend-wizard-step-review">
          {duplicate ? (
            <div className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" role="alert" data-testid="backend-wizard-duplicate">
              A {KIND_LABEL[kind]} quick-start backend already exists, in namespace "{duplicate.namespace}". Manage that one from the panel below instead of setting up a second - only one is tracked per kind.
            </div>
          ) : (
            <>
              <div className="space-y-1.5" data-testid="backend-wizard-checks">
                {compatibilityChecks({ kind, modality, namespace: namespace.trim(), enabledModalities, currentEndpoint, currentProtocol, extraProcessors }).map((c, i) => {
                  const Icon = CHECK_ICON[c.tone]
                  return (
                    <p key={i} className={clsx('flex items-start gap-1.5 text-xs', CHECK_CLASS[c.tone])}>
                      <Icon size={ICON_SM} className="mt-0.5 shrink-0" aria-hidden /> <span>{c.text}</span>
                    </p>
                  )
                })}
              </div>

              {kind === 'custom' ? (
                <p className="text-xs text-nb-500">Not from this app's own catalog - nothing is installed or checked for you here. Adding it just remembers the name and URL above.</p>
              ) : (
                <>
                  <div className="rounded-md border border-nb-850 bg-nb-950 p-3">
                    <div className="mb-1 flex items-center justify-between">
                      <span className="text-xs font-medium uppercase tracking-wide text-nb-500">Install command</span>
                      <CopyButton text={spec!.command(namespace.trim() || spec!.defaultNamespace, retention.trim() || spec!.defaultRetention)} />
                    </div>
                    <pre className="overflow-x-auto whitespace-pre font-mono text-xs text-nb-300">{spec!.command(namespace.trim() || spec!.defaultNamespace, retention.trim() || spec!.defaultRetention)}</pre>
                  </div>
                  <div className="rounded-md border border-nb-850 bg-nb-950 p-3">
                    <div className="mb-1 flex items-center justify-between">
                      <span className="text-xs font-medium uppercase tracking-wide text-nb-500">Then, to reach {spec!.openHint.toLowerCase()}</span>
                      <CopyButton text={spec!.portForward(namespace.trim() || spec!.defaultNamespace)} />
                    </div>
                    <pre className="overflow-x-auto whitespace-pre font-mono text-xs text-nb-300">{spec!.portForward(namespace.trim() || spec!.defaultNamespace)}</pre>
                  </div>
                </>
              )}
            </>
          )}

          {error && <p role="alert" className="text-xs text-bad">{error}</p>}

          <div className="flex items-center gap-2 pt-1">
            <BackLink onClick={() => setStep('details')} testId="backend-wizard-back-review" />
            {admin && !duplicate && (
              <Button variant="primary" className="ml-auto" disabled={busy} onClick={save} data-testid="backend-wizard-save">
                {kind === 'custom' ? "Add" : "I've installed it"}
              </Button>
            )}
            {duplicate && <Button className="ml-auto" onClick={close}>Done</Button>}
          </div>
        </div>
      )}
    </Modal>
  )
}

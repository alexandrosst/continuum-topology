import clsx from 'clsx'
import { useState, type ReactNode } from 'react'
import { ComboField, Field, InfoTip, Input, Select, TagsInput } from '@/components/ui/primitives'
import { EXPORT_PRESETS, presetSupportsModalities, unsupportedDestinationNote } from '@/lib/exportPresets'
import { applyIntentPreset, TELEMETRY_INTENT_PRESETS, TELEMETRY_SIGNALS, TELEMETRY_UNIVERSAL_PERMISSION } from '@/lib/consent'
import { telemetryActive, telemetryProblems, type TelemetryInput } from '@/lib/install'
import GuidedWizard from './GuidedWizard'
import ProcessorEditor from './ProcessorEditor'
import { DebugChoice, TagEditor } from './ProcessStep'
import QuickStartBackends from './QuickStartBackends'

export type SignalId = 'resourceUsage' | 'energy' | 'kubernetesState' | 'nodeRuntime' | 'networkLatency' | 'applicationMetrics' | 'systemLogs' | 'kubernetesEvents' | 'applicationLogs' | 'traces' | 'accelerators'

/** cluster/node scope needs no form control (cluster: physically unfilterable; node: the chart's own
 * nodeSelector/tolerations values, structured k8s scheduling objects that don't fit this form's --set
 * model) - so those two just get an honest caption here instead. application scope gets a real control,
 * either the flat grid's own "Application scope overrides" details block or the guided wizard's scope step. */
const scopeCaption = (s: (typeof TELEMETRY_SIGNALS)[number]): string | undefined => {
  if (s.scope === 'cluster') return 'Always cluster-wide - cannot be narrowed.'
  if (s.scope === 'node') return "Runs on every node - narrow which nodes with the chart's own nodeSelector/tolerations values, not from this form."
  return undefined
}

/** Energy's own source picker, shown once `energy` is on. Exported so the guided wizard (GuidedScope.tsx)
 * renders the exact same block right after its "pick signals" step, instead of a second, driftable copy. */
export function EnergyFields({ value, onChange, testIdPrefix }: { value: TelemetryInput; onChange: (v: TelemetryInput) => void; testIdPrefix: string }) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  return (
    <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
      <Field label="Energy source">
        <Select
          value={value.energySource}
          onChange={(e) => set('energySource', e.target.value as TelemetryInput['energySource'])}
          data-testid={`${testIdPrefix}-energy-source`}
        >
          <option value="bundle-kepler">Deploy Kepler (privileged, one pod per node)</option>
          <option value="existing">Scrape one I already run</option>
        </Select>
      </Field>
      {value.energySource === 'existing' && (
        <Field label="Its Prometheus endpoint">
          <Input
            value={value.energyExistingEndpoint}
            onChange={(e) => set('energyExistingEndpoint', e.target.value)}
            placeholder="kepler.monitoring:9102/metrics"
            data-testid={`${testIdPrefix}-energy-endpoint`}
          />
        </Field>
      )}
    </div>
  )
}

/** Accelerators' own source picker and "apply the install's scope to GPU metrics" toggle, shown once
 * `accelerators` is on. Exported for the same reason as EnergyFields above. */
export function AcceleratorsFields({ value, onChange, testIdPrefix }: { value: TelemetryInput; onChange: (v: TelemetryInput) => void; testIdPrefix: string }) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const accelerators = TELEMETRY_SIGNALS.find((s) => s.id === 'accelerators')
  return (
    <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
      <Field label="Accelerators source">
        <Select
          value={value.acceleratorsSource}
          onChange={(e) => set('acceleratorsSource', e.target.value as TelemetryInput['acceleratorsSource'])}
          data-testid={`${testIdPrefix}-accelerators-source`}
        >
          <option value="bundle-dcgm">Deploy dcgm-exporter (GPU nodes only, needs the NVIDIA driver + Container Toolkit already on the node)</option>
          <option value="existing">Scrape one I already run</option>
        </Select>
      </Field>
      {value.acceleratorsSource === 'existing' && (
        <Field label="Its Prometheus endpoint">
          <Input
            value={value.acceleratorsExistingEndpoint}
            onChange={(e) => set('acceleratorsExistingEndpoint', e.target.value)}
            placeholder="dcgm-exporter.monitoring:9400/metrics"
            data-testid={`${testIdPrefix}-accelerators-endpoint`}
          />
        </Field>
      )}
      {accelerators?.namespaceScopable && (
        <label className="flex cursor-pointer items-start gap-2.5 text-sm sm:col-span-2">
          <input
            type="checkbox"
            className="mt-0.5 size-4 accent-[var(--color-accent)]"
            checked={value.acceleratorsApplyScope}
            onChange={(e) => set('acceleratorsApplyScope', e.target.checked)}
            data-testid={`${testIdPrefix}-accelerators-apply-scope`}
          />
          <span>
            <span className="text-nb-300">Apply the install's namespace scope to GPU metrics</span>
            <InfoTip>Off by default. Accelerators are deployed per node (infrastructure), but GPU metrics can carry the namespace/pod using the GPU - turning this on asks dcgm-exporter to attach that identity, so the install's namespace scope narrows GPU metrics the same way it narrows application data.</InfoTip>
            <span className="block text-xs text-nb-500">Only takes effect while deploying dcgm-exporter above, not when scraping one you already run.</span>
          </span>
        </label>
      )}
    </div>
  )
}

/** One signal's checkbox row: the label, its permissions info tip, what it collects, and (cluster/node
 * signals only) the caption explaining why there's no scope control for it. Exported so the guided wizard's
 * "pick signals" step (GuidedScope.tsx) renders the exact same row the flat grid does, rather than a second,
 * driftable copy. */
export function SignalRow({
  signal,
  checked,
  onChange,
  testIdPrefix,
}: {
  signal: (typeof TELEMETRY_SIGNALS)[number]
  checked: boolean
  onChange: (v: boolean) => void
  testIdPrefix: string
}) {
  const caption = scopeCaption(signal)
  return (
    <label className="flex cursor-pointer items-start gap-2.5 text-sm">
      <input
        type="checkbox"
        className="mt-0.5 size-4 accent-[var(--color-accent)]"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        data-testid={`${testIdPrefix}-${signal.id}`}
      />
      <span>
        <span className="text-nb-300">{signal.label}</span>
        <InfoTip>{signal.permissions}</InfoTip>
        <span className="block text-xs text-nb-500">{signal.what}</span>
        {caption && <span className="block text-xs text-nb-600">{caption}</span>}
      </span>
    </label>
  )
}

/**
 * The telemetry form: intent presets, one checkbox per signal (grouped infrastructure/application,
 * following each signal's own `layer` in TELEMETRY_SIGNALS), a browsing-only scope/layer/modality filter
 * row, energy's and accelerators' source pickers, per-kind application scope overrides, pipeline processor
 * controls, and the shared export target - built once so the install wizard and the post-install "change
 * telemetry" panel render the exact same fields from the exact same TelemetryInput shape (see install.ts),
 * instead of two hand-rolled copies that drift apart.
 * `measurementsOn` only feeds the networkLatency warning (see telemetryProblems); omit it where the caller
 * doesn't also control that separate extra.
 */
export default function TelemetryFields({
  value,
  onChange,
  measurementsOn,
  testIdPrefix = 'telemetry',
  initialScope,
  agentId,
  clusterId,
  runSection,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  /** The command to run (or, for an operator destination, the button that generates it) - the caller builds
   *  it, since it depends on things this form doesn't hold. In guided mode it is the wizard's last step,
   *  after Review, so nobody meets a command before they have chosen everything it contains; in the flat
   *  grid, which has no steps, it follows the form. */
  runSection?: ReactNode
  measurementsOn?: boolean
  testIdPrefix?: string
  /** The agent/cluster being configured, when known - handed to the guided wizard's destination step. */
  agentId?: string
  clusterId?: string
  /** A scope pre-filled from outside this form (see GuidedScope.tsx) - when present, also starts the form
   * in guided mode, so the person lands directly on their pre-filled draft instead of needing to notice
   * and click into guided mode themselves first. */
  initialScope?: { name: string; namespaces: string[] }
}) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const problems = telemetryProblems(value, measurementsOn)
  const destinationNote = unsupportedDestinationNote(value.exportEndpoint)
  const exportPreset = EXPORT_PRESETS.find((p) => p.endpointPattern === value.exportEndpoint)
  const grantedRules = TELEMETRY_SIGNALS.filter((s) => (value as unknown as Record<string, boolean>)[s.id])
  // There is only ever one exportEndpoint for every signal together - a preset that only speaks a subset
  // of modalities (Jaeger: traces) has to be flagged the moment something it can't carry is also on, not
  // left to be noticed once telemetry that looked configured never shows up anywhere.
  const enabledModalitySet = new Set(grantedRules.map((s) => s.modality))
  const compatiblePresets = EXPORT_PRESETS.filter((p) => presetSupportsModalities(p, enabledModalitySet))
  // A mismatch here is also (now) one of `problems` above - install.ts's telemetryProblems is what
  // actually blocks withTelemetry's generated command, not this component. This derivation only decides
  // whether to show exportPreset.note (the mismatch already has its own, blocking message in `problems`).
  const modalityMismatch = exportPreset && !presetSupportsModalities(exportPreset, enabledModalitySet)

  // Which entry path is showing: local UI state, defaulting to the flat grid so a form nobody has opted
  // into guided mode for renders exactly as it always has (see the plan note on TelemetryFields.tsx) -
  // unless a pre-filled scope was just handed to this form from outside, in which case guided is the only
  // mode that has anywhere to show it.
  const [guided, setGuided] = useState(() => !!initialScope)

  // A browsing aid only - local state, never written into TelemetryInput - so leaving every facet at
  // its "All" default reproduces byte-identical infra/app lists to before facets existed.
  const [scopeFacet, setScopeFacet] = useState<'all' | 'cluster' | 'node' | 'application'>('all')
  const [layerFacet, setLayerFacet] = useState<'all' | 'infrastructure' | 'application'>('all')
  const [modalityFacet, setModalityFacet] = useState<'all' | 'metrics' | 'logs' | 'traces'>('all')
  const visible = TELEMETRY_SIGNALS.filter(
    (s) =>
      (scopeFacet === 'all' || s.scope === scopeFacet) &&
      (layerFacet === 'all' || s.layer === layerFacet) &&
      (modalityFacet === 'all' || s.modality === modalityFacet),
  )
  const infra = visible.filter((s) => s.layer === 'infrastructure')
  const app = visible.filter((s) => s.layer === 'application')

  // Credentials and processor tuning start collapsed - the same "hidden until it's on" instinct the
  // namespace-scope fieldset elsewhere in this app already applies to its own fields - but open on arrival
  // if any of them already hold a non-default value, so an existing configuration is never hidden.
  const [advancedOpen, setAdvancedOpen] = useState(
    () => value.exportAuthSecretName.trim() !== '' || value.resourceDetection || !value.redaction || value.tracesSamplingPercent !== 100,
  )
  // Same "open if already non-default" instinct, for whichever app-layer kinds already carry their own
  // scope override (e.g. seeded from an existing install) - otherwise an existing override would be hidden.
  const [scopeOverridesOpen, setScopeOverridesOpen] = useState(
    () =>
      value.applicationMetricsScope.namespaces.length > 0 ||
      value.applicationMetricsScope.exclude.length > 0 ||
      value.applicationLogsScope.namespaces.length > 0 ||
      value.applicationLogsScope.exclude.length > 0 ||
      value.tracesScope.namespaces.length > 0 ||
      value.tracesScope.exclude.length > 0,
  )

  const row = (s: (typeof TELEMETRY_SIGNALS)[number]) => {
    const id = s.id as SignalId
    return <SignalRow key={id} signal={s} checked={value[id]} onChange={(v) => set(id, v)} testIdPrefix={testIdPrefix} />
  }

  return (
    <div className="space-y-4">
      <Field label="Start from a preset" hint="Sets the signals below to exactly this combination; everything else on this form stays as you set it - pick one, then still hand-tune anything.">
        <Select
          value=""
          placeholder="Pick a preset…"
          onChange={(e) => {
            const preset = TELEMETRY_INTENT_PRESETS.find((p) => p.id === e.target.value)
            if (preset) onChange(applyIntentPreset(value, preset))
          }}
          data-testid={`${testIdPrefix}-intent-preset`}
        >
          {TELEMETRY_INTENT_PRESETS.map((p) => (
            <option key={p.id} value={p.id} title={p.description}>{p.label}</option>
          ))}
        </Select>
      </Field>

      <div className="flex items-center gap-1 rounded-lg border border-nb-850 p-1 text-xs" role="radiogroup" aria-label="How to configure signals and scope">
        {(
          [
            ['All fields', 'Every signal on one screen, exactly as before.'],
            ['Guided setup', 'Pick what to collect on one screen, then step through scope, processing, destination and the command - like the discovery wizard.'],
          ] as const
        ).map(([label, hint], i) => (
          <button
            key={label}
            type="button"
            role="radio"
            aria-checked={guided === (i === 1)}
            title={hint}
            onClick={() => setGuided(i === 1)}
            className={clsx(
              'flex-1 rounded-md px-2.5 py-1.5 font-medium transition-colors',
              guided === (i === 1) ? 'bg-accent text-white' : 'text-nb-400 hover:text-nb-300',
            )}
            data-testid={`${testIdPrefix}-mode-${i === 1 ? 'guided' : 'flat'}`}
          >
            {label}
          </button>
        ))}
      </div>

      {guided ? (
        <GuidedWizard value={value} onChange={onChange} testIdPrefix={testIdPrefix} initialScope={initialScope} agentId={agentId} clusterId={clusterId} runSection={runSection} />
      ) : (
      <>
      <div className="space-y-1.5">
        <p className="text-xs text-nb-500">Filter which signals are shown below - a browsing aid, doesn't change what's selected.</p>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Scope">
            <Select value={scopeFacet} onChange={(e) => setScopeFacet(e.target.value as typeof scopeFacet)} data-testid={`${testIdPrefix}-facet-scope`}>
              <option value="all">All</option>
              <option value="cluster">Cluster</option>
              <option value="node">Node</option>
              <option value="application">Application</option>
            </Select>
          </Field>
          <Field label="Layer">
            <Select value={layerFacet} onChange={(e) => setLayerFacet(e.target.value as typeof layerFacet)} data-testid={`${testIdPrefix}-facet-layer`}>
              <option value="all">All</option>
              <option value="infrastructure">Infrastructure</option>
              <option value="application">Application</option>
            </Select>
          </Field>
          <Field label="Modality">
            <Select value={modalityFacet} onChange={(e) => setModalityFacet(e.target.value as typeof modalityFacet)} data-testid={`${testIdPrefix}-facet-modality`}>
              <option value="all">All</option>
              <option value="metrics">Metrics</option>
              <option value="logs">Logs</option>
              <option value="traces">Traces</option>
            </Select>
          </Field>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <fieldset className="space-y-2.5">
          <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-500">Infrastructure</legend>
          {infra.map(row)}
          {infra.length === 0 && <p className="text-xs text-nb-600">No infrastructure signal matches this filter.</p>}
        </fieldset>
        <fieldset className="space-y-2.5">
          <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-500">Application</legend>
          {app.map(row)}
          {app.length === 0 && <p className="text-xs text-nb-600">No application signal matches this filter.</p>}
        </fieldset>
      </div>

      {value.energy && <EnergyFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}

      {value.accelerators && <AcceleratorsFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}

      {(value.applicationMetrics || value.applicationLogs || value.traces) && (
        <details
          className="group rounded-lg border border-nb-850"
          open={scopeOverridesOpen}
          onToggle={(e) => setScopeOverridesOpen(e.currentTarget.open)}
          data-testid={`${testIdPrefix}-scope-overrides`}
        >
          <summary className="flex cursor-pointer select-none items-center gap-1.5 px-3 py-2 text-xs font-medium text-nb-400 hover:text-nb-300 marker:content-none">
            Application scope overrides
          </summary>
          <div className="space-y-3 border-t border-nb-850 p-3">
            <p className="text-xs text-nb-500">Each falls back to the install's own namespace scope; set either field below to narrow just that one signal instead.</p>
            {value.applicationMetrics && (
              <div className="grid gap-3 sm:grid-cols-2">
                <legend className="text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Application metrics</legend>
                <Field label="Only these namespaces" hint="Empty: falls back to the install's global scope.">
                  <TagsInput
                    value={value.applicationMetricsScope.namespaces}
                    onChange={(v) => set('applicationMetricsScope', { ...value.applicationMetricsScope, namespaces: v })}
                    placeholder="shop payments"
                    data-testid={`${testIdPrefix}-applicationMetrics-scope-namespaces`}
                  />
                </Field>
                <Field label="Never these" hint="Falls back to the install's global scope when empty.">
                  <TagsInput
                    value={value.applicationMetricsScope.exclude}
                    onChange={(v) => set('applicationMetricsScope', { ...value.applicationMetricsScope, exclude: v })}
                    placeholder="hr-data"
                    data-testid={`${testIdPrefix}-applicationMetrics-scope-exclude`}
                  />
                </Field>
              </div>
            )}
            {value.applicationLogs && (
              <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
                <legend className="text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Application logs</legend>
                <Field label="Only these namespaces" hint="Empty: falls back to the install's global scope.">
                  <TagsInput
                    value={value.applicationLogsScope.namespaces}
                    onChange={(v) => set('applicationLogsScope', { ...value.applicationLogsScope, namespaces: v })}
                    placeholder="shop payments"
                    data-testid={`${testIdPrefix}-applicationLogs-scope-namespaces`}
                  />
                </Field>
                <Field label="Never these" hint="Falls back to the install's global scope when empty.">
                  <TagsInput
                    value={value.applicationLogsScope.exclude}
                    onChange={(v) => set('applicationLogsScope', { ...value.applicationLogsScope, exclude: v })}
                    placeholder="hr-data"
                    data-testid={`${testIdPrefix}-applicationLogs-scope-exclude`}
                  />
                </Field>
              </div>
            )}
            {value.traces && (
              <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
                <legend className="text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Traces</legend>
                <Field label="Only these namespaces" hint="Empty: falls back to the install's global scope.">
                  <TagsInput
                    value={value.tracesScope.namespaces}
                    onChange={(v) => set('tracesScope', { ...value.tracesScope, namespaces: v })}
                    placeholder="shop payments"
                    data-testid={`${testIdPrefix}-traces-scope-namespaces`}
                  />
                </Field>
                <Field label="Never these" hint="Falls back to the install's global scope when empty.">
                  <TagsInput
                    value={value.tracesScope.exclude}
                    onChange={(v) => set('tracesScope', { ...value.tracesScope, exclude: v })}
                    placeholder="hr-data"
                    data-testid={`${testIdPrefix}-traces-scope-exclude`}
                  />
                </Field>
              </div>
            )}
          </div>
        </details>
      )}
      </>
      )}

      {/* The guided wizard's Destination step already owns the destination, the protocol, TLS and the
          credential - rendering this block under it as well showed every one of those choices twice. */}
      {!guided && (
        <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
          <Field label="Send telemetry to" hint="Pick a known backend to fill in its endpoint pattern and credential header, or type your own - an existing collector gateway or observability backend.">
            <ComboField
              value={value.exportEndpoint}
              onChange={(v) => {
                // Picking a preset also carries its known-correct protocol and credential header, not just
                // the endpoint text - otherwise a backend that needs OTLP/HTTP (Grafana Cloud, Datadog) or a
                // non-"Authorization" header (New Relic, SigNoz, ...) would silently deploy misconfigured
                // even though the preset already knew the right values.
                const preset = EXPORT_PRESETS.find((p) => p.endpointPattern === v)
                onChange({
                  ...value,
                  exportEndpoint: v,
                  exportOperatorId: '',
                  exportProtocol: preset ? preset.protocol : value.exportProtocol,
                  exportAuthHeaderName: preset && preset.headerName ? preset.headerName : value.exportAuthHeaderName,
                })
              }}
              placeholder="otel-gateway.example.com:4317"
              options={compatiblePresets.map((p) => ({ value: p.endpointPattern, label: p.label }))}
            />
          </Field>
          <Field label="Protocol">
            <Select
              value={value.exportProtocol}
              onChange={(e) => set('exportProtocol', e.target.value as TelemetryInput['exportProtocol'])}
              data-testid={`${testIdPrefix}-export-protocol`}
            >
              <option value="grpc">OTLP/gRPC</option>
              <option value="http">OTLP/HTTP</option>
            </Select>
          </Field>
          {destinationNote && (
            <p role="alert" className="text-xs text-warn sm:col-span-2">{destinationNote}</p>
          )}
          {!destinationNote && !modalityMismatch && exportPreset?.note && (
            <p className="text-xs text-nb-500 sm:col-span-2">{exportPreset.note}</p>
          )}
          <QuickStartBackends
            enabledModalities={enabledModalitySet}
            onUseAsDestination={(endpoint, protocol) => onChange({ ...value, exportEndpoint: endpoint, exportProtocol: protocol, exportOperatorId: '' })}
            currentDestination={{ endpoint: value.exportEndpoint, protocol: value.exportProtocol }}
            extraProcessors={value.extraProcessors}
          />
          <label className="flex cursor-pointer items-center gap-2 text-sm sm:col-span-2">
            <input
              type="checkbox"
              className="size-4 accent-[var(--color-accent)]"
              checked={value.exportInsecure}
              onChange={(e) => set('exportInsecure', e.target.checked)}
              data-testid={`${testIdPrefix}-export-insecure`}
            />
            <span className="text-nb-300">Skip TLS verification for this endpoint</span>
            <InfoTip>Only for a self-signed or internal endpoint you already trust by other means - the connection is still encrypted, its certificate is just not checked.</InfoTip>
          </label>
        </div>
      )}

      {/* The guided wizard has its own Process step for all of this; only the flat grid keeps it here. */}
      {!guided && (
      <details
        className="group rounded-lg border border-nb-850"
        open={advancedOpen}
        onToggle={(e) => setAdvancedOpen(e.currentTarget.open)}
        data-testid={`${testIdPrefix}-advanced`}
      >
        <summary className="flex cursor-pointer select-none items-center gap-1.5 px-3 py-2 text-xs font-medium text-nb-400 hover:text-nb-300 marker:content-none">
          {guided ? 'Processing options' : 'Credentials & processing'}
        </summary>
        <div className="grid gap-3 border-t border-nb-850 p-3 sm:grid-cols-2">
          {!guided && (
            <>
              <Field label="Credential header" hint="Which header the destination expects its credential in.">
                <Input
                  value={value.exportAuthHeaderName}
                  onChange={(e) => set('exportAuthHeaderName', e.target.value)}
                  placeholder="Authorization"
                  data-testid={`${testIdPrefix}-export-auth-header`}
                />
              </Field>
              <Field label="Secret holding it" hint="A Secret you create in the release namespace, outside this chart - never the credential value itself.">
                <Input
                  value={value.exportAuthSecretName}
                  onChange={(e) => set('exportAuthSecretName', e.target.value)}
                  placeholder="telemetry-export-token"
                  data-testid={`${testIdPrefix}-export-auth-secret`}
                />
              </Field>
            </>
          )}
          <legend className="text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Processing</legend>
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-[var(--color-accent)]"
              checked={value.resourceDetection}
              onChange={(e) => set('resourceDetection', e.target.checked)}
              data-testid={`${testIdPrefix}-resource-detection`}
            />
            <span>
              <span className="text-nb-300">Enrich with collector environment</span>
              <span className="block text-xs text-nb-500">Adds resource attributes about the collector's own runtime environment.</span>
            </span>
          </label>
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-[var(--color-accent)]"
              checked={value.redaction}
              onChange={(e) => set('redaction', e.target.checked)}
              data-testid={`${testIdPrefix}-redaction`}
            />
            <span>
              <span className="text-nb-300">Mask likely secrets</span>
              <InfoTip>On by default. Masks the values of attributes whose key looks like a token/password/secret/API key before anything leaves the cluster - the receiver has no auth of its own unless you set one up separately.</InfoTip>
              <span className="block text-xs text-nb-500">Recommended: turning this off sends attribute values through unmasked.</span>
            </span>
          </label>
          <Field label="Traces sampling %" hint="100 keeps every span (the default). Lower it to cut trace volume and cost.">
            <Input
              type="number"
              min={0}
              max={100}
              value={value.tracesSamplingPercent}
              onChange={(e) => set('tracesSamplingPercent', e.target.valueAsNumber || 0)}
              data-testid={`${testIdPrefix}-traces-sampling`}
            />
          </Field>
          <legend className="border-t border-nb-850 pt-3 text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Tags on everything</legend>
          <div className="sm:col-span-2">
            <TagEditor value={value} onChange={onChange} testIdPrefix={testIdPrefix} />
          </div>
          <legend className="border-t border-nb-850 pt-3 text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Check that it works</legend>
          <div className="sm:col-span-2">
            <DebugChoice value={value} onChange={onChange} testIdPrefix={testIdPrefix} />
          </div>
          <legend className="border-t border-nb-850 pt-3 text-xs font-medium uppercase tracking-wide text-nb-500 sm:col-span-2">Extra processors</legend>
          <div className="sm:col-span-2">
            <ProcessorEditor entries={value.extraProcessors} onChange={(extraProcessors) => set('extraProcessors', extraProcessors)} testIdPrefix={testIdPrefix} />
          </div>
        </div>
      </details>
      )}

      {telemetryActive(value) && (
        <p className="border-t border-nb-850 pt-3 text-xs text-nb-500" data-testid={`${testIdPrefix}-permissions-summary`}>
          {TELEMETRY_UNIVERSAL_PERMISSION}
          {grantedRules.length > 0 && ` Currently on: ${grantedRules.map((s) => s.label).join(', ')}.`}
        </p>
      )}

      {problems.length > 0 && (
        <p role="alert" className="text-xs text-bad" data-testid={`${testIdPrefix}-problems`}>
          {problems.join('. ')}.
        </p>
      )}

      {!guided && runSection}
    </div>
  )
}

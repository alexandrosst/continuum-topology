import { ComboField, Field, InfoTip, Input, Select } from '@/components/ui/primitives'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '@/lib/exportPresets'
import { applyIntentPreset, TELEMETRY_INTENT_PRESETS, TELEMETRY_SIGNALS, TELEMETRY_UNIVERSAL_PERMISSION } from '@/lib/consent'
import { telemetryActive, telemetryProblems, type TelemetryInput } from '@/lib/install'

type SignalId = 'resourceUsage' | 'energy' | 'kubernetesState' | 'nodeRuntime' | 'networkLatency' | 'applicationMetrics' | 'systemLogs' | 'kubernetesEvents' | 'applicationLogs' | 'traces' | 'accelerators'

/**
 * The telemetry form: intent presets, one checkbox per signal (grouped infrastructure/application,
 * following each signal's own `domain` in TELEMETRY_SIGNALS), energy's and accelerators' source pickers,
 * pipeline processor controls, and the shared export target - built once so the install wizard and the
 * post-install "change telemetry" panel render the exact same fields from the exact same TelemetryInput
 * shape (see install.ts), instead of two hand-rolled copies that drift apart.
 * `measurementsOn` only feeds the networkLatency warning (see telemetryProblems); omit it where the caller
 * doesn't also control that separate extra.
 */
export default function TelemetryFields({
  value,
  onChange,
  measurementsOn,
  testIdPrefix = 'telemetry',
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  measurementsOn?: boolean
  testIdPrefix?: string
}) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const problems = telemetryProblems(value, measurementsOn)
  const infra = TELEMETRY_SIGNALS.filter((s) => s.domain === 'infrastructure')
  const app = TELEMETRY_SIGNALS.filter((s) => s.domain === 'application')
  const destinationNote = unsupportedDestinationNote(value.exportEndpoint)
  const exportPreset = EXPORT_PRESETS.find((p) => p.endpointPattern === value.exportEndpoint)
  const grantedRules = TELEMETRY_SIGNALS.filter((s) => (value as unknown as Record<string, boolean>)[s.id])

  const row = (s: (typeof TELEMETRY_SIGNALS)[number]) => {
    const id = s.id as SignalId
    return (
      <label key={id} className="flex cursor-pointer items-start gap-2.5 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 size-4 accent-[var(--color-accent)]"
          checked={value[id]}
          onChange={(e) => set(id, e.target.checked)}
          data-testid={`${testIdPrefix}-${id}`}
        />
        <span>
          <span className="text-nb-300">{s.label}</span>
          <InfoTip>{s.permissions}</InfoTip>
          <span className="block text-xs text-nb-500">{s.what}</span>
        </span>
      </label>
    )
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

      <div className="grid gap-3 sm:grid-cols-2">
        <fieldset className="space-y-2.5">
          <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-500">Infrastructure</legend>
          {infra.map(row)}
        </fieldset>
        <fieldset className="space-y-2.5">
          <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-500">Application</legend>
          {app.map(row)}
        </fieldset>
      </div>

      {value.energy && (
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
      )}

      {value.accelerators && (
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
        </div>
      )}

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
                exportProtocol: preset ? preset.protocol : value.exportProtocol,
                exportAuthHeaderName: preset && preset.headerName ? preset.headerName : value.exportAuthHeaderName,
              })
            }}
            placeholder="otel-gateway.example.com:4317"
            options={EXPORT_PRESETS.map((p) => ({ value: p.endpointPattern, label: p.label }))}
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
        {!destinationNote && exportPreset?.note && (
          <p className="text-xs text-nb-500 sm:col-span-2">{exportPreset.note}</p>
        )}
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
      </div>

      <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
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
      </div>

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
    </div>
  )
}

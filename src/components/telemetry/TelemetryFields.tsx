import { Field, InfoTip, Input, Select } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { telemetryProblems, type TelemetryInput } from '@/lib/install'

type SignalId = 'resourceUsage' | 'energy' | 'kubernetesState' | 'nodeRuntime' | 'networkLatency' | 'applicationMetrics' | 'systemLogs' | 'kubernetesEvents' | 'applicationLogs' | 'traces'

/**
 * The telemetry form: one checkbox per signal (grouped infrastructure/application, following each signal's
 * own `domain` in TELEMETRY_SIGNALS), energy's source picker, and the shared export target - built once so
 * the install wizard and the post-install "change telemetry" panel render the exact same fields from the
 * exact same TelemetryInput shape (see install.ts), instead of two hand-rolled copies that drift apart.
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
          <span className="block text-xs text-nb-500">{s.what}</span>
        </span>
      </label>
    )
  }

  return (
    <div className="space-y-4">
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

      <div className="grid gap-3 border-t border-nb-850 pt-3 sm:grid-cols-2">
        <Field label="Send telemetry to" hint="host:port of your OTLP receiver - an existing collector gateway or observability backend.">
          <Input
            value={value.exportEndpoint}
            onChange={(e) => set('exportEndpoint', e.target.value)}
            placeholder="otel-gateway.example.com:4317"
            data-testid={`${testIdPrefix}-export-endpoint`}
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

      {problems.length > 0 && (
        <p role="alert" className="text-xs text-bad" data-testid={`${testIdPrefix}-problems`}>
          {problems.join('. ')}.
        </p>
      )}
    </div>
  )
}

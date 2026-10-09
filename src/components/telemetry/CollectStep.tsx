import clsx from 'clsx'
import { Activity, FileText, Waypoints, type LucideIcon } from 'lucide-react'
import { Button, Field, ICON_SM, InfoTip, Input, Select } from '@/components/ui/primitives'
import { applyIntentPreset, PICKABLE_SIGNALS, TELEMETRY_INTENT_PRESETS, TELEMETRY_SIGNALS } from '@/lib/consent'
import type { TelemetryInput } from '@/lib/install'
import { LAYER_CARDS, LAYER_META, type Layer } from '@/lib/telemetryLayers'

export type SignalId = 'resourceUsage' | 'energy' | 'kubernetesState' | 'nodeRuntime' | 'networkLatency' | 'applicationMetrics' | 'systemLogs' | 'kubernetesEvents' | 'applicationLogs' | 'traces' | 'accelerators'

/** cluster/node scope needs no form control (cluster: physically unfilterable; node: the chart's own
 * nodeSelector/tolerations values, structured k8s scheduling objects that don't fit this form's --set
 * model) - so those two just get an honest caption here instead. application scope gets a real control,
 * the wizard's namespace scope. */
const scopeCaption = (s: (typeof TELEMETRY_SIGNALS)[number]): string | undefined => {
  if (s.scope === 'cluster') return 'Always cluster-wide - cannot be narrowed.'
  if (s.scope === 'node') return "Runs on every node - narrow which nodes with the chart's own nodeSelector/tolerations values, not from this form."
  return undefined
}

/** Energy's own source picker, shown once `energy` is on. */
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
        <Field label="Its Prometheus endpoint" hint="Host and port only. The collector adds /metrics itself; a scheme or a path stops it from starting.">
          <Input
            value={value.energyExistingEndpoint}
            onChange={(e) => set('energyExistingEndpoint', e.target.value)}
            placeholder="kepler.monitoring:9102"
            data-testid={`${testIdPrefix}-energy-endpoint`}
          />
        </Field>
      )}
    </div>
  )
}

/** Accelerators' own source picker and "apply the install's scope to GPU metrics" toggle, shown once `accelerators` is on. */
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
        <Field label="Its Prometheus endpoint" hint="Host and port only. The collector adds /metrics itself; a scheme or a path stops it from starting.">
          <Input
            value={value.acceleratorsExistingEndpoint}
            onChange={(e) => set('acceleratorsExistingEndpoint', e.target.value)}
            placeholder="dcgm-exporter.monitoring:9400"
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

/** One signal's checkbox row: the label, its permissions info tip, what it collects, and (cluster/node signals only) the caption explaining why there is no scope control for it. */
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


type Modality = 'metrics' | 'logs' | 'traces'

const MODALITY_META: Record<Modality, { label: string; icon: LucideIcon }> = {
  metrics: { label: 'Metrics', icon: Activity },
  logs: { label: 'Logs', icon: FileText },
  traces: { label: 'Traces', icon: Waypoints },
}
const MODALITY_ORDER: Modality[] = ['metrics', 'logs', 'traces']

const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x))

/**
 * Everything this wizard can collect, on one screen: grouped by layer (infrastructure, then application) and
 * inside each by modality (metrics, logs, traces), every signal one checkbox. This replaces the old
 * layer -> modality -> kind walk, which asked three questions to arrive at a list that is eleven rows long
 * in total - short enough to read at once, and much quicker to scan than to click through. Each group has
 * its own "Select all" / "Clear" and an "n of m" count; a handful of starting points (the same presets the
 * flat grid has) fill the whole list in one click and can then be adjusted row by row.
 *
 * Every checkbox writes straight into `value`, exactly like the flat grid does - nothing here is committed
 * later, so leaving the screen never loses a change.
 */
export default function CollectStep({
  value,
  onChange,
  testIdPrefix,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
}) {
  const rec = value as unknown as Record<string, boolean>
  const on = PICKABLE_SIGNALS.filter((s) => rec[s.id])
  const onIds = on.map((s) => s.id)
  const setMany = (ids: string[], v: boolean) => {
    const next = { ...value } as unknown as Record<string, unknown>
    for (const id of ids) next[id] = v
    onChange(next as unknown as TelemetryInput)
  }

  return (
    <div className="space-y-4" data-testid={`${testIdPrefix}-guided-step-collect`}>
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Start from" data-testid={`${testIdPrefix}-collect-presets`}>
        <span className="mr-1 text-xs text-nb-600">Start from:</span>
        {TELEMETRY_INTENT_PRESETS.map((p) => {
          const active = sameSet(onIds, p.signals)
          return (
            <button
              key={p.id}
              type="button"
              title={p.description}
              aria-pressed={active}
              onClick={() => onChange(applyIntentPreset(value, p))}
              data-testid={`${testIdPrefix}-collect-preset-${p.id}`}
              className={clsx(
                'rounded-full border px-2.5 py-1 text-xs transition-colors',
                active ? 'border-accent bg-accent-soft text-accent' : 'border-nb-850 bg-nb-925 text-nb-400 hover:border-nb-800 hover:text-nb-200',
              )}
            >
              {p.label}
            </button>
          )
        })}
      </div>

      {LAYER_CARDS.map((layer: Layer) => {
        const Icon = LAYER_META[layer].icon
        return (
          <section key={layer} className="rounded-xl border border-nb-850 bg-nb-925" data-testid={`${testIdPrefix}-collect-layer-${layer}`}>
            <header className="flex items-start gap-2.5 border-b border-nb-850 px-4 py-3">
              <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-nb-930 text-nb-500" aria-hidden>
                <Icon size={ICON_SM} />
              </span>
              <div>
                <h3 className="text-sm font-medium text-nb-200">{LAYER_META[layer].label}</h3>
                <p className="text-xs text-nb-500">{LAYER_META[layer].hint}</p>
              </div>
            </header>
            <div className="divide-y divide-nb-850">
              {MODALITY_ORDER.map((m) => {
                const signals = PICKABLE_SIGNALS.filter((s) => s.layer === layer && s.modality === m)
                if (signals.length === 0) return null
                const MIcon = MODALITY_META[m].icon
                const ids = signals.map((s) => s.id)
                const count = signals.filter((s) => rec[s.id]).length
                const base = `${testIdPrefix}-collect-${layer}-${m}`
                return (
                  <div key={m} className="space-y-2.5 px-4 py-3" data-testid={base}>
                    <div className="flex items-center gap-2 text-xs">
                      <MIcon size={ICON_SM} className="text-nb-500" aria-hidden />
                      <span className="font-medium text-nb-300">{MODALITY_META[m].label}</span>
                      <span className="text-nb-600" data-testid={`${base}-count`}>
                        {count} of {signals.length}
                      </span>
                      {signals.length > 1 && (
                        <span className="ml-auto flex items-center gap-1">
                          <Button size="sm" variant="ghost" disabled={count === signals.length} onClick={() => setMany(ids, true)} data-testid={`${base}-all`}>
                            Select all
                          </Button>
                          <Button size="sm" variant="ghost" disabled={count === 0} onClick={() => setMany(ids, false)} data-testid={`${base}-none`}>
                            Clear
                          </Button>
                        </span>
                      )}
                    </div>
                    <div className="space-y-2.5">
                      {signals.map((s) => (
                        <SignalRow key={s.id} signal={s} checked={rec[s.id]} onChange={(v) => setMany([s.id], v)} testIdPrefix={testIdPrefix} />
                      ))}
                    </div>
                    {signals.some((s) => s.id === 'energy') && value.energy && <EnergyFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
                    {signals.some((s) => s.id === 'accelerators') && value.accelerators && <AcceleratorsFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
                  </div>
                )
              })}
            </div>
          </section>
        )
      })}
    </div>
  )
}

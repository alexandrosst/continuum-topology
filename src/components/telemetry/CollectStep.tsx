import clsx from 'clsx'
import { Activity, FileText, Waypoints, type LucideIcon } from 'lucide-react'
import { Button, ICON_SM } from '@/components/ui/primitives'
import { applyIntentPreset, PICKABLE_SIGNALS, TELEMETRY_INTENT_PRESETS } from '@/lib/consent'
import type { TelemetryInput } from '@/lib/install'
import { LAYER_CARDS, LAYER_META, type Layer } from '@/lib/telemetryLayers'
import { AcceleratorsFields, EnergyFields, SignalRow } from './TelemetryFields'

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
  onContinue,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  onContinue: () => void
}) {
  const rec = value as unknown as Record<string, boolean>
  const on = PICKABLE_SIGNALS.filter((s) => rec[s.id])
  const onIds = on.map((s) => s.id)
  const setMany = (ids: string[], v: boolean) => {
    const next = { ...value } as unknown as Record<string, unknown>
    for (const id of ids) next[id] = v
    onChange(next as unknown as TelemetryInput)
  }
  const appCount = on.filter((s) => s.layer === 'application').length

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

      <div className="flex flex-wrap items-center gap-3 pt-1">
        <p className="text-xs text-nb-500" role="status" data-testid={`${testIdPrefix}-collect-count`}>
          {on.length === 0
            ? 'Nothing picked yet.'
            : `${on.length} of ${PICKABLE_SIGNALS.length} signals picked${appCount > 0 ? ' - the next step narrows which namespaces your applications are collected from.' : '.'}`}
        </p>
        <Button variant="primary" className="ml-auto" disabled={on.length === 0} onClick={onContinue} data-testid={`${testIdPrefix}-guided-continue`}>
          Continue
        </Button>
      </div>
    </div>
  )
}

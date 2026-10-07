import { ChevronDown, ChevronUp, Plus, X } from 'lucide-react'
import { useState } from 'react'
import { Button, Field, ICON_MD, ICON_SM, Input, Select } from '@/components/ui/primitives'
import {
  FILTER_OPS,
  newProcessorEntry,
  newTailSamplingPolicy,
  PROCESSOR_KINDS,
  processorKey,
  processorProblems,
  SIGNALS,
  type FilterCondition,
  type FilterConfig,
  type FilterOp,
  type ProcessorEntry,
  type ProcessorKind,
  type Signal,
  type TailSamplingConfig,
  type TailSamplingPolicy,
  type TailSamplingPolicyType,
  type TransformConfig,
  type TransformStatement,
} from '@/lib/processorCatalog'

/**
 * The OTel Collector extra-processor list: add, reorder, and configure the chart's `extraProcessors` -
 * inserted, in this order, right before `batch` in every pipeline that includes them (traces-only for
 * tail sampling, every pipeline for filter/transform - see processorTarget() in processorCatalog.ts, and
 * the values.yaml/telemetry-cluster-config.yaml comments on why tail sampling can't share the other two's
 * list). Reordering is plain up/down buttons rather than a drag library - the list is short in practice
 * and this keeps the feature dependency-free, per the plan (no sortable-list dependency existed in
 * package.json at the time this was written).
 *
 * Each row is a one-line summary; opening it shows a typed form for its kind, or - via the "raw JSON"
 * fallback - a textarea for anything the typed form can't express, the same guided-but-escapable pattern
 * this app already uses for scope overrides (GuidedScope.tsx) rather than a new paradigm. A non-empty raw
 * override replaces the typed config entirely (see processorBody() in processorCatalog.ts), so the typed
 * form hides itself while one is set instead of pretending both apply at once.
 */
export default function ProcessorEditor({
  entries,
  onChange,
  testIdPrefix,
}: {
  entries: ProcessorEntry[]
  onChange: (entries: ProcessorEntry[]) => void
  testIdPrefix: string
}) {
  const [openIds, setOpenIds] = useState<string[]>([])
  const problems = processorProblems(entries)

  const toggle = (id: string) => setOpenIds((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]))
  const update = (id: string, patch: Partial<ProcessorEntry>) => onChange(entries.map((e) => (e.id === id ? { ...e, ...patch } : e)))
  const remove = (id: string) => onChange(entries.filter((e) => e.id !== id))
  const move = (id: string, dir: -1 | 1) => {
    const i = entries.findIndex((e) => e.id === id)
    const j = i + dir
    if (i < 0 || j < 0 || j >= entries.length) return
    const next = [...entries]
    ;[next[i], next[j]] = [next[j], next[i]]
    onChange(next)
  }
  const add = (kind: ProcessorKind) => {
    const entry = newProcessorEntry(kind)
    onChange([...entries, entry])
    setOpenIds((cur) => [...cur, entry.id])
  }

  return (
    <div className="space-y-2.5">
      <p className="text-xs text-nb-500">
        Runs, in this order, after the chart&apos;s own safety and scope processors and before <code className="text-nb-400">batch</code>. Tail
        sampling only ever reaches the traces pipeline; filter and transform reach every pipeline this install exports to.
      </p>
      {entries.length === 0 && <p className="text-xs text-nb-600">No extra processors.</p>}
      {entries.length > 0 && (
        <ul className="space-y-1.5">
          {entries.map((entry, i) => (
            <ProcessorRow
              key={entry.id}
              entry={entry}
              index={i}
              count={entries.length}
              open={openIds.includes(entry.id)}
              onToggle={() => toggle(entry.id)}
              onChange={(patch) => update(entry.id, patch)}
              onRemove={() => remove(entry.id)}
              onMove={(dir) => move(entry.id, dir)}
              testIdPrefix={testIdPrefix}
            />
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-1.5">
        {PROCESSOR_KINDS.map((k) => (
          <Button key={k.id} type="button" size="sm" title={k.hint} onClick={() => add(k.id)} data-testid={`${testIdPrefix}-processor-add-${k.id}`}>
            <Plus size={ICON_SM} /> {k.label}
          </Button>
        ))}
      </div>
      {problems.length > 0 && (
        <p role="alert" className="text-xs text-bad" data-testid={`${testIdPrefix}-processor-problems`}>
          {problems.join('. ')}.
        </p>
      )}
    </div>
  )
}

function ProcessorRow({
  entry,
  index,
  count,
  open,
  onToggle,
  onChange,
  onRemove,
  onMove,
  testIdPrefix,
}: {
  entry: ProcessorEntry
  index: number
  count: number
  open: boolean
  onToggle: () => void
  onChange: (patch: Partial<ProcessorEntry>) => void
  onRemove: () => void
  onMove: (dir: -1 | 1) => void
  testIdPrefix: string
}) {
  const kindLabel = PROCESSOR_KINDS.find((k) => k.id === entry.kind)?.label ?? entry.kind
  const rawActive = entry.raw.trim() !== ''
  return (
    <li className="fade-in rounded-lg border border-nb-850" data-testid={`${testIdPrefix}-processor-row`}>
      <div className="flex items-center gap-1.5 px-2 py-1.5">
        <div className="flex flex-col">
          <button
            type="button"
            disabled={index === 0}
            onClick={() => onMove(-1)}
            className="text-nb-600 hover:text-nb-300 disabled:cursor-not-allowed disabled:opacity-30"
            aria-label={`Move ${entry.name || 'processor'} up`}
            data-testid={`${testIdPrefix}-processor-up-${entry.id}`}
          >
            <ChevronUp size={ICON_MD} />
          </button>
          <button
            type="button"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
            className="text-nb-600 hover:text-nb-300 disabled:cursor-not-allowed disabled:opacity-30"
            aria-label={`Move ${entry.name || 'processor'} down`}
            data-testid={`${testIdPrefix}-processor-down-${entry.id}`}
          >
            <ChevronDown size={ICON_MD} />
          </button>
        </div>
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="flex flex-1 items-center gap-2 overflow-hidden py-0.5 text-left"
          data-testid={`${testIdPrefix}-processor-toggle-${entry.id}`}
        >
          <span className="shrink-0 rounded bg-nb-940 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-nb-500">{kindLabel}</span>
          <span className="truncate text-sm text-nb-300">{entry.name || processorKey(entry)}</span>
          {rawActive && <span className="shrink-0 text-[10px] text-nb-600">raw</span>}
        </button>
        <button
          type="button"
          onClick={onRemove}
          className="shrink-0 rounded-md p-1.5 text-nb-500 transition-colors hover:bg-nb-850 hover:text-bad"
          aria-label={`Remove ${entry.name || kindLabel}`}
          data-testid={`${testIdPrefix}-processor-remove-${entry.id}`}
        >
          <X size={ICON_MD} />
        </button>
      </div>
      {open && (
        <div className="space-y-3 border-t border-nb-850 p-3">
          <Field label="Name" hint="A short slug - combines with the processor type to form its config key.">
            <Input
              value={entry.name}
              onChange={(e) => onChange({ name: e.target.value })}
              placeholder="drop_debug_logs"
              data-testid={`${testIdPrefix}-processor-name-${entry.id}`}
            />
          </Field>

          {!rawActive && entry.kind === 'filter' && (
            <FilterForm config={entry.config as FilterConfig} onChange={(config) => onChange({ config })} testIdPrefix={testIdPrefix} entryId={entry.id} />
          )}
          {!rawActive && entry.kind === 'tailSampling' && (
            <TailSamplingForm config={entry.config as TailSamplingConfig} onChange={(config) => onChange({ config })} testIdPrefix={testIdPrefix} entryId={entry.id} />
          )}
          {!rawActive && entry.kind === 'transform' && (
            <TransformForm config={entry.config as TransformConfig} onChange={(config) => onChange({ config })} testIdPrefix={testIdPrefix} entryId={entry.id} />
          )}

          <details data-testid={`${testIdPrefix}-processor-raw-details-${entry.id}`}>
            <summary className="cursor-pointer select-none text-xs text-nb-500 hover:text-nb-400 marker:content-none">
              {rawActive ? 'Raw JSON body (active - replaces the form above)' : 'Raw JSON body instead'}
            </summary>
            <textarea
              value={entry.raw}
              onChange={(e) => onChange({ raw: e.target.value })}
              rows={4}
              placeholder={'{"error_mode":"ignore","log_conditions":["log.attributes[\\"level\\"] == \\"debug\\""]}'}
              aria-label="Raw processor JSON body"
              className="mt-1.5 w-full rounded-md border border-nb-800 bg-nb-925 p-2 font-mono text-xs text-nb-300 placeholder:text-nb-600 focus:border-accent/60 focus:outline-none focus:ring-2 focus:ring-accent/20"
              data-testid={`${testIdPrefix}-processor-raw-${entry.id}`}
            />
          </details>
        </div>
      )}
    </li>
  )
}

function FilterForm({
  config,
  onChange,
  testIdPrefix,
  entryId,
}: {
  config: FilterConfig
  onChange: (c: FilterConfig) => void
  testIdPrefix: string
  entryId: string
}) {
  const updateCondition = (i: number, patch: Partial<FilterCondition>) =>
    onChange({ ...config, conditions: config.conditions.map((c, j) => (j === i ? { ...c, ...patch } : c)) })
  const removeCondition = (i: number) => onChange({ ...config, conditions: config.conditions.filter((_, j) => j !== i) })
  const addCondition = () => onChange({ ...config, conditions: [...config.conditions, { field: '', op: 'eq', value: '' }] })
  return (
    <div className="space-y-2">
      <Field label="Signal">
        <Select
          value={config.signal}
          onChange={(e) => onChange({ ...config, signal: e.target.value as Signal })}
          data-testid={`${testIdPrefix}-processor-filter-signal-${entryId}`}
        >
          {SIGNALS.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label}
            </option>
          ))}
        </Select>
      </Field>
      <p className="text-xs text-nb-500">Drops a record when any condition below matches (OR).</p>
      <div className="space-y-1.5">
        {config.conditions.map((c, i) => (
          <div key={i} className="fade-in flex items-center gap-1.5">
            <Select
              value={c.level ?? 'record'}
              onChange={(e) => updateCondition(i, { level: e.target.value as 'record' | 'resource' })}
              className="w-28 shrink-0"
              aria-label="Where the attribute is"
              title="A resource attribute describes where the data came from (k8s.namespace.name, service.name); the others belong to the data point, log line or span itself."
              data-testid={`${testIdPrefix}-processor-filter-level-${entryId}-${i}`}
            >
              <option value="record">{config.signal === 'metric' ? 'Data point' : config.signal === 'log' ? 'Log line' : 'Span'}</option>
              <option value="resource">Resource</option>
            </Select>
            <Input
              value={c.field}
              onChange={(e) => updateCondition(i, { field: e.target.value })}
              placeholder="attribute"
              className="min-w-0 flex-1"
              aria-label="Attribute"
              data-testid={`${testIdPrefix}-processor-filter-field-${entryId}-${i}`}
            />
            <Select
              value={c.op}
              onChange={(e) => updateCondition(i, { op: e.target.value as FilterOp })}
              className="w-40 shrink-0"
              aria-label="Comparison"
              data-testid={`${testIdPrefix}-processor-filter-op-${entryId}-${i}`}
            >
              {FILTER_OPS.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </Select>
            <Input
              value={c.value}
              onChange={(e) => updateCondition(i, { value: e.target.value })}
              placeholder="value"
              className="min-w-0 flex-1"
              aria-label="Value"
              data-testid={`${testIdPrefix}-processor-filter-value-${entryId}-${i}`}
            />
            <button
              type="button"
              onClick={() => removeCondition(i)}
              className="shrink-0 rounded-md p-1.5 text-nb-500 transition-colors hover:bg-nb-850 hover:text-bad"
              aria-label="Remove condition"
              data-testid={`${testIdPrefix}-processor-filter-remove-${entryId}-${i}`}
            >
              <X size={ICON_MD} />
            </button>
          </div>
        ))}
      </div>
      <Button type="button" size="sm" onClick={addCondition} data-testid={`${testIdPrefix}-processor-filter-add-${entryId}`}>
        <Plus size={ICON_SM} /> Condition
      </Button>
    </div>
  )
}

const POLICY_TYPES: { id: TailSamplingPolicyType; label: string }[] = [
  { id: 'probabilistic', label: 'Probabilistic %' },
  { id: 'statusCode', label: 'Status code' },
  { id: 'latency', label: 'Latency threshold' },
]

function TailSamplingForm({
  config,
  onChange,
  testIdPrefix,
  entryId,
}: {
  config: TailSamplingConfig
  onChange: (c: TailSamplingConfig) => void
  testIdPrefix: string
  entryId: string
}) {
  const updatePolicy = (i: number, patch: Partial<TailSamplingPolicy>) =>
    onChange({ ...config, policies: config.policies.map((p, j) => (j === i ? { ...p, ...patch } : p)) })
  const removePolicy = (i: number) => onChange({ ...config, policies: config.policies.filter((_, j) => j !== i) })
  const addPolicy = () => onChange({ ...config, policies: [...config.policies, newTailSamplingPolicy()] })
  return (
    <div className="space-y-2">
      <Field label="Decision wait (seconds)" hint="How long the collector holds a trace's spans before deciding whether to keep it.">
        <Input
          type="number"
          min={0}
          value={config.decisionWaitSeconds}
          onChange={(e) => onChange({ ...config, decisionWaitSeconds: e.target.valueAsNumber || 0 })}
          className="w-32"
          data-testid={`${testIdPrefix}-processor-ts-wait-${entryId}`}
        />
      </Field>
      <p className="text-xs text-nb-500">A trace is kept if it matches any policy below (OR).</p>
      <div className="space-y-2">
        {config.policies.map((p, i) => (
          <div key={i} className="fade-in space-y-1.5 rounded-md border border-nb-850 p-2">
            <div className="flex items-center gap-1.5">
              <Input
                value={p.name}
                onChange={(e) => updatePolicy(i, { name: e.target.value })}
                placeholder="policy name"
                className="min-w-0 flex-1"
                aria-label="Policy name"
                data-testid={`${testIdPrefix}-processor-ts-name-${entryId}-${i}`}
              />
              <Select
                value={p.type}
                onChange={(e) => updatePolicy(i, { type: e.target.value as TailSamplingPolicyType })}
                className="w-44 shrink-0"
                aria-label="Policy type"
                data-testid={`${testIdPrefix}-processor-ts-type-${entryId}-${i}`}
              >
                {POLICY_TYPES.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.label}
                  </option>
                ))}
              </Select>
              <button
                type="button"
                onClick={() => removePolicy(i)}
                className="shrink-0 rounded-md p-1.5 text-nb-500 transition-colors hover:bg-nb-850 hover:text-bad"
                aria-label="Remove policy"
                data-testid={`${testIdPrefix}-processor-ts-remove-${entryId}-${i}`}
              >
                <X size={ICON_MD} />
              </button>
            </div>
            {p.type === 'probabilistic' && (
              <Input
                type="number"
                min={0}
                max={100}
                value={p.probabilisticPercent}
                onChange={(e) => updatePolicy(i, { probabilisticPercent: e.target.valueAsNumber || 0 })}
                className="w-32"
                aria-label="Sampling percentage"
                data-testid={`${testIdPrefix}-processor-ts-percent-${entryId}-${i}`}
              />
            )}
            {p.type === 'statusCode' && (
              <Input
                value={p.statusCodes}
                onChange={(e) => updatePolicy(i, { statusCodes: e.target.value })}
                placeholder="ERROR, UNSET"
                aria-label="Status codes"
                data-testid={`${testIdPrefix}-processor-ts-statuscodes-${entryId}-${i}`}
              />
            )}
            {p.type === 'latency' && (
              <Input
                type="number"
                min={0}
                value={p.latencyThresholdMs}
                onChange={(e) => updatePolicy(i, { latencyThresholdMs: e.target.valueAsNumber || 0 })}
                className="w-32"
                aria-label="Latency threshold in milliseconds"
                data-testid={`${testIdPrefix}-processor-ts-latency-${entryId}-${i}`}
              />
            )}
          </div>
        ))}
      </div>
      <Button type="button" size="sm" onClick={addPolicy} data-testid={`${testIdPrefix}-processor-ts-add-${entryId}`}>
        <Plus size={ICON_SM} /> Policy
      </Button>
    </div>
  )
}

function TransformForm({
  config,
  onChange,
  testIdPrefix,
  entryId,
}: {
  config: TransformConfig
  onChange: (c: TransformConfig) => void
  testIdPrefix: string
  entryId: string
}) {
  const updateStatement = (i: number, patch: Partial<TransformStatement>) =>
    onChange({ ...config, statements: config.statements.map((s, j) => (j === i ? { ...s, ...patch } : s)) })
  const removeStatement = (i: number) => onChange({ ...config, statements: config.statements.filter((_, j) => j !== i) })
  const addStatement = () => onChange({ ...config, statements: [...config.statements, { action: 'set', key: '', value: '' }] })
  return (
    <div className="space-y-2">
      <Field label="Signal">
        <Select
          value={config.signal}
          onChange={(e) => onChange({ ...config, signal: e.target.value as Signal })}
          data-testid={`${testIdPrefix}-processor-transform-signal-${entryId}`}
        >
          {SIGNALS.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label}
            </option>
          ))}
        </Select>
      </Field>
      <div className="space-y-1.5">
        {config.statements.map((s, i) => (
          <div key={i} className="fade-in flex items-center gap-1.5">
            <Select
              value={s.action}
              onChange={(e) => updateStatement(i, { action: e.target.value as TransformStatement['action'] })}
              className="w-28 shrink-0"
              aria-label="Action"
              data-testid={`${testIdPrefix}-processor-transform-action-${entryId}-${i}`}
            >
              <option value="set">Set</option>
              <option value="delete">Delete</option>
            </Select>
            <Input
              value={s.key}
              onChange={(e) => updateStatement(i, { key: e.target.value })}
              placeholder="attribute"
              className="min-w-0 flex-1"
              aria-label="Attribute key"
              data-testid={`${testIdPrefix}-processor-transform-key-${entryId}-${i}`}
            />
            {s.action === 'set' && (
              <Input
                value={s.value}
                onChange={(e) => updateStatement(i, { value: e.target.value })}
                placeholder="value"
                className="min-w-0 flex-1"
                aria-label="Attribute value"
                data-testid={`${testIdPrefix}-processor-transform-value-${entryId}-${i}`}
              />
            )}
            <button
              type="button"
              onClick={() => removeStatement(i)}
              className="shrink-0 rounded-md p-1.5 text-nb-500 transition-colors hover:bg-nb-850 hover:text-bad"
              aria-label="Remove statement"
              data-testid={`${testIdPrefix}-processor-transform-remove-${entryId}-${i}`}
            >
              <X size={ICON_MD} />
            </button>
          </div>
        ))}
      </div>
      <Button type="button" size="sm" onClick={addStatement} data-testid={`${testIdPrefix}-processor-transform-add-${entryId}`}>
        <Plus size={ICON_SM} /> Statement
      </Button>
    </div>
  )
}

// The OTel Collector processor pipeline editor's data model: typed configs for a small, common-case set of
// extra processors (filter, tail_sampling, transform), each compiling to the exact raw processor body the
// chart's own extraProcessors/extraProcessorNames escape hatch already accepts (see values.yaml and
// telemetry-cluster-config.yaml in backend/internal/chart/continuum-agent) - this file only ever produces
// what a person could otherwise have typed into that escape hatch by hand, just with a form and validation
// in front of it. A `raw` override on any entry bypasses the typed config entirely, for anything a
// three-kind catalog can't express (an unusual OTTL statement, a tail_sampling policy type not listed here).
//
// One correctness rule that isn't obvious from the chart alone: tail_sampling only implements the traces
// processor interface, so a tailSampling entry is routed to `extraTracesProcessorNames` (traces pipeline
// only), never the shared `extraProcessorNames` (every pipeline) that filter/transform entries use - see
// processorTarget() below, and the values.yaml/telemetry-cluster-config.yaml comments on why.

export type ProcessorKind = 'filter' | 'tailSampling' | 'transform'

/** The OTel processor "type/" prefix each kind renders as - combined with a person-given name to form the
 * actual key in the collector config's `processors:` map (see processorKey). */
const BASE_TYPE: Record<ProcessorKind, string> = { filter: 'filter', tailSampling: 'tail_sampling', transform: 'transform' }

export const PROCESSOR_KINDS: { id: ProcessorKind; label: string; hint: string }[] = [
  { id: 'filter', label: 'Filter', hint: 'Drop records matching a condition' },
  { id: 'tailSampling', label: 'Tail sampling', hint: 'Keep a subset of traces by policy - traces only' },
  { id: 'transform', label: 'Transform', hint: 'Set or remove an attribute' },
]

export type Signal = 'metric' | 'log' | 'trace'
export const SIGNALS: { id: Signal; label: string }[] = [
  { id: 'metric', label: 'Metrics' },
  { id: 'log', label: 'Logs' },
  { id: 'trace', label: 'Traces' },
]

export type FilterOp = 'eq' | 'neq' | 'matches'
export const FILTER_OPS: { id: FilterOp; label: string }[] = [
  { id: 'eq', label: 'equals' },
  { id: 'neq', label: 'does not equal' },
  { id: 'matches', label: 'matches (regex)' },
]
export interface FilterCondition {
  field: string
  op: FilterOp
  value: string
}
/** `signal` picks which condition field this compiles to (metric_conditions/log_conditions/trace_conditions
 * - the exact flat field names this chart's own filter/scope_* processors already use, see
 * telemetry-cluster-config.yaml). Conditions are ORed together and matching one DROPS the record - the
 * same semantics the chart's own scope filter documents itself with. */
export interface FilterConfig {
  signal: Signal
  conditions: FilterCondition[]
}
export const emptyFilterConfig: FilterConfig = { signal: 'metric', conditions: [] }

export type TailSamplingPolicyType = 'probabilistic' | 'statusCode' | 'latency'
export interface TailSamplingPolicy {
  name: string
  type: TailSamplingPolicyType
  probabilisticPercent: number
  statusCodes: string
  latencyThresholdMs: number
}
export interface TailSamplingConfig {
  decisionWaitSeconds: number
  policies: TailSamplingPolicy[]
}
export const emptyTailSamplingConfig: TailSamplingConfig = { decisionWaitSeconds: 10, policies: [] }
export const newTailSamplingPolicy = (): TailSamplingPolicy => ({ name: '', type: 'probabilistic', probabilisticPercent: 10, statusCodes: 'ERROR', latencyThresholdMs: 500 })

export type TransformAction = 'set' | 'delete'
export interface TransformStatement {
  action: TransformAction
  key: string
  value: string
}
export interface TransformConfig {
  signal: Signal
  statements: TransformStatement[]
}
export const emptyTransformConfig: TransformConfig = { signal: 'trace', statements: [] }

export type ProcessorConfig = FilterConfig | TailSamplingConfig | TransformConfig

export interface ProcessorEntry {
  id: string
  kind: ProcessorKind
  /** A short slug identifying this processor instance, e.g. "drop_debug" - combined with its kind's base
   * OTel type to form the config key (see processorKey). Must be unique among an install's processors. */
  name: string
  config: ProcessorConfig
  /** Raw processor body, as JSON - used verbatim in place of `config` when non-empty. */
  raw: string
}

let entrySeq = 0
const newEntryId = () => `proc-${++entrySeq}`

export function newProcessorEntry(kind: ProcessorKind): ProcessorEntry {
  const config: ProcessorConfig = kind === 'filter' ? emptyFilterConfig : kind === 'tailSampling' ? emptyTailSamplingConfig : emptyTransformConfig
  return { id: newEntryId(), kind, name: '', config, raw: '' }
}

const slug = (s: string) =>
  s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, '_')
    .replace(/^_+|_+$/g, '')

/** The key this entry renders as in the collector config's `processors:` map, and the name referenced in
 * whichever pipeline(s) it's routed to (see processorTarget). Falls back to the entry's own id when the
 * person hasn't named it yet, so an unnamed draft still renders to something valid rather than colliding
 * with another unnamed one. */
export function processorKey(entry: ProcessorEntry): string {
  return `${BASE_TYPE[entry.kind]}/${slug(entry.name) || entry.id}`
}

/** Which extra-processor values list this entry belongs in. tailSampling only implements the traces
 * processor interface - referencing it from a metrics or logs pipeline makes the collector refuse to
 * start - so it alone is routed to the traces-only list; filter and transform both implement every
 * signal's processor interface and are safe in the shared, every-pipeline list. */
export function processorTarget(entry: ProcessorEntry): 'extraProcessorNames' | 'extraTracesProcessorNames' {
  return entry.kind === 'tailSampling' ? 'extraTracesProcessorNames' : 'extraProcessorNames'
}

const CONDITION_FIELD: Record<Signal, string> = { metric: 'metric_conditions', log: 'log_conditions', trace: 'trace_conditions' }
const compileCondition = (c: FilterCondition): string => {
  const attr = `attributes["${c.field}"]`
  if (c.op === 'eq') return `${attr} == "${c.value}"`
  if (c.op === 'neq') return `${attr} != "${c.value}"`
  return `IsMatch(${attr}, "${c.value}")`
}

const STATEMENT_FIELD: Record<Signal, string> = { metric: 'metric_statements', log: 'log_statements', trace: 'trace_statements' }
const STATEMENT_CONTEXT: Record<Signal, string> = { metric: 'datapoint', log: 'log', trace: 'span' }
const compileStatement = (s: TransformStatement): string => (s.action === 'set' ? `set(attributes["${s.key}"], "${s.value}")` : `delete_key(attributes, "${s.key}")`)

/** The processor's raw config body (what lands under its key in the collector config's `processors:` map),
 * either from `raw` verbatim (parsed as JSON) or generated from the typed config. Returns `{}` for
 * unparseable raw JSON - paired with processorProblems() below, which is what actually surfaces that to
 * the person, so this stays a pure "best effort" builder rather than throwing mid-render. */
export function processorBody(entry: ProcessorEntry): unknown {
  if (entry.raw.trim()) {
    try {
      return JSON.parse(entry.raw)
    } catch {
      return {}
    }
  }
  if (entry.kind === 'filter') {
    const c = entry.config as FilterConfig
    return { error_mode: 'ignore', [CONDITION_FIELD[c.signal]]: c.conditions.map(compileCondition) }
  }
  if (entry.kind === 'tailSampling') {
    const c = entry.config as TailSamplingConfig
    return {
      decision_wait: `${c.decisionWaitSeconds}s`,
      policies: c.policies.map((p) => {
        if (p.type === 'probabilistic') return { name: p.name, type: 'probabilistic', probabilistic: { sampling_percentage: p.probabilisticPercent } }
        if (p.type === 'statusCode') return { name: p.name, type: 'status_code', status_code: { status_codes: p.statusCodes.split(',').map((s) => s.trim()).filter(Boolean) } }
        return { name: p.name, type: 'latency', latency: { threshold_ms: p.latencyThresholdMs } }
      }),
    }
  }
  const c = entry.config as TransformConfig
  return { [STATEMENT_FIELD[c.signal]]: [{ context: STATEMENT_CONTEXT[c.signal], statements: c.statements.map(compileStatement) }] }
}

/** The full `telemetry.processors.extraProcessors` map, ready to JSON-encode into a `--set-json` flag -
 * every entry's body, keyed by its processorKey(), regardless of which pipeline list it's routed to. */
export function buildExtraProcessors(entries: ProcessorEntry[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const e of entries) out[processorKey(e)] = processorBody(e)
  return out
}

/** What is wrong with the processor list, in words a person can act on; empty when it is fine. */
export function processorProblems(entries: ProcessorEntry[]): string[] {
  const out: string[] = []
  const seen = new Map<string, number>()
  for (const e of entries) {
    const key = processorKey(e)
    seen.set(key, (seen.get(key) ?? 0) + 1)
    if (!e.name.trim()) out.push('Every processor needs a name.')
    if (e.raw.trim()) {
      try {
        JSON.parse(e.raw)
      } catch {
        out.push(`"${e.name || e.id}"'s raw override is not valid JSON.`)
      }
    } else if (e.kind === 'filter' && (e.config as FilterConfig).conditions.length === 0) {
      out.push(`"${e.name || e.id}" (filter) has no conditions - it would drop nothing.`)
    } else if (e.kind === 'tailSampling' && (e.config as TailSamplingConfig).policies.length === 0) {
      out.push(`"${e.name || e.id}" (tail sampling) has no policies - it would sample nothing.`)
    } else if (e.kind === 'transform' && (e.config as TransformConfig).statements.length === 0) {
      out.push(`"${e.name || e.id}" (transform) has no statements - it would do nothing.`)
    }
  }
  for (const [key, count] of seen) if (count > 1) out.push(`More than one processor renders to "${key}" - give each a distinct name.`)
  return out
}

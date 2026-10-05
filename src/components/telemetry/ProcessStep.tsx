import clsx from 'clsx'
import { ChevronDown, ChevronLeft, Plus, X } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button, Field, ICON_MD, ICON_SM, InfoTip, Input } from '@/components/ui/primitives'
import { scopeTag, tagProblems, TAG_LIMIT, telemetryActive, type DebugVerbosity, type TagEntry, type TelemetryInput } from '@/lib/install'
import ProcessorEditor from './ProcessorEditor'

/** A titled block of the Process step - the same bordered-box language as the rest of the wizard. */
function Card({ title, hint, children, testId }: { title: string; hint?: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <section className="space-y-2.5 rounded-xl border border-nb-850 bg-nb-925 p-3.5" data-testid={testId}>
      <div>
        <h4 className="text-xs font-medium uppercase tracking-wide text-nb-400">{title}</h4>
        {hint && <p className="mt-0.5 text-xs text-nb-500">{hint}</p>}
      </div>
      {children}
    </section>
  )
}

/** A name = value pair shown as a chip: what is added, read-only. */
function Fact({ name, value, testId }: { name: string; value?: string; testId?: string }) {
  return (
    <span className="inline-flex max-w-full items-center gap-1 rounded-md border border-nb-800 bg-nb-930 px-2 py-0.5 font-mono text-[11px] text-nb-300" data-testid={testId}>
      <span className="text-nb-400">{name}</span>
      {value !== undefined && (
        <>
          <span className="text-nb-600">=</span>
          <span className="truncate">{value}</span>
        </>
      )}
    </span>
  )
}

/**
 * The tags a person puts on everything this install emits. Written to the chart as one list, inserted rather
 * than overwritten (a name an application already sets on its own telemetry keeps the application's value),
 * and the `continuum.` names are refused because those are what Continuum adds itself. `suggestion` is a
 * name and value worth offering in one click (the cluster's own name), left out once it is already there.
 */
export function TagEditor({ value, onChange, testIdPrefix, suggestion }: { value: TelemetryInput; onChange: (v: TelemetryInput) => void; testIdPrefix: string; suggestion?: TagEntry }) {
  const tags = value.tags
  const set = (next: TagEntry[]) => onChange({ ...value, tags: next })
  const problems = tagProblems(tags)
  const filled = tags.filter((t) => t.key.trim() !== '' || t.value.trim() !== '').length
  const offer = suggestion && !tags.some((t) => t.key.trim() === suggestion.key) ? suggestion : undefined
  return (
    <div className="space-y-2" data-testid={`${testIdPrefix}-tags`}>
      {tags.length > 0 && (
        <ul className="space-y-1.5">
          {tags.map((t, i) => (
            <li key={i} className="flex items-center gap-2">
              <Input value={t.key} onChange={(e) => set(tags.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} placeholder="name, e.g. team" aria-label={`Tag ${i + 1} name`} className="w-2/5 font-mono text-xs" data-testid={`${testIdPrefix}-tag-key-${i}`} />
              <span className="text-nb-600" aria-hidden>=</span>
              <Input value={t.value} onChange={(e) => set(tags.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} placeholder="value" aria-label={`Tag ${i + 1} value`} className="min-w-0 flex-1 font-mono text-xs" data-testid={`${testIdPrefix}-tag-value-${i}`} />
              <button type="button" onClick={() => set(tags.filter((_, j) => j !== i))} aria-label={`Remove tag ${i + 1}`} className="rounded p-1 text-nb-600 hover:bg-nb-930 hover:text-nb-300" data-testid={`${testIdPrefix}-tag-remove-${i}`}>
                <X size={ICON_MD} aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" disabled={filled >= TAG_LIMIT || tags.length >= TAG_LIMIT + 2} onClick={() => set([...tags, { key: '', value: '' }])} data-testid={`${testIdPrefix}-tag-add`}>
          <Plus size={ICON_SM} /> Add a tag
        </Button>
        {offer && (
          <button type="button" className="rounded-md border border-dashed border-nb-800 px-2 py-1 font-mono text-[11px] text-nb-400 hover:border-nb-700 hover:text-nb-300" onClick={() => set([...tags, offer])} data-testid={`${testIdPrefix}-tag-suggest`}>
            + {offer.key} = {offer.value}
          </button>
        )}
        <span className="ml-auto text-xs text-nb-600" data-testid={`${testIdPrefix}-tag-count`}>{filled} of {TAG_LIMIT}</span>
      </div>
      {problems.length > 0 && (
        <ul role="alert" className="space-y-0.5 text-xs text-bad" data-testid={`${testIdPrefix}-tag-problems`}>
          {problems.map((p) => <li key={p}>{p}</li>)}
        </ul>
      )}
    </div>
  )
}

const DEBUG_CHOICES: { id: DebugVerbosity; label: string; hint: string }[] = [
  { id: '', label: 'Off', hint: 'Nothing extra is logged.' },
  { id: 'basic', label: 'Count only', hint: 'The collector’s log says how many records passed, never what was in them. Enough to see that data is flowing.' },
  { id: 'detailed', label: 'Full content', hint: 'Every record is written to the collector’s log.' },
]

/** The debug exporter: a copy of what is sent, to the collector's own log, for checking that anything flows at all. */
export function DebugChoice({ value, onChange, testIdPrefix }: { value: TelemetryInput; onChange: (v: TelemetryInput) => void; testIdPrefix: string }) {
  return (
    <div className="space-y-2" role="radiogroup" aria-label="Debug output" data-testid={`${testIdPrefix}-debug`}>
      <div className="grid gap-2 sm:grid-cols-3">
        {DEBUG_CHOICES.map((c) => (
          <label key={c.id || 'off'} className={clsx('flex cursor-pointer items-start gap-2 rounded-lg border p-2.5 text-sm transition-colors', value.debugVerbosity === c.id ? 'border-accent bg-accent-soft' : 'border-nb-850 hover:border-nb-800')}>
            <input type="radio" name={`${testIdPrefix}-debug`} className="mt-0.5 accent-[var(--color-accent)]" checked={value.debugVerbosity === c.id} onChange={() => onChange({ ...value, debugVerbosity: c.id })} data-testid={`${testIdPrefix}-debug-${c.id || 'off'}`} />
            <span>
              <span className="block text-nb-200">{c.label}{c.id === 'basic' && <span className="ml-1.5 text-[11px] text-nb-500">default</span>}</span>
              <span className="block text-xs text-nb-500">{c.hint}</span>
            </span>
          </label>
        ))}
      </div>
      {value.debugVerbosity === 'detailed' && (
        <p role="alert" className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" data-testid={`${testIdPrefix}-debug-warning`}>
          Full content can include whatever your telemetry carries, secrets and personal data among it, in a log other people can read. If this collector’s own logs are collected as telemetry, they feed straight back into it. Use it briefly, then switch back.
        </p>
      )}
    </div>
  )
}

/**
 * The guided wizard's Process step: what happens to telemetry between being collected and leaving the cluster.
 * Everything has a sensible default, so most people read it and press Continue. First comes what Continuum
 * adds on its own (and why it can be trusted), then what you add, then masking and cost, then a way to check
 * it works. Extra processors - filters, tail sampling - stay behind a disclosure: rarely needed.
 */
export default function ProcessStep({
  value,
  onChange,
  testIdPrefix,
  clusterName,
  onBack,
  onContinue,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** The cluster's name, when known - offered as a one-click `k8s.cluster.name` tag. */
  clusterName?: string
  onBack: () => void
  onContinue: () => void
}) {
  const p = `${testIdPrefix}-guided`
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const scope = scopeTag(value)
  const blocked = telemetryActive(value) && tagProblems(value.tags).length > 0
  return (
    <div className="space-y-3" data-testid={`${p}-step-process`}>
      <div>
        <h3 className="text-sm font-medium text-nb-200">What happens before it leaves the cluster?</h3>
        <p className="mt-0.5 text-xs text-nb-500">The defaults are safe. Change what you need, or just continue.</p>
      </div>

      <Card title="Added to everything" hint="So whoever reads this data can tell where it came from. These win over anything else that sets the same names." testId={`${p}-process-auto`}>
        <div className="flex flex-wrap gap-1.5">
          <Fact name="continuum.org.id" testId={`${p}-fact-org`} />
          <Fact name="continuum.cluster.id" testId={`${p}-fact-cluster`} />
          {scope && <Fact name="continuum.scope" value={scope} testId={`${p}-fact-scope`} />}
        </div>
        {scope === '' && <p className="text-xs text-nb-500">No scope tag: this covers everything the agent can see.</p>}
      </Card>

      <Card title="Your tags" hint="Added only where the telemetry doesn’t already carry that name, so they can never overwrite what an application says about itself." testId={`${p}-process-tags`}>
        <TagEditor value={value} onChange={onChange} testIdPrefix={testIdPrefix} suggestion={clusterName ? { key: 'k8s.cluster.name', value: clusterName } : undefined} />
      </Card>

      <Card title="Privacy and cost" testId={`${p}-process-privacy`}>
        <div className="space-y-3">
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <input type="checkbox" className="mt-0.5 size-4 accent-[var(--color-accent)]" checked={value.redaction} onChange={(e) => set('redaction', e.target.checked)} data-testid={`${testIdPrefix}-redaction`} />
            <span>
              <span className="text-nb-300">Mask likely secrets</span>
              <InfoTip>On by default. Masks the values of attributes whose key looks like a token/password/secret/API key before anything leaves the cluster - the receiver has no auth of its own unless you set one up separately.</InfoTip>
              <span className="block text-xs text-nb-500">Recommended: turning this off sends attribute values through unmasked.</span>
            </span>
          </label>
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <input type="checkbox" className="mt-0.5 size-4 accent-[var(--color-accent)]" checked={value.resourceDetection} onChange={(e) => set('resourceDetection', e.target.checked)} data-testid={`${testIdPrefix}-resource-detection`} />
            <span>
              <span className="text-nb-300">Enrich with collector environment</span>
              <span className="block text-xs text-nb-500">Adds resource attributes about the collector’s own runtime environment.</span>
            </span>
          </label>
          {value.traces && (
            <Field label="Traces sampling %" hint="100 keeps every span (the default). Lower it to cut trace volume and cost.">
              <Input type="number" min={0} max={100} value={value.tracesSamplingPercent} onChange={(e) => set('tracesSamplingPercent', e.target.valueAsNumber || 0)} className="w-28" data-testid={`${testIdPrefix}-traces-sampling`} />
            </Field>
          )}
        </div>
      </Card>

      <Card title="Check that it works" hint="A copy of what is sent, in the collector’s own log." testId={`${p}-process-debug`}>
        <DebugChoice value={value} onChange={onChange} testIdPrefix={testIdPrefix} />
      </Card>

      <details className="group rounded-lg border border-nb-850" data-testid={`${p}-process-extra`}>
        <summary className="flex cursor-pointer select-none items-center justify-between px-3 py-2.5 text-xs font-medium text-nb-400 marker:content-none hover:text-nb-300">
          <span>Extra processors{value.extraProcessors.length > 0 && ` (${value.extraProcessors.length})`}</span>
          <ChevronDown size={ICON_MD} className="text-nb-500 transition-transform group-open:rotate-180" aria-hidden />
        </summary>
        <div className="border-t border-nb-850 p-3">
          <ProcessorEditor entries={value.extraProcessors} onChange={(extraProcessors) => set('extraProcessors', extraProcessors)} testIdPrefix={testIdPrefix} />
        </div>
      </details>

      <div className="flex items-center gap-2 pt-1">
        <Button variant="ghost" size="sm" onClick={onBack} data-testid={`${testIdPrefix}-guided-back`}>
          <ChevronLeft size={ICON_SM} /> Back
        </Button>
        <Button variant="primary" className="ml-auto" disabled={blocked} onClick={onContinue} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
      </div>
    </div>
  )
}

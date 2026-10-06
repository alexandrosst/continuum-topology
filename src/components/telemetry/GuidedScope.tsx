import clsx from 'clsx'
import { useState } from 'react'
import { Button, Field, Input, SectionLabel, TagsInput } from '@/components/ui/primitives'
import { combinedScope, emptyScopeOverride, scopeNarrows, scopeOverlap, type ScopeOverrideInput, type TelemetryInput, type WorkloadScope } from '@/lib/install'
import { useTopology } from '@/store/topology'

const APP_SCOPED_KINDS = ['applicationMetrics', 'applicationLogs', 'traces'] as const
type AppScopedKind = (typeof APP_SCOPED_KINDS)[number]
const SCOPE_FIELD: Record<AppScopedKind, 'applicationMetricsScope' | 'applicationLogsScope' | 'tracesScope'> = {
  applicationMetrics: 'applicationMetricsScope',
  applicationLogs: 'applicationLogsScope',
  traces: 'tracesScope',
}
const KIND_LABEL: Record<AppScopedKind, string> = {
  applicationMetrics: 'Application metrics',
  applicationLogs: 'Application logs',
  traces: 'Traces',
}

interface Draft {
  id: string
  name: string
  namespaces: string[]
  exclude: string[]
  workloads: WorkloadScope[]
}

let draftSeq = 0
const newDraftId = () => `draft-${++draftSeq}`

const sortedWorkloads = (w: WorkloadScope[]) =>
  [...w].map((e) => ({ namespace: e.namespace, names: [...e.names].sort() })).sort((x, y) => x.namespace.localeCompare(y.namespace))

/** Workload narrowing only means something for a namespace the scope names, so a namespace taken out of the list takes its workloads with it. */
const pruneWorkloads = (w: WorkloadScope[], namespaces: string[]) => w.filter((e) => namespaces.includes(e.namespace))

const sameScope = (a: ScopeOverrideInput, b: ScopeOverrideInput) =>
  a.namespaces.length === b.namespaces.length &&
  a.exclude.length === b.exclude.length &&
  a.namespaces.every((n) => b.namespaces.includes(n)) &&
  a.exclude.every((n) => b.exclude.includes(n)) &&
  JSON.stringify(sortedWorkloads(a.workloads)) === JSON.stringify(sortedWorkloads(b.workloads))

/**
 * Seeds the wizard's draft list from whatever per-kind scope overrides already exist on the value (an
 * existing install being edited, or a value carried over from switching into guided mode mid-form) - one
 * draft per distinct non-empty override, so re-entering guided mode never silently drops one. Purely a
 * starting point for in-memory wizard state: nothing here is written back until a kind is attached to a draft.
 */
function seedDrafts(value: TelemetryInput): Draft[] {
  const drafts: Draft[] = []
  for (const kind of APP_SCOPED_KINDS) {
    const scope = value[SCOPE_FIELD[kind]]
    if (!scopeNarrows(scope)) continue
    const existing = drafts.find((d) => sameScope(d, scope))
    if (!existing) drafts.push({ id: newDraftId(), name: `${KIND_LABEL[kind]} scope`, namespaces: scope.namespaces, exclude: scope.exclude, workloads: scope.workloads })
  }
  return drafts
}

/** Seeds which draft (or 'custom'/'global') each app-scoped kind currently follows, matching seedDrafts above. */
function seedAttach(value: TelemetryInput, drafts: Draft[]): Record<AppScopedKind, string> {
  const out = {} as Record<AppScopedKind, string>
  for (const kind of APP_SCOPED_KINDS) {
    const scope = value[SCOPE_FIELD[kind]]
    if (!scopeNarrows(scope)) {
      out[kind] = 'global'
      continue
    }
    const draft = drafts.find((d) => sameScope(d, scope))
    out[kind] = draft ? draft.id : 'custom'
  }
  return out
}

/**
 * The scope-drafting step of the navigable guided wizard (see GuidedWizard.tsx, the only caller): once at
 * least one application-scoped signal is on, define one or more named scopes and attach each such signal
 * to one. A draft scope is pure in-memory wizard state, not a new persisted concept: attaching just copies
 * its {namespaces, exclude} into that signal's existing per-kind override field, exactly as if it had been
 * typed there directly (see ScopeOverrideInput in install.ts). Two signals attached to the same draft simply
 * end up with equal values; editing the draft afterward re-copies to every signal still attached to it.
 * Signal selection itself (which of `value`'s kinds are even on) is entirely GuidedWizard's own Layer/
 * Modality/Kind steps - this component only ever sees the result of that, never a picker of its own.
 */
export default function GuidedScope({
  value,
  onChange,
  testIdPrefix,
  initialDraft,
  clusterId,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** The cluster whose discovered workloads the picker offers; without one (or without discovery data) names are typed. */
  clusterId?: string
  /** A scope pre-filled from outside the wizard, e.g. a topology-canvas selection handed off via
   * ScopeFromSelection.tsx - consumed once, at first mount, same as `value`'s own seeded drafts below.
   * Deliberately only adds a draft, never auto-attaches it to a signal: attaching stays the person's own
   * explicit step, exactly as it already is for a hand-built draft. */
  initialDraft?: { name: string; namespaces: string[] }
}) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const { services } = useTopology()
  // Discovered workloads by namespace, for the picker: only this cluster's, never the platform's own.
  const known: Record<string, string[]> = {}
  for (const sv of services ?? []) {
    if (clusterId && sv.clusterId !== clusterId) continue
    ;(known[sv.namespace] ??= []).push(sv.name)
  }
  for (const k of Object.keys(known)) known[k] = [...new Set(known[k])].sort()

  const [drafts, setDrafts] = useState<Draft[]>(() => {
    const seeded = seedDrafts(value)
    if (!initialDraft) return seeded
    return [...seeded, { id: newDraftId(), name: initialDraft.name, namespaces: initialDraft.namespaces, exclude: [], workloads: [] }]
  })
  const [attach, setAttach] = useState<Record<AppScopedKind, string>>(() => seedAttach(value, drafts))

  const enabledKinds = APP_SCOPED_KINDS.filter((k) => value[k])
  const needsScope = enabledKinds.length > 0

  const applyDraftEverywhere = (draft: Draft) => {
    let next = value
    for (const kind of enabledKinds) {
      if (attach[kind] === draft.id) next = { ...next, [SCOPE_FIELD[kind]]: { namespaces: draft.namespaces, exclude: draft.exclude, workloads: draft.workloads } }
    }
    onChange(next)
  }

  const updateDraft = (id: string, patch: Partial<Draft>) => {
    const next = drafts.map((d) => (d.id === id ? { ...d, ...patch, workloads: pruneWorkloads(patch.workloads ?? d.workloads, patch.namespaces ?? d.namespaces) } : d))
    setDrafts(next)
    const updated = next.find((d) => d.id === id)!
    applyDraftEverywhere(updated)
  }

  const addDraft = () => {
    const d: Draft = { id: newDraftId(), name: `Scope ${drafts.length + 1}`, namespaces: [], exclude: [], workloads: [] }
    setDrafts([...drafts, d])
  }

  const removeDraft = (id: string) => {
    setDrafts(drafts.filter((d) => d.id !== id))
    // Anyone attached to the removed draft falls back to the install's own global scope, not a dangling reference.
    let next = value
    const stillAttached: AppScopedKind[] = []
    for (const kind of enabledKinds) {
      if (attach[kind] === id) stillAttached.push(kind)
    }
    for (const kind of stillAttached) next = { ...next, [SCOPE_FIELD[kind]]: emptyScopeOverride }
    if (stillAttached.length) onChange(next)
    setAttach((cur) => {
      const out = { ...cur }
      for (const kind of stillAttached) out[kind] = 'global'
      return out
    })
  }

  const mergeInto = (fromId: string, intoId: string) => {
    const from = drafts.find((d) => d.id === fromId)
    const into = drafts.find((d) => d.id === intoId)
    if (!from || !into) return
    const merged: Draft = {
      ...into,
      namespaces: [...new Set([...into.namespaces, ...from.namespaces])],
      exclude: [...new Set([...into.exclude, ...from.exclude])],
      // A namespace both narrow keeps the union of their workloads; one only a side narrows stays as that side has it.
      workloads: [...into.workloads, ...from.workloads].reduce<WorkloadScope[]>((acc, w) => {
        const have = acc.find((e) => e.namespace === w.namespace)
        if (have) have.names = [...new Set([...have.names, ...w.names])]
        else acc.push({ namespace: w.namespace, names: [...w.names] })
        return acc
      }, []),
    }
    const next = drafts.filter((d) => d.id !== fromId).map((d) => (d.id === intoId ? merged : d))
    setDrafts(next)
    setAttach((cur) => {
      const out = { ...cur }
      for (const kind of enabledKinds) if (out[kind] === fromId) out[kind] = intoId
      return out
    })
    let nextValue = value
    for (const kind of enabledKinds) {
      if (attach[kind] === intoId || attach[kind] === fromId) nextValue = { ...nextValue, [SCOPE_FIELD[kind]]: { namespaces: merged.namespaces, exclude: merged.exclude, workloads: merged.workloads } }
    }
    onChange(nextValue)
  }

  const setAttachFor = (kind: AppScopedKind, choice: string) => {
    setAttach((cur) => ({ ...cur, [kind]: choice }))
    if (choice === 'global') set(SCOPE_FIELD[kind], emptyScopeOverride)
    else if (choice === 'custom') {
      // Leave whatever the field already holds (or the drafted values it was just detached from) as a
      // starting point for hand-editing, rather than clearing it.
    } else {
      const draft = drafts.find((d) => d.id === choice)
      if (draft) set(SCOPE_FIELD[kind], { namespaces: draft.namespaces, exclude: draft.exclude, workloads: draft.workloads })
    }
  }

  return (
    <div className="space-y-5">
      {needsScope && (
        <div className="space-y-2.5 border-t border-nb-850 pt-3">
          <SectionLabel as="p">Define scope</SectionLabel>
          <p className="text-xs text-nb-500">
            Give one or more namespace scopes a name, then attach each application-scoped signal below to one - or leave it on the install's own global scope.
          </p>
          <div className="space-y-3">
            {drafts.map((d) => {
              const overlaps = drafts.filter((o) => o.id !== d.id && scopeOverlap(d, o).length > 0)
              return (
                <div key={d.id} className="space-y-2 rounded-lg border border-nb-850 p-3" data-testid={`${testIdPrefix}-guided-draft-${d.id}`}>
                  <div className="flex items-center gap-2">
                    <Input
                      value={d.name}
                      onChange={(e) => updateDraft(d.id, { name: e.target.value })}
                      placeholder="Name this scope"
                      className="h-8 flex-1"
                      data-testid={`${testIdPrefix}-guided-draft-name-${d.id}`}
                    />
                    <Button type="button" variant="ghost" size="sm" onClick={() => removeDraft(d.id)} data-testid={`${testIdPrefix}-guided-draft-remove-${d.id}`}>
                      Remove
                    </Button>
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label="Only these namespaces" hint="Empty: falls back to the install's global scope.">
                      <TagsInput
                        value={d.namespaces}
                        onChange={(v) => updateDraft(d.id, { namespaces: v })}
                        placeholder="shop payments"
                        data-testid={`${testIdPrefix}-guided-draft-namespaces-${d.id}`}
                      />
                    </Field>
                    <Field label="Never these" hint="Falls back to the install's global scope when empty.">
                      <TagsInput
                        value={d.exclude}
                        onChange={(v) => updateDraft(d.id, { exclude: v })}
                        placeholder="hr-data"
                        data-testid={`${testIdPrefix}-guided-draft-exclude-${d.id}`}
                      />
                    </Field>
                  </div>
                  {d.namespaces.length > 0 && (
                    <WorkloadPicker
                      namespaces={d.namespaces}
                      workloads={d.workloads}
                      known={known}
                      onChange={(w) => updateDraft(d.id, { workloads: w })}
                      testId={`${testIdPrefix}-guided-draft-${d.id}-wl`}
                    />
                  )}
                  {overlaps.map((o) => (
                    <p key={o.id} role="alert" className="text-xs text-warn" data-testid={`${testIdPrefix}-guided-overlap-${d.id}-${o.id}`}>
                      {scopeOverlap(d, o).join(', ')} {scopeOverlap(d, o).length === 1 ? 'is' : 'are'} already in <strong>{o.name || 'another scope'}</strong>.{' '}
                      <button type="button" className="underline hover:no-underline" onClick={() => mergeInto(d.id, o.id)} data-testid={`${testIdPrefix}-guided-merge-${d.id}-${o.id}`}>
                        Merge into {o.name || 'it'}
                      </button>
                    </p>
                  ))}
                </div>
              )
            })}
            <Button type="button" variant="ghost" size="sm" onClick={addDraft} data-testid={`${testIdPrefix}-guided-add-scope`}>
              + Add a scope
            </Button>
          </div>

          <SectionLabel as="p" className="pt-1">Attach</SectionLabel>
          <div className="space-y-3">
            {enabledKinds.map((kind) => {
              const choice = attach[kind] ?? 'global'
              return (
                <div key={kind} className="space-y-2 rounded-lg border border-nb-850 p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="text-sm text-nb-300">{KIND_LABEL[kind]}</span>
                    <select
                      className="h-8 rounded-md border border-nb-800 bg-nb-925 px-2 text-xs text-nb-300"
                      value={choice}
                      onChange={(e) => setAttachFor(kind, e.target.value)}
                      data-testid={`${testIdPrefix}-guided-attach-${kind}`}
                    >
                      <option value="global">Use the install's global scope</option>
                      {drafts.map((d) => (
                        <option key={d.id} value={d.id}>
                          Use {d.name || 'this scope'}
                        </option>
                      ))}
                      <option value="custom">Custom, just for this signal</option>
                    </select>
                  </div>
                  {choice === 'custom' && (
                    <div className="grid gap-3 sm:grid-cols-2">
                      <Field label="Only these namespaces" hint="Empty: falls back to the install's global scope.">
                        <TagsInput
                          value={value[SCOPE_FIELD[kind]].namespaces}
                          onChange={(v) => set(SCOPE_FIELD[kind], { ...value[SCOPE_FIELD[kind]], namespaces: v })}
                          placeholder="shop payments"
                          data-testid={`${testIdPrefix}-guided-custom-namespaces-${kind}`}
                        />
                      </Field>
                      <Field label="Never these" hint="Falls back to the install's global scope when empty.">
                        <TagsInput
                          value={value[SCOPE_FIELD[kind]].exclude}
                          onChange={(v) => set(SCOPE_FIELD[kind], { ...value[SCOPE_FIELD[kind]], exclude: v })}
                          placeholder="hr-data"
                          data-testid={`${testIdPrefix}-guided-custom-exclude-${kind}`}
                        />
                      </Field>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
          <InfraScope value={value} onChange={onChange} testIdPrefix={testIdPrefix} />
        </div>
      )}
    </div>
  )
}

/** One namespace's workloads: the whole namespace, or only the ones ticked. */
function WorkloadPicker({
  namespaces,
  workloads,
  known,
  onChange,
  testId,
}: {
  namespaces: string[]
  workloads: WorkloadScope[]
  known: Record<string, string[]>
  onChange: (w: WorkloadScope[]) => void
  testId: string
}) {
  const setFor = (ns: string, names: string[] | null) => {
    const rest = workloads.filter((w) => w.namespace !== ns)
    onChange(names === null ? rest : [...rest, { namespace: ns, names }])
  }
  return (
    <div className="space-y-1.5" data-testid={testId}>
      <p className="text-xs text-nb-500">Narrow a namespace to some of its workloads, or leave it whole.</p>
      {namespaces.map((ns) => {
        const entry = workloads.find((w) => w.namespace === ns)
        const options = known[ns] ?? []
        return (
          <div key={ns} className="rounded-md border border-nb-850 px-2.5 py-2" data-testid={`${testId}-${ns}`}>
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="font-mono text-nb-300">{ns}</span>
              <span className="text-nb-500" data-testid={`${testId}-${ns}-summary`}>
                {!entry ? 'all workloads' : entry.names.length === 0 ? 'none selected, so this namespace sends nothing' : `${entry.names.length}${options.length ? ` of ${options.length}` : ''} workloads`}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="ml-auto"
                onClick={() => setFor(ns, entry ? null : options.slice())}
                data-testid={`${testId}-${ns}-toggle`}
              >
                {entry ? 'Whole namespace' : 'Choose workloads'}
              </Button>
            </div>
            {entry && (
              <div className="mt-2">
                {options.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {options.map((n) => {
                      const on = entry.names.includes(n)
                      return (
                        <button
                          key={n}
                          type="button"
                          aria-pressed={on}
                          onClick={() => setFor(ns, on ? entry.names.filter((x) => x !== n) : [...entry.names, n])}
                          data-testid={`${testId}-${ns}-w-${n}`}
                          className={clsx('rounded-md border px-2 py-1 font-mono text-xs', on ? 'border-accent bg-accent-soft text-accent' : 'border-nb-850 text-nb-400 hover:border-nb-800')}
                        >
                          {n}
                        </button>
                      )
                    })}
                  </div>
                ) : (
                  <TagsInput value={entry.names} onChange={(v) => setFor(ns, v)} placeholder="cart payment-api" data-testid={`${testId}-${ns}-names`} />
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

/** What each infrastructure signal does with a scope: follows it (in part - nodes have no namespace), or is not narrowed. */
const INFRA_EFFECT: { id: keyof TelemetryInput; label: string; follows: boolean; note: string }[] = [
  { id: 'resourceUsage', label: 'Resource usage', follows: true, note: 'Pod and container metrics follow the scope. Node and host totals stay whole-node.' },
  { id: 'nodeRuntime', label: 'Node runtime', follows: true, note: 'Pod and volume metrics follow the scope. Node stats stay.' },
  { id: 'kubernetesState', label: 'Kubernetes state', follows: true, note: 'Pods, deployments and replicas follow the scope. Nodes are not namespaced.' },
  { id: 'kubernetesEvents', label: 'Kubernetes events', follows: true, note: 'Events in your namespaces only; workloads do not apply to events. Node events stay.' },
  { id: 'energy', label: 'Energy', follows: false, note: 'Not narrowed: power is reported per node.' },
  { id: 'accelerators', label: 'Accelerators (GPU)', follows: false, note: 'Not narrowed by this - GPU metrics have their own scope switch under Collect.' },
  { id: 'systemLogs', label: 'System logs', follows: false, note: 'Not narrowed: read per node.' },
]

/** The switch that lets infrastructure signals follow the scope, with what it does to each one that is on. */
function InfraScope({ value, onChange, testIdPrefix }: { value: TelemetryInput; onChange: (v: TelemetryInput) => void; testIdPrefix: string }) {
  const combined = combinedScope(value)
  const on = INFRA_EFFECT.filter((e) => value[e.id])
  if (!scopeNarrows(combined) || !on.some((e) => e.follows)) return null
  const where = [
    combined.namespaces.length ? combined.namespaces.map((n) => combined.workloads.find((w) => w.namespace === n) ? `${n}: ${combined.workloads.find((w) => w.namespace === n)!.names.join('+') || 'nothing'}` : n).join('; ') : 'all namespaces',
    combined.exclude.length ? `excluding ${combined.exclude.join('+')}` : '',
  ].filter(Boolean).join(', ')
  return (
    <div className="space-y-2 rounded-lg border border-nb-850 p-3" data-testid={`${testIdPrefix}-guided-infra`}>
      <label className="flex cursor-pointer items-start gap-2.5 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 size-4 accent-[var(--color-accent)]"
          checked={value.scopeInfrastructure}
          onChange={(e) => onChange({ ...value, scopeInfrastructure: e.target.checked })}
          data-testid={`${testIdPrefix}-guided-infra-follow`}
        />
        <span>
          <span className="text-nb-300">Apply this scope to infrastructure signals too</span>
          <span className="block text-xs text-nb-500" data-testid={`${testIdPrefix}-guided-infra-where`}>
            Combined from the application signals above: {where}.
          </span>
        </span>
      </label>
      <ul className="space-y-1 text-xs" data-testid={`${testIdPrefix}-guided-infra-effects`}>
        {on.map((e) => (
          <li key={e.id} className="flex gap-2" data-testid={`${testIdPrefix}-guided-infra-effect-${e.id}`}>
            <span className={clsx('w-28 shrink-0 text-nb-300', !(e.follows && value.scopeInfrastructure) && 'text-nb-500')}>{e.label}</span>
            <span className="text-nb-500">{e.follows && !value.scopeInfrastructure ? 'Whole cluster.' : e.note}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

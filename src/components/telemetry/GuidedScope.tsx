import { useState } from 'react'
import { Button, Field, Input, TagsInput } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { emptyScopeOverride, scopeOverlap, type ScopeOverrideInput, type TelemetryInput } from '@/lib/install'
import { AcceleratorsFields, EnergyFields, SignalRow, type SignalId } from './TelemetryFields'

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
}

let draftSeq = 0
const newDraftId = () => `draft-${++draftSeq}`

const sameScope = (a: ScopeOverrideInput, b: ScopeOverrideInput) =>
  a.namespaces.length === b.namespaces.length &&
  a.exclude.length === b.exclude.length &&
  a.namespaces.every((n) => b.namespaces.includes(n)) &&
  a.exclude.every((n) => b.exclude.includes(n))

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
    if (scope.namespaces.length === 0 && scope.exclude.length === 0) continue
    const existing = drafts.find((d) => sameScope(d, scope))
    if (!existing) drafts.push({ id: newDraftId(), name: `${KIND_LABEL[kind]} scope`, namespaces: scope.namespaces, exclude: scope.exclude })
  }
  return drafts
}

/** Seeds which draft (or 'custom'/'global') each app-scoped kind currently follows, matching seedDrafts above. */
function seedAttach(value: TelemetryInput, drafts: Draft[]): Record<AppScopedKind, string> {
  const out = {} as Record<AppScopedKind, string>
  for (const kind of APP_SCOPED_KINDS) {
    const scope = value[SCOPE_FIELD[kind]]
    if (scope.namespaces.length === 0 && scope.exclude.length === 0) {
      out[kind] = 'global'
      continue
    }
    const draft = drafts.find((d) => sameScope(d, scope))
    out[kind] = draft ? draft.id : 'custom'
  }
  return out
}

/**
 * The guided entry path into telemetry configuration (see TelemetryFields.tsx, which renders this instead
 * of its own flat grid once "Guided setup" is picked): pick signals first, then - only once at least one
 * application-scoped signal is on - define one or more named scopes and attach each such signal to one.
 * A draft scope is pure in-memory wizard state, not a new persisted concept: attaching just copies its
 * {namespaces, exclude} into that signal's existing per-kind override field, exactly as if it had been
 * typed there directly (see ScopeOverrideInput in install.ts). Two signals attached to the same draft simply
 * end up with equal values; editing the draft afterward re-copies to every signal still attached to it.
 */
export default function GuidedScope({
  value,
  onChange,
  testIdPrefix,
  initialDraft,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** A scope pre-filled from outside the wizard, e.g. a topology-canvas selection handed off via
   * ScopeFromSelection.tsx - consumed once, at first mount, same as `value`'s own seeded drafts below.
   * Deliberately only adds a draft, never auto-attaches it to a signal: attaching stays the person's own
   * explicit step, exactly as it already is for a hand-built draft. */
  initialDraft?: { name: string; namespaces: string[] }
}) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  const infra = TELEMETRY_SIGNALS.filter((s) => s.layer === 'infrastructure')
  const app = TELEMETRY_SIGNALS.filter((s) => s.layer === 'application')

  const [drafts, setDrafts] = useState<Draft[]>(() => {
    const seeded = seedDrafts(value)
    if (!initialDraft) return seeded
    return [...seeded, { id: newDraftId(), name: initialDraft.name, namespaces: initialDraft.namespaces, exclude: [] }]
  })
  const [attach, setAttach] = useState<Record<AppScopedKind, string>>(() => seedAttach(value, drafts))

  const enabledKinds = APP_SCOPED_KINDS.filter((k) => value[k])
  const needsScope = enabledKinds.length > 0

  const applyDraftEverywhere = (draft: Draft) => {
    let next = value
    for (const kind of enabledKinds) {
      if (attach[kind] === draft.id) next = { ...next, [SCOPE_FIELD[kind]]: { namespaces: draft.namespaces, exclude: draft.exclude } }
    }
    onChange(next)
  }

  const updateDraft = (id: string, patch: Partial<Draft>) => {
    const next = drafts.map((d) => (d.id === id ? { ...d, ...patch } : d))
    setDrafts(next)
    const updated = next.find((d) => d.id === id)!
    applyDraftEverywhere(updated)
  }

  const addDraft = () => {
    const d: Draft = { id: newDraftId(), name: `Scope ${drafts.length + 1}`, namespaces: [], exclude: [] }
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
      if (attach[kind] === intoId || attach[kind] === fromId) nextValue = { ...nextValue, [SCOPE_FIELD[kind]]: { namespaces: merged.namespaces, exclude: merged.exclude } }
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
      if (draft) set(SCOPE_FIELD[kind], { namespaces: draft.namespaces, exclude: draft.exclude })
    }
  }

  return (
    <div className="space-y-5">
      <div className="space-y-2.5">
        <p className="text-xs font-medium uppercase tracking-wide text-nb-500">Step 1 · Pick signals</p>
        <div className="grid gap-3 sm:grid-cols-2">
          <fieldset className="space-y-2.5">
            <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-600">Infrastructure</legend>
            {infra.map((s) => (
              <SignalRow key={s.id} signal={s} checked={value[s.id as SignalId]} onChange={(v) => set(s.id as SignalId, v)} testIdPrefix={testIdPrefix} />
            ))}
          </fieldset>
          <fieldset className="space-y-2.5">
            <legend className="mb-0.5 text-xs font-medium uppercase tracking-wide text-nb-600">Application</legend>
            {app.map((s) => (
              <SignalRow key={s.id} signal={s} checked={value[s.id as SignalId]} onChange={(v) => set(s.id as SignalId, v)} testIdPrefix={testIdPrefix} />
            ))}
          </fieldset>
        </div>
      </div>

      {value.energy && <EnergyFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
      {value.accelerators && <AcceleratorsFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}

      {needsScope && (
        <div className="space-y-2.5 border-t border-nb-850 pt-3">
          <p className="text-xs font-medium uppercase tracking-wide text-nb-500">Step 2 · Define scope</p>
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

          <p className="pt-1 text-xs font-medium uppercase tracking-wide text-nb-500">Step 3 · Attach</p>
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
        </div>
      )}
    </div>
  )
}

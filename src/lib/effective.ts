import { pruneDependencies } from './migrate'
import type { Topology } from './types'

type Layered = { overrides?: Record<string, unknown> }

/** The value everyone sees: detected/base fields with human overrides on top. */
export function effective<T extends Layered>(e: T): T {
  return e.overrides && Object.keys(e.overrides).length ? ({ ...e, ...e.overrides } as T) : e
}

/** Keys that are bookkeeping, never user-editable values (so never overridden). */
const META = new Set([
  'id',
  'orgId',
  'source',
  'key',
  'lastSeen',
  'firstSeen',
  'detectedAt',
  'agentId',
  'revision',
  'stale',
  'deletedAt',
  'evidence',
  'overrides',
])

/**
 * Turn an edited copy back into a stored entity.
 *  - manual/imported entities: the edit simply becomes the new base values.
 *  - discovered entities: the base (detected) values are kept; only fields that
 *    differ from them are stored as overrides, and fields edited back to the
 *    detected value drop their override.
 */
export function applyEdit<T extends Layered & { source: string }>(raw: T | undefined, next: T): T {
  if (!raw || raw.source !== 'discovered') return { ...next, overrides: undefined }
  const overrides: Record<string, unknown> = {}
  for (const k of Object.keys(next) as (keyof T & string)[]) {
    if (META.has(k)) continue
    if (JSON.stringify(next[k]) !== JSON.stringify(raw[k])) overrides[k] = next[k]
  }
  return { ...raw, overrides: Object.keys(overrides).length ? overrides : undefined }
}

/**
 * A short "field: old → new" summary of what changed between two shapes of the same entity, for the audit
 * log (see `saveCluster`/`saveNode`/`saveService`/`saveDevice`/`upsertSite` in `store/topology.ts`). Compares
 * the *effective* (override-merged) shape a person saw and edited, not the raw stored one, since that is what
 * their edit is actually a change from - this is also what makes turning a guessed/unknown value into a
 * declared one show up here, the same way it clears an "unknown" evidence chip: both read off the same
 * effective value. Skips bookkeeping (see META) and prints only the cheap-to-read values (strings, numbers,
 * booleans); anything else (labels, nested objects) is still named, just without old/new. Empty when nothing
 * meaningful differs, so a no-op save writes no entry.
 */
export function describeEdit<T extends object>(before: T | undefined, after: T): string {
  // Entity types (Cluster, Site, ...) have no string index signature, so they cannot be typed as
  // Record<string, unknown> directly; this reads them that way without demanding one from callers.
  const b0 = before as Record<string, unknown> | undefined
  const a0 = after as Record<string, unknown>
  const scalar = (v: unknown) => v === undefined || v === null || typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean'
  const label = (v: unknown) => (v === undefined || v === null || v === '' ? '(unset)' : String(v))
  const changed: string[] = []
  for (const k of new Set([...(b0 ? Object.keys(b0) : []), ...Object.keys(a0)])) {
    if (META.has(k)) continue
    const a = b0?.[k]
    const b = a0[k]
    if (JSON.stringify(a) === JSON.stringify(b)) continue
    changed.push(scalar(a) && scalar(b) ? `${k}: ${label(a)} → ${label(b)}` : k)
  }
  if (!changed.length) return ''
  const shown = changed.slice(0, 3)
  return shown.join(', ') + (changed.length > shown.length ? ` (+${changed.length - shown.length} more)` : '')
}

const live = <T extends { deletedAt?: string }>(l: T[]) => l.filter((e) => !e.deletedAt)

/**
 * The view the UI works with: human overrides merged in, and records the agent
 * reported as gone (tombstones) left out.
 */
export function applyEffective<T extends Topology>(t: T): T {
  const services = live(t.services).map(effective)
  const devices = live(t.devices).map(effective)
  const externalEndpoints = live(t.externalEndpoints).map(effective)
  return {
    ...t,
    clusters: live(t.clusters).map(effective),
    nodes: live(t.nodes).map(effective),
    namespaces: live(t.namespaces).map(effective),
    services,
    devices,
    applications: live(t.applications).map(effective),
    externalEndpoints,
    dependencies: pruneDependencies(t.dependencies, { services, devices, externalEndpoints }),
  }
}

export const hasOverrides = (e: Layered) => !!e.overrides && Object.keys(e.overrides).length > 0

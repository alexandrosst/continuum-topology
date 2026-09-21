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

/**
 * Types and pure helpers for the System Health page (GET /api/v1/orgs/{org}/telemetry/self - see
 * backend/internal/server/admin_telemetry.go's own doc comment for the exact response shape this
 * mirrors). Kept separate from api.ts/SystemHealthPage.tsx so the "what does this field mean, is it
 * available, what's a clean axis tick" logic is unit-testable on its own, the same split lib/history.ts
 * and lib/observed.ts already use for their own page's supporting logic.
 */

/** One point in one entity's self-telemetry history, exactly as the server sends it - every optional
 *  field really is sometimes absent (never a fabricated 0), so every caller here treats `undefined`
 *  as "not measured", not "measured as zero". */
export interface SelfTelemetrySample {
  t: string
  rssBytes: number
  goroutines: number
  /** Omitted on an entity's very first sample (nothing yet to derive a rate from). */
  cpuPct?: number
  /** Agent entities only. Omitted when the cluster's own observed throughput is zero/unknown, or there
   *  is no previous sample yet to derive a rate from. */
  bandwidthSharePct?: number
  /** Agent entities only. Omitted when no node in the cluster exposes Intel RAPL - the common case on
   *  most real hardware. */
  watts?: number
  /** Always omitted in this pass of the backend (Continuum's own estimated share of watts is deferred -
   *  see admin_telemetry.go's own "Deferred" paragraph). Kept in the type so a client written against it
   *  today needs no change once a later backend pass fills it in. */
  continuumWattsEstimate?: number
  /** Agent entities only. */
  flowIntervalSeconds?: number
  /** Agent entities only. */
  probeIntervalSeconds?: number
  /** Server entity only. Always present on every server sample (0 is a real reading - e.g. no agent
   *  currently connected), never omitted for "nothing to derive a rate from" the way cpuPct is. */
  connectedAgents?: number
  /** Server entity only. Always present on every server sample - already a rate when sampled on the
   *  backend, so unlike cpuPct/modelCacheHitPct/gcPauseMsPerSec it needs no previous sample to derive
   *  (0 on the very first sample after a restart, a real reading, not an omission). */
  flowIngestBytesPerSec?: number
  /** Server entity only. This interval's own cache hit rate (hits over hits+misses since the previous
   *  sample, not the all-time ratio) - omitted on an entity's very first sample, or on any interval
   *  where no cache lookup happened at all. */
  modelCacheHitPct?: number
  /** Server entity only. Milliseconds of Go garbage-collector pause time per second of wall-clock time
   *  since the previous sample - omitted on an entity's very first sample. */
  gcPauseMsPerSec?: number
}

/** One agent (one organisation's cluster) or the server itself, with its own self-telemetry history. */
export interface SelfTelemetryEntity {
  id: string
  kind: 'agent' | 'server'
  /** The agent's own display name - agent entities only. */
  clusterName?: string
  samples: SelfTelemetrySample[]
}

/** A field that is sometimes absent on a sample, rather than always present (rssBytes/goroutines are
 *  always present and so never need this). */
export type OptionalSampleKey =
  | 'cpuPct'
  | 'bandwidthSharePct'
  | 'watts'
  | 'continuumWattsEstimate'
  | 'connectedAgents'
  | 'flowIngestBytesPerSec'
  | 'modelCacheHitPct'
  | 'gcPauseMsPerSec'

/** The name an entity's card should show. */
export function entityLabel(entity: SelfTelemetryEntity): string {
  return entity.kind === 'server' ? 'Continuum server' : entity.clusterName || entity.id
}

/** An agent reports on its own heartbeat cadence, the server on selfStatsSampleEvery - both 30s (see
 *  admin_telemetry.go). This mirrors AgentsPage's own LATE_AFTER_MS (two missed reports) so "live" means
 *  the same thing here as it does on the Agents page, rather than this page inventing its own threshold. */
export const TELEMETRY_LATE_AFTER_MS = 75_000

export interface Freshness {
  live: boolean
  lastSampleAt: number
  ageMs: number
}

/** How current an entity's self-telemetry is, from its own last sample's timestamp - null only when the
 *  entity somehow has no samples at all (the backend never actually sends such an entity, but a defensive
 *  caller still has to handle it). */
export function freshnessOf(entity: SelfTelemetryEntity, now = Date.now()): Freshness | null {
  const last = entity.samples[entity.samples.length - 1]
  if (!last) return null
  const lastSampleAt = Date.parse(last.t)
  if (!Number.isFinite(lastSampleAt)) return null
  return { live: now - lastSampleAt <= TELEMETRY_LATE_AFTER_MS, lastSampleAt, ageMs: Math.max(0, now - lastSampleAt) }
}

/** Whether this entity has EVER reported a given optional field, across its whole kept history - not
 *  just the latest sample. False here means "not available for this entity" (e.g. no RAPL on any node in
 *  this cluster), which is a different, calmer message than "no reading yet" would be. */
export function fieldEverReported(entity: SelfTelemetryEntity, key: OptionalSampleKey): boolean {
  return entity.samples.some((s) => s[key] !== undefined)
}

/** The most recent sample that actually carries a value for `key` (skipping any trailing samples where
 *  it happens to be momentarily missing), for a card's "right now" headline figure. undefined only when
 *  no sample ever carried one. */
export function latestDefined(entity: SelfTelemetryEntity, key: keyof SelfTelemetrySample): number | undefined {
  for (let i = entity.samples.length - 1; i >= 0; i--) {
    const v = entity.samples[i][key]
    if (typeof v === 'number') return v
  }
  return undefined
}

/** One field's whole history as plain {t, v} points, in chart-ready order - undefined values are kept
 *  (not dropped) so the chart can draw a real gap instead of silently bridging over a missing reading. */
export interface ChartPoint {
  t: number
  v: number | undefined
}
export function toPoints(entity: SelfTelemetryEntity, key: keyof SelfTelemetrySample): ChartPoint[] {
  return entity.samples.map((s) => {
    const raw = s[key]
    return { t: Date.parse(s.t), v: typeof raw === 'number' ? raw : undefined }
  })
}

/**
 * A d3-"nice"-style axis domain: rounds a data range outward to a clean step (1/2/5 x a power of ten) and
 * returns evenly spaced tick values at that step - see dataviz's marks-and-anatomy.md: "Y-axis ticks:
 * round to clean numbers (0 / 1,000 / 2,000)". `tickCount` is a target, not a guarantee - a tight or
 * degenerate range still comes back with at least two sensible ticks. Never returns a negative minimum
 * for a series whose real data never goes negative (every metric on this page: bytes, counts, percentages,
 * watts), so a flat-zero series gets a [0, step] axis instead of a confusing negative tick.
 */
export function niceDomain(rawMin: number, rawMax: number, tickCount = 4): { min: number; max: number; ticks: number[] } {
  if (!Number.isFinite(rawMin) || !Number.isFinite(rawMax)) return { min: 0, max: 1, ticks: [0, 1] }
  const nonNegative = rawMin >= 0
  let min = rawMin
  let max = rawMax
  if (min === max) {
    const pad = Math.abs(min) * 0.1 || 1
    min -= pad
    max += pad
  }
  const rawStep = (max - min) / Math.max(1, tickCount - 1)
  const mag = 10 ** Math.floor(Math.log10(rawStep))
  const norm = rawStep / mag
  const niceNorm = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10
  const step = niceNorm * mag
  let niceMin = Math.floor(min / step) * step
  let niceMax = Math.ceil(max / step) * step
  if (nonNegative && niceMin < 0) niceMin = 0
  if (niceMax <= niceMin) niceMax = niceMin + step
  const ticks: number[] = []
  for (let v = niceMin; v <= niceMax + step / 1e6; v += step) ticks.push(Math.round(v / step) * step)
  return { min: niceMin, max: niceMax, ticks }
}

/** "14:32" in the viewer's own locale/timezone - the same toLocaleTimeString shape
 *  ConnectClusterWizard already uses for a short clock reading. */
export function clockLabel(epochMs: number): string {
  return new Date(epochMs).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

import type { FusionRetentionRequest, FusionRetentionStore, FusionSignal } from '@/lib/api'

/** One GiB, the unit volumes are asked for and shown in. */
export const GIB = 1024 ** 3

/** "512 MiB", "4.2 GiB", "1.5 TiB": binary units, the ones a volume is sized in. */
export function formatBytes(n: number): string {
  if (n < GIB) return `${Math.max(0, Math.round(n / 1024 ** 2))} MiB`
  if (n < 1024 * GIB) return `${trim(n / GIB)} GiB`
  return `${trim(n / (1024 * GIB))} TiB`
}
const trim = (x: number) => (x >= 100 ? Math.round(x) : Math.round(x * 10) / 10).toString()

/** "7 days", "1 day". */
export const daysText = (d: number) => `${d} day${d === 1 ? '' : 's'}`

/** The volume's size in whole GiB, rounded up: what the size field starts at and cannot go below. */
export const volumeGiB = (s: FusionRetentionStore) => Math.ceil(s.volumeBytes / GIB - 1e-9)

/** The bytes the data of `days` days takes at today's growth, with the headroom the store needs to work in; null while growth is unknown. */
export function neededBytes(s: FusionRetentionStore, days: number): number | null {
  if (s.bytesPerDay === undefined || s.bytesPerDay <= 0 || !s.share) return null
  return (s.bytesPerDay * days) / s.share
}

/** The smallest whole-GiB volume that holds `days` days; null while growth is unknown. */
export function neededGiB(s: FusionRetentionStore, days: number): number | null {
  const n = neededBytes(s, days)
  return n === null ? null : Math.max(1, Math.ceil(n / GIB))
}

export interface Verdict {
  tone: 'ok' | 'warn' | 'muted'
  text: string
  /** The volume to suggest growing to, when this retention does not fit the one entered. */
  suggestGiB?: number
}

/** Whether `days` days fit a volume of `gib` GiB, in a sentence. Only an estimate, from the growth seen so far, and says so. */
export function retentionVerdict(s: FusionRetentionStore, days: number, gib: number): Verdict {
  const need = neededBytes(s, days)
  if (need === null || s.bytesPerDay === undefined) {
    return { tone: 'muted', text: 'Not enough data yet to estimate the room this needs. The estimate appears once a day or so of data has been saved.' }
  }
  const have = gib * GIB
  if (need <= have) {
    return { tone: 'ok', text: `At today's growth about ${formatBytes(need)} is needed; the volume has ${formatBytes(have)}.` }
  }
  const fits = Math.max(0, Math.floor((have * s.share) / s.bytesPerDay))
  const suggestGiB = neededGiB(s, days) ?? undefined
  const consequence =
    s.component === 'metrics'
      ? `Prometheus' size limit would drop the oldest data after about ${daysText(fits)}.`
      : `the volume would fill up after about ${daysText(fits)}.`
  return { tone: 'warn', text: `At today's growth about ${formatBytes(need)} is needed, more than the ${formatBytes(have)} volume: ${consequence}`, suggestGiB }
}

/** The most days that fit a volume of `gib` GiB at today's growth, at least 1; null while growth is unknown. */
export function daysThatFit(s: FusionRetentionStore, gib: number): number | null {
  if (s.bytesPerDay === undefined || s.bytesPerDay <= 0 || !s.share) return null
  return Math.max(1, Math.floor((gib * GIB * s.share) / s.bytesPerDay))
}

export type FullLevel = 'warn' | 'critical'
/** How full the volume is once it is nearly full: 85% and 95% of what was asked for. Only the kubelet's count for the whole volume says it
 *  (Prometheus' own figure is its data, and it deletes its oldest blocks at its size limit before the volume fills). */
export function fullness(s: FusionRetentionStore): { level: FullLevel; pct: number } | null {
  if (s.usedBytes === undefined || s.usedSource !== 'volume' || !s.volumeKnown || s.volumeBytes <= 0) return null
  const pct = Math.round((s.usedBytes / s.volumeBytes) * 100)
  if (pct >= 95) return { level: 'critical', pct }
  if (pct >= 85) return { level: 'warn', pct }
  return null
}

/** What to do about a nearly full volume, in a sentence. */
export function fullnessAdvice(s: FusionRetentionStore): string {
  const f = fullness(s)
  if (!f) return ''
  const lead = `${s.label}'s volume is ${f.pct}% full${f.level === 'critical' ? ' and it may stop accepting data' : ''}.`
  return s.canGrow === false
    ? `${lead} This volume cannot be grown, so lower the days it keeps.`
    : `${lead} Lower the days it keeps or grow the volume.`
}

/** A figure as a bar: how much of the whole it is, which of the bar tones it takes, and the figure in a few words. */
export interface StoreBar {
  pct: number
  tone: 'ok' | 'warn' | 'bad' | 'neutral'
  text: string
}

/** Prometheus' own size limit keeps fewer days than the retention asks for: it drops the oldest blocks first. */
export const sizeCapped = (s: FusionRetentionStore): boolean => s.sizeLimitDays !== undefined && s.sizeLimitDays < s.days

/** How much of the volume is used: green, amber from 85% and red from 95% (the marks `fullness` is told by). Null when use or volume is not known. */
export function diskBar(s: FusionRetentionStore): StoreBar | null {
  if (s.usedBytes === undefined || !s.volumeKnown || s.volumeBytes <= 0) return null
  const f = fullness(s)
  return { pct: Math.min(100, (s.usedBytes / s.volumeBytes) * 100), tone: f ? (f.level === 'critical' ? 'bad' : 'warn') : 'ok', text: `${formatBytes(s.usedBytes)} of ${formatBytes(s.volumeBytes)}` }
}

/** How much of the retention window holds data: the days of data the store has, of the days it keeps. Amber while its size limit caps the days
 *  below the setting. Null until the days of data are known. */
export function keptBar(s: FusionRetentionStore): StoreBar | null {
  if (s.dataDays === undefined || s.days <= 0) return null
  const held = Math.min(s.dataDays, s.days)
  return { pct: (held / s.days) * 100, tone: sizeCapped(s) ? 'warn' : 'neutral', text: `${trim(held)} of ${daysText(s.days)} held` }
}

/** In about how many days the volume fills, from what is on it and what is added a day: only for a volume whose use is counted whole, and only
 *  when the retention asks for more than the volume holds (otherwise old data is cleaned up as fast as new arrives, and it never fills). */
export function fillsInDays(s: FusionRetentionStore): number | null {
  const need = neededBytes(s, s.days)
  if (need === null || need <= s.volumeBytes || !s.volumeKnown || s.usedSource !== 'volume' || s.usedBytes === undefined || s.bytesPerDay === undefined) return null
  const room = s.volumeBytes - s.usedBytes
  return room > 0 ? Math.max(1, Math.round(room / s.bytesPerDay)) : null
}

export interface RetentionForm {
  days: string
  gib: string
}

/** The whole form: one entry per store, as text, starting at what is set now. */
export type RetentionFormState = Record<FusionSignal, RetentionForm>

export function initialForm(stores: FusionRetentionStore[]): RetentionFormState {
  const form = {} as RetentionFormState
  for (const c of ['metrics', 'logs', 'traces'] as const) {
    const s = stores.find((x) => x.component === c)
    form[c] = { days: s ? String(s.days) : '', gib: s && s.volumeKnown ? String(volumeGiB(s)) : '' }
  }
  return form
}

const whole = (v: string) => /^[0-9]+$/.test(v.trim())

/** What is wrong with one store's entries, in a sentence; '' when they are fine (or unchanged). */
export function formError(s: FusionRetentionStore, f: RetentionForm): string {
  if (!whole(f.days) || Number(f.days) < s.minDays || Number(f.days) > s.maxDays) return `${s.label} keeps between ${s.minDays} and ${s.maxDays} days.`
  if (s.volumeKnown) {
    if (!whole(f.gib) || Number(f.gib) < 1) return `${s.label}'s volume must be a whole number of GiB.`
    if (Number(f.gib) < volumeGiB(s)) return `${s.label}'s volume is ${volumeGiB(s)} GiB. A volume can be grown but not made smaller.`
  }
  return ''
}

/** What differs from the settings now, as the request body; empty when nothing does. A volume field of a store whose volume is unknown is never sent. */
export function formChanges(stores: FusionRetentionStore[], form: RetentionFormState): FusionRetentionRequest {
  const req: FusionRetentionRequest = {}
  for (const s of stores) {
    const f = form[s.component]
    const change: { days?: number; volumeGiB?: number } = {}
    if (whole(f.days) && Number(f.days) !== s.days) change.days = Number(f.days)
    if (s.volumeKnown && s.canGrow !== false && whole(f.gib) && Number(f.gib) > volumeGiB(s)) change.volumeGiB = Number(f.gib)
    if (change.days !== undefined || change.volumeGiB !== undefined) req[s.component] = change
  }
  return req
}

/** The stores a request restarts for a moment: those whose retention changes (and Prometheus when its volume grows, which may move its size limit). */
export function restartedBy(stores: FusionRetentionStore[], req: FusionRetentionRequest): string[] {
  return stores
    .filter((s) => {
      const c = req[s.component]
      return !!c && (c.days !== undefined || (s.component === 'metrics' && c.volumeGiB !== undefined))
    })
    .map((s) => s.label)
}

/** The stores a request makes keep fewer days than they do now: what is deleted, and the setting it is deleted down to. */
export function shortened(stores: FusionRetentionStore[], req: FusionRetentionRequest): { label: string; from: number; to: number }[] {
  return stores.flatMap((s) => {
    const to = req[s.component]?.days
    return to !== undefined && to < s.days ? [{ label: s.label, from: s.days, to }] : []
  })
}

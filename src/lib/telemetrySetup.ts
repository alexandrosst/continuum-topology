import { uptimeWords, type AgentDiagnostics } from './consent'
import { exportReach, exportRows } from './exportHealth'
import { cleanTags, TELEMETRY_SIGNALS, type Modality, type TelemetryInput } from './install'

/**
 * The state of one thing in the product's one vocabulary. Always shown as a glyph, the word and a colour, never colour alone.
 * (Unknown is not a fault: it is what is said when nothing is known, for example before the first data.)
 */
export type Health = 'healthy' | 'attention' | 'broken' | 'unknown'
export const HEALTH_WORD: Record<Health, string> = { healthy: 'Healthy', attention: 'Needs attention', broken: 'Not working', unknown: 'Unknown' }
const HEALTH_RANK: Record<Health, number> = { healthy: 0, unknown: 1, attention: 2, broken: 3 }

/** The rail of the setup, in the product's words: the same four steps wherever it opens. */
export const SETUP_STEPS = ['Where from', 'What to collect', 'Where to send', 'Review and install']

/** How long after the command was shown "no data yet" stops being normal and starts being a thing to look at. */
export const ARRIVAL_PATIENCE_SECONDS = 5 * 60
/** An agent that has been silent this long is not reporting any more: what it last said is not the state now. */
const STALE_REPORT_SECONDS = 15 * 60

const MODALITY_LABEL: Record<Modality, string> = { metrics: 'Metrics', logs: 'Logs', traces: 'Traces' }
const ago = (iso: string | undefined, now: number) => (iso ? `${uptimeWords(Math.max(0, (now - Date.parse(iso)) / 1000))} ago` : undefined)

export interface ArrivalRow {
  modality: Modality
  label: string
  health: Health
  /** "Last data 2 min ago" or "No data yet". */
  lastData: string
  /** One sentence: what is wrong, or that nothing is. */
  sentence: string
}

export interface Arrival {
  health: Health
  /** The state in one sentence. */
  headline: string
  /** The one thing to do about it, when there is something. */
  todo?: string
  rows: ArrivalRow[]
  /** Some collector pods could not be read: the rest still count, and the gap is said. */
  partial?: string
}

/**
 * Whether the data that was just set up is arriving, from what the agent last reported about itself: which signals run in the cluster now
 * (so a command that has not been applied yet is not mistaken for data that is not flowing) and what each signal type's exporters did.
 * `waited` is how long the person has been looking at this check. Pure: the caller owns the clock and the polling.
 */
export function arrivalCheck(o: { wanted: string[]; diagnostics?: AgentDiagnostics; connected?: boolean; waited: number; now: number; cluster: string }): Arrival {
  const { wanted, diagnostics: d, connected, waited, now, cluster } = o
  const unknown = (headline: string, todo?: string): Arrival => ({ health: 'unknown', headline, todo, rows: [] })

  if (!d) return unknown('The agent has not reported yet.', `Check that the discovery agent of ${cluster} is running; this check reads what it reports.`)
  const quiet = (now - Date.parse(d.reportedAt)) / 1000
  if (connected === false || (Number.isFinite(quiet) && quiet > STALE_REPORT_SECONDS)) {
    return unknown(`Its agent has not reported for ${uptimeWords(Math.max(0, quiet))}, so whether data arrives is not known.`, `Check that the discovery agent of ${cluster} is running and can reach this server.`)
  }

  const running = d.installedTelemetry ?? []
  const missing = wanted.filter((id) => !running.includes(id))
  if (missing.length > 0) {
    const names = missing.map((id) => TELEMETRY_SIGNALS.find((s) => s.id === id)?.label ?? id).join(', ')
    if (waited < ARRIVAL_PATIENCE_SECONDS) return unknown(`Waiting for the command to take effect: the agent does not yet report ${names} running.`)
    return {
      health: 'attention',
      headline: `${uptimeWords(waited)} later the agent still does not report ${names} running, so the command has probably not been applied.`,
      todo: `Run the command in ${cluster} and read its output for errors; its namespace and release name must be the agent's.`,
      rows: [],
    }
  }

  const reach = exportReach(d)
  if (reach === 'not-reported') return unknown('This agent does not report whether data leaves the cluster (an older install, or telemetry.health is off).', `Look at the collectors' logs in ${cluster}.`)
  if (reach === 'unreadable') {
    return { health: 'attention', headline: `The agent cannot read the collectors' counters${d.exportHealth?.lastError ? ` (${d.exportHealth.lastError})` : ''}, so it cannot say whether data arrives.`, todo: `Check that the collectors are running in ${cluster} and that no network policy stops the agent reaching them.`, rows: [] }
  }

  const rows: ArrivalRow[] = exportRows(wanted, d.exportHealth!).map((r) => {
    const label = MODALITY_LABEL[r.modality]
    const last = ago(r.lastSentAt, now)
    const lastData = last ? `Last data ${last}` : 'No data yet'
    if (r.state === 'failing') return { modality: r.modality, label, health: 'broken', lastData, sentence: `${r.failed.toLocaleString()} sends failed (the last ${ago(r.lastFailedAt, now) ?? 'recently'}) and none got through since.` }
    if (r.state === 'exporting') return { modality: r.modality, label, health: 'healthy', lastData, sentence: 'The destination accepted the data.' }
    if (r.state === 'silent') return { modality: r.modality, label, health: 'healthy', lastData, sentence: `Nothing in the last few minutes. That is normal when nothing is producing ${label.toLowerCase()} right now.` }
    if (waited >= ARRIVAL_PATIENCE_SECONDS) return { modality: r.modality, label, health: 'attention', lastData, sentence: `Nothing has gone out after ${uptimeWords(waited)}.` }
    return { modality: r.modality, label, health: 'unknown', lastData, sentence: 'The first data usually goes out within a minute or two.' }
  })
  const health = rows.reduce<Health>((w, r) => (HEALTH_RANK[r.health] > HEALTH_RANK[w] ? r.health : w), 'healthy')
  const h = d.exportHealth!
  const partial = h.podsFailed > 0 ? `${h.podsFailed} of ${h.podsReached + h.podsFailed} collector reads failed on the last attempt${h.lastError ? ` (${h.lastError})` : ''}; the rest still count.` : undefined
  if (health === 'healthy') return { health, headline: 'Data is arriving.', rows, partial }
  if (health === 'unknown') return { health, headline: 'Waiting for the first data.', rows, partial }
  if (health === 'broken') return { health, headline: 'The destination is refusing the data or cannot be reached.', todo: 'Go back to Where to send and check the address, the protocol, the credential and the certificate; then run the command again.', rows, partial }
  return { health, headline: 'Nothing has arrived yet.', todo: `Check the collector pods in ${cluster} are running, and that the destination's address is reachable from there.`, rows, partial }
}

export interface ReviewNotes {
  /** What running the command does to the cluster. */
  changes: string[]
  /** What it leaves alone. */
  unchanged: string[]
  /** How to undo it. */
  stop: string[]
}

const list = (xs: string[]) => (xs.length <= 2 ? xs.join(' and ') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`)

/**
 * The review step in plain words, from the draft alone: what changes in the cluster, what does not, and how to stop. `diff` is what the
 * command changes against what the install reports (describeTelemetryChanges); with an install to compare against it is the list, and
 * without one (nothing is installed yet) everything is new, so the draft is described instead.
 */
export function reviewNotes(o: { draft: TelemetryInput; diff: string[]; installed: boolean; turningOff: boolean; destination: string; kept: string[]; /** With one destination per signal type: "metrics to Mimir", "logs to Loki". */ routes?: string[] }): ReviewNotes {
  const { draft, diff, installed, turningOff, destination, kept, routes } = o
  const unchanged = [
    'What the discovery agent may see: its access level stays as approved.',
    'Your applications: nothing in them is edited. The collectors only read from the cluster.',
    kept.length > 0 ? `Everything else in the agent's release, including ${list(kept)}.` : "Everything else in the agent's release.",
  ]
  if (turningOff) {
    return { changes: ['Turns every telemetry signal off, and clears its routes and extra processors.', 'Nothing is sent from the cluster any more once the command has run.'], unchanged, stop: ['Run this setup again and pick signals to turn them back on.'] }
  }
  const stop = ['Run this setup again with nothing picked: the command turns every signal off.', 'Data already delivered stays at its destination until that destination removes it.']
  if (installed) return { changes: diff.length > 0 ? diff : ['Nothing: this is already how the install is set up, so the command would leave it as it is.'], unchanged, stop }

  const on = TELEMETRY_SIGNALS.filter((s) => (draft as unknown as Record<string, boolean>)[s.id])
  const changes = [`Starts collecting ${list(on.map((s) => s.label))}.`, routes && routes.length > 0 ? `Sends ${list(routes)}.` : `Sends it to ${destination}.`]
  if (draft.redaction) changes.push('Masks values that look like secrets before anything leaves the cluster.')
  if (draft.traces && draft.tracesSamplingPercent < 100) changes.push(`Keeps ${draft.tracesSamplingPercent}% of traces.`)
  const tags = cleanTags(draft.tags)
  if (tags.length > 0) changes.push(`Adds ${tags.length === 1 ? 'one tag' : `${tags.length} tags`} to everything it sends.`)
  return { changes, unchanged, stop }
}

/** What a cluster does today, for the list that picks one: "Sends metrics and logs · Last data 2 min ago", or that nothing is set up. */
export function clusterTelemetryLine(d: AgentDiagnostics | undefined, now: number): string {
  const installed = d?.installedTelemetry ?? []
  if (installed.length === 0) return 'No telemetry set up yet'
  const kinds = (['metrics', 'logs', 'traces'] as Modality[]).filter((m) => TELEMETRY_SIGNALS.some((s) => s.modality === m && installed.includes(s.id)))
  const sends = `Sends ${list(kinds)}`
  const last = d?.exportHealth?.routes.reduce<string | undefined>((l, r) => (r.lastSentAt && (!l || Date.parse(r.lastSentAt) > Date.parse(l)) ? r.lastSentAt : l), undefined)
  return last ? `${sends} · Last data ${ago(last, now)}` : `${sends} · No data yet`
}

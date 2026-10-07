import type { AgentDiagnostics, AgentExportHealth, AgentExportRoute, ExportState } from './consent'
import { TELEMETRY_SIGNALS, type Modality } from './install'

/**
 * What the agent's export counters say about each signal type this install sends: one row per type that has
 * a signal turned on, in the order the wizard shows them. A type that has a signal on but no route in the
 * report is `waiting` - the collector counts a route only once something has gone through it, so "absent"
 * is exactly "nothing has gone out yet".
 */
export interface ExportRow {
  modality: Modality
  /** The signals behind it, by label: Resource usage, Kubernetes state... */
  signals: string[]
  state: ExportState
  /** The exporters its data goes through (normally one). */
  exporters: string[]
  sent: number
  failed: number
  lastSentAt?: string
  lastFailedAt?: string
}

/** How alarming each state is, so that several exporters for one signal type show the worst. */
const RANK: Record<ExportState, number> = { waiting: 0, exporting: 1, silent: 2, failing: 3 }

const ORDER: Modality[] = ['metrics', 'logs', 'traces']

const later = (a?: string, b?: string) => (a && b ? (Date.parse(a) >= Date.parse(b) ? a : b) : (a ?? b))

export function exportRows(installed: string[], health: AgentExportHealth): ExportRow[] {
  const rows: ExportRow[] = []
  for (const modality of ORDER) {
    const signals = TELEMETRY_SIGNALS.filter((s) => s.modality === modality && installed.includes(s.id)).map((s) => s.label)
    if (signals.length === 0) continue
    const routes = health.routes.filter((r) => r.signal === modality)
    const row: ExportRow = { modality, signals, state: 'waiting', exporters: [], sent: 0, failed: 0 }
    for (const r of routes) {
      row.exporters.push(r.exporter)
      row.sent += r.sent
      row.failed += r.failed
      if (RANK[r.state] > RANK[row.state]) row.state = r.state
      row.lastSentAt = later(row.lastSentAt, r.lastSentAt)
      row.lastFailedAt = later(row.lastFailedAt, r.lastFailedAt)
    }
    rows.push(row)
  }
  return rows
}

/** Whether the agent could read the collectors' counters at all. */
export type ExportReach = 'not-reported' | 'unreadable' | 'ok'

export function exportReach(d: AgentDiagnostics | undefined): ExportReach {
  const h = d?.exportHealth
  if (!h) return 'not-reported'
  return h.podsReached === 0 && h.podsFailed > 0 ? 'unreadable' : 'ok'
}

export type { AgentExportRoute }

/** One dot's worth of a whole agent's export health, for a table row: the worst state of any signal type it sends, in words. */
export type ExportSummary = { kind: 'online' | 'offline' | 'starting' | 'idle'; text: string }

/** `exportRows`' worst row as one dot and one short phrase. Nothing reported (an older agent, or counters it cannot read) is
 *  the neutral hollow dot, never a fault: it says nothing about the data itself. */
export function exportSummary(installed: string[], d: AgentDiagnostics | undefined): ExportSummary {
  if (installed.length === 0) return { kind: 'idle', text: 'Nothing installed' }
  const reach = exportReach(d)
  if (reach === 'not-reported') return { kind: 'idle', text: 'Not reported' }
  if (reach === 'unreadable') return { kind: 'idle', text: 'Cannot read counters' }
  const rows = exportRows(installed, d!.exportHealth!)
  const worst = rows.reduce<ExportState | undefined>((w, r) => (w === undefined || RANK[r.state] > RANK[w] ? r.state : w), undefined)
  switch (worst) {
    case 'failing':
      return { kind: 'offline', text: 'Failing' }
    case 'exporting':
      // One signal type going out says nothing for another that has not started: "Sending" for the whole install would overclaim.
      if (rows.some((r) => r.state === 'waiting')) return { kind: 'starting', text: 'Partly sending' }
      return { kind: 'online', text: 'Sending' }
    case 'silent':
      return { kind: 'idle', text: 'Quiet' }
    default:
      return { kind: 'starting', text: 'Waiting for data' }
  }
}

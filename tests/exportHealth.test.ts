import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AgentDiagnostics, AgentExportHealth, AgentExportRoute } from '../src/lib/consent'
import { exportReach, exportRows, exportSummary } from '../src/lib/exportHealth'

const route = (over: Partial<AgentExportRoute>): AgentExportRoute => ({
  exporter: 'otlp',
  signal: 'metrics',
  state: 'exporting',
  sent: 10,
  failed: 0,
  ...over,
})
const health = (routes: AgentExportRoute[], over: Partial<AgentExportHealth> = {}): AgentExportHealth => ({ podsReached: 2, podsFailed: 0, routes, ...over })

test('exportRows: one row per signal type that has a signal on, in metrics, logs, traces order', () => {
  const rows = exportRows(['traces', 'systemLogs', 'resourceUsage', 'energy'], health([]))
  assert.deepEqual(
    rows.map((r) => r.modality),
    ['metrics', 'logs', 'traces'],
  )
  assert.deepEqual(rows[0].signals, ['Resource usage', 'Energy'])
})

test('exportRows: a signal type with nothing installed has no row, and one with no route yet is waiting', () => {
  const rows = exportRows(['resourceUsage'], health([route({ signal: 'logs' })]))
  assert.equal(rows.length, 1)
  assert.deepEqual(
    {
      m: rows[0].modality,
      s: rows[0].state,
      e: rows[0].exporters,
      n: rows[0].sent,
    },
    { m: 'metrics', s: 'waiting', e: [], n: 0 },
  )
})

test('exportRows: each signal type reads its own route', () => {
  const rows = exportRows(
    ['resourceUsage', 'systemLogs', 'traces'],
    health([
      route({
        exporter: 'otlphttp/metrics',
        signal: 'metrics',
        state: 'exporting',
        sent: 5,
        lastSentAt: '2026-10-06T12:00:00Z',
      }),
      route({
        exporter: 'otlphttp/logs',
        signal: 'logs',
        state: 'failing',
        failed: 9,
      }),
      route({
        exporter: 'zipkin/traces',
        signal: 'traces',
        state: 'silent',
        sent: 3,
      }),
    ]),
  )
  assert.deepEqual(
    rows.map((r) => [r.modality, r.state, r.exporters]),
    [
      ['metrics', 'exporting', ['otlphttp/metrics']],
      ['logs', 'failing', ['otlphttp/logs']],
      ['traces', 'silent', ['zipkin/traces']],
    ],
  )
})

test('exportRows: several exporters for one signal type show the worst state and add up', () => {
  const [row] = exportRows(
    ['resourceUsage'],
    health([
      route({
        exporter: 'otlp',
        state: 'exporting',
        sent: 4,
        lastSentAt: '2026-10-06T12:00:00Z',
      }),
      route({
        exporter: 'otlphttp/x',
        state: 'failing',
        sent: 1,
        failed: 2,
        lastSentAt: '2026-10-06T12:05:00Z',
      }),
    ]),
  )
  assert.deepEqual(
    {
      s: row.state,
      n: row.sent,
      f: row.failed,
      e: row.exporters,
      t: row.lastSentAt,
    },
    {
      s: 'failing',
      n: 5,
      f: 2,
      e: ['otlp', 'otlphttp/x'],
      t: '2026-10-06T12:05:00Z',
    },
  )
})

const diag = (h?: AgentExportHealth) => ({ exportHealth: h }) as AgentDiagnostics
test('exportReach: not reported without the field, unreadable only when nothing was reached and something failed', () => {
  assert.equal(exportReach(undefined), 'not-reported')
  assert.equal(exportReach(diag()), 'not-reported')
  assert.equal(exportReach(diag(health([], { podsReached: 0, podsFailed: 2 }))), 'unreadable')
  assert.equal(exportReach(diag(health([], { podsReached: 1, podsFailed: 1 }))), 'ok')
  // Nothing found and nothing failed: no collectors to read, which is not a failure to read them.
  assert.equal(exportReach(diag(health([], { podsReached: 0, podsFailed: 0 }))), 'ok')
})

test('exportSummary: one dot for the worst signal type, and "not reported" is never a fault', () => {
  const d = (routes: AgentExportRoute[], over: Partial<AgentExportHealth> = {}) => diag(health(routes, over))
  assert.deepEqual(exportSummary([], d([])), { kind: 'idle', text: 'Nothing installed' })
  assert.deepEqual(exportSummary(['resourceUsage'], undefined), { kind: 'idle', text: 'Not reported' })
  assert.deepEqual(exportSummary(['resourceUsage'], d([], { podsReached: 0, podsFailed: 2 })), { kind: 'idle', text: 'Cannot read counters' })
  assert.deepEqual(exportSummary(['resourceUsage'], d([])), { kind: 'starting', text: 'Waiting for data' })
  assert.deepEqual(exportSummary(['resourceUsage'], d([route({})])), { kind: 'online', text: 'Sending' })
  // Metrics are sending and logs have not started: not "Sending" for the install as a whole.
  assert.deepEqual(exportSummary(['resourceUsage', 'systemLogs'], d([route({})])), { kind: 'starting', text: 'Partly sending' })
  // Metrics are sending but logs are failing: the row says the worse of the two.
  assert.deepEqual(exportSummary(['resourceUsage', 'systemLogs'], d([route({}), route({ signal: 'logs', state: 'failing', failed: 3 })])), { kind: 'offline', text: 'Failing' })
})

import { describe, expect, test } from 'vitest'
import type { AgentDiagnostics, AgentExportHealth } from '@/lib/consent'
import { emptyTelemetry } from '@/lib/install'
import { ARRIVAL_PATIENCE_SECONDS, arrivalCheck, clusterTelemetryLine, reviewNotes } from '@/lib/telemetrySetup'

const NOW = Date.parse('2026-10-06T12:10:00Z')
const iso = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString()

function diag(o: { installed?: string[]; health?: AgentExportHealth; reportedAgo?: number } = {}): AgentDiagnostics {
  return { reportedAt: iso(o.reportedAgo ?? 30), agentVersion: '1', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [], installedTelemetry: o.installed ?? ['resourceUsage'], exportHealth: o.health }
}
const route = (signal: string, state: 'waiting' | 'exporting' | 'silent' | 'failing', more: object = {}) => ({ exporter: 'otlp', signal, state, sent: 5, failed: 0, ...more })
const check = (d: AgentDiagnostics | undefined, o: { wanted?: string[]; waited?: number; connected?: boolean } = {}) =>
  arrivalCheck({ wanted: o.wanted ?? ['resourceUsage'], diagnostics: d, connected: o.connected, waited: o.waited ?? 0, now: NOW, cluster: 'vradipus' })

describe('arrivalCheck: whether the data that was just set up arrives', () => {
  test('no report from the agent at all is Unknown, with the one thing to do', () => {
    const a = check(undefined)
    expect(a.health).toBe('unknown')
    expect(a.todo).toMatch(/discovery agent of vradipus is running/)
  })

  test('an agent that has gone quiet is Unknown, not whatever it last said; so is one that is not connected', () => {
    const h: AgentExportHealth = { podsReached: 1, podsFailed: 0, routes: [route('metrics', 'exporting', { lastSentAt: iso(40) })] }
    expect(check(diag({ health: h, reportedAgo: 3600 })).headline).toMatch(/has not reported for 60 min/)
    expect(check(diag({ health: h }), { connected: false }).health).toBe('unknown')
  })

  test('a signal the agent does not yet report running is waiting for the command; after the patience it says the command was probably not applied', () => {
    const d = diag({ installed: [], health: { podsReached: 1, podsFailed: 0, routes: [] } })
    const early = check(d, { waited: 60 })
    expect(early.health).toBe('unknown')
    expect(early.headline).toMatch(/Waiting for the command to take effect.*Resource usage/)
    expect(early.todo).toBeUndefined()
    const late = check(d, { waited: ARRIVAL_PATIENCE_SECONDS + 1 })
    expect(late.health).toBe('attention')
    expect(late.todo).toMatch(/Run the command in vradipus/)
  })

  test('an agent that does not report export health, or cannot read the counters, is said so with what to look at', () => {
    expect(check(diag()).headline).toMatch(/does not report whether data leaves the cluster/)
    const bad = check(diag({ health: { podsReached: 0, podsFailed: 2, lastError: 'no such host', routes: [] } }))
    expect(bad.health).toBe('attention')
    expect(bad.headline).toMatch(/cannot read the collectors.*no such host/)
    expect(bad.todo).toMatch(/network policy/)
  })

  test('each signal type is Healthy with its last data, Not working when sends fail, and a quiet one is not an error', () => {
    const h: AgentExportHealth = {
      podsReached: 3,
      podsFailed: 0,
      routes: [route('metrics', 'exporting', { lastSentAt: iso(120) }), route('logs', 'failing', { sent: 0, failed: 12, lastFailedAt: iso(30) })],
    }
    const a = check(diag({ installed: ['resourceUsage', 'systemLogs'], health: h }), { wanted: ['resourceUsage', 'systemLogs'] })
    const [metrics, logs] = a.rows
    expect(metrics).toMatchObject({ modality: 'metrics', health: 'healthy', lastData: 'Last data 2 min ago' })
    expect(logs).toMatchObject({ modality: 'logs', health: 'broken', lastData: 'No data yet' })
    expect(logs.sentence).toMatch(/12 sends failed .*none got through/)
    expect(a.health).toBe('broken')
    expect(a.todo).toMatch(/Where to send/)

    const quiet = check(diag({ health: { podsReached: 1, podsFailed: 0, routes: [route('metrics', 'silent', { lastSentAt: iso(3000) })] } }))
    expect(quiet.health).toBe('healthy')
    expect(quiet.rows[0].sentence).toMatch(/normal when nothing is producing metrics/)
  })

  test('nothing gone out yet is Unknown at first and Needs attention once the patience has run out', () => {
    const d = diag({ health: { podsReached: 1, podsFailed: 0, routes: [] } })
    expect(check(d, { waited: 30 })).toMatchObject({ health: 'unknown', headline: 'Waiting for the first data.' })
    const late = check(d, { waited: ARRIVAL_PATIENCE_SECONDS })
    expect(late.health).toBe('attention')
    expect(late.rows[0].lastData).toBe('No data yet')
    expect(late.todo).toMatch(/collector pods in vradipus/)
  })

  test('collector reads that failed are named, and the rest still count', () => {
    const h: AgentExportHealth = { podsReached: 2, podsFailed: 1, lastError: 'timeout', routes: [route('metrics', 'exporting', { lastSentAt: iso(20) })] }
    expect(check(diag({ health: h })).partial).toBe('1 of 3 collector reads failed on the last attempt (timeout); the rest still count.')
  })
})

describe('reviewNotes: the review in plain words', () => {
  const draft = { ...emptyTelemetry, resourceUsage: true, traces: true, tracesSamplingPercent: 20, redaction: true, tags: [{ key: 'team', value: 'sre' }] }

  test('before anything is installed it says what starts, where it goes, and what it adds', () => {
    const n = reviewNotes({ draft, diff: [], installed: false, turningOff: false, destination: 'eu-west', kept: [] })
    expect(n.changes).toContain('Sends it to eu-west.')
    expect(n.changes.join(' ')).toMatch(/Starts collecting .*Resource usage.*Traces/)
    expect(n.changes).toContain('Masks values that look like secrets before anything leaves the cluster.')
    expect(n.changes).toContain('Keeps 20% of traces.')
    expect(n.changes).toContain('Adds one tag to everything it sends.')
  })

  test('with an install it is the diff against it, or says nothing changes', () => {
    expect(reviewNotes({ draft, diff: ['Turns on Traces.'], installed: true, turningOff: false, destination: 'x', kept: [] }).changes).toEqual(['Turns on Traces.'])
    expect(reviewNotes({ draft, diff: [], installed: true, turningOff: false, destination: 'x', kept: [] }).changes[0]).toMatch(/^Nothing: this is already how the install is set up/)
  })

  test('what does not change names access and applications, and what is kept as installed', () => {
    const n = reviewNotes({ draft, diff: [], installed: true, turningOff: false, destination: 'x', kept: ['the protocol', 'the credential'] })
    expect(n.unchanged[0]).toMatch(/access level stays as approved/)
    expect(n.unchanged[1]).toMatch(/nothing in them is edited/)
    expect(n.unchanged[2]).toBe("Everything else in the agent's release, including the protocol and the credential.")
  })

  test('how to stop is to run the setup with nothing picked, and what stays at the destination', () => {
    const n = reviewNotes({ draft, diff: [], installed: false, turningOff: false, destination: 'x', kept: [] })
    expect(n.stop[0]).toMatch(/nothing picked: the command turns every signal off/)
    expect(n.stop[1]).toMatch(/already delivered stays at its destination/)
  })

  test('turning everything off says exactly that, and how to turn it back on', () => {
    const n = reviewNotes({ draft: emptyTelemetry, diff: [], installed: true, turningOff: true, destination: '', kept: [] })
    expect(n.changes[0]).toMatch(/Turns every telemetry signal off/)
    expect(n.stop[0]).toMatch(/pick signals to turn them back on/)
  })
})

describe('clusterTelemetryLine: what a cluster does today, for the list that picks one', () => {
  test('nothing set up, or what it sends and when the last data went out', () => {
    expect(clusterTelemetryLine(undefined, NOW)).toBe('No telemetry set up yet')
    expect(clusterTelemetryLine(diag({ installed: [] }), NOW)).toBe('No telemetry set up yet')
    expect(clusterTelemetryLine(diag({ installed: ['resourceUsage', 'systemLogs', 'traces'], health: { podsReached: 1, podsFailed: 0, routes: [route('metrics', 'exporting', { lastSentAt: iso(120) })] } }), NOW)).toBe('Sends metrics, logs and traces · Last data 2 min ago')
    expect(clusterTelemetryLine(diag({ installed: ['resourceUsage'] }), NOW)).toBe('Sends metrics · No data yet')
  })
})

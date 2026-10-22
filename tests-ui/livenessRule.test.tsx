import { describe, expect, test } from 'vitest'
import { healthOf } from '@/lib/agentHealth'
import { healthSummary, type AgentDiagnostics } from '@/lib/consent'
import { probeRecent, type LinkPairLatest } from '@/lib/clusterLinks'
import { agentVerdict } from '@/features/pipeline/lib/operatorsView'
import { buildPlatform } from '@/lib/platformLayer'
import { freshnessOf, type SelfTelemetryEntity } from '@/lib/selfHealth'
import { AGENT_LATE_AFTER_MS, agentLiveness, PROBE_STALE_AFTER_MS, REPORT_STALE_AFTER_SECONDS, reportIsStale } from '@/lib/status'
import { arrivalCheck } from '@/features/pipeline/lib/telemetrySetup'
import type { Agent } from '@/lib/types'

/**
 * The rules that say an agent is late, and a report is stale, are written out in several places. This pins what each of them says at
 * the edge, so that bringing them into one place (or changing any one of them by accident) shows up here. Nothing in it is a choice:
 * it is what the screens do today.
 */

const NOW = Date.parse('2026-10-09T12:00:00Z')
const agoMs = (ms: number) => new Date(NOW - ms).toISOString()

const agent = (over: Partial<Agent>): Agent =>
  ({ id: 'ag', orgId: 'o', name: 'ag', clusterId: 'cl', version: '0.19.3', accessTier: 2, status: 'approved', fingerprint: '', modules: [], ...over }) as Agent

/** What the Platform layer says about the one agent: its status word. */
const platformStatus = (a: Agent) => buildPlatform({ clusters: [{ id: 'cl', name: 'atlas' }], agents: [a], rawAgents: [], intents: [], operators: [] }, NOW).entities.find((e) => e.id === 'agent:ag')!.status

// [connected, heartbeat age in ms or undefined for none, Agents page, Platform layer, Operators view]
const HEARTBEAT: [boolean | undefined, number | undefined, string, string, string][] = [
  [true, 0, 'ok', 'healthy', 'healthy'],
  [true, 30_000, 'ok', 'healthy', 'healthy'],
  [true, 74_999, 'ok', 'healthy', 'healthy'],
  [true, 75_000, 'ok', 'healthy', 'healthy'], // exactly two and a half beats is not yet late
  [true, 75_001, 'late', 'attention', 'attention'],
  [true, 5 * 60_000, 'late', 'attention', 'attention'],
  [true, undefined, 'ok', 'healthy', 'healthy'], // connected and never heard from: nothing to call late
  [false, 0, 'offline', 'down', 'down'],
  [false, 5 * 60_000, 'offline', 'down', 'down'],
  [false, undefined, 'offline', 'down', 'down'],
  [undefined, 0, 'unknown', 'unknown', 'unknown'], // a sample or hand-made agent has no link to judge
  [undefined, 5 * 60_000, 'unknown', 'unknown', 'unknown'],
]

describe('an agent is late after 75 seconds without a heartbeat, in every screen that says so', () => {
  test.each(HEARTBEAT)('connected=%s, last heartbeat %s ms ago', (connected, age, onAgentsPage, onPlatform, onOperators) => {
    const a = agent({ connected, lastHeartbeat: age === undefined ? undefined : agoMs(age) })
    expect(healthOf(a, NOW)).toBe(onAgentsPage)
    expect(platformStatus(a)).toBe(onPlatform)
    expect(agentVerdict(a, NOW).state).toBe(onOperators)
  })

  test('only an approved agent is judged by its heartbeat on the Agents page', () => {
    const live = { connected: true, lastHeartbeat: agoMs(0) }
    expect(healthOf(agent({ ...live, status: 'pending' }), NOW)).toBe('waiting')
    expect(healthOf(agent({ ...live, status: 'expired' }), NOW)).toBe('expired')
    expect(healthOf(agent({ ...live, status: 'revoked' }), NOW)).toBe('gone')
  })

  test('the self-telemetry page calls a sample live up to the same 75 seconds', () => {
    const entity = (ageMs: number): SelfTelemetryEntity => ({ id: 'ag', kind: 'agent', samples: [{ t: agoMs(ageMs) } as SelfTelemetryEntity['samples'][number]] })
    expect(freshnessOf(entity(75_000), NOW)?.live).toBe(true)
    expect(freshnessOf(entity(75_001), NOW)?.live).toBe(false)
    expect(freshnessOf({ id: 'ag', kind: 'agent', samples: [] }, NOW)).toBeNull()
  })

  test('the constants themselves', () => {
    expect(AGENT_LATE_AFTER_MS).toBe(75_000)
  })
})

const diagnostics = (reportedAgoS: number, over: Partial<AgentDiagnostics> = {}): AgentDiagnostics =>
  ({
    reportedAt: agoMs(reportedAgoS * 1000), agentVersion: '0.19.3', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0,
    collectors: [], informers: [], problems: [], installedTelemetry: [], ...over,
  }) as AgentDiagnostics

describe('what an agent last said about itself is history after 15 minutes, or at once when it is not connected', () => {
  test('the health line', () => {
    expect(healthSummary(diagnostics(899), { now: NOW, connected: true }).level).toBe('healthy')
    expect(healthSummary(diagnostics(900), { now: NOW, connected: true }).level).toBe('healthy')
    expect(healthSummary(diagnostics(901), { now: NOW, connected: true }).level).toBe('unknown')
    expect(healthSummary(diagnostics(1), { now: NOW, connected: false }).level).toBe('unknown')
    expect(healthSummary(diagnostics(1), { now: NOW, connected: undefined }).level).toBe('healthy')
    expect(healthSummary(diagnostics(901)).level).toBe('healthy') // without a clock there is nothing to compare
  })

  test('the arrival check of the telemetry setup', () => {
    const check = (reportedAgoS: number, connected?: boolean) =>
      arrivalCheck({ wanted: ['resourceUsage'], diagnostics: diagnostics(reportedAgoS), connected, waited: 10, now: NOW, cluster: 'atlas' }).headline
    expect(check(899, true)).toMatch(/Waiting for the command/)
    expect(check(900, true)).toMatch(/Waiting for the command/)
    expect(check(901, true)).toMatch(/has not reported for/)
    expect(check(1, false)).toMatch(/has not reported for/)
    expect(check(1, undefined)).toMatch(/Waiting for the command/)
  })

  test('a probe of a link pair counts as recent for the same 15 minutes', () => {
    const probed = (ageMs: number) => ({ probedAt: agoMs(ageMs) }) as LinkPairLatest
    expect(probeRecent(probed(15 * 60_000), NOW)).toBe(true)
    expect(probeRecent(probed(15 * 60_000 + 1), NOW)).toBe(false)
    expect(probeRecent(undefined, NOW)).toBe(false)
  })

  test('the constants themselves', () => {
    expect(REPORT_STALE_AFTER_SECONDS).toBe(900)
    expect(PROBE_STALE_AFTER_MS).toBe(15 * 60_000)
  })
})

describe('agentLiveness, the one place the late rule is decided', () => {
  test('it names the state and says how long the agent has been silent', () => {
    expect(agentLiveness({ connected: true, lastHeartbeat: agoMs(10_000) }, NOW)).toEqual({ state: 'live', silentMs: 10_000 })
    expect(agentLiveness({ connected: true, lastHeartbeat: agoMs(90_000) }, NOW)).toEqual({ state: 'late', silentMs: 90_000 })
    expect(agentLiveness({ connected: false, lastHeartbeat: agoMs(90_000) }, NOW)).toEqual({ state: 'down', silentMs: 90_000 })
    expect(agentLiveness({ connected: undefined }, NOW)).toEqual({ state: 'unknown', silentMs: undefined })
    expect(agentLiveness({ connected: true }, NOW)).toEqual({ state: 'live', silentMs: undefined })
  })

  test('a heartbeat that is not a date is not late', () => {
    expect(agentLiveness({ connected: true, lastHeartbeat: 'garbage' }, NOW).state).toBe('live')
  })

  test('a report is stale only by a readable age', () => {
    expect(reportIsStale(900)).toBe(false)
    expect(reportIsStale(901)).toBe(true)
    expect(reportIsStale(Number.NaN)).toBe(false)
    expect(reportIsStale(Number.POSITIVE_INFINITY)).toBe(false)
  })
})

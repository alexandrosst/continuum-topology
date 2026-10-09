import { describe, expect, test } from 'vitest'
import { buildPlatform, shortAge, type PlatformInput } from '@/lib/platformLayer'
import type { Agent, RegionalOperator, TelemetryIntent } from '@/lib/types'

const NOW = Date.parse('2026-10-09T12:00:00Z')
const ago = (s: number) => new Date(NOW - s * 1000).toISOString()

const agent = (id: string, clusterId: string, over: Partial<Agent> = {}): Agent => ({ id, orgId: 'o', name: id, clusterId, version: '0.19.3', accessTier: 2, status: 'approved', fingerprint: '', modules: [], connected: true, lastHeartbeat: ago(20), ...over })

/** What an agent reports about itself: telemetry installed, and what its collector has sent. */
const report = (id: string, state: 'exporting' | 'silent' | 'failing' | 'waiting', lastSentAgoS = 2) => ({
  id,
  diagnostics: {
    reportedAt: ago(30), agentVersion: '0.19.3', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [],
    installedTelemetry: ['resourceUsage'],
    exportHealth: { podsReached: 1, podsFailed: 0, routes: state === 'waiting' ? [] : [{ exporter: 'otlp', signal: 'metrics', state, sent: 10, failed: state === 'failing' ? 3 : 0, lastSentAt: ago(lastSentAgoS) }] },
  },
})

const op = (id: string, over: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id, name: id.replace('op-', ''), status: 'active', sourceClusterIds: [], destination: { kind: 'operator', endpoint: 'c:4317', targetOperatorId: 'op-central' }, createdAt: ago(9e5), createdBy: 'a',
  health: { state: 'online', reporting: true, lastSeenAt: ago(30) }, certState: 'ok', addressState: 'set', ...over,
})
const central = (over: Partial<RegionalOperator> = {}): RegionalOperator => op('op-central', { name: 'central', destination: { kind: 'fusion', endpoint: 'c:4317' }, health: { state: 'online', reporting: false, lastSeenAt: ago(8) }, ...over })

const intent = (agentId: string, to: string): TelemetryIntent => ({
  id: `ti-${agentId}`, agentId, name: 'x', status: 'active', namespaces: [], exclude: [], signals: [{ id: 'resourceUsage', source: 'builtin' }],
  destination: to.startsWith('ext:') ? { kind: 'external', endpoint: to.slice(4) } : { kind: 'operator', endpoint: 'x:4317', targetOperatorId: to }, createdAt: ago(1e5), createdBy: 'a',
})

const base = (over: Partial<PlatformInput> = {}): PlatformInput => ({
  clusters: [{ id: 'cl-a', name: 'atlas' }, { id: 'cl-k', name: 'kestrel' }],
  agents: [agent('ag-a', 'cl-a'), agent('ag-k', 'cl-k')],
  rawAgents: [report('ag-a', 'exporting'), report('ag-k', 'exporting', 5)],
  intents: [intent('ag-a', 'op-eu'), intent('ag-k', 'op-eu')],
  operators: [op('op-eu', { sourceClusterIds: ['cl-a', 'cl-k'] }), central()],
  ...over,
})

const ent = (m: ReturnType<typeof buildPlatform>, id: string) => m.entities.find((e) => e.id === id)!

describe('buildPlatform', () => {
  test('a healthy pipeline: agent and local operator per cluster, then regional, central and FUSION, joined in that order', () => {
    const m = buildPlatform(base(), NOW)
    expect(m.entities.map((e) => e.id)).toEqual(['agent:ag-a', 'local:ag-a', 'agent:ag-k', 'local:ag-k', 'regional:op-eu', 'central', 'fusion'])
    expect(m.entities.every((e) => e.status === 'healthy')).toBe(true)
    expect(m.edges.map((e) => `${e.from}>${e.to}`)).toEqual(['agent:ag-a>local:ag-a', 'agent:ag-k>local:ag-k', 'local:ag-a>regional:op-eu', 'local:ag-k>regional:op-eu', 'regional:op-eu>central', 'central>fusion'])
    // The hop shows the age of the last data it carried when something knows it: an agent's last report, a local operator's own counters, and FUSION's.
    expect(m.edges.map((e) => e.age)).toEqual(['20 s', '20 s', '2 s', '5 s', undefined, '8 s'])
    expect(ent(m, 'local:ag-a').detail).toBe('Metrics')
    expect(ent(m, 'fusion').sentence).toBe('Running, last data just now.')
  })

  test('an agent that is not connected is Not working; its local operator is Unknown, with the reason, not claimed healthy', () => {
    const m = buildPlatform(base({ agents: [agent('ag-a', 'cl-a'), agent('ag-k', 'cl-k', { connected: false, lastHeartbeat: ago(7680) })] }), NOW)
    expect(ent(m, 'agent:ag-k')).toMatchObject({ status: 'down', todo: expect.any(String) })
    // The hop out of an agent that is not working is as bad as it is, and each part keeps its own list of what it sends to.
    expect(m.edges.find((e) => e.id === 'agent:ag-k>local:ag-k')).toMatchObject({ status: 'down', age: '2 h' })
    expect(ent(m, 'local:ag-k').sendsTo.map((t) => t.id)).toEqual(['regional:op-eu'])
    expect(ent(m, 'local:ag-k')).toMatchObject({ status: 'unknown', sentence: 'Not known: its agent has not reported for 2 h.' })
    expect(ent(m, 'agent:ag-a').status).toBe('healthy')
  })

  test('a local operator that went quiet needs attention and says for how long; one whose sends fail is not working', () => {
    const quiet = buildPlatform(base({ rawAgents: [report('ag-a', 'silent', 840), report('ag-k', 'exporting')] }), NOW)
    expect(ent(quiet, 'local:ag-a')).toMatchObject({ status: 'attention', sentence: 'Quiet: no data for 14 min.', todo: expect.any(String) })
    expect(quiet.edges.find((e) => e.from === 'local:ag-a')).toMatchObject({ status: 'attention', age: '14 min' })
    const failing = buildPlatform(base({ rawAgents: [report('ag-a', 'failing'), report('ag-k', 'exporting')] }), NOW)
    expect(ent(failing, 'local:ag-a').status).toBe('down')
    const none = buildPlatform(base({ rawAgents: [report('ag-a', 'waiting'), report('ag-k', 'exporting')] }), NOW)
    expect(ent(none, 'local:ag-a')).toMatchObject({ status: 'unknown', sentence: 'No data yet.' })
  })

  test('certificates renew themselves: only a failing renewal is a problem, and it says what to do', () => {
    const fine = buildPlatform(base(), NOW)
    expect(ent(fine, 'regional:op-eu').sentence).toMatch(/certificates renew automatically/)
    const failing = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'], certState: 'renewal-failing', certStateSince: ago(3 * 86_400) }), central()] }), NOW)
    expect(ent(failing, 'regional:op-eu')).toMatchObject({ status: 'attention', sentence: 'Its certificate is not renewing since 3 d ago.', todo: expect.stringMatching(/Renew it by hand/) })
    const expired = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'], certState: 'expired' }), central()] }), NOW)
    expect(ent(expired, 'regional:op-eu').status).toBe('down')
  })

  test('a regional operator that never opted into health reports is Unknown, an offline one Not working', () => {
    const unreported = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'], health: { state: 'unknown', reporting: false } }), central()] }), NOW)
    expect(ent(unreported, 'regional:op-eu')).toMatchObject({ status: 'unknown', sentence: 'Its health is not reported.' })
    const off = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'], health: { state: 'offline', reporting: true, lastSeenAt: ago(900) } }), central()] }), NOW)
    expect(ent(off, 'regional:op-eu')).toMatchObject({ status: 'down', sentence: 'No heartbeat since 15 min ago.' })
  })

  test('an operator with no address others can use needs attention', () => {
    const m = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'], addressState: 'pending' }), central()] }), NOW)
    expect(ent(m, 'regional:op-eu')).toMatchObject({ status: 'attention', todo: 'Record its address in the Pipeline.' })
  })

  test('FUSION and the central operator are drawn only while FUSION is on; when it is off and the person may know, FUSION is there as Not turned on', () => {
    const operators = [op('op-eu', { sourceClusterIds: ['cl-a'] }), central({ health: { state: 'off', reporting: false } })]
    const off = buildPlatform(base({ operators }), NOW)
    expect(off.entities.map((e) => e.kind)).not.toContain('fusion')
    expect(off.entities.map((e) => e.kind)).not.toContain('central')
    const shown = buildPlatform(base({ operators, showFusionOff: true }), NOW)
    expect(ent(shown, 'fusion')).toMatchObject({ off: true, status: 'unknown', detail: 'Not turned on', todo: expect.any(String) })
    expect(shown.entities.map((e) => e.kind)).not.toContain('central')
    expect(shown.edges.some((e) => e.to === 'fusion')).toBe(false)
    const starting = buildPlatform(base({ operators: [op('op-eu', { sourceClusterIds: ['cl-a'] }), central({ health: { state: 'starting', reporting: false } })] }), NOW)
    expect(ent(starting, 'fusion')).toMatchObject({ status: 'attention', sentence: 'Starting.' })
  })

  test('local operators that send to the central operator directly, or to an address of the organisation’s own, are told apart', () => {
    const m = buildPlatform(base({ intents: [intent('ag-a', 'op-central'), intent('ag-k', 'ext:otel.corp.example:4317')], operators: [central()] }), NOW)
    expect(m.edges.map((e) => `${e.from}>${e.to}`)).toEqual(['agent:ag-a>local:ag-a', 'agent:ag-k>local:ag-k', 'local:ag-a>central', 'central>fusion'])
    expect(ent(m, 'local:ag-k')).toMatchObject({ elsewhere: 'otel.corp.example:4317', sendsTo: [] })
  })

  test('a regional operator whose clusters are not shown is left out', () => {
    const only = { clusters: [{ id: 'cl-a', name: 'atlas' }], agents: [agent('ag-a', 'cl-a')], rawAgents: [report('ag-a', 'exporting')], intents: [intent('ag-a', 'op-eu')] }
    const m = buildPlatform({ ...only, operators: [op('op-eu', { sourceClusterIds: ['cl-a'] }), op('op-ap', { sourceClusterIds: ['cl-x'] }), central()] }, NOW)
    expect(m.entities.filter((e) => e.kind === 'regional').map((e) => e.name)).toEqual(['eu'])
  })

  test('someone who may not list operators still sees their agents and local operators, without a destination', () => {
    const m = buildPlatform(base({ operators: [] }), NOW)
    expect(m.entities.map((e) => e.kind)).toEqual(['agent', 'local', 'agent', 'local'])
    expect(m.edges.map((e) => e.id)).toEqual(['agent:ag-a>local:ag-a', 'agent:ag-k>local:ag-k'])
  })
})

describe('shortAge', () => {
  test('seconds, minutes, hours, days; nothing when there is no time', () => {
    expect([ago(2), ago(840), ago(7200), ago(5 * 86_400)].map((t) => shortAge(t, NOW))).toEqual(['2 s', '14 min', '2 h', '5 d'])
    expect(shortAge(undefined, NOW)).toBeUndefined()
  })
})

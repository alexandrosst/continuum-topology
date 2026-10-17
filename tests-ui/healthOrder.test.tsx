import { describe, expect, test } from 'vitest'
import type { AgentDiagnostics } from '@/lib/consent'
import { buildPlatform } from '@/lib/platformLayer'
import { arrivalCheck } from '@/lib/telemetrySetup'
import type { RegionalOperator } from '@/lib/types'

/**
 * Where several things are summed up in one word, the worst wins: not working, then needs attention, then unknown, then healthy. Each screen
 * that sums up carried its own table for the order; this pins what each of them does with the pairs that can meet, so that one table can
 * replace them. (The operators view, which also has "setting up", is pinned in tests/operatorsView.test.ts.)
 */

const NOW = Date.parse('2026-10-09T12:00:00Z')
const ago = (s: number) => new Date(NOW - s * 1000).toISOString()

const op = (over: Partial<RegionalOperator>): RegionalOperator =>
  ({
    id: 'op-eu', name: 'eu', status: 'active', sourceClusterIds: [], destination: { kind: 'operator', endpoint: 'c:4317', targetOperatorId: 'op-central' }, createdAt: ago(9e5), createdBy: 'a',
    health: { state: 'online', reporting: true, lastSeenAt: ago(30) }, certState: 'ok', addressState: 'set', ...over,
  }) as RegionalOperator

const platformStatus = (o: RegionalOperator) => buildPlatform({ clusters: [], agents: [], rawAgents: [], intents: [], operators: [o] }, NOW).entities.find((e) => e.id === 'regional:op-eu')!.status

describe('a regional operator is as bad as the worst thing about it', () => {
  const online = { state: 'online', reporting: true, lastSeenAt: ago(30) } as RegionalOperator['health']
  const offline = { state: 'offline', reporting: false, lastSeenAt: ago(9000) } as RegionalOperator['health']
  const waiting = { state: 'waiting', reporting: false } as RegionalOperator['health']

  test('on its own', () => {
    expect(platformStatus(op({ health: online }))).toBe('healthy')
    expect(platformStatus(op({ health: waiting }))).toBe('unknown')
    expect(platformStatus(op({ health: offline }))).toBe('down')
    expect(platformStatus(op({ health: online, addressState: 'pending' }))).toBe('attention')
  })

  test('not working beats needs attention, which beats unknown, which beats healthy', () => {
    expect(platformStatus(op({ health: online, certState: 'expired', addressState: 'pending' }))).toBe('down')
    expect(platformStatus(op({ health: offline, addressState: 'pending' }))).toBe('down')
    expect(platformStatus(op({ health: waiting, certState: 'renewal-failing' }))).toBe('attention')
    expect(platformStatus(op({ health: waiting, addressState: 'pending' }))).toBe('attention')
    expect(platformStatus(op({ health: online, certState: 'renewal-failing' }))).toBe('attention')
  })
})

const diagnostics = (routes: { signal: 'metrics' | 'logs'; state: 'exporting' | 'failing' | 'silent' | 'waiting' }[]): AgentDiagnostics =>
  ({
    reportedAt: ago(30), agentVersion: '0.19.3', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [],
    installedTelemetry: ['resourceUsage', 'systemLogs'],
    exportHealth: { podsReached: 1, podsFailed: 0, routes: routes.map((r) => ({ exporter: 'otlp', signal: r.signal, state: r.state, sent: 10, failed: r.state === 'failing' ? 3 : 0, lastSentAt: ago(2) })) },
  }) as AgentDiagnostics

const arrival = (routes: Parameters<typeof diagnostics>[0], waited: number) => arrivalCheck({ wanted: ['resourceUsage', 'systemLogs'], diagnostics: diagnostics(routes), connected: true, waited, now: NOW, cluster: 'atlas' }).health

describe('the check that data arrives sums its signal types up by the worst', () => {
  test('each on its own', () => {
    expect(arrival([{ signal: 'metrics', state: 'exporting' }, { signal: 'logs', state: 'exporting' }], 10)).toBe('healthy')
    expect(arrival([{ signal: 'metrics', state: 'waiting' }, { signal: 'logs', state: 'waiting' }], 10)).toBe('unknown')
    expect(arrival([{ signal: 'metrics', state: 'waiting' }, { signal: 'logs', state: 'waiting' }], 400)).toBe('attention')
    expect(arrival([{ signal: 'metrics', state: 'failing' }, { signal: 'logs', state: 'exporting' }], 10)).toBe('broken')
  })

  test('not working beats needs attention, which beats unknown, which beats healthy', () => {
    expect(arrival([{ signal: 'metrics', state: 'failing' }, { signal: 'logs', state: 'waiting' }], 400)).toBe('broken')
    expect(arrival([{ signal: 'metrics', state: 'failing' }, { signal: 'logs', state: 'waiting' }], 10)).toBe('broken')
    expect(arrival([{ signal: 'metrics', state: 'exporting' }, { signal: 'logs', state: 'waiting' }], 10)).toBe('unknown')
    expect(arrival([{ signal: 'metrics', state: 'exporting' }, { signal: 'logs', state: 'waiting' }], 400)).toBe('attention')
  })
})

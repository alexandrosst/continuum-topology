import { describe, expect, test } from 'vitest'
import { isReportingHealth, operatorLiveness, receiverAuthOf } from '@/lib/operatorHealth'
import { operatorCommandBlock, operatorCommandDraft } from '@/lib/operatorIntent'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'

const NOW = Date.parse('2026-10-05T12:00:00Z')

describe('operatorLiveness', () => {
  test('online, offline with a relative last-seen, and not reported - the text alone carries each', () => {
    expect(operatorLiveness({ health: { state: 'online', lastSeenAt: '2026-10-05T11:59:30Z', reporting: true } }, NOW)).toEqual({ kind: 'online', text: 'Online' })
    expect(operatorLiveness({ health: { state: 'offline', lastSeenAt: '2026-10-05T11:40:00Z', reporting: true } }, NOW)).toEqual({ kind: 'offline', text: 'Offline, last seen 20 min ago' })
    expect(operatorLiveness({ health: { state: 'unknown', reporting: false } }, NOW)).toEqual({ kind: 'unreported', text: 'Health not reported' })
    expect(operatorLiveness({}, NOW)).toEqual({ kind: 'unreported', text: 'Health not reported' })
  })

  test('waiting for the first heartbeat is its own state, and the central operator takes FUSION\'s words', () => {
    expect(operatorLiveness({ health: { state: 'waiting', reporting: true } }, NOW)).toEqual({ kind: 'waiting', text: 'Waiting for first heartbeat' })
    expect(operatorLiveness({ health: { state: 'starting', reporting: false } }, NOW)).toEqual({ kind: 'starting', text: 'Starting' })
    expect(operatorLiveness({ health: { state: 'off', reporting: false } }, NOW)).toEqual({ kind: 'off', text: 'Off' })
    expect(operatorLiveness({ health: { state: 'attention', reporting: false } }, NOW)).toEqual({ kind: 'attention', text: 'Needs attention' })
    expect(operatorLiveness({ health: { state: 'online', reporting: true } }, NOW, { central: true }).text).toBe('Running')
  })

  test('the central operator is running when the server says online, though it has no heartbeat to report', () => {
    // The server's health for op-central is { state: 'online', reporting: false }: FUSION's own state, with nothing sent by it.
    expect(operatorLiveness({ health: { state: 'online', reporting: false } }, NOW, { central: true })).toEqual({ kind: 'online', text: 'Running' })
    // Any other operator that is "online" without having reported is still not taken at its word.
    expect(operatorLiveness({ health: { state: 'online', reporting: false } }, NOW).kind).toBe('unreported')
  })

  test('never reads an operator that is not reporting as offline, whatever state says', () => {
    expect(operatorLiveness({ health: { state: 'offline', reporting: false } }, NOW).kind).toBe('unreported')
  })

  test('receiverAuthOf defaults to bearer; isReportingHealth follows reporting', () => {
    expect(receiverAuthOf({ receiverAuth: 'mtls' })).toBe('mtls')
    expect(receiverAuthOf({})).toBe('bearer')
    expect(receiverAuthOf(undefined)).toBe('bearer')
    expect(isReportingHealth({ health: { state: 'online', reporting: true } })).toBe(true)
    expect(isReportingHealth({})).toBe(false)
  })
})

describe('operatorCommandBlock and receiver authentication', () => {
  const draft: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-eu.continuum-system.svc:4317', exportOperatorId: 'op-eu', exportAuthSecretName: 'receiver-token' }
  const result = { installFragment: '--set telemetry.export.otlp.endpoint=op-eu.continuum-system.svc:4317', secretCommands: ['kubectl create secret generic op-eu-export-mtls ...'] }

  test('a bearer (or unspecified) receiver keeps the credential Secret and its flags', () => {
    for (const receiverAuth of [undefined, 'bearer' as const]) {
      const block = operatorCommandBlock({ install: undefined, draft, result: { ...result, receiverAuth } })
      expect(block).toContain('kubectl create secret generic receiver-token')
      expect(block).toContain('auth.secretName=receiver-token')
    }
  })

  test('a certificate-only receiver drops it', () => {
    const block = operatorCommandBlock({ install: undefined, draft, result: { ...result, receiverAuth: 'mtls' } })
    expect(block).not.toContain('receiver-token')
    // The credential Secret is not named, and the name is stated empty so one an earlier command set stops being sent.
    expect(block).toMatch(/auth\.secretName= /)
    expect(block).not.toContain('auth.secretKey')
    expect(block.startsWith('kubectl create secret generic op-eu-export-mtls')).toBe(true)
    expect(operatorCommandDraft(draft, 'mtls').exportAuthSecretName).toBe('')
    expect(operatorCommandDraft(draft, 'bearer').exportAuthSecretName).toBe('receiver-token')
  })
})

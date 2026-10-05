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
    expect(block).not.toContain('auth.secretName')
    expect(block.startsWith('kubectl create secret generic op-eu-export-mtls')).toBe(true)
    expect(operatorCommandDraft(draft, 'mtls').exportAuthSecretName).toBe('')
    expect(operatorCommandDraft(draft, 'bearer').exportAuthSecretName).toBe('receiver-token')
  })
})

import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { emptyExportTarget, emptyTelemetry, shArg, shQuote, telemetryProblems, type TelemetryInput } from '@/lib/install'

const hasBash = spawnSync('bash', ['--version']).status === 0

const on: TelemetryInput = { ...emptyTelemetry, applicationLogs: true, exportEndpoint: 'otel.example.com:4317' }

describe.skipIf(!hasBash)('what the telemetry form refuses, and the quoting helpers, checked in bash', () => {
  test('a comma in an endpoint is refused (helm --set would split it), so no command is printed', () => {
    for (const bad of ['a.example.com:4317,b.example.com:4317', 'x:4317/a b', "x:4317/'", 'x:4317/\\', 'x:4317/`id`']) {
      const t = { ...on, exportEndpoint: bad }
      expect(telemetryProblems(t).join(' '), bad).toMatch(/endpoint/)
    }
    const lane: TelemetryInput = { ...on, exportEndpoint: '', exportSplit: true, exportLanes: { ...emptyTelemetry.exportLanes, logs: { ...emptyExportTarget, exportEndpoint: 'a:1,b:2' } } }
    expect(telemetryProblems(lane).join(' ')).toMatch(/Logs: the endpoint/)
    expect(telemetryProblems({ ...on, energy: true, energySource: 'existing', energyExistingEndpoint: 'a:1,b:2' }).join(' ')).toMatch(/Energy/)
    expect(telemetryProblems({ ...on, accelerators: true, acceleratorsSource: 'existing', acceleratorsExistingEndpoint: 'a b' }).join(' ')).toMatch(/Accelerators/)
  })

  test('a credential Secret name or key with a space is refused, and a valid one passes', () => {
    for (const bad of [{ exportAuthSecretName: 'my secret' }, { exportAuthSecretName: 'ok', exportAuthSecretKey: 'a key' }, { exportAuthSecretName: 'ok', exportAuthHeaderName: 'X Auth' }, { exportAuthSecretName: 'Upper' }]) {
      const t = { ...on, ...bad }
      expect(telemetryProblems(t).length, JSON.stringify(bad)).toBeGreaterThan(0)
    }
    const good = { ...on, exportAuthSecretName: 'tel-token', exportAuthSecretKey: 'api_key', exportAuthHeaderName: 'X-Api-Key' }
    expect(telemetryProblems(good)).toEqual([])
  })

  test('shArg leaves plain words bare and quotes anything else, including a single quote', () => {
    expect(shArg('otel.example.com:4317')).toBe('otel.example.com:4317')
    expect(shArg('')).toBe('')
    expect(shArg('a; b')).toBe("'a; b'")
    expect(shQuote("it's")).toBe(`'it'\\''s'`)
    for (const v of ["it's; $(id)", 'a\nb', '`x`', '&&']) {
      const r = spawnSync('bash', ['-c', `printf %s ${shArg(v)}`], { encoding: 'utf8' })
      expect(r.stdout).toBe(v)
    }
  })
})


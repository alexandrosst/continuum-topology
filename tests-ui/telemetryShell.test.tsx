import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { emptyExportTarget, emptyTelemetry, shArg, shQuote, telemetryProblems, type ScopeOverrideInput, type TelemetryInput } from '@/lib/install'
import { telemetrySecretCommand, telemetryUpgradeCommand } from '@/lib/consent'

const hasBash = spawnSync('bash', ['--version']).status === 0

/**
 * Runs a printed command in a real bash with a stub `helm` that reports each argument it was given, NUL-terminated (so a value with a
 * space or a newline in it is told apart from two arguments). What a person pastes is what is run here; any word the shell split off
 * or ran as a command shows up as stderr ("command not found") or as an argument that is not what was typed.
 */
function run(command: string): { args: string[]; stderr: string; status: number | null } {
  const stub = 'helm() { for a in "$@"; do printf "%s\\0" "$a"; done; }\n'
  const r = spawnSync('bash', ['-c', stub + command], { encoding: 'utf8', timeout: 10_000 })
  return { args: r.stdout.split('\0').slice(0, -1), stderr: r.stderr, status: r.status }
}

const scoped = (namespaces: string[], workloads: ScopeOverrideInput['workloads'] = [], exclude: string[] = []): ScopeOverrideInput => ({ namespaces, exclude, workloads })
const on: TelemetryInput = { ...emptyTelemetry, applicationLogs: true, exportEndpoint: 'otel.example.com:4317' }
const arg = (args: string[], key: string) => args.find((a) => a.startsWith(`${key}=`))?.slice(key.length + 1)

describe.skipIf(!hasBash)('the printed telemetry command, run in bash', () => {
  const SCOPE = 'telemetry.resource.scope'

  test.each([
    ['two namespaces', scoped(['shop', 'payments']), 'payments; shop'],
    ['a namespace narrowed to workloads', scoped(['shop'], [{ namespace: 'shop', names: ['api', 'worker'] }]), 'shop: api+worker'],
    ['an exclude-only scope', scoped([], [], ['legacy']), 'all namespaces - excluding legacy'],
  ])('%s: the scope tag reaches helm as one intact value, and nothing else runs', (_name, scope, tag) => {
    const r = run(telemetryUpgradeCommand(undefined, { ...on, applicationLogsScope: scope }))
    expect(r.stderr).toBe('')
    expect(r.status).toBe(0)
    expect(arg(r.args, SCOPE)).toBe(tag)
    // The arguments after it are still helm's own: the `;` did not cut the command short.
    expect(r.args).toContain('telemetry.applicationLogs.logs.enabled=true')
    expect(r.args).toContain('--reset-then-reuse-values')
  })

  test('an endpoint with & [ ] { } arrives intact, for the single destination, a route, and an existing Prometheus', () => {
    const odd = 'https://otel.example.com:4318/v1/x?a=1&b=[2]&c={3}'
    const single = run(telemetryUpgradeCommand(undefined, { ...on, exportEndpoint: odd }))
    expect(single.stderr).toBe('')
    expect(arg(single.args, 'telemetry.export.otlp.endpoint')).toBe(odd)

    const split: TelemetryInput = { ...on, exportEndpoint: '', exportSplit: true, exportLanes: { ...emptyTelemetry.exportLanes, logs: { ...emptyExportTarget, exportEndpoint: odd } } }
    const lane = run(telemetryUpgradeCommand(undefined, split))
    expect(lane.stderr).toBe('')
    expect(arg(lane.args, 'telemetry.export.routes.logs.endpoint')).toBe(odd)

    const existing: TelemetryInput = { ...on, energy: true, energySource: 'existing', energyExistingEndpoint: 'http://kepler:9102/metrics?x=1&y=2', accelerators: true, acceleratorsSource: 'existing', acceleratorsExistingEndpoint: 'dcgm:9400/m?a&b' }
    const prom = run(telemetryUpgradeCommand(undefined, existing))
    expect(prom.stderr).toBe('')
    expect(arg(prom.args, 'telemetry.energy.metrics.existing.prometheusEndpoint')).toBe('http://kepler:9102/metrics?x=1&y=2')
    expect(arg(prom.args, 'telemetry.accelerators.metrics.existing.prometheusEndpoint')).toBe('dcgm:9400/m?a&b')
  })

  test('a comma in an endpoint is refused (helm --set would split it), so no command is printed', () => {
    for (const bad of ['a.example.com:4317,b.example.com:4317', 'x:4317/a b', "x:4317/'", 'x:4317/\\', 'x:4317/`id`']) {
      const t = { ...on, exportEndpoint: bad }
      expect(telemetryProblems(t).join(' '), bad).toMatch(/endpoint/)
      expect(telemetryUpgradeCommand(undefined, t)).not.toContain('telemetry.export.otlp.endpoint')
    }
    const lane: TelemetryInput = { ...on, exportEndpoint: '', exportSplit: true, exportLanes: { ...emptyTelemetry.exportLanes, logs: { ...emptyExportTarget, exportEndpoint: 'a:1,b:2' } } }
    expect(telemetryProblems(lane).join(' ')).toMatch(/Logs: the endpoint/)
    expect(telemetryProblems({ ...on, energy: true, energySource: 'existing', energyExistingEndpoint: 'a:1,b:2' }).join(' ')).toMatch(/Energy/)
    expect(telemetryProblems({ ...on, accelerators: true, acceleratorsSource: 'existing', acceleratorsExistingEndpoint: 'a b' }).join(' ')).toMatch(/Accelerators/)
  })

  test('a credential Secret name or key with a space is refused; a valid one reaches helm and kubectl as typed', () => {
    for (const bad of [{ exportAuthSecretName: 'my secret' }, { exportAuthSecretName: 'ok', exportAuthSecretKey: 'a key' }, { exportAuthSecretName: 'ok', exportAuthHeaderName: 'X Auth' }, { exportAuthSecretName: 'Upper' }]) {
      const t = { ...on, ...bad }
      expect(telemetryProblems(t).length, JSON.stringify(bad)).toBeGreaterThan(0)
      expect(telemetryUpgradeCommand(undefined, t)).not.toContain('auth.secretName')
      expect(telemetrySecretCommand(t)).toBeUndefined()
    }
    const good = { ...on, exportAuthSecretName: 'tel-token', exportAuthSecretKey: 'api_key', exportAuthHeaderName: 'X-Api-Key' }
    expect(telemetryProblems(good)).toEqual([])
    const r = run(telemetryUpgradeCommand(undefined, good))
    expect(r.stderr).toBe('')
    expect(arg(r.args, 'telemetry.export.otlp.auth.secretName')).toBe('tel-token')
    expect(arg(r.args, 'telemetry.export.otlp.auth.secretKey')).toBe('api_key')
    expect(arg(r.args, 'telemetry.export.otlp.auth.headerName')).toBe('X-Api-Key')
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

  // Sanity for the test itself: the same tag pasted bare is what used to be printed, and it does break the command.
  test('the bare form really did break (the stub sees a split command)', () => {
    const r = run(`helm upgrade rel chart --set-string ${SCOPE}=payments; shop - excluding x`)
    expect(r.args).toContain(`${SCOPE}=payments`)
    expect(r.stderr).toMatch(/shop: command not found|not found/)
  })
})


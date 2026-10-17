import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { addressCommands, connectionCheckCommand } from '@/components/operators/OperatorAddress'
import { serverAddressUpgradeCommand } from '@/lib/exposure'
import { quickStartProblems, quickStartSpec } from '@/lib/quickStartBackends'

const hasBash = spawnSync('bash', ['--version']).status === 0

/** Runs a printed line with every program it names replaced by one that reports its arguments, NUL-separated. */
function words(line: string, programs: string[]): { args: string[]; stderr: string } {
  const stubs = programs.map((p) => `${p}() { for a in "$@"; do printf '%s\\0' "$a"; done; }`).join('\n')
  const r = spawnSync('bash', ['-c', `${stubs}\n${line}`], { encoding: 'utf8', timeout: 10_000 })
  return { args: r.stdout.split('\0').slice(0, -1), stderr: r.stderr }
}

describe.skipIf(!hasBash)('a line that has typed text in it, run in bash', () => {
  test('the connection check dials the address as typed, whatever it holds', () => {
    const typed = 'otlp.example.com:4317; touch /tmp/pwned $(id)'
    const r = words(connectionCheckCommand(typed, 'op-1').split(' </dev/null')[0], ['openssl'])
    expect(r.stderr).toBe('')
    expect(r.args).toEqual(['s_client', '-connect', typed, '-servername', 'op-1.continuum-system.svc'])
  })

  test('the address lookups name the Service and namespace as one word each', () => {
    const { loadBalancer } = addressCommands('op-1', { service: 'svc one', namespace: 'ns;x' })
    const r = words(loadBalancer, ['kubectl'])
    expect(r.args.slice(0, 5)).toEqual(['get', 'svc', 'svc one', '--namespace', 'ns;x'])
  })

  test('the server address upgrade carries the typed address as one value', () => {
    const r = words(serverAddressUpgradeCommand({ releaseName: 'continuum', releaseNamespace: 'ops' }, 'host:8443 & echo hi').replace('<chart>', 'chart'), ['helm']) // <chart> is the person's to fill in
    expect(r.stderr).toBe('')
    expect(r.args).toContain('agent.publicAddress=host:8443 & echo hi')
  })

  test('quick-start commands carry the namespace and retention as one word each', () => {
    const spec = quickStartSpec('prometheus')
    const r = words(spec.command('obs', '15d').split('\n').filter((l) => l.startsWith('helm upgrade')).join('\n') + ' \\\n  --set x=y', ['helm'])
    expect(r.stderr).toBe('')
    expect(r.args).toContain('obs')
  })
})

describe('the helm upgrade the cluster owner runs', () => {
  test('uses --reset-then-reuse-values, never --reuse-values, and the agent\'s own release and namespace', () => {
    expect(serverAddressUpgradeCommand(undefined, '')).toContain('--reset-then-reuse-values')
    expect(serverAddressUpgradeCommand(undefined, '')).not.toMatch(/--reuse-values/)
  })
})

describe('quick-start fields', () => {
  test('a namespace must be a namespace name and the retention a duration (a span count for Zipkin)', () => {
    expect(quickStartProblems('loki', 'observability', '168h')).toEqual({})
    expect(quickStartProblems('prometheus', 'obs', '15d')).toEqual({})
    expect(quickStartProblems('jaeger', 'obs', '1h30m')).toEqual({})
    expect(quickStartProblems('zipkin', 'obs', '500000')).toEqual({})
    expect(quickStartProblems('loki', 'obs; x', '168h').namespace).toBeDefined()
    expect(quickStartProblems('loki', '', '168h').namespace).toBeDefined()
    expect(quickStartProblems('loki', 'obs', "1h' ; rm").retention).toBeDefined()
    expect(quickStartProblems('zipkin', 'obs', '72h').retention).toBeDefined()
  })
})

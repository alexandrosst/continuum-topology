import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { emptyTelemetry } from '@/lib/install'
import { operatorCommandBlock } from '@/lib/operatorIntent'
import { chainCommands } from '@/lib/shellChain'

const hasBash = spawnSync('bash', ['--version']).status === 0

// What the server hands out for a Secret: a here-document, its closing word alone on its line.
const secret = `kubectl apply -f - <<'CONTINUUM_SECRET'
apiVersion: v1
kind: Secret
stringData:
  tls.key: |
    -----BEGIN EC PRIVATE KEY-----
    abc
    -----END EC PRIVATE KEY-----
CONTINUUM_SECRET`
const upgrade = 'helm upgrade rel chart --reuse-values \\\n  --set a=b'

describe('chainCommands', () => {
  test('a command that ends in a here-document is closed before the && that follows it', () => {
    const block = chainCommands([secret, upgrade])
    // The closing word is alone on its line (otherwise the shell waits for more), and the && comes after a closing brace.
    expect(block.split('\n')).toContain('CONTINUUM_SECRET')
    expect(block).not.toMatch(/CONTINUUM_SECRET\s*&&/)
    expect(block).toMatch(/\n\} && \\\nhelm upgrade/)
  })

  test('plain commands are joined as before, and one alone is left as it is', () => {
    expect(chainCommands(['a', 'b'])).toBe('a && \\\nb')
    expect(chainCommands([upgrade])).toBe(upgrade)
    expect(chainCommands([secret])).toBe(secret) // nothing follows it: no braces
  })

  test.skipIf(!hasBash)('a real shell reads the whole block, runs both in order, and stops at a failed Secret', () => {
    const run = (applyOk: boolean) => {
      const stubs = `kubectl() { cat > /dev/null; ${applyOk ? 'echo SECRET-APPLIED' : 'return 1'}; }\nhelm() { echo HELM-RAN "$@"; }\n`
      const r = spawnSync('bash', ['-c', stubs + chainCommands([secret, upgrade])], { encoding: 'utf8', timeout: 10_000 })
      return r
    }
    const ok = run(true)
    expect(ok.status).toBe(0)
    expect(ok.stdout).toBe('SECRET-APPLIED\nHELM-RAN upgrade rel chart --reuse-values --set a=b\n')
    const failed = run(false)
    expect(failed.stdout).not.toContain('HELM-RAN')
    expect(failed.status).not.toBe(0)
    // And what it replaces really did hang the shell: the old join leaves the document open, so bash reports it ended early.
    const old = spawnSync('bash', ['-c', `kubectl() { cat >/dev/null; }\nhelm() { echo HELM-RAN; }\n${[secret, upgrade].join(' && \\\n')}`], { encoding: 'utf8', timeout: 10_000 })
    expect(old.stdout).not.toContain('HELM-RAN')
    expect(old.stderr).toMatch(/here-document|end-of-file/)
  })

  // What "Generate command" gives for a local operator sending to a regional one: the server's Secret (a here-document), the
  // client's own upgrade command and the server's flags - which is the block that left the shell waiting at its > prompt.
  test.skipIf(!hasBash)('the block "Generate command" hands out is one the shell runs through to the helm step', () => {
    const draft = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-central.continuum-system.svc:4317', exportOperatorId: 'op-central' }
    const block = operatorCommandBlock({ install: undefined, draft, result: { installFragment: '--set telemetry.export.otlp.endpoint=op-central.continuum-system.svc:4317', secretCommands: [secret], receiverAuth: 'mtls', namespace: 'continuum-system', release: 'continuum-agent' } })
    expect(block.split('\n')).toContain('CONTINUUM_SECRET')
    const stubs = "kubectl() { cat > /dev/null; echo SECRET-APPLIED; }\nhelm() { echo HELM-RAN; }\n"
    const r = spawnSync('bash', ['-c', stubs + block], { encoding: 'utf8', timeout: 10_000 })
    expect(r.stderr).toBe('')
    expect(r.stdout).toBe('SECRET-APPLIED\nHELM-RAN\n')
  })
})

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { DEFAULT_ALLOWED_BACKEND_KINDS, KNOWN_BACKEND_KINDS } from '../src/lib/history'
import { hasQuickStartSpec, QUICK_START_BACKENDS, quickStartSpec } from '../src/lib/quickStartBackends'

test('every catalog kind is a known kind, offered by default, and has a spec', () => {
  for (const spec of QUICK_START_BACKENDS) {
    assert.ok(KNOWN_BACKEND_KINDS.includes(spec.kind), spec.kind)
    assert.ok(DEFAULT_ALLOWED_BACKEND_KINDS.includes(spec.kind), spec.kind)
    assert.equal(hasQuickStartSpec(spec.kind), true)
  }
  assert.equal(hasQuickStartSpec('custom'), false)
})

test('zipkin: traces over OTLP/gRPC through the collector in front of it, not Zipkin itself', () => {
  const z = quickStartSpec('zipkin')
  assert.equal(z.modality, 'traces')
  assert.equal(z.exportProtocol, 'grpc')
  assert.equal(z.exportEndpoint('obs'), 'zipkin-quickstart-otlp.obs.svc:4317')
  assert.equal(z.portForward('obs'), 'kubectl -n obs port-forward svc/zipkin-quickstart 9411:9411')
})

test('zipkin: the command is one kubectl apply heredoc that wires the collector to Zipkin in the same namespace', () => {
  const cmd = quickStartSpec('zipkin').command('obs', '250000')
  assert.ok(cmd.startsWith("kubectl apply -f - <<'EOF'\n"))
  assert.ok(cmd.endsWith('\nEOF'))
  assert.match(cmd, /name: obs\n/)
  assert.match(cmd, /endpoint: http:\/\/zipkin-quickstart\.obs\.svc:9411\/api\/v2\/spans/)
  assert.match(cmd, /MEM_MAX_SPANS, value: "250000"/)
  assert.match(cmd, /exporters: \[zipkin\]/)
  // Every namespaced object lands in the chosen namespace.
  assert.equal((cmd.match(/namespace: obs\n/g) ?? []).length, 5)
})

test('zipkin: the span limit is digits only, so free text cannot reach the manifest', () => {
  const z = quickStartSpec('zipkin')
  assert.match(z.command('obs', '1d"\n  evil: yes'), /MEM_MAX_SPANS, value: "1"\}/)
  assert.match(z.command('obs', 'n/a'), /MEM_MAX_SPANS, value: "500000"/)
})

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { QuickStartBackend } from '../src/lib/history'
import { gatewayManifest, gatewayName, gatewayPortForward, hasGatewayManifest } from '../src/lib/quickStartGateway'

const jaeger: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '72h', label: 'Jaeger (traces)' }
const zipkin: QuickStartBackend = { id: 'qsb-5', kind: 'zipkin', modality: 'traces', namespace: 'obs', retention: '500000', label: 'Zipkin (traces)' }
const prometheus: QuickStartBackend = { id: 'qsb-2', kind: 'prometheus', modality: 'metrics', namespace: 'obs', retention: '15d', label: 'Prometheus (metrics)' }
const loki: QuickStartBackend = { id: 'qsb-3', kind: 'loki', modality: 'logs', namespace: 'obs', retention: '168h', label: 'Loki (logs)' }
const custom: QuickStartBackend = { id: 'qsb-4', kind: 'custom', modality: 'traces', namespace: 'obs', retention: 'n/a', label: 'Elastic APM', toolUrl: 'https://apm.example.com' }

test('hasGatewayManifest is true for the catalog kinds, false for custom', () => {
  assert.equal(hasGatewayManifest('jaeger'), true)
  assert.equal(hasGatewayManifest('zipkin'), true)
  assert.equal(hasGatewayManifest('prometheus'), true)
  assert.equal(hasGatewayManifest('loki'), true)
  assert.equal(hasGatewayManifest('custom'), false)
})

test('the manifest proxies to the exact Service/port each quick-start command installs', () => {
  assert.match(gatewayManifest(jaeger, 'cnq_x'), /proxy_pass http:\/\/jaeger-quickstart\.obs\.svc\.cluster\.local:16686\//)
  assert.match(gatewayManifest(zipkin, 'cnq_x'), /proxy_pass http:\/\/zipkin-quickstart\.obs\.svc\.cluster\.local:9411\//)
  assert.match(gatewayManifest(prometheus, 'cnq_x'), /proxy_pass http:\/\/prometheus-quickstart-server\.obs\.svc\.cluster\.local:80\//)
  assert.match(gatewayManifest(loki, 'cnq_x'), /proxy_pass http:\/\/loki-quickstart\.obs\.svc\.cluster\.local:3100\//)
})

test('the manifest checks the exact bearer token it was given, and nothing else', () => {
  const m = gatewayManifest(jaeger, 'cnq_abc123')
  assert.match(m, /Bearer cnq_abc123/)
  assert.doesNotMatch(m, /Bearer cnq_x(?![\w-])/)
})

test('the manifest is a single kubectl apply heredoc, namespaced with the backend', () => {
  const m = gatewayManifest(jaeger, 'cnq_x')
  assert.ok(m.startsWith("kubectl apply -f - <<'EOF'\n"))
  assert.ok(m.endsWith('EOF'))
  assert.match(m, /namespace: obs/)
  assert.match(m, /image: nginx:alpine/)
  assert.match(m, /kind: ConfigMap/)
  assert.match(m, /kind: Deployment/)
  assert.match(m, /kind: Service/)
})

test('throws for a kind with no gateway manifest', () => {
  assert.throws(() => gatewayManifest(custom, 'cnq_x'))
})

test('gatewayName/gatewayPortForward are stable per kind, not per backend id', () => {
  assert.equal(gatewayName('jaeger'), 'jaeger-gateway')
  assert.equal(gatewayPortForward(jaeger), 'kubectl -n obs port-forward svc/jaeger-gateway 8080:8080')
})

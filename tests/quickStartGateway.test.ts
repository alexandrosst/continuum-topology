import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { QuickStartBackend } from '../src/lib/history'
import { GATEWAY_IMAGE, gatewayManifest, gatewayName, gatewayPortForward, hasGatewayManifest } from '../src/lib/quickStartGateway'

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

test('the manifest is one kubectl apply heredoc, namespaced with the backend, then a cleanup line', () => {
  const m = gatewayManifest(jaeger, 'cnq_x')
  assert.ok(m.startsWith("kubectl apply -f - <<'EOF'\n"))
  const end = m.indexOf('\nEOF\n')
  assert.ok(end > 0, 'the heredoc is closed')
  const yaml = m.slice(0, end)
  assert.match(yaml, /namespace: obs/)
  assert.match(yaml, /kind: Secret/)
  assert.match(yaml, /kind: Deployment/)
  assert.match(yaml, /kind: Service/)
  // after the heredoc: only the cleanup of the ConfigMap that earlier versions of this manifest created
  assert.equal(m.slice(end + 5), "kubectl -n obs delete configmap jaeger-gateway-config --ignore-not-found")
})

test('the token is only ever in the Secret, never in a ConfigMap', () => {
  const m = gatewayManifest(jaeger, 'cnq_sekret_value')
  assert.doesNotMatch(m, /kind: ConfigMap/)
  assert.doesNotMatch(m, /configMap:/)
  const docs = m.slice(0, m.indexOf('\nEOF\n')).split(/^---$/m)
  const holding = docs.filter((d) => d.includes('cnq_sekret_value'))
  assert.equal(holding.length, 1, 'exactly one document holds the token')
  assert.match(holding[0], /kind: Secret/)
  // the pod reads it from that Secret
  const dep = docs.find((d) => /kind: Deployment/.test(d)) ?? ''
  assert.match(dep, /secret:\s*\n\s*secretName: jaeger-gateway-config/)
})

test('the image is an exact version, not a moving tag', () => {
  const m = gatewayManifest(jaeger, 'cnq_x')
  assert.match(m, new RegExp(`image: ${GATEWAY_IMAGE.replace(/[.]/g, '\\.')}\\n`))
  assert.match(GATEWAY_IMAGE, /^nginx:\d+\.\d+\.\d+-alpine$/)
  assert.doesNotMatch(m, /image: nginx:(alpine|latest)\b/)
})

test('the pod does not mount a service account token it never uses', () => {
  assert.match(gatewayManifest(jaeger, 'cnq_x'), /automountServiceAccountToken: false/)
})

test('a re-minted token changes the pod template, so applying it rolls the pod', () => {
  const fp = (t: string) => /config-fingerprint: "([0-9a-f]+)"/.exec(gatewayManifest(jaeger, t))?.[1]
  const a = fp('cnq_first')
  const b = fp('cnq_second')
  assert.ok(a && b)
  assert.notEqual(a, b, 'a new token must change the pod template, or the old token stays valid in the running pod')
  assert.equal(a, fp('cnq_first'), 'the same token gives the same template, so a re-apply does not restart the pod')
  assert.ok(!gatewayManifest(jaeger, 'cnq_first').includes(`"${'cnq_first'}"`))
  assert.doesNotMatch(a!, /cnq/)
})

test('throws for a kind with no gateway manifest', () => {
  assert.throws(() => gatewayManifest(custom, 'cnq_x'))
})

test('gatewayName/gatewayPortForward are stable per kind, not per backend id', () => {
  assert.equal(gatewayName('jaeger'), 'jaeger-gateway')
  assert.equal(gatewayPortForward(jaeger), 'kubectl -n obs port-forward svc/jaeger-gateway 8080:8080')
})

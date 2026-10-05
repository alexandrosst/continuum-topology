import { describe, expect, test } from 'vitest'
import { applyDestination, buildDestinationCatalog, destinationGroup, destinationKey, destinationNeedsCredential, layoutDestinations, searchDestinations } from '@/lib/destinationCatalog'
import { detectBackends, imageRepository } from '@/lib/detectBackends'
import { emptyTelemetry } from '@/lib/install'
import type { QuickStartBackend } from '@/lib/history'
import type { RegionalOperator, Service } from '@/lib/types'

// Pure-logic test for the guided wizard's destination step (GuidedWizard.tsx): buildDestinationCatalog
// merges regional operators, external-backend presets and already-quick-started backends into one
// modality-filtered list, never touching the network itself - every assertion below is about what comes
// out of the merge, never about anything actually fetched.

const operator = (overrides: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id: 'op-1',
  orgId: 'org-1',
  name: 'EU regional operator',
  status: 'active',
  sourceClusterIds: [],
  destination: { kind: 'external', endpoint: 'otel-gateway.example.com:4317' },
  createdAt: '2026-01-01T00:00:00Z',
  createdBy: 'alice',
  ...overrides,
})

const backend = (overrides: Partial<QuickStartBackend> = {}): QuickStartBackend => ({
  id: 'qsb-1',
  kind: 'jaeger',
  modality: 'traces',
  namespace: 'observability',
  retention: '72h',
  label: 'Jaeger (traces)',
  ...overrides,
})

describe('buildDestinationCatalog: regional operators', () => {
  test('an operator with no acceptedModalities at all is always compatible, whatever is enabled', () => {
    const { entries } = buildDestinationCatalog({
      operators: [operator()],
      enabledModalities: new Set(['metrics', 'logs', 'traces']),
      quickStartBackends: [],
      isAdmin: true,
    })
    const op = entries.find((e) => e.kind === 'operator')
    expect(op).toBeDefined()
    expect(op!.compatible).toBe(true)
  })

  test('an operator restricted to traces is excluded (shown disabled, with a reason) when only metrics is enabled', () => {
    const { entries } = buildDestinationCatalog({
      operators: [operator({ acceptedModalities: ['traces'] })],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: true,
    })
    const op = entries.find((e) => e.kind === 'operator')
    expect(op).toBeDefined()
    expect(op!.compatible).toBe(false)
    expect(op).toMatchObject({ reason: expect.stringContaining('traces') })
  })

  test('an operator restricted to traces stays compatible once only traces is enabled', () => {
    const { entries } = buildDestinationCatalog({
      operators: [operator({ acceptedModalities: ['traces'] })],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [],
      isAdmin: true,
    })
    expect(entries.find((e) => e.kind === 'operator')!.compatible).toBe(true)
  })

  test('a revoked operator never appears at all', () => {
    const { entries } = buildDestinationCatalog({
      operators: [operator({ status: 'revoked' })],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: true,
    })
    expect(entries.find((e) => e.kind === 'operator')).toBeUndefined()
  })

  test("an operator entry's export endpoint matches the backend's own in-cluster receiver address", () => {
    const { entries } = buildDestinationCatalog({
      operators: [operator({ id: 'op-eu' })],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: true,
    })
    const op = entries.find((e) => e.kind === 'operator')
    expect(op).toMatchObject({ exportEndpoint: 'op-eu.continuum-system.svc:4317', exportProtocol: 'grpc' })
  })
})

describe('buildDestinationCatalog: external presets', () => {
  test('a generic preset (no restriction) is offered regardless of what is enabled', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['metrics', 'logs', 'traces']),
      quickStartBackends: [],
      isAdmin: false,
    })
    expect(entries.find((e) => e.kind === 'external-preset' && e.id === 'honeycomb')).toBeDefined()
  })

  test('Jaeger (traces only) stays in the catalog once a non-traces modality is enabled, but cannot carry it and says why', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: false,
    })
    const jaeger = entries.find((e) => e.kind === 'external-preset' && e.id === 'jaeger')
    expect(jaeger).toBeDefined()
    expect(jaeger!.compatible).toBe(false)
    expect(jaeger!.reason).toBe('Takes traces only, not metrics.')
  })

  test('Jaeger is offered once only traces is enabled, and every preset entry reports compatible: true', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [],
      isAdmin: false,
    })
    const jaeger = entries.find((e) => e.kind === 'external-preset' && e.id === 'jaeger')
    expect(jaeger).toBeDefined()
    expect(jaeger!.compatible).toBe(true)
  })
})

describe('buildDestinationCatalog: already quick-started backends', () => {
  test('a saved backend whose modality is not enabled at all cannot carry what is enabled, and says so', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [backend({ modality: 'traces' })],
      isAdmin: false,
    })
    expect(entries.find((e) => e.kind === 'quickstart')).toMatchObject({ compatible: false, reason: 'Takes traces only, not metrics.' })
  })

  test('a saved backend whose modality is enabled, alone, is compatible and carries its computed endpoint', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [backend({ namespace: 'obs' })],
      isAdmin: false,
    })
    const qs = entries.find((e) => e.kind === 'quickstart')
    expect(qs).toBeDefined()
    expect(qs!.compatible).toBe(true)
    expect(qs).toMatchObject({ exportEndpoint: 'jaeger-quickstart.obs.svc:4317', exportProtocol: 'grpc' })
  })

  test('a saved backend is shown disabled (not hidden) once a second modality it cannot carry is also enabled', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['traces', 'metrics']),
      quickStartBackends: [backend()],
      isAdmin: false,
    })
    const qs = entries.find((e) => e.kind === 'quickstart')
    expect(qs).toBeDefined()
    expect(qs!.compatible).toBe(false)
  })

  test('a "custom" backend never appears - it has no catalog spec, so no endpoint this step could fill in', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [backend({ kind: 'custom', id: 'qsb-custom', label: 'Elastic APM' })],
      isAdmin: false,
    })
    expect(entries.find((e) => e.kind === 'quickstart')).toBeUndefined()
  })
})

describe('buildDestinationCatalog: deploy entry points', () => {
  test('canDeployBackend is always true - TelemetryBackendWizard gates the actual setup form on admin itself', () => {
    expect(buildDestinationCatalog({ operators: [], enabledModalities: new Set(), quickStartBackends: [], isAdmin: false }).canDeployBackend).toBe(true)
    expect(buildDestinationCatalog({ operators: [], enabledModalities: new Set(), quickStartBackends: [], isAdmin: true }).canDeployBackend).toBe(true)
  })

  test('canDeployOperator mirrors isAdmin exactly - creating a regional operator is adminRole-gated server-side', () => {
    expect(buildDestinationCatalog({ operators: [], enabledModalities: new Set(), quickStartBackends: [], isAdmin: false }).canDeployOperator).toBe(false)
    expect(buildDestinationCatalog({ operators: [], enabledModalities: new Set(), quickStartBackends: [], isAdmin: true }).canDeployOperator).toBe(true)
  })
})

describe('layoutDestinations', () => {
  const enabled = new Set(['metrics'] as const)

  test('the organisation\'s own destinations lead and are all that is shown by default; presets wait behind "more"', () => {
    const catalog = buildDestinationCatalog({ operators: [operator()], enabledModalities: new Set(['traces']), quickStartBackends: [backend()], isAdmin: true })
    const layout = layoutDestinations(catalog)
    expect(layout.known.map(destinationKey)).toEqual(['operator-op-1', 'quickstart-qsb-1'])
    expect(layout.primary.map(destinationKey)).toEqual(['operator-op-1', 'quickstart-qsb-1'])
    expect(layout.all.length).toBeGreaterThan(layout.primary.length)
    expect(layout.all.slice(0, 2).map(destinationKey)).toEqual(['operator-op-1', 'quickstart-qsb-1'])
  })

  test('with nothing of its own, the first three of each preset group are what is shown by default', () => {
    const layout = layoutDestinations(buildDestinationCatalog({ operators: [], enabledModalities: enabled, quickStartBackends: [], isAdmin: false }))
    expect(layout.known).toEqual([])
    expect(layout.sections.map((s) => s.group)).toEqual(['self', 'cloud'])
    for (const s of layout.sections) expect(s.shown).toHaveLength(3)
    expect(layout.primary.every((e) => e.kind === 'external-preset')).toBe(true)
    expect(layout.all.length).toBeGreaterThan(layout.primary.length)
  })

  test('an entry that cannot carry what is enabled is "unavailable", never in primary or all', () => {
    const layout = layoutDestinations(buildDestinationCatalog({ operators: [operator({ acceptedModalities: ['traces'] })], enabledModalities: enabled, quickStartBackends: [], isAdmin: true }))
    // Metrics-only presets (Prometheus...) are fine; the traces-only and logs-only ones are unavailable too.
    expect(layout.unavailable.map(destinationKey)).toContain('operator-op-1')
    expect(layout.unavailable.map(destinationKey)).toContain('external-preset-jaeger')
    expect(layout.unavailable.every((e) => !!e.reason)).toBe(true)
    expect(layout.all.some((e) => e.kind === 'operator')).toBe(false)
    expect(layout.known).toEqual([])
  })
})

describe('layoutDestinations: recommended', () => {
  test('only an operator that already has this cluster as a source is recommended, and it sorts first', () => {
    const catalog = buildDestinationCatalog({
      operators: [operator({ id: 'op-a', name: 'A' }), operator({ id: 'op-b', name: 'B', sourceClusterIds: ['cl-1'] })],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: true,
    })
    const layout = layoutDestinations(catalog, { clusterId: 'cl-1' })
    expect([...layout.recommended]).toEqual(['operator-op-b'])
    expect(layout.known.map(destinationKey)).toEqual(['operator-op-b', 'operator-op-a'])
  })

  test('with no cluster known, nothing is recommended and the order is the catalog\'s own', () => {
    const catalog = buildDestinationCatalog({ operators: [operator({ id: 'op-a' }), operator({ id: 'op-b', sourceClusterIds: ['cl-1'] })], enabledModalities: new Set(['metrics']), quickStartBackends: [], isAdmin: true })
    const layout = layoutDestinations(catalog)
    expect(layout.recommended.size).toBe(0)
    expect(layout.known.map(destinationKey)).toEqual(['operator-op-a', 'operator-op-b'])
  })
})

describe('applyDestination', () => {
  test('a preset carries its endpoint pattern, protocol and credential header', () => {
    const catalog = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['metrics']), quickStartBackends: [], isAdmin: false })
    const grafana = catalog.entries.find((e) => e.kind === 'external-preset' && e.id === 'grafana-cloud')!
    const next = applyDestination(emptyTelemetry, grafana)
    expect(next.exportEndpoint).toBe('otlp-gateway-<region>.grafana.net/otlp')
    expect(next.exportProtocol).toBe('http')
    expect(next.exportAuthHeaderName).toBe('Authorization')
    expect(destinationNeedsCredential(grafana)).toBe(true)
  })

  test('an operator carries its in-cluster receiver over gRPC, records its id, and drops what only an external endpoint uses', () => {
    const catalog = buildDestinationCatalog({ operators: [operator()], enabledModalities: new Set(['metrics']), quickStartBackends: [], isAdmin: true })
    const op = catalog.entries.find((e) => e.kind === 'operator')!
    const leftover = { ...emptyTelemetry, exportProtocol: 'http' as const, exportInsecure: true, exportAuthHeaderName: 'x-honeycomb-team', exportAuthSecretName: 'honeycomb-token', exportAuthSecretKey: 'k' }
    const next = applyDestination(leftover, op)
    expect(next.exportEndpoint).toBe('op-1.continuum-system.svc:4317')
    expect(next.exportProtocol).toBe('grpc')
    expect(next.exportOperatorId).toBe('op-1')
    // The receiver is mutual TLS: a previous preset's skip-verify and credential header/Secret must not ride along.
    expect(next.exportInsecure).toBe(false)
    expect([next.exportAuthHeaderName, next.exportAuthSecretName, next.exportAuthSecretKey]).toEqual(['', '', ''])
    expect(destinationNeedsCredential(op)).toBe(false)
  })

  test('exportOperatorId is empty by default and cleared by every other kind of destination', () => {
    expect(emptyTelemetry.exportOperatorId).toBe('')
    const catalog = buildDestinationCatalog({
      operators: [operator()],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [backend()],
      isAdmin: true,
    })
    const picked = applyDestination(emptyTelemetry, catalog.entries.find((e) => e.kind === 'operator')!)
    expect(picked.exportOperatorId).toBe('op-1')
    for (const kind of ['external-preset', 'quickstart'] as const) {
      const e = catalog.entries.find((x) => x.kind === kind)!
      expect(applyDestination(picked, e).exportOperatorId, kind).toBe('')
    }
  })
})

const svc = (o: Partial<Service>): Service => ({ id: 's1', name: 'loki', namespace: 'monitoring', clusterId: 'cl-1', kind: 'deployment', image: 'grafana/loki:3.1.0', replicas: 1, nodeIds: [], status: 'healthy', labels: {}, ...o }) as Service

describe('imageRepository', () => {
  test('drops registry host, tag and digest', () => {
    expect(imageRepository('docker.io/grafana/loki:3.1.0')).toBe('grafana/loki')
    expect(imageRepository('registry.local:5000/team/prometheus:v2@sha256:abc')).toBe('team/prometheus')
    expect(imageRepository('otel/opentelemetry-collector-contrib:0.160.0')).toBe('otel/opentelemetry-collector-contrib')
    expect(imageRepository('prom/prometheus')).toBe('prom/prometheus')
  })
})

describe('detectBackends', () => {
  test('finds a known receiver in the named cluster and builds its in-cluster address from the workload name', () => {
    const found = detectBackends([svc({}), svc({ id: 's2', name: 'prometheus-server', image: 'quay.io/prometheus/prometheus:v3', ports: [9090] })], { clusterId: 'cl-1' })
    expect(found.map((d) => [d.kind.id, d.endpoint])).toEqual([
      ['loki', 'loki.monitoring.svc:3100/otlp'],
      ['prometheus', 'prometheus-server.monitoring.svc:9090/api/v1/otlp'],
    ])
  })

  test('ignores other clusters, the platform’s own namespace, unknown images and a workload that does not expose the receiver port', () => {
    const found = detectBackends(
      [
        svc({ id: 'a', clusterId: 'cl-2' }),
        svc({ id: 'b', namespace: 'continuum-system', image: 'otel/opentelemetry-collector-contrib:0.160.0' }),
        svc({ id: 'c', image: 'nginx:1.27' }),
        svc({ id: 'd', ports: [3101] }),
      ],
      { clusterId: 'cl-1' },
    )
    expect(found).toEqual([])
  })
})

describe('Zipkin', () => {
  test('a running Zipkin is found by its image and addressed by host:port - the chart adds /api/v2/spans', () => {
    const found = detectBackends([svc({ id: 'z', name: 'zipkin', image: 'openzipkin/zipkin-slim:3', ports: [9411] })], { clusterId: 'cl-1' })
    expect(found.map((d) => [d.kind.id, d.kind.protocol, d.endpoint])).toEqual([['zipkin', 'zipkin', 'zipkin.monitoring.svc:9411']])
  })

  test('the catalog carries Zipkin as a traces-only self-hosted destination that picks the zipkin protocol', () => {
    const { entries } = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['traces']), quickStartBackends: [], isAdmin: false })
    const z = entries.find((e) => e.id === 'zipkin')!
    expect(z.kind).toBe('external-preset')
    expect(z.compatible).toBe(true)
    expect(z.accepts).toEqual(['traces'])
    const picked = applyDestination(emptyTelemetry, z)
    expect(picked.exportProtocol).toBe('zipkin')
    expect(picked.exportInsecure).toBe(true)
    // With metrics also on it is greyed out, with the reason, like Jaeger.
    const mixed = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['traces', 'metrics']), quickStartBackends: [], isAdmin: false }).entries.find((e) => e.id === 'zipkin')!
    expect(mixed.compatible).toBe(false)
    expect(mixed.reason).toMatch(/traces only, not metrics/)
  })

  test('a quick-started Zipkin exports natively: its own host:port and the zipkin protocol', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['traces']),
      quickStartBackends: [backend({ id: 'qsb-z', kind: 'zipkin', label: 'Zipkin (traces)' })],
      isAdmin: false,
    })
    const q = entries.find((e) => e.kind === 'quickstart')!
    if (q.kind !== 'quickstart') throw new Error('expected a quick-start entry')
    expect([q.exportEndpoint, q.exportProtocol]).toEqual(['zipkin-quickstart.observability.svc:9411', 'zipkin'])
  })
})

describe('buildDestinationCatalog: found in the cluster', () => {
  const catalog = (enabled: Array<'metrics' | 'logs' | 'traces'>, clusterId = 'cl-1') =>
    buildDestinationCatalog({ operators: [], enabledModalities: new Set(enabled), quickStartBackends: [], isAdmin: false, services: [svc({})], clusterId })

  test('a detected receiver leads its own group and carries the signals it takes', () => {
    const entry = catalog(['logs']).entries.find((e) => e.kind === 'detected')!
    expect(entry).toMatchObject({ label: 'Grafana Loki', compatible: true, accepts: ['logs'], exportEndpoint: 'loki.monitoring.svc:3100/otlp', exportProtocol: 'http' })
    expect(destinationGroup(entry)).toBe('cluster')
    const layout = layoutDestinations(catalog(['logs']))
    expect(layout.sections[0].group).toBe('cluster')
    // Something already in the cluster is enough for the presets to wait behind "show more".
    expect(layout.primary.map(destinationKey)).toEqual([destinationKey(entry)])
  })

  test('with no cluster named nothing is detected', () => {
    const none = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['logs']), quickStartBackends: [], isAdmin: false, services: [svc({})] })
    expect(none.entries.some((e) => e.kind === 'detected')).toBe(false)
  })

  test('it is greyed out, with a reason, when it cannot carry everything turned on', () => {
    const entry = catalog(['logs', 'metrics']).entries.find((e) => e.kind === 'detected')!
    expect(entry).toMatchObject({ compatible: false, reason: 'Takes logs only, not metrics.' })
  })

  test('picking it sets a plain in-cluster connection with no credential; a lone detected one is never auto-picked as "own"', () => {
    const entry = catalog(['logs']).entries.find((e) => e.kind === 'detected')!
    const next = applyDestination({ ...emptyTelemetry, exportAuthHeaderName: 'x-honeycomb-team', exportAuthSecretName: 'hc' }, entry)
    expect(next).toMatchObject({ exportEndpoint: 'loki.monitoring.svc:3100/otlp', exportProtocol: 'http', exportInsecure: true, exportAuthHeaderName: '', exportAuthSecretName: '', exportOperatorId: '' })
    expect(layoutDestinations(catalog(['logs'])).own).toEqual([])
  })
})

describe('self-hosted presets', () => {
  test('they form their own group, plain in-cluster, and only the ones that take what is turned on are usable', () => {
    const catalog = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['logs']), quickStartBackends: [], isAdmin: false })
    const loki = catalog.entries.find((e) => e.kind === 'external-preset' && e.id === 'loki')!
    expect(destinationGroup(loki)).toBe('self')
    expect(loki.compatible).toBe(true)
    expect(catalog.entries.find((e) => e.kind === 'external-preset' && e.id === 'prometheus')!.compatible).toBe(false)
    const next = applyDestination(emptyTelemetry, loki)
    expect(next).toMatchObject({ exportEndpoint: 'loki.<namespace>.svc:3100/otlp', exportProtocol: 'http', exportInsecure: true })
    // A cloud preset picked afterwards starts verified again.
    const honeycomb = catalog.entries.find((e) => e.kind === 'external-preset' && e.id === 'honeycomb')!
    expect(applyDestination(next, honeycomb).exportInsecure).toBe(false)
  })
})

describe('searchDestinations', () => {
  const catalog = buildDestinationCatalog({ operators: [operator()], enabledModalities: new Set(['logs']), quickStartBackends: [], isAdmin: true })

  test('matches name, address, group and signals, every word having to match', () => {
    const ids = (q: string) => searchDestinations(catalog, q).usable.map(destinationKey)
    expect(ids('grafana')).toEqual(expect.arrayContaining(['external-preset-loki', 'external-preset-grafana-cloud']))
    expect(ids('grafana self')).toEqual(['external-preset-loki'])
    expect(ids('EU')).toContain('operator-op-1')
    expect(ids('nothing like this')).toEqual([])
  })

  test('reports a match that cannot carry the signals separately, with its reason', () => {
    const r = searchDestinations(catalog, 'prometheus')
    expect(r.usable).toEqual([])
    expect(r.unavailable.map(destinationKey)).toEqual(['external-preset-prometheus'])
    expect(r.unavailable[0].reason).toBe('Takes metrics only, not logs.')
  })
})

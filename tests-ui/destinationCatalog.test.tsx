import { describe, expect, test } from 'vitest'
import { applyDestination, buildDestinationCatalog, destinationKey, destinationNeedsCredential, layoutDestinations } from '@/lib/destinationCatalog'
import { emptyTelemetry } from '@/lib/install'
import type { QuickStartBackend } from '@/lib/history'
import type { RegionalOperator } from '@/lib/types'

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

  test('Jaeger (traces only) is left out once a non-traces modality is also enabled - still unchanged, filtering only', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [],
      isAdmin: false,
    })
    expect(entries.find((e) => e.kind === 'external-preset' && e.id === 'jaeger')).toBeUndefined()
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
  test('a saved backend whose modality is not enabled at all is left out', () => {
    const { entries } = buildDestinationCatalog({
      operators: [],
      enabledModalities: new Set(['metrics']),
      quickStartBackends: [backend({ modality: 'traces' })],
      isAdmin: false,
    })
    expect(entries.find((e) => e.kind === 'quickstart')).toBeUndefined()
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

  test('with nothing of its own, the first three presets are what is shown by default', () => {
    const layout = layoutDestinations(buildDestinationCatalog({ operators: [], enabledModalities: enabled, quickStartBackends: [], isAdmin: false }))
    expect(layout.known).toEqual([])
    expect(layout.primary).toHaveLength(3)
    expect(layout.primary.every((e) => e.kind === 'external-preset')).toBe(true)
  })

  test('an entry that cannot carry what is enabled is "unavailable", never in primary or all', () => {
    const layout = layoutDestinations(buildDestinationCatalog({ operators: [operator({ acceptedModalities: ['traces'] })], enabledModalities: enabled, quickStartBackends: [], isAdmin: true }))
    expect(layout.unavailable.map(destinationKey)).toEqual(['operator-op-1'])
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

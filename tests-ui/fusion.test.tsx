import { describe, expect, test } from 'vitest'
import { fusionName, fusionProblems, fusionStores } from '@/lib/fusion'

// The same cases backend/internal/server/fusion_test.go pins: the form shows addresses the server will derive.
describe('fusion naming', () => {
  test('the prefix is the release, with -fusion added unless it already says fusion', () => {
    expect(fusionName('fusion')).toBe('fusion')
    expect(fusionName('prod')).toBe('prod-fusion')
    expect(fusionName('my-fusion')).toBe('my-fusion')
    expect(fusionName('a'.repeat(50))).toBe('a'.repeat(50))
    expect(fusionName('a'.repeat(49))).toBe('a'.repeat(49))
    expect(fusionName('a'.repeat(48))).toBe(`${'a'.repeat(48)}-f`)
  })

  test('the three stores, each at its own in-cluster address', () => {
    expect(fusionStores('', '').map((s) => [s.signal, s.store, s.endpoint])).toEqual([
      ['metrics', 'Prometheus', 'fusion-prometheus.continuum-system.svc:9090/api/v1/otlp'],
      ['logs', 'Loki', 'fusion-loki.continuum-system.svc:3100/otlp'],
      ['traces', 'Tempo', 'fusion-tempo.continuum-system.svc:4317'],
    ])
    expect(fusionStores('eu', 'obs')[2].endpoint).toBe('eu-fusion-tempo.obs.svc:4317')
  })

  test('problems: empty is fine (defaults), a bad name or namespace is not', () => {
    expect(fusionProblems('', '')).toEqual([])
    expect(fusionProblems('eu-1', 'obs')).toEqual([])
    expect(fusionProblems('Has Caps', 'obs')).toHaveLength(1)
    expect(fusionProblems('a'.repeat(51), 'obs')).toHaveLength(1)
    expect(fusionProblems('ok', 'under_score')).toHaveLength(1)
    expect(fusionProblems('-bad', 'bad-')).toHaveLength(2)
  })
})

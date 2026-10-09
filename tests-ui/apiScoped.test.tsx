import { describe, expect, test } from 'vitest'
import { scoped, type Conn } from '@/lib/api'

const c = { url: '', org: 'org-1' } as Conn

describe('scoped', () => {
  test('puts the organisation in the path of per-organisation routes, FUSION switch and retention included', () => {
    expect(scoped(c, '/api/v1/fusion')).toBe('/api/v1/orgs/org-1/fusion')
    expect(scoped(c, '/api/v1/fusion/retention')).toBe('/api/v1/orgs/org-1/fusion/retention')
    expect(scoped(c, '/api/v1/fusion/tokens')).toBe('/api/v1/orgs/org-1/fusion/tokens')
  })

  test('leaves the FUSION data API where the server serves it, with no organisation', () => {
    expect(scoped(c, '/api/v1/fusion/applications?from=now-24h')).toBe('/api/v1/fusion/applications?from=now-24h')
    expect(scoped(c, '/api/v1/fusion/services?from=now-24h')).toBe('/api/v1/fusion/services?from=now-24h')
  })
})

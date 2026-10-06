import { describe, expect, test } from 'vitest'
import { buildDestinationCatalog, catalogOperators, fusionForCatalog, fusionOffer } from '@/lib/destinationCatalog'
import type { FusionStatus } from '@/lib/api'
import type { OperatorDestinationEntry, RegionalOperator } from '@/lib/types'

// Pure logic for the FUSION row of the destination picker: what each person is offered, from where it is read.

const NOW = Date.parse('2026-10-06T12:00:00Z')
const part = (ready: number) => ({ component: 'central' as const, label: 'Central', desired: 1, ready })
const status = (over: Partial<FusionStatus>): FusionStatus => ({ available: true, state: 'running', components: [part(1)], ...over })
const central = (state: OperatorDestinationEntry['health']['state'], over: Partial<OperatorDestinationEntry> = {}): OperatorDestinationEntry =>
  ({ id: 'op-central', kind: 'central', name: 'FUSION', endpoint: 'c.svc:4317', acceptedModalities: [], reachableFromOtherClusters: false, health: { state }, ...over }) as OperatorDestinationEntry
const regional = (id: string): OperatorDestinationEntry => central('online', { id, kind: 'regional', name: id })

describe('fusionOffer: an administrator reads FUSION itself', () => {
  const admin = (s: FusionStatus | null) => fusionOffer({ status: s, destinations: [], isAdmin: true, now: NOW })

  test('running and starting can be sent to; off, attention and unavailable cannot', () => {
    expect(admin(status({}))?.usable).toBe(true)
    expect(admin(status({ state: 'starting', components: [part(0)] }))?.usable).toBe(true)
    expect(admin(status({ state: 'off', components: [{ ...part(0), desired: 0 }] }))?.usable).toBe(false)
    expect(admin(status({ state: 'attention' }))?.usable).toBe(false)
    expect(admin(status({ available: false, state: 'off', reason: 'no-access', message: 'No access.' }))).toMatchObject({ usable: false, kind: 'unavailable', message: 'No access.' })
  })

  test('only an off FUSION on a server that can run it can be switched on from here', () => {
    expect(admin(status({ state: 'off', components: [{ ...part(0), desired: 0 }] }))?.canEnable).toBe(true)
    expect(admin(status({}))?.canEnable).toBe(false)
    expect(admin(status({ available: false, state: 'off', reason: 'other-org' }))?.canEnable).toBe(false)
  })

  test('another organisation\'s FUSION is named as that', () => {
    expect(admin(status({ available: false, state: 'off', reason: 'other-org' }))?.otherOrg).toBe(true)
  })

  test('a server that does not run FUSION at all has no row, rather than a dead one in every list; a server still being asked has one', () => {
    expect(admin(status({ available: false, state: 'off', reason: 'not-configured' }))).toBeUndefined()
    expect(admin(null)?.kind).toBe('checking')
  })

  test('while it starts, how many of its parts are up', () => {
    const offer = admin(status({ state: 'starting', components: [part(1), { ...part(0), component: 'logs' }, { ...part(0), component: 'grafana' }] }))
    expect(offer?.parts).toEqual({ up: 1, wanted: 2 })
  })
})

describe('fusionOffer: anyone else reads the central entry of the read model', () => {
  const editor = (d: OperatorDestinationEntry[]) => fusionOffer({ status: null, destinations: d, isAdmin: false })

  test('no central entry, no row; a running one is usable, an off one asks for an administrator', () => {
    expect(editor([regional('op-eu')])).toBeUndefined()
    expect(editor([central('online')])).toMatchObject({ kind: 'running', usable: true, canEnable: false })
    expect(editor([central('starting')])).toMatchObject({ kind: 'starting', usable: true })
    expect(editor([central('off')])).toMatchObject({ kind: 'off', usable: false, canEnable: false })
  })

  test('the server\'s own recommendation is carried through', () => {
    expect(editor([central('online', { recommended: true })])?.recommended).toBe(true)
  })
})

describe('the catalog with FUSION', () => {
  const op = (id: string): RegionalOperator => ({ id, orgId: 'o', name: id, status: 'active', sourceClusterIds: [], destination: { kind: 'external', endpoint: 'x:1' }, createdAt: '', createdBy: '' })

  test('the central operator is the FUSION row, never a regional one in the list beside it', () => {
    const operators = [op('op-central'), op('op-eu')]
    expect(catalogOperators({ operators, destinations: [], isAdmin: true }).map((o) => o.id)).toEqual(['op-eu'])
    expect(catalogOperators({ operators: [], destinations: [central('online'), regional('op-eu')], isAdmin: false }).map((o) => o.id)).toEqual(['op-eu'])
  })

  test('FUSION is a row whatever its state, takes every signal, and dials the same name an operator would', () => {
    const fusion = fusionForCatalog({ status: status({ state: 'off', components: [{ ...part(0), desired: 0 }] }), operators: [], destinations: [], isAdmin: true })
    const { entries } = buildDestinationCatalog({ operators: [], enabledModalities: new Set(['metrics', 'logs', 'traces']), quickStartBackends: [], isAdmin: true, fusion })
    const row = entries.find((e) => e.kind === 'fusion')
    expect(row).toMatchObject({ id: 'op-central', label: 'FUSION - this server', compatible: true })
    expect(row?.kind === 'fusion' && row.fusion.usable).toBe(false)
  })

  test('the central operator is the server\'s own record once there is one, otherwise a stand-in', () => {
    const real = { ...op('op-central'), name: 'Central (FUSION)', endpoint: 'continuum-fusion-central.continuum.svc:4317' }
    expect(fusionForCatalog({ status: status({}), operators: [real], destinations: [], isAdmin: true })?.central.endpoint).toBe('continuum-fusion-central.continuum.svc:4317')
    expect(fusionForCatalog({ status: status({}), operators: [], destinations: [], isAdmin: true })?.central.id).toBe('op-central')
    expect(fusionForCatalog({ status: status({ available: false, state: 'off', reason: 'not-configured' }), operators: [], destinations: [], isAdmin: true })).toBeUndefined()
  })
})

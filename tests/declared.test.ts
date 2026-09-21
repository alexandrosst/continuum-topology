// The declared / observed split in the browser: what is exported and saved, what a reload brings back, and what an
// import of an older file may carry. Mirrors backend/internal/workspace.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { declaredNote, rehydrate, toDeclared } from '../src/lib/declared'
import { mergeDiscovered, normalizeServerState } from '../src/lib/discovered'
import { normalize } from '../src/lib/migrate'
import { seedTopology } from '../src/lib/seed'
import { SCHEMA_VERSION, type Model } from '../src/lib/types'

const seed = () => seedTopology()
const discovered = (m: Model) => [...m.clusters, ...m.nodes, ...m.namespaces, ...m.services].filter((r) => r.source === 'discovered')

test('toDeclared removes every discovered record and keeps what a person typed', () => {
  const m = seed()
  assert.ok(discovered(m).length > 0, 'the sample holds discovered records')
  const { model, report } = toDeclared(m)
  assert.equal(discovered(model).length, 0)
  assert.equal(model.agents.length, 0)
  assert.equal(model.clusters.length, m.clusters.filter((c) => c.source !== 'discovered').length)
  assert.equal(model.sites.length, m.sites.length)
  assert.equal(model.devices.length, m.devices.length)
  assert.ok(Object.values(report.stripped).reduce((a, b) => a + b, 0) > 0)
})

test('toDeclared is idempotent and leaves nothing that describes an observation', () => {
  const once = toDeclared(seed()).model
  const twice = toDeclared(once)
  assert.deepEqual(twice.model, once)
  assert.equal(declaredNote(twice.report), '')
  for (const l of [once.applications, once.externalEndpoints, once.devices]) for (const r of l) for (const k of ['lastSeen', 'detectedAt', 'revision', 'stale', 'agentId', 'state']) assert.equal(k in r, false, k)
})

test('what a person said about a discovered record survives as a ref, and comes back onto it', () => {
  const m = seed()
  const c = m.clusters.find((x) => x.source === 'discovered')!
  c.overrides = { region: 'eu-west-9' }
  c.siteId = 's-mine'
  const { model } = toDeclared(m)
  assert.deepEqual(model.refs[c.id], { kind: 'cluster', overrides: { region: 'eu-west-9' }, siteId: 's-mine' })
  const back = rehydrate(model, m)
  const again = back.clusters.find((x) => x.id === c.id)!
  assert.deepEqual(again.overrides, { region: 'eu-west-9' })
  assert.equal(again.siteId, 's-mine')
  assert.equal(back.refs[c.id], undefined, 'a ref that found its record is not kept twice')
})

test('a ref for a record not held yet waits, and lands when discovery reports it', () => {
  const m = seed()
  const c = m.clusters.find((x) => x.source === 'discovered')!
  c.overrides = { region: 'x' }
  const declared = toDeclared(m).model
  const empty = { ...declared, clusters: declared.clusters, agents: [] } as Model
  const held = rehydrate(empty, { ...empty, clusters: [] } as Model)
  assert.ok(held.refs[c.id], 'kept while the record is unknown')
  const doc = normalizeServerState({ generatedAt: '2026-09-21T00:00:00Z', agents: [], topology: { clusters: [{ ...c, overrides: undefined }] } } as never)
  const merged = mergeDiscovered({ ...held, clusters: [] } as Model, doc)
  assert.deepEqual(merged.clusters!.find((x) => x.id === c.id)!.overrides, { region: 'x' })
  assert.equal(merged.refs![c.id], undefined)
})

test('an application derived from a label is not a person\'s assignment', () => {
  const m = seed()
  const s = { ...m.services[0], id: 'sv-d', source: 'discovered' as const }
  m.services = [...m.services, s]
  s.applicationHint = 'app-x'
  s.applicationId = 'app-x'
  assert.equal(toDeclared(m).model.refs[s.id], undefined)
  s.applicationId = 'app-mine'
  assert.equal(toDeclared(m).model.refs[s.id]?.applicationId, 'app-mine')
})

test('declaredNote says what was removed and that overrides were kept; empty when nothing was', () => {
  const m = seed()
  m.clusters.find((x) => x.source === 'discovered')!.overrides = { region: 'x' }
  const note = declaredNote(toDeclared(m).report)
  assert.match(note, /Removed \d+ discovered records/)
  assert.match(note, /kept/)
  assert.equal(declaredNote({ stripped: {}, refs: 0, dropped: {} }), '')
})

test('open suggestions, observed dependencies and measured links are dropped; decisions and declared ones stay', () => {
  const m = seed()
  m.suggestions = [
    { id: 'a', status: 'open' },
    { id: 'b', status: 'accepted' },
    { id: 'c', status: 'dismissed' },
  ] as never
  m.dependencies = [
    { id: 'd1', sources: ['observed'] },
    { id: 'd2', sources: ['declared'] },
    { id: 'd3', sources: ['declared', 'observed'] },
  ] as never
  m.siteLinks = [
    { id: 'l1', a: 'x', b: 'y', source: 'measured' },
    { id: 'l2', a: 'x', b: 'z', source: 'declared' },
  ] as never
  const { model, report } = toDeclared(m)
  assert.deepEqual(model.suggestions.map((s) => s.id), ['b', 'c'])
  assert.deepEqual(model.dependencies.map((d) => [d.id, d.sources]), [['d2', ['declared']], ['d3', ['declared']]])
  assert.deepEqual(model.siteLinks.map((l) => l.id), ['l2'])
  assert.equal(report.dropped['open suggestions'], 1)
})

test('a file from a newer version is refused with a message; an older one is upgraded', () => {
  assert.throws(() => normalize({ ...seed(), schemaVersion: SCHEMA_VERSION + 1 }), /newer than this app understands/)
  const old = normalize({ ...seed(), schemaVersion: 3, refs: undefined })
  assert.deepEqual(old.refs, {})
})

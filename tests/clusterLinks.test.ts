import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  PRESETS, VIA_OPTIONS, clusterChecks, costLine, delayWords, everyWords, hasPair, latestView, lossWords, newPair, normalizeLinksLatest, rateWords, presetOf, removePair, replacePair, runnerOf, runsFromWords, throughputFieldProblems, throughputFor, throughputProblems, throughputSummary, viaWords,
} from '../src/lib/clusterLinks'
import type { Agent } from '../src/lib/types'

const agent = (o: Partial<Agent>): Agent => ({ id: 'ag', orgId: 'o', name: 'a', version: '1', accessTier: 2, status: 'approved', fingerprint: '', modules: [], connected: true, observer: { lastReport: '', collectors: [{ node: 'n', method: 'ebpf', bytesKnown: true }], lost: 0 }, ...o }) as Agent

test('pairs are kept in id order, once', () => {
  assert.deepEqual(newPair('b', 'a'), { a: 'a', b: 'b' })
  assert.equal(hasPair([{ a: 'a', b: 'b' }], 'b', 'a'), true)
  assert.deepEqual(removePair([{ a: 'a', b: 'b' }, { a: 'a', b: 'c' }], 'b', 'a'), [{ a: 'a', b: 'c' }])
  assert.deepEqual(replacePair([{ a: 'a', b: 'b' }], { a: 'a', b: 'b', noProbe: true }), [{ a: 'a', b: 'b', noProbe: true }])
})

test('a throughput setting is recognised as a named amount, or custom, or off', () => {
  assert.equal(presetOf(undefined), undefined)
  assert.equal(presetOf(throughputFor('standard')), 'standard')
  assert.equal(presetOf({ intervalMinutes: 360, budgetMB: 501 }), 'custom')
  assert.equal(presetOf({}), 'custom')
  for (const id of Object.keys(PRESETS) as (keyof typeof PRESETS)[]) assert.equal(presetOf(throughputFor(id)), id)
})

test('times and sizes are said in words', () => {
  assert.equal(everyWords(1440), 'every day')
  assert.equal(everyWords(2880), 'every 2 days')
  assert.equal(everyWords(60), 'every hour')
  assert.equal(everyWords(360), 'every 6 hours')
  assert.equal(everyWords(90), 'every 90 minutes')
  assert.deepEqual(throughputSummary({ intervalMinutes: 360, budgetMB: 5120 }), { when: 'every 6 hours', budget: 'up to 5 GB a day' })
  assert.equal(throughputSummary({}).when, "on the cluster's own schedule")
})

test('the form refuses what the agents would clamp', () => {
  assert.deepEqual(throughputProblems('6', '500'), [])
  assert.deepEqual(throughputProblems('0.25', '1'), [])
  assert.equal(throughputProblems('0.1', '500').length, 1)
  assert.equal(throughputProblems('169', '500').length, 1)
  assert.equal(throughputProblems('', '').length, 2)
  assert.equal(throughputProblems('6', '0').length, 1)
  assert.equal(throughputProblems('6', '1.5').length, 1)
  assert.equal(throughputProblems('6', '2000000').length, 1)
})

test('each problem is reported against its own field', () => {
  assert.deepEqual(throughputFieldProblems('6', '500'), {})
  assert.deepEqual(Object.keys(throughputFieldProblems('0.01', '500')), ['hours'])
  assert.deepEqual(Object.keys(throughputFieldProblems('6', '0')), ['budget'])
  assert.deepEqual(Object.keys(throughputFieldProblems('', '')).sort(), ['budget', 'hours'])
})

test('the cost line says how often and where it stops', () => {
  assert.match(costLine(throughputFor('standard')), /^About 4 sets of tests a day, stopping for the day at 500 MB/)
  assert.match(costLine({ intervalMinutes: 4320, budgetMB: 100 }), /^One set of tests every 3 days/)
})

test('the first cluster id runs the tests', () => {
  assert.equal(runnerOf({ a: 'cloud-eu', b: 'edge-athens' }), 'cloud-eu')
})

test('a pair says which cluster probing and the tests run from', () => {
  const p = { a: 'cloud-eu', b: 'edge-athens' }
  assert.equal(runsFromWords(p, 'EU'), 'probing runs from EU; no throughput tests')
  assert.equal(runsFromWords({ ...p, throughput: {} }, 'EU'), 'probing and tests run from EU')
  assert.equal(runsFromWords({ ...p, noProbe: true, throughput: {} }, 'EU'), 'tests run from EU')
  assert.equal(runsFromWords({ ...p, noProbe: true }, 'EU'), 'real traffic only')
})

test('a pinned pair says which kind of address it is measured through', () => {
  const p = { a: 'cloud-eu', b: 'edge-athens', via: 'mesh' as const }
  assert.equal(runsFromWords(p, 'EU'), 'probing runs from EU, through mesh addresses; no throughput tests')
  assert.equal(runsFromWords({ ...p, via: 'public', throughput: {} }, 'EU'), 'probing and tests run from EU, through public addresses')
  assert.equal(runsFromWords({ ...p, via: 'private', noProbe: true, throughput: {} }, 'EU'), 'tests run from EU, through private addresses')
  // nothing active runs, so there is nothing to pin
  assert.equal(runsFromWords({ ...p, noProbe: true }, 'EU'), 'real traffic only')
  assert.equal(viaWords('mesh'), 'mesh addresses')
  assert.deepEqual(VIA_OPTIONS.map((o) => o.value), ['', 'mesh', 'private', 'public'])
  assert.deepEqual(replacePair([{ a: 'a', b: 'b' }], { a: 'a', b: 'b', via: 'public' }), [{ a: 'a', b: 'b', via: 'public' }])
})

test('a cluster is checked from its approved agent', () => {
  assert.equal(clusterChecks('c1', 'cloud', []).every((c) => !c.ok), true)
  assert.equal(clusterChecks('c1', 'cloud', [agent({ clusterId: 'c1' })]).every((c) => c.ok), true)
  const low = clusterChecks('c1', 'cloud', [agent({ clusterId: 'c1', accessTier: 1 })])
  assert.deepEqual(low.map((c) => c.ok), [true, false, true])
  const none = clusterChecks('c1', 'cloud', [agent({ clusterId: 'c1', observer: { lastReport: '', collectors: [], lost: 0 } })])
  assert.deepEqual(none.map((c) => c.ok), [true, true, false])
  assert.equal(clusterChecks('c1', 'cloud', [agent({ clusterId: 'c1', status: 'revoked' })])[0].ok, false)
})

test('figures are said in the units a person reads them in', () => {
  assert.equal(lossWords(0), '0.0 %')
  assert.equal(lossWords(0.018), '1.8 %')
  assert.equal(lossWords(0.12), '12 %')
  assert.equal(delayWords(0.0004), '0.40 ms')
  assert.equal(delayWords(0.0062), '6.2 ms')
  assert.equal(delayWords(0.048), '48 ms')
  assert.equal(delayWords(1.34), '1.3 s')
  assert.equal(rateWords(9.2e8), '920 Mbit/s')
  assert.equal(rateWords(1.25e9), '1.3 Gbit/s')
  assert.equal(rateWords(48_400), '48 kbit/s')
})

test('the latest result of a pair is worded in the order the pair is shown', () => {
  const now = Date.parse('2026-10-14T12:00:00Z')
  const v = latestView({ a: 'x', b: 'y', lossAToB: 0.018, lossBToA: 0, rttSeconds: 0.012, jitterSeconds: 0.0062, mtuBytes: 1420, throughputAToB: 4.8e7, throughputBToA: 9.2e7, probedAt: '2026-10-14T11:57:00Z', testedAt: '2026-10-14T07:00:00Z' }, now)
  assert.ok(v)
  assert.equal(v.loss?.text, 'loss 1.8 % → · 0.0 % ←')
  assert.equal(v.loss?.tone, 'warn')
  assert.deepEqual(v.details, ['round trip 12 ms', 'jitter 6.2 ms', 'MTU 1420', '48 Mbit/s → · 92 Mbit/s ←'])
  assert.deepEqual(v.when, { text: 'probed 3 min ago · tested 5 h ago', stale: false })
})

test('the tone follows the worse direction, and an old probe is marked old', () => {
  const now = Date.parse('2026-10-14T12:00:00Z')
  assert.equal(latestView({ a: 'x', b: 'y', lossAToB: 0.06, lossBToA: 0 }, now)?.loss?.tone, 'bad')
  assert.equal(latestView({ a: 'x', b: 'y', lossAToB: 0.0005, lossBToA: 0 }, now)?.loss?.tone, 'ok')
  assert.equal(latestView({ a: 'x', b: 'y', lossAToB: 0, probedAt: '2026-10-14T09:00:00Z' }, now)?.when?.stale, true)
})

test('a figure that is not there is left out, and a pair with none has no result', () => {
  assert.equal(latestView(undefined), null)
  assert.equal(latestView({ a: 'x', b: 'y' }), null)
  const v = latestView({ a: 'x', b: 'y', lossBToA: 0.001, mtuBytes: 1500 })
  assert.equal(v?.loss?.text, 'loss 0.1 % ←')
  assert.deepEqual(v?.details, ['MTU 1500'])
  assert.equal(v?.when, undefined)
  // a throughput time without a throughput figure is not shown
  assert.equal(latestView({ a: 'x', b: 'y', mtuBytes: 1500, testedAt: '2026-10-14T07:00:00Z' })?.when, undefined)
})

test('the server\'s answer is read as it comes, with nothing missing', () => {
  assert.deepEqual(normalizeLinksLatest(undefined), { available: false, reason: undefined, pairs: [], notes: [], at: '' })
  assert.deepEqual(normalizeLinksLatest({ available: true }).pairs, [])
})

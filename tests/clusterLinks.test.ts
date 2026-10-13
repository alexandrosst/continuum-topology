import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  PRESETS, clusterChecks, costLine, everyWords, hasPair, newPair, presetOf, removePair, replacePair, runnerOf, throughputFor, throughputProblems, throughputSummary,
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

test('the cost line says how often and where it stops', () => {
  assert.match(costLine(throughputFor('standard')), /^About 4 sets of tests a day, stopping for the day at 500 MB/)
  assert.match(costLine({ intervalMinutes: 4320, budgetMB: 100 }), /^One set of tests every 3 days/)
})

test('the first cluster id runs the tests', () => {
  assert.equal(runnerOf({ a: 'cloud-eu', b: 'edge-athens' }), 'cloud-eu')
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

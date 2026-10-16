import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  canAccept,
  canReject,
  changeLines,
  effectLines,
  metricValue,
  outcomeRows,
  percent,
  timeLeft,
  waitingFirst,
  type Proposal,
  type ProposalOutcome,
} from '../src/lib/proposals'

test('a tier change reads as a sentence, with where it comes from when the server said', () => {
  assert.deepEqual(changeLines({ agentId: 'a', tier: 2 }), ['Set the approved access to Services'])
  assert.deepEqual(changeLines({ agentId: 'a', tier: 1 }, { agents: 1, tierFrom: 2, tierTo: 1 }), ['Change the approved access from Services to Infrastructure'])
  assert.deepEqual(changeLines({ agentId: 'a', tier: 9 }), ['Set the approved access to tier 9'], 'an unknown tier is named by its number, not hidden')
})

test('overrides read as sentences, one per part, with the collectors named as the screens name them', () => {
  assert.deepEqual(changeLines({ agentId: 'a', pause: ['flow'] }), ['Pause the traffic observer'])
  assert.deepEqual(changeLines({ agentId: 'a', pause: ['flow', 'measure'], resume: ['probes'] }), ['Pause the traffic observer and path measurements', 'Resume the node probe'])
  assert.deepEqual(changeLines({ agentId: 'a', exclude: ['shop'] }), ['Stop collecting from namespace shop'])
  assert.deepEqual(changeLines({ agentId: 'a', exclude: ['a', 'b', 'c'], include: ['d'] }), ['Stop collecting from namespaces a, b and c', 'Collect from namespace d again'])
  assert.deepEqual(changeLines({ agentId: 'a', pause: ['mystery'] }), ['Pause the mystery'], 'a collector this screen does not know keeps its id')
  assert.deepEqual(changeLines({ agentId: 'a' }), [])
})

test('relative changes read as signed whole percentages', () => {
  assert.equal(percent(-0.5), '−50 %')
  assert.equal(percent(0.2), '+20 %')
  assert.equal(percent(0), '0 %')
  assert.equal(percent(0.004), '0 %', 'a change too small to show has no sign')
  assert.equal(percent(-1), '−100 %')
})

test('a metric is shown in its own unit', () => {
  assert.equal(metricValue('link_bytes_per_sec', 2048), '2.0 KB/s')
  assert.equal(metricValue('agent_rss_bytes', 5 * 1024 * 1024), '5.0 MB')
  assert.equal(metricValue('agent_cpu_cores', 0.126), '0.13 cores')
  assert.equal(metricValue('agent_cpu_cores', 0.004), '<0.01 cores')
  assert.equal(metricValue('something_new', 3), '3')
})

test('predicted effects come in a stable order, known metrics first', () => {
  const e = effectLines({ zeta: 0.1, agent_rss_bytes: -0.1, link_bytes_per_sec: -0.5, agent_cpu_cores: -0.3 })
  assert.deepEqual(e.map((x) => x.metric), ['link_bytes_per_sec', 'agent_cpu_cores', 'agent_rss_bytes', 'zeta'])
  assert.deepEqual(e[0], { metric: 'link_bytes_per_sec', label: 'Traffic to the server', text: '−50 %' })
  assert.deepEqual(effectLines({}), [])
})

test('the outcome puts the expected change beside the observed one, per metric either side mentions', () => {
  const o: ProposalOutcome = {
    result: 'measured',
    predicted: { agent_cpu_cores: -0.3 },
    observed: { before: { agent_cpu_cores: 0.5, link_bytes_per_sec: 1000 }, after: { agent_cpu_cores: 0.4, link_bytes_per_sec: 900 }, change: { agent_cpu_cores: -0.2, link_bytes_per_sec: -0.1 } },
    recordedAt: '2026-10-16T10:00:00Z',
  }
  const rows = outcomeRows(o)
  assert.deepEqual(rows.map((r) => r.metric), ['link_bytes_per_sec', 'agent_cpu_cores'])
  const cpu = rows.find((r) => r.metric === 'agent_cpu_cores')!
  assert.deepEqual([cpu.predicted, cpu.before, cpu.after, cpu.observed], ['−30 %', '0.50 cores', '0.40 cores', '−20 %'])
  const link = rows.find((r) => r.metric === 'link_bytes_per_sec')!
  assert.equal(link.predicted, undefined, 'nothing was predicted for it, and nothing is made up')
  assert.equal(link.observed, '−10 %')
  assert.deepEqual(outcomeRows({ result: 'unconfirmed', predicted: {}, observed: {}, recordedAt: 'x' }), [])
})

test('who may accept: an administrator for a widening, an editor otherwise, and only while it waits', () => {
  const p = (widens: boolean, status: Proposal['status'] = 'pending') => ({ widens, status })
  assert.equal(canAccept('editor', p(false)), true)
  assert.equal(canAccept('editor', p(true)), false)
  assert.equal(canAccept('admin', p(true)), true)
  assert.equal(canAccept('owner', p(true)), true)
  assert.equal(canAccept('viewer', p(false)), false)
  assert.equal(canAccept(undefined, p(false)), false)
  assert.equal(canAccept('admin', p(false, 'applied')), false)
  assert.equal(canAccept('admin', p(false, 'expired')), false)
  // Rejecting never gives anyone more access, so an editor may reject a widening.
  assert.equal(canReject('editor', { status: 'pending' }), true)
  assert.equal(canReject('viewer', { status: 'pending' }), false)
  assert.equal(canReject('admin', { status: 'rejected' }), false)
})

test('time left reads in the unit that matters, and a passed time is expired', () => {
  const now = Date.parse('2026-10-16T10:00:00Z')
  assert.equal(timeLeft('2026-10-16T10:20:00Z', now), '20 min')
  assert.equal(timeLeft('2026-10-16T10:00:20Z', now), '1 min')
  assert.equal(timeLeft('2026-10-16T14:00:00Z', now), '4 h')
  assert.equal(timeLeft('2026-10-20T10:00:00Z', now), '4 d')
  assert.equal(timeLeft('2026-10-16T10:00:00Z', now), 'expired')
  assert.equal(timeLeft('garbage', now), 'expired')
})

test('waiting proposals come first and the server’s order is kept within each group', () => {
  const mk = (id: string, status: Proposal['status']) => ({ id, status }) as Proposal
  const out = waitingFirst([mk('1', 'applied'), mk('2', 'pending'), mk('3', 'rejected'), mk('4', 'pending')])
  assert.deepEqual(out.map((p) => p.id), ['2', '4', '1', '3'])
})

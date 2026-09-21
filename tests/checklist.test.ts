import assert from 'node:assert/strict'
import { test } from 'node:test'
import { deriveChecklist, dismiss, wasDismissed, type ChecklistInput } from '../src/lib/checklist'

const agent = (over: Partial<ChecklistInput['agents'][number]> = {}) => ({ id: 'a1', name: 'edge-1', status: 'approved' as const, connected: true, ...over })
const cluster = (over: Partial<ChecklistInput['clusters'][number]> = {}) => ({ id: 'c1', name: 'edge-1', source: 'discovered' as const, state: 'live' as const, ...over })
const input = (over: Partial<ChecklistInput> = {}): ChecklistInput => ({ imagesConfigured: false, agents: [], clusters: [], ...over })
const by = (c: ReturnType<typeof deriveChecklist>) => Object.fromEntries(c.steps.map((s) => [s.id, s]))

test('a new organisation: the registry is the current step and the note is honest about the built-in names', () => {
  const c = deriveChecklist(input())
  assert.deepEqual(c.steps.map((s) => s.state), ['current', 'todo', 'todo', 'todo', 'todo'])
  assert.equal(c.done, 0)
  assert.equal(c.finished, false)
  assert.match(by(c).images.line, /built-in image names/)
  assert.deepEqual(by(c).images.action, { kind: 'settings', label: 'Set a registry' })
  assert.equal(by(c).connect.action?.kind, 'connect')
})

test('a configured registry is done and says where images come from', () => {
  const c = deriveChecklist(input({ imagesConfigured: true, imageRegistry: 'reg.example.com/team' }))
  assert.equal(by(c).images.state, 'done')
  assert.match(by(c).images.line, /reg\.example\.com\/team/)
  assert.equal(by(c).connect.state, 'current')
})

test('a waiting agent: connect is done, approve is current with a link to its card, and the registry no longer holds the focus', () => {
  const c = deriveChecklist(input({ agents: [agent({ status: 'pending', connected: undefined })] }))
  assert.deepEqual(c.steps.map((s) => s.state), ['todo', 'done', 'current', 'todo', 'todo'])
  assert.deepEqual(by(c).approve.action, { kind: 'approval', label: 'Review approval', agentId: 'a1' })
  assert.match(by(c).approve.line, /edge-1 is waiting/)
  assert.equal(by(c).connect.action, undefined, 'nothing more to do for a step that is done')
})

test('approved but no report yet: see-it-live is current, and it does not claim the cluster is live', () => {
  const c = deriveChecklist(input({ agents: [agent()] }))
  assert.deepEqual(c.steps.map((s) => s.state), ['todo', 'done', 'done', 'current', 'todo'])
  assert.match(by(c).live.line, /Waiting for the agent/)
  assert.equal(by(c).live.action?.kind, 'agents')
  assert.equal(c.finished, false)
  const away = deriveChecklist(input({ agents: [agent({ connected: false })] }))
  assert.match(by(away).live.line, /not connected/)
})

test('a cluster that is reported but not live (stale) is not "live": the reason is shown and the list stays', () => {
  const c = deriveChecklist(input({ agents: [agent()], clusters: [cluster({ state: 'stale', stateReason: 'stale for 2 h', stale: true })] }))
  assert.equal(by(c).live.state, 'current')
  assert.match(by(c).live.line, /stale for 2 h/)
  assert.equal(c.finished, false)
})

test('a live cluster finishes the list; a hand-made cluster does not', () => {
  assert.equal(deriveChecklist(input({ agents: [agent()], clusters: [cluster()] })).finished, true)
  assert.equal(deriveChecklist(input({ clusters: [cluster({ source: 'manual', state: undefined })] })).finished, false)
  assert.equal(deriveChecklist(input({ clusters: [cluster({ deletedAt: '2026-01-01T00:00:00Z' })] })).finished, false)
  assert.equal(deriveChecklist(input({ clusters: [cluster({ state: 'revoked', stale: true })] })).finished, false)
})

test('a request that ran out or was rejected says so instead of pretending nothing happened', () => {
  const exp = deriveChecklist(input({ imagesConfigured: true, agents: [agent({ status: 'expired', connected: undefined })] }))
  assert.match(by(exp).approve.line, /ran out/)
  assert.equal(by(exp).connect.state, 'current', 'nothing is enrolled, so connecting is still the step')
  const rej = deriveChecklist(input({ agents: [agent({ status: 'rejected', connected: undefined })] }))
  assert.match(by(rej).approve.line, /rejected/)
})

test('traffic observation and the node probe are optional and tracked from what reports', () => {
  assert.equal(by(deriveChecklist(input())).observe.optional, true)
  assert.equal(by(deriveChecklist(input({ agents: [agent({ observer: { lastReport: 'x', collectors: [{ node: 'n', method: 'ebpf', bytesKnown: true }], lost: 0 } })] }))).observe.state, 'done')
  assert.equal(by(deriveChecklist(input({ agents: [agent()], probedNodes: 2 }))).observe.state, 'done')
  assert.equal(by(deriveChecklist(input({ agents: [agent({ observer: { lastReport: 'x', collectors: [], lost: 0 } })] }))).observe.state, 'todo', 'an observer with no collectors is not watching anything')
})

test('exactly one step is current at a time, and none once everything before the end is done', () => {
  for (const i of [input(), input({ agents: [agent({ status: 'pending' })] }), input({ agents: [agent()] }), input({ imagesConfigured: true })]) {
    assert.equal(deriveChecklist(i).steps.filter((s) => s.state === 'current').length, 1)
  }
  const all = deriveChecklist(input({ imagesConfigured: true, agents: [agent()], clusters: [cluster()], probedNodes: 1 }))
  assert.equal(all.steps.filter((s) => s.state === 'current').length, 0)
  assert.equal(all.done, 5)
})

test('several agents are counted, not listed in full', () => {
  const c = deriveChecklist(input({ agents: [agent({ id: 'a', name: 'one', status: 'pending' }), agent({ id: 'b', name: 'two', status: 'pending' }), agent({ id: 'c', name: 'three', status: 'pending' })] }))
  assert.match(by(c).connect.line, /3 agents have enrolled \(one and 2 more\)/)
})

test('dismissal is kept per organisation and works without storage', () => {
  const mem = new Map<string, string>()
  const storage = { getItem: (k: string) => mem.get(k) ?? null, setItem: (k: string, v: string) => void mem.set(k, v) }
  assert.equal(wasDismissed('org-a', storage), false)
  dismiss('org-a', storage)
  assert.equal(wasDismissed('org-a', storage), true)
  assert.equal(wasDismissed('org-b', storage), false, 'another organisation is not affected')
  assert.equal(wasDismissed(undefined, storage), false)
  const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
  assert.equal(wasDismissed('org-a', broken), false, 'blocked storage reads as not dismissed')
  assert.doesNotThrow(() => dismiss('org-a', broken))
})

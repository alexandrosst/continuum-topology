import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  collectorState,
  consentChange,
  effectiveNote,
  extrasOf,
  healthSummary,
  helmUpgradeCommand,
  inForce,
  parseExclusions,
  scopeWords,
  sortProblems,
  uptimeWords,
  type AgentDiagnostics,
} from '../src/lib/consent'

const diag = (over: Partial<AgentDiagnostics> = {}): AgentDiagnostics => ({
  reportedAt: '2026-01-01T00:00:00Z',
  agentVersion: '1.0',
  uptimeSeconds: 100,
  installedTier: 2,
  approvedTier: 2,
  effectiveTier: 2,
  pausedCollectors: [],
  excludedNamespaces: 0,
  collectors: [],
  informers: [],
  problems: [],
  ...over,
})

test('a report that has gone stale or lost its connection is not called healthy', () => {
  const now = Date.parse('2026-01-01T01:00:00Z')
  const old = diag({ reportedAt: '2026-01-01T00:00:00Z' })
  const stale = healthSummary(old, { now, connected: true })
  assert.equal(stale.level, 'unknown')
  assert.ok(/^Last report /.test(stale.line) && stale.line !== 'Healthy', stale.line)
  const down = healthSummary(diag({ reportedAt: '2026-01-01T00:59:00Z' }), { now, connected: false })
  assert.equal(down.level, 'unknown')
  assert.ok(down.line.startsWith('Not connected'), down.line)
  assert.equal(healthSummary(diag({ reportedAt: '2026-01-01T00:59:00Z' }), { now, connected: true }).line, 'Healthy')
  const junk = healthSummary(diag({ reportedAt: 'not a date' }), { now, connected: true })
  assert.equal(junk.line, 'Healthy', 'an unreadable date is not treated as stale on its own')
  // the problems that were reported still count when the report is old
  assert.equal(healthSummary(diag({ reportedAt: '2026-01-01T00:00:00Z', problems: [{ code: 'a', severity: 'warn', message: 'm' }] }), { now }).count, 1)
})

test('the health line counts problems that need a person, not notices', () => {
  assert.equal(healthSummary(undefined).level, 'unknown')
  assert.equal(healthSummary(diag()).line, 'Healthy')
  assert.equal(healthSummary(diag({ problems: [{ code: 'override_ignored', severity: 'info', message: 'm' }] })).line, 'Healthy')
  const one = healthSummary(diag({ problems: [{ code: 'clock_skew', severity: 'warn', message: 'm' }] }))
  assert.deepEqual([one.line, one.level, one.count], ['1 problem', 'warn', 1])
  const two = healthSummary(diag({ problems: [{ code: 'a', severity: 'warn', message: 'm' }, { code: 'rbac_forbidden', severity: 'error', message: 'm' }, { code: 'i', severity: 'info', message: 'm' }] }))
  assert.deepEqual([two.line, two.level], ['2 problems', 'error'])
})

test('problems are shown worst first without reordering equals', () => {
  const sorted = sortProblems([
    { code: 'a', severity: 'info', message: '' },
    { code: 'b', severity: 'warn', message: '' },
    { code: 'c', severity: 'error', message: '' },
    { code: 'd', severity: 'warn', message: '' },
  ])
  assert.deepEqual(sorted.map((p) => p.code), ['c', 'b', 'd', 'a'])
})

test('the helm command matches what the server prints', () => {
  // the chart file the server serves
  assert.equal(
    helmUpgradeCommand({ chartFile: 'continuum-agent-0.4.0.tgz', chartRef: '', chartVersion: '0.4.0' }, 2),
    'helm upgrade continuum-agent ./continuum-agent-0.4.0.tgz --namespace continuum-system --reuse-values --set access.tier=2',
  )
  // a registry chart names its version
  assert.equal(
    helmUpgradeCommand({ chartFile: 'x.tgz', chartRef: 'oci://registry.example.com/team/continuum-agent', chartVersion: '0.4.0' }, 1),
    'helm upgrade continuum-agent oci://registry.example.com/team/continuum-agent --version 0.4.0 --namespace continuum-system --reuse-values --set access.tier=1',
  )
  // a chart reference that is itself a file has no version
  assert.ok(!helmUpgradeCommand({ chartRef: 'https://x.example/agent.tgz', chartVersion: '0.4.0' }, 2).includes('--version'))
  assert.ok(helmUpgradeCommand(undefined, 2).endsWith('--reuse-values --set access.tier=2'))
})

test('the note on a lower effective tier says why', () => {
  assert.equal(effectiveNote(diag()), undefined)
  assert.match(effectiveNote(diag({ installedTier: 1, approvedTier: 2, effectiveTier: 1 })) ?? '', /Held back by the install.*infrastructure/)
  assert.match(effectiveNote(diag({ installedTier: 2, approvedTier: 2, effectiveTier: 1 })) ?? '', /not yet applied/)
})

test('collectors are described honestly', () => {
  assert.equal(collectorState(undefined).tone, 'off')
  const base = { name: 'flow', configured: true, enabled: true, producing: true, reporting: 3, expected: 3 }
  assert.deepEqual(collectorState(base), { tone: 'ok', label: 'On, reporting from 3 of 3 nodes' })
  assert.equal(collectorState({ ...base, reporting: 2 }).tone, 'warn')
  assert.match(collectorState({ ...base, producing: false, reporting: 0 }).label, /silent/)
  assert.equal(collectorState({ ...base, pausedByServer: true }).tone, 'paused')
  assert.equal(collectorState({ ...base, configured: false }).label, 'Not installed in this cluster')
  assert.equal(collectorState({ name: 'measure', configured: true, enabled: true, producing: true, reporting: 0, expected: 0, note: 'timing 2 addresses' }).label, 'On, timing 2 addresses')
})

test('namespaces to leave out are validated with the install wizard rules', () => {
  assert.deepEqual(parseExclusions('shop, batch  shop\nops'), { names: ['batch', 'ops', 'shop'], problems: [] })
  assert.deepEqual(parseExclusions('').names, [])
  assert.match(parseExclusions('Bad_Name').problems[0], /not a valid namespace name/)
  assert.match(parseExclusions('kube-system').problems[0], /system namespace/)
  assert.equal(parseExclusions('a-'.repeat(40)).problems.length, 1)
  assert.match(parseExclusions(Array.from({ length: 201 }, (_, i) => `ns${i}`).join(',')).problems.join(' '), /At most 200/)
})

test('a change is only offered when something differs', () => {
  const stored = { pausedCollectors: ['flow'], excludedNamespaces: ['shop', 'batch'] }
  assert.deepEqual(consentChange({ tier: 2, paused: ['flow'], excluded: ['batch', 'shop'] }, 2, stored), { tier: false, overrides: false })
  assert.deepEqual(consentChange({ tier: 1, paused: ['flow'], excluded: ['batch', 'shop'] }, 2, stored), { tier: true, overrides: false })
  assert.deepEqual(consentChange({ tier: 2, paused: [], excluded: ['batch', 'shop'] }, 2, stored), { tier: false, overrides: true })
  assert.deepEqual(consentChange({ tier: 2, paused: [], excluded: [] }, 2, undefined), { tier: false, overrides: false })
})

test('the agent confirms a change when its own account matches', () => {
  const consent = { pausedCollectors: ['flow'], excludedNamespaces: ['shop'] }
  assert.equal(inForce(consent, 1, undefined), false)
  assert.equal(inForce(consent, 1, diag({ partial: true })), false)
  assert.equal(inForce(consent, 1, diag({ effectiveTier: 2, pausedCollectors: ['flow'], excludedNamespaces: 1 })), false)
  assert.equal(inForce(consent, 1, diag({ approvedTier: 1, effectiveTier: 1, pausedCollectors: ['flow'], excludedNamespaces: 1 })), true)
  // an approval above the install's ceiling is confirmed at the ceiling
  assert.equal(inForce(undefined, 2, diag({ installedTier: 1, approvedTier: 2, effectiveTier: 1 })), true)
})

test('what the agent can see, in words', () => {
  assert.equal(scopeWords(diag()), 'not reported')
  assert.equal(scopeWords(diag({ scope: { description: 'all', namespaces: 30, inScope: 30 } })), '30 of 30 namespaces in view')
  assert.equal(scopeWords(diag({ scope: { description: 'x', namespaces: 30, inScope: 25 }, ownExcludedNamespaces: 3, excludedNamespaces: 2 })), '25 of 30 namespaces in view (3 left out by the install, 2 more by an administrator)')
  assert.equal(scopeWords(diag({ scope: { description: '', namespaces: 0, inScope: 0 } })), 'no namespaces read at this tier')
  assert.equal(uptimeWords(30), '30 s')
  assert.equal(uptimeWords(3600), '60 min')
  assert.equal(uptimeWords(7200), '2 h')
})

test('the two fields only editors receive are read from the raw agent list', () => {
  const agents = [{ id: 'a', diagnostics: { ...diag(), pausedCollectors: undefined, problems: undefined }, consent: { pausedCollectors: undefined, excludedNamespaces: ['x'] } }, { id: 'b' }]
  const a = extrasOf(agents, 'a')
  assert.deepEqual(a.diagnostics?.problems, [])
  assert.deepEqual(a.consent, { pausedCollectors: [], excludedNamespaces: ['x'] })
  assert.deepEqual(extrasOf(agents, 'b'), { diagnostics: undefined, consent: undefined })
  assert.deepEqual(extrasOf(agents, 'nope'), {})
  assert.deepEqual(extrasOf(undefined, 'a'), {})
})

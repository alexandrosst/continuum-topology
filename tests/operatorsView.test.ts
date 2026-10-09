import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  addressCell, agentVerdict, buildComponents, buildHops, certCell, duration, intentDestination, joinLocal, lastDataText, localVerdict, needAttention, operatorVerdict, operatorServiceName,
  type LocalRow,
} from '../src/lib/operatorsView'
import type { Agent, RegionalOperator, TelemetryIntent } from '../src/lib/types'

const NOW = Date.parse('2026-10-06T12:00:00Z')

test('addressCell: a recorded address, "Needs an address" while an exposed one has none, otherwise its own cluster only', () => {
  assert.deepEqual(addressCell({ addressState: 'set', address: 'otlp.eu.example.com:4317' }), { kind: 'set', address: 'otlp.eu.example.com:4317', text: 'Reachable at otlp.eu.example.com:4317' })
  assert.equal(addressCell({ addressState: 'pending' }).kind, 'pending')
  assert.equal(addressCell({ addressState: 'none' }).text, 'This cluster only')
  // A server that does not send addressState yet: read from the fields it does send.
  assert.equal(addressCell({ reachableFromOtherClusters: true, address: 'a:1' }).kind, 'set')
  assert.equal(addressCell({ exposure: 'loadbalancer' }).kind, 'pending')
  assert.equal(addressCell({ exposure: 'cluster' }).kind, 'cluster')
})

const intent = (over: Partial<TelemetryIntent>): TelemetryIntent => ({
  id: 'i1',
  agentId: 'a1',
  name: 'n',
  status: 'active',
  namespaces: [],
  exclude: [],
  signals: [{ id: 'resourceUsage' }],
  destination: { kind: 'external', endpoint: 'gw.example.com:4317' },
  createdAt: '2026-10-01T00:00:00Z',
  createdBy: 'alice',
  ...over,
}) as TelemetryIntent

const raw = (id: string, installed: string[]) => ({ id, diagnostics: { reportedAt: '2026-10-06T11:59:00Z', installedTelemetry: installed } })
const agent = (id: string, over: Record<string, unknown> = {}) => ({ id, name: id, status: 'approved', clusterId: `c-${id}`, ...over })

test('joinLocal: running telemetry, or asked-for and not yet reported (pending); never an unapproved agent or an idle one', () => {
  const rows = joinLocal({
    agents: [agent('a1'), agent('a2'), agent('a3'), agent('a4', { status: 'pending' })],
    clusters: [{ id: 'c-a1', name: 'Prod' }],
    rawAgents: [raw('a1', ['resourceUsage']), raw('a2', []), raw('a3', []), raw('a4', ['resourceUsage'])],
    intents: [intent({ agentId: 'a3' })],
  })
  assert.deepEqual(rows.map((r) => [r.agent.id, r.pending]), [['a1', false], ['a3', true]])
  assert.equal(rows[0].cluster?.name, 'Prod')
})

test('joinLocal: drifted only when something is running and differs from the newest active intent', () => {
  const rows = (installed: string[], intents: TelemetryIntent[]) => joinLocal({ agents: [agent('a1')], clusters: [], rawAgents: [raw('a1', installed)], intents })
  assert.equal(rows(['resourceUsage'], [intent({})])[0].drifted, false)
  assert.equal(rows(['resourceUsage', 'traces'], [intent({})])[0].drifted, true)
  assert.equal(rows(['resourceUsage'], [])[0].drifted, false)
  // An older intent was replaced, and a revoked one is not asked for any more.
  const newer = intent({ id: 'i2', createdAt: '2026-10-05T00:00:00Z', signals: [{ id: 'traces' }] as never })
  assert.equal(rows(['traces'], [intent({}), newer])[0].drifted, false)
  assert.equal(rows(['traces'], [intent({ status: 'revoked' as never })])[0].drifted, false)
})

test('intentDestination: the operator it names, one line for split routes, otherwise the endpoint', () => {
  assert.equal(intentDestination(undefined), undefined)
  assert.deepEqual(intentDestination(intent({ destination: { kind: 'operator', endpoint: 'x:4317', targetOperatorId: 'op-eu' } as never })), { operatorId: 'op-eu', label: 'op-eu' })
  assert.deepEqual(intentDestination(intent({})), { label: 'gw.example.com:4317' })
  assert.deepEqual(intentDestination(intent({ routes: { logs: { kind: 'external', endpoint: 'l:1' } } as never })), { label: 'One per signal type' })
})

const ago = (ms: number) => new Date(NOW - ms).toISOString()
const MIN = 60_000
const DAY = 86_400_000
const ahead = (ms: number) => new Date(NOW + ms).toISOString()

test('duration and lastDataText: seconds while they matter, then minutes, hours, days; "No data yet" when there is none', () => {
  assert.equal(duration(40_000), '40 s')
  assert.equal(duration(14 * MIN), '14 min')
  assert.equal(duration(128 * MIN), '2 h 8 min')
  assert.equal(duration(3 * 60 * MIN), '3 h')
  assert.equal(duration(50 * 60 * MIN), '2 d 2 h')
  assert.equal(lastDataText(undefined, NOW), 'No data yet')
  assert.equal(lastDataText(ago(12_000), NOW), '12 s ago')
  assert.equal(lastDataText(ago(14 * MIN), NOW), '14 min ago')
})

const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', name: 'atlas', status: 'approved', clusterId: 'c1', version: '0.19.3', connected: true, lastHeartbeat: ago(10_000), ...over }) as Agent

test('agentVerdict: connected is Healthy, a late heartbeat Needs attention, disconnected Not working with the time, a sample agent Unknown', () => {
  assert.equal(agentVerdict(ag(), NOW).state, 'healthy')
  const late = agentVerdict(ag({ lastHeartbeat: ago(3 * MIN) }), NOW)
  assert.equal(late.state, 'attention')
  assert.match(late.reason!, /heartbeat is late: the last one was 3 min ago/)
  const down = agentVerdict(ag({ connected: false, lastHeartbeat: ago(128 * MIN) }), NOW)
  assert.equal(down.state, 'down')
  assert.match(down.reason!, /Has not reported for 2 h 8 min/)
  // The steps carry the exact commands, in the agent's own namespace and release (the defaults when it did not say).
  assert.equal(down.todo?.steps[0].command, 'kubectl get pods --namespace continuum-system')
  assert.match(down.todo!.steps[1].command!, /--selector app\.kubernetes\.io\/instance=continuum-agent/)
  assert.equal(agentVerdict(ag({ namespace: 'ikhnos', releaseName: 'edge' }), NOW).todo, undefined)
  assert.equal(agentVerdict(ag({ connected: false, namespace: 'ikhnos', releaseName: 'edge' }), NOW).todo?.steps[0].command, 'kubectl get pods --namespace ikhnos')
  assert.equal(agentVerdict(ag({ connected: undefined }), NOW).state, 'unknown')
})

const row = (over: Partial<LocalRow> = {}): LocalRow => ({ agent: { id: 'a1', name: 'atlas' }, installed: ['resourceUsage'], pending: false, drifted: false, diagnostics: undefined, ...over })
const withRoutes = (routes: { state: string; lastSentAt?: string }[]) =>
  ({ reportedAt: ago(MIN), installedTelemetry: ['resourceUsage'], exportHealth: { podsReached: 1, podsFailed: 0, routes: routes.map((r) => ({ exporter: 'otlp', signal: 'metrics', sent: 1, failed: 0, ...r })) } }) as never

test('localVerdict: judged by what its agent reports exporting - and Unknown, never Healthy, while the agent is offline', () => {
  const v = (r: LocalRow, a = ag()) => localVerdict(r, a, 'eu-west', NOW)
  assert.equal(v(row({ diagnostics: withRoutes([{ state: 'exporting', lastSentAt: ago(20_000) }]) })).verdict.state, 'healthy')
  assert.equal(v(row({ diagnostics: withRoutes([{ state: 'exporting', lastSentAt: ago(20_000) }]) })).lastData, ago(20_000))
  const quiet = v(row({ diagnostics: withRoutes([{ state: 'silent', lastSentAt: ago(14 * MIN) }]) }))
  assert.equal(quiet.verdict.state, 'attention')
  assert.equal(quiet.verdict.reason, 'No data has been sent for 14 min.')
  assert.equal(quiet.verdict.todo?.action?.kind, 'configure')
  const failing = v(row({ diagnostics: withRoutes([{ state: 'failing' }]) }))
  assert.equal(failing.verdict.state, 'down')
  assert.equal(failing.verdict.reason, 'Sending to eu-west is failing.')
  assert.equal(v(row({ diagnostics: withRoutes([{ state: 'waiting' }]) })).verdict.state, 'unknown')
  assert.equal(v(row({ diagnostics: undefined })).verdict.state, 'unknown')
  assert.equal(v(row({ drifted: true })).verdict.state, 'attention')
  const offline = v(row({ diagnostics: withRoutes([{ state: 'exporting', lastSentAt: ago(20_000) }]) }), ag({ connected: false, lastHeartbeat: ago(128 * MIN) }))
  assert.equal(offline.verdict.state, 'unknown')
  assert.equal(offline.verdict.reason, 'Its agent has not reported for 2 h 8 min, so what this collector is doing is not known.')
})

test('localVerdict: a request nothing has answered is Unknown at first and Needs attention once the command has had time to be run', () => {
  const pending = (createdAt: string) => localVerdict(row({ pending: true, installed: [], intent: intent({ createdAt }) }), ag(), 'eu-west', NOW).verdict
  assert.equal(pending(ago(5 * MIN)).state, 'unknown')
  const late = pending(ago(3 * 60 * MIN))
  assert.equal(late.state, 'attention')
  assert.match(late.reason!, /3 h ago, but nothing is running/)
})

const op = (over: Partial<RegionalOperator> = {}): RegionalOperator => ({
  id: 'op-eu', name: 'eu-west', status: 'active', sourceClusterIds: [], destination: { kind: 'operator', endpoint: 'c:4317', targetOperatorId: 'op-central' }, createdAt: '2026-01-01T00:00:00Z', createdBy: 'me',
  receiverAuth: 'mtls', health: { state: 'online', reporting: true, lastSeenAt: ago(20_000) }, certs: { receiverNotAfter: ahead(25 * DAY), clientNotAfter: ahead(26 * DAY), caNotAfter: ahead(900 * DAY) }, certState: 'ok', addressState: 'set', ...over,
}) as RegionalOperator

test('certCell: renewing automatically with the day the next renewal is due (20 days before the end), a problem only when renewal fails', () => {
  const ok = certCell(op())
  assert.equal(ok.kind, 'auto')
  assert.equal(ok.kind === 'auto' && ok.next, ahead(5 * DAY))
  assert.deepEqual(certCell(op({ certs: { caNotAfter: ahead(900 * DAY) } })), { kind: 'none' })
  assert.deepEqual(certCell(op({ certs: undefined, certState: undefined })), { kind: 'none' })
  assert.equal(certCell(op({ certState: 'renewal-failing', certs: { receiverNotAfter: ahead(13 * DAY) } })).kind, 'failing')
  assert.equal(certCell(op({ certState: 'expired', certs: { receiverNotAfter: ago(DAY) } })).kind, 'expired')
})

test('operatorVerdict: the worst thing wins and is said once, each with what to do', () => {
  const v = (o: RegionalOperator) => operatorVerdict(o, certCell(o), NOW)
  assert.equal(v(op()).state, 'healthy')
  const failing = v(op({ certState: 'renewal-failing', certs: { receiverNotAfter: ahead(13 * DAY) } }))
  assert.equal(failing.state, 'attention')
  assert.match(failing.reason!, /^Certificate renewal is failing\. The current one is valid until /)
  assert.equal(failing.todo?.action?.kind, 'renew')
  assert.match(failing.todo!.steps[1].command!, /^kubectl logs deployment\/op-eu-regional-operator --namespace continuum-system/)
  const expired = v(op({ certState: 'expired', certs: { receiverNotAfter: ago(DAY) } }))
  assert.equal(expired.state, 'down')
  // Offline beats a failing renewal; a revoked one is Not working and says so.
  assert.equal(v(op({ health: { state: 'offline', reporting: true, lastSeenAt: ago(20 * MIN) }, certState: 'renewal-failing' })).reason, 'Offline, last heard from 20 min ago. Data sent to it is not arriving.')
  assert.equal(v(op({ status: 'revoked', reason: 'rebuilt' })).reason, 'Disconnected: rebuilt. It no longer counts as part of the path.')
  assert.equal(v(op({ addressState: 'pending' })).todo?.action?.kind, 'address')
  // No health reporting is not a fault: Unknown, with the reason.
  assert.equal(v(op({ health: { state: 'unknown', reporting: false } })).state, 'unknown')
  assert.equal(v(op({ health: { state: 'waiting', reporting: true } })).reason, 'Waiting for its first health report.')
})

test('operatorVerdict: the central operator is as healthy as FUSION; its renewals are the server\'s own, so there is no renew action', () => {
  const central = (o: Partial<RegionalOperator>) => op({ id: 'op-central', name: 'central', ...o })
  const v = (o: RegionalOperator) => operatorVerdict(o, certCell(o), NOW)
  assert.equal(v(central({ health: { state: 'online', reporting: false } })).state, 'healthy')
  assert.equal(v(central({ health: { state: 'attention', reporting: false } })).todo?.action?.kind, 'fusion')
  assert.equal(v(central({ health: { state: 'starting', reporting: false } })).state, 'unknown')
  assert.equal(v(central({ health: { state: 'off', reporting: false } })).state, 'unknown')
  assert.equal(v(central({ certState: 'renewal-failing', certs: { receiverNotAfter: ahead(13 * DAY) } })).todo?.action, undefined)
})

test('operatorServiceName: the operator id plus -regional-operator, cut at 63', () => {
  assert.equal(operatorServiceName('op-eu'), 'op-eu-regional-operator')
  assert.equal(operatorServiceName('op-eu-regional-operator'), 'op-eu-regional-operator')
  assert.equal(operatorServiceName('x'.repeat(80)).length, 63)
})

test('buildComponents: one list of agents, local operators, regional and central, worst first; what needs attention leaves out ended ones and unknowns', () => {
  const agents = [ag({ id: 'a1', name: 'atlas', clusterId: 'c1' }), ag({ id: 'a2', name: 'kestrel', clusterId: 'c2', connected: false, lastHeartbeat: ago(2 * 60 * MIN) })]
  const rows = buildComponents({
    agents,
    clusters: [{ id: 'c1', name: 'atlas', tier: 'edge' }, { id: 'c2', name: 'kestrel', tier: 'edge' }] as never,
    rawAgents: [{ id: 'a1', diagnostics: withRoutes([{ state: 'exporting', lastSentAt: ago(5_000) }]) }, { id: 'a2', diagnostics: withRoutes([{ state: 'exporting', lastSentAt: ago(2 * 60 * MIN) }]) }],
    intents: [intent({ agentId: 'a1', destination: { kind: 'operator', endpoint: 'x', targetOperatorId: 'op-eu' } as never }), intent({ id: 'i2', agentId: 'a2', destination: { kind: 'operator', endpoint: 'x', targetOperatorId: 'op-eu' } as never })],
    operators: [op(), op({ id: 'op-old', name: 'old', status: 'revoked' })],
    known: [{ id: 'op-eu', name: 'eu-west' }],
    now: NOW,
  })
  assert.deepEqual(rows.map((r) => [r.kind, r.name, r.verdict.state]), [
    ['agent', 'kestrel', 'down'],
    ['regional', 'old', 'down'],
    ['local', 'kestrel collector', 'unknown'],
    ['agent', 'atlas', 'healthy'],
    ['local', 'atlas collector', 'healthy'],
    ['regional', 'eu-west', 'healthy'],
  ])
  assert.deepEqual(needAttention(rows).map((r) => r.name), ['kestrel'])
  // A regional operator's last data is the newest a local operator reports sending to it; a local operator sending to an operator has a certificate that renews with its agent.
  assert.equal(rows.find((r) => r.name === 'eu-west')?.lastData, ago(5_000))
  assert.equal(rows.find((r) => r.name === 'atlas collector')?.sendsTo, 'eu-west')
})

test('buildHops: counts, worst state and newest data per hop, ended components left out; FUSION from its own state', () => {
  const rows = buildComponents({ agents: [ag(), ag({ id: 'a2', name: 'b', connected: false })], clusters: [], rawAgents: [], intents: [], operators: [op({ status: 'revoked' })], known: [], now: NOW })
  const hops = buildHops(rows, { kind: 'running', lastDataAt: ago(3_000) })
  assert.deepEqual(hops.map((h) => [h.key, h.total, h.healthy, h.state]), [['agent', 2, 1, 'down'], ['local', 0, 0, undefined], ['regional', 0, 0, undefined], ['central', 0, 0, undefined], ['fusion', 1, 1, 'healthy']])
  assert.equal(hops[0].lastData, ago(10_000))
  assert.deepEqual(buildHops([], { kind: 'off' }).at(-1), { key: 'fusion', label: 'FUSION', total: 1, healthy: 0, state: 'unknown', lastData: undefined })
  assert.equal(buildHops([], undefined).at(-1)?.total, 0)
})
